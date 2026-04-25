#!/bin/bash
# Gorganizer uninstaller. Removes everything install.sh placed (per the
# install manifest) and unregisters the nxm:// MIME handler.
#
# Does NOT remove user data unless --purge is passed:
#   - ~/.config/gorganizer/         (config: game paths, API key, etc.)
#   - ~/.local/share/gorganizer/    (profiles, mod stores, downloads)
#   - ~/.local/state/gorganizer/    (daemon log)
#
# Flags:
#   --prefix DIR   Look for the manifest under DIR/share/gorganizer/.
#                  Default: ~/.local.
#   --purge        Also remove user data (config, profiles, mod stores).
#                  Asks for confirmation first.
#   --help, -h     Show this message.
set -euo pipefail

PREFIX="$HOME/.local"
PURGE=false

if [ -t 1 ]; then
    BOLD='\033[1m'; CYAN='\033[0;36m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; RED='\033[0;31m'; RESET='\033[0m'
else
    BOLD=''; CYAN=''; GREEN=''; YELLOW=''; RED=''; RESET=''
fi
log()  { echo -e "${CYAN}[uninstall]${RESET} $*"; }
ok()   { echo -e "${CYAN}[uninstall]${RESET} ${GREEN}OK${RESET} $*"; }
warn() { echo -e "${CYAN}[uninstall]${RESET} ${YELLOW}!!${RESET} $*"; }
err()  { echo -e "${CYAN}[uninstall]${RESET} ${RED}XX${RESET} $*" >&2; }

while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) shift; PREFIX="${1:-}"; shift ;;
        --purge)  PURGE=true; shift ;;
        --help|-h)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^#\s\?//;s/^# //;/^set -euo/d'
            exit 0
            ;;
        *) err "Unknown option: $1"; exit 2 ;;
    esac
done

MANIFEST="$PREFIX/share/gorganizer/install-manifest.txt"

# Stop any running daemon before yanking the binary.
if pgrep -x gorganizerd >/dev/null 2>&1; then
    log "Stopping running gorganizerd..."
    pkill -TERM -x gorganizerd 2>/dev/null || true
    for _ in $(seq 1 30); do
        pgrep -x gorganizerd >/dev/null 2>&1 || break
        sleep 0.1
    done
    pkill -KILL -x gorganizerd 2>/dev/null || true
fi

# --- remove tracked files via manifest ------------------------------------
if [ -f "$MANIFEST" ]; then
    log "Reading manifest: $MANIFEST"
    # Read in reverse so files come off before the manifest itself.
    mapfile -t paths < <(grep -v '^#' "$MANIFEST" | tac)
    for p in "${paths[@]}"; do
        [ -z "$p" ] && continue
        if [ -e "$p" ] || [ -L "$p" ]; then
            rm -f "$p"
            log "  removed $p"
        fi
    done
    ok "Tracked files removed."
else
    warn "No manifest at $MANIFEST — falling back to known paths."
    for p in \
        "$PREFIX/bin/gorganizer" \
        "$PREFIX/bin/gorganizerd" \
        "$PREFIX/bin/gorganizerctl" \
        "$PREFIX/libexec/gorganizer/gorganizer-gui" \
        "$PREFIX/share/applications/gorganizer.desktop" \
        "$PREFIX/share/applications/gorganizer-nxm.desktop" \
        "$PREFIX/share/icons/hicolor/256x256/apps/gorganizer.png" \
        "$PREFIX/share/gorganizer/register-nxm.sh" \
        "$PREFIX/share/gorganizer/uninstall.sh"; do
        [ -e "$p" ] && rm -f "$p" && log "  removed $p"
    done
fi

# Prune now-empty install directories. rmdir refuses non-empty dirs, which is
# the safe behavior — leave anything we didn't put there alone.
for d in \
    "$PREFIX/libexec/gorganizer" \
    "$PREFIX/share/gorganizer"; do
    [ -d "$d" ] && rmdir --ignore-fail-on-non-empty "$d" 2>/dev/null || true
done

# --- unregister nxm:// MIME handler ---------------------------------------
log "Unregistering nxm:// handler..."
MIMEAPPS="${XDG_CONFIG_HOME:-$HOME/.config}/mimeapps.list"
if [ -f "$MIMEAPPS" ]; then
    # Strip our entry from both relevant sections; leave the rest of the file
    # untouched so we don't disturb other apps' associations.
    python3 - "$MIMEAPPS" <<'PYEOF' 2>/dev/null || true
import sys
path = sys.argv[1]
with open(path) as f:
    text = f.read()
out = []
for line in text.splitlines():
    if line.strip().startswith("x-scheme-handler/nxm=gorganizer-nxm.desktop"):
        continue
    out.append(line)
new = "\n".join(out)
if not new.endswith("\n"):
    new += "\n"
with open(path, "w") as f:
    f.write(new)
PYEOF
fi
xdg-mime default '' x-scheme-handler/nxm 2>/dev/null || true
update-desktop-database "$PREFIX/share/applications" 2>/dev/null || true

# --- optional purge --------------------------------------------------------
USER_DATA_DIRS=(
    "${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer"
    "${XDG_DATA_HOME:-$HOME/.local/share}/gorganizer"
    "${XDG_STATE_HOME:-$HOME/.local/state}/gorganizer"
)

if $PURGE; then
    echo ""
    warn "${BOLD}--purge will remove all of:${RESET}"
    for d in "${USER_DATA_DIRS[@]}"; do
        [ -e "$d" ] && warn "    $d"
    done
    echo ""
    read -r -p "Type 'yes' to confirm: " confirm
    if [ "$confirm" = "yes" ]; then
        for d in "${USER_DATA_DIRS[@]}"; do
            if [ -e "$d" ]; then
                rm -rf "$d"
                log "  purged $d"
            fi
        done
        ok "User data purged."
    else
        warn "Confirmation declined. User data preserved."
    fi
else
    echo ""
    log "User data preserved at:"
    for d in "${USER_DATA_DIRS[@]}"; do
        [ -e "$d" ] && log "    $d"
    done
    log "Pass --purge to remove these too."
fi

# Remind about per-game mod stores so the user knows where mods live.
echo ""
log "Per-game mod folders are NOT touched. They live under:"
log "    \${GORGANIZER_ROOT:-\$HOME/.local/share/gorganizer}/<Game>_Mods/"
log "Delete those manually if you want a complete wipe."
echo ""
ok "${GREEN}Gorganizer uninstalled.${RESET}"
