package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Resolve("../nope"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := w.Resolve("link/secret"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
	p, err := w.ResolveForCreate("a/b/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "a", "b", "new.txt")
	if p != want {
		t.Fatalf("got %q want %q", p, want)
	}
}

func TestResolveForMutationRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ResolveForMutation("link"); err == nil {
		t.Fatal("expected final symlink mutation rejection")
	}
}

func TestMultipleRootsRequireExplicitProject(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewRoots([]Root{{Name: "one", Path: a}, {Name: "two", Path: b}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Resolve("a.txt"); err == nil {
		t.Fatal("expected ambiguous relative path rejection")
	}
	got, err := w.Resolve("/one/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(a, "a.txt") {
		t.Fatalf("got %q", got)
	}
	got, err = w.Resolve("two/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(b, "b.txt") {
		t.Fatalf("got %q", got)
	}
	if d := w.Display(got); d != "/two/b.txt" {
		t.Fatalf("display=%q", d)
	}
	if _, err := w.Resolve("/etc/passwd"); err == nil {
		t.Fatal("expected outside-root rejection")
	}
}
