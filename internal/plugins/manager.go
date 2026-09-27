package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ayush/cos-lite/internal/control"
	"github.com/ayush/cos-lite/internal/tools"
)

type Manager struct {
	mu       sync.Mutex
	servers  []*managedServer
	stop     chan struct{}
	once     sync.Once
	onChange func()
}

const codexMCPIdleTTL = 10 * time.Minute

type managedServer struct {
	mu          sync.Mutex
	ctx         context.Context
	cfg         Config
	client      *Client
	lastUsed    time.Time
	lastStarted time.Time
	lastError   string
	calls       uint64
	failures    uint64
	toolCount   int
	manager     *Manager
}

type ServerHealth struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	PID         int    `json:"pid,omitempty"`
	ToolCount   int    `json:"tool_count"`
	Calls       uint64 `json:"calls"`
	Failures    uint64 `json:"failures"`
	LastStarted string `json:"last_started,omitempty"`
	LastUsed    string `json:"last_used,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Command     string `json:"command,omitempty"`
}

type HealthFile struct {
	UpdatedAt string         `json:"updated_at"`
	Servers   []ServerHealth `json:"servers"`
}

func NewManager(onChange func()) *Manager { return &Manager{onChange: onChange} }

func (m *Manager) signal() {
	if m != nil && m.onChange != nil {
		m.onChange()
	}
}

func HealthPath() (string, error) {
	d, err := control.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "mcp-health.json"), nil
}

func WriteHealth(servers []ServerHealth) error {
	if err := control.EnsureDir(); err != nil {
		return err
	}
	p, err := HealthPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(HealthFile{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Servers: servers}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func ReadHealth() (HealthFile, error) {
	p, err := HealthPath()
	if err != nil {
		return HealthFile{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return HealthFile{}, err
	}
	var h HealthFile
	err = json.Unmarshal(b, &h)
	return h, err
}

func (m *Manager) Health() []ServerHealth {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	servers := append([]*managedServer(nil), m.servers...)
	m.mu.Unlock()
	out := make([]ServerHealth, 0, len(servers))
	for _, server := range servers {
		if server == nil {
			continue
		}
		server.mu.Lock()
		state := "idle"
		pid := 0
		if server.client != nil && !server.client.Closed() {
			state = "running"
			pid = server.client.PID()
		} else if server.lastError != "" {
			state = "error"
		}
		h := ServerHealth{
			Name: server.cfg.Name, State: state, PID: pid, ToolCount: server.toolCount,
			Calls: server.calls, Failures: server.failures, LastError: server.lastError,
			Command: strings.Join(server.cfg.Command, " "),
		}
		if !server.lastStarted.IsZero() {
			h.LastStarted = server.lastStarted.UTC().Format(time.RFC3339Nano)
		}
		if !server.lastUsed.IsZero() {
			h.LastUsed = server.lastUsed.UTC().Format(time.RFC3339Nano)
		}
		server.mu.Unlock()
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CodexConfigPath returns Codex's standard config.toml. CODEX_HOME follows the
// same convention as Codex; otherwise ~/.codex is used.
func CodexConfigPath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		if !filepath.IsAbs(home) {
			abs, err := filepath.Abs(home)
			if err != nil {
				return "", err
			}
			home = abs
		}
		return filepath.Join(home, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// LoadCodexConfig reads only Codex's standard [mcp_servers.*] tables. cos-lite
// intentionally does not maintain a second MCP-server configuration format.
func LoadCodexConfig() ([]Config, []string, error) {
	path, err := CodexConfigPath()
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return parseCodexMCPConfig(string(b), path)
}

type codexServer struct {
	name           string
	command        string
	args           []string
	env            map[string]string
	envVars        []string
	cwd            string
	enabled        bool
	enabledSet     bool
	url            string
	startupTimeout time.Duration
	toolTimeout    time.Duration
}

func parseCodexMCPConfig(text, source string) ([]Config, []string, error) {
	servers := map[string]*codexServer{}
	sectionName := ""
	sectionEnv := false
	lines := logicalTOMLLines(text)
	for lineNo, raw := range lines {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sectionName, sectionEnv = parseMCPSection(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if sectionName == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d: expected key = value", source, lineNo+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		server := servers[sectionName]
		if server == nil {
			server = &codexServer{name: sectionName, env: map[string]string{}}
			servers[sectionName] = server
		}
		if sectionEnv {
			v, err := parseTOMLString(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d env %s: %w", source, lineNo+1, key, err)
			}
			server.env[unquoteTOMLKey(key)] = v
			continue
		}
		switch key {
		case "command":
			v, err := parseTOMLString(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d command: %w", source, lineNo+1, err)
			}
			server.command = v
		case "args":
			v, err := parseTOMLStringArray(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d args: %w", source, lineNo+1, err)
			}
			server.args = v
		case "env_vars":
			v, err := parseTOMLStringArray(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d env_vars: %w", source, lineNo+1, err)
			}
			server.envVars = v
		case "cwd":
			v, err := parseTOMLString(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d cwd: %w", source, lineNo+1, err)
			}
			server.cwd = v
		case "enabled":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d enabled: %w", source, lineNo+1, err)
			}
			server.enabled, server.enabledSet = v, true
		case "url":
			v, err := parseTOMLString(value)
			if err != nil {
				return nil, nil, fmt.Errorf("parse Codex MCP config %s line %d url: %w", source, lineNo+1, err)
			}
			server.url = v
		case "startup_timeout_sec":
			server.startupTimeout = parseSeconds(value)
		case "tool_timeout_sec":
			server.toolTimeout = parseSeconds(value)
		}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Config
	var warnings []string
	for _, name := range names {
		s := servers[name]
		if s.enabledSet && !s.enabled {
			continue
		}
		if s.url != "" && s.command == "" {
			warnings = append(warnings, fmt.Sprintf("Codex MCP server %s uses remote URL transport; cos-lite currently proxies stdio MCP servers only", name))
			continue
		}
		if strings.TrimSpace(s.command) == "" {
			warnings = append(warnings, fmt.Sprintf("Codex MCP server %s has no command; skipped", name))
			continue
		}
		cmd := append([]string{s.command}, s.args...)
		out = append(out, Config{Name: name, Command: cmd, Env: s.env, EnvVars: s.envVars, CWD: s.cwd, StartupTimeout: s.startupTimeout, ToolTimeout: s.toolTimeout})
	}
	return out, warnings, nil
}

func parseMCPSection(section string) (name string, env bool) {
	const prefix = "mcp_servers."
	if !strings.HasPrefix(section, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(section, prefix))
	parts := splitDottedTOMLKey(rest)
	if len(parts) == 0 {
		return "", false
	}
	name = unquoteTOMLKey(parts[0])
	if name == "" {
		return "", false
	}
	return name, len(parts) == 2 && unquoteTOMLKey(parts[1]) == "env"
}

func splitDottedTOMLKey(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	for _, r := range s {
		if quote != 0 {
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			b.WriteRune(r)
			continue
		}
		if r == '.' {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	out = append(out, strings.TrimSpace(b.String()))
	return out
}

func unquoteTOMLKey(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		if s[0] == '\'' {
			return s[1 : len(s)-1]
		}
		if v, err := strconv.Unquote(s); err == nil {
			return v
		}
	}
	return s
}

func parseTOMLString(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return "", fmt.Errorf("expected TOML string")
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1], nil
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		return strconv.Unquote(s)
	}
	return "", fmt.Errorf("expected quoted TOML string")
}

func parseTOMLStringArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("expected array")
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return []string{}, nil
	}
	var raw []string
	var b strings.Builder
	quote := rune(0)
	escaped := false
	for _, r := range body {
		if quote != 0 {
			b.WriteRune(r)
			if quote == '"' && r == '\\' && !escaped {
				escaped = true
				continue
			}
			if r == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			b.WriteRune(r)
			continue
		}
		if r == ',' {
			if v := strings.TrimSpace(b.String()); v != "" {
				raw = append(raw, v)
			}
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if v := strings.TrimSpace(b.String()); v != "" {
		raw = append(raw, v)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		v, err := parseTOMLString(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func logicalTOMLLines(text string) []string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	var out []string
	var current strings.Builder
	depth := 0
	for scanner.Scan() {
		line := scanner.Text()
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
		depth += bracketDelta(stripTOMLComment(line))
		if depth <= 0 {
			out = append(out, current.String())
			current.Reset()
			depth = 0
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func bracketDelta(s string) int {
	delta := 0
	quote := rune(0)
	escaped := false
	for _, r := range s {
		if quote != 0 {
			if quote == '"' && r == '\\' && !escaped {
				escaped = true
				continue
			}
			if r == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '[' {
			delta++
		} else if r == ']' {
			delta--
		}
	}
	return delta
}

func stripTOMLComment(s string) string {
	quote := rune(0)
	escaped := false
	for i, r := range s {
		if quote != 0 {
			if quote == '"' && r == '\\' && !escaped {
				escaped = true
				continue
			}
			if r == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '#' {
			return s[:i]
		}
	}
	return s
}

func parseSeconds(s string) time.Duration {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return time.Duration(v * float64(time.Second))
}

func (m *Manager) Register(ctx context.Context, reg *tools.Registry, cfgs []Config) []string {
	type discoveredServer struct {
		index  int
		cfg    Config
		client *Client
		defs   []map[string]any
		err    error
	}
	results := make([]discoveredServer, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		i, cfg := i, cfg
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := discoveredServer{index: i, cfg: cfg}
			c, err := Start(ctx, cfg)
			if err != nil {
				result.err = fmt.Errorf("start Codex MCP server %s: %w", cfg.Name, err)
				results[i] = result
				return
			}
			startupTimeout := cfg.StartupTimeout
			if startupTimeout <= 0 {
				startupTimeout = 10 * time.Second
			}
			discoverCtx, cancel := context.WithTimeout(ctx, startupTimeout)
			defs, err := c.Discover(discoverCtx)
			cancel()
			if err != nil {
				c.Close()
				result.err = fmt.Errorf("discover Codex MCP server %s: %w", cfg.Name, err)
				results[i] = result
				return
			}
			result.client = c
			result.defs = defs
			results[i] = result
		}()
	}
	wg.Wait()

	var warnings []string
	for _, discovered := range results {
		if discovered.err != nil {
			warnings = append(warnings, discovered.err.Error())
			server := &managedServer{ctx: ctx, cfg: discovered.cfg, lastError: discovered.err.Error(), manager: m}
			m.mu.Lock()
			m.servers = append(m.servers, server)
			m.mu.Unlock()
			continue
		}
		cfg := discovered.cfg
		c := discovered.client
		if c == nil {
			continue
		}
		server := &managedServer{ctx: ctx, cfg: cfg, lastUsed: time.Now(), toolCount: len(discovered.defs), manager: m}
		// Discovery is needed to expose concrete tools, but keeping every Codex
		// MCP process resident forever would make idle memory scale with config.
		// Close the discovery instance and restart lazily on first actual call.
		c.Close()
		m.mu.Lock()
		m.servers = append(m.servers, server)
		if m.stop == nil {
			m.stop = make(chan struct{})
			go m.reaper(m.stop)
		}
		m.mu.Unlock()

		for _, raw := range discovered.defs {
			orig, _ := raw["name"].(string)
			if orig == "" {
				continue
			}
			pub := c.Prefix() + orig
			desc, _ := raw["description"].(string)
			if desc == "" {
				desc = "Tool from Codex MCP server " + cfg.Name
			}
			schema, _ := raw["inputSchema"].(map[string]any)
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			managed := server
			original := orig
			t := tools.Tool{Definition: tools.Definition{Name: pub, Title: fmt.Sprintf("%s: %s", cfg.Name, orig), Description: desc, InputSchema: schema, Annotations: map[string]any{"openWorldHint": true}}, Handler: func(ctx context.Context, args map[string]any) (tools.Result, error) {
				res, err := managed.Call(ctx, original, args)
				if err != nil {
					return tools.Error(err.Error()), nil
				}
				content := []tools.Content{}
				if arr, ok := res["content"].([]any); ok {
					for _, v := range arr {
						if cm, ok := v.(map[string]any); ok {
							content = append(content, tools.Content(cm))
						}
					}
				}
				if len(content) == 0 {
					b, _ := json.MarshalIndent(res, "", "  ")
					content = []tools.Content{{"type": "text", "text": string(b)}}
				}
				structured := res["structuredContent"]
				isErr, _ := res["isError"].(bool)
				return tools.Result{Content: content, Structured: structured, IsError: isErr}, nil
			}}
			if err := reg.Register(t); err != nil {
				warnings = append(warnings, fmt.Sprintf("Codex MCP server %s tool %s: %v", cfg.Name, orig, err))
			}
		}
	}
	m.signal()
	return warnings
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.once.Do(func() {
		m.mu.Lock()
		if m.stop != nil {
			close(m.stop)
		}
		servers := append([]*managedServer(nil), m.servers...)
		m.servers = nil
		m.mu.Unlock()
		for _, server := range servers {
			server.Close()
		}
		m.signal()
	})
}

func (s *managedServer) Call(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.client.Closed() {
		s.lastStarted = time.Now()
		c, err := Start(s.ctx, s.cfg)
		if err != nil {
			s.lastError = err.Error()
			s.failures++
			if s.manager != nil {
				s.manager.signal()
			}
			return nil, err
		}
		startupTimeout := s.cfg.StartupTimeout
		if startupTimeout <= 0 {
			startupTimeout = 10 * time.Second
		}
		startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		_, err = c.Discover(startupCtx)
		cancel()
		if err != nil {
			c.Close()
			s.lastError = err.Error()
			s.failures++
			if s.manager != nil {
				s.manager.signal()
			}
			return nil, err
		}
		s.client = c
		s.lastError = ""
		if s.manager != nil {
			s.manager.signal()
		}
	}
	s.lastUsed = time.Now()
	s.calls++
	res, err := s.client.Call(ctx, tool, args)
	if err != nil && s.client.Closed() {
		s.client = nil
	}
	if err != nil {
		s.failures++
		s.lastError = err.Error()
	} else {
		s.lastError = ""
	}
	s.lastUsed = time.Now()
	if s.manager != nil {
		s.manager.signal()
	}
	return res, err
}

func (s *managedServer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if s.manager != nil {
		s.manager.signal()
	}
}

func (m *Manager) reaper(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			m.mu.Lock()
			servers := append([]*managedServer(nil), m.servers...)
			m.mu.Unlock()
			for _, server := range servers {
				server.mu.Lock()
				if server.client != nil && now.Sub(server.lastUsed) >= codexMCPIdleTTL {
					c := server.client
					server.client = nil
					server.mu.Unlock()
					c.Close()
					m.signal()
					continue
				}
				server.mu.Unlock()
			}
		}
	}
}
