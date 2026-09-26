package process

import (
	"strings"
	"testing"
	"time"
)

func TestStartAndPoll(t *testing.T) {
	m := NewManager()
	res, err := m.Start(StartOptions{Command: "printf hello", Workdir: t.TempDir(), Yield: time.Second, Env: SanitizedEnv()})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("bad exit: %+v", res)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Fatalf("output=%q", res.Output)
	}
}

func TestBackgroundAndWriteStdin(t *testing.T) {
	m := NewManager()
	res, err := m.Start(StartOptions{Command: "read x; echo got:$x", Workdir: t.TempDir(), Yield: 50 * time.Millisecond, Env: SanitizedEnv()})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID == nil {
		t.Fatalf("expected session: %+v", res)
	}
	res2, err := m.Write(*res.SessionID, "abc\n", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Output, "got:abc") {
		t.Fatalf("output=%q", res2.Output)
	}
}
