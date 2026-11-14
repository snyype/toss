package main

import (
	"os"
	"regexp"
	"unicode/utf8"
)

var useColor = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return stdoutIsTerminal() && enableVirtualTerminal()
}()

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func stdoutIsTerminal() bool { return isTerminal(os.Stdout) }
func stdinIsTerminal() bool  { return isTerminal(os.Stdin) }

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return paint("1", s) }
func dim(s string) string    { return paint("2", s) }
func red(s string) string    { return paint("31", s) }
func green(s string) string  { return paint("32", s) }
func yellow(s string) string { return paint("33", s) }
func cyan(s string) string   { return paint("36", s) }

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
func visibleLen(s string) int   { return utf8.RuneCountInString(stripANSI(s)) }
