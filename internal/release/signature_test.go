//go:build !releasefixture

package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSigningKey derives a test-only signing key from a public seed phrase.
func testSigningKey(n int) (ed25519.PrivateKey, string) {
	seed := sha256.Sum256([]byte(fmt.Sprintf("gorganizer test-only release signing key %d", n)))
	private := ed25519.NewKeyFromSeed(seed[:])
	digest := sha256.Sum256(private.Public().(ed25519.PublicKey))
	return private, hex.EncodeToString(digest[:8])
}

// testSignature produces one canonical test signature line.
func testSignature(n int, tag string, sums []byte) []byte {
	private, id := testSigningKey(n)
	return []byte(fmt.Sprintf("gorganizer-sig-v1 %s %s\n", id, base64.StdEncoding.EncodeToString(ed25519.Sign(private, SignedStatement(tag, sums)))))
}

// testTrust loads both test-only public keys without modifying production trust.
func testTrust(t *testing.T) *TrustSet {
	t.Helper()
	var bundle []byte
	for _, name := range []string{"test-k1.pub.pem", "test-k2.pub.pem"} {
		data, err := os.ReadFile(filepath.Join("testdata", "signing", name))
		if err != nil {
			t.Fatal(err)
		}
		bundle = append(bundle, data...)
	}
	trust, err := ParseTrust(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return &trust
}

// TestSigningGoldenVectors checks statement and signature bytes against shared OpenSSL vectors.
func TestSigningGoldenVectors(t *testing.T) {
	sums, err := os.ReadFile("testdata/signing/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	statement, err := os.ReadFile("testdata/signing/statement.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(SignedStatement("v0.0.9", sums), statement) {
		t.Fatal("statement differs from signing vector")
	}
	trust := testTrust(t)
	for _, tc := range []struct {
		name string
		key  int
	}{{"SHA256SUMS.k1.sig", 1}, {"SHA256SUMS.k2.sig", 2}} {
		golden, err := os.ReadFile(filepath.Join("testdata", "signing", tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(testSignature(tc.key, "v0.0.9", sums), golden) {
			t.Fatalf("signature differs from %s", tc.name)
		}
		if err := VerifySums(*trust, "v0.0.9", sums, golden); err != nil {
			t.Fatal(err)
		}
	}
	both, err := os.ReadFile("testdata/signing/SHA256SUMS.k1k2.sig")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(both, append(testSignature(1, "v0.0.9", sums), testSignature(2, "v0.0.9", sums)...)) {
		t.Fatal("two-key vector differs")
	}
}

// TestParseTrustRejectsInvalidPEM checks strict trust parsing and duplicate detection.
func TestParseTrustRejectsInvalidPEM(t *testing.T) {
	key := testTrust(t)
	if len(*key) != 2 {
		t.Fatal("missing test keys")
	}
	valid, err := os.ReadFile("testdata/signing/test-k1.pub.pem")
	if err != nil {
		t.Fatal(err)
	}
	private, _ := testSigningKey(1)
	der, err := x509.MarshalPKIXPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][]byte{nil, []byte("junk"), append([]byte("junk"), valid...), append(append([]byte{}, valid...), []byte("junk")...), append(append([]byte{}, valid...), valid...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("bad")})} {
		if _, err := ParseTrust(tc); err == nil {
			t.Fatalf("accepted invalid trust %q", tc)
		}
	}
	saved := productionKeys
	productionKeys = nil
	_, emptyErr := ProductionTrust()
	productionKeys = saved
	if emptyErr == nil {
		t.Fatal("empty production trust was accepted")
	}
	production, err := ProductionTrust()
	if err != nil || len(production) != 1 || production["b107acc071785a65"] == nil {
		t.Fatalf("production trust is not exactly the release key: %v %v", production, err)
	}
}

// TestVerifySumsRejectsMalformedSignatures checks syntax, bounds, unknown keys and tampering.
func TestVerifySumsRejectsMalformedSignatures(t *testing.T) {
	trust := testTrust(t)
	sums := []byte("sums\n")
	valid := testSignature(1, "v1.2.3", sums)
	unknown := TrustSet{}
	for _, tc := range [][]byte{nil, valid[:len(valid)-1], append(append([]byte{}, valid...), '\n'), append(append([]byte{}, valid...), valid...), bytes.Repeat(valid, 5), bytes.Repeat([]byte("x"), 2049), bytes.Replace(valid, []byte("-sig-v1 "), []byte("-sig-v2 "), 1), bytes.Replace(valid, []byte(" "), []byte("  "), 1), bytes.Replace(valid, []byte("\n"), []byte("\r\n"), 1), bytes.Replace(valid, []byte("c893dee9499f4b4b"), []byte("C893DEE9499F4B4B"), 1)} {
		if err := VerifySums(*trust, "v1.2.3", sums, tc); err == nil {
			t.Fatalf("accepted invalid signature %q", tc)
		}
	}
	if VerifySums(*trust, "v1.2.4", sums, valid) == nil || VerifySums(*trust, "v1.2.3", []byte("tampered"), valid) == nil || VerifySums(unknown, "v1.2.3", sums, valid) == nil {
		t.Fatal("accepted an untrusted statement")
	}
	if err := VerifySums(*trust, "v1.2.3", sums, append(testSignature(2, "v1.2.3", []byte("wrong")), valid...)); err != nil {
		t.Fatalf("failed valid second signature: %v", err)
	}
}

// TestDescribeConfigGolden checks deterministic local configuration with empty production trust.
func TestDescribeConfigGolden(t *testing.T) {
	var out bytes.Buffer
	if err := DescribeConfig(&out); err != nil {
		t.Fatal(err)
	}
	want := "trust b107acc071785a65\nlatest-url https://api.github.com/repos/parka5040/gorganizer/releases/latest\nassets-url https://github.com/parka5040/gorganizer/releases/download/\nlatest-origin api.github.com\nassets-origin github.com *.githubusercontent.com\nsignature required\n"
	if out.String() != want {
		t.Fatalf("config: %q", out.String())
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("private key in output")
	}
}
