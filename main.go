package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type SharedFile struct {
	Filename   string
	Expiration time.Time
}

var (
	sharedFiles []SharedFile
	hostname    string
	version     = "1.0.0"
)

func main() {
	// Define command-line flags
	var duration int
	var showVersion bool
	var port int

	// Set default duration to 300 seconds
	flag.IntVar(&duration, "t", 300, "Duration in seconds (default is 300 seconds)")
	flag.IntVar(&port, "p", 8080, "Port for the HTTP server (default is 8080)")
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
		fmt.Println("Error: No filename provided. Specify a file as a positional argument.")
		return
	}
	fileName := args[0]

	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("Error getting current directory:", err)
		return
	}

	ip, err := getLocalIP()
	if err != nil {
		fmt.Println("Error fetching IP address:", err)
		return
	}

	hostname, err = os.Hostname()
	if err != nil {
		fmt.Println("Error fetching hostname:", err)
		return
	}

	// Add the shared file and expiration to the list
	sharedFiles = append(sharedFiles, SharedFile{
		Filename:   fileName,
		Expiration: time.Now().Add(time.Duration(duration) * time.Second),
	})

	// Start the HTTP server
	http.HandleFunc("/"+hostname+"/download/", func(w http.ResponseWriter, r *http.Request) {
		fileDownloadHandler(w, r, currentDir)
	})

	go func() {
		fmt.Printf("Starting server on %s:%d...\n", ip, port)
		if err := http.ListenAndServe(fmt.Sprintf("%s:%d", ip, port), nil); err != nil {
			fmt.Println("Error starting server:", err)
		}
	}()

	// Generate the curl command
	fmt.Printf("File %s is now available for download for %d seconds.\n", fileName, duration)
	fmt.Printf("Use this curl command to download the file from another PC:\n")
	fmt.Printf("curl -O http://%s:%d/%s/download/%s\n", ip, port, hostname, fileName)

	// Block main goroutine to keep the server running
	select {}
}

func fileDownloadHandler(w http.ResponseWriter, r *http.Request, currentDir string) {
	filename := r.URL.Path[len("/"+hostname+"/download/"):]

	// Check if the requested file is in the list of shared files
	index := findSharedFileIndex(filename)
	if index == -1 {
		http.Error(w, "औलो दिदा हात निल्नु हुँदैन !", http.StatusForbidden)
		return
	}

	// Check if the file has expired before serving
	if time.Now().After(sharedFiles[index].Expiration) {
		removeSharedFile(filename)
		http.Error(w, "लिन्क को समय सक्यो , समय थप गर्न toss सँग -t 120  गर्नुहोस अनि २ मिनेट काम गर्छ !", http.StatusGone)
		return
	}

	// Prevent directory traversal
	filepath := filepath.Join(currentDir, filename)

	// Check if the file exists
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		http.Error(w, "फाईल फेला परेन !", http.StatusNotFound)
		return
	}

	// Serve the file
	http.ServeFile(w, r, filepath)
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
			sharedFiles = append(sharedFiles[:i], sharedFiles[i+1:]...) // Remove the file
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
