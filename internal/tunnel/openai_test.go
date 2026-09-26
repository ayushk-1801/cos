package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindBinarySearchesLocalBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	p := filepath.Join(home, ".local", "bin", "tunnel-client")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindBinary("tunnel-client")
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %q, want %q", got, p)
	}
}

func TestTunnelIDFromArgv(t *testing.T) {
	id := "tunnel_0123456789abcdef0123456789abcdef"
	if got := tunnelIDFromArgv([]string{"tunnel-client", "run", "--control-plane.tunnel-id=" + id}); got != id {
		t.Fatalf("equals form: got %q", got)
	}
	if got := tunnelIDFromArgv([]string{"tunnel-client", "run", "--control-plane.tunnel-id", id}); got != id {
		t.Fatalf("split form: got %q", got)
	}
}
