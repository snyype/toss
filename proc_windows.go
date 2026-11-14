package main

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this pid is still running.
func processAlive(pid int) bool {
	const queryLimitedInfo = 0x1000
	const stillActive = 259
	h, err := syscall.OpenProcess(queryLimitedInfo, false, uint32(pid))
	if err != nil {
		// Access denied means it exists but belongs to someone else.
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
