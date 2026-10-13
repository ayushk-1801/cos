#!/usr/bin/env bash
set -euo pipefail

REPO="${COS_GITHUB_REPO:-ayushk-1801/cos}"
INSTALL_DIR="${COS_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${COS_VERSION:-latest}"

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="cos-${os}-${arch}"
if [[ "$VERSION" == "latest" ]]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  VERSION="${VERSION#v}"
  base="https://github.com/${REPO}/releases/download/v${VERSION}"
fi

for cmd in curl install mktemp awk; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Required command not found: $cmd" >&2; exit 1
  fi
done
if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  echo "No SHA-256 verification utility found (sha256sum or shasum)." >&2; exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${asset}..."
curl -fL --retry 3 --retry-delay 1 -o "$tmp/$asset" "$base/$asset"
curl -fL --retry 3 --retry-delay 1 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected="$(awk -v name="$asset" '$2 == name {print $1}' "$tmp/SHA256SUMS")"
if [[ -z "$expected" ]]; then echo "Checksum for $asset not found" >&2; exit 1; fi
actual="$(checksum "$tmp/$asset")"
if [[ "$actual" != "$expected" ]]; then
  echo "Checksum verification failed: expected $expected, got $actual" >&2; exit 1
fi

echo "Checksum verified."
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/$asset" "$INSTALL_DIR/cos"
echo "Installed cos-lite to $INSTALL_DIR/cos"
"$INSTALL_DIR/cos" version
if [[ "${COS_ENABLE_SERVICE:-1}" != "0" ]]; then
  "$INSTALL_DIR/cos" service enable
fi
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  echo "Add cos to your PATH: export PATH=\"$INSTALL_DIR:\$PATH\""
fi
echo "Next: run 'cos' to open the TUI and add a project / configure the tunnel."
