//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether a process with this pid is still running.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
