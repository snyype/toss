package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const logFileName = "toss.log"

// logEntry is one line (JSON) in the log file.
type logEntry struct {
	Time     time.Time `json:"time"`
	Event    string    `json:"event"` // share | download | pull | end
	File     string    `json:"file"`
	Path     string    `json:"path,omitempty"`
	Client   string    `json:"client,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	Status   string    `json:"status,omitempty"`
	URL      string    `json:"url,omitempty"`
	Duration int       `json:"duration,omitempty"` // share length in seconds
	Elapsed  string    `json:"elapsed,omitempty"`  // download time
	Count    int64     `json:"count,omitempty"`    // completed downloads at end
}

type logger struct {
	mu   sync.Mutex
	file string // empty when logging is unavailable
}

// logDir returns <toss install dir>/logs, i.e. a "logs" folder next to the
// toss executable. If that folder cannot be written (e.g. toss was installed
// into a read-only location), it falls back to the user config dir.
func logDir() (string, error) {
	var lastErr error
	for _, dir := range appDirCandidates("logs") {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			lastErr = err
			continue
		}
		f.Close()
		return dir, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no location available")
	}
	return "", lastErr
}

func openLogger() *logger {
	dir, err := logDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: activity log disabled:", err)
		return &logger{}
	}
	return &logger{file: filepath.Join(dir, logFileName)}
}

func (l *logger) write(e logEntry) {
	if l.file == "" {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not write log:", err)
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// runLogs implements `toss logs`.
func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	n := fs.Int("n", 20, "number of recent entries to show (0 = all)")
	showPath := fs.Bool("path", false, "print the log file location and exit")
	clear := fs.Bool("clear", false, "delete all log entries")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: toss logs [-n count] [-path] [-clear]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := logDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot open log folder:", err)
		return 1
	}
	file := filepath.Join(dir, logFileName)

	if *showPath {
		fmt.Println(file)
		return 0
	}
	if *clear {
		if err := os.Truncate(file, 0); err != nil {
			fmt.Fprintln(os.Stderr, "Error: cannot clear log:", err)
			return 1
		}
		fmt.Println("Log cleared.")
		return 0
	}

	entries, err := readEntries(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot read log:", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Println("No activity yet. Logs are stored in", file)
		return 0
	}
	if *n > 0 && len(entries) > *n {
		entries = entries[len(entries)-*n:]
	}

	fmt.Printf("%-19s  %-8s  %-28s  %-15s  %-10s  %s\n", "TIME", "EVENT", "FILE", "CLIENT", "SIZE", "DETAILS")
	for _, e := range entries {
		size, details := "", ""
		switch e.Event {
		case "share":
			size = humanBytes(e.Bytes)
			details = fmt.Sprintf("for %ds  %s", e.Duration, e.URL)
		case "download":
			size = humanBytes(e.Bytes)
			details = e.Status
			if e.Elapsed != "" {
				details += " in " + e.Elapsed
			}
		case "pull":
			size = humanBytes(e.Bytes)
			details = e.Status
			if e.Elapsed != "" {
				details += " in " + e.Elapsed
			}
			if e.Path != "" {
				details += "  " + e.Path
			} else if e.URL != "" {
				details += "  " + e.URL
			}
		case "end":
			details = fmt.Sprintf("%s, %d download(s)", e.Status, e.Count)
		}
		fmt.Printf("%-19s  %-8s  %-28s  %-15s  %-10s  %s\n",
			e.Time.Local().Format("2006-01-02 15:04:05"), e.Event, truncate(e.File, 28), e.Client, size, details)
	}
	fmt.Printf("\nLog file: %s\n", file)
	return 0
}

func readEntries(file string) ([]logEntry, error) {
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []logEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e logEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries, sc.Err()
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
