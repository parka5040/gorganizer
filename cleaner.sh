#!/bin/bash
# cleaner.sh — Developer reset of local build files and known old mod folders.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$SCRIPT_DIR/release.json" ]; then
    printf '%s\n' 'This is a prebuilt copy of Gorganizer. Cleaner is only available in a source checkout.' >&2
    exit 1
fi
CTL_BIN="$SCRIPT_DIR/gorganizerctl"
KEEP_MODS=false
ASSUME_YES=false
LOCKED_RUN=false
for arg in "$@"; do
    case "$arg" in
        --cleaner-locked-run) LOCKED_RUN=true ;;
        --keep-mods) KEEP_MODS=true ;;
        --yes|-y) ASSUME_YES=true ;;
        --help|-h)
            printf '%s\n' 'Usage: ./cleaner.sh [--keep-mods] [--yes]'
            exit 0 ;;
        *) printf 'Unknown option: %s\n' "$arg" >&2; exit 2 ;;
    esac
done

if [ ! -x "$CTL_BIN" ]; then
    printf '%s\n' "Gorganizer's maintenance tool is missing, so nothing was removed. Rebuild with ./gorganizer.sh, then run cleaner again." >&2
    exit 1
fi
if ! $LOCKED_RUN || [ "${GORGANIZER_CLEANER_HELD:-}" != 1 ]; then
    exec "$CTL_BIN" uninstall --check --hold-locks -- env GORGANIZER_CLEANER_HELD=1 bash "$SCRIPT_DIR/cleaner.sh" --cleaner-locked-run "$@"
fi

