package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const refreshEvery = 3 * time.Second

// transfer is one in-flight download.
type transfer struct {
	id       int64
	client   string
	agent    string
	rangeHdr string
	start    time.Time
	total    atomic.Int64 // bytes this response will send (Content-Length)
	sent     atomic.Int64

	// sampled by the dashboard only
	lastSent int64
	lastAt   time.Time
	speed    float64
}

// tracker keeps the live state shown on the dashboard.
type tracker struct {
	mu        sync.Mutex
	nextID    int64
	transfers map[int64]*transfer
	conns     atomic.Int64 // open TCP connections
	served    atomic.Int64 // total bytes sent this session

	namesMu sync.Mutex
	names   map[string]string // ip -> reverse-DNS name ("" if none)
}

func newTracker() *tracker {
	return &tracker{transfers: map[int64]*transfer{}, names: map[string]string{}}
}

func (t *tracker) begin(r *http.Request) *transfer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	tr := &transfer{
		id:       t.nextID,
		client:   clientIP(r),
		agent:    shortAgent(r.UserAgent()),
		rangeHdr: r.Header.Get("Range"),
		start:    time.Now(),
		lastAt:   time.Now(),
	}
	t.transfers[tr.id] = tr
	go t.resolve(tr.client)
	return tr
}

func (t *tracker) end(tr *transfer) {
	t.mu.Lock()
	delete(t.transfers, tr.id)
	t.mu.Unlock()
}

func (t *tracker) connState(_ net.Conn, st http.ConnState) {
	switch st {
	case http.StateNew:
		t.conns.Add(1)
	case http.StateClosed, http.StateHijacked:
		t.conns.Add(-1)
	}
}

// resolve looks up the client's hostname once, in the background.
func (t *tracker) resolve(ip string) {
	t.namesMu.Lock()
	if _, done := t.names[ip]; done {
		t.namesMu.Unlock()
		return
	}
	t.names[ip] = ""
	t.namesMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return
	}
	// Keep just the machine name: "laptop.lan." / "pc.isp.example." -> "laptop" / "pc".
	name := strings.SplitN(strings.TrimSuffix(names[0], "."), ".", 2)[0]
	t.namesMu.Lock()
	t.names[ip] = name
	t.namesMu.Unlock()
}

func (t *tracker) clientLabel(ip string) string {
	t.namesMu.Lock()
	name := t.names[ip]
	t.namesMu.Unlock()
	if name == "" || name == ip {
		return ip
	}
	return ip + " (" + name + ")"
}

// console serialises terminal output so event lines and the live dashboard
// don't trample each other. On a terminal the dashboard is redrawn in place.
type console struct {
	mu        sync.Mutex
	live      bool // can move the cursor (interactive terminal)
	dashLines int
	dash      []string
}

func newConsole() *console {
	return &console{live: stdoutIsTerminal() && enableVirtualTerminal()}
}

func (c *console) clearDash() {
	if c.live && c.dashLines > 0 {
		fmt.Printf("\x1b[%dA\x1b[J", c.dashLines)
	}
	c.dashLines = 0
}

func (c *console) drawDash() {
	if !c.live {
		return
	}
	for _, l := range c.dash {
		fmt.Println(l)
	}
	c.dashLines = len(c.dash)
}

// event prints a normal log line above the dashboard.
func (c *console) event(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearDash()
	fmt.Printf(format, a...)
	c.drawDash()
}

func (c *console) setDash(lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		c.clearDash()
		c.dash = lines
		c.drawDash()
		return
	}
	// Piped output: print a snapshot only while something is happening.
	if len(lines) > 0 {
		for _, l := range lines {
			fmt.Println(stripANSI(l))
		}
	}
}

// finish removes the dashboard before the final summary is printed.
func (c *console) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearDash()
	c.dash = nil
}

// runDashboard refreshes the live view every 3 seconds until ctx is done.
func runDashboard(ctx context.Context, s *share, t *tracker, c *console) {
	tick := time.NewTicker(refreshEvery)
	defer tick.Stop()
	c.setDash(t.snapshot(s, c.live))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.setDash(t.snapshot(s, c.live))
		}
	}
}

