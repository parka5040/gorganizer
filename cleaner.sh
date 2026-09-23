#!/bin/bash
# cleaner.sh — Reset gorganizer to a clean first-time-user state.
# Removes all build artifacts, mod folders, config, and runtime data.
# Source code is untouched.
#
# Usage:
#   ./cleaner.sh                Clean everything (asks for confirmation)
#   ./cleaner.sh --keep-mods    Clean build/config but keep mod folders
#   ./cleaner.sh --yes          Skip confirmation (for scripted reset)
set -euo pipefail

if [ -t 1 ]; then
    CYAN='\033[0;36m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    RESET='\033[0m'
else
    CYAN='' GREEN='' YELLOW='' RESET=''
fi

log()  { echo -e "${CYAN}[cleaner]${RESET} $*"; }
ok()   { echo -e "${CYAN}[cleaner]${RESET} ${GREEN}✓${RESET} $*"; }
warn() { echo -e "${CYAN}[cleaner]${RESET} ${YELLOW}⚠${RESET} $*"; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

KEEP_MODS=false
ASSUME_YES=false
for arg in "$@"; do
    case "$arg" in
        --keep-mods) KEEP_MODS=true ;;
        --yes|-y) ASSUME_YES=true ;;
        --help|-h)
            echo "Usage: $0 [--keep-mods] [--yes]"
            echo ""
            echo "  (no args)     Remove everything: build, mods, config, runtime"
            echo "  --keep-mods   Keep mod folders (*_Mods/), clean everything else"
            echo "  --yes, -y     Skip the destructive-action confirmation prompt"
            exit 0
            ;;
        *) echo "Unknown flag: $arg"; exit 1 ;;
    esac
done

# Confirm before doing anything destructive. The script removes mods,
# config, and downloads — without a gate, a fat-fingered tab-complete
# can wipe a user's entire modding setup. --yes opts out for CI / dev
# scripts that already know what they're doing.
if ! $ASSUME_YES; then
    if $KEEP_MODS; then
        warn "About to remove: build artifacts, config (~/.config/gorganizer),"
        warn "                 data (~/.local/share/gorganizer), runtime, desktop entries."
        warn "Mod folders (*_Mods/) will be KEPT."
    else
        warn "About to remove: build artifacts, ALL *_Mods/ folders in $SCRIPT_DIR,"
        warn "                 config (~/.config/gorganizer),"
        warn "                 data (~/.local/share/gorganizer), runtime, desktop entries."
    fi
    if [ -t 0 ]; then
        read -r -p "$(echo -e "${CYAN}[cleaner]${RESET} Type 'yes' to proceed: ")" reply || reply=""
        if [ "$reply" != "yes" ]; then
            log "Cancelled."
            exit 0
        fi
    else
        warn "Non-interactive shell and --yes not given; aborting."
        exit 1
    fi
fi

# Stop a running daemon before deleting anything. SIGTERM lets it capture
# new writes and restore each game's vanilla Data/ on the way out; wait out
# the daemon's own 45s shutdown watchdog instead of killing it mid-teardown.
if pgrep -x gorganizerd >/dev/null 2>&1; then
    warn "Stopping running gorganizerd..."
    pkill -TERM -x gorganizerd 2>/dev/null || true
    for _ in $(seq 1 460); do
        pgrep -x gorganizerd >/dev/null 2>&1 || break
        sleep 0.1
    done
    if pgrep -x gorganizerd >/dev/null 2>&1; then
        warn "gorganizerd did not exit. Nothing was removed; stop it and re-run."
        exit 1
    fi
    ok "Daemon stopped."
fi

# Build artifacts.
log "Removing build artifacts..."
rm -rf "$SCRIPT_DIR/build"
rm -rf "$SCRIPT_DIR/CMakeFiles"
rm -f  "$SCRIPT_DIR/gorganizerd" "$SCRIPT_DIR/gorganizerctl"
rm -f  "$SCRIPT_DIR/api/proto/"*.pb.go
ok "Build artifacts removed."

# Mod folders.
if $KEEP_MODS; then
    warn "Keeping mod folders (--keep-mods)."
else
    log "Removing mod folders..."
    rm -rf "$SCRIPT_DIR/"*_Mods
    ok "Mod folders removed."
fi

# Config (daemon config + Qt settings).
log "Removing config..."
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer"
ok "Config removed."

# Data (profiles, downloads).
log "Removing data..."
rm -rf "${XDG_DATA_HOME:-$HOME/.local/share}/gorganizer"
ok "Data removed."

# Runtime (socket, pid files).
log "Removing runtime..."
rm -rf "${XDG_RUNTIME_DIR:-/tmp}/gorganizer"
rm -rf "/tmp/gorganizer-$(id -u)"
rm -rf /tmp/gorganizer-extract-*
ok "Runtime removed."

# Desktop file registrations.
log "Removing desktop registrations..."
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/applications/gorganizer-nxm.desktop"
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/applications/gorganizer.desktop"
update-desktop-database "${XDG_DATA_HOME:-$HOME/.local/share}/applications" 2>/dev/null || true
ok "Desktop registrations removed."

echo ""
ok "Clean. Run ${GREEN}./gorganizer.sh${RESET} for a fresh start."
