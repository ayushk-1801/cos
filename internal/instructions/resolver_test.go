package instructions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestHierarchy(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "AGENTS.md"), []byte("src rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "x.go")
	if err := os.WriteFile(file, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := (&Resolver{WS: ws}).ForPath("/proj/src/pkg/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Text != "root rules" || docs[1].Text != "src rules" {
		t.Fatalf("docs=%#v", docs)
	}
}

func TestSymlinkAgentsIgnored(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	_ = os.WriteFile(outside, []byte("secret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	docs, err := (&Resolver{WS: ws}).ForProject("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("symlink instructions exposed: %#v", docs)
	}
}
