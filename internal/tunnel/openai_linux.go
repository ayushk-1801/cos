//go:build linux

package tunnel

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func CountTunnelIDProcesses(tunnelID string) (int, error) {
	if tunnelID == "" {
		return 0, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(args) > 0 && strings.Contains(filepath.Base(args[0]), "tunnel-client") && tunnelIDFromArgv(args) == tunnelID {
			count++
		}
	}
	return count, nil
}
