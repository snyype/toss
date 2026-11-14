package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const releasesAPI = "https://api.github.com/repos/snyype/toss/releases?per_page=50"

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Releases are tagged per platform, e.g. win-2.1.0, linux-2.1.0, darwin-2.1.0.
// Each release has a plain "toss"/"toss.exe" asset (amd64) and, from 2.1.0,
// "toss-arm64"/"toss-arm64.exe" for ARM machines.
func platformTagPrefix() (prefix string, assets []string, ok bool) {
	ext := ""
	switch runtime.GOOS {
	case "windows":
		prefix, ext = "win-", ".exe"
	case "linux":
		prefix = "linux-"
	case "darwin":
		prefix = "darwin-"
	default:
		return "", nil, false
	}
	if runtime.GOARCH == "arm64" {
		assets = append(assets, "toss-arm64"+ext)
		// Windows and macOS on ARM can still run the amd64 build under emulation.
		if runtime.GOOS != "linux" {
			assets = append(assets, "toss"+ext)
		}
		return prefix, assets, true
	}
	return prefix, []string{"toss" + ext}, true
}

// runUpdate implements `toss update`.
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	yes := fs.Bool("y", false, "install without asking")
	checkOnly := fs.Bool("check", false, "only check, don't install")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: toss update [-check] [-y]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	prefix, assetNames, ok := platformTagPrefix()
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: automatic updates are not available for %s.\n", runtime.GOOS)
		return 1
	}

	stop := spinner("Checking for updates")
	rel, latest, err := latestRelease(prefix)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, red("✗ ")+"Could not check for updates: "+err.Error())
		return 1
	}

	current, _ := parseVersion(version)
	if latest == nil || compareVersions(latest, current) <= 0 {
		fmt.Printf("%s You're on the latest version %s\n", green("✓"), bold("v"+version))
		return 0
	}

	var asset *ghAsset
	for _, want := range assetNames {
		for i := range rel.Assets {
			if rel.Assets[i].Name == want {
				asset = &rel.Assets[i]
				break
			}
		}
		if asset != nil {
			break
		}
	}

	latestStr := strings.TrimPrefix(rel.TagName, prefix)
	printUpdateBox(latestStr, rel, asset)

	if asset == nil {
		fmt.Fprintf(os.Stderr, "%s This release has no %s download for your system. Get it from:\n  %s\n", yellow("!"), assetNames[0], rel.HTMLURL)
		return 1
	}
	if *checkOnly {
		fmt.Printf("Run %s to install it.\n", cyan("toss update"))
		return 0
	}

	if !*yes {
		if !stdinIsTerminal() {
			fmt.Println("Run " + cyan("toss update -y") + " to install without a prompt.")
			return 1
		}
		fmt.Printf("%s Update to %s now? %s ", cyan("?"), bold("v"+latestStr), dim("[Y/n]"))
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Update cancelled.")
			return 0
		}
	}

	exe, err := currentExecutable()
	if err != nil {
		fmt.Fprintln(os.Stderr, red("✗ ")+"Cannot locate the toss executable: "+err.Error())
		return 1
	}

	if err := installUpdate(exe, asset.URL, asset.Size); err != nil {
		fmt.Fprintln(os.Stderr, red("✗ ")+"Update failed: "+err.Error())
		if errors.Is(err, os.ErrPermission) {
			if runtime.GOOS == "windows" {
				fmt.Fprintln(os.Stderr, "  Try again from a terminal opened with \"Run as administrator\".")
			} else {
				fmt.Fprintln(os.Stderr, "  Try again with sudo, or check permissions on "+filepath.Dir(exe))
			}
		}
		return 1
	}

	fmt.Printf("%s Updated toss %s → %s\n", green("✓"), dim("v"+version), bold(green("v"+latestStr)))
	return 0
}

func latestRelease(prefix string) (*ghRelease, []int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "toss-cli/"+version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("no connection to GitHub (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, nil, errors.New("GitHub rate limit reached, try again in a few minutes")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub returned %s", resp.Status)
	}

	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, nil, fmt.Errorf("unexpected response from GitHub: %v", err)
	}

	var best *ghRelease
	var bestV []int
	for i := range releases {
		r := &releases[i]
		if r.Draft || r.Prerelease || !strings.HasPrefix(r.TagName, prefix) {
			continue
		}
		v, ok := parseVersion(strings.TrimPrefix(r.TagName, prefix))
		if !ok {
			continue
		}
		if best == nil || compareVersions(v, bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best, bestV, nil
}

func parseVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}

