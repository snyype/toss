package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	refreshLive  = time.Second     // redraw rate on an interactive terminal
	refreshPiped = 3 * time.Second // snapshot rate when output is piped to a file
	historyLen   = 60              // seconds of speed history in the sparkline
	tcpPayload   = 1460            // typical TCP segment payload on Ethernet/Wi-Fi (MSS)
	stallAfter   = 5 * time.Second // a connection with no bytes for this long is "stalled"
)

// transfer is one in-flight HTTP response.
type transfer struct {
	id       int64
	client   string
	agent    string
	rangeHdr string
	part     string       // connection number of a parallel `toss pull`, else ""
	pull     *pullSession // the parallel pull this connection belongs to
	start    time.Time
	total    atomic.Int64 // bytes this response will send (Content-Length)
	sent     atomic.Int64
	writes   atomic.Int64 // socket writes (chunks handed to the network stack)
	lastByte atomic.Int64 // unix nanos of the last write

	// sampled by the dashboard only
	lastSent int64
	lastAt   time.Time
	speed    float64
}

// pullSession groups the connections of one parallel `toss pull`, so the
// dashboard can show progress for the whole file instead of per connection.
type pullSession struct {
	client string
	agent  string
	conns  int
	have   int64 // bytes the client already had from an earlier attempt
	start  time.Time
	sent   atomic.Int64
	writes atomic.Int64
	idle   time.Time // when its last connection ended (zero while active)
}

// tracker keeps the live state shown on the dashboard.
type tracker struct {
	mu        sync.Mutex
	nextID    int64
	transfers map[int64]*transfer
	pulls     map[string]*pullSession // by client IP
	conns     atomic.Int64            // open TCP connections
	served    atomic.Int64            // total bytes sent this session
	writes    atomic.Int64            // total socket writes this session
	completed atomic.Int64            // finished downloads (mirrors share.downloads)

	// sampled by the dashboard only
	history    []float64
	peak       float64
	activeTime time.Duration
	lastServed int64
	lastSample time.Time

	namesMu sync.Mutex
	names   map[string]string // ip -> reverse-DNS name ("" if none)
}

func newTracker() *tracker {
	return &tracker{transfers: map[int64]*transfer{}, pulls: map[string]*pullSession{},
		names: map[string]string{}, lastSample: time.Now()}
}

func (t *tracker) begin(r *http.Request) *transfer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	now := time.Now()
	tr := &transfer{
		id:       t.nextID,
		client:   clientIP(r),
		agent:    shortAgent(r.UserAgent()),
		rangeHdr: r.Header.Get("Range"),
		start:    now,
		lastAt:   now,
	}
	tr.lastByte.Store(now.UnixNano())
	if part := r.Header.Get("X-Toss-Part"); part != "" {
		tr.part = part
		tr.rangeHdr = ""
		ps := t.pulls[tr.client]
		if ps == nil { // the start notice was missed (e.g. sent before a restart)
			ps = &pullSession{client: tr.client, agent: tr.agent, start: now}
			t.pulls[tr.client] = ps
		}
		ps.idle = time.Time{}
		tr.pull = ps
	}
	t.transfers[tr.id] = tr
	go t.resolve(tr.client)
	return tr
}

func (t *tracker) end(tr *transfer) {
	t.mu.Lock()
	delete(t.transfers, tr.id)
	if tr.pull != nil {
		busy := false
		for _, o := range t.transfers {
			if o.pull == tr.pull {
				busy = true
				break
			}
		}
		if !busy {
			tr.pull.idle = time.Now()
		}
	}
	t.mu.Unlock()
}

// startPull registers a parallel `toss pull` announced by its HEAD request.
func (t *tracker) startPull(ip, agent string, conns int, have int64) {
	t.mu.Lock()
	t.pulls[ip] = &pullSession{client: ip, agent: agent, conns: conns, have: have, start: time.Now()}
	t.mu.Unlock()
	go t.resolve(ip)
}

