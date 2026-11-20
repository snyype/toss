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

// termWidth returns the console window's column count, or 0 if unknown.
func termWidth() int {
	type coord struct{ X, Y int16 }
	var info struct {
		Size, Cursor             coord
		Attributes               uint16
		Left, Top, Right, Bottom int16
		MaxWindowSize            coord
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")
	if r, _, _ := proc.Call(uintptr(syscall.Handle(os.Stdout.Fd())), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0
	}
	return int(info.Right-info.Left) + 1
}
