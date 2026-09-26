package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	proc "github.com/ayush/cos-lite/internal/process"
)

const (
	DefaultTTL          = 24 * time.Hour
	DefaultPollInterval = time.Second
)

type Task struct {
	TaskID         string         `json:"taskId"`
	Status         string         `json:"status"`
	StatusMessage  string         `json:"statusMessage,omitempty"`
	CreatedAt      string         `json:"createdAt"`
	LastUpdatedAt  string         `json:"lastUpdatedAt"`
	TTLMS          *int64         `json:"ttlMs"`
	PollIntervalMS int64          `json:"pollIntervalMs,omitempty"`
	Result         map[string]any `json:"result,omitempty"`
	Error          map[string]any `json:"error,omitempty"`

	sessionID string
	created   time.Time
	updated   time.Time
}

type Manager struct {
	mu    sync.RWMutex
	pm    *proc.Manager
	tasks map[string]*Task
}

func NewManager(pm *proc.Manager) *Manager {
	m := &Manager{pm: pm, tasks: map[string]*Task{}}
	go m.reaper()
	return m
}

func (m *Manager) CreateProcess(sessionID string) (Task, error) {
	if m == nil || m.pm == nil {
		return Task{}, errors.New("task manager is not initialized")
	}
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	ttl := int64(DefaultTTL / time.Millisecond)
	t := &Task{
		TaskID: id, Status: "working", StatusMessage: "Command is still running.",
		CreatedAt: now.Format(time.RFC3339Nano), LastUpdatedAt: now.Format(time.RFC3339Nano), TTLMS: &ttl,
		PollIntervalMS: int64(DefaultPollInterval / time.Millisecond), sessionID: sessionID, created: now, updated: now,
	}
	m.mu.Lock()
	m.tasks[id] = t
	m.mu.Unlock()
	go m.watch(t)
	return cloneTask(t), nil
}

func (m *Manager) Get(id string) (Task, error) {
	m.mu.RLock()
	t := m.tasks[id]
	m.mu.RUnlock()
	if t == nil {
		return Task{}, fmt.Errorf("unknown taskId %q", id)
	}
	return cloneTask(t), nil
}

func (m *Manager) Update(id string, _ map[string]any) error {
	_, err := m.Get(id)
	return err
}

func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	t := m.tasks[id]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("unknown taskId %q", id)
	}
	if t.Status != "working" {
		return nil
	}
	if err := m.pm.Kill(t.sessionID, syscall.SIGTERM); err != nil && !strings.Contains(err.Error(), "unknown session_id") {
		return err
	}
	m.mu.Lock()
	if current := m.tasks[id]; current != nil && current.Status == "working" {
		m.setStatusLocked(current, "cancelled", "Cancellation requested.", nil, nil)
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) watch(t *Task) {
	for {
		res, err := m.pm.Poll(t.sessionID, 500*time.Millisecond)
		if err != nil {
			m.mu.Lock()
			if current := m.tasks[t.TaskID]; current != nil && current.Status == "working" {
				m.setStatusLocked(current, "failed", "Task polling failed.", nil, map[string]any{"code": -32603, "message": err.Error()})
			}
			m.mu.Unlock()
			return
		}
		if res.Running {
			continue
		}
		if full, fullErr := m.pm.Full(t.sessionID); fullErr == nil {
			res = full
		}
		m.mu.Lock()
		current := m.tasks[t.TaskID]
		if current == nil || current.Status != "working" {
			m.mu.Unlock()
			return
		}
		payload := processResultPayload(res)
		m.setStatusLocked(current, "completed", "Command completed.", payload, nil)
		m.mu.Unlock()
		return
	}
}

func (m *Manager) setStatusLocked(t *Task, status, message string, result, rpcErr map[string]any) {
	now := time.Now().UTC()
	t.Status = status
	t.StatusMessage = message
	t.updated = now
	t.LastUpdatedAt = now.Format(time.RFC3339Nano)
	t.Result = result
	t.Error = rpcErr
}

func processResultPayload(res proc.Result) map[string]any {
	var b strings.Builder
	if res.SessionID != nil {
		fmt.Fprintf(&b, "session_id: %s\n", *res.SessionID)
	}
	if res.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *res.ExitCode)
	} else {
		b.WriteString("status: running\n")
	}
	if res.Truncated {
		b.WriteString("warning: retained output hit its configured cap\n")
	}
	if res.Output != "" {
		b.WriteString("\n")
		b.WriteString(res.Output)
	}
	return map[string]any{
		"resultType":        "complete",
		"content":           []any{map[string]any{"type": "text", "text": strings.TrimRight(b.String(), "\n")}},
		"structuredContent": res,
	}
}

func cloneTask(t *Task) Task {
	if t == nil {
		return Task{}
	}
	out := *t
	if t.Result != nil {
		out.Result = cloneMap(t.Result)
	}
	if t.Error != nil {
		out.Error = cloneMap(t.Error)
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(b), nil
}

func (m *Manager) reaper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-DefaultTTL)
		m.mu.Lock()
		for id, task := range m.tasks {
			if task.Status != "working" && task.created.Before(cutoff) {
				delete(m.tasks, id)
			}
		}
		m.mu.Unlock()
	}
}
