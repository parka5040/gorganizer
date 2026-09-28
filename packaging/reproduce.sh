#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 2 ] || { printf 'Usage: reproduce.sh <src-dir> <out-dir>\n' >&2; exit 2; }
src="$(realpath "$1")"
mkdir -p "$2"
out="$(realpath "$2")"
base="$(mktemp -d "${TMPDIR:?}/gorganizer-repro.XXXXXXXX")"
trap 'rm -rf -- "$base"' EXIT
"$src/packaging/build-release.sh" "$src" "$base/a"
"$src/packaging/build-release.sh" "$src" "$base/b"
version="$(tr -d '\n' < "$src/VERSION")"
name="gorganizer-$version-linux-x86_64.tar.gz"
if ! cmp -s "$base/a/$name" "$base/b/$name"; then
    printf 'The two release tarballs differ:\n' >&2
    mkdir -p "$base/first" "$base/second"
    tar -xzf "$base/a/$name" -C "$base/first"
    tar -xzf "$base/b/$name" -C "$base/second"
    diff -rq "$base/first" "$base/second" >&2 || true
    while IFS= read -r -d '' file; do
        relative="${file#"$base/first"/}"
        if [ -f "$base/second/$relative" ] && ! cmp -s "$file" "$base/second/$relative"; then
            printf 'Different bytes: %s\n' "$relative" >&2
        fi
    done < <(find "$base/first" -type f -print0 | sort -z)
    exit 1
fi
cp "$base/a/$name" "$base/a/SHA256SUMS" "$out/"
(cd "$out" && sha256sum -c SHA256SUMS)
if [ -n "${RELEASE_OWNER:-}" ]; then chown "$RELEASE_OWNER" "$out" "$out/$name" "$out/SHA256SUMS"; fi
printf 'Reproduced %s twice with identical SHA-256.\n' "$name"