func (t *tracker) endPull(ip string) {
	t.mu.Lock()
	delete(t.pulls, ip)
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

// clearSeq moves the cursor back over the dashboard and erases it.
func (c *console) clearSeq() string {
	if c.live && c.dashLines > 0 {
		return fmt.Sprintf("\x1b[%dA\r\x1b[J", c.dashLines)
	}
	return ""
}

func (c *console) dashText() string {
	if !c.live || len(c.dash) == 0 {
		return ""
	}
	return strings.Join(c.dash, "\n") + "\n"
}

// event prints a normal log line above the dashboard.
func (c *console) event(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// One write per frame, so the redraw doesn't flicker.
	fmt.Print(c.clearSeq() + fmt.Sprintf(format, a...) + c.dashText())
	c.dashLines = len(c.dash)
	if !c.live {
		c.dashLines = 0
	}
}

func (c *console) setDash(lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		clear := c.clearSeq()
		c.dash = lines
		fmt.Print(clear + c.dashText())
		c.dashLines = len(lines)
		return
	}
	// Piped output: print a snapshot only while something is happening.
	for _, l := range lines {
		fmt.Println(stripANSI(l))
	}
}

// finish removes the dashboard before the final summary is printed.
func (c *console) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Print(c.clearSeq())
	c.dashLines = 0
	c.dash = nil
}

// runDashboard refreshes the live view until ctx is done.
func runDashboard(ctx context.Context, s *share, t *tracker, c *console) {
	every := refreshPiped
	if c.live {
		every = refreshLive
	}
	tick := time.NewTicker(every)
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

// dashGroup is one client download: a single transfer or a parallel pull.
type dashGroup struct {
	client string
	agent  string
	start  time.Time
	sent   int64 // bytes of the file the client has (incl. earlier attempts)
	size   int64
	speed  float64
	writes int64
	conns  []*transfer
	pull   *pullSession
	note   string
}

func (t *tracker) snapshot(s *share, live bool) []string {
	now := time.Now()

	t.mu.Lock()
	list := make([]*transfer, 0, len(t.transfers))
	for _, tr := range t.transfers {
		sent := tr.sent.Load()
		if dt := now.Sub(tr.lastAt).Seconds(); dt > 0.2 {
			tr.speed = float64(sent-tr.lastSent) / dt
			tr.lastSent, tr.lastAt = sent, now
		}
		list = append(list, tr)
	}
	// Forget pulls that went quiet without saying they were done (client stopped).
	for ip, ps := range t.pulls {
		if !ps.idle.IsZero() && now.Sub(ps.idle) > time.Minute {
			delete(t.pulls, ip)
		}
	}

	served := t.served.Load()
	var cur float64
	if dt := now.Sub(t.lastSample).Seconds(); dt > 0.2 {
		cur = float64(served-t.lastServed) / dt
		if served > t.lastServed {
			t.activeTime += now.Sub(t.lastSample)
		}
		t.history = append(t.history, cur)
		if len(t.history) > historyLen {
			t.history = t.history[len(t.history)-historyLen:]
		}
		t.lastServed, t.lastSample = served, now
	} else if n := len(t.history); n > 0 {
		cur = t.history[n-1]
	}
	if cur > t.peak {
		t.peak = cur
	}
	history := append([]float64(nil), t.history...)
	peak, activeTime := t.peak, t.activeTime
	t.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })

	if len(list) == 0 && !live {
		return nil
	}

	// Group parallel-pull connections by session; other downloads stand alone.
	var groups []*dashGroup
	byPull := map[*pullSession]*dashGroup{}
	for _, tr := range list {
		if tr.pull != nil {
			g := byPull[tr.pull]
			if g == nil {
				g = &dashGroup{client: tr.client, agent: tr.pull.agent, start: tr.pull.start, size: s.size, pull: tr.pull,
					sent: tr.pull.have + tr.pull.sent.Load()}
				byPull[tr.pull] = g
				groups = append(groups, g)
			}
			g.conns = append(g.conns, tr)
			g.speed += tr.speed
			g.writes = tr.pull.writes.Load()
			continue
		}
		g := &dashGroup{client: tr.client, agent: tr.agent, start: tr.start, sent: tr.sent.Load(), size: tr.total.Load(),
			speed: tr.speed, writes: tr.writes.Load(), conns: []*transfer{tr}}
		if tr.rangeHdr != "" {
			g.note = "partial request, bytes " + strings.TrimPrefix(tr.rangeHdr, "bytes=")
			// A resume ("bytes=N-"): show progress through the whole file.
			var from int64
			if _, err := fmt.Sscanf(tr.rangeHdr, "bytes=%d-", &from); err == nil && from > 0 && from+g.size == s.size {
				g.sent += from
				g.size = s.size
				g.note = "resumed from " + humanBytes(from)
			}
		}
		groups = append(groups, g)
	}

	return t.render(s, groups, cur, peak, served, activeTime, history, now)
}

