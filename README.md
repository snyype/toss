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
toss -t 300 myfile.txt
```
This command shares myfile.txt for 300 seconds (5 minutes).

# Example Command
To download the shared file from another device, use:
```
curl -O http://<your-ip>:8080/<hostname>/download/myfile.txt
```

# License
This project is licensed under the MIT License. See the LICENSE file for details.

# Contribution
Contributions are welcome! Feel free to open issues or submit pull requests.

# Contact
For questions, please open an issue on the GitHub repository.
