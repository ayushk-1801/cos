package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/app"
	"github.com/ayush/cos-lite/internal/browser"
	"github.com/ayush/cos-lite/internal/clientctx"
	"github.com/ayush/cos-lite/internal/codeintel"
	"github.com/ayush/cos-lite/internal/config"
	"github.com/ayush/cos-lite/internal/control"
	"github.com/ayush/cos-lite/internal/instructions"
	"github.com/ayush/cos-lite/internal/managedproc"
	"github.com/ayush/cos-lite/internal/mcp"
	planpkg "github.com/ayush/cos-lite/internal/plan"
	"github.com/ayush/cos-lite/internal/plugins"
	proc "github.com/ayush/cos-lite/internal/process"
	resourcepkg "github.com/ayush/cos-lite/internal/resources"
	"github.com/ayush/cos-lite/internal/service"
	skillpkg "github.com/ayush/cos-lite/internal/skills"
	"github.com/ayush/cos-lite/internal/sysinfo"
	taskpkg "github.com/ayush/cos-lite/internal/tasks"
	"github.com/ayush/cos-lite/internal/telemetry"
	"github.com/ayush/cos-lite/internal/tools"
	"github.com/ayush/cos-lite/internal/tui"
	tunnelpkg "github.com/ayush/cos-lite/internal/tunnel"
	"github.com/ayush/cos-lite/internal/workspace"
)

const version = "0.7.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	binary, err := os.Executable()
	if err != nil {
		binary = os.Args[0]
	}
	if abs, e := filepath.Abs(binary); e == nil {
		binary = abs
	}

	if len(args) == 0 {
		return tui.Run(binary)
	}
	// Backwards-compatible one-off mode: `cos --transport ... PATH`.
	if strings.HasPrefix(args[0], "-") {
		return runServe(args)
	}

	switch args[0] {
	case "help", "-h", "--help":
		printHelp(os.Stdout)
		return nil
	case "version", "--version":
		fmt.Printf("cos-lite %s\n", version)
		return nil
	case "doctor":
		return doctor()
	case "top":
		return topCommand(args[1:])
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return app.RunDaemon(ctx, version)
	case "start":
		return app.Start(binary)
	case "stop":
		return app.Stop()
	case "restart":
		return app.Restart(binary)
	case "status":
		return printStatus()
	case "endpoint":
		return printEndpoint()
	case "logs":
		return printLogs(args[1:])
	case "audit":
		return printAudit(args[1:])
	case "project":
		return projectCommand(binary, args[1:])
	case "skill":
		return skillCommand(binary, args[1:])
	case "code-intel":
		return codeIntelCommand(binary, args[1:])
	case "tunnel":
		return tunnelCommand(binary, args[1:])
	case "service":
		return serviceCommand(binary, args[1:])
	case "serve":
		return runServe(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run `cos help`", args[0])
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `cos-lite - Ubuntu-first local MCP tools

Usage:
  cos                         Open the TUI
  cos start|stop|restart      Manage the background daemon
  cos status                  Show daemon/projects/tunnel status
  cos endpoint                Print the URL to connect to ChatGPT
  cos project list
  cos project add PATH [NAME]
  cos project remove NAME
  cos project enable|disable NAME
  cos skill list
  cos skill add PATH [ID]
  cos skill remove ID
  cos skill enable|disable
  cos code-intel status|enable|disable
  cos tunnel status
  cos tunnel set none|openai TUNNEL_ID|cloudflare
  cos tunnel set custom COMMAND
  cos tunnel key-from-file PATH
  cos service enable|disable|status
  cos logs [--raw] [LINES]
  cos audit [--json] [LINES]
  cos doctor
  cos top [--once] [--interval 1s]
  cos serve [flags] [PATH]    One-off MCP server (stdio or HTTP)
`)
}

