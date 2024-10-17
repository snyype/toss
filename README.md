# Toss

**Toss** is a lightweight command-line tool designed to simplify file sharing over a local network. With Toss, you can quickly make files available for download from any device on the same network for a specified duration.

## Features

- **Instant Sharing**: Easily share files with others using a simple command.
- **Time-Limited Access**: Control how long the shared file remains accessible.
- **User-Friendly**: Minimal setup and straightforward usage.

## Installation

### Step 1: Clone the Repository

```css
git clone https://github.com/YOUR_USERNAME/toss.git
cd toss
```

Step 2: Build the Executable
Run the following command to build the Toss executable:

For Linux/macOS:

```css
go build -o toss
```
For Windows users, build with:
```css
go build -o toss.exe
```

Step 3: Add Toss to Your PATH

On Windows:

- Search for "Environment Variables" in the Start menu.
- Edit the "Path" variable to include the directory containing toss.exe.
- On Linux/macOS: Add the following line to your ~/.bashrc or ~/.zshrc:

```
export PATH=$PATH:/path/to/toss-directory
```
# Usage
Run the command with the desired file and an optional duration:


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
