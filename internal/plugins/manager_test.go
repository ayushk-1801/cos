package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseCodexMCPConfig(t *testing.T) {
	text := `
[mcp_servers.playwright]
command = "npx"
args = [
  "-y",
  "@playwright/mcp@latest",
]
startup_timeout_sec = 45
tool_timeout_sec = 12.5

[mcp_servers.playwright.env]
FOO = "bar"

[mcp_servers."quoted-name"]
command = '/usr/bin/example'
args = []
env_vars = ["HOME", "PATH"]
cwd = "/tmp"

[mcp_servers.disabled]
command = "false"
enabled = false

[mcp_servers.remote]
url = "https://example.test/mcp"
`
	cfg, warnings, err := parseCodexMCPConfig(text, "test.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 2 {
		t.Fatalf("configs=%#v warnings=%#v", cfg, warnings)
	}
	if cfg[0].Name != "playwright" || !reflect.DeepEqual(cfg[0].Command, []string{"npx", "-y", "@playwright/mcp@latest"}) {
		t.Fatalf("playwright=%#v", cfg[0])
	}
	if cfg[0].Env["FOO"] != "bar" || cfg[0].StartupTimeout != 45*time.Second || cfg[0].ToolTimeout != 12500*time.Millisecond {
		t.Fatalf("playwright options=%#v", cfg[0])
	}
	if cfg[1].Name != "quoted-name" || cfg[1].CWD != "/tmp" || !reflect.DeepEqual(cfg[1].EnvVars, []string{"HOME", "PATH"}) {
		t.Fatalf("quoted=%#v", cfg[1])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "remote URL transport") {
		t.Fatalf("warnings=%#v", warnings)
	}
}

func TestLoadCodexConfigUsesCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`[mcp_servers.fake]
command = "echo"
args = ["hello"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := LoadCodexConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(cfg) != 1 || !reflect.DeepEqual(cfg[0].Command, []string{"echo", "hello"}) {
		t.Fatalf("cfg=%#v warnings=%#v", cfg, warnings)
	}
}

func TestHealthFileRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	in := []ServerHealth{{Name: "fake", State: "running", PID: 123, ToolCount: 4, Calls: 7, Failures: 1, LastError: "boom"}}
	if err := WriteHealth(in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHealth()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 || got.Servers[0].Name != "fake" || got.Servers[0].Calls != 7 || got.Servers[0].ToolCount != 4 {
		t.Fatalf("health=%+v", got)
	}
}
