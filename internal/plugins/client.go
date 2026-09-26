package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Config struct {
	Name           string
	Command        []string
	Env            map[string]string
	EnvVars        []string
	CWD            string
	StartupTimeout time.Duration
	ToolTimeout    time.Duration
	Prefix         string
}

type Client struct {
	cfg      Config
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	next     atomic.Int64
	mu       sync.Mutex
	pending  map[int64]chan reply
	closed   chan struct{}
	protocol string
}

type reply struct {
	Result   json.RawMessage
	RPCError *rpcError
	Err      error
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
type wire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Name == "" || len(cfg.Command) == 0 {
		return nil, fmt.Errorf("plugin name and command are required")
	}
	binary, err := findPluginExecutable(cfg.Command[0])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.Command[0], err)
	}
	cmd := exec.CommandContext(ctx, binary, cfg.Command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
	envMap := map[string]string{}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			envMap[key] = value
		}
	}
	for _, name := range cfg.EnvVars {
		if value, ok := os.LookupEnv(name); ok {
			envMap[name] = value
		}
	}
	for key, value := range cfg.Env {
		envMap[key] = value
	}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, cmd: cmd, stdin: stdin, pending: make(map[int64]chan reply), closed: make(chan struct{}), protocol: "2026-07-28"}
	go c.readLoop(stdout)
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			log.Printf("plugin %s: %s", cfg.Name, s.Text())
		}
	}()
	go func() { err := cmd.Wait(); c.failAll(fmt.Errorf("plugin %s exited: %v", cfg.Name, err)) }()
	return c, nil
}

func findPluginExecutable(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", exec.ErrNotFound
	}
	if filepath.IsAbs(name) {
		st, err := os.Stat(name)
		if err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exec.ErrNotFound
	}
	for _, dir := range []string{
		filepath.Join(home, ".vite-plus", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".cargo", "bin"),
	} {
		candidate := filepath.Join(dir, name)
		if st, statErr := os.Stat(candidate); statErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func (c *Client) Close() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	}
	c.failAll(fmt.Errorf("plugin closed"))
}

func (c *Client) Closed() bool {
	if c == nil {
		return true
	}
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}
func (c *Client) Prefix() string {
	if c.cfg.Prefix != "" {
		return c.cfg.Prefix
	}
	return c.cfg.Name + "."
}
func (c *Client) Name() string { return c.cfg.Name }

func (c *Client) readLoop(r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for s.Scan() {
		line := append([]byte(nil), s.Bytes()...)
		var w wire
		if json.Unmarshal(line, &w) != nil || len(w.ID) == 0 {
			continue
		}
		var id int64
		if err := json.Unmarshal(w.ID, &id); err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- reply{Result: w.Result, RPCError: w.Error}
		}
	}
	if err := s.Err(); err != nil {
		c.failAll(err)
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	for id, ch := range c.pending {
		ch <- reply{Err: err}
		delete(c.pending, id)
	}
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, *rpcError, error) {
	id := c.next.Add(1)
	ch := make(chan reply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	if _, err := c.stdin.Write(b); err != nil {
		c.remove(id)
		return nil, nil, err
	}
	select {
	case r := <-ch:
		return r.Result, r.RPCError, r.Err
	case <-ctx.Done():
		c.remove(id)
		return nil, nil, ctx.Err()
	case <-time.After(c.requestTimeout(method)):
		c.remove(id)
		return nil, nil, fmt.Errorf("plugin %s %s timed out", c.cfg.Name, method)
	}
}

func (c *Client) requestTimeout(method string) time.Duration {
	if method == "server/discover" || method == "initialize" || method == "tools/list" {
		if c.cfg.StartupTimeout > 0 {
			return c.cfg.StartupTimeout
		}
		return 10 * time.Second
	}
	if c.cfg.ToolTimeout > 0 {
		return c.cfg.ToolTimeout
	}
	return 60 * time.Second
}
func (c *Client) remove(id int64) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
func (c *Client) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	_, err := c.stdin.Write(b)
	return err
}

func currentMeta() map[string]any {
	return map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]any{"name": "cos-lite", "version": "0.6.0"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}
}

func (c *Client) Discover(ctx context.Context) ([]map[string]any, error) {
	discoverRaw, rpcErr, err := c.request(ctx, "server/discover", currentMeta())
	if err != nil {
		return nil, err
	}
	modern := false
	if rpcErr == nil {
		var discovered struct {
			SupportedVersions []string `json:"supportedVersions"`
		}
		if json.Unmarshal(discoverRaw, &discovered) == nil {
			for _, version := range discovered.SupportedVersions {
				if version == "2026-07-28" {
					modern = true
					break
				}
			}
		}
	}
	if !modern {
		// Fall back to the newest legacy lifecycle. Servers may answer discovery yet
		// deliberately advertise only legacy revisions, so a successful discovery call
		// is not by itself proof that 2026-07-28 is usable.
		c.protocol = "2025-11-25"
		params := map[string]any{"protocolVersion": c.protocol, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cos-lite", "version": "0.6.0"}}
		raw, re, e := c.request(ctx, "initialize", params)
		if e != nil {
			return nil, e
		}
		if re != nil {
			return nil, fmt.Errorf("plugin initialize: %s", re.Message)
		}
		var initialized struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(raw, &initialized) == nil && initialized.ProtocolVersion != "" {
			c.protocol = initialized.ProtocolVersion
		}
		_ = c.notify("notifications/initialized", map[string]any{})
	} else {
		c.protocol = "2026-07-28"
	}
	params := map[string]any{}
	if c.protocol == "2026-07-28" {
		params = currentMeta()
	}
	raw, re, err := c.request(ctx, "tools/list", params)
	if err != nil {
		return nil, err
	}
	if re != nil {
		return nil, fmt.Errorf("plugin tools/list: %s", re.Message)
	}
	var result struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (c *Client) Call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	params := map[string]any{"name": name, "arguments": args}
	if c.protocol == "2026-07-28" {
		for k, v := range currentMeta() {
			params[k] = v
		}
	}
	raw, re, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	if re != nil {
		return nil, fmt.Errorf("plugin tools/call: %s", re.Message)
	}
	var result map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