func (t *tracker) snapshot(s *share, live bool) []string {
	now := time.Now()

	t.mu.Lock()
	list := make([]*transfer, 0, len(t.transfers))
	for _, tr := range t.transfers {
		sent := tr.sent.Load()
		if dt := now.Sub(tr.lastAt).Seconds(); dt > 0 {
			tr.speed = float64(sent-tr.lastSent) / dt
		}
		tr.lastSent, tr.lastAt = sent, now
		list = append(list, tr)
	}
	t.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })

	if len(list) == 0 && !live {
		return nil
	}

	left := time.Until(s.expiresAt).Round(time.Second)
	expiry := "link expires in " + left.String()
	if left <= 0 {
		expiry = yellow("link expired, finishing active downloads")
	}

	var total float64
	for _, tr := range list {
		total += tr.speed
	}

	stamp := dim(now.Format("15:04:05"))
	header := fmt.Sprintf("%s %s  %s · %s · %s · served %s",
		stamp, bold(cyan("● LIVE")),
		plural(len(list), "download"), plural(int(t.conns.Load()), "connection"),
		expiry, humanBytes(t.served.Load()))
	if len(list) == 0 {
		return []string{"", header, dim("  Waiting for someone to open the link...")}
	}

	lines := []string{"", header,
		dim(fmt.Sprintf("  %-34s %-14s %-42s %-12s %-10s %s", "CLIENT", "APP", "PROGRESS", "SPEED", "ELAPSED", "ETA"))}
	for _, tr := range list {
		sent, size := tr.sent.Load(), tr.total.Load()
		elapsed := now.Sub(tr.start)
		eta := "-"
		if tr.speed > 0 && size > sent {
			eta = (time.Duration(float64(size-sent)/tr.speed) * time.Second).Round(time.Second).String()
		}
		lines = append(lines, fmt.Sprintf("  %-34s %-14s %s %-12s %-10s %s",
			truncate(t.clientLabel(tr.client), 34),
			truncate(tr.agent, 14),
			progressCell(sent, size),
			humanBytes(int64(tr.speed))+"/s",
			elapsed.Round(time.Second).String(),
			eta))
		if tr.rangeHdr != "" {
			lines = append(lines, dim("    ↳ partial download, bytes "+strings.TrimPrefix(tr.rangeHdr, "bytes=")))
		}
	}
	if len(list) > 1 {
		lines = append(lines, fmt.Sprintf("  %s %s/s", dim("Total speed:"), bold(humanBytes(int64(total)))))
	}
	return lines
}

// progressCell renders a fixed-width (42 visible chars) progress column.
func progressCell(sent, size int64) string {
	const barW = 12
	if size <= 0 {
		return fmt.Sprintf("%-42s", humanBytes(sent))
	}
	frac := float64(sent) / float64(size)
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * barW)
	bar := green(strings.Repeat("█", filled)) + dim(strings.Repeat("░", barW-filled))
	text := fmt.Sprintf(" %3.0f%% %s/%s", frac*100, humanBytes(sent), humanBytes(size))
	pad := 42 - barW - len(text)
	if pad < 0 {
		pad = 0
	}
	return bar + text + strings.Repeat(" ", pad)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// shortAgent turns a User-Agent header into something readable.
func shortAgent(ua string) string {
	if ua == "" {
		return "unknown"
	}
	l := strings.ToLower(ua)
	switch {
	case strings.HasPrefix(l, "curl/"), strings.HasPrefix(l, "wget/"):
		return strings.SplitN(ua, " ", 2)[0]
	case strings.Contains(l, "powershell"):
		return "PowerShell"
	case strings.Contains(l, "edg/"):
		return "Edge" + osHint(l)
	case strings.Contains(l, "opr/"), strings.Contains(l, "opera"):
		return "Opera" + osHint(l)
	case strings.Contains(l, "firefox/"):
		return "Firefox" + osHint(l)
	case strings.Contains(l, "chrome/"):
		return "Chrome" + osHint(l)
	case strings.Contains(l, "safari/"):
		return "Safari" + osHint(l)
	}
	return strings.SplitN(ua, " ", 2)[0]
}

func osHint(l string) string {
	switch {
	case strings.Contains(l, "android"):
		return " (Android)"
	case strings.Contains(l, "iphone"), strings.Contains(l, "ipad"):
		return " (iOS)"
	case strings.Contains(l, "windows"):
		return " (Win)"
	case strings.Contains(l, "mac os"):
		return " (Mac)"
	case strings.Contains(l, "linux"):
		return " (Linux)"
	}
	return ""
}
