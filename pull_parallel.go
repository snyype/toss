package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Parallel pulls: the file is split into byte ranges fetched over several
// connections at once. On Wi-Fi and busy networks this usually beats a single
// TCP stream, and a dropped connection only costs a retry of its own range.
const (
	defaultPullConns = 4
	maxPullConns     = 16
	minSegment       = 8 << 20          // never split a range smaller than this
	copyBufSize      = 256 << 10        // read size per connection
	idleTimeout      = 30 * time.Second // restart a connection that receives nothing for this long
	maxStalls        = 8                // failed attempts in a row (without progress) before giving up
	saveEvery        = 2 * time.Second
)

// pullState is saved next to the .part file so an interrupted parallel
// download can resume exactly where each range stopped.
type pullState struct {
	Size     int64      `json:"size"`
	Modified string     `json:"modified,omitempty"`
	Segments [][3]int64 `json:"segments"` // start, end (exclusive), bytes done
}

type segment struct {
	id    int
	start int64
	end   atomic.Int64 // may shrink when another connection takes over part of the range
	done  atomic.Int64
}

func (s *segment) pos() int64 { return s.start + s.done.Load() }

func (s *segment) remaining() int64 {
	if r := s.end.Load() - s.pos(); r > 0 {
		return r
	}
	return 0
}

// fatalError marks failures that retrying will not fix (link expired, file changed...).
type fatalError struct{ error }

func (e fatalError) Unwrap() error { return e.error }

type writeError struct{ error }

func (e writeError) Unwrap() error { return e.error }

type puller struct {
	client   *http.Client
	link     string
	part     string
	size     int64
	modified string
	f        *os.File
	got      atomic.Int64 // bytes saved so far, for the progress bar

	mu     sync.Mutex
	segs   []*segment
	nextID int
}

func newPullClient(conns int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxIdleConnsPerHost = conns + 1
	transport.DisableCompression = true
	transport.ReadBufferSize = 64 << 10
	// No overall timeout: large files must be able to stream for as long as they need.
	return &http.Client{Transport: transport}
}

// fetch downloads link into part, resuming whatever an earlier run saved.
func fetch(ctx context.Context, link, part string, conns int) (fetchResult, error) {
	client := newPullClient(conns)
	saved := savedBytes(part)
	size, name, modified, ranges, err := probe(ctx, client, link, conns, saved)
	if err != nil {
		res := fetchResult{got: saved, resumable: saved > 0}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	if !ranges || size <= 0 {
		// The server can't do byte ranges (not a toss sender?): one plain stream.
		if _, err := os.Stat(part + ".state"); err == nil {
			os.Remove(part)
			os.Remove(part + ".state")
		}
		return fetchSingle(ctx, client, link, part)
	}

	p := &puller{client: client, link: link, part: part, size: size, modified: modified}
	res, err := p.run(ctx, conns)
	res.name = name
	return res, err
}

// probe asks the sender about the file with a HEAD request.
func probe(ctx context.Context, client *http.Client, link string, conns int, have int64) (size int64, name, modified string, ranges bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return 0, "", "", false, err
	}
	req.Header.Set("User-Agent", "toss/"+version)
	req.Header.Set("X-Toss-Event", "start")
	req.Header.Set("X-Toss-Conns", strconv.Itoa(conns))
	req.Header.Set("X-Toss-Have", strconv.FormatInt(have, 10)) // lets the sender show whole-file progress
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", false, reachError(req, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", "", false, statusError(resp)
	}
	size, perr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if perr != nil {
		size = -1
	}
	return size, dispositionName(resp.Header.Get("Content-Disposition")), resp.Header.Get("Last-Modified"),
		resp.Header.Get("Accept-Ranges") == "bytes", nil
}

