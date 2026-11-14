# Toss

  <div align="center">
    <img src="https://github.com/snyype/toss/blob/main/Toss.png" alt="Toss by Snype" width="500"/>
</div>

**Toss** is a lightweight command-line tool designed to simplify file sharing over a local network. With Toss, you can quickly make files available for download from any device on the same network for a specified duration.

## Features

- **Instant Sharing**: Easily share files with others using a simple command.
- **Time-Limited Access**: Control how long the shared file remains accessible.
- **User-Friendly**: Minimal setup and straightforward usage.

## Installation

You can install Toss with the following command:

```
curl -o- https://raw.githubusercontent.com/snyype/toss/main/install.sh | bash
```

# Usage
Run the command with the desired file and an optional duration:

<div>
    <img src="https://github.com/snyype/toss/blob/main/toss-terminal.png" alt="Toss in terminal" width="500"/>
</div>

```
toss -t 300 myfile.txt      # share for 300 seconds (5 minutes)
toss myfile.txt -t 60       # flags can also go after the file
toss -p 9000 myfile.txt     # use a different port (a free one is picked if busy)
```

Toss prints a download link. On the other device:
```
curl -o myfile.txt http://<your-ip>:8080/<hostname>/download/myfile.txt
curl -C - -o myfile.txt http://<your-ip>:8080/<hostname>/download/myfile.txt   # resume an interrupted download
```
Or just open the link in a browser. Files are streamed from disk, so large files work without using extra memory, and downloads already in progress are allowed to finish when the link expires.

If the printed address is not reachable (VPN, multiple network adapters), Toss also lists your other addresses to try. On Windows, allow `toss.exe` through the firewall when prompted.

While sharing, Toss shows a live view refreshed every 3 seconds: each client's IP and machine name, the app downloading (curl, Chrome, ...), progress, current speed, elapsed time and ETA, plus total speed and open connections. Use `-q` to hide it. Large files (several GB) are fine.

# Logs
Every share and download is recorded in a `logs` folder inside the Toss install directory (e.g. `C:\toss\logs` or `~/.local/bin/logs`).

```
toss logs            # show the 20 most recent entries
toss logs -n 50      # show more
toss logs -path      # print the log file location
toss logs -clear     # delete the history
```

# Updating
```
toss update          # check GitHub for a newer release and ask before installing
toss update -check   # only check, don't install
toss update -y       # install without asking
```
The new version is downloaded, checked to make sure it runs, and only then swapped in, so a failed download leaves your current install untouched.

# Building
```
go build -o toss .            # Linux / macOS
go build -o toss.exe .        # Windows (icon and version info come from the rsrc_windows_*.syso files)
```
When the version or icon changes, regenerate the Windows resources (needs [go-winres](https://github.com/tc-hib/go-winres)):
```
go-winres simply --icon winres/toss.ico --manifest cli --arch amd64,386,arm64   --product-version 2.1.0.0 --file-version 2.1.0.0 --product-name Toss   --file-description "Toss - share files on your local network" --original-filename toss.exe
```

# License
This project is licensed under the MIT License. See the LICENSE file for details.

# Contribution
Contributions are welcome! Feel free to open issues or submit pull requests.

# Contact
For questions, please open an issue on the GitHub repository.
