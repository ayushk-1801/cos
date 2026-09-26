package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

const unitName = "cos-lite.service"

func unitPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "systemd", "user", unitName), nil
}
func Available() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}
func Enabled() bool {
	if !Available() {
		return false
	}
	return exec.Command("systemctl", "--user", "is-enabled", "--quiet", unitName).Run() == nil
}
func Active() bool {
	if !Available() {
		return false
	}
	return exec.Command("systemctl", "--user", "is-active", "--quiet", unitName).Run() == nil
}

func Install(binary string) error {
	if !Available() {
		return fmt.Errorf("systemctl not found")
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	p, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`[Unit]
Description=cos-lite local MCP daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="%s" daemon
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, systemdQuote(abs))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Best effort: lingering makes a user service start at boot even before the first
	// interactive login. Some distributions require polkit/admin approval, so failure
	// here must not break ordinary login-time autostart.
	if !LingerEnabled() {
		_ = EnableLinger()
	}
	if out, err := exec.Command("systemctl", "--user", "restart", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart after enable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func Remove() error {
	if !Available() {
		return fmt.Errorf("systemctl not found")
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	p, err := unitPath()
	if err != nil {
		return err
	}
	_ = os.Remove(p)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}
func Restart() error {
	out, err := exec.Command("systemctl", "--user", "restart", unitName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func Stop() error {
	out, err := exec.Command("systemctl", "--user", "stop", unitName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "%", "%%")
}

func LingerEnabled() bool {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return false
	}
	u, err := user.Current()
	if err != nil {
		return false
	}
	out, err := exec.Command("loginctl", "show-user", u.Username, "-p", "Linger", "--value").Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

func EnableLinger() error {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	out, err := exec.Command("loginctl", "enable-linger", u.Username).CombinedOutput()
	if err != nil {
		return fmt.Errorf("loginctl enable-linger: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
