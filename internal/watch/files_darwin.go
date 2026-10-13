//go:build darwin

package watch

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Files watches named files, including atomic replacements. A small periodic
// digest avoids coupling to Linux inotify and also works across macOS volumes.
// The daemon additionally performs a 30-second checksum safety check.
func Files(ctx context.Context, paths ...string) (<-chan struct{}, error) {
	var names []string
	known := map[string]bool{}
	for _, name := range paths {
		if name == "" {
			continue
		}
		name = filepath.Clean(name)
		if !known[name] {
			names = append(names, name)
			known[name] = true
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no files to watch")
	}
	previous := make(map[string][32]byte, len(names))
	for _, name := range names {
		previous[name] = digest(name)
	}
	output := make(chan struct{}, 1)
	go func() {
		defer close(output)
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				changed := false
				for _, name := range names {
					current := digest(name)
					if current != previous[name] {
						previous[name] = current
						changed = true
					}
				}
				if changed {
					select {
					case output <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return output, nil
}
func digest(name string) [32]byte {
	bytes, err := os.ReadFile(name)
	if err != nil {
		return sha256.Sum256([]byte("missing:" + err.Error()))
	}
	return sha256.Sum256(bytes)
}
