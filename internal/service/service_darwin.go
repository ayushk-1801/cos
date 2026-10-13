//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const agentLabel = "ai.cos-lite.daemon"

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func target() string { return domain() + "/" + agentLabel }

func Available() bool {
	if os.Getenv("COS_DISABLE_LAUNCHD") == "1" || os.Getenv("COS_DISABLE_SYSTEMD") == "1" {
		return false
	}
	if _, err := exec.LookPath("launchctl"); err != nil {
		return false
	}
	return exec.Command("launchctl", "print", domain()).Run() == nil
}

func Enabled() bool {
	p, err := agentPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func loaded() bool { return exec.Command("launchctl", "print", target()).Run() == nil }

func Active() bool {
	if !Available() {
		return false
	}
	out, err := exec.Command("launchctl", "print", target()).Output()
	return err == nil && strings.Contains(string(out), "state = running")
}

func launch(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func Install(binary string) error {
	if !Available() {
		return fmt.Errorf("macOS GUI launchd user domain is unavailable")
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	p, err := agentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	// launchd does not inherit Terminal/Homebrew's PATH at login.
	searchPath := strings.Join([]string{
		filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".cargo", "bin"), "/opt/homebrew/bin",
		"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}, ":")
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>/dev/null</string>
<key>StandardErrorPath</key><string>/dev/null</string>
</dict></plist>
`, agentLabel, escapeXML(abs), escapeXML(searchPath))
	if loaded() {
		if err := launch("bootout", target()); err != nil {
			return err
		}
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		return err
	}
	if err := launch("enable", target()); err != nil {
		return err
	}
	return Start()
}

func Start() error {
	if !Available() {
		return fmt.Errorf("macOS GUI launchd user domain is unavailable")
	}
	if !Enabled() {
		return fmt.Errorf("cos-lite LaunchAgent has not been installed")
	}
	if !loaded() {
		p, _ := agentPath()
		if err := launch("bootstrap", domain(), p); err != nil {
			return err
		}
	}
	return launch("kickstart", "-k", target())
}

func Remove() error {
	if loaded() {
		if err := launch("bootout", target()); err != nil {
			return err
		}
	}
	p, err := agentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Stop() error {
	if !loaded() {
		return nil
	}
	return launch("bootout", target())
}

func Restart() error { return Start() }

// Unlike Linux lingering, macOS user LaunchAgents start at login, not at boot.
func LingerEnabled() bool { return false }
func EnableLinger() error {
	return fmt.Errorf("launchd user agents start at login; Linux lingering is not available on macOS")
}