func doctor() error {
	fmt.Printf("cos-lite %s doctor\n", version)
	fmt.Printf("platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	missingRequired := false
	check := func(label string, names []string, required bool) string {
		for _, name := range names {
			if p, err := exec.LookPath(name); err == nil {
				fmt.Printf("[ok] %-12s %s\n", label, p)
				return p
			}
		}
		if required {
			missingRequired = true
			fmt.Printf("[missing] %-12s required (%s)\n", label, strings.Join(names, " or "))
		} else {
			fmt.Printf("[optional] %s not found (%s)\n", label, strings.Join(names, " or "))
		}
		return ""
	}
	check("bash", []string{"bash"}, true)
	if check("ripgrep", []string{"rg"}, false) == "" {
		fmt.Println("[info] find will use the built-in search engine; install ripgrep for faster large-repo searches")
	}
	check("tty", []string{"script"}, false)
	if mode, err := tools.JSSandboxStatus(); err == nil {
		fmt.Printf("[ok] %-12s %s\n", "js sandbox", mode)
	} else {
		fmt.Printf("[optional] JavaScript exec sandbox unavailable (%v); declarative exec calls still work\n", err)
	}
	check("browser", []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"}, false)
	if p, err := tunnelpkg.FindBinary("tunnel-client"); err == nil {
		fmt.Printf("[ok] %-12s %s\n", "OpenAI tunnel", p)
	} else {
		fmt.Println("[optional] OpenAI tunnel not found (tunnel-client)")
	}
	if p, err := tunnelpkg.FindBinary("cloudflared"); err == nil {
		fmt.Printf("[ok] %-12s %s\n", "Cloudflare", p)
	} else {
		fmt.Println("[optional] Cloudflare not found (cloudflared)")
	}
	check("systemd", []string{"systemctl"}, false)
	servers := codeintel.AvailableServers()
	showLSP := func(label, id, hint string) {
		if p := servers[id]; p != "" {
			fmt.Printf("[ok] %-12s %s\n", label, p)
		} else {
			fmt.Printf("[optional] %s not found (%s)\n", label, hint)
		}
	}
	showLSP("go LSP", "go", "gopls")
	showLSP("C/C++ LSP", "cpp", "clangd")
	showLSP("Rust LSP", "rust", "rust-analyzer")
	showLSP("Python LSP", "python", "basedpyright-langserver or pyright-langserver")
	showLSP("TS/JS LSP", "typescript", "typescript-language-server")
	if p, err := telemetry.AuditPath(); err == nil {
		fmt.Printf("[info] %-12s %s\n", "audit", p)
	}
	if p, err := telemetry.ActivityPath(); err == nil {
		fmt.Printf("[info] %-12s %s\n", "activity", p)
	}
	if endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); endpoint != "" {
		fmt.Printf("[ok] %-12s %s\n", "OTLP traces", endpoint)
	} else {
		fmt.Println("[info] OTLP tracing disabled (set OTEL_EXPORTER_OTLP_ENDPOINT to enable)")
	}
	cfg, _ := config.Load()
	fmt.Printf("[info] autostart policy=%v; linger=%v\n", cfg.Autostart, service.LingerEnabled())
	for _, p := range cfg.EnabledProjects() {
		st, err := os.Stat(p.Path)
		if err != nil {
			fmt.Printf("[warn] project %-10s %s (%v)\n", p.Name, p.Path, err)
			continue
		}
		if !st.IsDir() {
			fmt.Printf("[warn] project %-10s %s is not a directory\n", p.Name, p.Path)
		} else {
			fmt.Printf("[ok] project %-10s %s\n", p.Name, p.Path)
		}
	}
	if pid, running := control.Running(); running {
		sampler := sysinfo.NewSampler()
		snap := sampler.Sample(pid)
		fmt.Printf("[ok] %-12s pid=%d pss=%s memory-pressure=%s\n", "daemon", pid, sysinfo.Bytes(snap.TotalPSS), snap.Memory.Pressure)
		if state, err := control.ReadState(); err == nil {
			if err := probeMCPEndpoint(state.Endpoint); err != nil {
				fmt.Printf("[warn] %-12s %v\n", "MCP endpoint", err)
			} else {
				fmt.Printf("[ok] %-12s local endpoint responds\n", "MCP endpoint")
			}
		}
		if stale, err := managedproc.StaleEntries(pid); err == nil {
			if len(stale) == 0 {
				fmt.Printf("[ok] %-12s no registered orphan children\n", "child cleanup")
			} else {
				fmt.Printf("[warn] %-12s %d orphan children will be cleaned at next daemon start\n", "child cleanup", len(stale))
			}
		}
	} else {
		fmt.Printf("[info] %-12s stopped\n", "daemon")
	}
	if duplicate := duplicateOpenAITunnelProcesses(cfg.Tunnel.TunnelID); duplicate > 1 {
		fmt.Printf("[warn] %-12s tunnel ID is used by %d local tunnel-client processes\n", "tunnel ID", duplicate)
	} else if cfg.Tunnel.Provider == "openai" && strings.TrimSpace(cfg.Tunnel.TunnelID) != "" {
		fmt.Printf("[ok] %-12s no duplicate local process detected\n", "tunnel ID")
	}
	if codex, warnings, err := plugins.LoadCodexConfig(); err != nil {
		fmt.Printf("[warn] %-12s %v\n", "Codex MCP", err)
	} else {
		fmt.Printf("[ok] %-12s %d enabled stdio server(s)\n", "Codex MCP", len(codex))
		for _, warning := range warnings {
			fmt.Printf("[warn] Codex MCP   %s\n", warning)
		}
	}
	if health, err := plugins.ReadHealth(); err == nil {
		for _, h := range health.Servers {
			state := "ok"
			if h.State == "error" {
				state = "warn"
			}
			fmt.Printf("[%s] MCP %-8s state=%s tools=%d calls=%d failures=%d", state, h.Name, h.State, h.ToolCount, h.Calls, h.Failures)
			if h.PID > 0 {
				fmt.Printf(" pid=%d", h.PID)
			}
			if h.LastError != "" {
				fmt.Printf(" error=%s", clipDoctor(h.LastError, 100))
			}
			fmt.Println()
		}
	}
	if dir, err := control.Dir(); err == nil {
		for _, name := range []string{"plans.json", "tasks.json", "managed-processes.json", "mcp-health.json"} {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil {
				fmt.Printf("[ok] state %-7s %s (%d bytes, mode %04o)\n", strings.TrimSuffix(name, ".json"), p, st.Size(), st.Mode().Perm())
			}
		}
	}
	if missingRequired {
		fmt.Println("[fix] install the missing required tools shown above")
	}
	return nil
}

func probeMCPEndpoint(endpoint string) error {
	if strings.TrimSpace(endpoint) == "" {
		return errors.New("endpoint not recorded")
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"cos-doctor","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func duplicateOpenAITunnelProcesses(tunnelID string) int {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return 0
	}
	entries, _ := os.ReadDir("/proc")
	count := 0
	needle := "--control-plane.tunnel-id=" + tunnelID
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), needle) {
			count++
		}
	}
	return count
}

