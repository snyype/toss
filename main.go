package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var version = "2.2.1"

// share describes the single file a toss process is serving.
type share struct {
	path       string // absolute path on disk
	name       string // name exposed in the URL
	size       int64
	expiresAt  time.Time
	urlPath    string // decoded path the handler answers on
	active     sync.WaitGroup
	downloads  atomic.Int64
	inProgress atomic.Int64
	track      *tracker
	out        *console
}

func main() {
	cleanupOldBinary()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "logs":
			os.Exit(runLogs(os.Args[2:]))
		case "update":
			os.Exit(runUpdate(os.Args[2:]))
		case "pull", "stream":
			os.Exit(runPull(os.Args[1], os.Args[2:]))
		}
	}
	os.Exit(runShare())
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "toss %s - share a file over your local network\n\n", version)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  toss [-t seconds] [-p port] [-q] [-y] <file|folder>  share a file (folders are zipped)")
	fmt.Fprintln(out, "  toss pull <link>                     download a shared file into toss's downloads folder")
	fmt.Fprintln(out, "  toss stream <link>                   same as toss pull")
	fmt.Fprintln(out, "  toss logs [-n count] [-path] [-clear] show recent shares and downloads")
	fmt.Fprintln(out, "  toss update [-check] [-y]            update toss to the latest release")
	fmt.Fprintln(out, "  toss -v                              show version")
	fmt.Fprintln(out, "\nOptions:")
	flag.PrintDefaults()
}

