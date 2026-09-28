#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 1 ] && [ -f "$1" ] || { printf 'Usage: smoke-release.sh <tarball>\n' >&2; exit 2; }
archive="$(realpath "$1")"
base="$(mktemp -d "${TMPDIR:?}/gzs.XXXXXXXX")"
trap 'rm -rf -- "$base"' EXIT
mkdir -p "$base/path with spaces" "$base/home" "$base/config" "$base/data" "$base/state" "$base/cache" "$base/runtime" "$base/shims"
chmod 700 "$base/runtime"
tar -xzf "$archive" -C "$base/path with spaces"
shopt -s nullglob
bundles=("$base/path with spaces"/gorganizer-*)
[ "${#bundles[@]}" -eq 1 ] || { printf 'Unexpected tarball layout.\n' >&2; exit 1; }
bundle="${bundles[0]}"
"$bundle/bin/gorganizerd" --version
"$bundle/bin/gorganizerctl" --version
if ldd "$bundle/bin/gorganizer-gui" | grep -q 'not found'; then
    printf 'GUI needs a missing library.\n' >&2
    exit 1
fi
for name in xdg-open steam notify-send; do
    printf '#!/bin/sh\nexit 1\n' > "$base/shims/$name"
    chmod 755 "$base/shims/$name"
done
export HOME="$base/home" XDG_CONFIG_HOME="$base/config" XDG_DATA_HOME="$base/data" \
    XDG_STATE_HOME="$base/state" XDG_CACHE_HOME="$base/cache" XDG_RUNTIME_DIR="$base/runtime" \
    QT_QPA_PLATFORM=offscreen PATH="$base/shims:$PATH"
unset QT_PLUGIN_PATH LD_LIBRARY_PATH GORGANIZER_ROOT
status=0
timeout --signal=INT --kill-after=25s 10s "$bundle/bin/gorganizerctl" session \
    --daemon "$bundle/bin/gorganizerd" --gui "$bundle/bin/gorganizer-gui" \
    > "$base/session.out" 2>&1 || status=$?
if [ "$status" -ne 124 ]; then
    printf 'Session exited before ten seconds (status %s):\n' "$status" >&2
    sed -n '1,80p' "$base/session.out" >&2
    exit 1
fi
log="$base/state/gorganizer/daemon.log"
[ -f "$log" ] || { printf 'No daemon log after session.\n' >&2; exit 1; }
if grep 'ERROR' "$log" | grep -v -i 'steam root not found' >&2; then
    printf 'Unexpected daemon errors.\n' >&2
    exit 1
fi
if pgrep -f "^$bundle/bin/gorganizer(d|-gui)" >/dev/null; then
    printf 'Session left a Gorganizer process running.\n' >&2
    exit 1
fi
printf 'Release smoke OK.\n'
