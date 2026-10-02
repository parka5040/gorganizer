package packagingtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// signingFixture returns a release signing test vector path.
func signingFixture(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "../..", "internal", "release", "testdata", "signing", name)
}

// writeSigningKey derives and writes a test-only PKCS#8 Ed25519 private key.
func writeSigningKey(t *testing.T, phrase string) string {
	t.Helper()
	seed := sha256.Sum256([]byte(phrase))
	key := ed25519.NewKeyFromSeed(seed[:])
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signing-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSigningTrust writes selected public test-vector keys to a PEM bundle.
func writeSigningTrust(t *testing.T, keys ...string) string {
	t.Helper()
	var bundle []byte
	for _, key := range keys {
		data, err := os.ReadFile(signingFixture(t, key))
		if err != nil {
			t.Fatal(err)
		}
		bundle = append(bundle, data...)
	}
	path := filepath.Join(t.TempDir(), "trust.pem")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireOpenSSL skips signing tests when OpenSSL is unavailable.
func requireOpenSSL(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not on PATH")
	}
}

// runKeygen captures the separate output streams of the key generator.
func runKeygen(t *testing.T, dir string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", packagingScript(t, "release-keygen.sh"), dir)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// TestSignReleasePrintsGoldenStatement checks statement construction is byte-exact.
func TestSignReleasePrintsGoldenStatement(t *testing.T) {
	requireOpenSSL(t)
	out, err := runScript(t, packagingScript(t, "sign-release.sh"), "--print-statement", "--tag", "v0.0.9", "--sums", signingFixture(t, "SHA256SUMS"))
	if err != nil {
		t.Fatalf("print statement: %v: %s", err, out)
	}
	want, err := os.ReadFile(signingFixture(t, "statement.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("statement = %q, want %q", out, want)
	}
}

// TestSignReleaseMatchesGoldenSignatures checks OpenSSL signatures match the test vectors.
func TestSignReleaseMatchesGoldenSignatures(t *testing.T) {
	requireOpenSSL(t)
	key1 := writeSigningKey(t, "gorganizer test-only release signing key 1")
	key2 := writeSigningKey(t, "gorganizer test-only release signing key 2")
	for _, tc := range []struct {
		name, want string
		keys       []string
	}{
		{name: "K1", want: "SHA256SUMS.k1.sig", keys: []string{key1}},
		{name: "K1 then K2", want: "SHA256SUMS.k1k2.sig", keys: []string{key1, key2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outPath := filepath.Join(t.TempDir(), "SHA256SUMS.sig")
			args := []string{"--tag", "v0.0.9", "--sums", signingFixture(t, "SHA256SUMS"), "--out", outPath}
			args = append(args, tc.keys...)
			out, err := runScript(t, packagingScript(t, "sign-release.sh"), args...)
			if err != nil {
				t.Fatalf("sign: %v: %s", err, out)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(signingFixture(t, tc.want))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("signature = %q, want %q", got, want)
			}
		})
	}
}

// TestVerifyReleaseSignatureChecksGoldenAndRejectedInputs checks strict verification behavior.
func TestVerifyReleaseSignatureChecksGoldenAndRejectedInputs(t *testing.T) {
	requireOpenSSL(t)
	sums := signingFixture(t, "SHA256SUMS")
	verify := func(t *testing.T, tag, sig, trust string, requireAll bool) (string, error) {
		t.Helper()
		args := []string{"--tag", tag, "--sums", sums, "--sig", sig, "--trust", trust}
		if requireAll {
			args = append(args, "--require-all")
		}
		return runScript(t, packagingScript(t, "verify-release-sig.sh"), args...)
	}
	trustK1 := writeSigningTrust(t, "test-k1.pub.pem")
	trustK1K2 := writeSigningTrust(t, "test-k1.pub.pem", "test-k2.pub.pem")
	for _, tc := range []struct {
		name, sig, trust string
		requireAll       bool
	}{
		{name: "K1", sig: signingFixture(t, "SHA256SUMS.k1.sig"), trust: trustK1},
		{name: "K2", sig: signingFixture(t, "SHA256SUMS.k2.sig"), trust: trustK1K2},
		{name: "K1 and K2 require all", sig: signingFixture(t, "SHA256SUMS.k1k2.sig"), trust: trustK1K2, requireAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := verify(t, "v0.0.9", tc.sig, tc.trust, tc.requireAll)
			if err != nil || out != "" {
				t.Fatalf("verify: %v: %q", err, out)
			}
		})
	}
	tampered := filepath.Join(t.TempDir(), "SHA256SUMS")
	data, err := os.ReadFile(sums)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tampered, append(data, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(t.TempDir(), "malformed.sig")
	if err := os.WriteFile(malformed, []byte("not a signature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(signingFixture(t, "SHA256SUMS.k1.sig"))
	if err != nil {
		t.Fatal(err)
	}
	cr := filepath.Join(t.TempDir(), "cr.sig")
	if err := os.WriteFile(cr, bytes.ReplaceAll(golden, []byte("\n"), []byte("\r\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := filepath.Join(t.TempDir(), "too-many.sig")
	if err := os.WriteFile(tooMany, bytes.Repeat(golden, 5), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyTrust := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(emptyTrust, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	duplicateTrust := filepath.Join(t.TempDir(), "duplicate.pem")
	key1, err := os.ReadFile(signingFixture(t, "test-k1.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicateTrust, append(key1, key1...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tag, sumsPath, sig, trust string
		requireAll                      bool
	}{
		{name: "tampered sums", tag: "v0.0.9", sumsPath: tampered, sig: signingFixture(t, "SHA256SUMS.k1.sig"), trust: trustK1},
		{name: "wrong tag", tag: "v0.0.8", sumsPath: sums, sig: signingFixture(t, "SHA256SUMS.k1.sig"), trust: trustK1},
		{name: "malformed line", tag: "v0.0.9", sumsPath: sums, sig: malformed, trust: trustK1},
		{name: "carriage return", tag: "v0.0.9", sumsPath: sums, sig: cr, trust: trustK1},
		{name: "more than four lines", tag: "v0.0.9", sumsPath: sums, sig: tooMany, trust: trustK1},
		{name: "unknown require all", tag: "v0.0.9", sumsPath: sums, sig: signingFixture(t, "SHA256SUMS.k2.sig"), trust: trustK1, requireAll: true},
		{name: "K2 only against K1", tag: "v0.0.9", sumsPath: sums, sig: signingFixture(t, "SHA256SUMS.k2.sig"), trust: trustK1},
		{name: "empty trust", tag: "v0.0.9", sumsPath: sums, sig: signingFixture(t, "SHA256SUMS.k1.sig"), trust: emptyTrust},
		{name: "duplicate trust ids", tag: "v0.0.9", sumsPath: sums, sig: signingFixture(t, "SHA256SUMS.k1.sig"), trust: duplicateTrust},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--tag", tc.tag, "--sums", tc.sumsPath, "--sig", tc.sig, "--trust", tc.trust}
			if tc.requireAll {
				args = append(args, "--require-all")
			}
			out, err := runScript(t, packagingScript(t, "verify-release-sig.sh"), args...)
			if err == nil {
				t.Fatalf("verify unexpectedly succeeded: %q", out)
			}
		})
	}
}

// TestReleaseKeygenCreatesPrivateDirectoryAndPublicOutput checks exclusive key generation behavior.
func TestReleaseKeygenCreatesPrivateDirectoryAndPublicOutput(t *testing.T) {
	requireOpenSSL(t)
	dir := filepath.Join(t.TempDir(), "release-signing")
	out, stderr, err := runKeygen(t, dir)
	if err != nil {
		t.Fatalf("keygen: %v: stdout=%q stderr=%q", err, out, stderr)
	}
	block, rest := pem.Decode([]byte(out))
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 || strings.Contains(out, "PRIVATE") || strings.Contains(stderr, "PRIVATE") {
		t.Fatalf("unexpected keygen output: stdout=%q stderr=%q", out, stderr)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o", info.Mode().Perm())
	}
	keyInfo, err := os.Stat(filepath.Join(dir, "signing-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", keyInfo.Mode().Perm())
	}
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "marker")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	failureOut, failureErr, err := runKeygen(t, existing)
	if err == nil || failureOut != "" || strings.Contains(failureOut, "PRIVATE") || strings.Contains(failureErr, "PRIVATE") {
		t.Fatalf("existing directory result: %v: stdout=%q stderr=%q", err, failureOut, failureErr)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "unchanged" {
		t.Fatalf("existing directory was touched: %v: %q", err, got)
	}
}
