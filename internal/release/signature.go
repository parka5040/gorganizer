package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed release-signing.pub.pem
var productionKeys []byte

type TrustSet map[string]ed25519.PublicKey

// SignedStatement builds the canonical release statement for a tag and checksum file.
func SignedStatement(tag string, sums []byte) []byte {
	digest := sha256.Sum256(sums)
	return []byte(fmt.Sprintf("gorganizer-release-signature-v1\nrepository=parka5040/gorganizer\ntag=%s\nplatform=linux-x86_64\nsha256sums-sha256=%x\n", tag, digest))
}

// ParseTrust parses a bundle of unique PKIX Ed25519 public keys.
func ParseTrust(bundle []byte) (TrustSet, error) {
	trust := make(TrustSet)
	for len(bundle) > 0 {
		bundle = bytes.TrimSpace(bundle)
		if len(bundle) == 0 {
			break
		}
		if !bytes.HasPrefix(bundle, []byte("-----BEGIN PUBLIC KEY-----")) {
			return nil, fmt.Errorf("invalid release signing trust bundle")
		}
		block, rest := pem.Decode(bundle)
		if block == nil || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("invalid release signing public key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid release signing public key: %w", err)
		}
		key, ok := parsed.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("release signing key is not Ed25519")
		}
		digest := sha256.Sum256(key)
		id := hex.EncodeToString(digest[:8])
		if _, exists := trust[id]; exists {
			return nil, fmt.Errorf("duplicate release signing key %s", id)
		}
		trust[id] = append(ed25519.PublicKey(nil), key...)
		bundle = rest
	}
	if len(trust) == 0 {
		return nil, fmt.Errorf("no release signing keys")
	}
	return trust, nil
}

// ProductionTrust returns the release keys embedded in this build.
func ProductionTrust() (TrustSet, error) {
	if len(bytes.TrimSpace(productionKeys)) == 0 {
		return nil, fmt.Errorf("no release signing keys are built into this Gorganizer")
	}
	return ParseTrust(productionKeys)
}

// VerifySums verifies a strictly formatted signature file against the trusted keys.
func VerifySums(trust TrustSet, tag string, sums, sig []byte) error {
	if len(sig) == 0 || len(sig) > 2048 || sig[len(sig)-1] != '\n' {
		return fmt.Errorf("invalid release signature file")
	}
	lines := bytes.Split(sig[:len(sig)-1], []byte{'\n'})
	if len(lines) < 1 || len(lines) > 4 {
		return fmt.Errorf("invalid release signature file")
	}
	seen := make(map[string]bool)
	verified := false
	statement := SignedStatement(tag, sums)
	for _, line := range lines {
		if bytes.ContainsRune(line, '\r') {
			return fmt.Errorf("invalid release signature file")
		}
		fields := strings.Split(string(line), " ")
		if len(fields) != 3 || fields[0] != "gorganizer-sig-v1" || len(fields[1]) != 16 || strings.Trim(fields[1], "0123456789abcdef") != "" || seen[fields[1]] {
			return fmt.Errorf("invalid release signature file")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(fields[2])
		if err != nil || len(decoded) != ed25519.SignatureSize {
			return fmt.Errorf("invalid release signature file")
		}
		seen[fields[1]] = true
		if key, ok := trust[fields[1]]; ok && ed25519.Verify(key, statement, decoded) {
			verified = true
		}
	}
	if !verified {
		return fmt.Errorf("release signature verification failed")
	}
	return nil
}

// DescribeConfig prints the local release trust and origin policy.
func DescribeConfig(w io.Writer) error {
	trust, err := ProductionTrust()
	if err != nil && len(bytes.TrimSpace(productionKeys)) != 0 {
		return err
	}
	keys := make([]string, 0, len(trust))
	for id := range trust {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		keys = append(keys, "none")
	}
	for _, id := range keys {
		if _, err := fmt.Fprintf(w, "trust %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "latest-url %s\nassets-url %s\nlatest-origin api.github.com\nassets-origin github.com *.githubusercontent.com\nsignature required\n", latestURL, repositoryURL)
	return err
}
