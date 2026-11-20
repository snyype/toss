#!/bin/bash
# Installs the latest Toss release for this machine.
#   curl -o- https://raw.githubusercontent.com/snyype/toss/main/install.sh | bash
#
# No GitHub API calls: the API allows only 60 unauthenticated requests per hour
# per IP (shared NATs/offices/CI hit that fast and get HTTP 403). Downloads go
# through github.com's /releases/latest redirects instead, which aren't limited.
set -e

REPO="snyype/toss"
BASE="https://github.com/$REPO/releases"
OS_TYPE=$(uname -s)
ARCH=$(uname -m)

case "$OS_TYPE" in
    Linux)                OS="linux";   PREFIX="linux-";  EXT="";     DEST_DIR="$HOME/.local/bin"; RC="$HOME/.bashrc" ;;
    Darwin)               OS="darwin";  PREFIX="darwin-"; EXT="";     DEST_DIR="$HOME/.local/bin"; RC="$HOME/.zshrc" ;;
    CYGWIN*|MINGW*|MSYS*) OS="windows"; PREFIX="win-";    EXT=".exe"; DEST_DIR="C:/toss";          RC="" ;;
    *) echo "Unsupported OS: $OS_TYPE"; exit 1 ;;
esac

case "$ARCH" in
    x86_64|amd64)  CPU="amd64" ;;
    arm64|aarch64) CPU="arm64" ;;
    *) echo "Unsupported CPU architecture: $ARCH"; exit 1 ;;
esac

# Candidates, best first:
#   1. the combined release (tag v<x.y.z>, marked "latest"), e.g. toss-linux-amd64
#   2. the per-platform release (tag linux-<x.y.z>), e.g. toss / toss-arm64
#      (only releases before the combined release existed need this)
# macOS/Windows on ARM fall back to the amd64 build (Rosetta / emulation).
echo "Finding the latest Toss release..."
# /releases/latest redirects to /releases/tag/<tag>; read the tag without the API.
LATEST_TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE/latest" | sed -n 's|.*/tag/||p')
VERSION=$(printf '%s' "$LATEST_TAG" | sed 's/^[a-z]*-//; s/^v//')

# Pin the combined release to the resolved tag so the binary and its
# checksums.txt always come from the same release.
if [ -n "$LATEST_TAG" ]; then COMBINED="download/$LATEST_TAG"; else COMBINED="latest/download"; fi
CANDIDATES="$COMBINED/toss-$OS-$CPU$EXT"
if [ "$CPU" = "arm64" ] && [ "$OS" != "linux" ]; then
    CANDIDATES="$CANDIDATES $COMBINED/toss-$OS-amd64$EXT"
fi
if [ -n "$VERSION" ]; then
    if [ "$CPU" = "arm64" ]; then
        CANDIDATES="$CANDIDATES download/$PREFIX$VERSION/toss-arm64$EXT"
        [ "$OS" != "linux" ] && CANDIDATES="$CANDIDATES download/$PREFIX$VERSION/toss$EXT"
    else
        CANDIDATES="$CANDIDATES download/$PREFIX$VERSION/toss$EXT"
    fi
fi

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
    else return 1
    fi
}

# verify_checksum <downloaded file> <release path of the asset>
# Checks the file against checksums.txt from the same release. Hashes are never
# printed. Returns 1 on a mismatch; releases without checksums.txt (the old
# per-platform ones) are allowed through with a warning.
verify_checksum() {
    local file="$1" asset="${2##*/}" sums="$1.sha256" expected actual
    if ! curl -fsSL "$BASE/${2%/*}/checksums.txt" -o "$sums" 2>/dev/null; then
        rm -f "$sums"
        echo "Warning: this release has no checksums.txt, skipping checksum verification."
        return 0
    fi
    expected=$(awk -v a="$asset" '{n=$2; sub(/^\*/, "", n)} n == a {print tolower($1); exit}' "$sums")
    rm -f "$sums"
    if [ -z "$expected" ]; then
        echo "Error: checksums.txt has no entry for $asset."
        return 1
    fi
    if ! actual=$(sha256_of "$file"); then
        echo "Error: no sha256sum or shasum found, cannot verify the download."
        return 1
    fi
    if [ "$actual" != "$expected" ]; then
        echo "Error: checksum mismatch for $asset - the download is corrupted or was tampered with."
        return 1
    fi
    echo "Checksum verified."
}

mkdir -p "$DEST_DIR"
DESTINATION="$DEST_DIR/toss$EXT"
TMP="$DESTINATION.download"

OK=""
for PATH_PART in $CANDIDATES; do
    if curl -fsSLI -o /dev/null "$BASE/$PATH_PART" 2>/dev/null; then
        echo "Downloading Toss ${VERSION:+v$VERSION }for $OS/$CPU (${PATH_PART##*/})..."
        if curl -fL --progress-bar "$BASE/$PATH_PART" -o "$TMP"; then OK=1; break; fi
    fi
done
if [ -z "$OK" ]; then
    rm -f "$TMP"
    echo "Could not download a Toss build for $OS/$CPU. Get it manually from $BASE"
    exit 1
fi
if ! verify_checksum "$TMP" "$PATH_PART"; then
    rm -f "$TMP"
    echo "Toss was not installed. Try again, or get it manually from $BASE"
    exit 1
fi
mv -f "$TMP" "$DESTINATION"
chmod +x "$DESTINATION" 2>/dev/null || true
# Downloaded binaries on macOS get a quarantine flag that blocks unsigned apps.
if [ "$OS" = "darwin" ]; then
    xattr -d com.apple.quarantine "$DESTINATION" 2>/dev/null || true
fi

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

echo "Toss ${VERSION:+v$VERSION }installed to $DESTINATION. Run 'toss' to get started."
