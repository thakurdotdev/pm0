#!/bin/sh
# pm0 installer
#
#   curl -fsSL https://raw.githubusercontent.com/thakurdotdev/pm0/main/install.sh | bash
#
# System-wide install:
#   curl -fsSL https://raw.githubusercontent.com/thakurdotdev/pm0/main/install.sh | sudo bash
#
# Environment overrides:
#   PM0_VERSION      version tag to install (default: latest)
#   PM0_REPO         GitHub repository (default: github.com/thakurdotdev/pm0)
#   PM0_INSTALL_DIR  custom install directory
#   PM0_SRC          path to local source checkout for building
#   NO_COLOR         disable ANSI color formatting
set -eu

PM0_REPO="${PM0_REPO:-github.com/thakurdotdev/pm0}"
PM0_VERSION="${PM0_VERSION:-latest}"
INSTALL_DIR="${PM0_INSTALL_DIR:-}"
PM0_SRC="${PM0_SRC:-}"

# --- color support -----------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  BOLD=$(printf '\033[1m')
  DIM=$(printf '\033[2m')
  CYAN=$(printf '\033[36m')
  GREEN=$(printf '\033[32m')
  YELLOW=$(printf '\033[33m')
  RED=$(printf '\033[31m')
  MAGENTA=$(printf '\033[35m')
  RESET=$(printf '\033[0m')
else
  BOLD='' DIM='' CYAN='' GREEN='' YELLOW='' RED='' MAGENTA='' RESET=''
fi

# --- output helpers -----------------------------------------------------------

step()    { printf "\n  ${CYAN}●${RESET} ${BOLD}%s${RESET}\n" "$*"; }
info()    { printf "    ${DIM}%-17s${RESET} %s\n" "$1" "$2"; }
detail()  { printf "      ${DIM}↳ %s${RESET}\n" "$*"; }
ok()      { printf "  ${GREEN}✓${RESET} %s\n" "$*"; }
warn()    { printf "  ${YELLOW}!${RESET} %s\n" "$*"; }
fail()    { printf "\n  ${RED}✗ Error:${RESET} %s\n\n" "$*" >&2; exit 1; }

# --- header -------------------------------------------------------------------

printf "\n"
printf "  ${BOLD}pm0 installer${RESET}  ${DIM}— modern process manager for linux${RESET}\n"
printf "  ${DIM}https://%s${RESET}\n" "$PM0_REPO"
printf "  ${DIM}────────────────────────────────────────────────────────${RESET}\n"

# --- detect system ------------------------------------------------------------

step "Detecting System & Environment"

os_raw=$(uname -s)
os=$(echo "$os_raw" | tr '[:upper:]' '[:lower:]')

arch_raw=$(uname -m)
case "$arch_raw" in
  x86_64|amd64)     arch="amd64" ;;
  aarch64|arm64)    arch="arm64" ;;
  armv7*|armv6*|armhf) arch="arm" ;;
  i686|i386)        arch="386"   ;;
  *)                arch="$arch_raw" ;;
esac

distro=""
if [ -f /etc/os-release ]; then
  distro=$(. /etc/os-release 2>/dev/null && echo "${PRETTY_NAME:-$NAME}" || true)
fi

kernel=$(uname -r 2>/dev/null || echo "unknown")

init_sys="other"
if [ -d /run/systemd/system ]; then
  init_sys="systemd"
elif [ -f /sbin/openrc-run ] || [ -d /run/openrc ]; then
  init_sys="openrc"
elif [ -f /sbin/init ]; then
  init_sys="sysvinit"
fi

user_name=$(id -un 2>/dev/null || whoami 2>/dev/null || echo "user")
is_root=0
if [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
  is_root=1
  privilege_str="${BOLD}root${RESET}"
else
  privilege_str="user (${BOLD}${user_name}${RESET})"
fi

# Existing binary and daemon check
existing_bin=$(command -v pm0 2>/dev/null || true)
existing_ver=""
daemon_pid=""

if [ -n "$existing_bin" ]; then
  existing_ver=$("$existing_bin" version 2>/dev/null || echo "unknown")
  if ping_out=$("$existing_bin" ping 2>/dev/null); then
    daemon_pid=$(echo "$ping_out" | grep -o '"pid":[0-9]*' | cut -d: -f2 || true)
  fi
fi

if [ -n "$distro" ]; then
  info "Operating System" "${BOLD}${os_raw}${RESET} (${distro})"
else
  info "Operating System" "${BOLD}${os_raw}${RESET}"
fi
info "Kernel"           "${DIM}${kernel}${RESET}"
info "Architecture"     "${BOLD}${arch}${RESET} (${arch_raw})"
info "Init System"      "${BOLD}${init_sys}${RESET}"
info "Privileges"       "$privilege_str"

if [ -n "$existing_bin" ]; then
  if [ -n "$daemon_pid" ]; then
    info "Current State"    "${YELLOW}installed (${existing_ver})${RESET} — daemon active (PID ${daemon_pid})"
  else
    info "Current State"    "${YELLOW}installed (${existing_ver})${RESET} — daemon stopped"
  fi
else
  info "Current State"    "${GREEN}fresh installation${RESET}"
fi

if [ "$os" != "linux" ]; then
  warn "pm0 is purpose-built for Linux (cgroup v2 & /proc supervision) — non-Linux environments have limited features"
fi

# --- resolve target directory -------------------------------------------------

step "Resolving Installation Target"

pick_target() {
  if [ -n "$INSTALL_DIR" ]; then
    echo "$INSTALL_DIR"; return
  fi
  case ":$PATH:" in
    *":/usr/local/bin:"*)
      if [ -w /usr/local/bin ] || [ "$is_root" = 1 ]; then
        echo /usr/local/bin; return
      fi
      ;;
  esac
  echo "$HOME/.local/bin"
}

