package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	proc "github.com/ayush/cos-lite/internal/process"
)

func TestProcessTaskLifecycleCapability(t *testing.T) {
	pm := proc.NewManager()
	res, err := pm.Start(proc.StartOptions{Command: "printf early-; sleep 0.15; printf task-ok", Workdir: t.TempDir(), Yield: 50 * time.Millisecond, Env: proc.SanitizedEnv()})
	if err != nil || res.SessionID == nil {
		t.Fatalf("start: res=%+v err=%v", res, err)
	}
	m := NewManager(pm)
	task, err := m.CreateProcess(*res.SessionID)
	if err != nil || task.Status != "working" {
		t.Fatalf("create: %+v %v", task, err)
	}
	if !strings.HasPrefix(task.TaskID, "task_") || len(task.TaskID) != len("task_")+32 {
		t.Fatalf("task id is not an opaque capability: %q", task.TaskID)
	}
	if _, err := m.Get("task_00000000000000000000000000000000"); err == nil || !strings.Contains(err.Error(), "unknown taskId") {
		t.Fatalf("unknown capability should fail, got %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := m.Get(task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			text := strings.TrimSpace(got.Result["content"].([]any)[0].(map[string]any)["text"].(string))
			if !strings.Contains(text, "early-task-ok") {
				t.Fatalf("result=%#v", got.Result)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not complete")
}

func TestPersistentCompletedTaskSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	pm := proc.NewManager()
	res, err := pm.Start(proc.StartOptions{Command: "printf persisted-ok", Workdir: t.TempDir(), Yield: 10 * time.Millisecond, Env: proc.SanitizedEnv()})
	if err != nil || res.SessionID == nil {
		t.Fatalf("start: %+v %v", res, err)
	}
	m, err := NewPersistentManager(pm, path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateProcess(*res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := m.Get(task.TaskID)
		if got.Status == "completed" {
			m2, err := NewPersistentManager(proc.NewManager(), path)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := m2.Get(task.TaskID)
			if err != nil || reloaded.Status != "completed" || reloaded.Result == nil {
				t.Fatalf("reloaded=%+v err=%v", reloaded, err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not complete")
}

func TestPersistedWorkingTaskFailsOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	now := time.Now().UTC()
	task := &Task{TaskID: "task_00000000000000000000000000000001", Status: "working", StatusMessage: "running", CreatedAt: now.Format(time.RFC3339Nano), LastUpdatedAt: now.Format(time.RFC3339Nano)}
	b, err := json.Marshal(persistedTasks{Version: 1, Tasks: map[string]*Task{task.TaskID: task}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewPersistentManager(proc.NewManager(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(task.TaskID)
	if err != nil || got.Status != "failed" || !strings.Contains(got.StatusMessage, "restarted") {
		t.Fatalf("restored=%+v err=%v", got, err)
	}
}

func TestCancelTask(t *testing.T) {
	pm := proc.NewManager()
	res, err := pm.Start(proc.StartOptions{Command: "sleep 30", Workdir: t.TempDir(), Yield: 10 * time.Millisecond, Env: proc.SanitizedEnv()})
	if err != nil || res.SessionID == nil {
		t.Fatalf("start: %+v %v", res, err)
	}
	m := NewManager(pm)
	task, err := m.CreateProcess(*res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(task.TaskID)
	if got.Status != "cancelled" {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestNonZeroProcessTaskFails(t *testing.T) {
	pm := proc.NewManager()
	res, err := pm.Start(proc.StartOptions{Command: "exit 7", Workdir: t.TempDir(), Yield: 10 * time.Millisecond, Env: proc.SanitizedEnv()})
	if err != nil || res.SessionID == nil {
		t.Fatalf("start: %+v %v", res, err)
	}
	m := NewManager(pm)
	task, err := m.CreateProcess(*res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := m.Get(task.TaskID)
		if got.Status != "working" {
			if got.Status != "failed" || got.Error == nil || got.Result == nil {
				t.Fatalf("task=%+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task remained working")
}

func TestShutdownFailsWorkingTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	pm := proc.NewManager()
	res, err := pm.Start(proc.StartOptions{Command: "sleep 30", Workdir: t.TempDir(), Yield: 10 * time.Millisecond, Env: proc.SanitizedEnv()})
	if err != nil || res.SessionID == nil {
		t.Fatalf("start: %+v %v", res, err)
	}
	m, err := NewPersistentManager(pm, path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateProcess(*res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	got, err := m.Get(task.TaskID)
	if err != nil || got.Status != "failed" || !strings.Contains(got.StatusMessage, "Daemon stopped") {
		t.Fatalf("task=%+v err=%v", got, err)
	}
	_ = pm.Kill(*res.SessionID, syscall.SIGKILL)
	m2, err := NewPersistentManager(proc.NewManager(), path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := m2.Get(task.TaskID)
	if err != nil || reloaded.Status != "failed" {
		t.Fatalf("reloaded=%+v err=%v", reloaded, err)
	}
}
