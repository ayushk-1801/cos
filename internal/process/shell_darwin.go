//go:build darwin

package process

import (
	"fmt"
	"os/exec"
)

func shellCommand(command string, tty bool) (*exec.Cmd, error) {
	if !tty {
		return exec.Command("/bin/bash", "-lc", command), nil
	}
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("tty requested but macOS script utility is unavailable: %w", err)
	}
	// macOS/BSD script takes the output filename before the shell command, unlike util-linux.
	// -e propagates the child exit status.
	return exec.Command("script", "-q", "-e", "/dev/null", "/bin/bash", "-lc", command), nil
}
