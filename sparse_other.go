//go:build !windows

package main

import "os"

// makeSparse is a no-op: Linux and macOS file systems leave unwritten
// regions of a truncated file as holes already.
func makeSparse(*os.File) {}