func clipDoctor(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func topCommand(args []string) error {
	fs := flag.NewFlagSet("cos top", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	once := fs.Bool("once", false, "print one resource snapshot and exit")
	interval := fs.Duration("interval", time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *interval < 200*time.Millisecond {
		return fmt.Errorf("interval must be at least 200ms")
	}
	pid, running := control.Running()
	if !running {
		return fmt.Errorf("cos-lite daemon is not running")
	}
	interactive := false
	if st, err := os.Stdout.Stat(); err == nil {
		interactive = st.Mode()&os.ModeCharDevice != 0
	}
	if !interactive {
		*once = true
	}
	sampler := sysinfo.NewSampler()
	// Prime CPU deltas so --once is useful too; the first /proc sample has no
	// previous tick baseline by definition.
	_ = sampler.Sample(pid)
	time.Sleep(250 * time.Millisecond)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		snap := sampler.Sample(pid)
		if interactive && !*once {
			fmt.Print("\x1b[H\x1b[2J")
		}
		printTopSnapshot(os.Stdout, snap)
		if *once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func printTopSnapshot(w io.Writer, snap sysinfo.Snapshot) {
	fmt.Fprintf(w, "cos top  pid=%d  pss=%s  rss=%s  cpu=%.1f%%  memory=%s (%s available)\n\n",
		snap.RootPID, sysinfo.Bytes(snap.TotalPSS), sysinfo.Bytes(snap.TotalRSS), snap.TotalCPU, snap.Memory.Pressure, sysinfo.Bytes(snap.Memory.AvailableBytes))
	fmt.Fprintln(w, "GROUP      COUNT       PSS       RSS      CPU")
	for _, g := range snap.Groups {
		fmt.Fprintf(w, "%-10s %5d %9s %9s %7.1f%%\n", g.Kind, g.Count, sysinfo.Bytes(g.PSSBytes), sysinfo.Bytes(g.RSSBytes), g.CPU)
	}
	fmt.Fprintln(w, "\n  PID  KIND           PSS      CPU  LABEL / COMMAND")
	limit := min(15, len(snap.Processes))
	for _, p := range snap.Processes[:limit] {
		label := strings.TrimSpace(p.Label)
		if label == "" {
			label = clipDoctor(p.Command, 72)
		}
		fmt.Fprintf(w, "%5d  %-8s %9s %7.1f%%  %s\n", p.PID, p.Kind, sysinfo.Bytes(p.PSSBytes), p.CPU, label)
	}
	fmt.Fprintln(w, "\nCtrl-C to exit")
}

func printStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pid, running := control.Running()
	if !running {
		fmt.Println("daemon: stopped")
	} else {
		fmt.Printf("daemon: running (pid %d)\n", pid)
	}
	names := make([]string, 0, len(cfg.EnabledProjects()))
	for _, p := range cfg.EnabledProjects() {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		fmt.Println("projects: none")
	} else {
		fmt.Printf("projects: %s\n", strings.Join(names, ", "))
	}
	if state, e := control.ReadState(); e == nil && running {
		if state.Tunnel != "" {
			fmt.Printf("tunnel: %s\n", state.Tunnel)
		}
		if state.PublicURL != "" {
			fmt.Printf("public: %s\n", state.PublicURL)
		}
	}
	mode := "login"
	if service.LingerEnabled() {
		mode = "boot"
	}
	fmt.Printf("autostart: %v (%s)\n", cfg.Autostart, mode)
	return nil
}

func printEndpoint() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Tunnel.Provider == "openai" && cfg.Tunnel.TunnelID != "" {
		fmt.Println(cfg.Tunnel.TunnelID)
		return nil
	}
	state, err := control.ReadState()
	if err != nil {
		if len(cfg.EnabledProjects()) == 0 {
			return errors.New("cos-lite is not serving anything yet: add a project with `cos project add PATH [NAME]` or open `cos` and press `a`")
		}
		if _, running := control.Running(); !running {
			return errors.New("cos-lite daemon is stopped; run `cos start` (or `cos status` for details)")
		}
		return fmt.Errorf("daemon state is not ready yet: %w", err)
	}
	if state.PublicURL != "" {
		fmt.Println(state.PublicURL)
		return nil
	}
	if state.Endpoint == "" {
		return errors.New("daemon is running but has not published an MCP endpoint yet; retry in a moment")
	}
	fmt.Println(state.Endpoint)
	return nil
}

func printLogs(args []string) error {
	lines := 100
	raw := false
	var positional []string
	for _, arg := range args {
		if arg == "--raw" {
			raw = true
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) > 1 {
		return errors.New("usage: cos logs [--raw] [LINES]")
	}
	if len(positional) == 1 {
		n, err := strconv.Atoi(positional[0])
		if err != nil || n < 1 {
			return errors.New("LINES must be a positive integer")
		}
		lines = n
	}
	if !raw {
		entries, err := control.TailLogEntries(lines, false)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No noteworthy daemon events yet. Use `cos logs --raw` for full logs.")
			return nil
		}
		for _, e := range entries {
			fmt.Println(control.FormatLogEntry(e))
		}
		return nil
	}
	text, err := control.TailLog(lines)
	if err != nil {
		return err
	}
	if text == "" {
		fmt.Println("No daemon logs yet.")
		return nil
	}
	fmt.Println(text)
	return nil
}

func printAudit(args []string) error {
	lines := 100
	jsonMode := false
	var positional []string
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) > 1 {
		return errors.New("usage: cos audit [--json] [LINES]")
	}
	if len(positional) == 1 {
		n, err := strconv.Atoi(positional[0])
		if err != nil || n < 1 {
			return errors.New("LINES must be a positive integer")
		}
		lines = n
	}
	events, err := telemetry.ReadAudit(lines)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Println("No MCP audit events yet.")
		return nil
	}
	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(events)
	}
	for _, e := range events {
		ts := e.Timestamp
		if parsed, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			ts = parsed.Local().Format("15:04:05")
		}
		target := e.Target
		if target == "" {
			target = "-"
		}
		client := e.ClientName
		if client == "" {
			client = e.ClientKey
		} else if suffix := clientKeySuffix(e.ClientKey); suffix != "" {
			client += "/" + suffix
		}
		fmt.Printf("%s  %-7s  %-14s  %-30s  %7.1fms  %s\n", ts, e.Status, client, e.Method+" "+target, e.DurationMS, shortTrace(e.TraceID))
	}
	return nil
}

