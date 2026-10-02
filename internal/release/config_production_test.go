//go:build !releasefixture

package release

import (
	"bytes"
	"testing"
)

// TestProductionIgnoresFixtureEnvironment verifies test overrides do not affect production configuration.
func TestProductionIgnoresFixtureEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var before, after bytes.Buffer
	if err := DescribeConfig(&before); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GORGANIZER_FIXTURE_LATEST_URL", "GORGANIZER_FIXTURE_ASSETS_URL", "GORGANIZER_FIXTURE_NOTES_URL", "GORGANIZER_FIXTURE_ORIGIN", "GORGANIZER_FIXTURE_TRUST_PEM"} {
		t.Setenv(name, "https://invalid.example:9999/")
	}
	if err := DescribeConfig(&after); err != nil {
		t.Fatal(err)
	}
	if after.String() != before.String() {
		t.Fatalf("fixture environment changed production config: %q", after.String())
	}
	if keys, err := ProductionTrust(); err != nil || keys["b107acc071785a65"] == nil {
		t.Fatalf("production trust changed: %v", err)
	}
}
