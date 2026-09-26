package tunnel

import (
	"context"
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
