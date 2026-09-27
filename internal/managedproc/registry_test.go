package managedproc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRegisterListAndStaleDetection(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("COS_LITE_TEST_MANAGED_REGISTRY", "1")
	pid := os.Getpid()
	owner := 1 << 29
	if err := Register(pid, owner, "lsp", "test"); err != nil {
		t.Fatal(err)
	}
	entries, err := List()
	if err != nil || len(entries) != 1 || entries[0].PID != pid || entries[0].Kind != "lsp" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	stale, err := StaleEntries(os.Getpid())
	if err != nil || len(stale) != 1 || stale[0].PID != pid {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	Unregister(pid)
	entries, err = List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after unregister=%+v err=%v", entries, err)
	}
}

func TestCleanupStaleKillsManagedProcessGroup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("COS_LITE_TEST_MANAGED_REGISTRY", "1")
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if err := Register(cmd.Process.Pid, 1<<29, "mcp", "stale-test"); err != nil {
		t.Fatal(err)
	}
	report, err := CleanupStale(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Killed) != 1 || report.Killed[0].PID != cmd.Process.Pid {
		t.Fatalf("report=%+v", report)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		done <- err
	}()
	select {
	case <-done:
		return
	case <-time.After(time.Second):
		t.Fatal("managed stale process survived cleanup")
	}
}