target=$(pick_target)
target_bin="$target/pm0"

in_path=0
case ":$PATH:" in
  *":$target:"*) in_path=1 ;;
esac

info "Install Path"     "${BOLD}${target_bin}${RESET}"
if [ "$in_path" = 1 ]; then
  info "PATH Status"      "${GREEN}directory is in \$PATH${RESET}"
else
  info "PATH Status"      "${YELLOW}not in \$PATH (setup instructions below)${RESET}"
fi

# --- downloader helper --------------------------------------------------------

downloader=""
if command -v curl >/dev/null 2>&1; then
  downloader="curl"
elif command -v wget >/dev/null 2>&1; then
  downloader="wget"
fi

fetch() { # fetch <url> <outfile>
  if [ "$downloader" = "curl" ]; then
    if [ -t 1 ]; then
      printf "\n"
      curl -fL --progress-bar "$1" -o "$2"
      printf "\n"
    else
      curl -fsSL "$1" -o "$2"
    fi
  elif [ "$downloader" = "wget" ]; then
    if [ -t 1 ]; then
      printf "\n"
      wget --show-progress -qO "$2" "$1"
      printf "\n"
    else
      wget -qO "$2" "$1"
    fi
  else
    return 1
  fi
}

url_exists() { # url_exists <url>
  if [ "$downloader" = "curl" ]; then
    curl -fsSIL --connect-timeout 5 "$1" >/dev/null 2>&1
  elif [ "$downloader" = "wget" ]; then
    wget --spider -q --timeout=5 "$1" >/dev/null 2>&1
  else
    return 1
  fi
}

# --- atomic install -----------------------------------------------------------

