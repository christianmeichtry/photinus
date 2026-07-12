#!/bin/sh
# photinus installer. One binary, one place, nothing else.
#
#   curl -fsSL https://photinus.dev/install.sh | sh
#
# Installs to /usr/local/bin when writable, otherwise ~/.local/bin.
# Set PHOTINUS_BIN to choose the directory yourself.
# Windows: download https://photinus.dev/dl/photinus_windows_amd64.exe directly.

set -eu

base="https://github.com/christianmeichtry/photinus/releases/latest/download"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv6l|armv7l) arch=arm ;;
  *) echo "photinus has no build for $os/$arch yet. Open an issue: https://github.com/christianmeichtry/photinus" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "photinus has no installer for $os yet. Open an issue: https://github.com/christianmeichtry/photinus" >&2; exit 1 ;;
esac

name="photinus_${os}_${arch}"

dir="${PHOTINUS_BIN:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
    mkdir -p "$dir"
  fi
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "fetching $name..."
curl -fsSL "$base/$name.gz" -o "$tmp/$name.gz"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"

sum=$( (command -v sha256sum >/dev/null && sha256sum "$tmp/$name.gz" || shasum -a 256 "$tmp/$name.gz") | cut -d' ' -f1 )
want=$(grep " ${name}.gz\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if [ -z "$want" ] || [ "$sum" != "$want" ]; then
  echo "checksum mismatch for $name.gz, refusing to install" >&2
  exit 1
fi

gunzip "$tmp/$name.gz"
chmod +x "$tmp/$name"
mv "$tmp/$name" "$dir/photinus"

echo "photinus installed to $dir/photinus"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "note: $dir is not on your PATH" ;;
esac
echo "light one with: photinus run -seed <another-host>:7946 -watch disk:/"