func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func printUpdateBox(latest string, rel *ghRelease, asset *ghAsset) {
	lines := []string{
		bold("A new version of toss is available!"),
		"",
		fmt.Sprintf("%s  →  %s", dim("v"+version), bold(green("v"+latest))),
	}
	if !rel.PublishedAt.IsZero() {
		lines = append(lines, dim("Released "+rel.PublishedAt.Local().Format("Jan 2, 2006")))
	}
	if asset != nil {
		lines = append(lines, dim("Download size "+humanBytes(asset.Size)))
	}

	if notes := releaseNotes(rel.Body, 8); len(notes) > 0 {
		lines = append(lines, "", bold("What's new:"))
		for _, n := range notes {
			lines = append(lines, "  "+n)
		}
	}
	lines = append(lines, "", dim(rel.HTMLURL))

	width := 0
	for _, l := range lines {
		if w := visibleLen(l); w > width {
			width = w
		}
	}
	if width > 72 {
		width = 72
	}

	fmt.Println()
	fmt.Println(cyan("╭" + strings.Repeat("─", width+4) + "╮"))
	for _, l := range lines {
		if visibleLen(l) > width {
			l = truncate(stripANSI(l), width)
		}
		fmt.Printf("%s  %s%s  %s\n", cyan("│"), l, strings.Repeat(" ", width-visibleLen(l)), cyan("│"))
	}
	fmt.Println(cyan("╰" + strings.Repeat("─", width+4) + "╯"))
	fmt.Println()
}

func releaseNotes(body string, max int) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		l = strings.TrimLeft(l, "#")
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") {
			l = "• " + l[2:]
		}
		out = append(out, l)
		if len(out) == max {
			out = append(out, dim("…"))
			break
		}
	}
	return out
}

func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// installUpdate downloads the new binary next to the current one, checks that
// it runs, then swaps it in. A running .exe can't be overwritten on Windows but
// it can be renamed, so the old binary is moved aside to <exe>.old and removed
// on the next run.
func installUpdate(exe, url string, size int64) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".toss-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := download(url, size, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}

	// Sanity check: the new binary must start and report its version.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, tmpPath, "-v").CombinedOutput(); err != nil || !strings.Contains(string(out), "toss") {
		return errors.New("the downloaded file is not a working toss binary; nothing was changed")
	}

	old := exe + ".old"
	os.Remove(old)
	if runtime.GOOS == "windows" {
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			os.Rename(old, exe) // put the original back
			return err
		}
		return nil
	}
	return os.Rename(tmpPath, exe)
}

func download(url string, size int64, w io.Writer) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "toss-cli/"+version)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	if size <= 0 {
		size = resp.ContentLength
	}

	bar := &progressBar{total: size, label: "Downloading"}
	n, err := io.Copy(w, io.TeeReader(resp.Body, bar))
	bar.finish()
	if err != nil {
		return fmt.Errorf("download interrupted: %v", err)
	}
	if size > 0 && n != size {
		return fmt.Errorf("download incomplete (%s of %s)", humanBytes(n), humanBytes(size))
	}
	return nil
}

type progressBar struct {
	total, done int64
	label       string
	last        time.Time
}

func (p *progressBar) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 80*time.Millisecond || p.done == p.total {
		p.last = time.Now()
		p.draw()
	}
	return len(b), nil
}

func (p *progressBar) draw() {
	const width = 30
	if p.total <= 0 {
		fmt.Fprintf(os.Stderr, "\r%s %s", p.label, humanBytes(p.done))
		return
	}
	frac := float64(p.done) / float64(p.total)
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * width)
	bar := green(strings.Repeat("█", filled)) + dim(strings.Repeat("░", width-filled))
	fmt.Fprintf(os.Stderr, "\r%s %s %3.0f%%  %s / %s ", p.label, bar, frac*100, humanBytes(p.done), humanBytes(p.total))
}

func (p *progressBar) finish() {
	p.draw()
	fmt.Fprintln(os.Stderr)
}

// spinner shows an animated message until the returned func is called.
func spinner(msg string) func() {
	if !stdoutIsTerminal() {
		fmt.Println(msg + "...")
		return func() {}
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; ; i++ {
			select {
			case <-done:
				fmt.Printf("\r%s\r", strings.Repeat(" ", len(msg)+4))
				return
			default:
				fmt.Printf("\r%s %s", cyan(frames[i%len(frames)]), msg)
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
	return func() { close(done); <-finished }
}

// cleanupOldBinary removes the <exe>.old left behind by a Windows update.
func cleanupOldBinary() {
	if exe, err := currentExecutable(); err == nil {
		os.Remove(exe + ".old")
	}
}
