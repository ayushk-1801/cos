package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayush/cos-lite/internal/config"
)

func TestCustomTunnelCapturesPublicURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var m Manager
	cfg := config.Tunnel{Provider: "custom", Command: `echo 'ready https://unit-test.example.test'; sleep 2`}
	if err := m.Start(ctx, cfg, "http://127.0.0.1:8765/mcp/secret"); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		st := m.Status()
		if st.PublicURL != "" {
			if st.PublicURL != "https://unit-test.example.test/mcp/secret" {
				t.Fatalf("url=%q", st.PublicURL)
			}
			if !st.Running {
				t.Fatal("expected running")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("public URL not captured: %+v", m.Status())
}

func TestOpenAITunnelUsesSecretFileAndBecomesReady(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	secret := "sk-runtime-super-secret"
	if err := WriteOpenAIKey(secret); err != nil {
		t.Fatal(err)
	}
	keyPath, err := OpenAIKeyPath()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o, want 600", st.Mode().Perm())
	}

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		http.NotFound(w, r)
	}))
	defer health.Close()

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := filepath.Join(binDir, "tunnel-client")
	content := `#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
health_file=""
for arg in "$@"; do
  case "$arg" in
    --health.url-file=*) health_file="${arg#*=}" ;;
  esac
done
printf '%s\n' "$HEALTH_URL" > "$health_file"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("HEALTH_URL", health.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var m Manager
	cfg := config.Tunnel{Provider: "openai", TunnelID: "tunnel_0123456789abcdef0123456789abcdef"}
	local := "http://127.0.0.1:8766/mcp/local-secret-token"
	if err := m.Start(ctx, cfg, local); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stt := m.Status()
	if !stt.Running || !stt.Ready {
		t.Fatalf("OpenAI tunnel not ready: %+v", stt)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	for _, want := range []string{
		"run",
		"--control-plane.tunnel-id=" + cfg.TunnelID,
		"--control-plane.api-key=file:" + keyPath,
		"--mcp.server-url=" + local,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("argv missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, secret) {
		t.Fatalf("runtime key leaked into argv: %s", args)
	}
	if stt.PublicURL != "" {
		t.Fatalf("OpenAI tunnel must not invent a public URL: %+v", stt)
	}
}
