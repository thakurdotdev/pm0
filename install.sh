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
  RESET=$(printf '\033[0m')
else
  BOLD='' DIM='' CYAN='' GREEN='' YELLOW='' RED='' RESET=''
fi

# --- output helpers -----------------------------------------------------------

status_line() { printf "  ${GREEN}✓${RESET} %-13s %s\n" "$1" "$2"; }
warn_line()   { printf "  ${YELLOW}!${RESET} %-13s %s\n" "$1" "$2"; }
fail()        { printf "\n  ${RED}✗ Error:${RESET} %s\n\n" "$*" >&2; exit 1; }

# --- header -------------------------------------------------------------------

printf "\n"
printf "  ${BOLD}pm0 installer${RESET} ${DIM}· modern process manager for linux${RESET}\n"
printf "  ${DIM}────────────────────────────────────────────────────────${RESET}\n"

# --- detect system ------------------------------------------------------------

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

sys_desc="${os_raw}"
[ -n "$distro" ] && sys_desc="${distro}"
sys_desc="${sys_desc} · ${arch}"

status_line "System:" "${BOLD}${sys_desc}${RESET}"

if [ "$os" != "linux" ]; then
  warn_line "Warning:" "pm0 is built for Linux — non-Linux environments have limited features"
fi

# --- detect existing version and daemon ---------------------------------------

existing_bin=$(command -v pm0 2>/dev/null || true)
existing_ver=""
daemon_pid=""

if [ -n "$existing_bin" ]; then
  existing_ver=$("$existing_bin" version 2>/dev/null | sed 's/^pm0 //' || echo "installed")
  if ping_out=$("$existing_bin" ping 2>/dev/null); then
    daemon_pid=$(echo "$ping_out" | grep -o '"pid":[0-9]*' | cut -d: -f2 || true)
  fi
fi

if [ -n "$existing_ver" ]; then
  if [ -n "$daemon_pid" ]; then
    status_line "Detected:" "pm0 ${existing_ver} (active daemon: PID ${daemon_pid})"
  else
    status_line "Detected:" "pm0 ${existing_ver} (daemon stopped)"
  fi
else
  status_line "Detected:" "fresh installation"
fi

# --- resolve target directory -------------------------------------------------

is_root=0
[ "$(id -u 2>/dev/null || echo 1)" = "0" ] && is_root=1

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
target_display="$target_bin"
[ -n "${HOME:-}" ] && target_display=$(echo "$target_bin" | sed "s|^$HOME|~|")

# --- downloader helper --------------------------------------------------------

downloader=""
if command -v curl >/dev/null 2>&1; then
  downloader="curl"
elif command -v wget >/dev/null 2>&1; then
  downloader="wget"
fi

