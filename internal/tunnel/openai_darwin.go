//go:build darwin

package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type tunnelProc struct {
	pid, ppid int
	argv      []string
}

func tunnelProcesses() ([]tunnelProc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate tunnel processes: %w", err)
	}
	var result []tunnelProc
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(parts[0])
		ppid, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil || pid == os.Getpid() {
			continue
		}
		argv := parts[2:]
		if !strings.Contains(filepath.Base(argv[0]), "tunnel-client") {
			continue
		}
		result = append(result, tunnelProc{pid, ppid, argv})
	}
	return result, nil
}
func FindTunnelIDCollision(tunnelID string) (int, error) {
	procs, err := tunnelProcesses()
	if err != nil {
		return 0, err
	}
	for _, p := range procs {
		if p.ppid != os.Getpid() && tunnelIDFromArgv(p.argv) == tunnelID && tunnelID != "" {
			return p.pid, nil
		}
	}
	return 0, nil
}
func CountTunnelIDProcesses(tunnelID string) (int, error) {
	procs, err := tunnelProcesses()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, p := range procs {
		if tunnelID != "" && tunnelIDFromArgv(p.argv) == tunnelID {
			count++
		}
	}
	return count, nil
}