atomic_install() { # atomic_install <source_file>
  mkdir -p "$target"
  # Atomic rename avoids "text file busy" (ETXTBSY) if pm0 is currently running
  tmp_dst="$target/.pm0.new.$$"
  cp "$1" "$tmp_dst"
  chmod 0755 "$tmp_dst"
  mv -f "$tmp_dst" "$target_bin"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

installed=0

# --- download prebuilt release ------------------------------------------------

if [ -n "$downloader" ]; then
  step "Downloading pm0 Release"

  if [ "$PM0_VERSION" = "latest" ]; then
    base_url="https://$PM0_REPO/releases/latest/download"
  else
    base_url="https://$PM0_REPO/releases/download/$PM0_VERSION"
  fi

  info "Requested"        "${BOLD}${PM0_VERSION}${RESET}"
  info "Downloader"       "${BOLD}${downloader}${RESET}"

  # Try candidates in order
  tarball_url="$base_url/pm0-$os-$arch.tar.gz"
  tarball_ver_url="$base_url/pm0-$PM0_VERSION-$os-$arch.tar.gz"
  bin_url="$base_url/pm0-$os-$arch"
  bin_ver_url="$base_url/pm0-$PM0_VERSION-$os-$arch"

  chosen_url=""
  is_tarball=0

  if url_exists "$tarball_url"; then
    chosen_url="$tarball_url"
    is_tarball=1
  elif [ "$PM0_VERSION" != "latest" ] && url_exists "$tarball_ver_url"; then
    chosen_url="$tarball_ver_url"
    is_tarball=1
  elif url_exists "$bin_url"; then
    chosen_url="$bin_url"
    is_tarball=0
  elif [ "$PM0_VERSION" != "latest" ] && url_exists "$bin_ver_url"; then
    chosen_url="$bin_ver_url"
    is_tarball=0
  fi

  if [ -n "$chosen_url" ]; then
    asset_name=$(basename "$chosen_url")
    info "Asset"            "${BOLD}${asset_name}${RESET}"
    info "URL"              "${DIM}${chosen_url}${RESET}"

    if [ "$is_tarball" = 1 ]; then
      if fetch "$chosen_url" "$tmp/pm0.tar.gz"; then
        file_sz=$(ls -lh "$tmp/pm0.tar.gz" 2>/dev/null | awk '{print $5}' || echo "")
        [ -n "$file_sz" ] && detail "Archive size: ${file_sz}"
        detail "Extracting binary..."
        tar -C "$tmp" -xzf "$tmp/pm0.tar.gz"

        # Locate extracted binary
        extracted=""
        if [ -f "$tmp/pm0-$os-$arch" ]; then
          extracted="$tmp/pm0-$os-$arch"
        elif [ -f "$tmp/pm0" ]; then
          extracted="$tmp/pm0"
        else
          extracted=$(find "$tmp" -maxdepth 2 -type f -perm /111 ! -name "*.tar.gz" 2>/dev/null | head -n 1 || true)
        fi

        if [ -n "$extracted" ] && [ -f "$extracted" ]; then
          atomic_install "$extracted"
          installed=1
        fi
      fi
    else
      if fetch "$chosen_url" "$tmp/pm0-bin"; then
        file_sz=$(ls -lh "$tmp/pm0-bin" 2>/dev/null | awk '{print $5}' || echo "")
        [ -n "$file_sz" ] && detail "Binary size: ${file_sz}"
        atomic_install "$tmp/pm0-bin"
        installed=1
      fi
    fi
  else
    warn "No precompiled release binary found for ${os}/${arch} at ${PM0_VERSION}"
  fi
fi

# --- fallback: build from source ----------------------------------------------

if [ "$installed" = 0 ]; then
  step "Compiling from Source"

  src="${PM0_SRC:-$(cd "$(dirname "$0")" 2>/dev/null && pwd)}"
  if [ ! -f "$src/go.mod" ]; then
    fail "No downloadable release found and no source tree at '${src}' (set PM0_SRC or clone the repo)"
  fi
  if ! command -v go >/dev/null 2>&1; then
    fail "Building from source requires Go compiler (>= 1.22) — install from https://go.dev/dl"
  fi

  go_ver=$(go version 2>/dev/null || echo "go")
  info "Source Path"     "${BOLD}${src}${RESET}"
  info "Compiler"        "${DIM}${go_ver}${RESET}"
  detail "Building static binary..."

  (cd "$src" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${PM0_VERSION:-source} -X main.commit=install.sh" -o "$tmp/pm0" ./cmd/pm0)

  atomic_install "$tmp/pm0"
  installed=1
fi

# --- post-install verification & live reload ----------------------------------

step "Verifying Installation"

new_ver=$("$target_bin" version 2>/dev/null || echo "ready")
info "Installed Version" "${GREEN}${BOLD}${new_ver}${RESET}"
info "Binary Location"   "${BOLD}${target_bin}${RESET}"

# Check if an existing daemon is active and hot reload it
if [ -n "$daemon_pid" ]; then
  step "Live Daemon Hot-Reload"
  info "Running Daemon"   "PID ${daemon_pid} detected"
  detail "Triggering zero-downtime hot reload via in-place re-exec..."

  if "$target_bin" update >/dev/null 2>&1; then
    ok "${BOLD}Daemon updated in place seamlessly (child processes preserved)${RESET}"
  else
    warn "Could not hot-reload running daemon automatically. Run '${BOLD}pm0 update${RESET}' manually."
  fi
fi

# --- summary & next steps -----------------------------------------------------

printf "\n"
printf "  ${BOLD}${GREEN}╭─────────────────────────────────────────────────────────────╮${RESET}\n"
printf "  ${BOLD}${GREEN}│${RESET}                 ${BOLD}pm0 installed successfully!${RESET}                 ${BOLD}${GREEN}│${RESET}\n"
printf "  ${BOLD}${GREEN}╰─────────────────────────────────────────────────────────────╯${RESET}\n"

if [ "$in_path" = 0 ]; then
  shell_name=$(basename "${SHELL:-bash}")
  shell_rc="~/.profile"
  case "$shell_name" in
    zsh)  shell_rc="~/.zshrc" ;;
    bash) shell_rc="~/.bashrc" ;;
    fish) shell_rc="~/.config/fish/config.fish" ;;
  esac

  printf "\n"
  warn "${BOLD}${target}${RESET} is not in your current PATH."
  printf "  Add it to your shell by running:\n\n"
  if [ "$shell_name" = "fish" ]; then
    printf "    ${BOLD}fish_add_path %s${RESET}\n" "$target"
  else
    printf "    ${BOLD}echo 'export PATH=\"%s:\$PATH\"' >> %s${RESET}\n" "$target" "$shell_rc"
    printf "    ${BOLD}source %s${RESET}\n" "$shell_rc"
  fi
  printf "\n"
fi

printf "\n"
printf "  ${BOLD}Quick Start:${RESET}\n"
printf "    ${CYAN}pm0 start app.js --name web${RESET}       ${DIM}Start and supervise an app${RESET}\n"
printf "    ${CYAN}pm0 status${RESET}                        ${DIM}View status table & resource metrics${RESET}\n"
printf "    ${CYAN}pm0 logs [name]${RESET}                   ${DIM}Stream live stdout/stderr logs${RESET}\n"
printf "    ${CYAN}pm0 monit${RESET}                         ${DIM}Interactive terminal dashboard${RESET}\n"
printf "    ${CYAN}pm0 --help${RESET}                        ${DIM}Display all commands and flags${RESET}\n"
printf "\n"
