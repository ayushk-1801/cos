package plan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	persistVersion = 1
	maxPlans       = 128
	maxPersistSize = 2 << 20
)

type Item struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type State struct {
	Explanation string `json:"explanation,omitempty"`
	Plan        []Item `json:"plan"`
}

type record struct {
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type diskState struct {
	Version int               `json:"version"`
	Plans   map[string]record `json:"plans"`
}

type Store struct {
	mu     sync.RWMutex
	states map[string]record
	path   string
}

func NewPersistent(path string) (*Store, error) {
	s := &Store{path: path, states: map[string]record{}}
	if strings.TrimSpace(path) == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Create(state State) (string, State, error) {
	id, err := newPlanID()
	if err != nil {
		return "", State{}, err
	}
	copy := cloneState(state)
	now := time.Now().UTC()
	s.mu.Lock()
	if s.states == nil {
		s.states = map[string]record{}
	}
	s.states[id] = record{State: copy, CreatedAt: now, UpdatedAt: now}
	s.pruneLocked()
	err = s.saveLocked()
	if err != nil {
		delete(s.states, id)
	}
	s.mu.Unlock()
	if err != nil {
		return "", State{}, err
	}
	return id, copy, nil
}

func (s *Store) Update(id string, state State) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.states[id]
	if s.states == nil || !ok || rec.State.Plan == nil {
		return State{}, fmt.Errorf("unknown plan_id %q", id)
	}
	copy := cloneState(state)
	old := rec
	rec.State = copy
	rec.UpdatedAt = time.Now().UTC()
	s.states[id] = rec
	if err := s.saveLocked(); err != nil {
		s.states[id] = old
		return State{}, err
	}
	return copy, nil
}

func (s *Store) Get(id string) (State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.states[id]
	if !ok {
		return State{}, fmt.Errorf("unknown plan_id %q", id)
	}
	return cloneState(rec.State), nil
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.states)
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) > maxPersistSize {
		return fmt.Errorf("plan state exceeds %d bytes", maxPersistSize)
	}
	var disk diskState
	if err := json.Unmarshal(b, &disk); err != nil {
		return fmt.Errorf("parse persisted plans: %w", err)
	}
	if disk.Version != 0 && disk.Version != persistVersion {
		return fmt.Errorf("unsupported plan state version %d", disk.Version)
	}
	if disk.Plans != nil {
		s.states = disk.Plans
	}
	s.pruneLocked()
	return nil
}

func (s *Store) saveLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	disk := diskState{Version: persistVersion, Plans: s.states}
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxPersistSize {
		return fmt.Errorf("plan state exceeds %d bytes", maxPersistSize)
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) pruneLocked() {
	if len(s.states) <= maxPlans {
		return
	}
	type pair struct {
		id string
		t  time.Time
	}
	items := make([]pair, 0, len(s.states))
	for id, rec := range s.states {
		items = append(items, pair{id: id, t: rec.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].t.Before(items[j].t) })
	for len(s.states) > maxPlans && len(items) > 0 {
		delete(s.states, items[0].id)
		items = items[1:]
	}
}

func cloneState(state State) State {
	return State{Explanation: state.Explanation, Plan: append([]Item(nil), state.Plan...)}
}

func newPlanID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "plan_" + hex.EncodeToString(b), nil
}
