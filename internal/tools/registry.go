package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry { return &Registry{tools: make(map[string]Tool)} }

func (r *Registry) Register(t Tool) error {
	if t.Definition.Name == "" || t.Handler == nil {
		return fmt.Errorf("invalid tool registration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Definition.Name]; ok {
		return fmt.Errorf("tool already registered: %s", t.Definition.Name)
	}
	r.tools[t.Definition.Name] = t
	r.order = append(r.order, t.Definition.Name)
	return nil
}

func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Definition)
	}
	return out
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	return t.Handler(ctx, args)
}
