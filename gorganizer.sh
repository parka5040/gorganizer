#!/bin/bash
# gorganizer.sh — single entry point for install, launch, and uninstall.
#
# Canonical install (any clone path; ~/gorganizer is just an example):
#     git clone https://github.com/parka5040/gorganizer ~/gorganizer
#     cd ~/gorganizer
#     ./gorganizer.sh        # builds + installs only; does NOT launch
#
# Subcommands:
#   (none)                Install or update: build, register desktop entry +
#                         nxm:// handler. If a prior install is detected the
#                         flow becomes an in-place rebuild + re-register.
#                         Does NOT launch the GUI — start it from your app
#                         menu, or run `./gorganizer.sh launch`.
#   launch                Start the daemon + GUI. Used by the desktop entry.
#   stop                  Ask the running daemon to shut down safely.
#   setup                 Detect distro, install build deps via system PM.
#   doctor                Check build and runtime dependencies without changes.
#   build [--rebuild]     Build only. --rebuild forces a clean rebuild.
#   update [--restart]    Update this branch from its own source, rebuild, and
#                         re-register. --restart only reminds you to reopen a
#                         running session; it never stops Gorganizer.
#   register              (Re-)install desktop file + icon + nxm:// handler.
#   unregister            Reverse `register`.
#   nxm <URI>             Open Gorganizer if needed and add a Nexus Mods download.
#   import --from PATH    Move old *_Mods/ folders to the personal data folder.
#   uninstall [--keep-data|--purge [--forget-missing-games]] [--yes]
#                         Restore every game before removing Gorganizer.
#                         Mods, downloads, settings and profiles stay by default.
#                         --purge requires a second confirmation to delete them.
#   uninstall --check     Check games without restoring or deleting anything.
#   --rebuild             Compatibility alias for `build --rebuild`.
#   --version, -v         Print version and exit.
#   --help, -h            Show this message.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# --- version ---------------------------------------------------------------
# The VERSION file at the repo root is the single source of truth. The
# Makefile reads it, ldflags-stamps both binaries, and the GUI's
# GORGANIZER_VERSION compile define mirrors it. We surface it here so
# `./gorganizer.sh --version` works even before anything is built.
gorganizer_version() {
    local v
    if [ -f "$SCRIPT_DIR/release.json" ]; then
        v="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$SCRIPT_DIR/release.json")"
    elif [ -f "$SCRIPT_DIR/VERSION" ]; then
        v="$(sed -n '1{s/[[:space:]]*$//;p;}' "$SCRIPT_DIR/VERSION" 2>/dev/null)"
    fi
    [ -z "${v:-}" ] && v="dev"
    if [ -d "$SCRIPT_DIR/.git" ] && command -v git >/dev/null 2>&1; then
        local desc
        desc="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || true)"
        if [ -n "$desc" ] && [ "$desc" != "$v" ]; then
            v="$v+$desc"
        fi
    fi
    echo "$v"
}

# --- paths -----------------------------------------------------------------

DAEMON_BIN="$SCRIPT_DIR/gorganizerd"
CTL_BIN="$SCRIPT_DIR/gorganizerctl"
GUI_BIN="$SCRIPT_DIR/build/src/gorganizer"
RELEASE_MODE=false
if [ -f "$SCRIPT_DIR/release.json" ]; then
    RELEASE_MODE=true
    SCRIPT_DIR="$(cd "$SCRIPT_DIR" && pwd -P)"
    DAEMON_BIN="$SCRIPT_DIR/bin/gorganizerd"
    CTL_BIN="$SCRIPT_DIR/bin/gorganizerctl"
    GUI_BIN="$SCRIPT_DIR/bin/gorganizer-gui"
fi
ICON_SRC="$SCRIPT_DIR/resources/icons/tmp_logo.png"

# User-facing install locations (XDG).
APPS_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
ICON_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/256x256/apps"
ICON_DEST="$ICON_DIR/gorganizer.png"
DESKTOP_FILE="$APPS_DIR/gorganizer.desktop"
NXM_DESKTOP_FILE="$APPS_DIR/gorganizer-nxm.desktop"
MIMEAPPS="${XDG_CONFIG_HOME:-$HOME/.config}/mimeapps.list"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/gorganizer"
RELEASE_CHECKOUT="$SCRIPT_DIR"
if $RELEASE_MODE && [ -d "$DATA_DIR/releases" ] && [ ! -L "$DATA_DIR/releases" ] &&
   [ "${SCRIPT_DIR%/*}" = "$(cd "$DATA_DIR/releases" && pwd -P)" ]; then
    RELEASE_CHECKOUT="$DATA_DIR/releases/current"
fi

# --- output helpers --------------------------------------------------------

if [ -t 1 ]; then
    BOLD='\033[1m'; CYAN='\033[0;36m'; GREEN='\033[0;32m'
    YELLOW='\033[0;33m'; RED='\033[0;31m'; RESET='\033[0m'
else
    BOLD=''; CYAN=''; GREEN=''; YELLOW=''; RED=''; RESET=''
fi
log()  { echo -e "${CYAN}[gorganizer]${RESET} $*"; }
ok()   { echo -e "${CYAN}[gorganizer]${RESET} ${GREEN}OK${RESET} $*"; }
warn() { echo -e "${CYAN}[gorganizer]${RESET} ${YELLOW}!!${RESET} $*"; }
err()  { echo -e "${CYAN}[gorganizer]${RESET} ${RED}XX${RESET} $*" >&2; }

