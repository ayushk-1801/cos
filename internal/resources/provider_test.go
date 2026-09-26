package resources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush/cos-lite/internal/instructions"
	"github.com/ayush/cos-lite/internal/workspace"
)

func TestProjectsAndInstructionsResources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{WS: ws, Instructions: &instructions.Resolver{WS: ws}}
	contents, ttl, scope, err := p.Read(context.Background(), "cos://projects/proj/instructions")
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || scope != "private" || len(contents) != 1 || !strings.Contains(contents[0].Text, "root rule") {
		t.Fatalf("bad instructions resource: %#v ttl=%d scope=%s", contents, ttl, scope)
	}
	contents, _, _, err = p.Read(context.Background(), "cos://projects")
	if err != nil || !strings.Contains(contents[0].Text, "proj") {
		t.Fatalf("projects resource: %#v %v", contents, err)
	}
}
