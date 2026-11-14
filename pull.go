package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// runPull implements `toss pull <link>` (also available as `toss stream`):
// it downloads a file shared by another toss into <toss install dir>/downloads,
// resuming a previous interrupted attempt if a .part file is there.
func runPull(cmd string, args []string) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: toss %s <link>\n\n", cmd)
		fmt.Fprintln(fs.Output(), "Downloads a file shared with toss into toss's downloads folder.")
		fmt.Fprintln(fs.Output(), "Paste the link printed by `toss <file>` on the other machine.")
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(rest) != 1 {
		fmt.Fprintf(os.Stderr, "Error: give exactly one link. Usage: toss %s <link>\n", cmd)
		return 2
	}

	link := strings.TrimSpace(rest[0])
	if !strings.Contains(link, "://") {
		link = "http://" + link
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintln(os.Stderr, "Error: that does not look like a toss link, e.g. http://192.168.1.10:8080/host/download/file.zip")
		return 2
	}

	dir, err := appDir("downloads")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot create the downloads folder:", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

	logger := openLogger()
	name := safeFileName(path.Base(u.Path))
	part := filepath.Join(dir, name+".part")
	start := time.Now()

	res, err := fetch(ctx, u.String(), part)
	elapsed := time.Since(start)
	if err != nil {
		status := "failed"
		switch {
		case ctx.Err() != nil:
			status = "stopped"
			fmt.Println()
			fmt.Printf("%s Stopped. %s saved so far; run the same command again to resume.\n", yellow("!"), humanBytes(res.got))
		case res.resumable:
			status = "interrupted"
			fmt.Println()
			fmt.Fprintf(os.Stderr, "%s Download interrupted: %v\n  %s saved so far; run the same command again to resume.\n", red("✗"), err, humanBytes(res.got))
		default:
			fmt.Fprintln(os.Stderr, red("✗ ")+err.Error())
		}
		logger.write(logEntry{Event: "pull", File: name, URL: link, Bytes: res.got, Status: status,
			Elapsed: elapsed.Round(time.Millisecond).String()})
		if status == "stopped" {
			return 130
		}
		return 1
	}

	if res.name != "" {
		name = res.name
	}
	final := uniquePath(dir, name)
	if err := os.Rename(part, final); err != nil {
		fmt.Fprintf(os.Stderr, "%s Downloaded, but could not rename %s: %v\n", red("✗"), part, err)
		return 1
	}

	logger.write(logEntry{Event: "pull", File: filepath.Base(final), Path: final, URL: link, Bytes: res.size,
		Status: "completed", Elapsed: elapsed.Round(time.Millisecond).String()})
	fmt.Printf("%s Downloaded %s (%s in %s, avg %s/s)\n", green("✓"), bold(filepath.Base(final)),
		humanBytes(res.size), elapsed.Round(100*time.Millisecond), humanBytes(rate(res.received, elapsed)))
	fmt.Printf("Saved to: %s\n", green(final))
	return 0
}

type fetchResult struct {
	name      string // file name the sender suggested (Content-Disposition)
	size      int64  // full size of the file
	got       int64  // bytes in the .part file
	received  int64  // bytes transferred by this run
	resumable bool   // a partial download is saved and can be resumed
}

