package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type State struct {
	PID         int      `json:"pid"`
	StartedAt   string   `json:"started_at"`
	Endpoint    string   `json:"endpoint"`
	Projects    []string `json:"projects"`
	Tunnel      string   `json:"tunnel"`
	PublicURL   string   `json:"public_url,omitempty"`
	TunnelError string   `json:"tunnel_error,omitempty"`
}

func Dir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "cos-lite"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "cos-lite"), nil
}
func StatePath() (string, error) {
	d, e := Dir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "state.json"), nil
}
func PIDPath() (string, error) {
	d, e := Dir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "daemon.pid"), nil
}
func LogPath() (string, error) {
	d, e := Dir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "daemon.log"), nil
}
func EnsureDir() error {
	d, e := Dir()
	if e != nil {
		return e
	}
	return os.MkdirAll(d, 0o700)
}

func WriteState(s State) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	p, _ := StatePath()
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if e = os.WriteFile(tmp, b, 0o600); e != nil {
		return e
	}
	return os.Rename(tmp, p)
}
func ReadState() (State, error) {
	p, e := StatePath()
	if e != nil {
		return State{}, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return State{}, e
	}
	var s State
	e = json.Unmarshal(b, &s)
	return s, e
}
func WritePID(pid int) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	p, _ := PIDPath()
	return os.WriteFile(p, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}
func ReadPID() (int, error) {
	p, e := PIDPath()
	if e != nil {
		return 0, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return 0, e
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
func RemoveRuntime() {
	if p, e := PIDPath(); e == nil {
		_ = os.Remove(p)
	}
	if p, e := StatePath(); e == nil {
		_ = os.Remove(p)
	}
}
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, e := os.FindProcess(pid)
	if e != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
func Running() (int, bool) {
	pid, e := ReadPID()
	if e != nil {
		return 0, false
	}
	if !Alive(pid) {
		RemoveRuntime()
		return 0, false
	}
	return pid, true
}
func WaitStopped(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := Running(); !ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
func TailLog(lines int) (string, error) {
	text, e := tailLogBytes(logTailBytes)
	if e != nil {
		return "", e
	}
	if text == "" {
		return "", nil
	}
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if lines > 0 && len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n"), nil
}
