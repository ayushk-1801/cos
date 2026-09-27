package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/clientctx"
	"github.com/ayush/cos-lite/internal/config"
	"github.com/ayush/cos-lite/internal/control"
	"github.com/ayush/cos-lite/internal/managedproc"
	"github.com/ayush/cos-lite/internal/mcp"
	"github.com/ayush/cos-lite/internal/plugins"
	"github.com/ayush/cos-lite/internal/service"
	"github.com/ayush/cos-lite/internal/telemetry"
	tunnelpkg "github.com/ayush/cos-lite/internal/tunnel"
	"github.com/ayush/cos-lite/internal/watch"
)

func RunDaemon(ctx context.Context, version string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.EnabledProjects()) == 0 {
		return fmt.Errorf("no enabled projects; add one with `cos project add PATH` or the TUI")
	}
	if err := control.EnsureDir(); err != nil {
		return err
	}
	if err := control.RotateLogIfNeeded(); err != nil {
		return fmt.Errorf("rotate daemon log: %w", err)
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
	if report, err := managedproc.CleanupStale(os.Getpid()); err != nil {
		log.Printf("stale process cleanup failed: %v", err)
	} else if len(report.Killed)+len(report.Legacy)+len(report.Pruned) > 0 {
		log.Printf("stale process cleanup: killed=%d legacy_browser=%d pruned=%d", len(report.Killed), len(report.Legacy), len(report.Pruned))
	}

	recorder, err := telemetry.New()
	if err != nil {
		return err
	}
	defer recorder.Close()
	svc, err := newDaemonServices(recorder)
	if err != nil {
		return err
	}
	defer svc.shutdown()

	var runtimeMu sync.RWMutex
	var current *runtimeBundle
	var appliedCfg = cfg
	var tm tunnelpkg.Manager
	tunnelChanges := tm.Changes()
	statusFn := func(reqCtx context.Context) any {
		runtimeMu.RLock()
		bundle := current
		cfgSnapshot := appliedCfg
		runtimeMu.RUnlock()
		names := []string{}
		if bundle != nil {
			names = append(names, bundle.names...)
		}
		ts := tm.Status()
		info := clientctx.From(reqCtx)
		return map[string]any{
			"version": version, "pid": os.Getpid(), "projects": names,
			"tunnel": tunnelState(ts), "tunnelProvider": cfgSnapshot.Tunnel.Provider,
			"browserEnabled": cfgSnapshot.Browser, "skillsEnabled": cfgSnapshot.Skills.Enabled, "codeIntelEnabled": cfgSnapshot.CodeIntel.Enabled,
			"hotReload": true, "client": map[string]any{"key": info.Key, "name": info.Name, "version": info.Version},
		}
	}
	bundle, err := buildRuntime(ctx, cfg, svc, statusFn)
	if err != nil {
		return err
	}
	current = bundle
	writeMCPHealth := func() {
		runtimeMu.RLock()
		b := current
		runtimeMu.RUnlock()
		if b != nil && b.plugins != nil {
			if err := plugins.WriteHealth(b.plugins.Health()); err != nil {
				log.Printf("write MCP health: %v", err)
			}
		}
	}
	writeMCPHealth()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-svc.healthChanges:
				writeMCPHealth()
			}
		}
	}()
	defer func() {
		runtimeMu.RLock()
		last := current
		runtimeMu.RUnlock()
		if last != nil && last.plugins != nil {
			last.plugins.Close()
		}
	}()

	srv := mcp.New(bundle.registry)
	srv.Version = version
	srv.Tasks = svc.tasks
	srv.Telemetry = recorder
	srv.Update(bundle.registry, bundle.resources, bundle.instructionText)
	tok, err := resolveToken("auto")
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Printf("listen %s failed: %v", cfg.Listen, err)
		return err
	}
	defer ln.Close()
	server := &http.Server{Handler: srv.HTTPHandler(tok), ReadHeaderTimeout: 5 * time.Second}
	endpoint := "http://" + ln.Addr().String() + "/mcp"
	if tok != "" {
		endpoint += "/" + tok
	}
	log.Printf("cos-lite %s started; projects=%s endpoint=%s", version, strings.Join(bundle.names, ","), redactEndpoint(endpoint))

	if err := tm.Start(ctx, cfg.Tunnel, endpoint); err != nil {
		log.Printf("tunnel start failed: %v", err)
	}
	defer tm.Stop()
	startedAt := time.Now().UTC().Format(time.RFC3339)
	var stateMu sync.Mutex
	var lastState control.State
	haveState := false
	writeState := func() {
		runtimeMu.RLock()
		names := []string{}
		if current != nil {
			names = append(names, current.names...)
		}
		runtimeMu.RUnlock()
		ts := tm.Status()
		next := control.State{PID: os.Getpid(), StartedAt: startedAt, Endpoint: endpoint, Projects: names, Tunnel: tunnelState(ts), PublicURL: ts.PublicURL, TunnelError: ts.Error}
		stateMu.Lock()
		defer stateMu.Unlock()
		if haveState && reflect.DeepEqual(lastState, next) {
			return
		}
		if err := control.WriteState(next); err == nil {
			lastState = next
			haveState = true
		}
	}
	writeState()
	initialDigest, _ := configDigest()
	initialKeyDigest, _ := tunnelKeyDigest()
	initialCodexDigest, _ := codexConfigDigest()
	configPath, _ := config.Path()
	keyPath, _ := tunnelpkg.OpenAIKeyPath()
	codexPath, _ := plugins.CodexConfigPath()
	watchCh, watchErr := watch.Files(ctx, configPath, keyPath, codexPath)
	if watchErr != nil {
		log.Printf("config watcher: inotify unavailable, using 30s checksum fallback: %v", watchErr)
	}
	go func() {
		lastDigest := initialDigest
		lastKeyDigest := initialKeyDigest
		lastCodexDigest := initialCodexDigest
		fallback := time.NewTicker(30 * time.Second)
		defer fallback.Stop()
		events := watchCh
		reloadIfChanged := func() {
			digest, err := configDigest()
			if err != nil {
				return
			}
			keyDigest, _ := tunnelKeyDigest()
			codexDigest, _ := codexConfigDigest()
			configChanged := digest != lastDigest
			keyChanged := keyDigest != lastKeyDigest
			codexChanged := codexDigest != lastCodexDigest
			if !configChanged && !keyChanged && !codexChanged {
				return
			}
			lastDigest = digest
			lastKeyDigest = keyDigest
			lastCodexDigest = codexDigest
			candidate, err := config.Load()
			if err != nil {
				log.Printf("hot reload rejected: %v", err)
				return
			}
			runtimeMu.RLock()
			oldCfg := appliedCfg
			oldBundle := current
			runtimeMu.RUnlock()
			if candidate.Listen != oldCfg.Listen {
				log.Printf("hot reload: listen change %s -> %s requires daemon restart; applying other settings", oldCfg.Listen, candidate.Listen)
				candidate.Listen = oldCfg.Listen
			}
			if len(candidate.EnabledProjects()) == 0 {
				log.Printf("hot reload skipped: no enabled projects")
				return
			}
			newBundle, err := buildRuntime(ctx, candidate, svc, statusFn)
			if err != nil {
				log.Printf("hot reload rejected: %v", err)
				return
			}
			runtimeMu.Lock()
			current = newBundle
			appliedCfg = candidate
			runtimeMu.Unlock()
			srv.Update(newBundle.registry, newBundle.resources, newBundle.instructionText)
			writeMCPHealth()
			if oldCfg.Tunnel != candidate.Tunnel || keyChanged {
				tm.Stop()
				if err := tm.Start(ctx, candidate.Tunnel, endpoint); err != nil {
					log.Printf("hot reload tunnel start failed: %v", err)
				}
			}
			if oldBundle != nil {
				oldBundle.closeDelayed()
			}
			log.Printf("hot reload applied; projects=%s", strings.Join(newBundle.names, ","))
			writeState()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				// Editors/config.Save commonly use temp-file + rename. Give the
				// rename/write burst a moment to settle and coalesce duplicate events.
				time.Sleep(60 * time.Millisecond)
				for events != nil {
					select {
					case <-events:
					default:
						reloadIfChanged()
						goto next
					}
				}
			case <-fallback.C:
				reloadIfChanged()
			}
		next:
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-tunnelChanges:
				writeState()
			}
		}
	}()
	done := make(chan struct{})
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

func configDigest() (string, error) {
	p, err := config.Path()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func tunnelKeyDigest() (string, error) {
	p, err := tunnelpkg.OpenAIKeyPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func codexConfigDigest() (string, error) {
	p, err := plugins.CodexConfigPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func tunnelState(s tunnelpkg.Status) string {
	if s.Provider == "" || s.Provider == "none" {
		return "disabled"
	}
	if s.Provider == "openai" {
		if s.Ready {
			return "connected"
		}
		if s.Running {
			return "starting"
		}
		if s.Error != "" {
			return "error"
		}
		return "stopped"
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

// Reconcile applies persisted config to the background process. Since v0.5 the
// daemon hot-reloads project/tool/tunnel configuration, so an already-running
// daemon is deliberately left in place. Reconcile only owns service policy and
// first/last-project lifecycle.
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
			return service.Install(binary)
		}
		if service.Active() {
			return nil
		}
		return service.Restart()
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
		return nil
	}
	return Start(binary)
}
