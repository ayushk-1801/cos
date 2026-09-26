package tunnel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ayush/cos-lite/internal/config"
)

func FindTunnelIDCollision(tunnelID string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	self := os.Getpid()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(parts) == 0 || !strings.Contains(filepath.Base(parts[0]), "tunnel-client") {
			continue
		}
		if tunnelIDFromArgv(parts) == tunnelID {
			// Ignore a child left over from this daemon's immediately preceding
			// manager instance; Stop() has already signaled it and it may take a
			// moment to disappear from /proc.
			if ppid, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat")); e == nil {
				fields := strings.Fields(string(ppid))
				if len(fields) > 3 {
					if parent, _ := strconv.Atoi(fields[3]); parent == self {
						continue
					}
				}
			}
			return pid, nil
		}
	}
	return 0, nil
}

func tunnelIDFromArgv(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--control-plane.tunnel-id=") {
			return strings.TrimPrefix(arg, "--control-plane.tunnel-id=")
		}
		if arg == "--control-plane.tunnel-id" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func FindBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exec.ErrNotFound
	}
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
	} {
		candidate := filepath.Join(dir, name)
		st, statErr := os.Stat(candidate)
		if statErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func OpenAIKeyPath() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "openai-tunnel.key"), nil
}

func WriteOpenAIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("OpenAI tunnel runtime API key is empty")
	}
	if strings.ContainsAny(key, "\r\n") {
		return errors.New("OpenAI tunnel runtime API key contains a newline")
	}
	d, err := config.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p := filepath.Join(d, "openai-tunnel.key")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func HasOpenAIKey() bool {
	p, err := OpenAIKeyPath()
	if err != nil {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Size() > 1
}

func ValidateOpenAIKeyFile() (string, error) {
	p, err := OpenAIKeyPath()
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("OpenAI tunnel runtime API key is not configured; open `cos`, press t, and choose OpenAI Secure MCP Tunnel")
		}
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("OpenAI tunnel key path is not a regular file")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("OpenAI tunnel key file %s is too permissive; chmod 600 it", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", errors.New("OpenAI tunnel runtime API key file is empty")
	}
	return p, nil
}
