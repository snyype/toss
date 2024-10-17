#!/bin/bash

# Define the Toss executable URL
OS_TYPE=$(uname)

if [[ "$OS_TYPE" == "Linux" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/latest/download/toss"
    DESTINATION="$HOME/.local/bin/toss"
elif [[ "$OS_TYPE" == "Darwin" ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/latest/download/toss"
    DESTINATION="$HOME/.local/bin/toss"
elif [[ "$OS_TYPE" == "CYGWIN"* || "$OS_TYPE" == "MINGW"* || "$OS_TYPE" == "MSYS"* ]]; then
    EXECUTABLE_URL="https://github.com/snyype/toss/releases/latest/download/toss.exe"
    DESTINATION="$USERPROFILE/toss.exe"
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

# Add to PATH if not already present
if [[ "$SHELL" == *"bash"* ]]; then
    if ! grep -q ".local/bin" "$HOME/.bashrc"; then
        echo "export PATH=\$PATH:\$HOME/.local/bin" >> "$HOME/.bashrc"
        echo "Toss installed successfully. Please restart your terminal or run 'source ~/.bashrc' to use it."
    else
        echo "Toss installed successfully. Please restart your terminal to use it."
    fi
elif [[ "$SHELL" == *"zsh"* ]]; then
    if ! grep -q ".local/bin" "$HOME/.zshrc"; then
        echo "export PATH=\$PATH:\$HOME/.local/bin" >> "$HOME/.zshrc"
        echo "Toss installed successfully. Please restart your terminal or run 'source ~/.zshrc' to use it."
    else
        echo "Toss installed successfully. Please restart your terminal to use it."
    fi
else
    echo "Toss installed successfully. Please add ~/.local/bin to your PATH manually."
fi
