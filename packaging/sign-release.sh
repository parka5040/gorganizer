#!/usr/bin/env bash
set -euo pipefail

fail() {
    printf '%s\n' "sign-release.sh: $1" >&2
    exit 1
}

for tool in openssl sha256sum base64; do
    command -v "$tool" >/dev/null 2>&1 || fail "required command not found: $tool"
done

tag=""
sums=""
out=""
print_statement=false
keys=()

while [ "$#" -gt 0 ]; do
    case "$1" in
        --tag)
            [ "$#" -ge 2 ] || fail "--tag requires a value"
            tag="$2"
            shift 2
            ;;
        --sums)
            [ "$#" -ge 2 ] || fail "--sums requires a path"
            sums="$2"
            shift 2
            ;;
        --out)
            [ "$#" -ge 2 ] || fail "--out requires a path"
            out="$2"
            shift 2
            ;;
        --print-statement)
            print_statement=true
            shift
            ;;
        --*)
            fail "unknown option: $1"
            ;;
        *)
            keys+=("$1")
            shift
            ;;
    esac
done

[[ "$tag" =~ ^v[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$ ]] || fail "tag must be vX.Y.Z"
[ -n "$sums" ] || fail "--sums is required"
[ -f "$sums" ] && [ ! -L "$sums" ] || fail "--sums must name a regular file"
sums_size="$(wc -c < "$sums")" || fail "could not read --sums"
[ "$sums_size" -le 65536 ] || fail "--sums must be at most 65536 bytes"

if $print_statement; then
    [ -z "$out" ] || fail "--print-statement cannot be used with --out"
    [ "${#keys[@]}" -eq 0 ] || fail "--print-statement does not accept key files"
else
    [ -n "$out" ] || fail "--out is required"
    [ "${#keys[@]}" -ge 1 ] && [ "${#keys[@]}" -le 4 ] || fail "supply between one and four key files"
fi

umask 077
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/gorganizer-release-signing.XXXXXX")" || fail "could not create temporary directory"
out_tmp=""
trap 'rm -rf -- "$tmpdir"; [ -z "$out_tmp" ] || rm -f -- "$out_tmp"' EXIT

sums_hash="$(LC_ALL=C sha256sum -- "$sums")" || fail "could not hash --sums"
sums_hash="${sums_hash%% *}"
[[ "$sums_hash" =~ ^[0-9a-f]{64}$ ]] || fail "could not hash --sums"
statement="$(mktemp "$tmpdir/statement.XXXXXX")" || fail "could not create statement"
printf '%s\n' \
    'gorganizer-release-signature-v1' \
    'repository=parka5040/gorganizer' \
    "tag=$tag" \
    'platform=linux-x86_64' \
    "sha256sums-sha256=$sums_hash" > "$statement" || fail "could not write statement"

if $print_statement; then
    cat -- "$statement"
    exit 0
fi

out_dir="$(dirname -- "$out")"
out_name="$(basename -- "$out")"
[ -d "$out_dir" ] || fail "output directory does not exist"
[ ! -d "$out" ] || fail "--out must name a file"
out_tmp="$(mktemp "$out_dir/.${out_name}.XXXXXX")" || fail "could not create output file"

declare -A seen_ids=()
for index in "${!keys[@]}"; do
    key="${keys[$index]}"
    [ -f "$key" ] && [ ! -L "$key" ] || fail "key file must be a regular file"
    public_der="$tmpdir/public-$index.der"
    signature="$tmpdir/signature-$index.bin"
    openssl pkey -in "$key" -pubout -outform DER > "$public_der" 2>/dev/null || fail "could not read signing key"
    der_size="$(wc -c < "$public_der")" || fail "could not read signing key"
    [ "$der_size" -eq 44 ] || fail "signing key must be Ed25519"
    der_prefix="$(od -An -tx1 -N 12 "$public_der" | tr -d '[:space:]')"
    [ "$der_prefix" = 302a300506032b6570032100 ] || fail "signing key must be Ed25519"
    keyid="$(dd if="$public_der" bs=1 skip=12 count=32 status=none | sha256sum)" || fail "could not identify signing key"
    keyid="${keyid%% *}"
    keyid="${keyid:0:16}"
    [ -z "${seen_ids[$keyid]+x}" ] || fail "duplicate signing key id: $keyid"
    seen_ids["$keyid"]=1
    openssl pkeyutl -sign -inkey "$key" -rawin -in "$statement" -out "$signature" 2>/dev/null || fail "could not sign release statement"
    [ "$(wc -c < "$signature")" -eq 64 ] || fail "signing key must be Ed25519"
    signature_b64="$(base64 -w0 < "$signature")" || fail "could not encode signature"
    printf 'gorganizer-sig-v1 %s %s\n' "$keyid" "$signature_b64" >> "$out_tmp" || fail "could not write output"
done

mv -f -- "$out_tmp" "$out" || fail "could not publish output"
out_tmp=""