fetch() { # fetch <url> <outfile>
  if [ "$downloader" = "curl" ]; then
    curl -fsSL "$1" -o "$2"
  elif [ "$downloader" = "wget" ]; then
    wget -qO "$2" "$1"
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

get_content_len() {
  if [ "$downloader" = "curl" ]; then
    curl -fsSIL --connect-timeout 5 "$1" 2>/dev/null | grep -i '^content-length:' | tail -n 1 | awk '{print $2}' | tr -d '\r\n'
  elif [ "$downloader" = "wget" ]; then
    wget --spider --server-response "$1" 2>&1 | grep -i 'Content-Length:' | tail -n 1 | awk '{print $2}' | tr -d '\r\n'
  fi
}

fetch_with_progress() { # fetch_with_progress <url> <outfile> <name>
  _url="$1"
  _dest="$2"
  _name="$3"

  if [ ! -t 1 ]; then
    fetch "$_url" "$_dest"
    return $?
  fi

  _total=$(get_content_len "$_url" || echo "")

  if [ -z "$_total" ] || [ "$_total" -le 0 ] 2>/dev/null; then
    printf "  ${CYAN}↓${RESET} %-13s %s" "Downloading:" "${BOLD}${_name}${RESET}..."
    fetch "$_url" "$_dest"
    _res=$?
    printf "\r\033[K"
    return $_res
  fi

  if [ "$downloader" = "curl" ]; then
    curl -fsSL "$_url" -o "$_dest" &
  else
    wget -qO "$_dest" "$_url" &
  fi
  _pid=$!

  _bar_f="===================="
  _bar_e="                    "

  while kill -0 "$_pid" 2>/dev/null; do
    _curr=0
    [ -f "$_dest" ] && _curr=$(wc -c < "$_dest" 2>/dev/null || echo 0)
    _pct=$((_curr * 100 / _total))
    [ "$_pct" -gt 100 ] && _pct=100
    _f=$((_pct * 20 / 100))
    _e=$((20 - _f))
    _bf=$(printf "%.*s" "$_f" "$_bar_f")
    _be=$(printf "%.*s" "$_e" "$_bar_e")

    printf "\r  ${CYAN}↓${RESET} %-13s ${BOLD}%s${RESET} [%s%s] %3d%%" "Downloading:" "$_name" "$_bf" "$_be" "$_pct"
    sleep 0.1
  done

  wait "$_pid"
  _res=$?
  printf "\r\033[K"
  return $_res
}

atomic_install() { # atomic_install <source_file>
  mkdir -p "$target"
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
  if [ "$PM0_VERSION" = "latest" ]; then
    base_url="https://$PM0_REPO/releases/latest/download"
  else
    base_url="https://$PM0_REPO/releases/download/$PM0_VERSION"
  fi

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

    if [ "$is_tarball" = 1 ]; then
      if fetch_with_progress "$chosen_url" "$tmp/pm0.tar.gz" "$asset_name"; then
        file_sz=$(ls -lh "$tmp/pm0.tar.gz" 2>/dev/null | awk '{print $5}' || echo "")
        tar -C "$tmp" -xzf "$tmp/pm0.tar.gz"

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
          [ -n "$file_sz" ] && file_sz=" (${file_sz})"
          status_line "Installing:" "${BOLD}${asset_name}${RESET}${file_sz} → ${target_display}"
        fi
      fi
    else
      if fetch_with_progress "$chosen_url" "$tmp/pm0-bin" "$asset_name"; then
        file_sz=$(ls -lh "$tmp/pm0-bin" 2>/dev/null | awk '{print $5}' || echo "")
        atomic_install "$tmp/pm0-bin"
        installed=1
        [ -n "$file_sz" ] && file_sz=" (${file_sz})"
        status_line "Installing:" "${BOLD}${asset_name}${RESET}${file_sz} → ${target_display}"
      fi
    fi
  fi
fi

# --- fallback: build from source ----------------------------------------------

if [ "$installed" = 0 ]; then
  src="${PM0_SRC:-$(cd "$(dirname "$0")" 2>/dev/null && pwd)}"
  if [ ! -f "$src/go.mod" ]; then
    fail "No downloadable release found and no source tree at '${src}' (set PM0_SRC or clone the repo)"
  fi
  if ! command -v go >/dev/null 2>&1; then
    fail "Building from source requires Go compiler (>= 1.22) — install from https://go.dev/dl"
  fi

  go_ver=$(go version 2>/dev/null | awk '{print $3}' || echo "go")
  (cd "$src" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${PM0_VERSION:-source} -X main.commit=install.sh" -o "$tmp/pm0" ./cmd/pm0)
  atomic_install "$tmp/pm0"
  installed=1
  status_line "Installing:" "source build (${go_ver}) → ${target_display}"
fi

# --- live reload if active daemon exists --------------------------------------

if [ -n "$daemon_pid" ]; then
  if "$target_bin" update >/dev/null 2>&1; then
    status_line "Daemon:" "Reloaded in place (PID ${daemon_pid} preserved)"
  else
    warn_line "Daemon:" "Could not auto-reload. Run 'pm0 update' manually"
  fi
fi

# --- summary & next steps -----------------------------------------------------

new_ver=$("$target_bin" version 2>/dev/null || echo "")
clean_ver=$(echo "$new_ver" | sed 's/^pm0 //')

printf "\n  ${GREEN}${BOLD}✓ pm0 %s installed successfully!${RESET}\n" "${clean_ver:-ready}"

case ":$PATH:" in
  *":$target:"*) ;;
  *)
    shell_name=$(basename "${SHELL:-bash}")
    shell_rc="~/.profile"
    case "$shell_name" in
      zsh)  shell_rc="~/.zshrc" ;;
      bash) shell_rc="~/.bashrc" ;;
      fish) shell_rc="~/.config/fish/config.fish" ;;
    esac

    printf "\n  ${YELLOW}!${RESET} ${BOLD}%s${RESET} is not in your PATH. Add it to %s:\n" "$target" "$shell_rc"
    if [ "$shell_name" = "fish" ]; then
      printf "    ${DIM}fish_add_path %s${RESET}\n" "$target"
    else
      printf "    ${DIM}export PATH=\"%s:\$PATH\"${RESET}\n" "$target"
    fi
    ;;
esac

printf "\n  ${BOLD}Quick start:${RESET}\n"
printf "    ${CYAN}%-29s${RESET} ${DIM}%s${RESET}\n" "pm0 start app.js --name web" "Start and supervise an app"
printf "    ${CYAN}%-29s${RESET} ${DIM}%s${RESET}\n" "pm0 status" "View status table & metrics"
printf "\n"
