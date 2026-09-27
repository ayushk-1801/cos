package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	proc "github.com/ayush/cos-lite/internal/process"
)

const (
	DefaultTTL          = 24 * time.Hour
	DefaultPollInterval = time.Second
	maxPersistedTasks   = 256
	maxTaskStateBytes   = 8 << 20
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
	mu      sync.RWMutex
	pm      *proc.Manager
	tasks   map[string]*Task
	path    string
	loadErr error
}

func NewManager(pm *proc.Manager) *Manager {
	m, _ := newManager(pm, "")
	return m
}

func NewPersistentManager(pm *proc.Manager, path string) (*Manager, error) {
	return newManager(pm, path)
}

func newManager(pm *proc.Manager, path string) (*Manager, error) {
	m := &Manager{pm: pm, tasks: map[string]*Task{}, path: strings.TrimSpace(path)}
	if m.path != "" {
		if err := m.load(); err != nil {
			return nil, err
		}
	}
	go m.reaper()
	return m, nil
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
	if err := m.saveLocked(); err != nil {
		delete(m.tasks, id)
		m.mu.Unlock()
		return Task{}, err
	}
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
		_ = m.saveLocked()
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
				_ = m.saveLocked()
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
		if res.ExitCode != nil && *res.ExitCode != 0 {
			m.setStatusLocked(current, "failed", "Command failed.", payload, map[string]any{
				"code": -32603, "message": fmt.Sprintf("command exited with code %d", *res.ExitCode),
			})
		} else {
			m.setStatusLocked(current, "completed", "Command completed.", payload, nil)
		}
		_ = m.saveLocked()
		m.mu.Unlock()
		return
	}
}

// Shutdown persists a terminal state for work that cannot survive this daemon
// process. This runs before service teardown so a graceful restart never turns
// an interrupted command into a misleading successful Task.
func (m *Manager) Shutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, task := range m.tasks {
		if task != nil && task.Status == "working" {
			m.setStatusLocked(task, "failed", "Daemon stopped before task completed.", nil, map[string]any{
				"code": -32603, "message": "cos-lite daemon stopped before the task completed",
			})
			changed = true
		}
	}
	if changed {
		_ = m.saveLocked()
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
		changed := false
		for id, task := range m.tasks {
			if task.Status != "working" && task.created.Before(cutoff) {
				delete(m.tasks, id)
				changed = true
			}
		}
		if changed {
			_ = m.saveLocked()
		}
		m.mu.Unlock()
	}
}

type persistedTasks struct {
	Version int              `json:"version"`
	Tasks   map[string]*Task `json:"tasks"`
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) > maxTaskStateBytes {
		return fmt.Errorf("task state exceeds %d bytes", maxTaskStateBytes)
	}
	var disk persistedTasks
	if err := json.Unmarshal(b, &disk); err != nil {
		return fmt.Errorf("parse persisted tasks: %w", err)
	}
	if disk.Version != 0 && disk.Version != 1 {
		return fmt.Errorf("unsupported task state version %d", disk.Version)
	}
	now := time.Now().UTC()
	for id, task := range disk.Tasks {
		if task == nil || task.TaskID != id {
			continue
		}
		task.created, _ = time.Parse(time.RFC3339Nano, task.CreatedAt)
		task.updated, _ = time.Parse(time.RFC3339Nano, task.LastUpdatedAt)
		if task.created.IsZero() {
			task.created = now
		}
		if task.updated.IsZero() {
			task.updated = task.created
		}
		task.sessionID = ""
		if task.Status == "working" {
			m.setStatusLocked(task, "failed", "Daemon restarted before task completed.", nil, map[string]any{
				"code": -32603, "message": "cos-lite daemon restarted before the task completed",
			})
		}
		m.tasks[id] = task
	}
	m.pruneLocked()
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	disk := persistedTasks{Version: 1, Tasks: m.tasks}
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxTaskStateBytes {
		return fmt.Errorf("task state exceeds %d bytes", maxTaskStateBytes)
	}
	b = append(b, '\n')
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manager) pruneLocked() {
	if len(m.tasks) <= maxPersistedTasks {
		return
	}
	type pair struct {
		id string
		t  time.Time
	}
	var items []pair
	for id, task := range m.tasks {
		if task.Status != "working" {
			items = append(items, pair{id: id, t: task.updated})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].t.Before(items[j].t) })
	for len(m.tasks) > maxPersistedTasks && len(items) > 0 {
		delete(m.tasks, items[0].id)
		items = items[1:]
	}
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}
