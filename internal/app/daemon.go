package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/browser"
	"github.com/ayush/cos-lite/internal/codeintel"
	"github.com/ayush/cos-lite/internal/config"
	"github.com/ayush/cos-lite/internal/control"
	"github.com/ayush/cos-lite/internal/mcp"
	planpkg "github.com/ayush/cos-lite/internal/plan"
	"github.com/ayush/cos-lite/internal/plugins"
	proc "github.com/ayush/cos-lite/internal/process"
	"github.com/ayush/cos-lite/internal/service"
	skillpkg "github.com/ayush/cos-lite/internal/skills"
	"github.com/ayush/cos-lite/internal/tools"
	tunnelpkg "github.com/ayush/cos-lite/internal/tunnel"
	"github.com/ayush/cos-lite/internal/workspace"
)

func RunDaemon(ctx context.Context, version string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	enabled := cfg.EnabledProjects()
	if len(enabled) == 0 {
		return fmt.Errorf("no enabled projects; add one with `cos project add PATH` or the TUI")
	}
	roots := make([]workspace.Root, 0, len(enabled))
	names := make([]string, 0, len(enabled))
	for _, p := range enabled {
		roots = append(roots, workspace.Root{Name: p.Name, Path: p.Path})
		names = append(names, p.Name)
	}
	ws, err := workspace.NewRoots(roots)
	if err != nil {
		return err
	}
	if err := control.EnsureDir(); err != nil {
		return err
	}
	logPath, _ := control.LogPath()
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(os.Stderr, lf))
	if err := control.WritePID(os.Getpid()); err != nil {
		return err
	}
	defer control.RemoveRuntime()

	pm := proc.NewManager()
	plans := &planpkg.Store{}
	reg := tools.NewRegistry()
	mustReg := func(t tools.Tool) error { return reg.Register(t) }
	for _, t := range []tools.Tool{(&tools.Reader{WS: ws}).Definition(), (&tools.ImageViewer{WS: ws}).Definition(), (&tools.Finder{WS: ws}).Definition(), (&tools.Patcher{WS: ws}).Definition()} {
		if err := mustReg(t); err != nil {
			return err
		}
	}
	ex := &tools.Executor{WS: ws, PM: pm}
	if err := mustReg(ex.ExecDefinition()); err != nil {
		return err
	}
	if err := mustReg(ex.StdinDefinition()); err != nil {
		return err
	}
	if err := mustReg((&tools.Planner{Store: plans}).Definition()); err != nil {
		return err
	}
	skillLib := &skillpkg.Library{WS: ws, Global: cfg.Skills.Global, Repo: cfg.Skills.Repo}
	if cfg.Skills.Enabled {
		if err := mustReg((&tools.SkillsTool{Library: skillLib}).Definition()); err != nil {
			return err
		}
	}
	if cfg.CodeIntel.Enabled {
		ci := &codeintel.Client{WS: ws, Overrides: cfg.CodeIntel.Overrides}
		if err := mustReg((&tools.CodeIntel{Client: ci}).Definition()); err != nil {
			return err
		}
	}

	pluginManager := &plugins.Manager{}
	pluginCfg, err := plugins.LoadConfig(cfg.PluginsPath)
	if err != nil {
		return err
	}
	if len(pluginCfg) > 0 {
		if err := pluginManager.Register(ctx, reg, pluginCfg); err != nil {
			return err
		}
	}
	defer pluginManager.Close()
	var bm *browser.Manager
	if cfg.Browser {
		bm = browser.NewManager(cfg.Headless)
		bt := &tools.BrowserTools{Browser: bm}
		for _, t := range bt.Definitions() {
			if err := reg.Register(t); err != nil {
				return err
			}
		}
		defer bm.Shutdown()
	}
	if err := reg.Register((&tools.ExecMode{Registry: reg}).Definition()); err != nil {
		return err
	}

	srv := mcp.New(reg)
	srv.Version = version
	srv.Instructions = "Ubuntu-first local coding tools. Approved project roots: " + ws.RootSummary() + ". With multiple projects, use /<project>/... paths explicitly."
	if cfg.Skills.Enabled {
		srv.Instructions += "\n\n# Skills\nUse the skills tool to load a workflow only when relevant. Skills never grant extra permissions.\n" + skillLib.CatalogText()
	}
	if cfg.CodeIntel.Enabled {
		srv.Instructions += "\n\n# Code intelligence\nPrefer code_intel for semantic definitions, references, types, diagnostics and rename previews; use find/rg for textual search."
	}
	tok, err := resolveToken("auto")
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	server := &http.Server{Handler: srv.HTTPHandler(tok), ReadHeaderTimeout: 5 * time.Second}
	endpoint := "http://" + ln.Addr().String() + "/mcp"
	if tok != "" {
		endpoint += "/" + tok
	}
	log.Printf("cos-lite %s started; projects=%s endpoint=%s", version, strings.Join(names, ","), redactEndpoint(endpoint))

	var tm tunnelpkg.Manager
	if err := tm.Start(ctx, cfg.Tunnel, endpoint); err != nil {
		log.Printf("tunnel start failed: %v", err)
	}
	defer tm.Stop()
	startedAt := time.Now().UTC().Format(time.RFC3339)
	writeState := func() {
		ts := tm.Status()
		_ = control.WriteState(control.State{PID: os.Getpid(), StartedAt: startedAt, Endpoint: endpoint, Projects: names, Tunnel: tunnelState(ts), PublicURL: ts.PublicURL, TunnelError: ts.Error})
	}
	writeState()
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeState()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(done)
	}()
	err = server.Serve(ln)
	if err == http.ErrServerClosed {
		err = nil
	}
	return err
}

