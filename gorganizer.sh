#!/bin/bash
# gorganizer.sh — single entry point for build, run, register, and uninstall.
#
# Canonical install (any clone path; ~/gorganizer is just an example):
#     git clone https://github.com/parka5040/gorganizer ~/gorganizer
#     cd ~/gorganizer
#     ./gorganizer.sh
#
# Subcommands:
#   (none)                Install (deps + build + desktop entry) and run.
#                         Re-running is cheap; register-state is verified
#                         each launch and refreshed if the clone moved.
#   setup                 Detect distro, install build deps via system PM.
#   build [--rebuild]     Build only. --rebuild forces a clean rebuild.
#   register              (Re-)install desktop file + icon + nxm:// handler.
#   unregister            Reverse `register`.
#   nxm <URI>             One-shot: forward an nxm:// URL to the running daemon.
#   import [--from PATH]  Migrate legacy *_Mods/ folders into this clone.
#   uninstall [--purge]   Stop daemon, unregister, prompt for user data + deps.
#   --rebuild             Compatibility alias for `build --rebuild`.
#   --help, -h            Show this message.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# --- paths -----------------------------------------------------------------

DAEMON_BIN="$SCRIPT_DIR/gorganizerd"
CTL_BIN="$SCRIPT_DIR/gorganizerctl"
GUI_BIN="$SCRIPT_DIR/build/src/gorganizer"
ICON_SRC="$SCRIPT_DIR/resources/icons/tmp_logo.png"

# Runtime — must match internal/config/paths.go and singleton.go.
RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp}/gorganizer"
SOCKET_PATH="$RUNTIME_DIR/gorganizer.sock"
LOCK_PATH="$RUNTIME_DIR/gorganizerd.lock"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/gorganizer"
DAEMON_LOG="$STATE_DIR/gorganizerd.log"

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

# Legacy *_Mods directory names. Keep in sync with `gameModsDirNames` in
# internal/config/paths.go — the daemon and this script must agree on the
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

usage() { sed -n '2,/^set -euo/p' "$0" | sed 's/^#\s\?//;s/^# //;/^set -euo/d'; }

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

# Build deps per family. Covers both build and runtime — building locally
# requires the dev headers anyway, and the runtime libs come along for free.
deps_for_family() {
    case "$1" in
        arch)
            echo "base-devel cmake ninja go protobuf grpc qt6-base fuse3 p7zip unzip"
            ;;
        debian)
            echo "build-essential cmake ninja-build golang-go protobuf-compiler protobuf-compiler-grpc libgrpc++-dev qt6-base-dev libfuse3-dev fuse3 p7zip-full unzip"
            ;;
        fedora)
            echo "gcc-c++ cmake ninja-build golang protobuf-compiler grpc-plugins grpc-devel qt6-qtbase-devel fuse3-devel fuse3 p7zip unzip"
            ;;
        suse)
            echo "gcc-c++ cmake ninja go protobuf-devel grpc-devel qt6-base-devel fuse3-devel fuse3 p7zip-full unzip"
            ;;
        *)
            echo ""
            ;;
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

