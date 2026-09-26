package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesUserUnit(t *testing.T) {
	cfg := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	fake := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	binary := filepath.Join(t.TempDir(), "cos binary")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(binary); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfg, "systemd", "user", unitName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `ExecStart="`+binary+`" daemon`) {
		t.Fatalf("unit missing quoted ExecStart:\n%s", text)
	}
	if !strings.Contains(text, "WantedBy=default.target") {
		t.Fatalf("bad unit:\n%s", text)
	}
}