func tunnelState(s tunnelpkg.Status) string {
	if s.Provider == "" || s.Provider == "none" {
		return "disabled"
	}
	if s.Running {
		if s.PublicURL != "" {
			return "connected"
		}
		return "starting"
	}
	if s.Error != "" {
		return "error"
	}
	return "stopped"
}
func redactEndpoint(v string) string {
	idx := strings.LastIndex(v, "/")
	if idx < 0 {
		return v
	}
	return v[:idx+1] + "********"
}

func Start(binary string) error {
	if _, ok := control.Running(); ok {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.EnabledProjects()) == 0 {
		return fmt.Errorf("no enabled projects")
	}
	if service.Enabled() {
		if service.Active() {
			return nil
		}
		cmd := exec.Command("systemctl", "--user", "start", "cos-lite.service")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl start: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return waitStarted(4 * time.Second)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(binary, "daemon")
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return waitStarted(4 * time.Second)
}
func waitStarted(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := control.Running(); ok {
			if _, err := control.ReadState(); err == nil {
				return nil
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	tail, _ := control.TailLog(20)
	return fmt.Errorf("daemon did not start\n%s", tail)
}
func Stop() error {
	if service.Enabled() && service.Active() {
		return service.Stop()
	}
	pid, ok := control.Running()
	if !ok {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	if !control.WaitStopped(4 * time.Second) {
		_ = p.Signal(syscall.SIGKILL)
		if !control.WaitStopped(time.Second) {
			return fmt.Errorf("daemon pid %d did not stop", pid)
		}
	}
	return nil
}
func Restart(binary string) error {
	if service.Enabled() {
		if !service.Active() {
			return service.Install(binary)
		}
		return service.Restart()
	}
	if err := Stop(); err != nil {
		return err
	}
	return Start(binary)
}

func resolveToken(v string) (string, error) {
	if v == "none" || v == "" {
		return "", nil
	}
	if v != "auto" {
		return cleanToken(v)
	}
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(d, "http-token")
	if b, err := os.ReadFile(p); err == nil {
		_ = os.Chmod(p, 0o600)
		return cleanToken(strings.TrimSpace(string(b)))
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}
func cleanToken(v string) (string, error) {
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("token contains invalid character %q", r)
		}
	}
	return v, nil
}

// Reconcile applies persisted config to the background process. Autostart is a
// policy: when enabled and at least one project is exposed, the systemd user
// service is installed/enabled and restarted so it will come back after reboot
// (with linger when the host permits it). With no enabled projects the service
// remains enabled but is stopped, avoiding a restart loop.
func Reconcile(binary string, cfg config.Config) error {
	enabled := len(cfg.EnabledProjects()) > 0
	if cfg.Autostart && service.Available() {
		if !enabled {
			if service.Active() {
				return service.Stop()
			}
			return nil
		}
		// Migrate a daemon that was started directly (for example by v0.2) before
		// handing ownership to systemd; otherwise both processes can race for the port.
		if !service.Enabled() {
			if _, running := control.Running(); running {
				if err := Stop(); err != nil {
					return err
				}
			}
		}
		return service.Install(binary)
	}
	if service.Enabled() && !cfg.Autostart {
		if err := service.Remove(); err != nil {
			return err
		}
	}
	if !enabled {
		return Stop()
	}
	if _, ok := control.Running(); ok {
		return Restart(binary)
	}
	return Start(binary)
}