func (t *tracker) render(s *share, groups []*dashGroup, cur, peak float64, served int64, activeTime time.Duration,
	history []float64, now time.Time) []string {
	width := termWidth()
	if width <= 0 {
		width = 100
	}
	width = min(max(width-1, 60), 120) // -1: never touch the last column, or the terminal wraps
	inner := width - 4                 // "│ " + content + " │"

	b := &box{inner: inner}

	// Title bar: live dot, file, size  ...  expiry.
	left := time.Until(s.expiresAt).Round(time.Second)
	expiry := dim("expires in ") + left.String()
	if left <= 0 {
		expiry = yellow("expired · finishing active downloads")
	}
	title := fmt.Sprintf("%s %s %s", bold(green("●")), bold("Sending "+truncate(s.name, inner-40)), dim("· "+humanBytes(s.size)))
	b.top(title, expiry)

	var avg float64
	if activeTime > 0 {
		avg = float64(served) / activeTime.Seconds()
	}
	b.line(fmt.Sprintf("%s %s  %s %s  %s %s", dim("Speed  "), bold(cyan(fmt.Sprintf("%-12s", humanBytes(int64(cur))+"/s"))),
		dim("peak"), fmt.Sprintf("%-12s", humanBytes(int64(peak))+"/s"), dim("avg"), humanBytes(int64(avg))+"/s"))
	b.line(dim("History") + " " + cyan(sparkline(history, inner-20)) + dim(" last "+fmt.Sprint(historyLen)+"s"))

	writes := t.writes.Load()
	b.line(fmt.Sprintf("%s %s sent · %s writes · %s/write · ≈%s TCP packets", dim("Traffic"),
		bold(humanBytes(served)), groupDigits(writes), humanBytes(safeDiv(served, writes)),
		groupDigits((served+tcpPayload-1)/tcpPayload)))
	b.line(fmt.Sprintf("%s %s · %s · %s", dim("Clients"),
		plural(len(groups), "active download"), plural(int(t.conns.Load()), "open connection"),
		plural(int(t.completed.Load()), "completed download")))

	if len(groups) == 0 {
		b.sep()
		b.line(dim("Waiting for someone to open the link..."))
		b.bottom()
		return b.lines
	}

	for _, g := range groups {
		b.sep()
		head := bold(t.clientLabel(g.client)) + dim(" · "+g.agent)
		if g.pull != nil {
			n := max(g.pull.conns, len(g.conns))
			head += dim(fmt.Sprintf(" · parallel pull, %d connections", n))
		}
		b.line(head)

		eta := "-"
		if g.speed > 0 && g.size > g.sent {
			eta = (time.Duration(float64(g.size-g.sent)/g.speed) * time.Second).Round(time.Second).String()
		}
		stats := fmt.Sprintf(" %s  %s  %s", bold(fmt.Sprintf("%-11s", humanBytes(int64(g.speed))+"/s")),
			dim("ETA ")+fmt.Sprintf("%-8s", eta), dim("elapsed ")+now.Sub(g.start).Round(time.Second).String())
		barW := inner - visibleLen(stats) - 28
		b.line(bigBar(g.sent, g.size, max(barW, 10)) + stats)

		if g.pull != nil {
			// One cell per connection: "#2 ▕████░░░░▏ 11.2 MiB/s".
			sort.Slice(g.conns, func(i, j int) bool {
				a, _ := strconv.Atoi(g.conns[i].part)
				b, _ := strconv.Atoi(g.conns[j].part)
				return a < b
			})
			var cells []string
			for _, tr := range g.conns {
				cells = append(cells, connCell(tr, now))
			}
			b.flow(cells, "  ")
		} else if g.note != "" {
			b.line(dim("↳ " + g.note))
		}
		if idle := now.Sub(time.Unix(0, lastByteOf(g.conns))); idle > stallAfter {
			b.line(yellow(fmt.Sprintf("! no data sent for %s: the receiver or the network has stalled", idle.Round(time.Second))))
		}
		run := g.sentThisRun()
		b.line(dim(fmt.Sprintf("  %s writes · avg %s/write · ≈%s packets", groupDigits(g.writes),
			humanBytes(safeDiv(run, g.writes)), groupDigits((run+tcpPayload-1)/tcpPayload))))
	}
	b.bottom()
	return b.lines
}

