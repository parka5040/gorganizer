#!/bin/bash
# gorganizer.sh — Build (if needed) and run Gorganizer in-tree.
#
# Alternative to install.sh: keeps everything inside the project directory,
# no system install, no SONAME-portability worries because you're building
# against your own libraries.
#
# Use this for development, for distros where the published release tarball
# fails ldd (Arch, bleeding-edge Fedora), or any time you just want to
# clone-and-run without touching ~/.local.
#
# Usage:
#   ./gorganizer.sh                  Build if needed, then run.
#   ./gorganizer.sh --rebuild        Force a clean rebuild from scratch.
#   ./gorganizer.sh --nxm <URI>      Forward an nxm:// link to the daemon.
#   ./gorganizer.sh --register-nxm   Register this script as the system
#                                    nxm:// handler (so browsers find it).
#   ./gorganizer.sh --help           Show this message.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

DAEMON_BIN="$SCRIPT_DIR/gorganizerd"
GUI_BIN="$SCRIPT_DIR/build/src/gorganizer"

# Runtime paths — must match internal/config/paths.go and singleton.go.
RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp}/gorganizer"
SOCKET_PATH="$RUNTIME_DIR/gorganizer.sock"
LOCK_PATH="$RUNTIME_DIR/gorganizerd.lock"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/gorganizer"
DAEMON_LOG="$STATE_DIR/gorganizerd.log"

if [ -t 1 ]; then
    BOLD='\033[1m'; CYAN='\033[0;36m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; RED='\033[0;31m'; RESET='\033[0m'
else
    BOLD=''; CYAN=''; GREEN=''; YELLOW=''; RED=''; RESET=''
fi
log()  { echo -e "${CYAN}[gorganizer]${RESET} $*"; }
ok()   { echo -e "${CYAN}[gorganizer]${RESET} ${GREEN}OK${RESET} $*"; }
warn() { echo -e "${CYAN}[gorganizer]${RESET} ${YELLOW}!!${RESET} $*"; }
err()  { echo -e "${CYAN}[gorganizer]${RESET} ${RED}XX${RESET} $*" >&2; }

usage() { sed -n '2,/^set -euo/p' "$0" | sed 's/^#\s\?//;s/^# //;/^set -euo/d'; }

FORCE_REBUILD=false
NXM_URI=""
DO_REGISTER_NXM=false

while [ $# -gt 0 ]; do
    case "$1" in
        --rebuild)       FORCE_REBUILD=true; shift ;;
        --nxm)           shift; NXM_URI="${1:-}"; shift ;;
        --register-nxm)  DO_REGISTER_NXM=true; shift ;;
        --help|-h)       usage; exit 0 ;;
        *)               err "Unknown option: $1"; usage >&2; exit 2 ;;
    esac
done

# --- nxm forwarding (one-shot, no daemon lifecycle) -----------------------
# Browsers invoke us as `gorganizer.sh --nxm nxm://...`. Don't kill the
# user's running daemon — just forward the URL and exit.
if [ -n "$NXM_URI" ]; then
    if [ ! -x "$DAEMON_BIN" ]; then
        err "Daemon not built yet. Run ./gorganizer.sh first to build."
        exit 1
    fi
    exec "$DAEMON_BIN" --handle-nxm "$NXM_URI"
fi

