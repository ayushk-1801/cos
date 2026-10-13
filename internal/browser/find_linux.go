//go:build linux

package browser

import (
	"fmt"
	"os/exec"
)

func FindBrowserExecutable() (string, error) {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Chrome/Chromium not found (install chromium)")
}
