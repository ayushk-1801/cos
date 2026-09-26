package plan

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
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
	state State
}

type Store struct {
	mu     sync.RWMutex
	states map[string]record
}

func (s *Store) Create(state State) (string, State, error) {
	id, err := newPlanID()
	if err != nil {
		return "", State{}, err
	}
	copy := cloneState(state)
	s.mu.Lock()
	if s.states == nil {
		s.states = map[string]record{}
	}
	s.states[id] = record{state: copy}
	s.mu.Unlock()
	return id, copy, nil
}

func (s *Store) Update(id string, state State) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.states[id]
	if s.states == nil || !ok || rec.state.Plan == nil {
		return State{}, fmt.Errorf("unknown plan_id %q", id)
	}
	copy := cloneState(state)
	s.states[id] = record{state: copy}
	return copy, nil
}

func (s *Store) Get(id string) (State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.states[id]
	if !ok {
		return State{}, fmt.Errorf("unknown plan_id %q", id)
	}
	return cloneState(rec.state), nil
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
