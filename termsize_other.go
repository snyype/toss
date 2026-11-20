//go:build !windows && !linux && !darwin

package main

func termWidth() int { return 0 }
