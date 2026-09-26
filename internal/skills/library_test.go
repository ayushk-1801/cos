package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ayush/cos-lite/internal/workspace"
)

func TestDiscoverGlobalAndRepoSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	global, _ := GlobalDir()
	if err := os.MkdirAll(filepath.Join(global, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "review", "SKILL.md"), []byte("---\nname: Review\ndescription: Review code carefully.\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "skills", "build", "SKILL.md"), []byte("---\nname: Build\ndescription: Build this repo.\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	lib := &Library{WS: ws, Global: true, Repo: true}
	got, errs := lib.List()
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if _, body, err := lib.Read("proj/build"); err != nil || body == "" {
		t.Fatalf("read: %v %q", err, body)
	}
}

func TestLegacyCosSkillsAreNotDiscovered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	legacy := filepath.Join(root, ".cos", "skills", "legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("---\nname: Legacy\ndescription: Legacy cos-lite path.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "proj", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	list, errs := (&Library{WS: ws, Global: false, Repo: true}).List()
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(list) != 0 {
		t.Fatalf("legacy .cos/skills must not be discovered: %+v", list)
	}
}

func TestDiscoverSkillInImmediateChildRepoOfBroadRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	repo := filepath.Join(root, "cos-test")
	dir := filepath.Join(repo, ".agents", "skills", "smoke")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: Smoke\ndescription: Nested repo smoke skill.\n---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "check.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.NewRoots([]workspace.Root{{Name: "Projects", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	lib := &Library{WS: ws, Global: false, Repo: true}
	list, errs := lib.List()
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(list) != 1 || list[0].ID != "Projects/cos-test/smoke" {
		t.Fatalf("list=%+v", list)
	}
	if list[0].Path != "/Projects/cos-test/.agents/skills/smoke/SKILL.md" {
		t.Fatalf("path=%q", list[0].Path)
	}
	_, body, err := lib.ReadFile("Projects/cos-test/smoke", "references/check.txt")
	if err != nil || body != "ok\n" {
		t.Fatalf("read nested support file: err=%v body=%q", err, body)
	}
}

func TestImportRemoveGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: Debug\ndescription: Debug things.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Import(dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "global/debug" {
		t.Fatalf("id %q", s.ID)
	}
	if err := RemoveGlobal("debug"); err != nil {
		t.Fatal(err)
	}
}
