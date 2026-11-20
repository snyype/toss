//go:build linux || darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// termWidth returns the terminal's column count, or 0 if unknown.
func termWidth() int {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}
