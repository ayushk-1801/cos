package browser

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/ayush/cos-lite/internal/sysinfo"
)

type poolEntry struct {
	manager  *Manager
	lastUsed time.Time
}

type Pool struct {
	mu       sync.Mutex
	headless bool
	clients  map[string]poolEntry
	defaults map[string]string
	idleTTL  time.Duration
	sweep    time.Duration
	stop     chan struct{}
	once     sync.Once
}

func NewPool(headless bool) *Pool {
	return NewPoolWithTTL(headless, 30*time.Minute, 30*time.Second)
}

func NewPoolWithTTL(headless bool, idleTTL, sweep time.Duration) *Pool {
	if idleTTL <= 0 {
		idleTTL = 30 * time.Minute
	}
	if sweep <= 0 {
		sweep = 5 * time.Minute
	}
	p := &Pool{headless: headless, clients: map[string]poolEntry{}, defaults: map[string]string{}, idleTTL: idleTTL, sweep: sweep, stop: make(chan struct{})}
	go p.reaper()
	return p
}

func (p *Pool) Create() (string, *Manager, error) {
	key, err := newContextID()
	if err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m := NewManagerForClient(p.headless, key)
	p.clients[key] = poolEntry{manager: m, lastUsed: time.Now()}
	return key, m, nil
}

func (p *Pool) Get(key string) (*Manager, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.clients[key]
	if !ok || entry.manager == nil {
		return nil, fmt.Errorf("unknown browser_context_id %q", key)
	}
	entry.lastUsed = time.Now()
	p.clients[key] = entry
	return entry.manager, nil
}

func (p *Pool) Default(clientKey string) (string, *Manager, error) {
	if clientKey == "" {
		clientKey = "anonymous"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id := p.defaults[clientKey]; id != "" {
		if entry, ok := p.clients[id]; ok && entry.manager != nil {
			entry.lastUsed = time.Now()
			p.clients[id] = entry
			return id, entry.manager, nil
		}
		delete(p.defaults, clientKey)
	}
	id, err := newContextID()
	if err != nil {
		return "", nil, err
	}
	m := NewManagerForClient(p.headless, id)
	p.clients[id] = poolEntry{manager: m, lastUsed: time.Now()}
	p.defaults[clientKey] = id
	return id, m, nil
}

func (p *Pool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

func (p *Pool) CloseContext(key string) error {
	p.mu.Lock()
	entry, ok := p.clients[key]
	if !ok || entry.manager == nil {
		p.mu.Unlock()
		return fmt.Errorf("unknown browser_context_id %q", key)
	}
	delete(p.clients, key)
	for client, id := range p.defaults {
		if id == key {
			delete(p.defaults, client)
		}
	}
	p.mu.Unlock()
	entry.manager.Shutdown()
	return nil
}

func newContextID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "browser_" + hex.EncodeToString(b), nil
}

func (p *Pool) Shutdown() {
	p.once.Do(func() {
		close(p.stop)
		p.mu.Lock()
		all := make([]*Manager, 0, len(p.clients))
		for _, entry := range p.clients {
			if entry.manager != nil {
				all = append(all, entry.manager)
			}
		}
		p.clients = map[string]poolEntry{}
		p.defaults = map[string]string{}
		p.mu.Unlock()
		for _, m := range all {
			m.Shutdown()
		}
	})
}

func (p *Pool) reaper() {
	ticker := time.NewTicker(p.sweep)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			p.evictIdle(now)
		}
	}
}

func (p *Pool) evictIdle(now time.Time) {
	p.mu.Lock()
	ttl := p.adaptiveTTL()
	var stale []struct {
		id string
		m  *Manager
	}
	for id, entry := range p.clients {
		if entry.manager != nil && now.Sub(entry.lastUsed) >= ttl {
			stale = append(stale, struct {
				id string
				m  *Manager
			}{id, entry.manager})
			delete(p.clients, id)
		}
	}
	if len(stale) > 0 {
		for client, id := range p.defaults {
			for _, item := range stale {
				if id == item.id {
					delete(p.defaults, client)
					break
				}
			}
		}
	}
	p.mu.Unlock()
	for _, item := range stale {
		item.m.Shutdown()
	}
}

func (p *Pool) adaptiveTTL() time.Duration {
	ttl := p.idleTTL
	switch sysinfo.MemoryStatus().Pressure {
	case sysinfo.PressureCritical:
		ttl = minDuration(ttl, time.Minute)
	case sysinfo.PressureElevated:
		ttl = minDuration(ttl, 5*time.Minute)
	}
	var total uint64
	for _, entry := range p.clients {
		if entry.manager != nil {
			if pid := entry.manager.PID(); pid > 0 {
				total += sysinfo.TreePSS(pid)
			}
		}
	}
	if total > 768<<20 {
		ttl = minDuration(ttl, 30*time.Second)
	} else if total > 512<<20 {
		ttl = minDuration(ttl, 2*time.Minute)
	}
	return ttl
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
