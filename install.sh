#!/usr/bin/env bash
set -euo pipefail

here="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) echo "Unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
artifact="$here/dist/cos-${os}-${arch}"
if [[ ! -x "$artifact" ]]; then
  echo "Prebuilt binary not found at $artifact. Run 'make dist' first." >&2
  exit 1
fi

dest="${COS_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dest"
install -m 0755 "$artifact" "$dest/cos"
echo "Installed: $dest/cos"
if [[ ":$PATH:" != *":$dest:"* ]]; then
  echo "Add $dest to PATH: export PATH=\"$dest:\$PATH\""
fi
"$dest/cos" doctor || true
if ! command -v rg >/dev/null 2>&1; then
  if [[ "$os" == "darwin" ]]; then hint="brew install ripgrep"; else hint="sudo apt install -y ripgrep"; fi
  echo "Optional: ripgrep missing. Install with: $hint"
fi
if ! command -v tunnel-client >/dev/null 2>&1 && [ ! -x "$dest/tunnel-client" ]; then
  echo "Optional: OpenAI tunnel-client missing. On macOS: brew install openai/tools/tunnel-client"
fi
if [[ "${COS_ENABLE_SERVICE:-1}" != "0" ]]; then
  "$dest/cos" service enable
fi
