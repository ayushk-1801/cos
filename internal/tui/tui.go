//go:build linux

package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ayush/cos-lite/internal/app"
	"github.com/ayush/cos-lite/internal/codeintel"
	"github.com/ayush/cos-lite/internal/config"
	"github.com/ayush/cos-lite/internal/control"
	"github.com/ayush/cos-lite/internal/service"
	skillpkg "github.com/ayush/cos-lite/internal/skills"
	"github.com/ayush/cos-lite/internal/telemetry"
	tunnelpkg "github.com/ayush/cos-lite/internal/tunnel"
	"github.com/ayush/cos-lite/internal/workspace"
)

const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	red     = "\x1b[31m"
	cyan    = "\x1b[36m"
	reverse = "\x1b[7m"
)

type screen int

const (
	dashboard screen = iota
	addProject
	tunnelSetup
	openAITunnelID
	openAITunnelKey
	customTunnel
	logsView
	confirmRemove
	skillsView
	activityView
)

type Model struct {
	cfg            config.Config
	selected       int
	screen         screen
	input          string
	message        string
	binary         string
	logsRaw        bool
	activityOffset int
}
type key struct {
	kind string
	r    rune
}

func Run(binary string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	defer tty.Close()
	restore, err := rawMode(tty)
	if err != nil {
		return err
	}
	defer restore()
	fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l"+reset)
	m := &Model{cfg: cfg, binary: binary}
	keys := make(chan key, 8)
	go readKeys(tty, keys)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	render(tty, m)
	for {
		select {
		case k := <-keys:
			if m.handle(k) {
				return nil
			}
			render(tty, m)
		case <-tick.C:
			render(tty, m)
		}
	}
}