// fetch downloads link into part, continuing from the bytes already in part.
func fetch(ctx context.Context, link, part string) (fetchResult, error) {
	var res fetchResult
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	// No overall timeout: large files must be able to stream for as long as they need.
	client := &http.Client{Transport: transport}

	for attempt := 0; ; attempt++ {
		var offset int64
		if st, err := os.Stat(part); err == nil {
			offset = st.Size()
		}
		res.got = offset

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return res, err
		}
		req.Header.Set("User-Agent", "toss/"+version)
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("cannot reach %s. Check that toss is still running on the other machine and that both are on the same network. (%v)", req.URL.Host, err)
		}

		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && attempt == 0 {
			// The saved part does not fit the file being shared now: start over.
			resp.Body.Close()
			os.Remove(part)
			continue
		}

		res.name = dispositionName(resp.Header.Get("Content-Disposition"))
		flags := os.O_CREATE | os.O_WRONLY
		switch resp.StatusCode {
		case http.StatusOK:
			flags |= os.O_TRUNC
			offset = 0
		case http.StatusPartialContent:
			if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
				resp.Body.Close()
				return res, errors.New("the sender returned an unexpected byte range; delete " + part + " and try again")
			}
			flags |= os.O_APPEND
			fmt.Printf("Resuming from %s...\n", humanBytes(offset))
		default:
			msg := readMessage(resp)
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusGone:
				return res, errors.New("this link has expired. Ask the sender to run toss again")
			case http.StatusNotFound:
				return res, errors.New("nothing is shared at this link. Check the link, or ask the sender to run toss again")
			}
			if msg == "" {
				msg = resp.Status
			}
			return res, fmt.Errorf("the sender refused the download: %s", msg)
		}

		res.size = -1
		if resp.ContentLength >= 0 {
			res.size = offset + resp.ContentLength
		}

		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			resp.Body.Close()
			return res, fmt.Errorf("cannot write %s: %w", part, err)
		}
		res.got = offset

		var got atomic.Int64
		got.Store(offset)
		stop := startPullProgress(&got, res.size)
		n, copyErr := io.Copy(f, &countReader{r: resp.Body, n: &got})
		stop()
		resp.Body.Close()
		closeErr := f.Close()

		res.received = n
		res.got = offset + n
		res.resumable = res.got > 0
		switch {
		case copyErr != nil:
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, copyErr
		case closeErr != nil:
			return res, fmt.Errorf("cannot write %s: %w", part, closeErr)
		case res.size >= 0 && res.got < res.size:
			return res, fmt.Errorf("the connection closed after %s of %s", humanBytes(res.got), humanBytes(res.size))
		}
		res.size = res.got
		return res, nil
	}
}

type countReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// startPullProgress redraws a one-line progress bar while downloading.
func startPullProgress(got *atomic.Int64, size int64) (stop func()) {
	if !stdoutIsTerminal() {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		lastN, lastAt := got.Load(), time.Now()
		var speed float64
		width := 0
		draw := func() {
			now := time.Now()
			n := got.Load()
			if dt := now.Sub(lastAt).Seconds(); dt >= 0.2 {
				speed = 0.6*speed + 0.4*float64(n-lastN)/dt
				lastN, lastAt = n, now
			}
			eta := "-"
			if speed > 0 && size > n {
				eta = (time.Duration(float64(size-n)/speed) * time.Second).Round(time.Second).String()
			}
			line := fmt.Sprintf("  %s %-12s ETA %s", progressCell(n, size), humanBytes(int64(speed))+"/s", eta)
			pad := width - visibleLen(line)
			if pad < 0 {
				pad = 0
			}
			width = visibleLen(line)
			fmt.Printf("\r%s%s", line, strings.Repeat(" ", pad))
		}
		for {
			draw()
			select {
			case <-done:
				draw()
				fmt.Println()
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); <-finished }
}

// readMessage returns the first line of a short error body from the sender.
func readMessage(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return line
}

func dispositionName(h string) string {
	if h == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(h)
	if err != nil || params["filename"] == "" {
		return ""
	}
	return safeFileName(params["filename"])
}

// safeFileName turns a name chosen by the sender into one that is safe to
// create inside the downloads folder: no folders, no characters Windows
// rejects, no reserved device names.
func safeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	stem := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	return name
}

// uniquePath returns dir/name, or dir/"name (1).ext", "name (2).ext"... if
// that file already exists, so earlier downloads are never overwritten.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		p = filepath.Join(dir, stem+" ("+strconv.Itoa(i)+")"+ext)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
}
