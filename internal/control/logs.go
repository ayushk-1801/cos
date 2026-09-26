package control

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	logTailBytes = 2 * 1024 * 1024
	logRotateAt  = 5 * 1024 * 1024
)

type LogEntry struct {
	Time      time.Time
	Level     string
	Component string
	Message   string
	Raw       string
	Verbose   bool
	Count     int
}

var logPrefixRE = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+(.*)$`)
var fieldRE = regexp.MustCompile(`(?:^|\s)([A-Za-z_][A-Za-z0-9_.-]*)=("(?:[^"\\]|\\.)*"|[^\s]+)`)

func RotateLogIfNeeded() error {
	p, err := LogPath()
	if err != nil {
		return err
	}
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Size() < logRotateAt {
		return nil
	}
	backup := p + ".1"
	_ = os.Remove(backup)
	return os.Rename(p, backup)
}

func TailLogEntries(limit int, includeVerbose bool) ([]LogEntry, error) {
	raw, err := tailLogBytes(logTailBytes)
	if err != nil || raw == "" {
		return nil, err
	}
	entries := ParseLogEntries(raw, includeVerbose)
	if !includeVerbose {
		entries = coalesceLogEntries(entries, 2*time.Minute)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func ParseLogEntries(raw string, includeVerbose bool) []LogEntry {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	out := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := parseLogLine(line)
		if e.Verbose && !includeVerbose {
			continue
		}
		out = append(out, e)
	}
	return out
}

func FormatLogEntry(e LogEntry) string {
	ts := "--:--:--"
	if !e.Time.IsZero() {
		ts = e.Time.Format("15:04:05")
	}
	level := strings.ToUpper(e.Level)
	if level == "" {
		level = "INFO"
	}
	component := e.Component
	if component == "" {
		component = "daemon"
	}
	suffix := ""
	if e.Count > 1 {
		suffix = fmt.Sprintf("  ×%d", e.Count)
	}
	return fmt.Sprintf("%s  %-5s  %-8s  %s%s", ts, level, component, e.Message, suffix)
}

func parseLogLine(line string) LogEntry {
	e := LogEntry{Raw: line, Level: "INFO", Component: "daemon", Count: 1}
	rest := line
	if m := logPrefixRE.FindStringSubmatch(line); len(m) == 3 {
		if ts, err := time.Parse("2006/01/02 15:04:05.999999", padMicros(m[1])); err == nil {
			e.Time = ts
		}
		rest = m[2]
	}

	if strings.HasPrefix(rest, "tunnel: ") {
		e.Component = "tunnel"
		fields := parseStructuredFields(strings.TrimPrefix(rest, "tunnel: "))
		if v := fields["level"]; v != "" {
			e.Level = strings.ToUpper(v)
		}
		if v := fields["component"]; v != "" {
			e.Component = shortComponent(v)
		}
		msg := fields["msg"]
		if msg == "" {
			msg = "tunnel event"
		}
		e.Message, e.Verbose = normalizeTunnelMessage(msg, fields)
		if e.Level == "INFO" && e.Message != "OpenAI tunnel connected" {
			e.Verbose = true
		}
		return e
	}

	switch {
	case strings.HasPrefix(rest, "cos-lite ") && strings.Contains(rest, " started;"):
		e.Component = "daemon"
		e.Message = normalizeDaemonStarted(rest)
	case strings.HasPrefix(rest, "tunnel start failed:"):
		e.Component = "tunnel"
		e.Level = "ERROR"
		e.Message = strings.TrimSpace(strings.TrimPrefix(rest, "tunnel start failed:"))
	case strings.HasPrefix(rest, "tunnel collision:"):
		e.Component = "tunnel"
		e.Level = "ERROR"
		e.Message = strings.TrimSpace(strings.TrimPrefix(rest, "tunnel collision:"))
	case strings.Contains(strings.ToLower(rest), " failed:"):
		e.Level = "ERROR"
		e.Message = rest
	default:
		e.Message = rest
	}
	return e
}

func coalesceLogEntries(entries []LogEntry, window time.Duration) []LogEntry {
	out := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		key := e.Level + "\x00" + e.Component + "\x00" + e.Message
		match := -1
		for i := len(out) - 1; i >= 0; i-- {
			candidate := out[i]
			if candidate.Level+"\x00"+candidate.Component+"\x00"+candidate.Message != key {
				continue
			}
			if !candidate.Time.IsZero() && !e.Time.IsZero() && e.Time.Sub(candidate.Time) > window {
				break
			}
			match = i
			break
		}
		if match < 0 {
			out = append(out, e)
			continue
		}
		merged := out[match]
		if merged.Count < 1 {
			merged.Count = 1
		}
		merged.Count++
		merged.Time = e.Time
		merged.Raw = e.Raw
		copy(out[match:], out[match+1:])
		out[len(out)-1] = merged
	}
	return out
}

