package plan

import "sync"

type Item struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type State struct {
	Explanation string `json:"explanation,omitempty"`
	Plan        []Item `json:"plan"`
}

type Store struct {
	mu    sync.RWMutex
	state State
}

func (s *Store) Update(state State) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = State{Explanation: state.Explanation, Plan: append([]Item(nil), state.Plan...)}
	return s.state
}

func (s *Store) Get() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return State{Explanation: s.state.Explanation, Plan: append([]Item(nil), s.state.Plan...)}
}
