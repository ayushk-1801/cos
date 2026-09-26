//go:build linux

package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
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
	customTunnel
	logsView
	confirmRemove
	skillsView
)

type Model struct {
	cfg      config.Config
	selected int
	screen   screen
	input    string
	message  string
	binary   string
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
	if m.screen == addProject || m.screen == customTunnel {
		return m.handleInput(k)
	}
	switch m.screen {
	case logsView, skillsView:
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
				m.setTunnel("cloudflare", "")
			case '3':
				m.screen = customTunnel
				m.input = m.cfg.Tunnel.Command
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
		} else {
			m.setTunnel("custom", strings.TrimSpace(m.input))
		}
	case "rune":
		if k.r >= 32 && k.r != 127 {
			m.input += string(k.r)
		}
	}
	return false
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
	case tunnelSetup:
		renderTunnel(&b, m)
	case logsView:
		renderLogs(&b, height)
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
	if state.Endpoint != "" {
		fmt.Fprintf(b, dim+"Local MCP %s"+reset+"\n", redactURL(state.Endpoint))
	}
	b.WriteString("\n" + bold + "Exposed projects" + reset + "\n")
	if len(m.cfg.Projects) == 0 {
		b.WriteString(dim + "  No projects yet. Press a to add one." + reset + "\n")
	} else {
		maxRows := height - 13
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
	b.WriteString("\n" + dim + "↑/↓ or j/k select   space exposure   a add   d remove   b browser   c code-intel\n" + "s start/stop   r restart   t tunnel   i skills   l logs   u autostart   q quit TUI" + reset + "\n")
	if m.message != "" {
		b.WriteString("\n" + yellow + m.message + reset + "\n")
	}
}
func renderInput(b *strings.Builder, title, label, value, help string) {
	fmt.Fprintf(b, "\n%s%s%s\n\n%s: %s_\n\n%s%s%s\n", bold, title, reset, label, value, dim, help, reset)
}
func renderTunnel(b *strings.Builder, m *Model) {
	fmt.Fprintf(b, "\n%sTunnel setup%s\n\nCurrent: %s%s%s\n\n  1  Disabled\n  2  Cloudflare quick tunnel", bold, reset, cyan, m.cfg.Tunnel.Provider, reset)
	if _, e := exec.LookPath("cloudflared"); e != nil {
		b.WriteString(dim + "  (cloudflared not installed)" + reset)
	}
	b.WriteString("\n  3  Custom command\n\n" + dim + "Custom commands may use {local_url} (tokenized MCP endpoint) and {origin}.\nPress 1/2/3, or q to go back." + reset + "\n")
}
func renderLogs(b *strings.Builder, height int) {
	text, err := control.TailLog(max(10, height-7))
	fmt.Fprintf(b, "\n%sDaemon logs%s\n\n", bold, reset)
	if err != nil {
		b.WriteString(red + err.Error() + reset)
	} else if text == "" {
		b.WriteString(dim + "No logs yet." + reset)
	} else {
		b.WriteString(text)
	}
	b.WriteString("\n\n" + dim + "Enter/q to return" + reset + "\n")
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
