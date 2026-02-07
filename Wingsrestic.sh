#!/bin/bash
set -euo pipefail

# Restic-Wings Installer
#
# This script is intended to be executed on Wings nodes. It:
# 1) Ensures Go is installed
# 2) Ensures restic is installed
# 3) Clones the Restic-Wings repo and builds Wings
# 4) Installs the built binary to /usr/local/bin/wings and restarts the service

TMP_DIR="/tmp/restic-wings-build"
WINGS_BIN="/usr/local/bin/wings"

# IMPORTANT:
# This should point to the same GitHub repo that hosts this script.
# If you fork this repo, change REPO_URL to your fork.
REPO_URL="https://github.com/Kowantify/Restic-Wings.git"
REPO_BRANCH="develop"

RESTIC_BIN="/usr/local/bin/restic"

echo -e "\n==============================="
echo " Restic-Wings Installer"
echo -e "===============================\n"

GO_VERSION_REQUIRED="1.24.0"
GO_TARBALL="go${GO_VERSION_REQUIRED}.linux-amd64.tar.gz"
GO_URL="https://go.dev/dl/${GO_TARBALL}"

echo "[0] Ensuring Go ${GO_VERSION_REQUIRED} is installed..."
needs_go_install=false
if command -v go >/dev/null 2>&1; then
  current_ver=$(go version | awk '{print $3}' | sed 's/^go//')
  if [ "$(printf '%s\n' "$GO_VERSION_REQUIRED" "$current_ver" | sort -V | head -n1)" != "$GO_VERSION_REQUIRED" ]; then
    needs_go_install=true
  fi
else
  needs_go_install=true
fi

if [ "$needs_go_install" = true ]; then
  echo "Installing Go ${GO_VERSION_REQUIRED}..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$GO_URL" -o "/tmp/${GO_TARBALL}"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "/tmp/${GO_TARBALL}" "$GO_URL"
  else
    echo "curl or wget is required to download Go." >&2
    exit 1
  fi
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
  rm -f "/tmp/${GO_TARBALL}"
  export PATH="/usr/local/go/bin:$PATH"
fi

echo "[0.5] Ensuring restic is installed..."
if ! command -v restic >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y || true
    apt-get install -y restic || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y restic || true
  elif command -v yum >/dev/null 2>&1; then
    yum install -y restic || true
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache restic || true
  fi
fi

if ! command -v restic >/dev/null 2>&1; then
  echo "Installing restic from GitHub releases..."
  if command -v curl >/dev/null 2>&1; then
    RESTIC_TAG=$(curl -fsSL "https://api.github.com/repos/restic/restic/releases/latest" | grep -m1 '"tag_name"' | sed -E 's/.*"([^"]+)".*/\\1/')
  elif command -v wget >/dev/null 2>&1; then
    RESTIC_TAG=$(wget -qO- "https://api.github.com/repos/restic/restic/releases/latest" | grep -m1 '"tag_name"' | sed -E 's/.*"([^"]+)".*/\\1/')
  else
    echo "curl or wget is required to download restic." >&2
    exit 1
  fi

  if [ -z "$RESTIC_TAG" ]; then
    echo "Failed to determine latest restic release tag." >&2
    exit 1
  fi

  RESTIC_VERSION="${RESTIC_TAG#v}"
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
      echo "Unsupported architecture: $ARCH" >&2
      exit 1
      ;;
  esac

  RESTIC_URL="https://github.com/restic/restic/releases/download/${RESTIC_TAG}/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2"
  RESTIC_TMP="/tmp/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2"

  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$RESTIC_URL" -o "$RESTIC_TMP"
  else
    wget -qO "$RESTIC_TMP" "$RESTIC_URL"
  fi

  if ! command -v bunzip2 >/dev/null 2>&1; then
    echo "bunzip2 is required to extract restic." >&2
    exit 1
  fi

  bunzip2 -f "$RESTIC_TMP"
  mv "/tmp/restic_${RESTIC_VERSION}_linux_${ARCH}" "$RESTIC_BIN"
  chmod +x "$RESTIC_BIN"
fi

echo "[1] Cloning Restic-Wings repository..."
rm -rf "$TMP_DIR"
git clone --depth=1 --branch "$REPO_BRANCH" "$REPO_URL" "$TMP_DIR"
cd "$TMP_DIR"

echo "[2] Building wings binary..."
go build -o wings
if [ ! -f wings ]; then
  echo "Build failed: wings binary not found." >&2
  exit 1
fi

echo "[3] Stopping Wings service..."
systemctl stop wings

echo "[4] Installing wings binary..."
chmod +x wings
cp wings "$WINGS_BIN"

RESTIC_BASE="/var/lib/pterodactyl/restic"
echo "[5] Ensuring restic backup base path exists at $RESTIC_BASE ..."
mkdir -p "$RESTIC_BASE"

echo "[6] Starting Wings service..."
systemctl start wings

echo "[7] Removing temporary build files..."
rm -rf "$TMP_DIR"

sleep 2
echo -e "\nAll done! Restic-Wings is installed and Wings has been restarted."
