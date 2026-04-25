#!/bin/bash
# Gorganizer installer.
#
# One-line install:
#   curl -fsSL https://raw.githubusercontent.com/parka735/gorganizer/main/install.sh | bash
#
# Or from a clone:
#   git clone https://github.com/parka735/gorganizer && cd gorganizer && ./install.sh
#
# Modes (auto-detected):
#   - offline: when this script lives next to a populated bin/ tree (it's
#     unpacked from a release tarball). Operates on the local files.
#   - network: otherwise. Fetches the latest release from GitHub, verifies
#     the sha256, extracts to a tempdir, then proceeds as if offline.
#
# Flags:
#   --version vX.Y.Z   Pin a specific release tag (network mode only).
#   --prerelease       Allow prerelease tags when picking "latest".
#   --prefix DIR       Override install prefix (default: ~/.local).
#   --skip-deps-check  Don't refuse to install when runtime libs look missing.
#   --help, -h         Show this message.
set -euo pipefail

GH_OWNER="parka735"
GH_REPO="gorganizer"
GH_API="https://api.github.com/repos/${GH_OWNER}/${GH_REPO}"

DEFAULT_PREFIX="$HOME/.local"
PREFIX="$DEFAULT_PREFIX"
PIN_VERSION=""
ALLOW_PRERELEASE=false
SKIP_DEPS_CHECK=false

if [ -t 1 ]; then
    BOLD='\033[1m'; CYAN='\033[0;36m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; RED='\033[0;31m'; RESET='\033[0m'
else
    BOLD=''; CYAN=''; GREEN=''; YELLOW=''; RED=''; RESET=''
fi
log()  { echo -e "${CYAN}[install]${RESET} $*"; }
ok()   { echo -e "${CYAN}[install]${RESET} ${GREEN}OK${RESET} $*"; }
warn() { echo -e "${CYAN}[install]${RESET} ${YELLOW}!!${RESET} $*"; }
err()  { echo -e "${CYAN}[install]${RESET} ${RED}XX${RESET} $*" >&2; }

usage() { sed -n '2,/^set -euo/p' "$0" | sed 's/^#\s\?//;s/^# //;/^set -euo/d'; }

# --- arg parse -------------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --version)        shift; PIN_VERSION="${1:-}"; shift ;;
        --prerelease)     ALLOW_PRERELEASE=true; shift ;;
        --prefix)         shift; PREFIX="${1:-}"; shift ;;
        --skip-deps-check) SKIP_DEPS_CHECK=true; shift ;;
        --help|-h)        usage; exit 0 ;;
        *)                err "Unknown option: $1"; usage >&2; exit 2 ;;
    esac
done

[ -n "$PREFIX" ] || { err "--prefix requires an argument"; exit 2; }

# --- mode detection --------------------------------------------------------
# When the script is part of an unpacked release tarball, bin/gorganizerd is
# its sibling. When fetched via raw.githubusercontent.com or invoked from a
# clone, that file is absent and we go to the network.
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

MODE=""
SOURCE_DIR=""
if [ -n "$SCRIPT_DIR" ] && [ -x "$SCRIPT_DIR/bin/gorganizerd" ]; then
    MODE="offline"
    SOURCE_DIR="$SCRIPT_DIR"
else
    MODE="network"
fi

# --- arch check ------------------------------------------------------------
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64) ;;
    *) err "Unsupported architecture: $ARCH (only x86_64 is built right now)"; exit 1 ;;
esac

# --- distro detect (for runtime-dep install hints) -------------------------
DISTRO_FAMILY="unknown"
if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    ids=" ${ID:-} ${ID_LIKE:-} "
    case "$ids" in
        *" arch "*|*" artix "*|*" manjaro "*|*" endeavouros "*) DISTRO_FAMILY="arch" ;;
        *" debian "*|*" ubuntu "*|*" linuxmint "*|*" pop "*|*" elementary "*) DISTRO_FAMILY="debian" ;;
        *" fedora "*|*" rhel "*|*" centos "*|*" nobara "*) DISTRO_FAMILY="fedora" ;;
        *" opensuse"*|*" suse "*) DISTRO_FAMILY="suse" ;;
    esac
fi

