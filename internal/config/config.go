package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const currentConfigVersion = 2

type Project struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type Tunnel struct {
	Provider string `json:"provider"` // none, openai, cloudflare, custom
	Command  string `json:"command,omitempty"`
	TunnelID string `json:"tunnel_id,omitempty"`
}

type Skills struct {
	Enabled bool `json:"enabled"`
	Global  bool `json:"global"`
	Repo    bool `json:"repo"`
}

type CodeIntel struct {
	Enabled   bool                `json:"enabled"`
	Overrides map[string][]string `json:"overrides,omitempty"`
}

type Config struct {
	Version     int       `json:"version"`
	Listen      string    `json:"listen"`
	Browser     bool      `json:"browser"`
	Headless    bool      `json:"headless"`
	Autostart   bool      `json:"autostart"`
	PluginsPath string    `json:"plugins_path"`
	Projects    []Project `json:"projects"`
	Tunnel      Tunnel    `json:"tunnel"`
	Skills      Skills    `json:"skills"`
	CodeIntel   CodeIntel `json:"code_intel"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func Default() Config {
	return Config{
		Version:     currentConfigVersion,
		Listen:      "127.0.0.1:8766",
		Browser:     true,
		Headless:    true,
		Autostart:   true,
		PluginsPath: "none",
		Tunnel:      Tunnel{Provider: "none"},
		Skills:      Skills{Enabled: true, Global: true, Repo: true},
		CodeIntel:   CodeIntel{Enabled: true, Overrides: map[string][]string{}},
	}
}

func Dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "cos-lite"), nil
}

func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var meta struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", p, err)
	}
	cfg := Default()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", p, err)
	}
	// json.Unmarshal onto defaults intentionally preserves new default policies
	// for fields older configs never had. Version is different: we need to know
	// whether it existed on disk so migrations are not skipped by the default.
	if meta.Version == nil {
		cfg.Version = 0
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(cfg Config) error {
	if err := cfg.Normalize(); err != nil {
		return err
	}
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p := filepath.Join(d, "config.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (c *Config) Normalize() error {
	// v0.3.0/0.3.1 used 8765, which is also Chat On Steroids' default local
	// listener. Existing default configs therefore could not run alongside CoS.
	// Config version 2 reserves 8766 for cos-lite. From v2 onward an explicit
	// 8765 remains a user choice.
	if c.Version < 2 {
		if strings.TrimSpace(c.Listen) == "127.0.0.1:8765" {
			c.Listen = "127.0.0.1:8766"
		}
		c.Version = currentConfigVersion
	}
	if c.Version > currentConfigVersion {
		return fmt.Errorf("config version %d is newer than this cos-lite supports (%d)", c.Version, currentConfigVersion)
	}
	if c.CodeIntel.Overrides == nil {
		c.CodeIntel.Overrides = map[string][]string{}
	}
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = "127.0.0.1:8766"
	}
	if strings.TrimSpace(c.PluginsPath) == "" {
		c.PluginsPath = "none"
	}
	if strings.TrimSpace(c.Tunnel.Provider) == "" {
		c.Tunnel.Provider = "none"
	}
	switch c.Tunnel.Provider {
	case "none", "cloudflare", "custom":
	case "openai":
		if !regexp.MustCompile(`^tunnel_[0-9a-f]{32}$`).MatchString(strings.TrimSpace(c.Tunnel.TunnelID)) {
			return fmt.Errorf("invalid OpenAI tunnel id %q (expected tunnel_<32 lowercase hex characters>)", c.Tunnel.TunnelID)
		}
		c.Tunnel.TunnelID = strings.TrimSpace(c.Tunnel.TunnelID)
	default:
		return fmt.Errorf("unsupported tunnel provider %q", c.Tunnel.Provider)
	}
	seen := map[string]bool{}
	for i := range c.Projects {
		p := &c.Projects[i]
		p.Name = strings.TrimSpace(p.Name)
		if !nameRE.MatchString(p.Name) {
			return fmt.Errorf("invalid project name %q", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate project name %q", p.Name)
		}
		seen[p.Name] = true
		abs, err := filepath.Abs(strings.TrimSpace(p.Path))
		if err != nil {
			return err
		}
		abs = filepath.Clean(abs)
		if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			p.Path = filepath.Clean(real)
		} else if os.IsNotExist(evalErr) {
			// Keep missing paths loadable so the TUI can show/remove stale projects.
			p.Path = abs
		} else {
			return fmt.Errorf("project %s: %w", p.Name, evalErr)
		}
	}
	sort.SliceStable(c.Projects, func(i, j int) bool { return strings.ToLower(c.Projects[i].Name) < strings.ToLower(c.Projects[j].Name) })
	return nil
}

func SuggestedName(path string) string {
	base := filepath.Base(filepath.Clean(path))
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-._")
	if s == "" {
		s = "project"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func (c *Config) AddProject(path, name string) error {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	st, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("project path is not a directory: %s", real)
	}
	path = real
	if name == "" {
		name = SuggestedName(path)
	}
	for _, p := range c.Projects {
		if p.Name == name {
			return fmt.Errorf("project %q already exists", name)
		}
	}
	c.Projects = append(c.Projects, Project{Name: name, Path: path, Enabled: true})
	return c.Normalize()
}

func (c *Config) RemoveProject(name string) bool {
	for i, p := range c.Projects {
		if p.Name == name {
			c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Config) EnabledProjects() []Project {
	out := make([]Project, 0, len(c.Projects))
	for _, p := range c.Projects {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}
