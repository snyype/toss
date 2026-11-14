package main

import (
	"os"
	"syscall"
	"unsafe"
)

// enableVirtualTerminal turns on ANSI color support in the Windows console
// (needed for the classic console host; Windows Terminal already has it).
func enableVirtualTerminal() bool {
	const enableVTProcessing = 0x0004
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")

	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	if mode&enableVTProcessing != 0 {
		return true
	}
	r, _, _ := setMode.Call(uintptr(h), uintptr(mode|enableVTProcessing))
	return r != 0
}
