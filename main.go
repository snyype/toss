package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
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
		fmt.Printf("Starting server on %s:8080...\n", ip)
		if err := http.ListenAndServe(fmt.Sprintf("%s:8080", ip), nil); err != nil {
			fmt.Println("Error starting server:", err)
		}
	}()

	// Generate the curl command and the download link
	downloadURL := fmt.Sprintf("http://%s:8080/%s/download/%s", ip, hostname, fileName)
	fmt.Printf("File %s is now available for download for %d seconds.\n", fileName, duration)
	fmt.Printf("Use this curl command to download the file from another PC:\n")
	fmt.Printf("curl -O %s\n", downloadURL)

	// Generate and display QR code for the download URL
	fmt.Println("Generating QR code for the download link...")
	err = generateQRCode(downloadURL)
	if err != nil {
		fmt.Println("Error generating QR code:", err)
	}

	// Block main goroutine to keep the server running
	select {}
}

func fileDownloadHandler(w http.ResponseWriter, r *http.Request, currentDir string) {
	filename := r.URL.Path[len("/"+hostname+"/download/"):]

	// Check if the requested file is in the list of shared files
	index := findSharedFileIndex(filename)
	if index == -1 {
		http.Error(w, "Access denied!", http.StatusForbidden)
		return
	}

	// Check if the file has expired before serving
	if time.Now().After(sharedFiles[index].Expiration) {
		removeSharedFile(filename)
		http.Error(w, "Link expired, extend time with toss -t 120.", http.StatusGone)
		return
	}

	// Prevent directory traversal
	filepath := filepath.Join(currentDir, filename)

	// Check if the file exists
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		http.Error(w, "File not found!", http.StatusNotFound)
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

// Generate the QR code and display it in the terminal
func generateQRCode(data string) error {
	// Create a new QR code with low error correction for a smaller size
	qr, err := qrcode.New(data, qrcode.Low)
	if err != nil {
		return err
	}

	fmt.Println(err)
	// Display the QR code in the terminal in ASCII
	printSmallQRCode(qr)

	return nil
}

// Function to print a smaller ASCII representation of the QR code
func printSmallQRCode(qr *qrcode.QRCode) {
	// Convert the QR code to string format
	qrString := qr.ToString(false) // false for black and white blocks

	// Split the string into lines and print
	for _, line := range strings.Split(qrString, "\n") {
		fmt.Println(line)
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
