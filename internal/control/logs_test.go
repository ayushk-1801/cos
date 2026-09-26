package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLogEntriesCompactsTunnelNoise(t *testing.T) {
	raw := strings.Join([]string{
		`2026/09/26 19:07:06.616076 tunnel: time=2026-09-26T19:07:06.613+05:30 level=INFO msg="OnStop hook executing" client_instance_id=x tunnel_id=tunnel_x component=runtimehealth`,
		`2026/09/26 19:07:37.970884 cos-lite 0.4.2 started; projects=College,eventmonitor,Hackathons,Projects,trendseer_audit endpoint=http://127.0.0.1:8766/mcp/********`,
		`2026/09/26 19:07:41.186551 tunnel: time=2026-09-26T19:07:41.186+05:30 level=INFO msg="🟢 tunnel-client started" component=controlplane tunnel_id=tunnel_x`,
		`2026/09/26 19:08:43.724260 tunnel start failed: OpenAI tunnel tunnel_x is already used by local tunnel-client pid 308454; use a dedicated tunnel ID for cos-lite`,
	}, "\n")

	entries := ParseLogEntries(raw, false)
	if len(entries) != 3 {
		t.Fatalf("got %d entries: %#v", len(entries), entries)
	}
	if entries[0].Component != "daemon" || !strings.Contains(entries[0].Message, "Started v0.4.2") || !strings.Contains(entries[0].Message, "5 projects") {
		t.Fatalf("bad daemon entry: %#v", entries[0])
	}
	if entries[1].Message != "OpenAI tunnel connected" {
		t.Fatalf("bad tunnel connected entry: %#v", entries[1])
	}
	if entries[2].Level != "ERROR" || !strings.Contains(entries[2].Message, "already used") {
		t.Fatalf("bad error entry: %#v", entries[2])
	}
}

func TestParseLogEntriesCanIncludeVerbose(t *testing.T) {
	raw := `2026/09/26 19:07:06.616076 tunnel: time=2026-09-26T19:07:06.613+05:30 level=INFO msg="OnStop hook executing" component=runtimehealth`
	entries := ParseLogEntries(raw, true)
	if len(entries) != 1 || !entries[0].Verbose {
		t.Fatalf("expected verbose entry: %#v", entries)
	}
}

func TestFormatLogEntry(t *testing.T) {
	entries := ParseLogEntries(`2026/09/26 19:08:43.724260 tunnel start failed: conflict`, false)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	got := FormatLogEntry(entries[0])
	for _, want := range []string{"19:08:43", "ERROR", "tunnel", "conflict"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
}

func TestCoalesceRepeatedEvents(t *testing.T) {
	raw := strings.Join([]string{
		`2026/09/26 19:08:40.000000 tunnel start failed: duplicate tunnel`,
		`2026/09/26 19:08:41.000000 cos-lite 0.4.2 started; projects=Projects endpoint=http://127.0.0.1:8766/mcp/********`,
		`2026/09/26 19:08:43.000000 tunnel start failed: duplicate tunnel`,
	}, "\n")
	entries := coalesceLogEntries(ParseLogEntries(raw, false), 2*time.Minute)
	if len(entries) != 2 {
		t.Fatalf("got %d entries: %#v", len(entries), entries)
	}
	if entries[1].Count != 2 || entries[1].Message != "duplicate tunnel" {
		t.Fatalf("bad merged entry: %#v", entries[1])
	}
}

func TestRotateLogIfNeeded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	stateDir := filepath.Join(dir, "cos-lite")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(stateDir, "daemon.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(logRotateAt + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if err := RotateLogIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("active log should be moved, stat err=%v", err)
	}
}
