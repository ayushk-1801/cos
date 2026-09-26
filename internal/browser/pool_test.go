package browser

import (
	"testing"
	"time"
)

func TestPoolSeparatesClients(t *testing.T) {
	p := NewPool(true)
	aID, a, err := p.Create()
	if err != nil {
		t.Fatal(err)
	}
	bID, b, err := p.Create()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a.profileKey == b.profileKey {
		t.Fatalf("clients share browser manager/profile: a=%p/%s b=%p/%s", a, a.profileKey, b, b.profileKey)
	}
	if aID == bID || len(aID) != len("browser_")+32 {
		t.Fatalf("bad context handles: %q %q", aID, bID)
	}
	got, err := p.Get(aID)
	if err != nil || got != a {
		t.Fatalf("context lookup: got=%p err=%v", got, err)
	}
	if _, err := p.Get("browser_00000000000000000000000000000000"); err == nil {
		t.Fatal("unknown browser context resolved")
	}
}

func TestPoolDefaultIsStablePerClientKey(t *testing.T) {
	p := NewPool(true)
	aID, a, err := p.Default("chat-a")
	if err != nil {
		t.Fatal(err)
	}
	aID2, a2, err := p.Default("chat-a")
	if err != nil {
		t.Fatal(err)
	}
	bID, b, err := p.Default("chat-b")
	if err != nil {
		t.Fatal(err)
	}
	if aID != aID2 || a != a2 {
		t.Fatal("default context was not stable for one client key")
	}
	if aID == bID || a == b {
		t.Fatal("different client keys shared a default browser context")
	}
}

func TestPoolEvictsIdleContexts(t *testing.T) {
	p := NewPoolWithTTL(true, 60*time.Millisecond, 20*time.Millisecond)
	defer p.Shutdown()
	id, _, err := p.Create()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.Count() == 0 {
			if _, err := p.Get(id); err == nil {
				t.Fatal("evicted context still resolved")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("idle context was not evicted, count=%d", p.Count())
}

func TestCloseContext(t *testing.T) {
	p := NewPool(true)
	defer p.Shutdown()
	id, _, err := p.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CloseContext(id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(id); err == nil {
		t.Fatal("closed context still resolved")
	}
}
