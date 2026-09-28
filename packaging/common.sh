#!/usr/bin/env bash
set -euo pipefail

release_tar() {
    local tree="$1" output="$2" epoch="$3"
    tar --sort=name --owner=0 --group=0 --numeric-owner \
        --mtime="@$epoch" \
        --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime \
        -C "$(dirname "$tree")" -cf - "$(basename "$tree")" | gzip -n -9 > "$output"
}

release_fetch() {
    local url="$1" hash="$2" destination="$3"
    curl -fL --retry 3 --output "$destination" "$url"
    printf '%s  %s\n' "$hash" "$destination" | sha256sum -c -
}

release_needed() {
    readelf -d "$1" | sed -n 's/.*(NEEDED).*\[\([^]]*\)\].*/\1/p'
}
