//go:build !linux

package tui

import "fmt"

func Run(binary string) error { return fmt.Errorf("the cos-lite TUI currently supports Linux only") }
