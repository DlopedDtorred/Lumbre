#!/usr/bin/env sh
set -eu

REPOSITORY=${LUMBRE_REPO:-DlopedDtorred/Lumbre}
INSTALL_DIR=${LUMBRE_INSTALL_DIR:-"$HOME/.local"}
VERSION=${LUMBRE_VERSION:-latest}

case "$REPOSITORY" in
  */*) ;;
  *) echo "Invalid LUMBRE_REPO: expected owner/repository." >&2; exit 1 ;;
esac
case "$REPOSITORY" in
  *[!A-Za-z0-9_.-]*/*|*/*[!A-Za-z0-9_.-]*)
    echo "Invalid LUMBRE_REPO: use only GitHub owner/repository characters." >&2
    exit 1
    ;;
esac

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS:$ARCH" in
  Linux:x86_64|Linux:amd64) ASSET="lumbre-linux-x86_64.tar.gz" ;;
  Linux:aarch64|Linux:arm64) ASSET="lumbre-linux-arm64.tar.gz" ;;
  Darwin:x86_64) ASSET="lumbre-macos-x86_64.tar.gz" ;;
  Darwin:arm64) ASSET="lumbre-macos-arm64.tar.gz" ;;
  *)
    echo "Lumbre has no prebuilt package for $OS/$ARCH." >&2
    exit 1
    ;;
esac

if [ "$VERSION" = latest ]; then
  DOWNLOAD_URL="https://github.com/$REPOSITORY/releases/latest/download/$ASSET"
else
  DOWNLOAD_URL="https://github.com/$REPOSITORY/releases/download/$VERSION/$ASSET"
fi

for tool in curl tar; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required command not found: $tool" >&2
    exit 1
  fi
done

TMP=$(mktemp -d)
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
ARCHIVE="$TMP/$ASSET"
CHECKSUMS="$TMP/SHA256SUMS"
if ! curl --fail --location --silent --show-error "$DOWNLOAD_URL" --output "$ARCHIVE"; then
  echo "Could not download $ASSET from $REPOSITORY." >&2
  echo "Confirm that a GitHub release containing this package has been published." >&2
  exit 1
fi
CHECKSUM_URL=${DOWNLOAD_URL%/*}/SHA256SUMS
if ! curl --fail --location --silent --show-error "$CHECKSUM_URL" --output "$CHECKSUMS"; then
  echo "Could not download release checksums." >&2
  exit 1
fi
EXPECTED=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }' "$CHECKSUMS")
if [ -z "$EXPECTED" ]; then
  echo "No checksum found for $ASSET." >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$ARCHIVE" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')
else
  echo "Install sha256sum or shasum to verify the downloaded package." >&2
  exit 1
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "The downloaded package failed its SHA-256 check." >&2
  exit 1
fi
tar -tzf "$ARCHIVE" | awk '
  /^\// || /(^|\/)\.\.(\/|$)/ { exit 1 }
' || {
  echo "The downloaded package contains an unsafe path." >&2
  exit 1
}
mkdir -p "$TMP/unpacked"
tar -xzf "$ARCHIVE" -C "$TMP/unpacked"
PACKAGE="$TMP/unpacked"
if [ ! -x "$PACKAGE/lumbre" ] || [ ! -x "$PACKAGE/llama-server" ] || [ ! -f "$PACKAGE/web/index.html" ]; then
  echo "The release package is missing Lumbre, llama-server, or its web app." >&2
  exit 1
fi

BIN_DIR="$INSTALL_DIR/bin"
SHARE_DIR="$INSTALL_DIR/share/lumbre"
mkdir -p "$BIN_DIR" "$SHARE_DIR"
install -m 755 "$PACKAGE/lumbre" "$BIN_DIR/lumbre"
install -m 755 "$PACKAGE/llama-server" "$BIN_DIR/llama-server"
rm -rf "$SHARE_DIR/web.new"
cp -R "$PACKAGE/web" "$SHARE_DIR/web.new"
rm -rf "$SHARE_DIR/web"
mv "$SHARE_DIR/web.new" "$SHARE_DIR/web"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    PROFILE="$HOME/.profile"
    case "$OS" in
      Darwin) PROFILE="$HOME/.zprofile" ;;
      Linux) if [ -f "$HOME/.bashrc" ]; then PROFILE="$HOME/.bashrc"; fi ;;
    esac
    if ! grep -Fq '# Lumbre CLI' "$PROFILE" 2>/dev/null; then
      printf '\n# Lumbre CLI\nexport PATH="%s/bin:$PATH"\n' "$INSTALL_DIR" >> "$PROFILE"
    fi
    echo "PATH configured in $PROFILE. Open a new terminal to use lumbre."
    ;;
esac
echo "Lumbre installed successfully."
echo "Open a new terminal and run: lumbre serve"