func shortTrace(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

func clientKeySuffix(v string) string {
	if i := strings.LastIndex(v, "-"); i >= 0 && len(v)-i-1 >= 6 {
		v = v[i+1:]
	}
	if len(v) > 8 {
		return v[:8]
	}
	return v
}

func projectCommand(binary string, args []string) error {
	if len(args) == 0 {
		return errors.New("project command required: list, add, remove, enable, disable")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	saveAndReconcile := func() error {
		if err := config.Save(cfg); err != nil {
			return err
		}
		_, running := control.Running()
		if running || cfg.Autostart {
			return app.Reconcile(binary, cfg)
		}
		return nil
	}
	switch args[0] {
	case "list":
		if len(cfg.Projects) == 0 {
			fmt.Println("No projects configured. Add one with `cos project add PATH [NAME]` or open `cos` and press `a`.")
			return nil
		}
		for _, p := range cfg.Projects {
			state := "disabled"
			if p.Enabled {
				state = "enabled"
			}
			fmt.Printf("%-20s %-8s %s\n", p.Name, state, p.Path)
		}
		return nil
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: cos project add PATH [NAME]")
		}
		name := ""
		if len(args) == 3 {
			name = args[2]
		}
		if err := cfg.AddProject(expandHome(args[1]), name); err != nil {
			return err
		}
		if err := saveAndReconcile(); err != nil {
			return err
		}
		for _, p := range cfg.Projects {
			if p.Path == mustReal(expandHome(args[1])) || (name != "" && p.Name == name) {
				fmt.Printf("added /%s -> %s\n", p.Name, p.Path)
				break
			}
		}
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: cos project remove NAME")
		}
		if !cfg.RemoveProject(args[1]) {
			return fmt.Errorf("project %q not found", args[1])
		}
		return saveAndReconcile()
	case "enable", "disable":
		if len(args) != 2 {
			return fmt.Errorf("usage: cos project %s NAME", args[0])
		}
		found := false
		for i := range cfg.Projects {
			if cfg.Projects[i].Name == args[1] {
				cfg.Projects[i].Enabled = args[0] == "enable"
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("project %q not found", args[1])
		}
		return saveAndReconcile()
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func skillCommand(binary string, args []string) error {
	if len(args) == 0 {
		return errors.New("skill command required: list, add, remove, enable, disable")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	lib := skillLibrary(cfg)
	switch args[0] {
	case "list":
		list, warnings := lib.List()
		for _, sk := range list {
			fmt.Printf("%-30s %s\n", sk.ID, sk.Description)
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		if len(list) == 0 {
			fmt.Println("No skills found. Use ~/.agents/skills/<id>/SKILL.md or <repo>/.agents/skills/<id>/SKILL.md.")
		}
		return nil
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: cos skill add PATH [ID]")
		}
		id := ""
		if len(args) == 3 {
			id = args[2]
		}
		sk, err := skillpkg.Import(expandHome(args[1]), id)
		if err != nil {
			return err
		}
		fmt.Printf("installed %s at %s\n", sk.ID, sk.Path)
		return reconcileIfRunning(binary, cfg)
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: cos skill remove ID")
		}
		id := strings.TrimPrefix(args[1], "global/")
		if err := skillpkg.RemoveGlobal(id); err != nil {
			return err
		}
		return reconcileIfRunning(binary, cfg)
	case "enable", "disable":
		cfg.Skills.Enabled = args[0] == "enable"
		if err := config.Save(cfg); err != nil {
			return err
		}
		return reconcileIfRunning(binary, cfg)
	default:
		return fmt.Errorf("unknown skill command %q", args[0])
	}
}

func codeIntelCommand(binary string, args []string) error {
	if len(args) == 0 {
		return errors.New("code-intel command required: status, enable, disable")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		fmt.Printf("enabled: %v\n", cfg.CodeIntel.Enabled)
		servers := codeintel.AvailableServers()
		keys := make([]string, 0, len(servers))
		for k := range servers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("%-12s %s\n", k, servers[k])
		}
		return nil
	case "enable", "disable":
		cfg.CodeIntel.Enabled = args[0] == "enable"
		if err := config.Save(cfg); err != nil {
			return err
		}
		return reconcileIfRunning(binary, cfg)
	default:
		return fmt.Errorf("unknown code-intel command %q", args[0])
	}
}

func tunnelCommand(binary string, args []string) error {
	if len(args) == 0 {
		return errors.New("tunnel command required: status, set, or key-from-file")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		fmt.Printf("provider: %s\n", cfg.Tunnel.Provider)
		if cfg.Tunnel.Provider == "openai" {
			fmt.Printf("tunnel_id: %s\n", cfg.Tunnel.TunnelID)
			fmt.Printf("runtime_key: %s\n", map[bool]string{true: "configured", false: "missing"}[tunnelpkg.HasOpenAIKey()])
			if p, e := tunnelpkg.FindBinary("tunnel-client"); e == nil {
				fmt.Printf("client: %s\n", p)
			} else {
				fmt.Println("client: missing (tunnel-client)")
			}
		}
		if state, e := control.ReadState(); e == nil {
			fmt.Printf("state: %s\n", state.Tunnel)
			if state.PublicURL != "" {
				fmt.Printf("url: %s\n", state.PublicURL)
			}
			if state.TunnelError != "" {
				fmt.Printf("error: %s\n", state.TunnelError)
			}
		}
		return nil
	case "key-from-file":
		if len(args) != 2 {
			return errors.New("usage: cos tunnel key-from-file PATH")
		}
		b, err := os.ReadFile(expandHome(args[1]))
		if err != nil {
			return err
		}
		if err := tunnelpkg.WriteOpenAIKey(string(b)); err != nil {
			return err
		}
		fmt.Println("OpenAI tunnel runtime key stored securely")
		return reconcileIfRunning(binary, cfg)
	case "set":
		if len(args) < 2 {
			return errors.New("usage: cos tunnel set none|openai TUNNEL_ID|cloudflare|custom [COMMAND]")
		}
		switch args[1] {
		case "none", "cloudflare":
			if len(args) != 2 {
				return fmt.Errorf("usage: cos tunnel set %s", args[1])
			}
			cfg.Tunnel = config.Tunnel{Provider: args[1]}
		case "openai":
			if len(args) != 3 {
				return errors.New("usage: cos tunnel set openai TUNNEL_ID")
			}
			cfg.Tunnel = config.Tunnel{Provider: "openai", TunnelID: strings.TrimSpace(args[2])}
			if !tunnelpkg.HasOpenAIKey() {
				return errors.New("OpenAI runtime key is not configured; use the TUI (`cos`, then t) or `cos tunnel key-from-file PATH` first")
			}
		case "custom":
			if len(args) < 3 {
				return errors.New("usage: cos tunnel set custom COMMAND")
			}
			cfg.Tunnel = config.Tunnel{Provider: "custom", Command: strings.Join(args[2:], " ")}
		default:
			return fmt.Errorf("unsupported tunnel provider %q", args[1])
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		return reconcileIfRunning(binary, cfg)
	default:
		return fmt.Errorf("unknown tunnel command %q", args[0])
	}
}

func serviceCommand(binary string, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		fmt.Printf("policy: %v\n", cfg.Autostart)
		fmt.Printf("enabled: %v\n", service.Enabled())
		fmt.Printf("active: %v\n", service.Active())
		fmt.Printf("linger: %v\n", service.LingerEnabled())
		return nil
	case "enable":
		cfg.Autostart = true
		if err := config.Save(cfg); err != nil {
			return err
		}
		if len(cfg.EnabledProjects()) == 0 {
			fmt.Println("autostart policy enabled; the systemd unit will be installed when you add the first project")
			return nil
		}
		return app.Reconcile(binary, cfg)
	case "disable":
		cfg.Autostart = false
		if err := config.Save(cfg); err != nil {
			return err
		}
		return app.Reconcile(binary, cfg)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func reconcileIfRunning(binary string, cfg config.Config) error {
	_, running := control.Running()
	if running || cfg.Autostart {
		return app.Reconcile(binary, cfg)
	}
	return nil
}

func skillLibrary(cfg config.Config) *skillpkg.Library {
	var roots []workspace.Root
	for _, p := range cfg.EnabledProjects() {
		roots = append(roots, workspace.Root{Name: p.Name, Path: p.Path})
	}
	var ws *workspace.Workspace
	if len(roots) > 0 {
		ws, _ = workspace.NewRoots(roots)
	}
	return &skillpkg.Library{WS: ws, Global: cfg.Skills.Global, Repo: cfg.Skills.Repo}
}

type serveOptions struct {
	transport   string
	listen      string
	token       string
	allowRemote bool
	browser     bool
	headless    bool
	skills      bool
	codeIntel   bool
}

type serveRuntime struct {
	registry     *tools.Registry
	resources    *resourcepkg.Provider
	tasks        *taskpkg.Manager
	telemetry    *telemetry.Recorder
	instructions func(context.Context) string
	cleanup      func()
}

func runServe(args []string) error {
	defaults := config.Default()
	fs := flag.NewFlagSet("cos serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts := serveOptions{}
	fs.StringVar(&opts.transport, "transport", "http", "transport: http or stdio")
	fs.StringVar(&opts.listen, "listen", defaults.Listen, "HTTP listen address")
	fs.StringVar(&opts.token, "token", "auto", "HTTP path token: auto, none, or explicit value")
	fs.BoolVar(&opts.allowRemote, "allow-remote", false, "allow a non-loopback HTTP bind; prefer a tunnel")
	fs.BoolVar(&opts.browser, "browser", true, "expose generic Chromium CDP tools")
	fs.BoolVar(&opts.headless, "headless", true, "launch Chromium headless when first used")
	fs.BoolVar(&opts.skills, "skills", true, "expose global and repo-local skills")
	fs.BoolVar(&opts.codeIntel, "code-intel", true, "expose semantic code intelligence through installed LSPs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: cos serve [flags] [PATH]")
	}
	root := "."
	if fs.NArg() == 1 {
		root = expandHome(fs.Arg(0))
	}
	ws, err := workspace.New(root)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := buildRegistry(ctx, ws, opts)
	if err != nil {
		return err
	}
	defer rt.cleanup()
	srv := mcp.New(rt.registry)
	srv.Version = version
	srv.Tasks = rt.tasks
	srv.Telemetry = rt.telemetry
	srv.Update(rt.registry, rt.resources, rt.instructions)
	if opts.transport == "stdio" {
		return srv.ServeStdio(ctx, os.Stdin, os.Stdout)
	}
	if opts.transport != "http" {
		return fmt.Errorf("unsupported transport %q", opts.transport)
	}
	if !opts.allowRemote && !loopbackListen(opts.listen) {
		return fmt.Errorf("refusing non-loopback listen address %q; use --allow-remote only if you understand the exposure", opts.listen)
	}
	tok, err := oneOffToken(opts.token)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	endpoint := "http://" + ln.Addr().String() + "/mcp"
	if tok != "" {
		endpoint += "/" + tok
	}
	fmt.Fprintf(os.Stderr, "cos-lite %s\nWorkspace: %s\nMCP: %s\n", version, ws.RootSummary(), endpoint)
	server := &http.Server{Handler: srv.HTTPHandler(tok), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func buildRegistry(ctx context.Context, ws *workspace.Workspace, opts serveOptions) (*serveRuntime, error) {
	reg := tools.NewRegistry()
	register := func(t tools.Tool) error { return reg.Register(t) }
	for _, t := range []tools.Tool{
		(&tools.Reader{WS: ws}).Definition(),
		(&tools.ImageViewer{WS: ws}).Definition(),
		(&tools.Finder{WS: ws}).Definition(),
		(&tools.Patcher{WS: ws}).Definition(),
	} {
		if err := register(t); err != nil {
			return nil, err
		}
	}
	pm := proc.NewManager()
	taskManager := taskpkg.NewManager(pm)
	executor := &tools.Executor{WS: ws, PM: pm}
	if err := register(executor.ExecDefinition()); err != nil {
		return nil, err
	}
	if err := register(executor.StdinDefinition()); err != nil {
		return nil, err
	}
	if err := register((&tools.Planner{Store: &planpkg.Store{}}).Definition()); err != nil {
		return nil, err
	}
	skillLib := &skillpkg.Library{WS: ws, Global: true, Repo: true}
	if opts.skills {
		if err := register((&tools.SkillsTool{Library: skillLib}).Definition()); err != nil {
			return nil, err
		}
	}
	var lspPool *codeintel.Pool
	if opts.codeIntel {
		lspPool = codeintel.NewPool(codeintel.DefaultPoolIdleTTL)
		if err := register((&tools.CodeIntel{Client: &codeintel.Client{WS: ws, Pool: lspPool}}).Definition()); err != nil {
			lspPool.Close()
			return nil, err
		}
	}
	pluginManager := &plugins.Manager{}
	pluginCfg, pluginWarnings, err := plugins.LoadCodexConfig()
	if err != nil {
		if lspPool != nil {
			lspPool.Close()
		}
		return nil, err
	}
	for _, warning := range pluginWarnings {
		fmt.Fprintln(os.Stderr, "cos-lite:", warning)
	}
	if len(pluginCfg) > 0 {
		for _, warning := range pluginManager.Register(ctx, reg, pluginCfg) {
			fmt.Fprintln(os.Stderr, "cos-lite:", warning)
		}
	}
	var browserPool *browser.Pool
	if opts.browser {
		browserPool = browser.NewPool(opts.headless)
		for _, t := range (&tools.BrowserTools{Pool: browserPool}).Definitions() {
			if err := register(t); err != nil {
				pluginManager.Close()
				browserPool.Shutdown()
				return nil, err
			}
		}
	}
	if err := register((&tools.ExecMode{Registry: reg}).Definition()); err != nil {
		pluginManager.Close()
		if browserPool != nil {
			browserPool.Shutdown()
		}
		return nil, err
	}
	recorder, err := telemetry.New()
	if err != nil {
		pluginManager.Close()
		if browserPool != nil {
			browserPool.Shutdown()
		}
		return nil, err
	}
	resolver := &instructions.Resolver{WS: ws}
	resources := &resourcepkg.Provider{
		WS: ws, Skills: skillLib, Instructions: resolver,
		Status: func(reqCtx context.Context) any {
			info := clientctx.From(reqCtx)
			return map[string]any{"version": version, "workspace": ws.RootSummary(), "client": map[string]any{"key": info.Key, "name": info.Name, "version": info.Version}}
		},
		Audit: func(reqCtx context.Context) any { return recorder.Recent(clientctx.Key(reqCtx), 100) },
	}
	cleanup := func() {
		pluginManager.Close()
		if lspPool != nil {
			lspPool.Close()
		}
		if browserPool != nil {
			browserPool.Shutdown()
		}
		_ = recorder.Close()
	}
	instructionProvider := func(context.Context) string {
		text := "Ubuntu-first local coding tools. Approved project roots: " + ws.RootSummary() + "."
		if agents := resolver.ServerText(); agents != "" {
			text += "\n\n# Project instructions (AGENTS.md)\n" + agents
		}
		if opts.skills {
			text += "\n\n# Skills\nUse the skills tool to load a workflow only when relevant. Skills never grant extra permissions.\n" + skillLib.CatalogText()
		}
		if opts.codeIntel {
			text += "\n\n# Code intelligence\nPrefer code_intel for semantic definitions, references, types, diagnostics and rename previews; use find/rg for textual search."
		}
		text += "\n\n# Resources\nUse MCP Resources for project/Git/instruction/Skill/audit context."
		return text
	}
	return &serveRuntime{registry: reg, resources: resources, tasks: taskManager, telemetry: recorder, instructions: instructionProvider, cleanup: cleanup}, nil
}

func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func oneOffToken(v string) (string, error) {
	if v == "none" || v == "" {
		return "", nil
	}
	if v != "auto" {
		return validateToken(v)
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
		return validateToken(strings.TrimSpace(string(b)))
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

func validateToken(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("token contains invalid character %q", r)
		}
	}
	return v, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return h
			}
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func mustReal(p string) string {
	abs, _ := filepath.Abs(p)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
}
