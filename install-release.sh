#!/usr/bin/env bash
set -euo pipefail

REPO="${COS_GITHUB_REPO:-ayushk-1801/cos}"
INSTALL_DIR="${COS_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${COS_VERSION:-latest}"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "cos-lite release binaries currently support Linux only." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="cos-linux-${arch}"
if [[ "$VERSION" == "latest" ]]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  VERSION="${VERSION#v}"
  base="https://github.com/${REPO}/releases/download/v${VERSION}"
fi

for cmd in curl sha256sum install mktemp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Required command not found: $cmd" >&2
    exit 1
  fi
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${asset}..."
curl -fL --retry 3 --retry-delay 1 -o "$tmp/$asset" "$base/$asset"
curl -fL --retry 3 --retry-delay 1 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected="$(awk -v name="$asset" '$2 == name {print $1}' "$tmp/SHA256SUMS")"
if [[ -z "$expected" ]]; then
  echo "Checksum for $asset not found in SHA256SUMS" >&2
  exit 1
fi
actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
if [[ "$actual" != "$expected" ]]; then
  echo "Checksum verification failed for $asset" >&2
  echo "expected: $expected" >&2
  echo "actual:   $actual" >&2
  exit 1
fi

echo "Checksum verified."
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/$asset" "$INSTALL_DIR/cos"

echo "Installed cos-lite to $INSTALL_DIR/cos"
"$INSTALL_DIR/cos" version

# Keep installation non-interactive. Enabling the user service is safe even on
# a fresh install: with no configured projects it records the autostart policy
# and the daemon starts after the first project is added.
"$INSTALL_DIR/cos" service enable >/dev/null 2>&1 || true

if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  echo
  echo "Add cos to your PATH:"
  echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
fi

echo
echo "Next: run 'cos' to open the TUI and add a project / configure the tunnel."
