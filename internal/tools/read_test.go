package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestReadLines(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.New(root)
	r := &Reader{WS: ws}
	res, err := r.Handle(context.Background(), map[string]any{"paths": []any{"a.txt"}, "start_line": float64(2), "end_line": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0]["text"].(string)
	if strings.Contains(text, "one") || !strings.Contains(text, "two") || !strings.Contains(text, "three") {
		t.Fatalf("%s", text)
	}
}
