//go:build releasefixture

package release

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixtureConfigChild inspects fixture configuration initialized before the test process starts.
func TestFixtureConfigChild(t *testing.T) {
	if os.Getenv("GORGANIZER_FIXTURE_TEST_CHILD") == "" {
		return
	}
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := DescribeConfig(&out)
	if os.Getenv("GORGANIZER_FIXTURE_TEST_CHILD") == "valid" {
		want := "trust c893dee9499f4b4b\nlatest-url https://127.0.0.1:8443/latest\nassets-url https://127.0.0.1:8443/download/\nlatest-origin 127.0.0.1:8443\nassets-origin 127.0.0.1:8443\nsignature required\n"
		if err != nil || out.String() != want {
			t.Fatalf("fixture config: %q: %v", out.String(), err)
		}
		good, _ := url.Parse("https://127.0.0.1:8443/latest")
		bad, _ := url.Parse("https://127.0.0.1:8444/latest")
		if allowLatestOrigin(good) != nil || allowAssetOrigin(bad) == nil {
			t.Fatal("fixture origin policy accepted the wrong origin")
		}
		_, err = (Source{}).fetch(context.Background(), "https://127.0.0.1:8444/SHA256SUMS", 100)
		if err == nil || !strings.Contains(err.Error(), "refusing release origin") {
			t.Fatalf("external request was not blocked: %v", err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "GORGANIZER_FIXTURE_") {
			t.Fatalf("invalid configuration was accepted: %v", err)
		}
		_, err = (Source{}).fetch(context.Background(), "https://127.0.0.1:8443/SHA256SUMS", 100)
		if err == nil || !strings.Contains(err.Error(), "GORGANIZER_FIXTURE_") {
			t.Fatalf("network operation was not rejected: %v", err)
		}
	}
}

// TestFixtureConfigValidation checks valid and missing settings across fresh processes.
func TestFixtureConfigValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	trust, err := filepath.Abs("testdata/signing/test-k1.pub.pem")
	if err != nil {
		t.Fatal(err)
	}
	base := []string{
		"GORGANIZER_FIXTURE_ORIGIN=127.0.0.1:8443",
		"GORGANIZER_FIXTURE_LATEST_URL=https://127.0.0.1:8443/latest",
		"GORGANIZER_FIXTURE_ASSETS_URL=https://127.0.0.1:8443/download/",
		"GORGANIZER_FIXTURE_NOTES_URL=https://127.0.0.1:8443/notes/",
		"GORGANIZER_FIXTURE_TRUST_PEM=" + trust,
	}
	for _, tc := range []struct {
		name, override, mode string
	}{
		{"valid", "", "valid"},
		{"missing trust", "GORGANIZER_FIXTURE_TRUST_PEM=", "invalid"},
		{"invalid origin", "GORGANIZER_FIXTURE_ORIGIN=example.com", "invalid"},
		{"off-origin endpoint", "GORGANIZER_FIXTURE_LATEST_URL=https://example.com/latest", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestFixtureConfigChild$")
			cmd.Env = append(append(os.Environ(), base...), "GORGANIZER_FIXTURE_TEST_CHILD="+tc.mode)
			if tc.override != "" {
				cmd.Env = append(cmd.Env, tc.override)
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture config: %v: %s", err, out)
			}
		})
	}
}
