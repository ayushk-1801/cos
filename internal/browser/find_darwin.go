//go:build darwin

package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func FindBrowserExecutable() (string, error) {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		filepath.Join(home, "Applications/Chromium.app/Contents/MacOS/Chromium"),
		"/opt/homebrew/bin/chromium", "/usr/local/bin/chromium",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("Chrome/Chromium not found in Applications or PATH")
}