usage() {
    cat <<USAGE
gorganizer.sh — single entry point for install, launch, and uninstall.

Version: $(gorganizer_version)

Canonical install (any clone path; ~/gorganizer is just an example):
    git clone https://github.com/parka5040/gorganizer ~/gorganizer
    cd ~/gorganizer
    ./gorganizer.sh        # builds + installs only; does NOT launch

Subcommands:
  (none)                Install or update: build, register desktop entry +
                        nxm:// handler. If a prior install is detected the
                        flow becomes an in-place rebuild + re-register.
                        Does NOT launch the GUI — start it from your app
                        menu, or run \`./gorganizer.sh launch\`.
  launch                Start the daemon + GUI (used by the desktop entry).
  stop                  Ask the running daemon to shut down safely.
  setup                 Detect distro, install build deps via system PM.
  doctor                Check build and runtime dependencies without changes.
  build [--rebuild]     Build only. --rebuild forces a clean rebuild.
  update [--restart]    Update this branch from its own source and rebuild.
                        Refuses to run if you have uncommitted changes.
                        Your mods and settings are preserved. --restart only
                        reminds you to reopen; it never stops Gorganizer.
  register              (Re-)install desktop file + icon + nxm:// handler.
  unregister            Reverse \`register\`.
  nxm <URI>             Open Gorganizer if needed and add a Nexus Mods download.
  import --from PATH    Move old *_Mods/ folders to your personal data folder.
  uninstall [--keep-data|--purge [--forget-missing-games]] [--yes]
                        Restore every game before removing Gorganizer.
                        Mods, downloads, settings and profiles stay by default.
                        --purge requires a second confirmation to delete them.
  uninstall --check     Check games without restoring or deleting anything.
  --rebuild             Compatibility alias for \`build --rebuild\`.
  --version, -v         Print version and exit.
  --help, -h            Show this message.
USAGE
}

# Read [Y/n] (default yes) or [y/N] (default no). Returns 0 for yes, 1 for no.
# Skips the prompt when stdin is not a tty and returns the default.
prompt_yn() {
    local question="$1" default="${2:-N}" reply prompt
    case "$default" in Y|y) prompt="[Y/n]";; *) prompt="[y/N]";; esac
    if [ ! -t 0 ]; then
        case "$default" in Y|y) return 0;; *) return 1;; esac
    fi
    read -r -p "$(echo -e "${CYAN}[gorganizer]${RESET} ${question} ${prompt} ")" reply || reply=""
    reply="${reply:-$default}"
    case "$reply" in y|Y|yes|YES) return 0;; *) return 1;; esac
}

# --- distro detection ------------------------------------------------------

detect_immutable_host() {
    [ -e "${GORGANIZER_OSTREE_MARKER:-/run/ostree-booted}" ] && return 0
    [ -r "${GORGANIZER_OS_RELEASE:-/etc/os-release}" ] || return 1
    local ID="" ID_LIKE="" VARIANT_ID=""
    . "${GORGANIZER_OS_RELEASE:-/etc/os-release}"
    case "${ID,,}" in
        steamos|nixos|bazzite|bluefin|aurora|endless) return 0 ;;
    esac
    case "${VARIANT_ID,,}" in
        *silverblue*|*kinoite*|*sericea*|*onyx*|*atomic*|*coreos*) return 0 ;;
    esac
    case " ${ID_LIKE,,} " in
        *" steamos "*) return 0 ;;
    esac
    return 1
}

detect_distro_family() {
    detect_immutable_host && { echo immutable; return; }
    local family="unknown"
    if [ -r "${GORGANIZER_OS_RELEASE:-/etc/os-release}" ]; then
        local ID="" ID_LIKE=""
        . "${GORGANIZER_OS_RELEASE:-/etc/os-release}"
        local ids=" ${ID:-} ${ID_LIKE:-} "
        case "$ids" in
            *" arch "*|*" artix "*|*" manjaro "*|*" endeavouros "*|*" cachyos "*) family="arch" ;;
            *" debian "*|*" ubuntu "*|*" linuxmint "*|*" pop "*|*" elementary "*) family="debian" ;;
            *" fedora "*|*" rhel "*|*" centos "*|*" nobara "*) family="fedora" ;;
            *" opensuse"*|*" suse "*) family="suse" ;;
        esac
    fi
    echo "$family"
}

# Build dependencies are logical tools paired with package-name candidates.
# The first candidate available from configured repositories is used, avoiding
# a single stale package name aborting an otherwise valid package-manager run.
deps_for_family() {
    case "$1" in
        arch) cat <<'EOF'
base-devel|base-devel
make|make
pkg-config|pkgconf
cmake|cmake
ninja|ninja
go|go
protobuf|protobuf
grpc|grpc
qt6-base|qt6-base
7-Zip|7zip p7zip
unzip|unzip
xdelta3|xdelta3
EOF
            ;;
        debian) cat <<'EOF'
build-essential|build-essential
make|make
pkg-config|pkg-config pkgconf
libprotobuf-dev|libprotobuf-dev
cmake|cmake
ninja-build|ninja-build
golang-go|golang-go
protobuf-compiler|protobuf-compiler
protobuf-compiler-grpc|protobuf-compiler-grpc
libgrpc++-dev|libgrpc++-dev
qt6-base-dev|qt6-base-dev
7-Zip|7zip p7zip-full
unzip|unzip
xdelta3|xdelta3
EOF
            ;;
        fedora) cat <<'EOF'
gcc-c++|gcc-c++
make|make
pkg-config|pkgconf-pkg-config pkgconf
protobuf-devel|protobuf-devel
cmake|cmake
ninja-build|ninja-build
golang|golang
protobuf-compiler|protobuf-compiler
grpc-plugins|grpc-plugins
grpc-devel|grpc-devel
qt6-qtbase-devel|qt6-qtbase-devel
7-Zip|7zip p7zip
unzip|unzip
xdelta3|xdelta3
EOF
            ;;
        suse) cat <<'EOF'
gcc-c++|gcc-c++
make|make
pkg-config|pkg-config pkgconf
cmake|cmake
ninja|ninja
go|go
protobuf-devel|protobuf-devel
grpc-devel|grpc-devel
qt6-base-devel|qt6-base-devel
7-Zip|7zip p7zip-full
unzip|unzip
xdelta3|xdelta
EOF
            ;;
    esac
}

pkg_available() {   # $1=family $2=package
    case "$1" in
        arch)   pacman -Si "$2" >/dev/null 2>&1 ;;
        debian) apt-cache show "$2" >/dev/null 2>&1 ;;
        fedora) dnf info "$2" >/dev/null 2>&1 ;;
        suse)   zypper info "$2" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}

pkg_installed() {
    local family="$1" pkg="$2"
    case "$family" in
        arch)   pacman -Qi "$pkg" >/dev/null 2>&1 ;;
        debian) dpkg -s "$pkg" 2>/dev/null | grep -q '^Status: install ok installed' ;;
        fedora) rpm -q "$pkg" >/dev/null 2>&1 ;;
        suse)   rpm -q "$pkg" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}

resolve_package_candidates() {
    local family="$1" candidates="$2" candidate
    local -a candidate_list=()
    read -r -a candidate_list <<< "$candidates"
    for candidate in "${candidate_list[@]}"; do
        if pkg_available "$family" "$candidate"; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

pm_install_cmd() {
    case "$1" in
        arch)   echo "sudo pacman -S --needed" ;;
        debian) echo "sudo apt-get install -y" ;;
        fedora) echo "sudo dnf install -y" ;;
        suse)   echo "sudo zypper install -y" ;;
        *)      echo "" ;;
    esac
}

pm_remove_cmd() {
    case "$1" in
        arch)   echo "sudo pacman -Rsn" ;;
        debian) echo "sudo apt-get remove -y" ;;
        fedora) echo "sudo dnf remove -y" ;;
        suse)   echo "sudo zypper remove -y" ;;
        *)      echo "" ;;
    esac
}

# Populates MISSING_BUILD_PACKAGES and UNRESOLVED_BUILD_DEPS. Returns 0 when
# a build dependency is missing, 1 for an unknown family, and 2 when complete.
MISSING_BUILD_PACKAGES=()
UNRESOLVED_BUILD_DEPS=()
missing_deps() {
    local family="$1" logical candidates resolved
    MISSING_BUILD_PACKAGES=()
    UNRESOLVED_BUILD_DEPS=()
    if [ -z "$(deps_for_family "$family")" ]; then
        return 1
    fi
    while IFS='|' read -r logical candidates; do
        resolved="$(resolve_package_candidates "$family" "$candidates" || true)"
        if [ -z "$resolved" ]; then
            warn "No available package for build dependency $logical (candidates: $candidates); install it manually."
            UNRESOLVED_BUILD_DEPS+=("$logical")
        elif ! pkg_installed "$family" "$resolved"; then
            MISSING_BUILD_PACKAGES+=("$resolved")
        fi
    done < <(deps_for_family "$family")
    if [ ${#MISSING_BUILD_PACKAGES[@]} -eq 0 ] && [ ${#UNRESOLVED_BUILD_DEPS[@]} -eq 0 ]; then
        return 2
    fi
    printf '%s\n' "${MISSING_BUILD_PACKAGES[*]}"
    return 0
}

IMMUTABLE_NOTICE_SHOWN=false
show_immutable_notice() {
    $IMMUTABLE_NOTICE_SHOWN && return 0
    warn "This system keeps its system files read-only, so Gorganizer will not install developer tools on it. To build Gorganizer here, open a Distrobox or Toolbox container, run ./gorganizer.sh inside it, and start Gorganizer from that container."
    IMMUTABLE_NOTICE_SHOWN=true
}

check_build_tools() {
    local tool pkg_tool="" missing=()
    for tool in make cmake go protoc grpc_cpp_plugin; do
        command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
    done
    if ! command -v c++ >/dev/null 2>&1 && ! command -v g++ >/dev/null 2>&1 && ! command -v clang++ >/dev/null 2>&1; then
        missing+=("C++ compiler")
    fi
    if command -v pkg-config >/dev/null 2>&1; then
        pkg_tool=pkg-config
    elif command -v pkgconf >/dev/null 2>&1; then
        pkg_tool=pkgconf
    else
        missing+=("pkg-config or pkgconf")
    fi
    if [ -n "$pkg_tool" ]; then
        "$pkg_tool" --exists protobuf || missing+=("protobuf development headers")
        "$pkg_tool" --exists grpc++ || missing+=("gRPC development headers")
    fi
    if [ ${#missing[@]} -ne 0 ]; then
        err "Missing build tools: ${missing[*]}."
        return 1
    fi
}

# Prompt-and-install build deps. Used by `setup` and the first-run flow.
install_deps_interactive() {
    local family="$1" missing install_cmd deps_rc=0
    local -a install_cmd_parts=()
    if [ "$family" = immutable ]; then
        show_immutable_notice
    else
        install_cmd="$(pm_install_cmd "$family")"
        if [ -z "$install_cmd" ]; then
            warn "Build packages cannot be installed automatically on this system."
        else
            missing_deps "$family" >/dev/null || deps_rc=$?
            case "$deps_rc" in
                2) ok "All build deps already installed." ;;
                1) warn "Build packages cannot be checked automatically on this system." ;;
                0)
                    missing="${MISSING_BUILD_PACKAGES[*]}"
                    if [ -n "$missing" ]; then
                        log "Missing build dependencies (${BOLD}$family${RESET}):"
                        printf '    %s\n' "$missing" >&2
                        log "Install command:"
                        printf '    %s %s\n' "$install_cmd" "$missing" >&2
                        if [ ! -t 0 ] || ! prompt_yn "Install now via sudo?" Y; then
                            err "Gorganizer cannot build because the needed tools were not installed. Install them and run ./gorganizer.sh again."
                            return 1
                        fi
                        sudo -v || { err "sudo authentication failed. Build stopped."; return 1; }
                        read -r -a install_cmd_parts <<< "$install_cmd"
                        if ! "${install_cmd_parts[@]}" "${MISSING_BUILD_PACKAGES[@]}"; then
                            err "Package install failed. Build stopped."
                            return 1
                        fi
                        ok "Build deps installed."
                    fi
                    ;;
            esac
        fi
    fi
    check_build_tools || return 1
    check_go_version_warning
}

# Required runtime binaries, their per-family package candidates, and the
# feature that degrades when they are unavailable.
runtime_tools() {
    cat <<'EOF'
7z|7zip p7zip|7zip p7zip-full|7zip p7zip|7zip p7zip-full|extracting .7z and .rar mod archives
unzip|unzip|unzip|unzip|unzip|extracting .zip mod archives
xdelta3|xdelta3|xdelta3|xdelta|xdelta3|Tale of Two Wastelands installs
protontricks|protontricks|protontricks|protontricks|protontricks|installing .NET and VC++ into Proton prefixes for modding tools and TTW
gst-launch-1.0|gstreamer|gstreamer1.0-tools|gstreamer1|gstreamer|audio conversion during Tale of Two Wastelands installs
EOF
}

RUNTIME_MISSING_PACKAGES=()
add_runtime_package() {
    local package="$1" existing
    for existing in "${RUNTIME_MISSING_PACKAGES[@]}"; do
        [ "$existing" = "$package" ] && return 0
    done
    RUNTIME_MISSING_PACKAGES+=("$package")
}

# Reports missing optional runtime tools and collects resolvable packages.
# It always returns success so missing feature-specific tools cannot abort an
# install or a doctor report.
runtime_tools_check() {
    local family="$1" binary arch debian fedora suse reason candidates resolved install_cmd
    RUNTIME_MISSING_PACKAGES=()
    install_cmd="$(pm_install_cmd "$family")"
    while IFS='|' read -r binary arch debian fedora suse reason; do
        if command -v "$binary" >/dev/null 2>&1; then
            printf 'present: %s\n' "$binary"
            continue
        fi
        case "$family" in
            arch) candidates="$arch" ;;
            debian) candidates="$debian" ;;
            fedora) candidates="$fedora" ;;
            suse) candidates="$suse" ;;
            *) candidates="" ;;
        esac
        resolved="$(resolve_package_candidates "$family" "$candidates" || true)"
        printf 'missing: %s — %s\n' "$binary" "$reason"
        if [ -n "$resolved" ] && [ -n "$install_cmd" ]; then
            printf '         fix: %s %s\n' "$install_cmd" "$resolved"
            add_runtime_package "$resolved"
        elif [ "$family" = immutable ]; then
            printf '         available inside a Distrobox or Toolbox container\n'
        else
            printf '         fix: install %s manually for this distro\n' "$binary"
        fi
    done < <(runtime_tools)
    return 0
}

# Offers to install all resolvable optional runtime tools after a successful
# build. Non-interactive runs deliberately never auto-accept a sudo action.
install_runtime_tools_interactive() {
    local family="$1" install_cmd
    local -a install_cmd_parts=()
    if [ "$family" = immutable ]; then
        runtime_tools_check "$family"
        return 0
    fi
    runtime_tools_check "$family"
    [ ${#RUNTIME_MISSING_PACKAGES[@]} -eq 0 ] && return 0
    if [ ! -t 0 ]; then
        warn "Runtime tool installation skipped because stdin is not a terminal."
        return 0
    fi
    if ! prompt_yn "Install missing runtime tools now via sudo?" Y; then
        warn "Skipped optional runtime tool installation."
        return 0
    fi
    install_cmd="$(pm_install_cmd "$family")"
    if ! sudo -v; then
        warn "sudo authentication failed; optional runtime tools were not installed."
        return 0
    fi
    read -r -a install_cmd_parts <<< "$install_cmd"
    if ! "${install_cmd_parts[@]}" "${RUNTIME_MISSING_PACKAGES[@]}"; then
        warn "Optional runtime tool installation failed."
        return 0
    fi
    ok "Optional runtime tools installed."
    return 0
}

check_go_version_warning() {
    export GOTOOLCHAIN=local
    local required found output req_major req_minor req_patch got_major got_minor got_patch
    required="$(sed -n 's/^go[[:space:]]\+\([0-9][0-9.]*\).*/\1/p' "$SCRIPT_DIR/go.mod" | sed -n '1p')"
    if [ -z "$required" ]; then
        err "Could not read the required Go version from go.mod."
        return 1
    fi
    if ! output="$(go version 2>/dev/null)" || [[ ! "$output" =~ ^go\ version\ (devel\ )?go([0-9]+\.[0-9]+(\.[0-9]+)?) ]]; then
        err "Could not check the installed Go version."
        return 1
    fi
    found="${BASH_REMATCH[2]}"
    IFS=. read -r req_major req_minor req_patch <<< "$required"
    IFS=. read -r got_major got_minor got_patch <<< "$found"
    req_patch="${req_patch:-0}"
    got_patch="${got_patch:-0}"
    if (( 10#$got_major < 10#$req_major ||
          (10#$got_major == 10#$req_major && 10#$got_minor < 10#$req_minor) ||
          (10#$got_major == 10#$req_major && 10#$got_minor == 10#$req_minor && 10#$got_patch < 10#$req_patch) )); then
        err "Gorganizer needs Go $required or newer, but this system has Go $found. Install a newer Go from https://go.dev/dl/ and run ./gorganizer.sh again."
        return 1
    fi
}

# --- build -----------------------------------------------------------------

build_fingerprint() (
    cd "$SCRIPT_DIR" || return 1
    find . \( -type d \( -name build -o -name .build-staging -o -name .tools \
        -o -name .git -o -name .gocache -o -name .tmp \) -prune \) -o \
        \( -type f \( -name '*.go' -o -name '*.cpp' -o -name '*.h' \
            -o -name '*.proto' -o -name CMakeLists.txt -o -name go.mod \
            -o -name go.sum -o -name Makefile -o -name VERSION \
            -o -path './resources/*' -o -path './assets/*' \) \
            ! -name '*.pb.go' -print0 \) | \
        LC_ALL=C sort -z | xargs -0 -r sha256sum | sha256sum
)

needs_build() {
    $RELEASE_MODE && return 1
    [ "${1:-}" = "force" ] && return 0
    [ ! -x "$DAEMON_BIN" ] && return 0
    [ ! -x "$CTL_BIN" ]    && return 0
    [ ! -x "$GUI_BIN" ]    && return 0
    [ ! -f "$SCRIPT_DIR/.build-fingerprint" ] && return 0
    command -v sha256sum >/dev/null 2>&1 || return 0
    local fingerprint
    fingerprint="$(build_fingerprint)" || return 0
    [ "$fingerprint" != "$(< "$SCRIPT_DIR/.build-fingerprint")" ]
}

validate_build_binary() {
    local output
    [ -x "$1" ] || return 1
    output="$("$1" --version)" || return 1
    [[ "$output" == *"$2"* ]]
}

build_and_publish() (
    local stage="$SCRIPT_DIR/.build-staging" version fingerprint
    local daemon_tmp="" ctl_tmp="" gui_tmp="" fingerprint_tmp=""
    trap 'rm -f "$daemon_tmp" "$ctl_tmp" "$gui_tmp" "$fingerprint_tmp"' EXIT
    trap 'exit 1' INT TERM

    if [ "${1:-}" = "force" ]; then
        log "Cleaning previous build..."
        rm -rf "$stage" || return 1
    fi
    mkdir -p "$stage/bin" || return 1
    command -v sha256sum >/dev/null 2>&1 || return 1
    version="$(sed -n '1{s/[[:space:]]*$//;p;}' "$SCRIPT_DIR/VERSION")" || return 1
    [ -n "$version" ] || return 1
    fingerprint="$(build_fingerprint)" || return 1

    log "Building (delegated to make)..."
    make OUT_DIR="$stage/bin" GUI_BUILD_DIR="$stage/gui" all gui || return 1
    validate_build_binary "$stage/bin/gorganizerd" "$version" || return 1
    validate_build_binary "$stage/bin/gorganizerctl" "$version" || return 1
    validate_build_binary "$stage/gui/src/gorganizer" "$version" || return 1
    [ "$(build_fingerprint)" = "$fingerprint" ] || return 1

    mkdir -p "$SCRIPT_DIR/build/src" || return 1
    daemon_tmp="$(mktemp "$DAEMON_BIN.tmp.XXXXXX")" || return 1
    ctl_tmp="$(mktemp "$CTL_BIN.tmp.XXXXXX")" || return 1
    gui_tmp="$(mktemp "$GUI_BIN.tmp.XXXXXX")" || return 1
    fingerprint_tmp="$(mktemp "$stage/fingerprint.XXXXXX")" || return 1
    install -m 755 "$stage/bin/gorganizerd" "$daemon_tmp" || return 1
    install -m 755 "$stage/bin/gorganizerctl" "$ctl_tmp" || return 1
    install -m 755 "$stage/gui/src/gorganizer" "$gui_tmp" || return 1
    printf '%s\n' "$fingerprint" > "$fingerprint_tmp" || return 1

    mv -f "$daemon_tmp" "$DAEMON_BIN" || return 1
    mv -f "$ctl_tmp" "$CTL_BIN" || return 1
    mv -f "$gui_tmp" "$GUI_BIN" || return 1
    mv -f "$fingerprint_tmp" "$SCRIPT_DIR/.build-fingerprint" || return 1
)

do_build() {
    local family
    family="$(detect_distro_family)"
    if [ "$family" = immutable ]; then
        show_immutable_notice
    fi
    check_build_tools || return 1
    check_go_version_warning || return 1
    if ! build_and_publish "${1:-}"; then
        err "The new build failed. Your installed version is unchanged."
        local install_cmd; install_cmd="$(pm_install_cmd "$family")"
        if [ -n "$install_cmd" ]; then
            warn "If this looks like a missing tool/header, run:"
            warn "    ./gorganizer.sh setup"
        fi
        return 1
    fi
    ok "Build complete."
}

# --- desktop / mime registration -------------------------------------------

# Returns 0 when the desktop shortcut or icon needs updating.
needs_register() {
    [ -f "$ICON_DEST" ] || return 0
    [ -x "$CTL_BIN" ] || return 0
    "$CTL_BIN" desktop status --checkout "$RELEASE_CHECKOUT" --icon "$ICON_DEST" >/dev/null 2>&1 || return 0
    return 1
}

# Build only the maintenance tool when registration precedes installation.
ensure_register_ctl() {
    if $RELEASE_MODE; then
        [ -x "$CTL_BIN" ] || { err "Gorganizer's maintenance tool is missing from this download."; return 1; }
        return 0
    fi
    [ -x "$CTL_BIN" ] && ! needs_build && return 0
    check_build_tools || return 1
    check_go_version_warning || return 1
    local stage="$SCRIPT_DIR/.build-staging" version tmp
    version="$(sed -n '1{s/[[:space:]]*$//;p;}' "$SCRIPT_DIR/VERSION")" || return 1
    mkdir -p "$stage/bin" || return 1
    make OUT_DIR="$stage/bin" GUI_BUILD_DIR="$stage/gui" ctl || return 1
    validate_build_binary "$stage/bin/gorganizerctl" "$version" || return 1
    tmp="$(mktemp "$CTL_BIN.tmp.XXXXXX")" || return 1
    if ! install -m 755 "$stage/bin/gorganizerctl" "$tmp" || ! mv -f "$tmp" "$CTL_BIN"; then
        rm -f "$tmp"
        return 1
    fi
}

cmd_register() {
    if [ ! -f "$ICON_SRC" ]; then
        err "Icon missing at $ICON_SRC"
        return 1
    fi
    ensure_register_ctl || { err "Could not build Gorganizer's maintenance tool."; return 1; }
    install -Dm644 "$ICON_SRC" "$ICON_DEST"
    "$CTL_BIN" desktop register --checkout "$RELEASE_CHECKOUT" --icon "$ICON_DEST" || return 1
    xdg-mime default gorganizer-nxm.desktop x-scheme-handler/nxm 2>/dev/null || true
    update-desktop-database "$APPS_DIR" >/dev/null 2>&1 || true
    gtk-update-icon-cache "${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor" >/dev/null 2>&1 || true
    ok "Registered. If you move Gorganizer, run this again from its new folder."
}

cmd_unregister() {
    if [ ! -x "$CTL_BIN" ]; then
        err "Gorganizer's maintenance tool is missing. Nothing was removed. Run ./gorganizer.sh register first."
        return 1
    fi
    "$CTL_BIN" desktop unregister --checkout "$RELEASE_CHECKOUT" || return 1
    if [ ! -e "$DESKTOP_FILE" ] && [ ! -L "$DESKTOP_FILE" ] &&
       [ ! -e "$NXM_DESKTOP_FILE" ] && [ ! -L "$NXM_DESKTOP_FILE" ]; then
        [ ! -f "$ICON_DEST" ] || rm -f "$ICON_DEST"
    fi
    update-desktop-database "$APPS_DIR" >/dev/null 2>&1 || true
    ok "Unregistered."
}

# --- migration -------------------------------------------------------------

cmd_import() {
    if [ "$#" -ne 2 ] || [ "$1" != --from ] || [ -z "$2" ]; then
        err "Usage: $0 import --from <path>"
        return 2
    fi
    "$CTL_BIN" migrate-data --from "$2"
}

# --- daemon lifecycle ------------------------------------------------------

cmd_stop() {
    "$CTL_BIN" stop "$@"
}

# --- install (default) -----------------------------------------------------

# Returns 0 if a prior install of the application is detected — i.e. both
# binaries already live in this clone AND the desktop entries are in place
# and point at this clone. Used by cmd_install to decide between "fresh
# install" wording and "in-place update" wording.
already_installed() {
    [ -x "$DAEMON_BIN" ] || return 1
    [ -x "$GUI_BIN" ]    || return 1
    needs_register && return 1
    return 0
}

cmd_install() {
    local mode="install"
    if already_installed; then
        mode="update"
    fi

    # Ensure build deps before the first build. On updates we trust them
    # already — `setup` is the explicit re-check command if the toolchain
    # changed under the user.
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ]; then
        local family
        family="$(detect_distro_family)"
        install_deps_interactive "$family" || return 1
    fi

    # Build (incremental): no-op when sources are unchanged AND both
    # binaries are present.
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ] || needs_build; then
        do_build || exit 1
    else
        ok "Binaries up to date — skipping build."
    fi

    # Optional runtime tools only affect specific features, never the build.
    local runtime_family
    runtime_family="$(detect_distro_family)"
    install_runtime_tools_interactive "$runtime_family"

    # Refresh the desktop entry on every run so a moved clone or a
    # version bump shows up in the launcher immediately.
    if needs_register; then
        log "Installing desktop entry and nxm:// handler..."
        cmd_register || warn "Desktop registration reported issues."
    else
        ok "Desktop entry already registered."
    fi

    echo ""
    echo -e "  ${BOLD}Gorganizer${RESET} ${CYAN}$(gorganizer_version)${RESET}"
    echo -e "  ${CYAN}-----------${RESET}"
    if [ "$mode" = "update" ]; then
        log "  In-place update complete."
    else
        log "  Install complete."
    fi
    log "  Daemon:    $DAEMON_BIN"
    log "  Frontend:  $GUI_BIN"
    log "  Mods:      $DATA_DIR/<game>/mods/"
    log "  Desktop:   $DESKTOP_FILE"
    echo ""
    log "Launch via your application menu, or run:"
    log "    ${BOLD}./gorganizer.sh launch${RESET}"
}

# --- launch ----------------------------------------------------------------

notify_user() {
    if command -v notify-send >/dev/null 2>&1; then
        notify-send "Gorganizer" "$1" || true
    fi
}

preflight_data_migration() {
    if [ "${GORGANIZER_ROOT+x}" != x ]; then
        local status plan count result first_blocker
        if ! status="$("$CTL_BIN" migrate-data --status)"; then
            err "Could not check whether your mods need moving. Please try again."
            notify_user "Could not check whether your mods need moving. Please try again."
            exit 1
        fi
        case "$status" in
            pending)
                if ! result="$("$CTL_BIN" migrate-data --resume 2>&1)"; then
                    err "$result"
                    notify_user "Could not finish moving your mods: $result"
                    exit 1
                fi
                [ -z "$result" ] || log "$result"
                ;;
            none) ;;
            *)
                err "Could not check whether your mods need moving: $status"
                notify_user "Could not check whether your mods need moving. Please try again."
                exit 1
                ;;
        esac
        plan="$("$CTL_BIN" migrate-data --from "$SCRIPT_DIR" --dry-run --count 2>&1)" || true
        if [[ "$plan" =~ ^[0-9]+$ ]]; then
            count="$plan"
            if [ "$count" -gt 0 ]; then
                notify_user "Moving your mods to your personal data folder. This happens once."
                log "Moving your mods to your personal data folder. This happens once."
                if result="$("$CTL_BIN" migrate-data --from "$SCRIPT_DIR" --yes 2>&1)"; then
                    [ -z "$result" ] || log "$result"
                    notify_user "Your mods are now in ~/.local/share/gorganizer."
                    ok "Your mods are now in ~/.local/share/gorganizer."
                else
                    first_blocker="$(printf '%s\n' "$result" | sed -n 's/^[[:space:]]*Cannot move yet: //p' | sed -n '1p')"
                    [ -n "$first_blocker" ] || first_blocker="${result##*$'\n'}"
                    first_blocker="${first_blocker#Could not move your mods: }"
                    first_blocker="${first_blocker%.}"
                    warn "Gorganizer couldn't move your mods yet: $first_blocker. It will try again next time."
                    notify_user "Gorganizer couldn't move your mods yet: $first_blocker. It will try again next time."
                    export GORGANIZER_ROOT="$SCRIPT_DIR"
                fi
            fi
        else
            warn "Gorganizer couldn't check your old mods yet: $plan. It will try again next time."
            notify_user "Gorganizer couldn't check your old mods yet. It will try again next time."
            export GORGANIZER_ROOT="$SCRIPT_DIR"
        fi
    fi
}

cmd_launch() {
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ] || [ ! -x "$CTL_BIN" ]; then
        err "Gorganizer is not built yet."
        err "Run \`./gorganizer.sh\` from this clone to build and install."
        exit 1
    fi

    export QT_LOGGING_RULES="${QT_LOGGING_RULES:+$QT_LOGGING_RULES;}qt.dbus.*=false;qt.qpa.systemtray.*=false;qt.qpa.theme.dbus.*=false;qt.qpa.theme.debug=false"
    if $RELEASE_MODE; then
        exec "$CTL_BIN" session --daemon "$DAEMON_BIN" --gui "$GUI_BIN" -- "$@"
    fi
    preflight_data_migration
    exec "$CTL_BIN" session --daemon "$DAEMON_BIN" --gui "$GUI_BIN" -- "$@"
}

# --- nxm forwarding --------------------------------------------------------

cmd_nxm() {
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$CTL_BIN" ]; then
        err "Gorganizer is not built yet. Run ./gorganizer.sh first."
        exit 1
    fi
    if ! $RELEASE_MODE && ! "$CTL_BIN" ping >/dev/null 2>&1; then
        preflight_data_migration
    fi
    exec "$CTL_BIN" nxm "$@"
}

# --- update ----------------------------------------------------------------

update_migration_reminder() {
    local status
    if [ -x "$CTL_BIN" ] && status="$("$CTL_BIN" migrate-data --status 2>/dev/null)" \
       && [ "$status" = pending ]; then
        log "Open Gorganizer to finish moving your mods."
    fi
}

cmd_update() {
    local restart=false branch upstream remote old_sha old_short new_sha
    while [ $# -gt 0 ]; do
        case "$1" in
            --restart) restart=true; shift ;;
            *) err "Unknown option: $1"; return 2 ;;
        esac
    done

    if $RELEASE_MODE; then
        "$CTL_BIN" release update || return $?
        if $restart; then
            log "Close Gorganizer and open it again to use the new version now."
        fi
        return 0
    fi

    if ! command -v git >/dev/null 2>&1; then
        err "git not found in PATH; can't update."
        return 1
    fi
    if [ ! -d "$SCRIPT_DIR/.git" ]; then
        err "$SCRIPT_DIR is not a git checkout."
        err "Re-clone the repo to update:"
        err "    git clone https://github.com/parka5040/gorganizer ~/gorganizer"
        return 1
    fi

    if ! git -C "$SCRIPT_DIR" diff --quiet HEAD -- 2>/dev/null \
       || [ -n "$(git -C "$SCRIPT_DIR" status --porcelain)" ]; then
        err "Working tree has uncommitted changes:"
        git -C "$SCRIPT_DIR" status -s >&2
        err "Stash or commit them, then re-run \`./gorganizer.sh update\`."
        return 1
    fi

    if ! branch="$(git -C "$SCRIPT_DIR" symbolic-ref --quiet --short HEAD)"; then
        err "This copy of Gorganizer is not on a branch, so it cannot be updated automatically."
        return 1
    fi
    if ! upstream="$(git -C "$SCRIPT_DIR" rev-parse --abbrev-ref '@{u}' 2>/dev/null)" \
       || ! remote="$(git -C "$SCRIPT_DIR" config --get "branch.$branch.remote")"; then
        err "This branch has no update source. Ask whoever set it up, or re-download Gorganizer."
        return 1
    fi

    old_sha="$(git -C "$SCRIPT_DIR" rev-parse HEAD)"
    old_short="$(git -C "$SCRIPT_DIR" rev-parse --short HEAD)"
    if ! git -C "$SCRIPT_DIR" fetch --quiet "$remote"; then
        err "Could not check for updates. Nothing was changed."
        return 1
    fi
    if ! git -C "$SCRIPT_DIR" merge-base --is-ancestor HEAD "$upstream"; then
        err "Your copy has changes that are not in the update source. Nothing was changed."
        return 1
    fi
    if [ "$old_sha" = "$(git -C "$SCRIPT_DIR" rev-parse "$upstream")" ]; then
        if ! needs_build; then
            ok "Gorganizer is already up to date ($old_short)."
            update_migration_reminder
            return 0
        fi
    else
        if ! git -C "$SCRIPT_DIR" merge --ff-only --quiet "$upstream"; then
            err "Could not install the update. Your previous version is still available."
            return 1
        fi
        new_sha="$(git -C "$SCRIPT_DIR" rev-parse HEAD)"
        ok "New in this update:"
        git -C "$SCRIPT_DIR" log --format='  %s' "${old_sha}..${new_sha}"
    fi

    if ! do_build force; then
        if [ -n "${new_sha:-}" ]; then
            if ! git -C "$SCRIPT_DIR" reset --keep "$old_sha"; then
                err "The update could not be built or rolled back. Your installed Gorganizer has not changed; ask whoever set it up for help."
                return 1
            fi
        fi
        err "The update was downloaded but could not be built, so Gorganizer stayed on the previous version ($old_short). Your mods and settings were not touched."
        return 1
    fi

    if ! cmd_register; then
        warn "Could not refresh the application menu entry. Gorganizer was updated."
    fi

    if [ -x "$CTL_BIN" ] && "$CTL_BIN" ping >/dev/null 2>&1; then
        ok "Update installed. It will be used next time you open Gorganizer."
        if $restart; then
            log "Close Gorganizer and open it again to use the new version now."
        fi
    else
        ok "Update installed."
    fi
    update_migration_reminder
}

# --- setup -----------------------------------------------------------------

cmd_setup() {
    local family
    family="$(detect_distro_family)"
    log "Distro family: ${BOLD}$family${RESET}"
    install_deps_interactive "$family"
}

# --- doctor -----------------------------------------------------------------

cmd_doctor() {
    if $RELEASE_MODE; then
        "$CTL_BIN" doctor "$@"
        return $?
    fi
    local family logical candidates resolved install_cmd build_rc=0
    family="$(detect_distro_family)"
    install_cmd="$(pm_install_cmd "$family")"
    log "Distro family: ${BOLD}$family${RESET}"
    if [ "$family" = immutable ]; then
        show_immutable_notice
        log "Build dependencies:"
        check_build_tools || build_rc=1
        if command -v go >/dev/null 2>&1; then
            check_go_version_warning || build_rc=1
        fi
        log "Runtime tools (optional):"
        runtime_tools_check "$family"
        return "$build_rc"
    fi
    if [ -n "$install_cmd" ]; then
        log "Package manager: $install_cmd"
    else
        warn "Package manager: unavailable for this distro family"
        build_rc=1
    fi
    log "Build dependencies:"
    if [ -z "$(deps_for_family "$family")" ]; then
        warn "missing: unsupported distro family"
        echo "         fix: install the documented build dependencies manually"
        build_rc=1
    else
        while IFS='|' read -r logical candidates; do
            resolved="$(resolve_package_candidates "$family" "$candidates" || true)"
            if [ -z "$resolved" ]; then
                warn "missing: $logical (no candidate is available: $candidates)"
                echo "         fix: install $logical manually"
                build_rc=1
            elif pkg_installed "$family" "$resolved"; then
                echo "present: $logical ($resolved)"
            else
                warn "missing: $logical ($resolved)"
                echo "         fix: $install_cmd $resolved"
                build_rc=1
            fi
        done < <(deps_for_family "$family")
    fi
    log "Runtime tools (optional):"
    runtime_tools_check "$family"
    return "$build_rc"
}

# --- uninstall -------------------------------------------------------------

uninstall_validate_build_paths() {
    local path ancestor kind uid
    local -a paths
    uid="$(id -u)"
    if [ "${1:-}" = release ]; then
        paths=("$DAEMON_BIN" "$CTL_BIN" "$SCRIPT_DIR/gorganizer.sh" "$SCRIPT_DIR/release.json")
    else
        paths=("$SCRIPT_DIR/build" "$SCRIPT_DIR/.build-staging" "$SCRIPT_DIR/CMakeFiles" "$SCRIPT_DIR/.tools" \
            "$DAEMON_BIN" "$CTL_BIN" "$SCRIPT_DIR/.build-fingerprint" \
            "$SCRIPT_DIR/api/proto/gorganizer.pb.go" "$SCRIPT_DIR/api/proto/gorganizer_grpc.pb.go")
    fi
    for path in "${paths[@]}"; do
        ancestor="${path%/*}"
        while [ "$ancestor" != / ]; do
            if [ -L "$ancestor" ] || [ ! -d "$ancestor" ]; then
                err "Cannot safely remove build files: $ancestor is not a real folder. Nothing was removed."
                return 1
            fi
            ancestor="${ancestor%/*}"
            [ -n "$ancestor" ] || ancestor=/
        done
        [ -e "$path" ] || [ -L "$path" ] || continue
        kind=-f
        case "$path" in
            "$SCRIPT_DIR/build"|"$SCRIPT_DIR/.build-staging"|"$SCRIPT_DIR/CMakeFiles"|"$SCRIPT_DIR/.tools") kind=-d ;;
        esac
        if [ -L "$path" ] || [ "$(stat -c %u -- "$path")" != "$uid" ] || [ ! "$kind" "$path" ]; then
            err "Cannot safely remove build files: $path is not an owned build artifact. Nothing was removed."
            return 1
        fi
    done
}

uninstall_validate_releases_paths() {
    local path="$DATA_DIR/releases" ancestor entry uid
    uid="$(id -u)"
    ancestor="${path%/*}"
    while [ "$ancestor" != / ]; do
        if [ -L "$ancestor" ] || [ ! -d "$ancestor" ]; then
            err "Cannot safely remove releases: $ancestor is not a real folder. Nothing was removed."
            return 1
        fi
        ancestor="${ancestor%/*}"
        [ -n "$ancestor" ] || ancestor=/
    done
    if [ -L "$path" ] || [ ! -d "$path" ] || [ "$(stat -c %u -- "$path")" != "$uid" ]; then
        err "Cannot safely remove releases: $path is not an owned folder. Nothing was removed."
        return 1
    fi
    for entry in "$path"/* "$path"/.[!.]*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        if [ "$(stat -c %u -- "$entry")" != "$uid" ]; then
            err "Cannot safely remove releases: $entry is not owned by you. Nothing was removed."
            return 1
        fi
    done
}

cmd_uninstall() {
    if $RELEASE_MODE; then
        local option purge=false check=false
        for option in "$@"; do
            [ "$option" != --purge ] || purge=true
            [ "$option" != --check ] || check=true
        done
        if $purge && ! $check; then
            uninstall_validate_build_paths release || return 1
            uninstall_validate_releases_paths || return 1
        fi
        if $purge && ! $check; then
            "$CTL_BIN" uninstall --preserve-releases "$@" || return $?
        else
            "$CTL_BIN" uninstall "$@" || return $?
        fi
        if ! $check; then
            cmd_unregister || return 1
            if $purge; then
                uninstall_validate_releases_paths || return 1
                ok "Uninstalled. Removing downloaded versions."
                exec /bin/sh -c 'rm -rf -- "$1"' sh "$DATA_DIR/releases"
            fi
            ok "Uninstalled. You can now delete this download folder."
        fi
        return 0
    fi
    local mods_json="" mods_list="" mods_checked=false
    if [ ! -x "$CTL_BIN" ]; then
        err "Gorganizer's maintenance tool is missing, so nothing was removed. Rebuild with ./gorganizer.sh, then run uninstall again."
        return 1
    fi
    for option in "$@"; do
        if [ "$option" = --check ]; then
            "$CTL_BIN" uninstall "$@"
            return $?
        fi
    done
    uninstall_validate_build_paths || return 1
    if mods_json="$("$CTL_BIN" migrate-data --from "$SCRIPT_DIR" --dry-run --json)"; then
        if command -v jq >/dev/null 2>&1; then
            if mods_list="$(printf '%s\n' "$mods_json" | jq -r 'if (.sources | type) == "array" and all(.sources[]; type == "string" and (index("\n") | not) and (index("\r") | not) and (index("\u0000") | not)) then .sources[] else error("invalid sources") end')"; then
                mods_checked=true
            fi
        elif mods_list="$("$CTL_BIN" migrate-data --from "$SCRIPT_DIR" --dry-run --list)"; then
            mods_checked=true
        fi
    fi
    "$CTL_BIN" uninstall "$@" || return $?
    uninstall_validate_build_paths || return 1
    if $mods_checked; then
        if [ -n "$mods_list" ]; then
            warn "Your mods are still inside this folder: ${mods_list//$'\n'/, }. Move them with ./gorganizer.sh import --from \"$SCRIPT_DIR\" before you delete it, or they will be lost."
        fi
    else
        warn "Your mods may still be inside this folder. Move them with ./gorganizer.sh import --from \"$SCRIPT_DIR\" before you delete it, or they will be lost."
    fi
    rm -rf -- "$SCRIPT_DIR/build" "$SCRIPT_DIR/.build-staging" "$SCRIPT_DIR/CMakeFiles" "$SCRIPT_DIR/.tools"
    rm -f -- "$DAEMON_BIN" "$CTL_BIN" "$SCRIPT_DIR/.build-fingerprint" \
        "$SCRIPT_DIR/api/proto/gorganizer.pb.go" "$SCRIPT_DIR/api/proto/gorganizer_grpc.pb.go"
    ok "Build artifacts removed."
    ok "Uninstalled."
}

# --- dispatch --------------------------------------------------------------

[ "${GORGANIZER_SH_SOURCE_ONLY:-}" = 1 ] && return 0

if $RELEASE_MODE; then
    case "${1:-}" in
        "") cmd_register; exit $? ;;
        setup|build|--rebuild)
            err "This is a prebuilt copy of Gorganizer. You do not need to build it."
            exit 1 ;;
        import)
            err "Import from an old source folder is not available in this prebuilt copy."
            exit 1 ;;
    esac
fi

# Compatibility alias: --rebuild → build --rebuild (top-level, no subcommand).
if [ "${1:-}" = "--rebuild" ]; then
    set -- build --rebuild
fi

case "${1:-}" in
    "")
        cmd_install
        ;;
    launch)
        shift; cmd_launch "$@"
        ;;
    stop)
        shift; cmd_stop "$@"
        ;;
    setup)
        shift; cmd_setup "$@"
        ;;
    doctor)
        shift; cmd_doctor "$@"
        ;;
    build)
        shift
        force=""
        while [ $# -gt 0 ]; do
            case "$1" in
                --rebuild) force="force"; shift ;;
                *) err "Unknown option: $1"; exit 2 ;;
            esac
        done
        if [ -z "$force" ] && ! needs_build; then
            ok "Already up to date."
            exit 0
        fi
        do_build "$force"
        ;;
    update)
        shift; cmd_update "$@"
        ;;
    register|--register-nxm)
        shift; cmd_register "$@"
        ;;
    unregister)
        shift; cmd_unregister "$@"
        ;;
    nxm|--nxm)
        shift; cmd_nxm "$@"
        ;;
    import)
        shift; cmd_import "$@"
        ;;
    uninstall)
        shift; cmd_uninstall "$@"
        ;;
    --version|-v|version)
        echo "gorganizer.sh $(gorganizer_version)"
        ;;
    --help|-h|help)
        usage
        ;;
    *)
        err "Unknown subcommand: $1"
        usage >&2
        exit 2
        ;;
esac