# Returns missing packages on stdout (space-separated). Returns 0 if any are
# missing, 1 if family is unknown, 2 if all installed.
missing_deps() {
    local family="$1" deps missing=()
    deps="$(deps_for_family "$family")"
    [ -z "$deps" ] && return 1
    for p in $deps; do
        pkg_installed "$family" "$p" || missing+=("$p")
    done
    if [ ${#missing[@]} -eq 0 ]; then
        return 2
    fi
    echo "${missing[*]}"
    return 0
}

# Prompt-and-install build deps. Used by `setup` and the first-run flow.
# Returns 0 on success, non-zero if the user declined or the install failed.
install_deps_interactive() {
    local family="$1" missing install_cmd
    install_cmd="$(pm_install_cmd "$family")"
    if [ -z "$install_cmd" ]; then
        warn "Unknown distro family ($family). Install build deps manually:"
        warn "    Need: cmake, ninja, go (1.26+), protoc, protoc-gen-grpc,"
        warn "          qt6-base dev, grpc dev, fuse3 dev, p7zip, unzip."
        return 1
    fi
    case "$(missing_deps "$family"; echo "rc=$?")" in
        *"rc=2") ok "All build deps already installed."; return 0 ;;
        *"rc=1") warn "Distro family unknown; can't auto-install."; return 1 ;;
    esac
    missing="$(missing_deps "$family" || true)"
    [ -z "$missing" ] && { ok "All build deps already installed."; return 0; }

    log "Missing build dependencies (${BOLD}$family${RESET}):"
    echo "    $missing" >&2
    log "Install command:"
    echo "    $install_cmd $missing" >&2
    if ! prompt_yn "Install now via sudo?" Y; then
        warn "Skipped. Run \`sudo $install_cmd $missing\` yourself, then rerun."
        return 1
    fi
    sudo -v || { err "sudo authentication failed."; return 1; }
    # shellcheck disable=SC2086
    if ! $install_cmd $missing; then
        err "Package install failed."
        return 1
    fi
    ok "Build deps installed."
    check_go_version_warning
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

needs_build() {
    [ "${1:-}" = "force" ] && return 0
    [ ! -x "$DAEMON_BIN" ] && return 0
    [ ! -x "$GUI_BIN" ]    && return 0
    local changed
    changed=$(find "$SCRIPT_DIR" \
        \( -name '*.go' -o -name '*.cpp' -o -name '*.h' \
           -o -name '*.proto' -o -name 'CMakeLists.txt' \) \
        -not -path '*/build/*' \
        -newer "$GUI_BIN" -print -quit 2>/dev/null)
    [ -n "$changed" ]
}

do_build() {
    local force="${1:-}"
    if [ "$force" = "force" ]; then
        log "Cleaning previous build..."
        make clean >/dev/null
    fi
    log "Building (delegated to make)..."
    if ! make all gui; then
        err "Build failed."
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
    cat > "$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Name=Gorganizer
Comment=Native Linux mod organizer for Bethesda games
Exec=$SCRIPT_DIR/gorganizer.sh
Icon=$ICON_DEST
Terminal=false
Categories=Game;Utility;
Keywords=mod;organizer;skyrim;fallout;bethesda;
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
import sys
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
with open(path, "w", encoding="utf-8") as f:
    f.write(new)
PYEOF
}

# Drop a `key=value` line from mimeapps.list (any section).
mimeapps_drop() {
    local entry="$1"
    [ -f "$MIMEAPPS" ] || return 0
    python3 - "$MIMEAPPS" "$entry" <<'PYEOF'
import sys
path, entry = sys.argv[1:3]
with open(path, "r", encoding="utf-8") as f:
    text = f.read()
out = [line for line in text.splitlines() if line.strip() != entry]
new = "\n".join(out)
if not new.endswith("\n"):
    new += "\n"
with open(path, "w", encoding="utf-8") as f:
    f.write(new)
PYEOF
}

# Returns 0 if the desktop entries / icon / NXM mime are missing or stale
# (Exec= paths point somewhere other than this clone). Returns 1 if all good.
needs_register() {
    [ -f "$DESKTOP_FILE" ]     || return 0
    [ -f "$NXM_DESKTOP_FILE" ] || return 0
    [ -f "$ICON_DEST" ]        || return 0
    grep -qF "Exec=$SCRIPT_DIR/gorganizer.sh" "$DESKTOP_FILE"     2>/dev/null || return 0
    grep -qF "Exec=$SCRIPT_DIR/gorganizer.sh" "$NXM_DESKTOP_FILE" 2>/dev/null || return 0
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
    local found=() mapping
    for mapping in "${GAME_MODS_DIRS[@]}"; do
        local game="${mapping%%:*}" name="${mapping##*:}"
        local src="$DATA_DIR/$game/mods"
        local dst="$SCRIPT_DIR/$name"
        if [ -d "$src" ] && [ ! -e "$dst" ]; then
            found+=("$src|$dst|$game")
        fi
    done
    [ ${#found[@]} -eq 0 ] && return 0

    log "Found legacy mod folders from a previous install:"
    local entry
    for entry in "${found[@]}"; do
        local s="${entry%%|*}"
        local rest="${entry#*|}"
        local d="${rest%%|*}"
        echo "    $s" >&2
        echo "        →  $d" >&2
    done
    if ! prompt_yn "Move them into this clone now?" N; then
        warn "Skipped. Run \`./gorganizer.sh import\` to revisit later."
        return 0
    fi

    for entry in "${found[@]}"; do
        local s="${entry%%|*}"
        local rest="${entry#*|}"
        local d="${rest%%|*}"
        local game="${rest##*|}"
        if mv "$s" "$d"; then
            ok "Moved $s → $d"
            # Remove now-empty parent if it has no other contents.
            rmdir "$DATA_DIR/$game" 2>/dev/null || true
        else
            err "Failed to move $s"
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

    local found=() mapping
    for mapping in "${GAME_MODS_DIRS[@]}"; do
        local name="${mapping##*:}"
        local src="$from/$name"
        local dst="$SCRIPT_DIR/$name"
        if [ -d "$src" ]; then
            if [ -e "$dst" ]; then
                warn "Skipping $name: target exists at $dst"
                continue
            fi
            found+=("$src|$dst")
        fi
    done
    [ ${#found[@]} -eq 0 ] && { log "No *_Mods/ folders found in $from"; return 0; }

    log "Will move from $from:"
    local entry
    for entry in "${found[@]}"; do
        local s="${entry%%|*}" d="${entry##*|}"
        echo "    $s  →  $d" >&2
    done
    if ! prompt_yn "Proceed?" N; then
        warn "Cancelled."
        return 0
    fi
    for entry in "${found[@]}"; do
        local s="${entry%%|*}" d="${entry##*|}"
        if mv "$s" "$d"; then
            ok "Moved $s → $d"
        else
            err "Failed to move $s"
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
# This is the orphan-daemon fix from the old launcher. internal/ipc/server.go
# unconditionally os.Remove()s the socket on bind, so without explicit kill
# of the prior daemon, every restart leaves an orphan. Don't simplify.

kill_stale_daemons() {
    if ! pgrep -x gorganizerd >/dev/null; then
        return 0
    fi
    log "Terminating stale gorganizerd process(es)..."
    pkill -TERM -x gorganizerd 2>/dev/null || true
    local i
    for i in $(seq 1 30); do
        pgrep -x gorganizerd >/dev/null || break
        sleep 0.1
    done
    if pgrep -x gorganizerd >/dev/null; then
        warn "Stubborn daemons, SIGKILL"
        pkill -KILL -x gorganizerd 2>/dev/null || true
        sleep 0.3
    fi
    rm -f "$SOCKET_PATH" "$LOCK_PATH"
}

DAEMON_PID=""
start_daemon() {
    mkdir -p "$RUNTIME_DIR" "$STATE_DIR"
    : > "$DAEMON_LOG"

    # GORGANIZER_ROOT pins per-game mod folders to the project dir
    # (e.g. ./FalloutNV_Mods/) instead of ~/.local/share/gorganizer/...
    GORGANIZER_ROOT="$SCRIPT_DIR" \
        "$DAEMON_BIN" --log-level info >"$DAEMON_LOG" 2>&1 &
    DAEMON_PID=$!

    local i
    for i in $(seq 1 50); do
        if [ -S "$SOCKET_PATH" ]; then
            ok "Daemon up (pid $DAEMON_PID, log: $DAEMON_LOG)"
            return 0
        fi
        if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
            err "Daemon exited during startup. Last 20 log lines:"
            tail -20 "$DAEMON_LOG" | sed 's/^/    /' >&2 || true
            exit 1
        fi
        sleep 0.1
    done
    err "Daemon started but socket never appeared at $SOCKET_PATH"
    tail -20 "$DAEMON_LOG" | sed 's/^/    /' >&2 || true
    kill -TERM "$DAEMON_PID" 2>/dev/null || true
    exit 1
}

stop_daemon_trap() {
    local rc=$?
    if [ -n "${DAEMON_PID:-}" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        log "Shutting down daemon (pid $DAEMON_PID)..."
        kill -TERM "$DAEMON_PID" 2>/dev/null || true
        local i
        for i in $(seq 1 50); do
            kill -0 "$DAEMON_PID" 2>/dev/null || break
            sleep 0.1
        done
        if kill -0 "$DAEMON_PID" 2>/dev/null; then
            kill -KILL "$DAEMON_PID" 2>/dev/null || true
        fi
    fi
    rm -f "$SOCKET_PATH" "$LOCK_PATH"
    exit "$rc"
}

# --- run -------------------------------------------------------------------

cmd_run() {
    if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ] || needs_build; then
        local family
        family="$(detect_distro_family)"
        if [ ! -x "$DAEMON_BIN" ] || [ ! -x "$GUI_BIN" ]; then
            # First build — try to ensure deps are present.
            install_deps_interactive "$family" || true
        fi
        do_build || exit 1
    fi

    # First-run migration: only if the bootstrap sentinel is absent.
    if [ ! -f "$BOOTSTRAP_SENTINEL" ]; then
        migrate_legacy_install || true
        mkdir -p "$CONFIG_DIR"
        touch "$BOOTSTRAP_SENTINEL"
    fi

    # Auto-register the desktop entry so the app shows up in the system
    # launcher / start menu. Cheap on subsequent runs (the check is just
    # three stat()s + a grep). Re-runs if the clone moved (Exec= path
    # mismatch) so launcher entries don't go stale silently.
    if needs_register; then
        log "Installing desktop entry and nxm:// handler..."
        cmd_register || warn "Desktop registration reported issues."
    fi

    # Silence Qt6 D-Bus / system tray noise on systems without a
    # cooperative notification server. Doesn't mute Qt's actual errors.
    export QT_LOGGING_RULES="${QT_LOGGING_RULES:+$QT_LOGGING_RULES;}qt.dbus.*=false;qt.qpa.systemtray.*=false;qt.qpa.theme.dbus.*=false;qt.qpa.theme.debug=false"
    export GORGANIZER_ROOT="$SCRIPT_DIR"

    echo ""
    echo -e "  ${BOLD}Gorganizer${RESET}"
    echo -e "  ${CYAN}----------${RESET}"
    log "  Daemon:    $DAEMON_BIN"
    log "  Frontend:  $GUI_BIN"
    log "  Mod root:  $SCRIPT_DIR/<Game>_Mods/"
    log "  Socket:    $SOCKET_PATH"
    log "  Log:       $DAEMON_LOG"
    echo ""

    kill_stale_daemons
    start_daemon
    trap stop_daemon_trap EXIT INT TERM

    exec "$GUI_BIN"
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

# --- setup -----------------------------------------------------------------

cmd_setup() {
    local family
    family="$(detect_distro_family)"
    log "Distro family: ${BOLD}$family${RESET}"
    install_deps_interactive "$family"
}

# --- uninstall -------------------------------------------------------------

cmd_uninstall() {
    local force_purge=false
    while [ $# -gt 0 ]; do
        case "$1" in
            --purge) force_purge=true; shift ;;
            *) err "Unknown option: $1"; return 2 ;;
        esac
    done

    # Stop daemon first.
    if pgrep -x gorganizerd >/dev/null 2>&1; then
        log "Stopping running gorganizerd..."
        pkill -TERM -x gorganizerd 2>/dev/null || true
        local i
        for i in $(seq 1 30); do
            pgrep -x gorganizerd >/dev/null 2>&1 || break
            sleep 0.1
        done
        pkill -KILL -x gorganizerd 2>/dev/null || true
        rm -f "$SOCKET_PATH" "$LOCK_PATH"
    fi

    cmd_unregister

    # User data prompt.
    local user_dirs=("$CONFIG_DIR" "$DATA_DIR" "${XDG_STATE_HOME:-$HOME/.local/state}/gorganizer")
    local existing=()
    for d in "${user_dirs[@]}"; do [ -e "$d" ] && existing+=("$d"); done
    if [ ${#existing[@]} -gt 0 ]; then
        if $force_purge; then
            for d in "${existing[@]}"; do rm -rf "$d" && log "Purged $d"; done
        else
            warn "User data exists at:"
            for d in "${existing[@]}"; do echo "    $d" >&2; done
            warn "These hold your config, profiles, downloads, and daemon log."
            if prompt_yn "Type yes to remove user data, anything else to keep:" N; then
                for d in "${existing[@]}"; do rm -rf "$d" && log "Removed $d"; done
            else
                log "User data preserved."
            fi
        fi
    fi

    # System packages prompt.
    local family deps remove_cmd
    family="$(detect_distro_family)"
    deps="$(deps_for_family "$family")"
    remove_cmd="$(pm_remove_cmd "$family")"
    if [ -n "$deps" ] && [ -n "$remove_cmd" ]; then
        echo ""
        warn "${BOLD}Optional:${RESET} remove the build/runtime packages installed for Gorganizer."
        warn "These may be shared with other applications:"
        echo "    $deps" >&2
        if prompt_yn "Remove them now? (default: no)" N; then
            sudo -v || { err "sudo failed; skipping package removal."; return 0; }
            # shellcheck disable=SC2086
            $remove_cmd $deps || warn "Package removal returned non-zero."
        else
            log "System packages preserved."
        fi
    fi

    # Local build artifacts in the clone.
    if [ -e "$DAEMON_BIN" ] || [ -e "$CTL_BIN" ] || [ -d "$SCRIPT_DIR/build" ]; then
        echo ""
        log "Local build artifacts in $SCRIPT_DIR:"
        [ -e "$DAEMON_BIN" ]      && echo "    $DAEMON_BIN" >&2
        [ -e "$CTL_BIN" ]         && echo "    $CTL_BIN" >&2
        [ -d "$SCRIPT_DIR/build" ] && echo "    $SCRIPT_DIR/build/" >&2
        if prompt_yn "Remove these build artifacts?" Y; then
            make clean >/dev/null 2>&1 || true
            ok "Build artifacts removed."
        fi
    fi

    echo ""
    log "${BOLD}*_Mods/${RESET} folders in $SCRIPT_DIR are user data — left untouched."
    log "To finish removal: ${BOLD}rm -rf $SCRIPT_DIR${RESET}"
    ok "Uninstalled."
}

# --- dispatch --------------------------------------------------------------

# Compatibility alias: --rebuild → build --rebuild (top-level, no subcommand).
if [ "${1:-}" = "--rebuild" ]; then
    set -- build --rebuild
fi

case "${1:-}" in
    "")
        cmd_run
        ;;
    setup)
        shift; cmd_setup "$@"
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
    register)
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
    --help|-h|help)
        usage
        ;;
    *)
        err "Unknown subcommand: $1"
        usage >&2
        exit 2
        ;;
esac