func (m *Model) handle(k key) bool {
	if k.kind == "ctrlc" {
		return true
	}
	if m.screen == addProject || m.screen == customTunnel || m.screen == openAITunnelID || m.screen == openAITunnelKey {
		return m.handleInput(k)
	}
	switch m.screen {
	case activityView:
		events, _ := telemetry.ReadActivity(200)
		maxOffset := max(0, len(events)-1)
		switch {
		case (k.kind == "rune" && (k.r == 'q')) || k.kind == "esc" || k.kind == "enter":
			m.screen = dashboard
		case k.kind == "up" || (k.kind == "rune" && k.r == 'k'):
			if m.activityOffset < maxOffset {
				m.activityOffset++
			}
		case k.kind == "down" || (k.kind == "rune" && k.r == 'j'):
			if m.activityOffset > 0 {
				m.activityOffset--
			}
		case k.kind == "rune" && k.r == 'g':
			m.activityOffset = 0
		}
		return false
	case logsView:
		if k.kind == "rune" && k.r == 'r' {
			m.logsRaw = !m.logsRaw
			return false
		}
		if (k.kind == "rune" && k.r == 'q') || k.kind == "esc" || k.kind == "enter" {
			m.screen = dashboard
		}
		return false
	case skillsView:
		if (k.kind == "rune" && k.r == 'q') || k.kind == "esc" || k.kind == "enter" {
			m.screen = dashboard
		}
		return false
	case confirmRemove:
		if k.kind == "rune" && (k.r == 'y' || k.r == 'Y') {
			if len(m.cfg.Projects) > 0 {
				running := isRunning()
				name := m.cfg.Projects[m.selected].Name
				m.cfg.RemoveProject(name)
				if m.selected >= len(m.cfg.Projects) && m.selected > 0 {
					m.selected--
				}
				if err := config.Save(m.cfg); err != nil {
					m.message = err.Error()
				} else {
					m.message = "removed " + name
					if running || m.cfg.Autostart {
						m.reconcileAfterProjectChange()
					}
				}
			}
			m.screen = dashboard
		} else if k.kind == "rune" && (k.r == 'n' || k.r == 'N') {
			m.screen = dashboard
		}
		return false
	case tunnelSetup:
		if (k.kind == "rune" && k.r == 'q') || k.kind == "esc" {
			m.screen = dashboard
			return false
		}
		if k.kind == "rune" {
			switch k.r {
			case '1':
				m.setTunnel("none", "")
			case '2':
				m.screen = openAITunnelID
				m.input = m.cfg.Tunnel.TunnelID
			case '3':
				m.setTunnel("cloudflare", "")
			case '4':
				m.screen = customTunnel
				m.input = m.cfg.Tunnel.Command
			case '5':
				m.screen = openAITunnelKey
				m.input = ""
			}
		}
		return false
	}
	switch k.kind {
	case "q":
		return true
	case "up":
		if m.selected > 0 {
			m.selected--
		}
	case "down":
		if m.selected+1 < len(m.cfg.Projects) {
			m.selected++
		}
	case "rune":
		switch k.r {
		case 'q':
			return true
		case 'j':
			if m.selected+1 < len(m.cfg.Projects) {
				m.selected++
			}
		case 'k':
			if m.selected > 0 {
				m.selected--
			}
		case 'a':
			m.screen = addProject
			m.input = ""
		case 'd':
			if len(m.cfg.Projects) > 0 {
				m.screen = confirmRemove
			}
		case ' ':
			m.toggleProject()
		case 'r':
			m.restart()
		case 's':
			m.toggleDaemon()
		case 't':
			m.screen = tunnelSetup
		case 'l':
			m.screen = logsView
		case 'i':
			m.screen = skillsView
		case 'o':
			m.screen = activityView
			m.activityOffset = 0
		case 'b':
			m.cfg.Browser = !m.cfg.Browser
			if err := config.Save(m.cfg); err != nil {
				m.message = err.Error()
			} else if err := app.Reconcile(m.binary, m.cfg); err != nil {
				m.message = err.Error()
			} else {
				m.message = fmt.Sprintf("browser tools %v", m.cfg.Browser)
			}
		case 'c':
			m.cfg.CodeIntel.Enabled = !m.cfg.CodeIntel.Enabled
			if err := config.Save(m.cfg); err != nil {
				m.message = err.Error()
			} else if err := app.Reconcile(m.binary, m.cfg); err != nil {
				m.message = err.Error()
			} else {
				m.message = fmt.Sprintf("code intelligence %v", m.cfg.CodeIntel.Enabled)
			}
		case 'u':
			m.toggleService()
		}
	}
	return false
}
func (m *Model) handleInput(k key) bool {
	switch k.kind {
	case "ctrlc":
		return true
	case "esc":
		m.screen = dashboard
	case "backspace":
		if len(m.input) > 0 {
			_, n := utf8.DecodeLastRuneInString(m.input)
			if n > 0 {
				m.input = m.input[:len(m.input)-n]
			}
		}
	case "enter":
		if m.screen == addProject {
			m.finishAdd()
		} else if m.screen == customTunnel {
			m.setTunnel("custom", strings.TrimSpace(m.input))
		} else if m.screen == openAITunnelID {
			m.finishOpenAITunnelID()
		} else if m.screen == openAITunnelKey {
			m.finishOpenAIKey()
		}
	case "rune":
		if k.r >= 32 && k.r != 127 {
			m.input += string(k.r)
		}
	}
	return false
}

func (m *Model) finishOpenAITunnelID() {
	id := strings.TrimSpace(m.input)
	if id == "" {
		m.message = "tunnel ID cannot be empty"
		return
	}
	candidate := m.cfg
	candidate.Tunnel = config.Tunnel{Provider: "openai", TunnelID: id}
	if err := candidate.Normalize(); err != nil {
		m.message = err.Error()
		return
	}
	m.cfg.Tunnel = candidate.Tunnel
	if tunnelpkg.HasOpenAIKey() {
		m.finishOpenAISetup()
		return
	}
	m.screen = openAITunnelKey
	m.input = ""
}