func (p *puller) run(ctx context.Context, conns int) (fetchResult, error) {
	var res fetchResult

	st := p.loadState()
	if st == nil {
		var prefix int64
		if _, err := os.Stat(p.part + ".state"); err == nil {
			// State from a different version of the file: start over.
			os.Remove(p.part)
			os.Remove(p.part + ".state")
		} else if fi, err := os.Stat(p.part); err == nil {
			if fi.Size() <= p.size {
				prefix = fi.Size() // a .part from a single-stream download: keep it
			} else {
				os.Remove(p.part)
			}
		}
		p.plan(prefix, conns)
	}

	f, err := os.OpenFile(p.part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return res, fmt.Errorf("cannot write %s: %w", p.part, err)
	}
	p.f = f
	makeSparse(f) // Windows would otherwise zero-fill gigabytes before the later ranges can be written
	if err := f.Truncate(p.size); err != nil {
		f.Close()
		return res, fmt.Errorf("cannot write %s: %w", p.part, err)
	}

	startGot := p.savedTotal()
	p.got.Store(startGot)
	if startGot > 0 {
		fmt.Printf("Resuming from %s...\n", humanBytes(startGot))
	}
	active := 0
	for _, s := range p.segs {
		if s.remaining() > 0 {
			active++
		}
	}
	if active > 1 {
		fmt.Printf("Downloading over %d connections...\n", active)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err })
		cancel()
	}

	saverDone := make(chan struct{})
	stopSaver := make(chan struct{})
	go func() {
		defer close(saverDone)
		t := time.NewTicker(saveEvery)
		defer t.Stop()
		for {
			select {
			case <-stopSaver:
				return
			case <-t.C:
				p.saveState()
			}
		}
	}()

	stopProgress := startPullProgress(&p.got, p.size)
	var wg sync.WaitGroup
	for _, s := range p.segs {
		if s.remaining() > 0 {
			wg.Add(1)
			go p.worker(runCtx, s, &wg, fail)
		}
	}
	wg.Wait()
	stopProgress()
	close(stopSaver)
	<-saverDone

	res.size = p.size
	res.got = p.savedTotal()
	res.received = res.got - startGot
	if res.received < 0 {
		res.received = 0
	}

	complete := true
	for _, s := range p.segs {
		if s.remaining() > 0 {
			complete = false
			break
		}
	}
	if complete {
		if err := f.Close(); err != nil {
			return res, fmt.Errorf("cannot write %s: %w", p.part, err)
		}
		os.Remove(p.part + ".state")
		res.got = p.size
		return res, nil
	}

	p.saveState()
	f.Close()
	res.resumable = res.got > 0
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if firstErr == nil {
		firstErr = errors.New("the download stopped before it finished")
	}
	return res, firstErr
}

// plan splits [prefix, size) into up to conns ranges of at least minSegment.
func (p *puller) plan(prefix int64, conns int) {
	if prefix > 0 {
		p.addSegment(0, prefix, prefix)
	}
	left := p.size - prefix
	if left <= 0 {
		return
	}
	n := int64(conns)
	if most := left / minSegment; most < n {
		n = most
	}
	if n < 1 {
		n = 1
	}
	chunk := left / n
	start := prefix
	for i := int64(0); i < n; i++ {
		end := start + chunk
		if i == n-1 {
			end = p.size
		}
		p.addSegment(start, end, 0)
		start = end
	}
}

func (p *puller) addSegment(start, end, done int64) *segment {
	p.nextID++
	s := &segment{id: p.nextID, start: start}
	s.end.Store(end)
	s.done.Store(done)
	p.segs = append(p.segs, s)
	return s
}

func (p *puller) savedTotal() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var n int64
	for _, s := range p.segs {
		d, span := s.done.Load(), s.end.Load()-s.start
		if d > span {
			d = span
		}
		n += d
	}
	return n
}