func parseStructuredFields(s string) map[string]string {
	out := map[string]string{}
	for _, m := range fieldRE.FindAllStringSubmatch(s, -1) {
		v := m[2]
		if strings.HasPrefix(v, "\"") {
			if unquoted, err := strconv.Unquote(v); err == nil {
				v = unquoted
			}
		}
		out[m[1]] = v
	}
	return out
}

func normalizeTunnelMessage(msg string, fields map[string]string) (string, bool) {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "tunnel-client started"):
		return "OpenAI tunnel connected", false
	case lower == "run":
		return msg, true
	case strings.Contains(lower, "admin ui enabled"), strings.Contains(lower, "web ui"):
		return msg, true
	case strings.Contains(lower, "harpoon enabled"):
		return msg, true
	case strings.Contains(lower, "probing mcp server"):
		return msg, true
	case strings.Contains(lower, "startup summary"):
		return msg, true
	case strings.Contains(lower, "codex detected without tunnel mcp plugin"):
		return msg, true
	case strings.Contains(lower, "mcp session initialized"):
		return "MCP session initialized", true
	case strings.Contains(lower, "oauth discovery failed"):
		return "OAuth discovery skipped for local MCP endpoint", true
	case strings.Contains(lower, "health url"), strings.Contains(lower, "health server listening"):
		return "Local tunnel health endpoint ready", true
	case strings.Contains(lower, "onstart hook"), strings.Contains(lower, "onstop hook"):
		return msg, true
	case strings.HasPrefix(lower, "invoking"):
		return msg, true
	case strings.Contains(lower, "tls trust summary"), strings.Contains(lower, "catalog digest"):
		return msg, true
	case strings.Contains(lower, "dispatcher forwarded command"):
		return "Forwarded MCP request", true
	case strings.Contains(lower, "tunnel metadata fetched"), strings.Contains(lower, "poller started"), strings.Contains(lower, "starting control-plane poller"):
		return msg, true
	}
	if fields["component"] == "" && strings.Contains(lower, "tunnel") {
		return msg, false
	}
	return msg, false
}

func normalizeDaemonStarted(rest string) string {
	parts := strings.Split(rest, ";")
	version := ""
	if first := strings.Fields(parts[0]); len(first) >= 2 {
		version = first[1]
	}
	projects := ""
	endpoint := ""
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "projects=") {
			projects = strings.TrimPrefix(p, "projects=")
		}
		if strings.HasPrefix(p, "endpoint=") {
			endpoint = strings.TrimPrefix(p, "endpoint=")
		}
	}
	count := 0
	if strings.TrimSpace(projects) != "" {
		count = len(strings.Split(projects, ","))
	}
	msg := "Started"
	if version != "" {
		msg += " v" + strings.TrimPrefix(version, "v")
	}
	if count > 0 {
		msg += fmt.Sprintf(" · %d project", count)
		if count != 1 {
			msg += "s"
		}
	}
	if endpoint != "" {
		if i := strings.Index(endpoint, "/mcp/"); i >= 0 {
			endpoint = endpoint[:i] + "/mcp/********"
		}
		msg += " · " + endpoint
	}
	return msg
}

func shortComponent(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		return v
	}
	switch v {
	case "controlplane":
		return "control"
	case "runtimehealth":
		return "health"
	}
	return v[:8]
}

func padMicros(s string) string {
	if !strings.Contains(s, ".") {
		return s + ".000000"
	}
	parts := strings.SplitN(s, ".", 2)
	frac := parts[1]
	if len(frac) < 6 {
		frac += strings.Repeat("0", 6-len(frac))
	} else if len(frac) > 6 {
		frac = frac[:6]
	}
	return parts[0] + "." + frac
}

func tailLogBytes(maxBytes int64) (string, error) {
	p, err := LogPath()
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if maxBytes > 0 && st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if start > 0 {
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b), nil
}
