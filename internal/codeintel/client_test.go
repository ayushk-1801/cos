package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestFindExecutableSearchesCommonUserBins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	p := filepath.Join(home, "go", "bin", "gopls")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findExecutable("gopls")
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %q, want %q", got, p)
	}
}

func TestNearestLanguageRootInsideBroadWorkspace(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "nested", "repo")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "pkg", "x.go")
	if err := os.WriteFile(file, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nearestLanguageRoot(file, root, "go"); got != repo {
		t.Fatalf("got %q, want %q", got, repo)
	}
}

func TestVirtualizeRedactsExternalFileURI(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{WS: ws}
	inside := filepath.Join(root, "main.go")
	got := c.virtualize(map[string]any{
		"inside":  fileURI(inside),
		"outside": fileURI("/usr/local/go/src/fmt/print.go"),
	}).(map[string]any)
	if got["inside"] != "/proj/main.go" {
		t.Fatalf("inside=%v", got["inside"])
	}
	if got["outside"] != "external://print.go" {
		t.Fatalf("outside=%v", got["outside"])
	}
}

func TestFilterWorkspaceSymbolsDropsExternalLocations(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{WS: ws}
	v := []any{
		map[string]any{"name": "local", "location": map[string]any{"uri": fileURI(filepath.Join(root, "main.go"))}},
		map[string]any{"name": "external", "location": map[string]any{"uri": fileURI("/usr/local/go/src/fmt/print.go")}},
	}
	got := c.filterWorkspaceSymbols(v).([]any)
	if len(got) != 1 || got[0].(map[string]any)["name"] != "local" {
		t.Fatalf("filtered=%#v", got)
	}
}

func TestDefinitionWithFakeLSP(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{WS: ws, Overrides: map[string][]string{"go": {os.Args[0], "-test.run=TestLSPHelperProcess", "--", "--lsp-helper"}}}
	res, err := c.Run(context.Background(), Request{Action: "definition", Path: "/proj/main.go", Line: 2, Column: 6})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.Result.(map[string]any)
	if !ok {
		t.Fatalf("result %#v", res.Result)
	}
	if got, _ := m["uri"].(string); got != "/proj/main.go" {
		t.Fatalf("virtual uri=%q", got)
	}
}

func TestLSPHelperProcess(t *testing.T) {
	marked := false
	for _, a := range os.Args {
		if a == "--lsp-helper" {
			marked = true
		}
	}
	if !marked {
		return
	}
	r := bufio.NewReader(os.Stdin)
	for {
		b, err := readFrame(r)
		if err != nil {
			os.Exit(0)
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		if dec.Decode(&m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		id, hasID := m["id"]
		if !hasID {
			if method == "exit" {
				os.Exit(0)
			}
			continue
		}
		var result any = map[string]any{}
		switch method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"definitionProvider": true}}
		case "textDocument/definition":
			cwd, _ := os.Getwd()
			result = map[string]any{"uri": fileURI(filepath.Join(cwd, "main.go")), "range": map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 4}}}
		case "shutdown":
			result = nil
		}
		writeFrame(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
}
func readFrame(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if n < 0 {
		return nil, fmt.Errorf("no length")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func writeFrame(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(b))
	_, _ = w.Write(b)
}