func (p *puller) loadState() *pullState {
	b, err := os.ReadFile(p.part + ".state")
	if err != nil {
		return nil
	}
	var st pullState
	if json.Unmarshal(b, &st) != nil || st.Size != p.size || st.Modified != p.modified || len(st.Segments) == 0 {
		return nil
	}
	if _, err := os.Stat(p.part); err != nil {
		return nil
	}
	for _, sg := range st.Segments {
		start, end, done := sg[0], sg[1], sg[2]
		if start < 0 || end > p.size || start > end || done < 0 {
			p.segs, p.nextID = nil, 0
			return nil
		}
		if done > end-start {
			done = end - start
		}
		p.addSegment(start, end, done)
	}
	return &st
}

func (p *puller) saveState() {
	p.mu.Lock()
	st := pullState{Size: p.size, Modified: p.modified}
	for _, s := range p.segs {
		d, end := s.done.Load(), s.end.Load()
		if d > end-s.start {
			d = end - s.start
		}
		st.Segments = append(st.Segments, [3]int64{s.start, end, d})
	}
	p.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := p.part + ".state.tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, p.part+".state")
	}
}

// savedBytes reports how much an earlier run saved, for messages only.
func savedBytes(part string) int64 {
	if b, err := os.ReadFile(part + ".state"); err == nil {
		var st pullState
		if json.Unmarshal(b, &st) == nil {
			var n int64
			for _, sg := range st.Segments {
				n += sg[2]
			}
			return n
		}
	}
	if fi, err := os.Stat(part); err == nil {
		return fi.Size()
	}
	return 0
}

// worker downloads its range, then keeps helping with the largest range left.
func (p *puller) worker(ctx context.Context, s *segment, wg *sync.WaitGroup, fail func(error)) {
	defer wg.Done()
	var first *openResp
	for s != nil {
		if err := p.download(ctx, s, first); err != nil {
			if ctx.Err() == nil {
				fail(err)
			}
			return
		}
		s, first = p.steal(ctx)
	}
}

type openResp struct {
	resp     *http.Response
	cancel   context.CancelFunc
	watchdog *time.Timer
}

func (o *openResp) close() {
	o.watchdog.Stop()
	o.resp.Body.Close()
	o.cancel()
}

// open requests bytes [from, to) for segment id.
func (p *puller) open(ctx context.Context, from, to int64, id int) (*openResp, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.link, nil)
	if err != nil {
		cancel()
		return nil, fatalError{err}
	}
	req.Header.Set("User-Agent", "toss/"+version)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to-1))
	req.Header.Set("X-Toss-Part", strconv.Itoa(id))
	if p.modified != "" {
		// If the file changed since we started, the sender answers 200 instead of 206.
		req.Header.Set("If-Range", p.modified)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		cancel()
		return nil, reachError(req, err)
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		want := fmt.Sprintf("bytes %d-%d/%d", from, to-1, p.size)
		if resp.Header.Get("Content-Range") != want {
			resp.Body.Close()
			cancel()
			return nil, fatalError{errors.New("the sender returned an unexpected byte range; run the command again to start over")}
		}
		return &openResp{resp: resp, cancel: cancel, watchdog: time.AfterFunc(idleTimeout, cancel)}, nil
	case resp.StatusCode == http.StatusOK:
		resp.Body.Close()
		cancel()
		os.Remove(p.part + ".state")
		return nil, fatalError{errors.New("the shared file changed on the sender's machine; run the command again to download the new version")}
	case resp.StatusCode >= 500:
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("the sender returned %s", resp.Status)
	default:
		err := statusError(resp)
		resp.Body.Close()
		cancel()
		return nil, fatalError{err}
	}
}

// download fetches the rest of s, reconnecting after drops and stalls.
func (p *puller) download(ctx context.Context, s *segment, o *openResp) error {
	stalls := 0
	for s.remaining() > 0 {
		if o == nil {
			var err error
			o, err = p.open(ctx, s.pos(), s.end.Load(), s.id)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				var fe fatalError
				if errors.As(err, &fe) {
					return err
				}
				if stalls++; stalls > maxStalls {
					return err
				}
				if !sleepCtx(ctx, backoff(stalls)) {
					return ctx.Err()
				}
				continue
			}
		}
		before := s.pos()
		err := p.copyRange(s, o)
		o.close()
		o = nil
		if s.pos() > before {
			stalls = 0
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var we writeError
		if errors.As(err, &we) {
			return fmt.Errorf("cannot write %s: %w", p.part, we.error)
		}
		if stalls++; stalls > maxStalls {
			return fmt.Errorf("the connection keeps dropping (%v)", err)
		}
		if !sleepCtx(ctx, backoff(stalls)) {
			return ctx.Err()
		}
	}
	return nil
}

