#!/bin/bash
# cleaner.sh — Remove local build artifacts and old in-checkout mods.
# Removes build artifacts, old in-checkout mod folders, config, and temporary extraction data.
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
# shellcheck source=scripts/deploy-check.sh
. "$SCRIPT_DIR/scripts/deploy-check.sh"

KEEP_MODS=false
ASSUME_YES=false
for arg in "$@"; do
    case "$arg" in
        --keep-mods) KEEP_MODS=true ;;
        --yes|-y) ASSUME_YES=true ;;
        --help|-h)
            echo "Usage: $0 [--keep-mods] [--yes]"
            echo ""
            echo "  (no args)     Remove build, old in-checkout mods, config and temporary extracts"
            echo "  --keep-mods   Keep old in-checkout folders (*_Mods/)"
            echo "  Personal data in ~/.local/share/gorganizer is not touched."
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
        warn "                 temporary extracts, desktop entries."
        warn "Old in-checkout mod folders (*_Mods/) will be KEPT."
    else
        warn "About to remove: build artifacts, old in-checkout *_Mods/ folders in $SCRIPT_DIR,"
        warn "                 config (~/.config/gorganizer), temporary extracts, desktop entries."
    fi
    warn "Personal data in ~/.local/share/gorganizer is not touched."
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

if pgrep -u "$(id -u)" -x gorganizerd >/dev/null 2>&1; then
    warn "Close Gorganizer first (or run ./gorganizer.sh stop)."
    exit 1
fi
verify_games_restored || exit 1

# Build artifacts.
log "Removing build artifacts..."
rm -rf "$SCRIPT_DIR/build"
rm -rf "$SCRIPT_DIR/CMakeFiles"
rm -f  "$SCRIPT_DIR/gorganizerd" "$SCRIPT_DIR/gorganizerctl"
rm -f  "$SCRIPT_DIR/api/proto/"*.pb.go
ok "Build artifacts removed."

# Mod folders.
if $KEEP_MODS; then
    warn "Keeping old in-checkout mod folders (--keep-mods)."
else
    log "Removing old in-checkout mod folders..."
    rm -rf "$SCRIPT_DIR/"*_Mods
    ok "Old in-checkout mod folders removed."
fi

# Config (daemon config + Qt settings).
log "Removing config..."
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer"
ok "Config removed."

# Leave the daemon's socket and lock alone; remove only this user's extraction cache.
log "Removing temporary extracts..."
temp_base="${TMPDIR:-/tmp}"
temp_base="${temp_base%/}"
rm -rf "$temp_base/gorganizer-extract-$(id -u)-"*
ok "Temporary extracts removed."

# Desktop file registrations.
log "Removing desktop registrations..."
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/applications/gorganizer-nxm.desktop"
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/applications/gorganizer.desktop"
update-desktop-database "${XDG_DATA_HOME:-$HOME/.local/share}/applications" 2>/dev/null || true
ok "Desktop registrations removed."

echo ""
ok "Local cleanup complete. Run ${GREEN}./gorganizer.sh${RESET} to rebuild."
