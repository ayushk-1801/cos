package tasks

import (
	"strings"
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
