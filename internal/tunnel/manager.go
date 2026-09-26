package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/config"
)

var publicURLRE = regexp.MustCompile(`https://[A-Za-z0-9.-]+(?::\d+)?`)

type Status struct {
	Provider  string
	Running   bool
	Ready     bool
	PublicURL string
	Error     string
}
type Manager struct {
	mu         sync.RWMutex
	cmd        *exec.Cmd
	status     Status
	cancel     context.CancelFunc
	healthFile string
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
	capturePublicURL := true
	switch cfg.Provider {
	case "openai":
		if pid, err := FindTunnelIDCollision(cfg.TunnelID); err == nil && pid != 0 {
			return fail(fmt.Errorf("OpenAI tunnel %s is already used by local tunnel-client pid %d; use a dedicated tunnel ID for cos-lite", cfg.TunnelID, pid))
		}
		binary, err := FindBinary("tunnel-client")
		if err != nil {
			return fail(fmt.Errorf("tunnel-client not found; install OpenAI tunnel-client and retry"))
		}
		keyPath, err := ValidateOpenAIKeyFile()
		if err != nil {
			return fail(err)
		}
		healthFile, err := os.CreateTemp("", "cos-lite-openai-tunnel-health-*.url")
		if err != nil {
			return fail(err)
		}
		healthPath := healthFile.Name()
		_ = healthFile.Close()
		_ = os.Remove(healthPath)
		m.healthFile = healthPath
		capturePublicURL = false
		cmd = exec.CommandContext(ctx, binary, "run",
			"--control-plane.tunnel-id="+cfg.TunnelID,
			"--control-plane.api-key=file:"+keyPath,
			"--mcp.server-url="+localEndpoint,
			"--health.listen-addr=127.0.0.1:0",
			"--health.url-file="+healthPath,
			"--log.level=info",
			"--log.format=struct-text",
		)
	case "cloudflare":
		binary, err := FindBinary("cloudflared")
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
	go m.consume(stdout, localEndpoint, capturePublicURL)
	go m.consume(stderr, localEndpoint, capturePublicURL)
	if cfg.Provider == "openai" {
		go m.watchOpenAIReady(ctx, m.healthFile)
		go m.watchOpenAICollision(ctx, cfg.TunnelID)
	}
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		m.status.Running = false
		m.status.Ready = false
		m.cmd = nil
		if err != nil && ctx.Err() == nil && m.status.Error == "" {
			m.status.Error = err.Error()
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("tunnel exited: %v", err)
		}
	}()
	return nil
}

func (m *Manager) consume(r io.Reader, localEndpoint string, capturePublicURL bool) {
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
		if capturePublicURL {
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
}

func (m *Manager) watchOpenAIReady(ctx context.Context, path string) {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			base := strings.TrimSpace(string(b))
			if base == "" {
				continue
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/readyz", nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				m.mu.Lock()
				m.status.Ready = true
				m.mu.Unlock()
				return
			}
		}
	}
}

func (m *Manager) watchOpenAICollision(ctx context.Context, tunnelID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pid, err := FindTunnelIDCollision(tunnelID)
			if err != nil || pid == 0 {
				continue
			}
			msg := fmt.Sprintf("OpenAI tunnel %s is also being used by local tunnel-client pid %d; cos-lite stopped its tunnel to prevent split MCP routing. Configure a dedicated tunnel ID for cos-lite", tunnelID, pid)
			m.mu.Lock()
			if !m.status.Running || m.status.Provider != "openai" {
				m.mu.Unlock()
				return
			}
			cmd := m.cmd
			m.status.Error = msg
			m.status.Ready = false
			m.mu.Unlock()
			log.Printf("tunnel collision: %s", msg)
			if cmd != nil && cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			}
			return
		}
	}
}
func (m *Manager) setError(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Error = s
	m.status.Running = false
	m.status.Ready = false
}
func (m *Manager) Status() Status { m.mu.RLock(); defer m.mu.RUnlock(); return m.status }
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	cancel := m.cancel
	m.cmd = nil
	m.cancel = nil
	m.status.Running = false
	m.status.Ready = false
	healthFile := m.healthFile
	m.healthFile = ""
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	if healthFile != "" {
		_ = os.Remove(healthFile)
	}
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
