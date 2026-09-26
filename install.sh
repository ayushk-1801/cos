#!/usr/bin/env bash
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
case "$(uname -m)" in
  x86_64|amd64) artifact="$here/dist/cos-linux-amd64" ;;
  aarch64|arm64) artifact="$here/dist/cos-linux-arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [[ ! -x "$artifact" ]]; then
  echo "Prebuilt binary not found. Run 'make dist' first." >&2
  exit 1
fi

dest="${COS_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dest"
install -m 0755 "$artifact" "$dest/cos"
echo "Installed: $dest/cos"
if [[ ":$PATH:" != *":$dest:"* ]]; then
  echo "Add $dest to PATH, for example: export PATH=\"$dest:\$PATH\""
fi
"$dest/cos" doctor || true
# Autostart is the default policy in v0.3+. With no projects this only records
# the policy; after the first project is added cos installs/starts the user unit.
"$dest/cos" service enable >/dev/null 2>&1 || true
