#!/usr/bin/env bash
set -euo pipefail
umask 077

fail() {
    printf '%s\n' "release-keygen.sh: $1" >&2
    exit 1
}

[ "$#" -eq 1 ] || fail "usage: release-keygen.sh DIR"
command -v openssl >/dev/null 2>&1 || fail "required command not found: openssl"

dir="$1"
if ! mkdir -m 700 -- "$dir"; then
    fail "could not create key directory"
fi

key="$dir/signing-key.pem"
public=""
cleanup() {
    [ -z "$public" ] || rm -f -- "$public"
    rm -f -- "$key"
    rmdir -- "$dir" 2>/dev/null || true
}

if ! openssl genpkey -algorithm ed25519 | ( set -C; cat > "$key" ); then
    cleanup
    fail "could not generate release signing key"
fi
if ! chmod 600 -- "$key"; then
    cleanup
    fail "could not secure release signing key"
fi
public="$(mktemp "${TMPDIR:-/tmp}/gorganizer-release-public-key.XXXXXX")" || {
    cleanup
    fail "could not prepare release public key"
}
if ! openssl pkey -in "$key" -pubout > "$public" 2>/dev/null; then
    cleanup
    fail "could not derive release public key"
fi

cat -- "$public"
rm -f -- "$public"
public=""
printf '%s\n' \
    'Next: commit the public PEM as internal/release/release-signing.pub.pem.' \
    "Next: run gh secret set RELEASE_SIGNING_KEY --env release < $key." \
    'Keep an offline backup of the private key.' \
    'Never commit or share the private key.' >&2
