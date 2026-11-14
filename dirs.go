package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// appDirCandidates lists where toss keeps a working folder named sub, in order
// of preference: next to the toss executable (e.g. C:\toss\temp), then the
// user config dir (e.g. %AppData%\toss\temp) for read-only installs.
func appDirCandidates(sub string) []string {
	var candidates []string
	if exe, err := currentExecutable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), sub))
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(cfg, "toss", sub))
	}
	return candidates
}

// appDir returns the first candidate folder for sub that exists (or can be
// created) and can be written to.
func appDir(sub string) (string, error) {
	var lastErr error
	for _, dir := range appDirCandidates(sub) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}
		probe, err := os.CreateTemp(dir, ".write-test-*")
		if err != nil {
			lastErr = err
			continue
		}
		probe.Close()
		os.Remove(probe.Name())
		return dir, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no location available")
	}
	return "", lastErr
}
