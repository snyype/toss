#!/bin/bash

# Define the Toss executable URL
OS_TYPE=$(uname)

if [[ "$OS_TYPE" == "Linux" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/linux-1.0.0/toss"
    DESTINATION="$HOME/.local/bin/toss"

    # Create destination directory if it doesn't exist
    mkdir -p "$HOME/.local/bin"

    # Download the Toss executable
    echo "Downloading Toss..."
    curl -L "$EXECUTABLE_URL" -o "$DESTINATION"

    # Make the file executable
    chmod +x "$DESTINATION"

    # Add to current PATH if it's not already included
    if ! [[ ":$PATH:" == *":$HOME/.local/bin:"* ]]; then
        echo "Adding $HOME/.local/bin to current PATH..."
        echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.bashrc"
        export PATH="$HOME/.local/bin:$PATH"  # Update current session PATH
    fi

    echo "Toss installed successfully. Use the command 'toss' to start using it."
    exit 0

elif [[ "$OS_TYPE" == "Darwin" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/darwin-1.0.0/toss"
    DESTINATION="$HOME/.local/bin/toss"

    # Create destination directory if it doesn't exist
    mkdir -p "$HOME/.local/bin"

    # Download the Toss executable
    echo "Downloading Toss..."
    curl -L "$EXECUTABLE_URL" -o "$DESTINATION"

    # Make the file executable
    chmod +x "$DESTINATION"

    # Add to current PATH if it's not already included
    if ! [[ ":$PATH:" == *":$HOME/.local/bin:"* ]]; then
        echo "Adding $HOME/.local/bin to current PATH..."
        echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.zshrc"  # Updated for zsh
        export PATH="$HOME/.local/bin:$PATH"  # Update current session PATH
    fi

    echo "Toss installed successfully. Use the command 'toss' to start using it."
    exit 0

elif [[ "$OS_TYPE" == "CYGWIN"* || "$OS_TYPE" == "MINGW"* || "$OS_TYPE" == "MSYS"* ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/download/win-1.0.0/toss.exe"
    DESTINATION="C:/toss/toss.exe"  # Set destination for Windows

    # Create destination directory if it doesn't exist
    mkdir -p "C:/toss"

    # Download the Toss executable
    echo "Downloading Toss..."
    curl -L "$EXECUTABLE_URL" -o "$DESTINATION"

    # Add to current PATH
    echo "Adding C:\\toss to current PATH..."
    set PATH=C:\toss;%PATH%  # Update current session PATH

    # Retrieve current PATH to preserve existing entries
    CURRENT_PATH=$(powershell -command "[System.Environment]::GetEnvironmentVariable('Path', [System.EnvironmentVariableTarget]::Machine)")

    # Append C:\toss to the existing PATH and set it persistently
    setx PATH "C:\toss;$CURRENT_PATH"

    echo "Toss installed successfully. Use the command 'toss' to start using it."
    exit 0
else
    echo "Unsupported OS: $OS_TYPE"
    exit 1  
fi
