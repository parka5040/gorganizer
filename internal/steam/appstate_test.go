package steam

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// steamAppFixture writes an app manifest into a disposable library.
func steamAppFixture(t *testing.T, body string) (string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	steamapps := filepath.Join(t.TempDir(), "steamapps")
	install := filepath.Join(steamapps, "common", "Skyrim Special Edition")
	if err := os.MkdirAll(install, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(steamapps, "appmanifest_489830.acf")
	if err := os.WriteFile(manifest, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return install, manifest
}

const appStateFixture = `"AppState"
{
 "appid" "489830"
 "installdir" "Skyrim Special Edition"
 "buildid" "123456"
 "StateFlags" "4"
 "UpdateResult" "0"
 "LastUpdated" "987654321"
 "InstalledDepots"
 {
  "222" { "manifest" "bbb" }
  "111" { "manifest" "aaa" }
 }
}`

func TestReadAppStateIdle(t *testing.T) {
	install, _ := steamAppFixture(t, appStateFixture)
	got, err := ReadAppState(install, 489830)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("111:aaa\n222:bbb"))
	if got.AppID != 489830 || got.InstallDir != "Skyrim Special Edition" || got.BuildID != "123456" ||
		got.StateFlags != 4 || got.UpdateResult != "0" || got.LastUpdated != 987654321 ||
		got.DepotFingerprint != hex.EncodeToString(sum[:]) || !got.Idle() {
		t.Fatalf("ReadAppState = %+v, want the complete idle app state", got)
	}
	for _, flags := range []uint64{0, 1, 2, 5, 8, 1024} {
		if (AppState{StateFlags: flags}).Idle() {
			t.Errorf("StateFlags %d must not be idle", flags)
		}
	}
}

func TestReadAppStateRejectsWrongAppOrInstallDir(t *testing.T) {
	for _, tc := range []struct {
		name, body, installSuffix string
		appID                     int
		wantNotFound              bool
	}{
		{"wrong appid", strings.Replace(appStateFixture, `"appid" "489830"`, `"appid" "22380"`, 1), "", 489830, false},
		{"wrong installdir", strings.Replace(appStateFixture, `"installdir" "Skyrim Special Edition"`, `"installdir" "skyrim special edition"`, 1), "", 489830, false},
		{"wrong path", appStateFixture, "elsewhere", 489830, true},
		{"missing manifest", appStateFixture, "", 22380, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install, _ := steamAppFixture(t, tc.body)
			if tc.installSuffix != "" {
				install = filepath.Join(t.TempDir(), tc.installSuffix)
			}
			_, err := ReadAppState(install, tc.appID)
			if err == nil || errors.Is(err, ErrAppManifestNotFound) != tc.wantNotFound {
				t.Fatalf("ReadAppState = %v, want error (not found: %v)", err, tc.wantNotFound)
			}
		})
	}
}

func TestReadAppStateRejectsSymlinkAndHugeFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"symlink", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Rename(path, path+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".real", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"huge file", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(strings.Repeat("x", maxAppManifestSize+1)), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install, manifest := steamAppFixture(t, appStateFixture)
			tc.setup(t, manifest)
			if _, err := ReadAppState(install, 489830); err == nil {
				t.Fatal("ReadAppState accepted an unsafe manifest")
			}
		})
	}
}

func TestDepotFingerprintStable(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantEqual  bool
	}{
		{"same depots reordered", strings.Replace(appStateFixture,
			`"222" { "manifest" "bbb" }
  "111" { "manifest" "aaa" }`,
			`"111" { "manifest" "aaa" }
  "222" { "manifest" "bbb" }`, 1), true},
		{"different manifest", strings.Replace(appStateFixture, `"manifest" "bbb"`, `"manifest" "ccc"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install, path := steamAppFixture(t, appStateFixture)
			original, err := ReadAppState(install, 489830)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			changed, err := ReadAppState(install, 489830)
			if err != nil {
				t.Fatal(err)
			}
			if (original.DepotFingerprint == changed.DepotFingerprint) != tc.wantEqual {
				t.Errorf("fingerprints = %q and %q, want equal: %v", original.DepotFingerprint, changed.DepotFingerprint, tc.wantEqual)
			}
		})
	}
	install, _ := steamAppFixture(t, strings.Replace(appStateFixture, `"InstalledDepots"`, `"NoInstalledDepots"`, 1))
	state, err := ReadAppState(install, 489830)
	if err != nil || state.DepotFingerprint != "" {
		t.Errorf("missing depots = %+v, %v; want empty fingerprint", state, err)
	}
}
