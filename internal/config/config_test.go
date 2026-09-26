package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectsRoundTripAndStalePathRemainsManageable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	cfg := Default()
	if err := cfg.AddProject(root, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("stale project must remain loadable: %v", err)
	}
	if len(loaded.Projects) != 1 || loaded.Projects[0].Name != "alpha" {
		t.Fatalf("unexpected config: %+v", loaded)
	}
	if !loaded.RemoveProject("alpha") {
		t.Fatal("expected remove")
	}
	if err := Save(loaded); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestedNameAndDuplicate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "hello world")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	if err := cfg.AddProject(p, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[0].Name != "hello-world" {
		t.Fatalf("got %q", cfg.Projects[0].Name)
	}
	if err := cfg.AddProject(p, "hello-world"); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestDefaultsEnableAutostartSkillsAndCodeIntel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Autostart {
		t.Fatal("autostart should default on")
	}
	if !cfg.Skills.Enabled || !cfg.Skills.Global || !cfg.Skills.Repo {
		t.Fatalf("skills defaults: %+v", cfg.Skills)
	}
	if !cfg.CodeIntel.Enabled {
		t.Fatal("code intel should default on")
	}
}

func TestLegacyConfigKeepsNewDefaultPolicies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "cos-lite", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"listen":"127.0.0.1:9999","projects":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Autostart || !cfg.Skills.Enabled || !cfg.CodeIntel.Enabled {
		t.Fatalf("new defaults lost during legacy load: %+v", cfg)
	}
}