func runShare() int {
	var (
		duration    int
		port        int
		showVersion bool
		quiet       bool
		assumeYes   bool
	)
	flag.IntVar(&duration, "t", 300, "how long the link stays valid, in seconds")
	flag.IntVar(&port, "p", 8080, "port to listen on (a free port is picked if it is busy)")
	flag.BoolVar(&showVersion, "v", false, "show version")
	flag.BoolVar(&quiet, "q", false, "hide the live transfer view (refreshed every 3s)")
	flag.BoolVar(&assumeYes, "y", false, "zip and share a folder without asking first")
	flag.Usage = usage

	// Allow flags after the filename too, e.g. `toss file.txt -t 60`.
	args, err := parseInterspersed(flag.CommandLine, os.Args[1:])
	if err != nil {
		return 2
	}

	if showVersion {
		fmt.Printf("toss version %s\n", version)
		return 0
	}
	if len(args) != 1 {
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "Error: no file given. Usage: toss [-t seconds] <file>")
		} else {
			fmt.Fprintln(os.Stderr, "Error: toss shares one file at a time. Put them in a folder and share the folder instead.")
		}
		return 2
	}
	if duration <= 0 {
		fmt.Fprintln(os.Stderr, "Error: -t must be a positive number of seconds.")
		return 2
	}
	if port < 0 || port > 65535 {
		fmt.Fprintln(os.Stderr, "Error: -p must be a port between 0 and 65535.")
		return 2
	}

	absPath, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot resolve file path:", err)
		return 1
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "Error: file not found: %s\n", args[0])
		} else {
			fmt.Fprintln(os.Stderr, "Error: cannot access file:", err)
		}
		return 1
	}
	sourcePath := absPath // what the user asked to share (a folder gets zipped)
	if info.IsDir() {
		zipPath, cleanup, code := shareFolder(absPath, assumeYes)
		defer cleanup()
		if code >= 0 {
			return code
		}
		absPath = zipPath
		if info, err = os.Stat(zipPath); err != nil {
			fmt.Fprintln(os.Stderr, "Error: cannot access the zip:", err)
			return 1
		}
	}
	// Make sure we can actually read it before handing out a link.
	if f, err := os.Open(absPath); err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot read file:", err)
		return 1
	} else {
		f.Close()
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "toss"
	}

	s := &share{
		path:      absPath,
		name:      filepath.Base(absPath),
		size:      info.Size(),
		expiresAt: time.Now().Add(time.Duration(duration) * time.Second),
		track:     newTracker(),
		out:       newConsole(),
	}
	s.urlPath = "/" + hostname + "/download/" + s.name

	// Listen on all interfaces so the link works no matter which adapter
	// the other machine reaches us through.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Printf("Port %d is busy, picking a free port instead...\n", port)
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error: cannot start server:", err)
			return 1
		}
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	primary, others := localIPs()
	escapedPath := "/" + url.PathEscape(hostname) + "/download/" + url.PathEscape(s.name)
	link := fmt.Sprintf("http://%s:%d%s", primary, actualPort, escapedPath)

	logger := openLogger()
	logger.write(logEntry{Event: "share", File: s.name, Path: sourcePath, Bytes: s.size, URL: link, Duration: duration})

	srv := &http.Server{
		Handler:           s.handler(logger),
		ReadHeaderTimeout: 10 * time.Second,
		ConnState:         s.track.connState,
		// No WriteTimeout: large files must be able to stream for as long as they need.
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	fmt.Printf("Sharing %s (%s) for %s.\n", s.name, humanBytes(s.size), time.Duration(duration)*time.Second)
	fmt.Println("Download it from another device on the same network with:")
	fmt.Printf("  %s\n", green("toss pull "+link))
	fmt.Printf("  curl -o %q %s\n", s.name, link)
	fmt.Printf("  or open in a browser: %s\n", link)
	if len(others) > 0 {
		fmt.Println("If that address is unreachable, try one of:")
		for _, ip := range others {
			fmt.Printf("  http://%s:%d%s\n", ip, actualPort, escapedPath)
		}
	}
	fmt.Println("Press Ctrl+C to stop sharing early.")

	dashCtx, stopDash := context.WithCancel(context.Background())
	dashDone := make(chan struct{})
	go func() {
		defer close(dashDone)
		if !quiet {
			runDashboard(dashCtx, s, s.track, s.out)
		}
	}()
	defer func() { stopDash(); <-dashDone }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	timer := time.NewTimer(time.Until(s.expiresAt))
	defer timer.Stop()

	reason := "expired"
	select {
	case <-timer.C:
		s.out.event("Link expired. No new downloads will be accepted.\n")
	case <-sig:
		reason = "stopped"
		s.out.event("Stopping...\n")
	case err := <-serveErr:
		stopDash()
		<-dashDone
		s.out.finish()
		fmt.Fprintln(os.Stderr, "Error: server stopped:", err)
		logger.write(logEntry{Event: "end", File: s.name, Status: "server error: " + err.Error()})
		return 1
	}

	// Let downloads that are already streaming finish; Ctrl+C again aborts them.
	if n := s.inProgress.Load(); n > 0 {
		s.out.event("Waiting for %d download(s) in progress to finish (Ctrl+C to abort)...\n", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := srv.Shutdown(ctx); err != nil {
		srv.Close()
	}
	cancel()
	s.active.Wait()
	stopDash()
	<-dashDone
	s.out.finish()

	logger.write(logEntry{Event: "end", File: s.name, Status: reason, Count: s.downloads.Load()})
	fmt.Printf("Done. %d download(s) served, %s sent in total.\n", s.downloads.Load(), humanBytes(s.track.served.Load()))
	return 0
}

func (s *share) handler(logger *logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != s.urlPath {
			http.Error(w, "Not found: this link does not match any shared file.", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		if time.Now().After(s.expiresAt) {
			http.Error(w, "This link has expired. Ask the sender to run toss again (use -t to share for longer, e.g. toss -t 600 <file>).", http.StatusGone)
			return
		}

		f, err := os.Open(s.path)
		if err != nil {
			http.Error(w, "The shared file is no longer available on the sender's machine.", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, "The shared file is no longer available on the sender's machine.", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": s.name}))
		w.Header().Set("Cache-Control", "no-store")

		if r.Method == http.MethodHead {
			http.ServeContent(w, r, s.name, info.ModTime(), f)
			return
		}

		s.active.Add(1)
		s.inProgress.Add(1)
		defer s.active.Done()
		defer s.inProgress.Add(-1)

		tr := s.track.begin(r)
		defer s.track.end(tr)
		client := tr.client
		start := tr.start
		cw := &countingWriter{ResponseWriter: w, tr: tr, served: &s.track.served}
		s.out.event("[%s] %s started downloading with %s%s\n", start.Format("15:04:05"), client, tr.agent, rangeNote(r))

		// ServeContent streams the file from disk in chunks (never loads it into
		// memory) and supports Range requests, so interrupted downloads can resume
		// with `curl -C -`.
		http.ServeContent(cw, r, s.name, info.ModTime(), f)

		elapsed := time.Since(start)
		expected, _ := strconv.ParseInt(cw.Header().Get("Content-Length"), 10, 64)
		status := "completed"
		switch {
		case cw.status >= 400:
			status = fmt.Sprintf("failed (HTTP %d)", cw.status)
		case cw.status == http.StatusNotModified:
			status = "not modified"
		case cw.sent() < expected:
			status = "interrupted"
		case cw.status == http.StatusPartialContent:
			status = "completed (range)"
		}
		if strings.HasPrefix(status, "completed") {
			s.downloads.Add(1)
		}

		s.out.event("[%s] %s %s: %s in %s (avg %s/s)\n", time.Now().Format("15:04:05"), s.track.clientLabel(client), status,
			humanBytes(cw.sent()), elapsed.Round(time.Millisecond), humanBytes(rate(cw.sent(), elapsed)))
		logger.write(logEntry{Event: "download", File: s.name, Client: client, Bytes: cw.sent(),
			Status: status, Elapsed: elapsed.Round(time.Millisecond).String()})
	})
}

type countingWriter struct {
	http.ResponseWriter
	status int
	tr     *transfer
	served *atomic.Int64
}

func (c *countingWriter) sent() int64 { return c.tr.sent.Load() }

func (c *countingWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
		if n, err := strconv.ParseInt(c.Header().Get("Content-Length"), 10, 64); err == nil {
			c.tr.total.Store(n)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	n, err := c.ResponseWriter.Write(p)
	c.tr.sent.Add(int64(n))
	c.served.Add(int64(n))
	return n, err
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func rangeNote(r *http.Request) string {
	if rg := r.Header.Get("Range"); rg != "" {
		return " (partial: " + rg + ")"
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func rate(n int64, d time.Duration) int64 {
	if d <= 0 {
		return n
	}
	return int64(float64(n) / d.Seconds())
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// parseInterspersed parses flags that may appear before or after positional args.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if rest[0] == "--" {
			return append(positional, rest[1:]...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// localIPs returns the address other machines most likely reach us on, plus
// any other usable IPv4 addresses (VPN, Wi-Fi + Ethernet, virtual adapters...).
func localIPs() (string, []string) {
	var primary string
	// No packets are sent: dialing UDP just asks the OS which local address it
	// would route outbound traffic through.
	if conn, err := net.Dial("udp4", "8.8.8.8:80"); err == nil {
		primary = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}

	var all []string
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipNet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil || ip4.IsLinkLocalUnicast() {
					continue
				}
				all = append(all, ip4.String())
			}
		}
	}

	if primary == "" {
		// Offline LAN: prefer a private address.
		for _, ip := range all {
			if net.ParseIP(ip).IsPrivate() {
				primary = ip
				break
			}
		}
		if primary == "" && len(all) > 0 {
			primary = all[0]
		}
		if primary == "" {
			primary = "127.0.0.1"
		}
	}

	var others []string
	for _, ip := range all {
		if ip != primary {
			others = append(others, ip)
		}
	}
	return primary, others
}
