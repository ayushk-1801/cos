package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/ayush/cos-lite/internal/config"
)

var publicURLRE = regexp.MustCompile(`https://[A-Za-z0-9.-]+(?::\d+)?`)

type Status struct {
	Provider  string
	Running   bool
	PublicURL string
	Error     string
}
type Manager struct {
	mu     sync.RWMutex
	cmd    *exec.Cmd
	status Status
	cancel context.CancelFunc
}

func (m *Manager) Start(parent context.Context, cfg config.Tunnel, localEndpoint string) error {
	m.Stop()
	m.mu.Lock()
	m.status = Status{Provider: cfg.Provider}
	m.mu.Unlock()
	fail := func(err error) error { m.setError(err.Error()); return err }
	if cfg.Provider == "" || cfg.Provider == "none" {
		return nil
	}
	endpoint, err := url.Parse(localEndpoint)
	if err != nil {
		return fail(err)
	}
	origin := endpoint.Scheme + "://" + endpoint.Host
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	var cmd *exec.Cmd
	switch cfg.Provider {
	case "cloudflare":
		binary, err := exec.LookPath("cloudflared")
		if err != nil {
			return fail(fmt.Errorf("cloudflared not found; install it or choose a custom tunnel command"))
		}
		cmd = exec.CommandContext(ctx, binary, "tunnel", "--no-autoupdate", "--url", origin)
	case "custom":
		if strings.TrimSpace(cfg.Command) == "" {
			return fail(fmt.Errorf("custom tunnel command is empty"))
		}
		command := strings.ReplaceAll(cfg.Command, "{local_url}", shellQuote(localEndpoint))
		command = strings.ReplaceAll(command, "{origin}", shellQuote(origin))
		cmd = exec.CommandContext(ctx, "bash", "-lc", command)
	default:
		return fail(fmt.Errorf("unsupported tunnel provider %q", cfg.Provider))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		m.setError(err.Error())
		return err
	}
	m.mu.Lock()
	m.cmd = cmd
	m.status.Running = true
	m.mu.Unlock()
	go m.consume(stdout, localEndpoint)
	go m.consume(stderr, localEndpoint)
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		m.status.Running = false
		m.cmd = nil
		if err != nil && ctx.Err() == nil {
			m.status.Error = err.Error()
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("tunnel exited: %v", err)
		}
	}()
	return nil
}

func (m *Manager) consume(r io.Reader, localEndpoint string) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 256*1024)
	for s.Scan() {
		line := s.Text()
		logLine := line
		if parsed, err := url.Parse(localEndpoint); err == nil {
			if token := strings.TrimPrefix(parsed.Path, "/mcp/"); token != "" {
				logLine = strings.ReplaceAll(logLine, token, "********")
			}
		}
		log.Printf("tunnel: %s", logLine)
		if u := publicURLRE.FindString(line); u != "" {
			m.mu.Lock()
			if m.status.PublicURL == "" {
				parsed, _ := url.Parse(localEndpoint)
				m.status.PublicURL = strings.TrimRight(u, "/") + parsed.Path
			}
			m.mu.Unlock()
		}
	}
}
func (m *Manager) setError(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Error = s
	m.status.Running = false
}
func (m *Manager) Status() Status { m.mu.RLock(); defer m.mu.RUnlock(); return m.status }
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	cancel := m.cancel
	m.cmd = nil
	m.cancel = nil
	m.status.Running = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
