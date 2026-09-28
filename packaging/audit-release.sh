#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

[ "$#" -eq 1 ] && [ -d "$1" ] || { printf 'Usage: audit-release.sh <bundle-dir>\n' >&2; exit 2; }
bundle="$(realpath "$1")"
failures=0
count=0
bad() { printf 'FAIL: %s\n' "$*" >&2; failures=$((failures + 1)); }

# Ubuntu 22.04 runtime ABI plus Qt 6.11's documented Linux/XCB/Wayland dependencies.
# ICU, Qt, gRPC, protobuf, abseil and OpenSSL are deliberately NOT on this list.
system_lib='^(ld-linux-x86-64|lib(c|m|pthread|dl|rt|resolv|util|anl|stdc\+\+|gcc_s|GL|EGL|GLX|OpenGL|GLdispatch|vulkan|X11|X11-xcb|Xau|Xdmcp|Xext|Xrender|Xfixes|Xcursor|Xinerama|Xi|Xrandr|Xss|Xtst|Xcomposite|Xdamage|xcb(-[A-Za-z0-9_-]+)?|wayland-client|wayland-cursor|wayland-egl|xkbcommon(-x11)?|fontconfig|freetype|harfbuzz|glib-2\.0|gthread-2\.0|gobject-2\.0|gio-2\.0|gmodule-2\.0|dbus-1|ssl|crypto|z|zstd|brotlidec|brotlicommon|pcre2-16|double-conversion|drm|gbm|udev|nss3|asound|pulse|SM|ICE|expat|png16|harfbuzz|bz2|uuid|selinux|mount|ffi|systemd|gcrypt|gpg-error|cap|lzma|atomic)\.so)(\..*)?$'

while IFS= read -r -d '' path; do
    relative="${path#"$bundle"/}"
    if LC_ALL=C strings -a "$path" | grep -E '/opt/(qt|grpc|go)(/|$)|/home/|/root/' | grep -vF '/home/qt/work/' | grep -q .; then
        bad "$relative contains a forbidden build or home path"
    fi
    if [ -n "${RELEASE_BUILD_DIR:-}" ] && LC_ALL=C strings -a "$path" | grep -F -- "$RELEASE_BUILD_DIR" > /dev/null; then
        bad "$relative contains the build directory"
    fi
    if ! readelf -h "$path" >/dev/null 2>&1; then
        continue
    fi
    count=$((count + 1))
    dynamic="$(readelf -d "$path")"
    if printf '%s\n' "$dynamic" | grep -q '(RPATH)'; then
        bad "$relative has RPATH"
    fi
    if [[ "$relative" == bin/gorganizerd || "$relative" == bin/gorganizerctl ]]; then
        if readelf -l "$path" | grep -q INTERP || [ -n "$(release_needed "$path")" ]; then
            bad "$relative is not static"
        fi
        if printf '%s\n' "$dynamic" | grep -q '(RUNPATH)'; then
            bad "$relative has RUNPATH"
        fi
        continue
    fi
    case "$relative" in
        bin/gorganizer-gui) expected='$ORIGIN/../lib' ;;
        lib/*) expected='$ORIGIN' ;;
        plugins/*/*) expected='$ORIGIN/../../lib' ;;
        *) bad "unexpected ELF: $relative"; continue ;;
    esac
    if [[ "$relative" == lib/* && ! "$relative" =~ ^lib/lib(Qt6|icu)[A-Za-z0-9_.+-]*\.so([.0-9]*)?$ ]]; then
        bad "non-Qt/ICU library bundled: $relative"
    fi
    actual="$(printf '%s\n' "$dynamic" | sed -n 's/.*(RUNPATH).*\[\([^]]*\)\].*/\1/p')"
    if [ "$actual" != "$expected" ]; then
        bad "$relative RUNPATH is '$actual' (expected '$expected')"
    fi
    while IFS= read -r name; do
        [ -n "$name" ] || continue
        if [ -f "$bundle/lib/$name" ] || [[ "$name" =~ $system_lib ]]; then
            continue
        fi
        bad "$relative needs unresolved $name"
    done < <(release_needed "$path")
    while IFS= read -r version; do
        number="${version#GLIBC_2.}"
        if [ "$number" -gt 35 ]; then
            bad "$relative requires $version (newer than Ubuntu 22.04)"
        fi
    done < <(readelf --version-info "$path" | grep -oE 'GLIBC_2\.[0-9]+' | sort -u || true)
done < <(LC_ALL=C find "$bundle" -type f -print0 | sort -z)

if [ -f "$bundle/MANIFEST.sha256" ]; then
    if ! (cd "$bundle" && sha256sum -c MANIFEST.sha256 >/dev/null); then
        bad 'MANIFEST.sha256 does not match the bundle'
    fi
fi
printf 'Audited %s ELF files; %s issue(s).\n' "$count" "$failures"
[ "$failures" -eq 0 ]
