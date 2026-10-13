//go:build linux

package process

import (
	"fmt"
	"os/exec"
)

func shellCommand(command string, tty bool) (*exec.Cmd, error) {
	if !tty {
		return exec.Command("bash", "-lc", command), nil
	}
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("tty requested but util-linux script is unavailable: %w", err)
	}
	return exec.Command("script", "-qefc", command, "/dev/null"), nil
}
