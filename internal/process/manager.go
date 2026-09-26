package process

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultYield     = 1000 * time.Millisecond
	DefaultTimeout   = 30 * time.Minute
	DefaultMaxOutput = 1 << 20
)

type Session struct {
	ID        int       `json:"session_id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
	TTY       bool      `json:"tty"`

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	output     bytes.Buffer
	readOffset int
	done       chan struct{}
	exitCode   *int
	err        error
	maxOutput  int
	truncated  bool
	lastAccess time.Time
}

type Manager struct {
	mu       sync.Mutex
	nextID   int
	sessions map[int]*Session
}

type StartOptions struct {
	Command   string
	Workdir   string
	TTY       bool
	Yield     time.Duration
	Timeout   time.Duration
	MaxOutput int
	Env       []string
}

type Result struct {
	SessionID *int   `json:"session_id,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Running   bool   `json:"running"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

func NewManager() *Manager {
	m := &Manager{nextID: 1, sessions: make(map[int]*Session)}
	go m.reaper()
	return m
}

func (m *Manager) Start(opts StartOptions) (Result, error) {
	if strings.TrimSpace(opts.Command) == "" {
		return Result{}, errors.New("command is empty")
	}
	if opts.Yield <= 0 {
		opts.Yield = DefaultYield
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = DefaultMaxOutput
	}

	var cmd *exec.Cmd
	if opts.TTY {
		if _, err := exec.LookPath("script"); err != nil {
			return Result{}, fmt.Errorf("tty requested but util-linux 'script' is unavailable: %w", err)
		}
		cmd = exec.Command("script", "-qefc", opts.Command, "/dev/null")
	} else {
		cmd = exec.Command("bash", "-lc", opts.Command)
	}
	cmd.Dir = opts.Workdir
	cmd.Env = opts.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}

	// Give os/exec writers instead of calling StdoutPipe/StderrPipe ourselves. Cmd.Wait
	// waits for its internal copy goroutines to finish before returning, which guarantees
	// that a completed session's final output is already in the retained buffer. Using
	// StdoutPipe concurrently with Wait has a documented race: Wait may close the pipe
	// before the reader goroutine has consumed the last bytes.
	s := &Session{Command: opts.Command, StartedAt: time.Now(), TTY: opts.TTY, cmd: cmd, stdin: stdin, done: make(chan struct{}), maxOutput: opts.MaxOutput, lastAccess: time.Now()}
	cmd.Stdout = sessionWriter{s: s}
	cmd.Stderr = sessionWriter{s: s}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return Result{}, err
	}

	m.mu.Lock()
	id := m.nextID
	m.nextID++
	s.ID = id
	m.sessions[id] = s
	m.mu.Unlock()

	go s.wait()
	go func() {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		select {
		case <-s.done:
			return
		case <-t.C:
			_ = killProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
			t2 := time.NewTimer(2 * time.Second)
			defer t2.Stop()
			select {
			case <-s.done:
			case <-t2.C:
				_ = killProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	}()

	return m.waitResult(s, opts.Yield), nil
}

type sessionWriter struct{ s *Session }

func (w sessionWriter) Write(p []byte) (int, error) {
	w.s.appendOutput(p)
	return len(p), nil
}

func (s *Session) appendOutput(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.maxOutput - s.output.Len()
	if remaining <= 0 {
		s.truncated = true
		return
	}
	if len(p) > remaining {
		_, _ = s.output.Write(p[:remaining])
		s.truncated = true
		return
	}
	_, _ = s.output.Write(p)
}

func (s *Session) wait() {
	err := s.cmd.Wait()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	s.mu.Lock()
	s.exitCode = &exit
	s.err = err
	s.lastAccess = time.Now()
	s.mu.Unlock()
	close(s.done)
}

func (m *Manager) waitResult(s *Session, d time.Duration) Result {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.done:
		return s.snapshot(true)
	case <-t.C:
		res := s.snapshot(true)
		res.Running = true
		id := s.ID
		res.SessionID = &id
		return res
	}
}

func (s *Session) snapshot(advance bool) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccess = time.Now()
	b := s.output.Bytes()
	start := s.readOffset
	if start > len(b) {
		start = len(b)
	}
	out := string(b[start:])
	if advance {
		s.readOffset = len(b)
	}
	running := s.exitCode == nil
	res := Result{ExitCode: s.exitCode, Running: running, Output: out, Truncated: s.truncated}
	if running {
		id := s.ID
		res.SessionID = &id
	}
	return res
}

func (m *Manager) Poll(id int, yield time.Duration) (Result, error) {
	s, err := m.get(id)
	if err != nil {
		return Result{}, err
	}
	if yield < 0 {
		yield = 0
	}
	if yield == 0 {
		return s.snapshot(true), nil
	}
	return m.waitResult(s, yield), nil
}

func (m *Manager) Write(id int, data string, yield time.Duration) (Result, error) {
	s, err := m.get(id)
	if err != nil {
		return Result{}, err
	}
	select {
	case <-s.done:
		return s.snapshot(true), nil
	default:
	}
	if data != "" {
		if data == "\x03" || data == "\u0003" {
			if err := killProcessGroup(s.cmd.Process.Pid, syscall.SIGINT); err != nil {
				return Result{}, err
			}
		} else {
			if _, err := io.WriteString(s.stdin, data); err != nil {
				return Result{}, err
			}
		}
	}
	if yield <= 0 {
		yield = 250 * time.Millisecond
	}
	return m.waitResult(s, yield), nil
}

func (m *Manager) Kill(id int, sig syscall.Signal) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	return killProcessGroup(s.cmd.Process.Pid, sig)
}

func killProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (m *Manager) get(id int) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return nil, fmt.Errorf("unknown session_id %d", id)
	}
	return s, nil
}

func (m *Manager) reaper() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-15 * time.Minute)
		m.mu.Lock()
		for id, s := range m.sessions {
			s.mu.Lock()
			done := s.exitCode != nil
			old := s.lastAccess.Before(cutoff)
			s.mu.Unlock()
			if done && old {
				delete(m.sessions, id)
			}
		}
		m.mu.Unlock()
	}
}

func SanitizedEnv() []string {
	blocked := map[string]bool{
		"OPENAI_API_KEY": true, "ANTHROPIC_API_KEY": true, "MCP_AUTH_TOKEN": true,
		"COS_TOKEN": true, "CHATGPT_TOKEN": true,
	}
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if blocked[name] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "TERM=xterm-256color", "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1")
	return out
}

func ParseSignal(v string) (syscall.Signal, error) {
	v = strings.TrimSpace(strings.ToUpper(v))
	switch v {
	case "", "TERM", "SIGTERM":
		return syscall.SIGTERM, nil
	case "INT", "SIGINT":
		return syscall.SIGINT, nil
	case "KILL", "SIGKILL":
		return syscall.SIGKILL, nil
	default:
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65 {
			return syscall.Signal(n), nil
		}
		return 0, fmt.Errorf("unsupported signal %q", v)
	}
}