print_dep_install_hint() {
    echo "" >&2
    case "$DISTRO_FAMILY" in
        arch)    warn "Install: sudo pacman -S qt6-base grpc fuse3 p7zip" ;;
        debian)  warn "Install: sudo apt install qt6-base-dev libgrpc++1.51 libfuse3-3 fusermount3 p7zip-full unzip" ;;
        fedora)  warn "Install: sudo dnf install qt6-qtbase grpc fuse3 p7zip" ;;
        suse)    warn "Install: sudo zypper install qt6-base grpc fuse3 p7zip-full" ;;
        unknown) warn "Install Qt6 base, grpc, fuse3, and 7z/unrar/unzip via your package manager." ;;
    esac
}

# --- runtime dep check -----------------------------------------------------
# Read-only; never auto-installs. Probes via ldconfig -p and command -v.
check_runtime_deps() {
    log "Checking runtime libraries..."
    local missing=()

    # Critical shared libraries the GUI dynamically links.
    local libs=(
        "libQt6Core.so.6"
        "libQt6Gui.so.6"
        "libQt6Widgets.so.6"
        "libgrpc++.so"
        "libprotobuf.so"
        "libfuse3.so.3"
    )
    for lib in "${libs[@]}"; do
        if ldconfig -p 2>/dev/null | grep -q "$lib"; then
            ok "$lib"
        else
            warn "$lib not found"
            missing+=("$lib")
        fi
    done

    # FUSE3 setuid helper — required for the daemon to mount; the lib alone
    # isn't enough on most distros.
    if command -v fusermount3 >/dev/null 2>&1; then
        ok "fusermount3"
    else
        warn "fusermount3 not found"
        missing+=("fusermount3")
    fi

    # Optional archive tools — warn only.
    for t in 7z unrar unzip; do
        if command -v "$t" >/dev/null 2>&1; then
            ok "$t"
        else
            warn "$t not found (optional — limits archive support)"
        fi
    done

    if [ ${#missing[@]} -gt 0 ]; then
        echo "" >&2
        err "Critical runtime dependencies missing: ${missing[*]}"
        print_dep_install_hint
        if ! $SKIP_DEPS_CHECK; then
            echo "" >&2
            err "Refusing to install. Pass --skip-deps-check to override."
            exit 1
        fi
        warn "--skip-deps-check set; proceeding anyway. Gorganizer may fail to launch."
    fi
}

# --- network mode: fetch and unpack a release ------------------------------
fetch_release() {
    local tag="$1"  # may be empty → resolve "latest"
    local tmp
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    local api_url
    if [ -n "$tag" ]; then
        api_url="${GH_API}/releases/tags/${tag}"
    elif $ALLOW_PRERELEASE; then
        api_url="${GH_API}/releases?per_page=1"
    else
        api_url="${GH_API}/releases/latest"
    fi

    log "Querying $api_url"
    local meta="$tmp/release.json"
    if ! curl -fsSL --retry 3 -o "$meta" "$api_url"; then
        err "Failed to fetch release metadata from GitHub."
        err "If you've never tagged a release yet, run: git tag v0.1.0 && git push origin v0.1.0"
        exit 1
    fi

    # Parse without jq. Pull tag_name and the matching asset's download URL.
    local resolved_tag
    resolved_tag="$(grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' "$meta" | head -1 | sed -E 's/.*"([^"]+)"$/\1/')"
    if [ -z "$resolved_tag" ]; then
        err "Could not parse tag_name from GitHub response."
        exit 1
    fi
    log "Resolved release: ${BOLD}${resolved_tag}${RESET}"

    local asset_pat="gorganizer-${resolved_tag}-linux-${ARCH}.tar.gz"
    local asset_url
    asset_url="$(grep -oE '"browser_download_url"[[:space:]]*:[[:space:]]*"[^"]+"' "$meta" \
        | sed -E 's/.*"([^"]+)"$/\1/' | grep "${asset_pat}\$" | head -1 || true)"
    if [ -z "$asset_url" ]; then
        err "Release ${resolved_tag} does not contain asset ${asset_pat}."
        exit 1
    fi
    local sha_url="${asset_url}.sha256"

    log "Downloading $asset_pat"
    curl -fsSL --retry 3 -o "$tmp/$asset_pat" "$asset_url"
    log "Downloading checksum"
    curl -fsSL --retry 3 -o "$tmp/$asset_pat.sha256" "$sha_url"

    log "Verifying sha256..."
    ( cd "$tmp" && sha256sum -c "$asset_pat.sha256" ) || {
        err "Checksum verification failed."
        exit 1
    }
    ok "Checksum OK."

    log "Extracting..."
    tar -xzf "$tmp/$asset_pat" -C "$tmp"
    SOURCE_DIR="$tmp/gorganizer-${resolved_tag}-linux-${ARCH}"
    if [ ! -x "$SOURCE_DIR/bin/gorganizerd" ]; then
        err "Tarball did not contain expected layout. Aborting."
        exit 1
    fi
    ok "Extracted to $SOURCE_DIR"
}

# --- file install ----------------------------------------------------------
MANIFEST=""
record() { echo "$1" >> "$MANIFEST"; }

install_files() {
    local src="$SOURCE_DIR"

    local version
    version="$(cat "$src/VERSION" 2>/dev/null || echo "unknown")"

    log "Installing to ${BOLD}$PREFIX${RESET}"

    install -d "$PREFIX/bin" \
               "$PREFIX/libexec/gorganizer" \
               "$PREFIX/share/applications" \
               "$PREFIX/share/icons/hicolor/256x256/apps" \
               "$PREFIX/share/gorganizer"

    # Manifest header records version + prefix; uninstall reads this.
    MANIFEST="$PREFIX/share/gorganizer/install-manifest.txt"
    {
        echo "# gorganizer install manifest"
        echo "# version=$version"
        echo "# prefix=$PREFIX"
        echo "# installed-at=$(date -Iseconds)"
    } > "$MANIFEST"

    install -Dm755 "$src/bin/gorganizerd"     "$PREFIX/bin/gorganizerd"
    record "$PREFIX/bin/gorganizerd"
    install -Dm755 "$src/bin/gorganizerctl"   "$PREFIX/bin/gorganizerctl"
    record "$PREFIX/bin/gorganizerctl"
    install -Dm755 "$src/bin/gorganizer-gui"  "$PREFIX/libexec/gorganizer/gorganizer-gui"
    record "$PREFIX/libexec/gorganizer/gorganizer-gui"

    # Launcher: substitute @@PREFIX@@ in template, install as executable.
    sed "s|@@PREFIX@@|$PREFIX|g" "$src/libexec/gorganizer-launcher" > "$PREFIX/bin/gorganizer"
    chmod 0755 "$PREFIX/bin/gorganizer"
    record "$PREFIX/bin/gorganizer"

    # Desktop files: same @@PREFIX@@ substitution.
    sed "s|@@PREFIX@@|$PREFIX|g" "$src/share/applications/gorganizer.desktop.in" \
        > "$PREFIX/share/applications/gorganizer.desktop"
    record "$PREFIX/share/applications/gorganizer.desktop"
    sed "s|@@PREFIX@@|$PREFIX|g" "$src/share/applications/gorganizer-nxm.desktop.in" \
        > "$PREFIX/share/applications/gorganizer-nxm.desktop"
    record "$PREFIX/share/applications/gorganizer-nxm.desktop"

    if [ -f "$src/share/icons/hicolor/256x256/apps/gorganizer.png" ]; then
        install -Dm644 "$src/share/icons/hicolor/256x256/apps/gorganizer.png" \
            "$PREFIX/share/icons/hicolor/256x256/apps/gorganizer.png"
        record "$PREFIX/share/icons/hicolor/256x256/apps/gorganizer.png"
    fi

    # Ship the uninstaller and the standalone register-nxm helper.
    if [ -f "$src/uninstall.sh" ]; then
        install -Dm755 "$src/uninstall.sh" "$PREFIX/share/gorganizer/uninstall.sh"
        record "$PREFIX/share/gorganizer/uninstall.sh"
    fi
    write_register_nxm_helper
    record "$PREFIX/share/gorganizer/register-nxm.sh"

    record "$MANIFEST"
    ok "Files installed."
}

# --- nxm registration ------------------------------------------------------
# Logic ported from the old gorganizer.sh. The historical "register on first
# launch" approach silently broke when:
#   1. The folder moved (Exec= path went stale) — fixed because the
#      installed launcher path is fixed at $PREFIX/bin/gorganizer.
#   2. xdg-mime exited 0 even when the underlying mimeapps.list write was
#      rejected — Firefox-family browsers drop nxm clicks because their
#      protocol-handler list never sees the handler.
#   3. update-desktop-database isn't installed.
# Mitigation: write mimeapps.list directly, then xdg-mime + update-desktop
# as belt-and-suspenders.

write_register_nxm_helper() {
    local out="$PREFIX/share/gorganizer/register-nxm.sh"
    cat > "$out" <<'NXMSH'
#!/bin/bash
# Re-runnable nxm:// MIME handler registration. Installed by gorganizer's
# install.sh; called by `gorganizer --register-nxm`.
set -euo pipefail
DESKTOP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
DESKTOP_ID="gorganizer-nxm.desktop"
MIMEAPPS="${XDG_CONFIG_HOME:-$HOME/.config}/mimeapps.list"
ENTRY="x-scheme-handler/nxm=gorganizer-nxm.desktop"

mkdir -p "$DESKTOP_DIR" "$(dirname "$MIMEAPPS")"
[ -f "$MIMEAPPS" ] || : > "$MIMEAPPS"

# Idempotently ensure ENTRY appears under [<section>] in MIMEAPPS, dropping
# any prior entry for the same key first so handlers don't stack up.
ensure_section() {
    local section="$1"
    python3 - "$MIMEAPPS" "$section" "$ENTRY" "${ENTRY%%=*}=" <<'PYEOF' 2>/dev/null && return 0
import sys
path, section, entry, key = sys.argv[1:5]
try:
    with open(path, 'r', encoding='utf-8') as f:
        text = f.read()
except FileNotFoundError:
    text = ""
hdr = f"[{section}]"
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
with open(path, 'w', encoding='utf-8') as f:
    f.write(new)
PYEOF
    awk -v section="[$section]" -v entry="$ENTRY" -v key="${ENTRY%%=*}=" '
    BEGIN { in_section=0; written=0; section_seen=0 }
    /^\[.*\]$/ {
        in_section = ($0 == section)
        if (in_section) section_seen = 1
        print
        if (in_section && !written) { print entry; written = 1 }
        next
    }
    {
        if (in_section && index($0, key) == 1) next
        print
    }
    END {
        if (!section_seen) {
            print ""
            print section
            print entry
        }
    }
    ' "$MIMEAPPS" > "$MIMEAPPS.tmp" && mv "$MIMEAPPS.tmp" "$MIMEAPPS"
}

ensure_section "Default Applications"
ensure_section "Added Associations"

xdg-mime default "$DESKTOP_ID" x-scheme-handler/nxm 2>/dev/null || true
update-desktop-database "$DESKTOP_DIR" 2>/dev/null || true

current="$(xdg-mime query default x-scheme-handler/nxm 2>/dev/null || true)"
if [ "$current" = "$DESKTOP_ID" ]; then
    echo "[gorganizer] OK nxm:// handler registered."
else
    echo "[gorganizer] !! xdg-mime says nxm:// default is '$current' (expected $DESKTOP_ID)."
    echo "             Browsers may not route nxm:// links. Try logging out and back in."
fi
NXMSH
    chmod 0755 "$out"
}

# --- post-install ----------------------------------------------------------
post_install() {
    log "Registering nxm:// handler..."
    "$PREFIX/share/gorganizer/register-nxm.sh" || warn "Handler registration reported issues."

    update-desktop-database "$PREFIX/share/applications" >/dev/null 2>&1 || true

    case ":$PATH:" in
        *":$PREFIX/bin:"*) ;;
        *)
            echo ""
            warn "$PREFIX/bin is not in your PATH."
            warn "Add this to your shell config:"
            case "${SHELL:-}" in
                */zsh)  echo "    echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.zshrc" ;;
                */fish) echo "    fish_add_path $PREFIX/bin" ;;
                *)      echo "    echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.bashrc" ;;
            esac
            ;;
    esac
}

# --- main ------------------------------------------------------------------
echo ""
echo -e "  ${BOLD}Gorganizer installer${RESET}"
echo -e "  ${CYAN}--------------------${RESET}  mode: $MODE"
echo ""

check_runtime_deps

if [ "$MODE" = "network" ]; then
    fetch_release "$PIN_VERSION"
fi

install_files
post_install

echo ""
ok "${GREEN}Gorganizer installed.${RESET}"
echo ""
log "  Launch:    $PREFIX/bin/gorganizer  (or your app menu)"
log "  Uninstall: $PREFIX/share/gorganizer/uninstall.sh"
log "  Config:    \$HOME/.config/gorganizer/"
echo ""
