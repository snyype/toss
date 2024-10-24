package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type SharedFile struct {
	Filename   string
	IsDir      bool
	Expiration time.Time
}

var (
	sharedFiles []SharedFile
	hostname    string
	version     = "1.0.0"
	mu          sync.Mutex
)

func main() {
	// Define command-line flags
	var duration int
	var showVersion bool

	// Set default duration to 300 seconds
	flag.IntVar(&duration, "t", 300, "Duration in seconds (default is 300 seconds)")
	flag.BoolVar(&showVersion, "v", false, "Show version")
	flag.Parse()

	// Show version and exit if -v flag is provided
	if showVersion {
		fmt.Printf("toss version %s\n", version)
		return
	}

	// Get positional arguments (non-flag arguments)
	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Error: No filename or directory provided. Specify a file or directory as a positional argument.")
		return
	}
	fileName := args[0]

	currentDir, err := os.Getwd()
	checkError("Error getting current directory:", err)

	ip, err := getLocalIP()
	checkError("Error fetching IP address:", err)

	hostname, err = os.Hostname()
	checkError("Error fetching hostname:", err)

	isDir := isDirectory(fileName)

	mu.Lock()
	sharedFiles = append(sharedFiles, SharedFile{
		Filename:   fileName,
		IsDir:      isDir,
		Expiration: time.Now().Add(time.Duration(duration) * time.Second),
	})
	mu.Unlock()

	http.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		fileDownloadHandler(w, r, currentDir)
	})

	go func() {
		fmt.Printf("Starting server on %s:8080...\n", ip)
		if err := http.ListenAndServe(fmt.Sprintf("%s:8080", ip), nil); err != nil {
			fmt.Println("Error starting server:", err)
		}
	}()

	fmt.Printf("Item %s is now available for download for %d seconds.\n", fileName, duration)
	fmt.Printf("Use this curl command to download the item from another PC:\n")
	fmt.Printf("curl -O http://%s:8080/download/%s\n", ip, fileName)

	select {}
}

func fileDownloadHandler(w http.ResponseWriter, r *http.Request, currentDir string) {
	filename := r.URL.Path[len("/download/"):]

	mu.Lock()
	index := findSharedFileIndex(filename)
	mu.Unlock()

	if index == -1 {
		http.Error(w, "औलो दिदा हात निल्नु हुँदैन !", http.StatusForbidden)
		return
	}

	mu.Lock()
	if time.Now().After(sharedFiles[index].Expiration) {
		removeSharedFile(filename)
		mu.Unlock()
		http.Error(w, "लिन्क को समय सक्यो , समय थप गर्न toss सँग -t 120 गर्नुहोस अनि २ मिनेट काम गर्छ !", http.StatusGone)
		return
	}
	mu.Unlock()

	filepath := filepath.Join(currentDir, filename)
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		http.Error(w, "फाईल फेला परेन !", http.StatusNotFound)
		return
	}

	if sharedFiles[index].IsDir {
		serveDirectoryAsZip(w, r, filepath)
	} else {
		http.ServeFile(w, r, filepath)
	}
}

func findSharedFileIndex(filename string) int {
	for i, sharedFile := range sharedFiles {
		if sharedFile.Filename == filename {
			return i
		}
	}
	return -1
}

func removeSharedFile(filename string) {
	for i, sharedFile := range sharedFiles {
		if sharedFile.Filename == filename {
			sharedFiles = append(sharedFiles[:i], sharedFiles[i+1:]...)
			break
		}
	}
}

func getLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				return ipNet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no valid IP address found")
}

func checkError(message string, err error) {
	if err != nil {
		fmt.Println(message, err)
		os.Exit(1)
	}
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func serveDirectoryAsZip(w http.ResponseWriter, r *http.Request, dirPath string) {
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.zip\"", filepath.Base(dirPath)))

	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath := strings.TrimPrefix(path, dirPath)
		if info.IsDir() {
			_, err := zipWriter.Create(relPath + "/")
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		writer, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, file)
		return err
	})
}
