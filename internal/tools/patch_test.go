package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestApplyPatch(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	if err := os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.New(root)
	tool := &Patcher{WS: ws}
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n one\n-two\n+TWO\n three\n*** Add File: b.txt\n+hello\n*** End Patch"
	res, err := tool.Handle(context.Background(), map[string]any{"patch": patch})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "one\nTWO\nthree\n" {
		t.Fatalf("%q", string(b))
	}
	b, _ = os.ReadFile(filepath.Join(root, "b.txt"))
	if string(b) != "hello\n" {
		t.Fatalf("%q", string(b))
	}
}

func TestPatchRejectsMissingContext(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a"), []byte("x\n"), 0o644)
	ws, _ := workspace.New(root)
	tool := &Patcher{WS: ws}
	res, _ := tool.Handle(context.Background(), map[string]any{"patch": "*** Begin Patch\n*** Update File: a\n@@\n-y\n+z\n*** End Patch"})
	if !res.IsError || !strings.Contains(res.Content[0]["text"].(string), "context not found") {
		t.Fatalf("%+v", res)
	}
}
