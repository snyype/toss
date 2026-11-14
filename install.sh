#!/bin/bash
# Installs the latest Toss release for this machine.
#   curl -o- https://raw.githubusercontent.com/snyype/toss/main/install.sh | bash
set -e

REPO="snyype/toss"
OS_TYPE=$(uname)
ARCH=$(uname -m)

case "$OS_TYPE" in
    Linux)                    PREFIX="linux-";  EXT="";     DEST_DIR="$HOME/.local/bin"; RC="$HOME/.bashrc" ;;
    Darwin)                   PREFIX="darwin-"; EXT="";     DEST_DIR="$HOME/.local/bin"; RC="$HOME/.zshrc" ;;
    CYGWIN*|MINGW*|MSYS*)     PREFIX="win-";    EXT=".exe"; DEST_DIR="C:/toss";          RC="" ;;
    *) echo "Unsupported OS: $OS_TYPE"; exit 1 ;;
esac

case "$ARCH" in
    arm64|aarch64) ASSETS="toss-arm64$EXT" ;;
    *)             ASSETS="toss$EXT" ;;
esac
# Windows/macOS on ARM can fall back to the amd64 build (emulation).
if [ "$ASSETS" != "toss$EXT" ] && [ "$PREFIX" != "linux-" ]; then
    ASSETS="$ASSETS toss$EXT"
fi

echo "Finding the latest Toss release..."
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=100" \
    | grep -o "\"tag_name\": *\"$PREFIX[0-9][0-9.]*\"" \
    | sed "s/.*\"\($PREFIX[0-9.]*\)\"/\1/" \
    | awk -v p="$PREFIX" '{
        v = substr($0, length(p) + 1); n = split(v, a, ".")
        key = sprintf("%06d%06d%06d", a[1], a[2], a[3])
        if (key > best) { best = key; tag = $0 }
      } END { print tag }')

if [ -z "$TAG" ]; then
    echo "Could not find a Toss release for $OS_TYPE. See https://github.com/$REPO/releases"
    exit 1
fi

mkdir -p "$DEST_DIR"
DESTINATION="$DEST_DIR/toss$EXT"
TMP="$DESTINATION.download"

OK=""
for ASSET in $ASSETS; do
    URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
    echo "Downloading Toss $TAG ($ASSET)..."
    if curl -fL --progress-bar "$URL" -o "$TMP"; then OK=1; break; fi
done
if [ -z "$OK" ]; then
    rm -f "$TMP"
    echo "Download failed. Get Toss manually from https://github.com/$REPO/releases/tag/$TAG"
    exit 1
fi
mv -f "$TMP" "$DESTINATION"
chmod +x "$DESTINATION" 2>/dev/null || true

if [ -n "$RC" ]; then
    if ! [[ ":$PATH:" == *":$DEST_DIR:"* ]]; then
        echo "Adding $DEST_DIR to PATH in $RC..."
        echo "export PATH=\"$DEST_DIR:\$PATH\"" >> "$RC"
        echo "Open a new terminal (or run: source $RC) to use toss."
    fi
else
    # Add C:\toss to the *user* PATH if it isn't there yet (no admin needed,
    # and unlike setx it doesn't truncate long PATH values).
    powershell -NoProfile -Command '
        $p = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not (($p -split ";") -contains "C:\toss")) {
            [Environment]::SetEnvironmentVariable("Path", (($p.TrimEnd(";") + ";C:\toss").TrimStart(";")), "User")
            Write-Host "Added C:\toss to your PATH. Open a new terminal to use toss."
        }'
fi

echo "Toss $TAG installed to $DESTINATION. Run 'toss' to get started."
