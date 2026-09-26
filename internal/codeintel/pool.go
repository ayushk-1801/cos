package codeintel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const DefaultPoolIdleTTL = 5 * time.Minute

type Pool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
	idleTTL time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
	stop    chan struct{}
	once    sync.Once
}

type poolEntry struct {
	useMu    sync.Mutex
	proc     *lspProc
	lastUsed time.Time
}

type Lease struct {
	pool  *Pool
	key   string
	entry *poolEntry
	once  sync.Once
}

func NewPool(idleTTL time.Duration) *Pool {
	if idleTTL <= 0 {
		idleTTL = DefaultPoolIdleTTL
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{entries: map[string]*poolEntry{}, idleTTL: idleTTL, ctx: ctx, cancel: cancel, stop: make(chan struct{})}
	go p.reaper()
	return p
}

func (p *Pool) Acquire(ctx context.Context, root string, argv []string) (*Lease, error) {
	if p == nil {
		return nil, fmt.Errorf("nil LSP pool")
	}
	key := root + "\x00" + strings.Join(argv, "\x00")
	for {
		p.mu.Lock()
		entry := p.entries[key]
		if entry == nil || entry.proc == nil || entry.proc.exited() {
			if entry != nil {
				delete(p.entries, key)
			}
			lp, err := startLSP(p.ctx, argv, root)
			if err != nil {
				p.mu.Unlock()
				return nil, err
			}
			if err := lp.initializeContext(ctx, root); err != nil {
				p.mu.Unlock()
				lp.close()
				return nil, err
			}
			entry = &poolEntry{proc: lp, lastUsed: time.Now()}
			p.entries[key] = entry
			p.mu.Unlock()
			entry.useMu.Lock()
			return &Lease{pool: p, key: key, entry: entry}, nil
		}
		p.mu.Unlock()

		entry.useMu.Lock()
		p.mu.Lock()
		current := p.entries[key]
		valid := current == entry && entry.proc != nil && !entry.proc.exited()
		if valid {
			entry.lastUsed = time.Now()
		}
		p.mu.Unlock()
		if valid {
			return &Lease{pool: p, key: key, entry: entry}, nil
		}
		entry.useMu.Unlock()
	}
}

func (l *Lease) Proc() *lspProc {
	if l == nil || l.entry == nil {
		return nil
	}
	return l.entry.proc
}

func (l *Lease) Release() {
	if l == nil || l.entry == nil || l.pool == nil {
		return
	}
	l.once.Do(func() {
		l.pool.mu.Lock()
		if l.pool.entries[l.key] == l.entry {
			l.entry.lastUsed = time.Now()
		}
		l.pool.mu.Unlock()
		l.entry.useMu.Unlock()
	})
}

func (p *Pool) Size() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.cancel()
		close(p.stop)
		p.mu.Lock()
		entries := make([]*poolEntry, 0, len(p.entries))
		for _, entry := range p.entries {
			entries = append(entries, entry)
		}
		p.entries = map[string]*poolEntry{}
		p.mu.Unlock()
		for _, entry := range entries {
			entry.useMu.Lock()
			if entry.proc != nil {
				entry.proc.close()
			}
			entry.useMu.Unlock()
		}
	})
}

func (p *Pool) reaper() {
	interval := p.idleTTL / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
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
	candidates := make([]struct {
		key   string
		entry *poolEntry
	}, 0)
	for key, entry := range p.entries {
		if entry == nil || entry.proc == nil || entry.proc.exited() || now.Sub(entry.lastUsed) >= p.idleTTL {
			candidates = append(candidates, struct {
				key   string
				entry *poolEntry
			}{key, entry})
		}
	}
	p.mu.Unlock()
	for _, candidate := range candidates {
		if candidate.entry == nil {
			continue
		}
		candidate.entry.useMu.Lock()
		p.mu.Lock()
		current := p.entries[candidate.key]
		remove := current == candidate.entry && (candidate.entry.proc == nil || candidate.entry.proc.exited() || now.Sub(candidate.entry.lastUsed) >= p.idleTTL)
		if remove {
			delete(p.entries, candidate.key)
		}
		p.mu.Unlock()
		if remove && candidate.entry.proc != nil {
			candidate.entry.proc.close()
		}
		candidate.entry.useMu.Unlock()
	}
}