cleaner_validate_path() {
    local path="$1" kind="$2" required="${3:-false}" part
    if [[ "$path" != /* || "$path" == *$'\n'* || "$path" == *$'\r'* ]] ||
        [[ "$path/" == *'/./'* || "$path/" == *'/../'* || "$path/" == *'//'* ]]; then
        printf 'Cannot safely clean: invalid path %s. Nothing has been removed.\n' "$path" >&2
        return 1
    fi
    if [ -e "$path" ] || [ -L "$path" ]; then
        if [ -L "$path" ] || [ "$(stat -c %u -- "$path")" != "$(id -u)" ] ||
            { [ "$kind" = directory ] && [ ! -d "$path" ]; } ||
            { [ "$kind" = file ] && [ ! -f "$path" ]; }; then
            printf 'Cannot safely clean: path is not an owned %s: %s. Nothing has been removed.\n' "$kind" "$path" >&2
            return 1
        fi
    elif $required; then
        printf 'Cannot safely clean: missing %s: %s. Nothing has been removed.\n' "$kind" "$path" >&2
        return 1
    fi
    part="${path%/*}"
    [ -n "$part" ] || part=/
    while [ "$part" != / ]; do
        if { [ -e "$part" ] || [ -L "$part" ]; } && { [ -L "$part" ] || [ ! -d "$part" ]; }; then
            printf 'Cannot safely clean: linked or non-directory ancestor %s. Nothing has been removed.\n' "$part" >&2
            return 1
        fi
        part="${part%/*}"
        [ -n "$part" ] || part=/
    done
}

cleaner_validate_desktop() {
    local path="$1" name="$2" action="$3" line name_found=false exec_found=false
    [ -e "$path" ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        [ "$line" != "Name=$name" ] || name_found=true
        [ "$line" != "Exec=$SCRIPT_DIR/gorganizer.sh $action" ] || exec_found=true
    done < "$path"
    if ! $name_found || ! $exec_found; then
        if "$CTL_BIN" desktop status --checkout "$SCRIPT_DIR" --icon "$data_base/icons/hicolor/256x256/apps/gorganizer.png" >/dev/null 2>&1; then
            return 0
        fi
        printf 'Cannot safely clean: desktop entry does not belong to this checkout: %s. Nothing has been removed.\n' "$path" >&2
        return 1
    fi
}

old_mods=()
if ! $KEEP_MODS; then
    old_list="$("$CTL_BIN" migrate-data --from "$SCRIPT_DIR" --dry-run --list)" || exit 1
    if [ -n "$old_list" ]; then
        mapfile -t old_mods <<< "$old_list"
    fi
fi

extracts=()
temp_base="${TMPDIR:-}"
if [ -n "$temp_base" ]; then
    if ! cleaner_validate_path "$temp_base" directory true; then
        printf 'Cannot safely check the temporary folder. Nothing has been removed.\n' >&2
        exit 1
    fi
    for path in "$temp_base"/gorganizer-extract-"$(id -u)"-*; do
        [ -e "$path" ] || [ -L "$path" ] || continue
        name="${path##*/}"
        [[ "$name" =~ ^gorganizer-extract-$(id -u)-[0-9a-f]{12}$ ]] || continue
        if ! cleaner_validate_path "$path" directory true; then
            printf 'Cannot safely check extraction folder %s. Nothing has been removed.\n' "$path" >&2
            exit 1
        fi
        extracts+=("$path")
    done
fi

config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/gorganizer"
data_base="${XDG_DATA_HOME:-$HOME/.local/share}"
cleaner_check_paths() {
    local path
    cleaner_validate_path "$SCRIPT_DIR" directory true || return 1
    cleaner_validate_path "$config_dir" directory || return 1
    [ -z "$temp_base" ] || cleaner_validate_path "$temp_base" directory true || return 1
    for path in "$SCRIPT_DIR/build" "$SCRIPT_DIR/.build-staging" "$SCRIPT_DIR/CMakeFiles" "$SCRIPT_DIR/.tools"; do
        cleaner_validate_path "$path" directory || return 1
    done
    for path in "$SCRIPT_DIR/gorganizerd" "$CTL_BIN" "$SCRIPT_DIR/.build-fingerprint" \
        "$SCRIPT_DIR/api/proto/gorganizer.pb.go" "$SCRIPT_DIR/api/proto/gorganizer_grpc.pb.go" \
        "$data_base/applications/gorganizer.desktop" "$data_base/applications/gorganizer-nxm.desktop" \
        "$data_base/gorganizer/bin/gorganizer"; do
        cleaner_validate_path "$path" file || return 1
    done
    for path in "${old_mods[@]}"; do
        if [ "${path%/*}" != "$SCRIPT_DIR" ] || [[ "${path##*/}" != *_Mods ]] ||
            ! cleaner_validate_path "$path" directory true; then
            printf 'Cannot safely check old mod folders. Nothing has been removed.\n' >&2
            return 1
        fi
    done
    for path in "${extracts[@]}"; do
        cleaner_validate_path "$path" directory true || return 1
    done
    cleaner_validate_desktop "$data_base/applications/gorganizer.desktop" Gorganizer launch || return 1
    cleaner_validate_desktop "$data_base/applications/gorganizer-nxm.desktop" 'Gorganizer NXM Handler' 'nxm %u' || return 1
}
cleaner_check_paths || exit 1
printf 'Remove build files, settings, %s old mod folders and %s extraction folders?\n' "${#old_mods[@]}" "${#extracts[@]}"
if ! $ASSUME_YES; then
    if [ ! -t 0 ]; then
        printf 'Use --yes in a non-interactive shell. Nothing has been removed.\n' >&2
        exit 1
    fi
    read -r -p "Type 'yes' to proceed: " reply || reply=""
    if [ "$reply" != yes ]; then
        printf 'Nothing has been removed.\n'
        exit 1
    fi
fi
cleaner_check_paths || exit 1
"$CTL_BIN" desktop unregister --checkout "$SCRIPT_DIR" || exit 1

rm -rf -- "$SCRIPT_DIR/build" "$SCRIPT_DIR/.build-staging" "$SCRIPT_DIR/CMakeFiles" "$SCRIPT_DIR/.tools"
rm -f -- "$SCRIPT_DIR/gorganizerd" "$SCRIPT_DIR/gorganizerctl" "$SCRIPT_DIR/.build-fingerprint" \
    "$SCRIPT_DIR/api/proto/gorganizer.pb.go" "$SCRIPT_DIR/api/proto/gorganizer_grpc.pb.go"
for path in "${old_mods[@]}" "${extracts[@]}"; do
    rm -rf -- "$path"
done
rm -rf -- "$config_dir"
printf 'Local cleanup complete. Run ./gorganizer.sh to rebuild.\n'
