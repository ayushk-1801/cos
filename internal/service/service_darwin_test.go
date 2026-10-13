//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentLifecycle(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	state := filepath.Join(t.TempDir(), "loaded")
	t.Setenv("HOME", home)
	t.Setenv("COS_LAUNCHD_TEST_STATE", state)
	t.Setenv("COS_DISABLE_SYSTEMD", "")
	t.Setenv("COS_DISABLE_LAUNCHD", "")
	// Fake launchctl, never interact with the real host's GUI session.
	fake := `#!/bin/sh
case "$1" in
 print)
   case "$2" in
     gui/*/*) test -f "$COS_LAUNCHD_TEST_STATE" && echo "state = running" ;;
     gui/*) exit 0 ;;
   esac
   ;;
 bootstrap|kickstart) touch "$COS_LAUNCHD_TEST_STATE" ;;
 bootout) rm -f "$COS_LAUNCHD_TEST_STATE" ;;
 enable) exit 0 ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "launchctl"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	binary := filepath.Join(t.TempDir(), "cos & app")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	if !Available() {
		t.Fatal("expected mock launchctl to be available")
	}
	if err := Install(binary); err != nil {
		t.Fatal(err)
	}
	if !Enabled() || !Active() {
		t.Fatal("LaunchAgent must be installed and active")
	}
	path, err := agentPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cos &amp; app", "RunAtLoad", "/opt/homebrew/bin"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("plist does not contain %q", want)
		}
	}
	if err := Stop(); err != nil {
		t.Fatal(err)
	}
	if Active() {
		t.Fatal("expected stopped service")
	}
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	if !Active() {
		t.Fatal("expected started service")
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if Enabled() || Active() {
		t.Fatal("expected uninstalled service")
	}
}