func (m *Model) finishOpenAIKey() {
	key := strings.TrimSpace(m.input)
	m.input = ""
	if key == "" {
		m.message = "runtime API key cannot be empty"
		return
	}
	if err := tunnelpkg.WriteOpenAIKey(key); err != nil {
		m.message = err.Error()
		return
	}
	if m.cfg.Tunnel.Provider != "openai" {
		m.message = "OpenAI tunnel runtime API key stored"
		m.screen = dashboard
		return
	}
	m.finishOpenAISetup()
}

func (m *Model) finishOpenAISetup() {
	if _, err := tunnelpkg.FindBinary("tunnel-client"); err != nil {
		m.message = "OpenAI tunnel saved, but tunnel-client is not installed"
	}
	if err := config.Save(m.cfg); err != nil {
		m.message = err.Error()
		return
	}
	if isRunning() || m.cfg.Autostart {
		if err := app.Reconcile(m.binary, m.cfg); err != nil {
			m.message = err.Error()
			m.screen = dashboard
			return
		}
	}
	if m.message == "" {
		m.message = "OpenAI Secure MCP Tunnel configured"
	}
	m.screen = dashboard
}
func (m *Model) finishAdd() {
	p := expandHome(strings.TrimSpace(m.input))
	if p == "" {
		m.message = "path cannot be empty"
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		m.message = err.Error()
		return
	}
	name := config.SuggestedName(abs)
	base := name
	for i := 2; projectNameExists(m.cfg, name); i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	running := isRunning()
	if err := m.cfg.AddProject(abs, name); err != nil {
		m.message = err.Error()
		return
	}
	if err := config.Save(m.cfg); err != nil {
		m.message = err.Error()
		return
	}
	m.message = "added /" + name
	m.screen = dashboard
	m.selected = indexOf(m.cfg, name)
	if running || m.cfg.Autostart {
		if err := app.Reconcile(m.binary, m.cfg); err != nil {
			m.message = err.Error()
		}
	}
}
func projectNameExists(c config.Config, name string) bool {
	for _, p := range c.Projects {
		if p.Name == name {
			return true
		}
	}
	return false
}
func indexOf(c config.Config, name string) int {
	for i, p := range c.Projects {
		if p.Name == name {
			return i
		}
	}
	return 0
}
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, e := os.UserHomeDir(); e == nil {
			if p == "~" {
				return h
			}
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
func isRunning() bool { _, ok := control.Running(); return ok }
func (m *Model) restart() {
	if len(m.cfg.EnabledProjects()) == 0 {
		m.message = "no enabled projects; enable or add one first"
		return
	}
	if err := app.Restart(m.binary); err != nil {
		m.message = "restart: " + err.Error()
	} else {
		m.message = "daemon restarted"
	}
}

func (m *Model) reconcileAfterProjectChange() {
	if err := app.Reconcile(m.binary, m.cfg); err != nil {
		m.message = err.Error()
	} else if len(m.cfg.EnabledProjects()) == 0 {
		m.message += "; daemon stopped (no enabled projects)"
	}
}
func (m *Model) toggleDaemon() {
	if isRunning() {
		if err := app.Stop(); err != nil {
			m.message = err.Error()
		} else {
			m.message = "daemon stopped"
		}
	} else {
		if err := app.Start(m.binary); err != nil {
			m.message = err.Error()
		} else {
			m.message = "daemon started"
		}
	}
}
func (m *Model) toggleProject() {
	if len(m.cfg.Projects) == 0 {
		return
	}
	m.cfg.Projects[m.selected].Enabled = !m.cfg.Projects[m.selected].Enabled
	running := isRunning()
	if err := config.Save(m.cfg); err != nil {
		m.message = err.Error()
		return
	}
	m.message = "project exposure updated"
	if running || m.cfg.Autostart {
		m.reconcileAfterProjectChange()
	}
}
func (m *Model) setTunnel(provider, command string) {
	m.cfg.Tunnel = config.Tunnel{Provider: provider, Command: command}
	if err := config.Save(m.cfg); err != nil {
		m.message = err.Error()
	} else {
		m.message = "tunnel set to " + provider
		if isRunning() || m.cfg.Autostart {
			if err := app.Reconcile(m.binary, m.cfg); err != nil {
				m.message = err.Error()
			}
		}
	}
	m.screen = dashboard
}
func (m *Model) toggleService() {
	m.cfg.Autostart = !m.cfg.Autostart
	if err := config.Save(m.cfg); err != nil {
		m.message = err.Error()
		return
	}
	if err := app.Reconcile(m.binary, m.cfg); err != nil {
		m.message = err.Error()
		return
	}
	if m.cfg.Autostart {
		m.message = "automatic startup enabled"
		if service.LingerEnabled() {
			m.message += " (boot)"
		} else {
			m.message += " (login; linger unavailable)"
		}
	} else {
		m.message = "automatic startup disabled"
	}
}

func render(w *os.File, m *Model) {
	width, height := terminalSize(w)
	if width < 70 {
		width = 70
	}
	if height < 18 {
		height = 18
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	title := bold + cyan + " cos-lite " + reset + dim + " Ubuntu MCP control plane" + reset
	b.WriteString(title + "\n")
	b.WriteString(strings.Repeat("─", min(width, 110)) + "\n")
	switch m.screen {
	case addProject:
		renderInput(&b, "Add project", "Path", m.input, "Enter add  •  Esc cancel")
	case customTunnel:
		renderInput(&b, "Custom tunnel", "Command", m.input, "Use {local_url} or {origin}. Enter save  •  Esc cancel")
	case openAITunnelID:
		renderInput(&b, "OpenAI Secure MCP Tunnel", "Tunnel ID", m.input, "Expected tunnel_<32 lowercase hex>. Enter continue  •  Esc cancel")
	case openAITunnelKey:
		renderSecretInput(&b, "OpenAI Secure MCP Tunnel", "Runtime API key", m.input, "Stored mode 0600 outside config.json. Enter save  •  Esc cancel")
	case tunnelSetup:
		renderTunnel(&b, m)
	case logsView:
		renderLogs(&b, width, height, m.logsRaw)
	case activityView:
		renderActivity(&b, width, height, m.activityOffset)
	case skillsView:
		renderSkills(&b, m, height)
	case confirmRemove:
		renderConfirm(&b, m)
	default:
		renderDashboard(&b, m, width, height)
	}
	fmt.Fprint(w, b.String())
}
func renderDashboard(b *strings.Builder, m *Model, width, height int) {
	pid, running := control.Running()
	state, _ := control.ReadState()
	status := red + "● stopped" + reset
	if running {
		status = green + "● running" + reset
	}
	fmt.Fprintf(b, "Daemon  %-20s", status)
	if running {
		fmt.Fprintf(b, dim+" pid %d"+reset, pid)
	}
	b.WriteString("\n")
	tunnel := state.Tunnel
	if tunnel == "" {
		tunnel = "disabled"
	}
	tc := dim + tunnel + reset
	if tunnel == "connected" {
		tc = green + "● connected" + reset
	} else if tunnel == "error" {
		tc = red + "● error" + reset
	} else if tunnel == "starting" {
		tc = yellow + "● starting" + reset
	}
	fmt.Fprintf(b, "Tunnel  %-20s", tc)
	if state.PublicURL != "" {
		fmt.Fprintf(b, " %s", state.PublicURL)
	}
	b.WriteString("\n")
	fmt.Fprintf(b, "Browser %s\n", map[bool]string{true: green + "enabled" + reset, false: dim + "disabled" + reset}[m.cfg.Browser])
	if m.cfg.Autostart {
		mode := "login"
		if service.LingerEnabled() {
			mode = "boot"
		}
		state := green + "enabled" + reset
		if !service.Enabled() && len(m.cfg.EnabledProjects()) > 0 {
			state = yellow + "pending" + reset
		}
		fmt.Fprintf(b, "Autostart %s %s(%s)%s\n", state, dim, mode, reset)
	} else {
		b.WriteString("Autostart " + dim + "disabled" + reset + "\n")
	}
	servers := codeintel.AvailableServers()
	ciState := dim + "disabled" + reset
	if m.cfg.CodeIntel.Enabled {
		ciState = green + "enabled" + reset + fmt.Sprintf(dim+" (%d server families found)"+reset, len(servers))
	}
	fmt.Fprintf(b, "CodeIntel %s\n", ciState)
	lib := tuiSkillLibrary(m.cfg)
	skills, _ := lib.List()
	if m.cfg.Skills.Enabled {
		fmt.Fprintf(b, "Skills   %senabled%s %s(%d discovered)%s\n", green, reset, dim, len(skills), reset)
	} else {
		fmt.Fprintf(b, "Skills   %sdisabled%s %s(%d discovered)%s\n", dim, reset, dim, len(skills), reset)
	}
	if events, err := telemetry.ReadActivity(1); err == nil && len(events) > 0 {
		e := events[len(events)-1]
		op := strings.TrimSpace(strings.TrimSpace(e.Method + " " + e.Target))
		fmt.Fprintf(b, "Activity %s%-28s%s %s%.1fms%s\n", cyan, clipText(op, 28), reset, dim, e.DurationMS, reset)
	} else {
		fmt.Fprintf(b, "Activity %sno calls yet%s\n", dim, reset)
	}
	if state.Endpoint != "" {
		fmt.Fprintf(b, dim+"Local MCP %s"+reset+"\n", redactURL(state.Endpoint))
	}
	b.WriteString("\n" + bold + "Exposed projects" + reset + "\n")
	if len(m.cfg.Projects) == 0 {
		b.WriteString(dim + "  No projects yet. Press a to add one." + reset + "\n")
	} else {
		maxRows := height - 14
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.selected >= maxRows {
			start = m.selected - maxRows + 1
		}
		end := min(len(m.cfg.Projects), start+maxRows)
		for i := start; i < end; i++ {
			p := m.cfg.Projects[i]
			plainMark := "○"
			mark := dim + plainMark + reset
			if p.Enabled {
				plainMark = "●"
				mark = green + plainMark + reset
			}
			if _, err := os.Stat(p.Path); err != nil {
				plainMark = "!"
				mark = red + "!" + reset
			}
			plain := fmt.Sprintf(" %s  /%-20s  %s", plainMark, p.Name, shortenHome(p.Path, width-34))
			line := fmt.Sprintf(" %s  /%-20s  %s", mark, p.Name, shortenHome(p.Path, width-34))
			if i == m.selected {
				line = reverse + plain + reset
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n" + dim + "↑/↓ or j/k select   space exposure   a add   d remove   b browser   c code-intel\n" + "s start/stop   r restart   t tunnel   i skills   o activity   l logs   u autostart   q quit TUI" + reset + "\n")
	if m.message != "" {
		b.WriteString("\n" + yellow + m.message + reset + "\n")
	}
}
func renderInput(b *strings.Builder, title, label, value, help string) {
	fmt.Fprintf(b, "\n%s%s%s\n\n%s: %s_\n\n%s%s%s\n", bold, title, reset, label, value, dim, help, reset)
}
func renderSecretInput(b *strings.Builder, title, label, value, help string) {
	mask := ""
	if value != "" {
		mask = strings.Repeat("•", min(24, utf8.RuneCountInString(value)))
	}
	fmt.Fprintf(b, "\n%s%s%s\n\n%s: %s_\n\n%s%s%s\n", bold, title, reset, label, mask, dim, help, reset)
}
func renderTunnel(b *strings.Builder, m *Model) {
	fmt.Fprintf(b, "\n%sTunnel setup%s\n\nCurrent: %s%s%s", bold, reset, cyan, m.cfg.Tunnel.Provider, reset)
	if m.cfg.Tunnel.Provider == "openai" && m.cfg.Tunnel.TunnelID != "" {
		fmt.Fprintf(b, " %s(%s)%s", dim, m.cfg.Tunnel.TunnelID, reset)
	}
	b.WriteString("\n\n  1  Disabled\n  2  OpenAI Secure MCP Tunnel")
	if _, e := tunnelpkg.FindBinary("tunnel-client"); e != nil {
		b.WriteString(dim + "  (tunnel-client not installed)" + reset)
	} else if tunnelpkg.HasOpenAIKey() {
		b.WriteString(green + "  (runtime key configured)" + reset)
	}
	b.WriteString("\n  3  Cloudflare quick tunnel")
	if _, e := tunnelpkg.FindBinary("cloudflared"); e != nil {
		b.WriteString(dim + "  (cloudflared not installed)" + reset)
	}
	b.WriteString("\n  4  Custom command\n  5  Replace OpenAI runtime API key\n\n" + dim + "OpenAI mode keeps the runtime key in ~/.config/cos-lite/openai-tunnel.key (0600).\nCustom commands may use {local_url} and {origin}.\nPress 1/2/3/4/5, or q to go back." + reset + "\n")
}
func renderLogs(b *strings.Builder, width, height int, raw bool) {
	mode := "compact"
	if raw {
		mode = "raw"
	}
	fmt.Fprintf(b, "\n%sDaemon logs%s %s(%s)%s\n\n", bold, reset, dim, mode, reset)
	rows := max(10, height-7)
	if raw {
		text, err := control.TailLog(rows)
		if err != nil {
			b.WriteString(red + err.Error() + reset)
		} else if text == "" {
			b.WriteString(dim + "No logs yet." + reset)
		} else {
			for _, line := range strings.Split(text, "\n") {
				b.WriteString(dim + clipText(line, width-2) + reset + "\n")
			}
		}
	} else {
		entries, err := control.TailLogEntries(rows, false)
		if err != nil {
			b.WriteString(red + err.Error() + reset)
		} else if len(entries) == 0 {
			b.WriteString(dim + "No noteworthy events yet. Press r for raw logs." + reset)
		} else {
			for _, e := range entries {
				ts := "--:--:--"
				if !e.Time.IsZero() {
					ts = e.Time.Format("15:04:05")
				}
				level := strings.ToUpper(e.Level)
				if level == "" {
					level = "INFO"
				}
				levelColor := dim
				switch level {
				case "ERROR", "FATAL":
					levelColor = red
				case "WARN", "WARNING":
					levelColor = yellow
				case "INFO":
					levelColor = green
				}
				component := e.Component
				if component == "" {
					component = "daemon"
				}
				prefixWidth := 8 + 2 + 5 + 2 + 8 + 2
				message := e.Message
				if e.Count > 1 {
					message += fmt.Sprintf("  ×%d", e.Count)
				}
				msg := clipText(message, max(10, width-prefixWidth-2))
				fmt.Fprintf(b, "%s%s%s  %s%-5s%s  %s%-8s%s  %s\n", dim, ts, reset, levelColor, level, reset, cyan, component, reset, msg)
			}
		}
	}
	footer := "[r] raw logs   Enter/q return"
	if raw {
		footer = "[r] compact logs   Enter/q return"
	}
	b.WriteString("\n" + dim + footer + reset + "\n")
}

func renderActivity(b *strings.Builder, width, height, offset int) {
	fmt.Fprintf(b, "\n%sMCP Activity / OpenTelemetry%s\n", bold, reset)
	b.WriteString(dim + "Tool/resource/task activity. Output previews are local-only and are not exported through OTLP.\n" + reset)
	events, err := telemetry.ReadActivity(200)
	if err != nil {
		b.WriteString("\n" + red + err.Error() + reset + "\n")
		return
	}
	if len(events) == 0 {
		b.WriteString("\n" + dim + "No MCP activity yet." + reset + "\n\n" + dim + "Enter/q return" + reset + "\n")
		return
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(events) {
		offset = len(events) - 1
	}
	selected := len(events) - 1 - offset
	listRows := min(10, max(4, (height-13)/2))
	start := selected - listRows + 1
	if start < 0 {
		start = 0
	}
	end := min(len(events), start+listRows)
	if selected >= end {
		start = selected - listRows + 1
		end = selected + 1
	}

	b.WriteString("\n" + dim + " TIME      STATUS  CLIENT             OPERATION                         LATENCY   TRACE" + reset + "\n")
	for i := start; i < end; i++ {
		e := events[i]
		ts := "--:--:--"
		if parsed, parseErr := time.Parse(time.RFC3339Nano, e.Timestamp); parseErr == nil {
			ts = parsed.Local().Format("15:04:05")
		}
		statusText := strings.ToUpper(e.Status)
		statusColor := green
		if e.Status != "ok" {
			statusColor = red
		}
		client := activityClientLabel(e)
		op := strings.TrimSpace(e.Method + " " + e.Target)
		trace := e.TraceID
		if len(trace) > 10 {
			trace = trace[:10]
		}
		plain := fmt.Sprintf(" %-8s  %-6s  %-17s  %-32s  %7.1fms  %s", ts, statusText, clipText(client, 17), clipText(op, 32), e.DurationMS, trace)
		line := fmt.Sprintf(" %s%-8s%s  %s%-6s%s  %-17s  %s%-32s%s  %7.1fms  %s%s%s", dim, ts, reset, statusColor, statusText, reset, clipText(client, 17), cyan, clipText(op, 32), reset, e.DurationMS, dim, trace, reset)
		if i == selected {
			line = reverse + clipText(plain, width-1) + reset
		} else {
			line = clipText(line, width+48)
		}
		b.WriteString(line + "\n")
	}

	e := events[selected]
	b.WriteString("\n" + bold + "Selected event" + reset + "\n")
	fmt.Fprintf(b, "%sOperation:%s %s%s%s    %sLatency:%s %.1fms\n", dim, reset, cyan, strings.TrimSpace(e.Method+" "+e.Target), reset, dim, reset, e.DurationMS)
	fmt.Fprintf(b, "%sClient:%s %s    %sTrace:%s %s    %sSpan:%s %s\n", dim, reset, activityClientLabel(e), dim, reset, e.TraceID, dim, reset, e.SpanID)
	b.WriteString("\n" + bold + "Output preview" + reset + " " + dim + "(local-only, max 1.2k chars)" + reset + "\n")
	preview := strings.TrimSpace(e.OutputPreview)
	if preview == "" {
		b.WriteString(dim + "  No textual output preview (empty/binary result or metadata-only call)." + reset + "\n")
	} else {
		maxLines := max(2, height-listRows-13)
		lines := wrapActivityText(preview, max(20, width-4))
		for i, line := range lines {
			if i >= maxLines {
				b.WriteString(dim + "  …" + reset + "\n")
				break
			}
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n" + dim + "↑/k older   ↓/j newer   g latest   Enter/q return" + reset + "\n")
}

func activityClientLabel(e telemetry.ActivityEvent) string {
	client := strings.TrimSpace(e.ClientName)
	if client == "" || client == "anonymous" {
		client = e.ClientKey
	}
	if strings.Contains(e.ClientKey, "-session-") {
		parts := strings.Split(e.ClientKey, "-session-")
		if len(parts) == 2 && len(parts[1]) >= 6 {
			client += "/" + parts[1][:min(8, len(parts[1]))]
		}
	}
	return client
}

func wrapActivityText(s string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		r := []rune(raw)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

func clipText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
func renderConfirm(b *strings.Builder, m *Model) {
	name := ""
	if len(m.cfg.Projects) > 0 {
		name = m.cfg.Projects[m.selected].Name
	}
	fmt.Fprintf(b, "\n%sRemove /%s?%s\n\nThis removes it from cos-lite config only. Files are never deleted.\n\n%s[y] remove   [n] cancel%s\n", bold, name, reset, dim, reset)
}

func tuiSkillLibrary(cfg config.Config) *skillpkg.Library {
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

func renderSkills(b *strings.Builder, m *Model, height int) {
	fmt.Fprintf(b, "\n%sSkills%s\n\n", bold, reset)
	list, errs := tuiSkillLibrary(m.cfg).List()
	limit := max(3, height-9)
	for i, sk := range list {
		if i >= limit {
			fmt.Fprintf(b, "%s... %d more%s\n", dim, len(list)-i, reset)
			break
		}
		fmt.Fprintf(b, "  %s%-28s%s %s\n", cyan, sk.ID, reset, sk.Description)
	}
	if len(list) == 0 {
		b.WriteString(dim + "No skills discovered. Global: ~/.agents/skills/<id>/SKILL.md; repo: .agents/skills/<id>/SKILL.md" + reset + "\n")
	}
	for _, e := range errs {
		fmt.Fprintf(b, "%swarning: %s%s\n", yellow, e, reset)
	}
	b.WriteString("\n" + dim + "Manage global skills with: cos skill add PATH [ID], cos skill remove ID. Enter/q to return." + reset + "\n")
}

func readKeys(f *os.File, out chan<- key) {
	r := bufio.NewReader(f)
	for {
		c, _, e := r.ReadRune()
		if e != nil {
			return
		}
		switch c {
		case 3:
			out <- key{kind: "ctrlc"}
		case '\r', '\n':
			out <- key{kind: "enter"}
		case 127, 8:
			out <- key{kind: "backspace"}
		case 27:
			// Escape is both a standalone key and the prefix of terminal escape
			// sequences. Do not block waiting forever when the user simply presses Esc.
			if r.Buffered() == 0 && !readableSoon(f, 35*time.Millisecond) {
				out <- key{kind: "esc"}
				continue
			}
			n1, _, e := r.ReadRune()
			if e != nil {
				out <- key{kind: "esc"}
				continue
			}
			if n1 == '[' {
				if r.Buffered() == 0 && !readableSoon(f, 35*time.Millisecond) {
					out <- key{kind: "esc"}
					continue
				}
				n2, _, e := r.ReadRune()
				if e == nil {
					if n2 == 'A' {
						out <- key{kind: "up"}
					} else if n2 == 'B' {
						out <- key{kind: "down"}
					} else {
						out <- key{kind: "esc"}
					}
				}
			} else {
				out <- key{kind: "esc"}
			}
		default:
			out <- key{kind: "rune", r: c}
		}
	}
}

func readableSoon(f *os.File, timeout time.Duration) bool {
	fd := int(f.Fd())
	var set syscall.FdSet
	word := fd / 64
	if word < 0 || word >= len(set.Bits) {
		return false
	}
	set.Bits[word] |= 1 << uint(fd%64)
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	n, err := syscall.Select(fd+1, &set, nil, nil, &tv)
	return err == nil && n > 0
}

func rawMode(f *os.File) (func(), error) {
	fd := f.Fd()
	var old syscall.Termios
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0); e != 0 {
		return nil, e
	}
	raw := old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw)), 0, 0, 0); e != 0 {
		return nil, e
	}
	return func() {
		_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0)
	}, nil
}

type winsize struct{ Row, Col, X, Y uint16 }

func terminalSize(f *os.File) (int, int) {
	var ws winsize
	_, _, e := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)), 0, 0, 0)
	if e != 0 || ws.Col == 0 {
		return 100, 30
	}
	return int(ws.Col), int(ws.Row)
}
func shortenHome(p string, maxLen int) string {
	if h, e := os.UserHomeDir(); e == nil && strings.HasPrefix(p, h) {
		p = "~" + strings.TrimPrefix(p, h)
	}
	if maxLen > 8 && len(p) > maxLen {
		return "…" + p[len(p)-maxLen+1:]
	}
	return p
}
func redactURL(v string) string {
	idx := strings.LastIndex(v, "/")
	if idx < 0 {
		return v
	}
	return v[:idx+1] + "********"
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
