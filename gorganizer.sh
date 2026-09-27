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
#   update [--restart]    Pull latest from origin/main, rebuild, re-register.
#                         Refuses to run if the working tree is dirty or not
#                         a git checkout. User config and *_Mods/ are
#                         preserved. --restart reminds you to reopen a running session.
#   register              (Re-)install desktop file + icon + nxm:// handler.
#   unregister            Reverse `register`.
#   nxm <URI>             One-shot: forward an nxm:// URL to the running daemon.
#   import [--from PATH]  Migrate legacy *_Mods/ folders into this clone.
#   uninstall [--purge]   After closing Gorganizer and restoring games, unregister
#                         and delete build artifacts. User data is preserved.
#                         --purge additionally removes config, profiles,
#                         caches, and the daemon log.
#   --rebuild             Compatibility alias for `build --rebuild`.
#   --version, -v         Print version and exit.
#   --help, -h            Show this message.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
# shellcheck source=scripts/deploy-check.sh
. "$SCRIPT_DIR/scripts/deploy-check.sh"

# --- version ---------------------------------------------------------------
# The VERSION file at the repo root is the single source of truth. The
# Makefile reads it, ldflags-stamps both binaries, and the GUI's
# GORGANIZER_VERSION compile define mirrors it. We surface it here so
# `./gorganizer.sh --version` works even before anything is built.
gorganizer_version() {
    local v
    if [ -f "$SCRIPT_DIR/VERSION" ]; then
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
BOOTSTRAP_SENTINEL="$CONFIG_DIR/.bootstrapped"

# Legacy *_Mods directory names. Keep in sync with `ModsDirName` in
# internal/gamedef/registry.go — the daemon and this script must agree on the
# folder layout. Format: "<gameID>:<DirName>".
GAME_MODS_DIRS=(
    "morrowind:Morrowind_Mods"
    "oblivion:Oblivion_Mods"
    "skyrim:Skyrim_Mods"
    "skyrimse:SkyrimSE_Mods"
    "fallout3:Fallout3_Mods"
    "falloutnv:FalloutNV_Mods"
    "fallout4:Fallout4_Mods"
    "starfield:Starfield_Mods"
    "oblivionremastered:OblivionRemastered_Mods"
    "ttw:TTW_Mods"
    "stardewvalley:StardewValley_Mods"
)

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
  update [--restart]    Pull latest from origin/main, rebuild, re-register.
                        Refuses to run if the working tree is dirty or not
                        a git checkout. User config and *_Mods/ are
                        preserved. --restart reminds you to reopen a running session.
  register              (Re-)install desktop file + icon + nxm:// handler.
  unregister            Reverse \`register\`.
  nxm <URI>             One-shot: forward an nxm:// URL to the running daemon.
  import [--from PATH]  Migrate legacy *_Mods/ folders into this clone.
  uninstall [--purge]   After closing Gorganizer and restoring games, unregister
                        and delete build artifacts. User data is preserved.
                        --purge additionally removes config, profiles,
                        caches, and the daemon log.
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

detect_distro_family() {
    local family="unknown"
    if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release
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

# Prompt-and-install build deps. Used by `setup` and the first-run flow.
# Returns 0 on success, non-zero if the user declined or the install failed.
install_deps_interactive() {
    local family="$1" missing install_cmd deps_rc=0
    local -a install_cmd_parts=()
    install_cmd="$(pm_install_cmd "$family")"
    if [ -z "$install_cmd" ]; then
        warn "Unknown distro family ($family). Install build deps manually:"
        warn "    Need: cmake, ninja, go (1.26+), protoc, protoc-gen-grpc,"
        warn "          qt6-base dev, grpc dev, 7zip, unzip."
        return 1
    fi
    missing_deps "$family" >/dev/null || deps_rc=$?
    case "$deps_rc" in
        2) ok "All build deps already installed."; return 0 ;;
        1) warn "Distro family unknown; can't auto-install."; return 1 ;;
    esac
    missing="${MISSING_BUILD_PACKAGES[*]}"
    if [ -z "$missing" ]; then
        warn "Build dependency packages could not be resolved; continuing without an install."
        return 0
    fi

    log "Missing build dependencies (${BOLD}$family${RESET}):"
    echo "    $missing" >&2
    log "Install command:"
    echo "    $install_cmd $missing" >&2
    if ! prompt_yn "Install now via sudo?" Y; then
        warn "Skipped. Run \`$install_cmd $missing\` yourself, then rerun."
        return 1
    fi
    sudo -v || { err "sudo authentication failed."; return 1; }
    read -r -a install_cmd_parts <<< "$install_cmd"
    if ! "${install_cmd_parts[@]}" "${MISSING_BUILD_PACKAGES[@]}"; then
        err "Package install failed."
        return 1
    fi
    ok "Build deps installed."
    check_go_version_warning
    return 0
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
    local gov
    gov="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
    [ -z "$gov" ] && return 0
    local major minor
    major="${gov%%.*}"
    minor="${gov#*.}"; minor="${minor%%.*}"
    if [ "${major:-0}" -lt 1 ] || { [ "${major:-0}" -eq 1 ] && [ "${minor:-0}" -lt 26 ]; }; then
        warn "Detected go${gov}; this project requires 1.26+."
        warn "If \`make\` fails with module-version errors, install a newer Go from"
        warn "    https://go.dev/dl/"
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
    if ! build_and_publish "${1:-}"; then
        err "The new build failed. Your installed version is unchanged."
        local family
        family="$(detect_distro_family)"
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

# Icon=<absolute path>  is more reliable than the theme-name lookup
# (Icon=gorganizer): the latter requires the icon cache to be current,
# which trips up launchers that read .desktop files synchronously
# (Niri's fuzzel/wofi, some KDE configurations).
write_desktop_file() {
    # Exec uses the `launch` subcommand because the no-arg form is the
    # install/update flow — running it from a launcher would silently
    # rebuild instead of opening the GUI. `launch` is the user-facing
    # run path and the only thing the desktop entry should ever invoke.
    cat > "$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Name=Gorganizer
Comment=Native Linux mod organizer for Bethesda games
Exec=$SCRIPT_DIR/gorganizer.sh launch
Icon=$ICON_DEST
Terminal=false
Categories=Game;Utility;
Keywords=mod;organizer;skyrim;fallout;bethesda;stardew;smapi;
Version=$(gorganizer_version)
EOF
}

write_nxm_desktop_file() {
    cat > "$NXM_DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Name=Gorganizer NXM Handler
Comment=Nexus Mods download handler for Gorganizer
Exec=$SCRIPT_DIR/gorganizer.sh nxm %u
Icon=$ICON_DEST
Terminal=false
Categories=Game;
NoDisplay=true
MimeType=x-scheme-handler/nxm;
EOF
}

# Idempotently write `key=value` under [section] in mimeapps.list. Drops any
# prior line for the same key first so handlers don't stack on re-runs. Uses
# python3 because the awk version was fiddly across awks.
mimeapps_ensure() {
    local section="$1" entry="$2"
    mkdir -p "$(dirname "$MIMEAPPS")"
    [ -f "$MIMEAPPS" ] || : > "$MIMEAPPS"
    python3 - "$MIMEAPPS" "$section" "$entry" <<'PYEOF'
import os, sys, tempfile
path, section, entry = sys.argv[1:4]
key = entry.split("=", 1)[0] + "="
hdr = f"[{section}]"
try:
    with open(path, "r", encoding="utf-8") as f:
        text = f.read()
except FileNotFoundError:
    text = ""
out, in_target, inserted, seen = [], False, False, False
for line in text.splitlines():
    s = line.strip()
    if s.startswith("[") and s.endswith("]"):
        in_target = (s == hdr)
        if in_target:
            seen = True
        out.append(line)
        if in_target and not inserted:
            out.append(entry); inserted = True
        continue
    if in_target and s.startswith(key):
        continue
    out.append(line)
if not seen:
    if out and out[-1].strip() != "":
        out.append("")
    out.append(hdr); out.append(entry)
new = "\n".join(out)
if not new.endswith("\n"):
    new += "\n"
# Atomic replace: write to a sibling tempfile, fsync, then os.replace.
# Without this, a crash mid-write (or two gorganizer instances racing)
# could leave mimeapps.list truncated and break every nxm:// link.
d = os.path.dirname(path) or "."
fd, tmp = tempfile.mkstemp(prefix=".mimeapps.", dir=d)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(new)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
except Exception:
    try: os.unlink(tmp)
    except OSError: pass
    raise
PYEOF
}

# Drop a `key=value` line from mimeapps.list (any section).
mimeapps_drop() {
    local entry="$1"
    [ -f "$MIMEAPPS" ] || return 0
    python3 - "$MIMEAPPS" "$entry" <<'PYEOF'
import os, sys, tempfile
path, entry = sys.argv[1:3]
with open(path, "r", encoding="utf-8") as f:
    text = f.read()
out = [line for line in text.splitlines() if line.strip() != entry]
new = "\n".join(out)
if not new.endswith("\n"):
    new += "\n"
d = os.path.dirname(path) or "."
fd, tmp = tempfile.mkstemp(prefix=".mimeapps.", dir=d)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(new)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
except Exception:
    try: os.unlink(tmp)
    except OSError: pass
    raise
PYEOF
}

# Returns 0 if the desktop entries / icon / NXM mime are missing or stale
# (Exec= paths point somewhere other than this clone, or still point to the
# pre-`launch`-subcommand entry that would re-run the installer instead of
# opening the GUI). Returns 1 if all good.
needs_register() {
    [ -f "$DESKTOP_FILE" ]     || return 0
    [ -f "$NXM_DESKTOP_FILE" ] || return 0
    [ -f "$ICON_DEST" ]        || return 0
    grep -qF "Exec=$SCRIPT_DIR/gorganizer.sh launch" "$DESKTOP_FILE"     2>/dev/null || return 0
    grep -qF "Exec=$SCRIPT_DIR/gorganizer.sh nxm"    "$NXM_DESKTOP_FILE" 2>/dev/null || return 0
    grep -qF "Icon=$ICON_DEST" "$DESKTOP_FILE"     2>/dev/null || return 0
    grep -qF "Icon=$ICON_DEST" "$NXM_DESKTOP_FILE" 2>/dev/null || return 0
    return 1
}

cmd_register() {
    if [ ! -f "$ICON_SRC" ]; then
        err "Icon missing at $ICON_SRC"
        return 1
    fi
    install -d "$APPS_DIR" "$ICON_DIR"
    install -Dm644 "$ICON_SRC" "$ICON_DEST"
    write_desktop_file
    write_nxm_desktop_file

    local entry="x-scheme-handler/nxm=gorganizer-nxm.desktop"
    mimeapps_ensure "Default Applications" "$entry"
    mimeapps_ensure "Added Associations"   "$entry"

    xdg-mime default gorganizer-nxm.desktop x-scheme-handler/nxm 2>/dev/null || true
    update-desktop-database "$APPS_DIR" >/dev/null 2>&1 || true
    gtk-update-icon-cache "${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor" >/dev/null 2>&1 || true

    log "Desktop file: $DESKTOP_FILE"
    log "NXM handler:  $NXM_DESKTOP_FILE"
    log "Icon:         $ICON_DEST"
    log "Mimeapps:     $MIMEAPPS"

    local current
    current="$(xdg-mime query default x-scheme-handler/nxm 2>/dev/null || true)"
    if [ "$current" = "gorganizer-nxm.desktop" ]; then
        ok "nxm:// handler is now Gorganizer."
    else
        warn "xdg-mime says nxm:// default is '$current' (expected gorganizer-nxm.desktop)."
        warn "Browsers may still find us via mimeapps.list. Try logging out + back in if not."
    fi
    ok "Registered. Re-run after moving the clone directory."
}

cmd_unregister() {
    local entry="x-scheme-handler/nxm=gorganizer-nxm.desktop"
    [ -f "$DESKTOP_FILE" ]     && rm -f "$DESKTOP_FILE"     && log "Removed $DESKTOP_FILE"
    [ -f "$NXM_DESKTOP_FILE" ] && rm -f "$NXM_DESKTOP_FILE" && log "Removed $NXM_DESKTOP_FILE"
    [ -f "$ICON_DEST" ]        && rm -f "$ICON_DEST"        && log "Removed $ICON_DEST"
    mimeapps_drop "$entry"
    xdg-mime default '' x-scheme-handler/nxm 2>/dev/null || true
    update-desktop-database "$APPS_DIR" >/dev/null 2>&1 || true
    ok "Unregistered."
}

# --- migration -------------------------------------------------------------

# Detect mods left behind by the old install.sh layout
# (~/.local/share/gorganizer/<gameID>/mods/) and offer to move them into
# this clone as <DirName>/. Returns 0 if any were found+moved (or nothing
# to do); 1 only on hard failure.
migrate_legacy_install() {
    # Parallel arrays — paths theoretically could contain `|`, and bash
    # has no clean way to escape a delimiter inside an array element. A
    # second parallel array per field is uglier but unambiguous.
    local srcs=() dsts=() games=() mapping
    for mapping in "${GAME_MODS_DIRS[@]}"; do
        local game="${mapping%%:*}" name="${mapping##*:}"
        local src="$DATA_DIR/$game/mods"
        local dst="$SCRIPT_DIR/$name"
        if [ -d "$src" ] && [ ! -e "$dst" ]; then
            srcs+=("$src")
            dsts+=("$dst")
            games+=("$game")
        fi
    done
    [ ${#srcs[@]} -eq 0 ] && return 0

    log "Found legacy mod folders from a previous install:"
    local i
    for ((i = 0; i < ${#srcs[@]}; i++)); do
        echo "    ${srcs[$i]}" >&2
        echo "        →  ${dsts[$i]}" >&2
    done
    if ! prompt_yn "Move them into this clone now?" N; then
        warn "Skipped. Run \`./gorganizer.sh import\` to revisit later."
        return 0
    fi

    for ((i = 0; i < ${#srcs[@]}; i++)); do
        if mv "${srcs[$i]}" "${dsts[$i]}"; then
            ok "Moved ${srcs[$i]} → ${dsts[$i]}"
            # Remove now-empty parent if it has no other contents.
            rmdir "$DATA_DIR/${games[$i]}" 2>/dev/null || true
        else
            err "Failed to move ${srcs[$i]}"
        fi
    done
}

# Migrate from another clone of gorganizer (the old in-tree dev pattern).
migrate_from_path() {
    local from="$1"
    [ -d "$from" ] || { err "No such directory: $from"; return 1; }
    from="$(cd "$from" && pwd)"
    if [ "$from" = "$SCRIPT_DIR" ]; then
        err "Source equals current clone. Nothing to do."
        return 1
    fi

    local srcs=() dsts=() mapping
    for mapping in "${GAME_MODS_DIRS[@]}"; do
        local name="${mapping##*:}"
        local src="$from/$name"
        local dst="$SCRIPT_DIR/$name"
        if [ -d "$src" ]; then
            if [ -e "$dst" ]; then
                warn "Skipping $name: target exists at $dst"
                continue
            fi
            srcs+=("$src")
            dsts+=("$dst")
        fi
    done
    [ ${#srcs[@]} -eq 0 ] && { log "No *_Mods/ folders found in $from"; return 0; }

    log "Will move from $from:"
    local i
    for ((i = 0; i < ${#srcs[@]}; i++)); do
        echo "    ${srcs[$i]}  →  ${dsts[$i]}" >&2
    done
    if ! prompt_yn "Proceed?" N; then
        warn "Cancelled."
        return 0
    fi
    for ((i = 0; i < ${#srcs[@]}; i++)); do
        if mv "${srcs[$i]}" "${dsts[$i]}"; then
            ok "Moved ${srcs[$i]} → ${dsts[$i]}"
        else
            err "Failed to move ${srcs[$i]}"
        fi
    done
}

cmd_import() {
    local from=""
    while [ $# -gt 0 ]; do
        case "$1" in
            --from) shift; from="${1:-}"; shift ;;
            *) err "Unknown option: $1"; return 2 ;;
        esac
    done
    if [ -n "$from" ]; then
        migrate_from_path "$from"
    else
        migrate_legacy_install
    fi
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
    [ -f "$DESKTOP_FILE" ] || return 1
    grep -qF "Exec=$SCRIPT_DIR/gorganizer.sh launch" "$DESKTOP_FILE" 2>/dev/null || return 1
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
        install_deps_interactive "$family" || true
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

    # First-install migration only — gated by the sentinel so updates
    # never re-prompt for legacy *_Mods/ moves.
    if [ ! -f "$BOOTSTRAP_SENTINEL" ]; then
        migrate_legacy_install || true
        mkdir -p "$CONFIG_DIR"
        touch "$BOOTSTRAP_SENTINEL"
    fi

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
    log "  Mod root:  $SCRIPT_DIR/<Game>_Mods/"
    log "  Desktop:   $DESKTOP_FILE"
    echo ""
    log "Launch via your application menu, or run:"
    log "    ${BOLD}./gorganizer.sh launch${RESET}"
}

# --- launch ----------------------------------------------------------------

cmd_launch() {
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ] || [ ! -x "$CTL_BIN" ]; then
        err "Gorganizer is not built yet."
        err "Run \`./gorganizer.sh\` from this clone to build and install."
        exit 1
    fi

    export QT_LOGGING_RULES="${QT_LOGGING_RULES:+$QT_LOGGING_RULES;}qt.dbus.*=false;qt.qpa.systemtray.*=false;qt.qpa.theme.dbus.*=false;qt.qpa.theme.debug=false"
    export GORGANIZER_ROOT="$SCRIPT_DIR"
    exec "$CTL_BIN" session --daemon "$DAEMON_BIN" --gui "$GUI_BIN" -- "$@"
}

# --- nxm forwarding --------------------------------------------------------

cmd_nxm() {
    local uri="${1:-}"
    if [ -z "$uri" ]; then
        err "Usage: $0 nxm <URI>"
        exit 2
    fi
    if [ ! -x "$DAEMON_BIN" ]; then
        err "Daemon not built yet. Run ./gorganizer.sh first."
        exit 1
    fi
    exec "$DAEMON_BIN" --handle-nxm "$uri"
}

# --- update ----------------------------------------------------------------

cmd_update() {
    # In-place update of an existing checkout. Pulls origin/main, forces a
    # clean rebuild and refreshes the desktop entry (Exec= path may have moved
    # under the user). User config under
    # $CONFIG_DIR and mod data under each <Game>_Mods/ tree are never
    # touched here — git pull only changes tracked files.
    while [ $# -gt 0 ]; do
        case "$1" in
            --restart) shift ;;
            *) err "Unknown option: $1"; return 2 ;;
        esac
    done

    if ! command -v git >/dev/null 2>&1; then
        err "git not found in PATH; can't update."
        exit 1
    fi
    if [ ! -d "$SCRIPT_DIR/.git" ]; then
        err "$SCRIPT_DIR is not a git checkout."
        err "Re-clone the repo to update:"
        err "    git clone https://github.com/parka5040/gorganizer ~/gorganizer"
        exit 1
    fi

    # Refuse to clobber local edits — they're the user's, not ours to merge.
    if ! git -C "$SCRIPT_DIR" diff --quiet HEAD -- 2>/dev/null \
       || [ -n "$(git -C "$SCRIPT_DIR" status --porcelain)" ]; then
        err "Working tree has uncommitted changes:"
        git -C "$SCRIPT_DIR" status -s >&2
        err "Stash or commit them, then re-run \`./gorganizer.sh update\`."
        exit 1
    fi

    local old_sha new_sha
    old_sha=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD)

    log "Fetching origin..."
    if ! git -C "$SCRIPT_DIR" fetch --quiet origin; then
        err "git fetch failed."
        exit 1
    fi

    log "Pulling --ff-only..."
    if ! git -C "$SCRIPT_DIR" pull --ff-only --quiet origin main; then
        err "Non-fast-forward (or other) pull failure. Resolve manually:"
        err "    cd $SCRIPT_DIR && git pull"
        exit 1
    fi

    new_sha=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD)
    if [ "$old_sha" = "$new_sha" ]; then
        ok "Already on the latest commit ($new_sha)."
    else
        ok "Updated $old_sha -> $new_sha:"
        git -C "$SCRIPT_DIR" log --oneline "${old_sha}..${new_sha}" || true
    fi

    log "Rebuilding from clean..."
    do_build force || exit 1

    log "Refreshing desktop entry..."
    cmd_register || warn "Desktop registration reported issues."

    if pgrep -u "$(id -u)" -x gorganizerd >/dev/null 2>&1; then
        warn "Close Gorganizer and open it again to use the update."
    fi

    ok "Update complete."
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
    local family logical candidates resolved install_cmd build_rc=0
    family="$(detect_distro_family)"
    install_cmd="$(pm_install_cmd "$family")"
    log "Distro family: ${BOLD}$family${RESET}"
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

cmd_uninstall() {
    local purge=false
    while [ $# -gt 0 ]; do
        case "$1" in
            --purge) purge=true; shift ;;
            *) err "Unknown option: $1"; return 2 ;;
        esac
    done

    if pgrep -u "$(id -u)" -x gorganizerd >/dev/null 2>&1; then
        err "Close Gorganizer first (or run ./gorganizer.sh stop), then run uninstall again."
        return 1
    fi
    verify_games_restored || return 1

    # Remove desktop entry, NXM handler, icon, mime registration.
    cmd_unregister

    # Always-on: blow away build artifacts. The whole point of `uninstall`
    # is to leave nothing executable behind that the launcher could still
    # find via PATH or a stale .desktop somewhere else.
    if [ -e "$DAEMON_BIN" ] || [ -e "$CTL_BIN" ] || [ -d "$SCRIPT_DIR/build" ]; then
        make clean >/dev/null 2>&1 || true
        rm -rf "$SCRIPT_DIR/build"
        rm -f "$DAEMON_BIN" "$CTL_BIN"
        ok "Build artifacts removed."
    fi

    # User data is preserved by default — config, profiles, downloads,
    # and the daemon log are exactly what the user wants to keep across
    # a reinstall. --purge is the explicit nuke option.
    local user_dirs=("$CONFIG_DIR" "$DATA_DIR" "${XDG_STATE_HOME:-$HOME/.local/state}/gorganizer")
    if $purge; then
        for d in "${user_dirs[@]}"; do
            [ -e "$d" ] && rm -rf "$d" && log "Purged $d"
        done
    else
        local kept=()
        for d in "${user_dirs[@]}"; do [ -e "$d" ] && kept+=("$d"); done
        if [ ${#kept[@]} -gt 0 ]; then
            log "User data preserved (run with --purge to remove):"
            for d in "${kept[@]}"; do echo "    $d" >&2; done
        fi
    fi

    echo ""
    log "${BOLD}*_Mods/${RESET} folders in $SCRIPT_DIR are user data — left untouched."
    warn "Your mods are still in $SCRIPT_DIR/*_Mods/ — move them somewhere safe before deleting this folder."
    ok "Uninstalled."
}

# --- dispatch --------------------------------------------------------------

[ "${GORGANIZER_SH_SOURCE_ONLY:-}" = 1 ] && return 0

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
        shift; cmd_nxm "${1:-}"
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
