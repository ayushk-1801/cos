//go:build linux

package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// Files emits a coalesced notification whenever one of the named files is
// created, replaced, removed, chmodded, or closed after a write. It watches the
// parent directories rather than the files themselves so atomic rename based
// saves continue to work.
func Files(ctx context.Context, paths ...string) (<-chan struct{}, error) {
	targets := map[string]map[string]struct{}{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		dir := filepath.Dir(p)
		name := filepath.Base(p)
		if targets[dir] == nil {
			targets[dir] = map[string]struct{}{}
		}
		targets[dir][name] = struct{}{}
	}
	if len(targets) == 0 {
		return nil, errors.New("no files to watch")
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	wdTargets := map[int]map[string]struct{}{}
	for dir, names := range targets {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_ = syscall.Close(fd)
			return nil, err
		}
		wd, err := syscall.InotifyAddWatch(fd, dir, syscall.IN_CLOSE_WRITE|syscall.IN_MOVED_TO|syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_ATTRIB)
		if err != nil {
			_ = syscall.Close(fd)
			return nil, err
		}
		wdTargets[wd] = names
	}
	out := make(chan struct{}, 1)
	var once sync.Once
	closeFD := func() { once.Do(func() { _ = syscall.Close(fd) }) }
	go func() {
		<-ctx.Done()
		closeFD()
	}()
	go func() {
		defer close(out)
		defer closeFD()
		buf := make([]byte, 16*1024)
		for {
			n, err := syscall.Read(fd, buf)
			if err != nil {
				if ctx.Err() != nil || err == syscall.EBADF || err == syscall.EINVAL {
					return
				}
				if err == syscall.EINTR {
					continue
				}
				return
			}
			for off := 0; off+syscall.SizeofInotifyEvent <= n; {
				ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
				sz := syscall.SizeofInotifyEvent + int(ev.Len)
				if sz <= 0 || off+sz > n {
					break
				}
				nameBytes := buf[off+syscall.SizeofInotifyEvent : off+sz]
				end := 0
				for end < len(nameBytes) && nameBytes[end] != 0 {
					end++
				}
				name := string(nameBytes[:end])
				if names := wdTargets[int(ev.Wd)]; names != nil {
					if _, ok := names[name]; ok {
						select {
						case out <- struct{}{}:
						default:
						}
					}
				}
				off += sz
			}
		}
	}()
	return out, nil
}
