#!/usr/bin/env bash
# Run from the checkout on a desktop: bash scripts/release-e2e/run-e2e.sh [--no-launch] [--cleanup].
# Try the update-consent prompt: Yes, No, and close (✕), each in a fresh scratch run.
# For offline handling start with E2E_MODE=normal, then stop the printed server PID.
# Try E2E_MODE=status403 for a refused latest check.
# With normal mode, choose Update Now, close the GUI, and reopen with the printed launch.sh launch.
# With E2E_MODE=throttle, cancel while the archive downloads; reopen with launch.sh launch.
# With E2E_MODE=badsig, verify that the archive is never requested; reopen with launch.sh launch.
# Never run the scratch gorganizer.sh directly; use launch.sh for every manual launch.
# Launch a dev GUI separately to verify that it makes zero requests to the fixture server.
write_launch_wrapper() {
    {
        printf '#!/bin/bash\nexec env -i'
        printf ' %q' "${fixture_env[@]}" "$scratch/data/gorganizer/releases/current/gorganizer.sh"
        printf ' "$@"\n'
    } > "$scratch/launch.sh"
    chmod 700 "$scratch/launch.sh"
}
if [ "${GORGANIZER_SH_SOURCE_ONLY:-}" = 1 ]; then return 0; fi
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
no_launch=false
cleanup=false
for arg in "$@"; do
    case "$arg" in
        --no-launch) no_launch=true ;;
        --cleanup) cleanup=true ;;
        *) printf 'Unknown option: %s\n' "$arg" >&2; exit 2 ;;
    esac
done
scratch="$(mktemp -d "${TMPDIR:-/tmp}/gorganizer-e2e.XXXXXXXX")"
server_pid=""
cleanup_scratch() {
    if $cleanup; then
        if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
        rm -rf -- "$scratch"
    fi
}
trap cleanup_scratch EXIT
mkdir -p "$scratch"/{home,config,data,state,cache,tmp,runtime,shims,logs,bundles,fixture-state}
chmod 700 "$scratch/runtime"
for name in xdg-open steam notify-send; do
    printf '#!/bin/sh\nprintf "%%s %%s\\n" "$0" "$*" >> %q\nexit 1\n' "$scratch/logs/shims.log" > "$scratch/shims/$name"
    chmod 755 "$scratch/shims/$name"
done
fixture_env=(
    "PATH=$scratch/shims:$PATH"
    "HOME=$scratch/home"
    "XDG_CONFIG_HOME=$scratch/config"
    "XDG_DATA_HOME=$scratch/data"
    "XDG_STATE_HOME=$scratch/state"
    "XDG_CACHE_HOME=$scratch/cache"
    "XDG_RUNTIME_DIR=$scratch/runtime"
    "TMPDIR=$scratch/tmp"
    "SSL_CERT_FILE=$scratch/fixture-ca.pem"
    "GORGANIZER_FIXTURE_TRUST_PEM=$repo/internal/release/testdata/signing/test-k1.pub.pem"
)
if [ -n "${WAYLAND_DISPLAY:-}" ]; then
    [ -n "${XDG_RUNTIME_DIR:-}" ] || { printf 'WAYLAND_DISPLAY needs a caller XDG_RUNTIME_DIR.\n' >&2; exit 1; }
    fixture_env+=("WAYLAND_DISPLAY=$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY")
fi
for name in DISPLAY XAUTHORITY DBUS_SESSION_BUS_ADDRESS QT_QPA_PLATFORM LANG LANGUAGE LC_ALL LC_CTYPE LC_MESSAGES; do
    if [ "${!name+x}" = x ]; then fixture_env+=("$name=${!name}"); fi
done
for version in 0.0.8 0.0.9; do
    bin="$scratch/bin-$version"
    mkdir -p "$bin"
    for name in gorganizerctl gorganizerd; do
        (cd "$repo" && go build -tags releasefixture -ldflags "-X main.version=$version+fixture" -o "$bin/$name" "./cmd/$name")
    done
    make -C "$repo" gui GUI_BUILD_DIR="$scratch/gui-$version" VERSION="$version" > "$scratch/logs/gui-$version.log" 2>&1
    (cd "$repo" && go run ./scripts/releasefixture bundle --version "$version" --bin "$bin" \
        --gui "$scratch/gui-$version/src/gorganizer" --out "$scratch/bundles/$version")
done
(cd "$repo" && go build -o "$scratch/releasefixture" ./scripts/releasefixture)
mode="${E2E_MODE:-normal}"
"$scratch/releasefixture" serve --dir "$scratch/bundles" --listen 127.0.0.1:0 \
    --ca-out "$scratch/fixture-ca.pem" --state-dir "$scratch/fixture-state" --mode "$mode" \
    > "$scratch/logs/server.log" 2>&1 &
server_pid=$!
for attempt in {1..100}; do
    if [ -s "$scratch/fixture-state/listen.addr" ]; then break; fi
    if ! kill -0 "$server_pid" 2>/dev/null; then
        printf 'Fixture server exited; see %s/logs/server.log\n' "$scratch" >&2
        exit 1
    fi
    sleep 0.1
done
[ -s "$scratch/fixture-state/listen.addr" ] || { printf 'Fixture server did not start.\n' >&2; exit 1; }
origin="$(< "$scratch/fixture-state/listen.addr")"
fixture_env+=(
    "GORGANIZER_FIXTURE_ORIGIN=$origin"
    "GORGANIZER_FIXTURE_LATEST_URL=https://$origin/latest"
    "GORGANIZER_FIXTURE_ASSETS_URL=https://$origin/download/"
    "GORGANIZER_FIXTURE_NOTES_URL=https://$origin/notes/"
)
write_launch_wrapper
env -i "${fixture_env[@]}" "$scratch/bin-0.0.8/gorganizerctl" release install \
    --from "$scratch/bundles/0.0.8/gorganizer-0.0.8" > "$scratch/logs/install.log" 2>&1 || {
    printf 'Fixture install failed; see %s/logs/install.log\n' "$scratch" >&2
    exit 1
}
mkdir -p "$scratch/config/gorganizer"
printf '[setup]\ncomplete=true\n' > "$scratch/config/gorganizer/gorganizer.conf"
store="$scratch/data/gorganizer/releases"
printf 'Scratch: %s\nLogs: %s/logs\nRequests: %s/fixture-state/requests.log\nStore: %s\nServer PID: %s\nLaunch or reopen with: %s/launch.sh launch\n' \
    "$scratch" "$scratch" "$scratch" "$store" "$server_pid" "$scratch"
if ! $no_launch; then
    "$scratch/launch.sh" launch > "$scratch/logs/launch.log" 2>&1
fi
