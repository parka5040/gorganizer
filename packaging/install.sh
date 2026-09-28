#!/bin/sh
set -eu

fail() { printf '%s\n' "$*" >&2; exit 1; }

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fL --output "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
else
    fail 'Install curl or wget to download Gorganizer, then try again.'
fi
if command -v sha256sum >/dev/null 2>&1; then
    checksum() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
    checksum() { shasum -a 256 "$1"; }
else
    fail 'Install sha256sum or shasum to verify the download, then try again.'
fi
command -v tar >/dev/null 2>&1 || fail 'Install tar to unpack Gorganizer, then try again.'

base=${GORGANIZER_RELEASE_BASE_URL:-https://github.com/parka5040/gorganizer/releases/download}
latest=${GORGANIZER_RELEASE_LATEST_URL:-https://api.github.com/repos/parka5040/gorganizer/releases/latest}
work_base=${TMPDIR:-${XDG_RUNTIME_DIR:-"$HOME/.cache"}}
mkdir -p "$work_base"
work=$(mktemp -d "$work_base/gorganizer-install.XXXXXXXX") || fail 'Could not make a temporary folder.'
trap 'rm -rf -- "$work"' EXIT HUP INT TERM

if [ "$#" -gt 1 ]; then fail 'Usage: install.sh [vX.Y.Z]'; fi
tag=${1:-}
if [ -z "$tag" ]; then
    fetch "$latest" "$work/latest.json" || fail 'Could not check for the latest release.'
    [ "$(wc -c < "$work/latest.json")" -le 1048576 ] || fail 'Latest release information is too large.'
    tag=$(tr -d '\n' < "$work/latest.json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p')
fi
case "$tag" in v*) ;; *) fail 'The release tag is not valid.' ;; esac
printf '%s\n' "$tag" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail 'The release tag is not valid.'
version=${tag#v}
archive="gorganizer-$version-linux-x86_64.tar.gz"
fetch "$base/$tag/SHA256SUMS" "$work/SHA256SUMS" || fail 'Could not download the release checksums.'
[ "$(wc -c < "$work/SHA256SUMS")" -le 65536 ] || fail 'Release checksums are too large.'
expected= count=0
while IFS= read -r line || [ -n "$line" ]; do
    hash=${line%%  *}
    name=${line#"$hash  "}
    [ "$name" = "$archive" ] || [ "$name" = "*$archive" ] || continue
    case "$hash" in *[!0-9a-fA-F]*|'') fail 'Release checksum is not valid.' ;; esac
    [ "${#hash}" -eq 64 ] || fail 'Release checksum is not valid.'
    expected=$hash
    count=$((count + 1))
done < "$work/SHA256SUMS"
[ "$count" -eq 1 ] || fail 'The release must have exactly one matching checksum.'
fetch "$base/$tag/$archive" "$work/$archive" || fail 'Could not download Gorganizer.'
actual=$(checksum "$work/$archive") || fail 'Could not check the download.'
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail 'The download did not match its checksum. Nothing was installed.'

tar -tzf "$work/$archive" > "$work/members" || fail 'The release archive is not complete.'
while IFS= read -r member || [ -n "$member" ]; do
    case "$member" in
        /*|../*|*/../*|*/..|..|*\\*) fail 'The release archive has an unsafe path.' ;;
        "gorganizer-$version"|"gorganizer-$version/"|"gorganizer-$version/"*) ;;
        *) fail 'The release archive contains files outside Gorganizer.' ;;
    esac
done < "$work/members"
tar --no-same-owner --no-same-permissions -xzf "$work/$archive" -C "$work" || fail 'Could not unpack the release.'
folder="$work/gorganizer-$version"
[ -x "$folder/bin/gorganizerctl" ] || fail 'The release is missing its maintenance tool.'
"$folder/bin/gorganizerctl" release install --from "$folder" || fail 'Could not install the verified release.'