// copyRange writes the response body into the file at the segment's position
// until the segment is complete (its end may move closer while we read).
func (p *puller) copyRange(s *segment, o *openResp) error {
	buf := make([]byte, copyBufSize)
	for {
		pos, end := s.pos(), s.end.Load()
		if pos >= end {
			return nil
		}
		want := int64(len(buf))
		if end-pos < want {
			want = end - pos
		}
		n, err := o.resp.Body.Read(buf[:want])
		if n > 0 {
			o.watchdog.Reset(idleTimeout)
			if _, werr := p.f.WriteAt(buf[:n], pos); werr != nil {
				return writeError{werr}
			}
			s.done.Add(int64(n))
			credit := int64(n)
			if limit := s.end.Load() - pos; credit > limit {
				credit = max(limit, 0)
			}
			p.got.Add(credit)
		}
		if err != nil {
			if s.pos() >= s.end.Load() {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

// steal splits the largest unfinished range and starts fetching its second
// half, so connections that finish early keep helping until the end.
func (p *puller) steal(ctx context.Context) (*segment, *openResp) {
	for tries := 0; tries < 3; tries++ {
		if ctx.Err() != nil {
			return nil, nil
		}
		p.mu.Lock()
		var victim *segment
		for _, s := range p.segs {
			if victim == nil || s.remaining() > victim.remaining() {
				victim = s
			}
		}
		if victim == nil || victim.remaining() < 2*minSegment {
			p.mu.Unlock()
			return nil, nil
		}
		mid := victim.pos() + victim.remaining()/2
		end := victim.end.Load()
		p.nextID++
		id := p.nextID
		p.mu.Unlock()

		// Only shrink the victim once the sender has accepted the new range.
		o, err := p.open(ctx, mid, end, id)
		if err != nil {
			return nil, nil
		}
		p.mu.Lock()
		if victim.end.Load() != end || victim.pos()+copyBufSize >= mid {
			p.mu.Unlock()
			o.close()
			continue // the victim moved on meanwhile; pick again
		}
		victim.end.Store(mid)
		s := &segment{id: id, start: mid}
		s.end.Store(end)
		p.segs = append(p.segs, s)
		p.mu.Unlock()
		return s, o
	}
	return nil, nil
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt-1, 4)) * time.Second // 1s, 2s, 4s, 8s, 16s
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// notifyDone tells a toss sender the parallel download finished, so it counts
// one download instead of one per connection. Older senders ignore it.
func notifyDone(link string, conns int, elapsed time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "toss/"+version)
	req.Header.Set("X-Toss-Event", "done")
	req.Header.Set("X-Toss-Conns", strconv.Itoa(conns))
	req.Header.Set("X-Toss-Elapsed", strconv.FormatInt(elapsed.Milliseconds(), 10))
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

func reachError(req *http.Request, err error) error {
	return fmt.Errorf("cannot reach %s. Check that toss is still running on the other machine and that both are on the same network. (%v)",
		req.URL.Host, strings.TrimPrefix(err.Error(), req.Method+" \""+req.URL.String()+"\": "))
}

// statusError explains a non-success answer from the sender.
func statusError(resp *http.Response) error {
	msg := readMessage(resp)
	switch resp.StatusCode {
	case http.StatusGone:
		return errors.New("this link has expired. Ask the sender to run toss again")
	case http.StatusNotFound:
		return errors.New("nothing is shared at this link. Check the link, or ask the sender to run toss again")
	}
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("the sender refused the download: %s", msg)
}
