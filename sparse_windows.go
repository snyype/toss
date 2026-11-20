package main

import (
	"os"
	"syscall"
)

const fsctlSetSparse = 0x000900c4

// makeSparse marks f as a sparse file. Without it NTFS zero-fills everything
// up to the offset of a write, so the last range of a parallel download would
// stall while gigabytes of zeros are written first.
func makeSparse(f *os.File) {
	var n uint32
	_ = syscall.DeviceIoControl(syscall.Handle(f.Fd()), fsctlSetSparse, nil, 0, nil, 0, &n, nil)
}
