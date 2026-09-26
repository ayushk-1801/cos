//go:build linux

package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilesSeesAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Files(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no inotify event for atomic replace")
	}
}
