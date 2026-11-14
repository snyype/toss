package main

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const tempPrefix = "share-"

// folderStats is what a pre-zip scan of a folder finds.
type folderStats struct {
	files, dirs, skipped int
	bytes                int64
}

// shareFolder asks the user to confirm, then zips dir into toss's own temp
// folder. It returns the zip path and a cleanup func that deletes it. A code of
// -1 means "go ahead and share zipPath"; anything else is the exit code.
func shareFolder(dir string, assumeYes bool) (zipPath string, cleanup func(), code int) {
	cleanup = func() {}
	zipName := zipNameFor(dir)

	base, err := appDir("temp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot create a temp folder for the zip:", err)
		return "", cleanup, 1
	}
	removeStaleTemp(base)

	fmt.Printf("Scanning %s...\n", dir)
	st := scanFolder(dir, base)

	if !assumeYes {
		if !stdinIsTerminal() {
			fmt.Fprintf(os.Stderr, "Error: %s is a folder. Run %s to zip and share it without a prompt.\n",
				filepath.Base(dir), cyan("toss -y <folder>"))
			return "", cleanup, 2
		}
		fmt.Printf("%s %s is a folder (%s, %s). Zip it and share as %s? %s ",
			cyan("?"), bold(filepath.Base(dir)), plural(st.files, "file"), humanBytes(st.bytes),
			bold(zipName), dim("[Y/n]"))
		answer, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		// No input at all (EOF, e.g. stdin redirected from NUL) is not a yes.
		if (readErr != nil && answer == "") || (answer != "" && answer != "y" && answer != "yes") {
			if readErr != nil {
				fmt.Println()
			}
			fmt.Println("Cancelled. Nothing was shared.")
			return "", cleanup, 0
		}
	}

	workDir, err := os.MkdirTemp(base, tempPrefix+strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: cannot create a temp folder for the zip:", err)
		return "", cleanup, 1
	}
	cleanup = func() { os.RemoveAll(workDir) }
	zipPath = filepath.Join(workDir, zipName)

	// Ctrl+C while zipping stops cleanly and deletes the partial zip.
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { signal.Stop(sig); cancel() }()

	start := time.Now()
	warnings, err := zipFolder(ctx, dir, zipPath, base, st)
	if err != nil {
		cleanup()
		if ctx.Err() != nil {
			fmt.Println("\nCancelled. The partial zip was deleted.")
			return "", func() {}, 130
		}
		fmt.Fprintln(os.Stderr, "\nError: zipping failed:", err)
		return "", func() {}, 1
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "%s skipped %s\n", yellow("!"), w)
	}
	if info, err := os.Stat(zipPath); err == nil {
		fmt.Printf("%s Zipped %s into %s (%s) in %s.\n", green("✓"), plural(st.files, "file"),
			zipName, humanBytes(info.Size()), time.Since(start).Round(100*time.Millisecond))
	}
	return zipPath, cleanup, -1
}

// zipNameFor picks the download name for a folder, e.g. "photos" -> "photos.zip".
func zipNameFor(dir string) string {
	name := filepath.Base(filepath.Clean(dir))
	if name == "" || name == "." || strings.ContainsAny(name, `/\:`) {
		name = "folder" // e.g. a drive root such as C:\
	}
	return name + ".zip"
}

// scanFolder counts what will go into the zip, so the prompt can show it.
func scanFolder(dir, skipDir string) folderStats {
	var st folderStats
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			if samePath(path, skipDir) {
				return fs.SkipDir
			}
			st.dirs++
		case d.Type().IsRegular():
			st.files++
			if info, err := d.Info(); err == nil {
				st.bytes += info.Size()
			}
		default:
			st.skipped++
		}
		return nil
	})
	return st
}

// zipFolder writes dir into a zip at dst. The folder's own name becomes the
// top-level entry, empty folders are kept, symlinks are not followed, and
// files that cannot be read are skipped and reported as warnings. skipDir
// (toss's temp folder) is never included, so zipping the toss install folder
// does not try to zip the zip being written.
func zipFolder(ctx context.Context, dir, dst, skipDir string, st folderStats) (warnings []string, err error) {
	out, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)
	zw := zip.NewWriter(bw)

	root := strings.TrimSuffix(zipNameFor(dir), ".zip")
	var doneFiles atomic.Int64
	var doneBytes atomic.Int64

	stopProgress := startZipProgress(st, &doneFiles, &doneBytes)
	defer stopProgress()

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if path == dir {
				return err
			}
			warnings = append(warnings, fmt.Sprintf("%s (%v)", path, err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := root
		if rel != "." {
			name = root + "/" + filepath.ToSlash(rel)
		}

		switch {
		case d.IsDir():
			if samePath(path, skipDir) {
				return fs.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s (%v)", path, err))
				return fs.SkipDir
			}
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name = name + "/"
			_, err = zw.CreateHeader(hdr)
			return err

		case d.Type().IsRegular():
			return addFile(ctx, zw, path, name, &doneBytes, &warnings, &doneFiles)

		default:
			if d.Type()&fs.ModeSymlink != 0 {
				warnings = append(warnings, path+" (symlink, not followed)")
			} else {
				warnings = append(warnings, path+" (not a regular file)")
			}
			return nil
		}
	})
	if walkErr != nil {
		return warnings, walkErr
	}
	if err := zw.Close(); err != nil {
		return warnings, err
	}
	if err := bw.Flush(); err != nil {
		return warnings, err
	}
	return warnings, out.Close()
}

func addFile(ctx context.Context, zw *zip.Writer, path, name string, doneBytes *atomic.Int64, warnings *[]string, doneFiles *atomic.Int64) error {
	f, err := os.Open(path)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s (%v)", path, err))
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s (%v)", path, err))
		return nil
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	if alreadyCompressed(name) {
		hdr.Method = zip.Store // recompressing these only costs time
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, &ctxReader{ctx: ctx, r: f, n: doneBytes}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	doneFiles.Add(1)
	return nil
}

func alreadyCompressed(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".7z", ".rar",
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic",
		".mp3", ".m4a", ".aac", ".ogg", ".flac", ".opus",
		".mp4", ".m4v", ".mkv", ".mov", ".avi", ".webm",
		".docx", ".xlsx", ".pptx", ".apk", ".jar":
		return true
	}
	return false
}

// ctxReader stops a copy when ctx is cancelled and counts bytes read.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
	n   *atomic.Int64
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// startZipProgress redraws a one-line "Zipping..." status while zipping runs.
func startZipProgress(st folderStats, files, bytes *atomic.Int64) (stop func()) {
	if !stdoutIsTerminal() {
		fmt.Println("Zipping...")
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	draw := func() {
		line := fmt.Sprintf("Zipping... %s  %d/%d files", progressCell(bytes.Load(), st.bytes), files.Load(), st.files)
		fmt.Printf("\r%s", line)
	}
	go func() {
		defer close(finished)
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
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

func samePath(a, b string) bool {
	if b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// removeStaleTemp deletes zips left behind by toss runs that crashed or were
// killed. Folders are named share-<pid>-*, so a folder whose process is still
// running (another toss sharing right now) is left alone.
func removeStaleTemp(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		pidStr, _, _ := strings.Cut(strings.TrimPrefix(e.Name(), tempPrefix), "-")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid == self || processAlive(pid) {
			continue
		}
		os.RemoveAll(filepath.Join(base, e.Name()))
	}
}
