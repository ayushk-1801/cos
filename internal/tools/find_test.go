package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestFinderFallsBackWithoutRipgrep(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\nfunc HelloWorld() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("HELLO text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &Finder{WS: ws}
	res, err := f.Handle(context.Background(), map[string]any{"query": "hello", "path": ".", "glob": "*.go", "fixed_strings": true})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected result: %#v", res)
	}
	text, _ := res.Content[0]["text"].(string)
	if !strings.Contains(text, "a.go:2:6:func HelloWorld() {}") || strings.Contains(text, "a.txt") {
		t.Fatalf("unexpected fallback output: %q", text)
	}
	structured, _ := res.Structured.(map[string]any)
	if structured["engine"] != "builtin" {
		t.Fatalf("expected builtin engine: %#v", structured)
	}
}

func TestFinderFallbackRegexError(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Finder{WS: ws}).Handle(context.Background(), map[string]any{"query": "[", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected regex error, got %#v", res)
	}
}
