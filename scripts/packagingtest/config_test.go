//go:build !releasefixture

package packagingtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/release"
)

// TestReleaseConfigExpectedMatchesProduction checks the audited bytes against production configuration.
func TestReleaseConfigExpectedMatchesProduction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want, err := os.ReadFile(packagingScript(t, "release-config.expected"))
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := release.DescribeConfig(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("production release configuration differs: %q", got.String())
	}
}

// TestAuditReleaseChecksBuildProvenance checks fixture tags, linker overrides and real production config.
func TestAuditReleaseChecksBuildProvenance(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is unavailable")
	}
	modCache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	for _, tc := range []struct {
		name, flag, reject string
	}{
		{"production", "", ""},
		{"fixture tag", "-tags=releasefixture", "releasefixture"},
		{"linker override", "-ldflags=-X github.com/parka/gorganizer/internal/release.anything=x", "linker override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.MkdirAll(filepath.Join(bundle, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"gorganizerctl", "gorganizerd"} {
				args := []string{"build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(bundle, "bin", name)}
				if tc.flag != "" {
					args = append(args, tc.flag)
				}
				args = append(args, "./cmd/"+name)
				cmd := exec.Command("go", args...)
				cmd.Dir = filepath.Join("..", "..")
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+strings.TrimSpace(string(modCache)), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build %s: %v: %s", name, err, out)
				}
			}
			out, err := runScript(t, packagingScript(t, "audit-release.sh"), bundle)
			if tc.reject == "" {
				if err != nil || strings.Contains(out, "release configuration") {
					t.Fatalf("production config audit: %v: %s", err, out)
				}
			} else if err == nil || !strings.Contains(out, tc.reject) {
				t.Fatalf("audit accepted %s: %v: %s", tc.name, err, out)
			}
		})
	}
}
