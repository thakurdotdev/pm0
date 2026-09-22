#!/bin/sh
# pm0 quick installer.
#
#   curl -fsSL https://raw.githubusercontent.com/pm0/pm0/main/install.sh | sh
#
# Resolution order:
#   1. download a release tarball from GitHub (PM0_VERSION / PM0_REPO override)
#   2. build from a local source checkout (this script's directory, or PM0_SRC)
#
# Install target: PM0_INSTALL_DIR, else /usr/local/bin when writable (or root),
# else ~/.local/bin.
set -eu

PM0_REPO="${PM0_REPO:-github.com/pm0/pm0}"
PM0_VERSION="${PM0_VERSION:-latest}"
INSTALL_DIR="${PM0_INSTALL_DIR:-}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin | freebsd
arch_raw=$(uname -m)
case "$arch_raw" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7*|armv6*) arch=arm ;;
  i686|i386) arch=386 ;;
  *) arch=$arch_raw ;;
esac

say() { printf 'pm0-install: %s\n' "$*"; }
die() { printf 'pm0-install: %s\n' "$*" >&2; exit 1; }

pick_target() {
  if [ -n "$INSTALL_DIR" ]; then
    echo "$INSTALL_DIR"; return
  fi
  case ":$PATH:" in
    *":/usr/local/bin:"*)
      if [ -w /usr/local/bin ] || [ "$(id -u)" = 0 ]; then echo /usr/local/bin; return; fi ;;
  esac
  echo "$HOME/.local/bin"
}

fetch() { # fetch <url> <outfile>
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else return 1; fi
}

install_binary() { # install_binary <file>
  target=$(pick_target)
  mkdir -p "$target"
  cp "$1" "$target/pm0"
  chmod 0755 "$target/pm0"
  say "installed $target/pm0"
  case ":$PATH:" in
    *":$target:"*) ;;
    *) say "NOTE: $target is not on your PATH — add it to your shell profile" ;;
  esac
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 1) release download
if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then
  if fetch "https://$PM0_REPO/releases/latest/download/pm0-$os-$arch.tar.gz" "$tmp/pm0.tar.gz" 2>/dev/null; then
    tar -C "$tmp" -xzf "$tmp/pm0.tar.gz"
    install_binary "$tmp/pm0-$os-$arch"
    exit 0
  fi
  say "no downloadable release for $os/$arch yet — trying a source build"
fi

# 2) source build
src="${PM0_SRC:-$(cd "$(dirname "$0")" 2>/dev/null && pwd)}"
if [ ! -f "$src/go.mod" ]; then
  die "no release tarball and no source tree found (set PM0_SRC or run from a checkout)"
fi
if ! command -v go >/dev/null 2>&1; then
  die "building from source needs Go >= 1.26 — install it from https://go.dev/dl"
fi
say "building pm0 from $src ..."
(cd "$src" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${ver:-source} -X main.commit=install.sh" -o "$tmp/pm0" ./cmd/pm0)
install_binary "$tmp/pm0"
