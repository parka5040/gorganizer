#!/usr/bin/env bash
set -euo pipefail

fail() {
    printf '%s\n' "verify-release-sig.sh: $1" >&2
    exit 1
}

for tool in openssl sha256sum base64; do
    command -v "$tool" >/dev/null 2>&1 || fail "required command not found: $tool"
done

tag=""
sums=""
sig=""
trust=""
require_all=false

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
        --sig)
            [ "$#" -ge 2 ] || fail "--sig requires a path"
            sig="$2"
            shift 2
            ;;
        --trust)
            [ "$#" -ge 2 ] || fail "--trust requires a path"
            trust="$2"
            shift 2
            ;;
        --require-all)
            require_all=true
            shift
            ;;
        *)
            fail "unknown option: $1"
            ;;
    esac
done

[[ "$tag" =~ ^v[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$ ]] || fail "tag must be vX.Y.Z"
[ -f "$sums" ] && [ ! -L "$sums" ] || fail "--sums must name a regular file"
sums_size="$(wc -c < "$sums")" || fail "could not read --sums"
[ "$sums_size" -le 65536 ] || fail "--sums must be at most 65536 bytes"
[ -f "$sig" ] && [ ! -L "$sig" ] || fail "--sig must name a regular file"
[ -f "$trust" ] && [ ! -L "$trust" ] || fail "--trust must name a regular file"
[ -s "$trust" ] || fail "no release signing keys are configured"

umask 077
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/gorganizer-release-verification.XXXXXX")" || fail "could not create temporary directory"
trap 'rm -rf -- "$tmpdir"' EXIT

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

declare -A trusted_keys=()
in_key=false
current_key=""
key_count=0
outside_content=false
while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
        '-----BEGIN PUBLIC KEY-----')
            $in_key && fail "invalid release signing trust bundle"
            current_key="$(mktemp "$tmpdir/public-key.XXXXXX")" || fail "could not read trust bundle"
            printf '%s\n' "$line" > "$current_key" || fail "could not read trust bundle"
            in_key=true
            ;;
        '-----END PUBLIC KEY-----')
            $in_key || fail "invalid release signing trust bundle"
            printf '%s\n' "$line" >> "$current_key" || fail "could not read trust bundle"
            public_der="$(mktemp "$tmpdir/public-key.der.XXXXXX")" || fail "could not read trust bundle"
            openssl pkey -pubin -in "$current_key" -pubout -outform DER > "$public_der" 2>/dev/null || fail "invalid release signing trust bundle"
            der_size="$(wc -c < "$public_der")" || fail "invalid release signing trust bundle"
            [ "$der_size" -eq 44 ] || fail "release signing key must be Ed25519"
            der_prefix="$(od -An -tx1 -N 12 "$public_der" | tr -d '[:space:]')"
            [ "$der_prefix" = 302a300506032b6570032100 ] || fail "release signing key must be Ed25519"
            keyid="$(dd if="$public_der" bs=1 skip=12 count=32 status=none | sha256sum)" || fail "could not identify release signing key"
            keyid="${keyid%% *}"
            keyid="${keyid:0:16}"
            [ -z "${trusted_keys[$keyid]+x}" ] || fail "duplicate release signing key id: $keyid"
            trusted_keys["$keyid"]="$current_key"
            key_count=$((key_count + 1))
            in_key=false
            current_key=""
            ;;
        '')
            $in_key && printf '\n' >> "$current_key"
            ;;
        *)
            if $in_key; then
                printf '%s\n' "$line" >> "$current_key" || fail "could not read trust bundle"
            else
                outside_content=true
            fi
            ;;
    esac
done < "$trust"
$in_key && fail "invalid release signing trust bundle"
[ "$key_count" -gt 0 ] || fail "no release signing keys are configured"
$outside_content && fail "invalid release signing trust bundle"

sig_size="$(wc -c < "$sig")" || fail "could not read --sig"
[ "$sig_size" -gt 0 ] && [ "$sig_size" -le 2048 ] || fail "invalid release signature file"
sig_last_byte="$(od -An -tx1 -j "$((sig_size - 1))" -N 1 "$sig" | tr -d '[:space:]')"
[ "$sig_last_byte" = 0a ] || fail "invalid release signature file"
LC_ALL=C tr -d '\r\000' < "$sig" | cmp -s - "$sig" || fail "invalid release signature file"
mapfile -t sig_lines < "$sig"
[ "${#sig_lines[@]}" -ge 1 ] && [ "${#sig_lines[@]}" -le 4 ] || fail "invalid release signature file"

declare -A seen_signatures=()
signature_ids=()
signature_files=()
for index in "${!sig_lines[@]}"; do
    line="${sig_lines[$index]}"
    [[ "$line" =~ ^gorganizer-sig-v1\ ([0-9a-f]{16})\ ([A-Za-z0-9+/]{86}==)$ ]] || fail "invalid release signature file"
    keyid="${BASH_REMATCH[1]}"
    signature_b64="${BASH_REMATCH[2]}"
    [ -z "${seen_signatures[$keyid]+x}" ] || fail "duplicate release signature key id: $keyid"
    seen_signatures["$keyid"]=1
    signature_file="$tmpdir/signature-$index.bin"
    printf '%s' "$signature_b64" | base64 -d > "$signature_file" 2>/dev/null || fail "invalid release signature file"
    [ "$(wc -c < "$signature_file")" -eq 64 ] || fail "invalid release signature file"
    [ "$(base64 -w0 < "$signature_file")" = "$signature_b64" ] || fail "invalid release signature file"
    signature_ids+=("$keyid")
    signature_files+=("$signature_file")
done

verified=false
for index in "${!signature_ids[@]}"; do
    keyid="${signature_ids[$index]}"
    signature_file="${signature_files[$index]}"
    if [ -z "${trusted_keys[$keyid]+x}" ]; then
        $require_all && fail "release signature key is not trusted: $keyid"
        continue
    fi
    if ! openssl pkeyutl -verify -pubin -inkey "${trusted_keys[$keyid]}" -rawin -in "$statement" -sigfile "$signature_file" >/dev/null 2>&1; then
        fail "release signature verification failed"
    fi
    verified=true
done

$verified || fail "no trusted release signature verified"