func (g *dashGroup) sentThisRun() int64 {
	if g.pull != nil {
		return g.pull.sent.Load()
	}
	var n int64
	for _, tr := range g.conns {
		n += tr.sent.Load()
	}
	return n
}

func lastByteOf(conns []*transfer) int64 {
	var last int64
	for _, tr := range conns {
		last = max(last, tr.lastByte.Load())
	}
	return last
}

// connCell renders one connection of a parallel pull.
func connCell(tr *transfer, now time.Time) string {
	const w = 8
	sent, size := tr.sent.Load(), tr.total.Load()
	filled := 0
	if size > 0 {
		filled = int(float64(sent) / float64(size) * w)
	}
	filled = min(max(filled, 0), w)
	bar := dim("▕") + green(strings.Repeat("█", filled)) + dim(strings.Repeat("░", w-filled)+"▏")
	speed := fmt.Sprintf("%-11s", humanBytes(int64(tr.speed))+"/s")
	if idle := now.Sub(time.Unix(0, tr.lastByte.Load())); idle > stallAfter {
		speed = yellow(fmt.Sprintf("%-11s", "stalled"))
	}
	return dim("#"+tr.part) + " " + bar + " " + speed
}

// bigBar is a progress bar with percentage and sizes (w + 27 columns).
func bigBar(sent, size int64, w int) string {
	if size <= 0 {
		return fmt.Sprintf("%-*s", w+27, humanBytes(sent))
	}
	frac := min(max(float64(sent)/float64(size), 0), 1)
	filled := int(frac * float64(w))
	bar := green(strings.Repeat("█", filled)) + dim(strings.Repeat("░", w-filled))
	return bar + fmt.Sprintf(" %3.0f%% %-21s", frac*100, humanBytes(sent)+"/"+humanBytes(size))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the speed history, newest on the right, scaled to its max.
func sparkline(h []float64, w int) string {
	w = max(w, 10)
	if len(h) > w {
		h = h[len(h)-w:]
	}
	var top float64
	for _, v := range h {
		top = max(top, v)
	}
	var sb strings.Builder
	sb.WriteString(strings.Repeat(" ", w-len(h)))
	for _, v := range h {
		i := 0
		if top > 0 && v > 0 {
			i = max(min(int(v/top*float64(len(sparkRunes)-1)+0.5), len(sparkRunes)-1), 1) // any traffic shows above the baseline
		}
		sb.WriteRune(sparkRunes[i])
	}
	return sb.String()
}

// box draws a rounded frame of a fixed inner width.
type box struct {
	inner int
	lines []string
}

func (b *box) top(title, right string) {
	// "╭─ " title " " fill " " right " ─╮" must be as wide as "│ " inner " │".
	fill := b.inner - visibleLen(title) - visibleLen(right) - 4
	if fill < 1 {
		right = ""
		fill = max(b.inner-visibleLen(title)-3, 1)
	}
	b.lines = append(b.lines, "", dim("╭─ ")+title+" "+dim(strings.Repeat("─", fill))+" "+right+dim(" ─╮"))
}

func (b *box) sep()    { b.lines = append(b.lines, dim("├"+strings.Repeat("─", b.inner+2)+"┤")) }
func (b *box) bottom() { b.lines = append(b.lines, dim("╰"+strings.Repeat("─", b.inner+2)+"╯")) }

func (b *box) line(s string) {
	if visibleLen(s) > b.inner {
		s = truncate(stripANSI(s), b.inner)
	}
	b.lines = append(b.lines, dim("│ ")+s+strings.Repeat(" ", max(b.inner-visibleLen(s), 0))+dim(" │"))
}

// flow lays cells out left to right, wrapping onto new lines as needed.
func (b *box) flow(cells []string, gap string) {
	cur := ""
	for _, c := range cells {
		if cur != "" && visibleLen(cur)+len(gap)+visibleLen(c) > b.inner {
			b.line(cur)
			cur = ""
		}
		if cur != "" {
			cur += gap
		}
		cur += c
	}
	if cur != "" {
		b.line(cur)
	}
}

func safeDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// groupDigits formats 1234567 as "1,234,567".
func groupDigits(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
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
