#!/bin/bash

# Define the Toss executable URL
OS_TYPE=$(uname)

if [[ "$OS_TYPE" == "Linux" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/unix-1.0.0/toss"
    DESTINATION="$HOME/.local/bin/toss"
elif [[ "$OS_TYPE" == "Darwin" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/unix-1.0.0/toss"
    DESTINATION="$HOME/.local/bin/toss"
elif [[ "$OS_TYPE" == "CYGWIN"* || "$OS_TYPE" == "MINGW"* || "$OS_TYPE" == "MSYS"* ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/win-1.0.0/toss.exe"
    DESTINATION="/c/toss/toss.exe"  # Change this line to your desired path
else
    echo "Unsupported OS: $OS_TYPE"
    exit 1
fi

# Create destination directory if it doesn't exist
mkdir -p "$(dirname "$DESTINATION")"

# Download the Toss executable
echo "Downloading Toss..."
curl -L "$EXECUTABLE_URL" -o "$DESTINATION"

# Make the executable runnable (for Linux and macOS)
if [[ "$OS_TYPE" != "CYGWIN"* && "$OS_TYPE" != "MINGW"* && "$OS_TYPE" != "MSYS"* ]]; then
    chmod +x "$DESTINATION"
fi

# Add to PATH for Windows
if [[ "$OS_TYPE" == "CYGWIN"* || "$OS_TYPE" == "MINGW"* || "$OS_TYPE" == "MSYS"* ]]; then
    # Add C:\toss to the Windows PATH using cmd.exe
    cmd.exe /C "setx PATH \"%PATH%;C:\\toss\""
    echo "C:\\toss has been added to your PATH. Please restart your terminal for the changes to take effect."
fi

# Inform user of successful installation
echo "Toss installed successfully."
