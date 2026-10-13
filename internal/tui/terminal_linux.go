//go:build linux

package tui

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

func readableSoon(f *os.File, timeout time.Duration) bool {
	fd := int(f.Fd())
	var set syscall.FdSet
	word := fd / 64
	if word < 0 || word >= len(set.Bits) {
		return false
	}
	set.Bits[word] |= 1 << uint(fd%64)
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	n, err := syscall.Select(fd+1, &set, nil, nil, &tv)
	return err == nil && n > 0
}

func rawMode(f *os.File) (func(), error) {
	fd := f.Fd()
	var old syscall.Termios
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0); e != 0 {
		return nil, e
	}
	raw := old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw)), 0, 0, 0); e != 0 {
		return nil, e
	}
	return func() {
		_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0)
	}, nil
}

type winsize struct{ Row, Col, X, Y uint16 }

func terminalSize(f *os.File) (int, int) {
	var ws winsize
	_, _, e := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)), 0, 0, 0)
	if e != 0 || ws.Col == 0 {
		return 100, 30
	}
	return int(ws.Col), int(ws.Row)
}
