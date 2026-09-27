package sysinfo

import (
	"os"
	"testing"
	"time"
)

func TestMemoryAndSampler(t *testing.T) {
	m := MemoryStatus()
	if m.TotalBytes == 0 || m.AvailableBytes == 0 || m.Pressure == "" {
		t.Fatalf("memory=%+v", m)
	}
	s := NewSampler()
	first := s.Sample(os.Getpid())
	if first.RootPID != os.Getpid() || len(first.Processes) == 0 || first.TotalPSS == 0 {
		t.Fatalf("first=%+v", first)
	}
	time.Sleep(20 * time.Millisecond)
	second := s.Sample(os.Getpid())
	if second.RootPID != os.Getpid() || second.At.Before(first.At) {
		t.Fatalf("second=%+v", second)
	}
}