# --- nxm handler registration ---------------------------------------------
# Writes ~/.local/share/applications/gorganizer-nxm.desktop pointing at THIS
# script's absolute path, plus mimeapps.list entries. Re-run after moving
# the project directory.
register_nxm_handler() {
    local desktop_dir="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
    local desktop_file="$desktop_dir/gorganizer-nxm.desktop"
    local mimeapps="${XDG_CONFIG_HOME:-$HOME/.config}/mimeapps.list"
    local entry="x-scheme-handler/nxm=gorganizer-nxm.desktop"

    mkdir -p "$desktop_dir" "$(dirname "$mimeapps")"
    [ -f "$mimeapps" ] || : > "$mimeapps"

    cat > "$desktop_file" <<NXMEOF
[Desktop Entry]
Type=Application
Name=Gorganizer NXM Handler (dev)
Exec=$SCRIPT_DIR/gorganizer.sh --nxm %u
Terminal=false
NoDisplay=true
MimeType=x-scheme-handler/nxm;
NXMEOF

    # Idempotent rewrite of mimeapps.list. Drops any prior nxm handler entry
    # so we don't stack them on re-runs.
    python3 - "$mimeapps" "$entry" <<'PYEOF' || true
import sys
path, entry = sys.argv[1:3]
key = entry.split("=", 1)[0] + "="
try:
    with open(path) as f:
        text = f.read()
except FileNotFoundError:
    text = ""
out, in_default, in_added, did_default, did_added = [], False, False, False, False
for line in text.splitlines():
    s = line.strip()
    if s.startswith("[") and s.endswith("]"):
        in_default = (s == "[Default Applications]")
        in_added   = (s == "[Added Associations]")
        out.append(line)
        if in_default and not did_default:
            out.append(entry); did_default = True
        if in_added and not did_added:
            out.append(entry); did_added = True
        continue
    if (in_default or in_added) and s.startswith(key):
        continue
    out.append(line)
if not did_default:
    if out and out[-1].strip() != "":
        out.append("")
    out.append("[Default Applications]"); out.append(entry); did_default = True
if not did_added:
    if out and out[-1].strip() != "":
        out.append("")
    out.append("[Added Associations]"); out.append(entry); did_added = True
new = "\n".join(out)
if not new.endswith("\n"):
    new += "\n"
with open(path, "w") as f:
    f.write(new)
PYEOF

    xdg-mime default gorganizer-nxm.desktop x-scheme-handler/nxm 2>/dev/null || true
    update-desktop-database "$desktop_dir" 2>/dev/null || true

    log "Desktop file: $desktop_file"
    log "Mimeapps:     $mimeapps"
}

if $DO_REGISTER_NXM; then
    register_nxm_handler
    ok "NXM handler registered to $SCRIPT_DIR/gorganizer.sh"
    exit 0
fi

# --- build (delegated to make) --------------------------------------------
needs_build() {
    $FORCE_REBUILD && return 0
    [ ! -x "$DAEMON_BIN" ] && return 0
    [ ! -x "$GUI_BIN" ]    && return 0

    # Rebuild if any source file is newer than the GUI binary. The GUI is
    # the slower of the two builds, so its mtime is the right target.
    local changed
    changed=$(find "$SCRIPT_DIR" \
        \( -name '*.go' -o -name '*.cpp' -o -name '*.h' \
           -o -name '*.proto' -o -name 'CMakeLists.txt' \) \
        -not -path '*/build/*' -not -path '*/release/*' -not -path '*/stage/*' \
        -newer "$GUI_BIN" -print -quit 2>/dev/null)
    [ -n "$changed" ] && return 0
    return 1
}

if needs_build; then
    if $FORCE_REBUILD; then
        log "Cleaning previous build..."
        make clean >/dev/null
    fi
    log "Building (delegated to make)..."
    make all gui
    ok "Build complete."
fi

# --- daemon lifecycle ------------------------------------------------------
# Same logic as the installed launcher (scripts/gorganizer-launcher.in):
# kill stale, spawn one, wait for socket, trap exit. The original
# gorganizer.sh did this inline; we keep it inline here too rather than
# sourcing the template, because the template's @@PREFIX@@ substitution
# only happens at install time.

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
    # This is the in-tree dev convention.
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

stop_daemon() {
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

# Silence Qt6 platform-integration noise on systems without libsystemd or a
# cooperative D-Bus notification server. Doesn't mute Qt's actual errors.
export QT_LOGGING_RULES="${QT_LOGGING_RULES:+$QT_LOGGING_RULES;}qt.dbus.*=false;qt.qpa.systemtray.*=false;qt.qpa.theme.dbus.*=false;qt.qpa.theme.debug=false"

export GORGANIZER_ROOT="$SCRIPT_DIR"

echo ""
echo -e "  ${BOLD}Gorganizer (dev runner)${RESET}"
echo -e "  ${CYAN}-----------------------${RESET}"
log "  Daemon:    $DAEMON_BIN"
log "  Frontend:  $GUI_BIN"
log "  Mod root:  $SCRIPT_DIR/<Game>_Mods/"
log "  Socket:    $SOCKET_PATH"
log "  Log:       $DAEMON_LOG"
echo ""

kill_stale_daemons
start_daemon
trap stop_daemon EXIT INT TERM

exec "$GUI_BIN"
