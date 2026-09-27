package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/dto"
	inipkg "github.com/parka/gorganizer/internal/ini"
)

// iniTestDocuments returns a game settings directory confined to the test home.
func iniTestDocuments(t *testing.T) string {
	t.Helper()
	spec, ok := inipkg.SpecFor("skyrimse")
	if !ok {
		t.Fatal("missing Skyrim SE INI spec")
	}
	path, err := inipkg.DocumentsPath(0, spec.MyGamesSubdir)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSaveProfileIniOutcomes checks profile saves with custom INIs disabled, applied, and blocked.
func TestSaveProfileIniOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		custom      bool
		blockDocs   bool
		missingDocs bool
		wantOutcome dto.IniSaveOutcome
	}{
		{name: "profile only", wantOutcome: dto.IniSaveSaved},
		{name: "applied", custom: true, wantOutcome: dto.IniSaveSavedAndApplied},
		{name: "apply failed", custom: true, blockDocs: true, wantOutcome: dto.IniSaveSavedApplyFailed},
		{name: "missing Documents", custom: true, missingDocs: true, wantOutcome: dto.IniSaveSavedApplyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			d := newProfileDaemon(t, "Default")
			if tc.custom {
				if _, err := d.SetProfileIniEnabled("skyrimse", "Default", true); err != nil {
					t.Fatal(err)
				}
			}
			docs := iniTestDocuments(t)
			if tc.blockDocs {
				blocker := filepath.Join(os.Getenv("HOME"), "Documents")
				if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if tc.custom && !tc.missingDocs {
				if err := os.MkdirAll(docs, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result, err := d.SaveProfileIniFile("skyrimse", "Default", "SkyrimPrefs.ini", "[Display]\niWidth=1920\n")
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %v", result.Outcome, tc.wantOutcome)
			}
			if (tc.blockDocs || tc.missingDocs) && !strings.Contains(result.ApplyError, "Documents") {
				t.Fatalf("missing apply error: %q", result.ApplyError)
			}
			if !tc.blockDocs && !tc.missingDocs && result.ApplyError != "" {
				t.Fatalf("unexpected apply error: %q", result.ApplyError)
			}
			profileContent, err := os.ReadFile(d.iniMgr.IniPath("skyrimse", "Default", "SkyrimPrefs.ini"))
			if err != nil || string(profileContent) != "[Display]\niWidth=1920\n" {
				t.Fatalf("profile INI = %q, err = %v", profileContent, err)
			}
			if tc.custom && !tc.blockDocs && !tc.missingDocs {
				gameContent, err := os.ReadFile(filepath.Join(docs, "SkyrimPrefs.ini"))
				if err != nil || string(gameContent) != string(profileContent) {
					t.Fatalf("game INI = %q, err = %v", gameContent, err)
				}
			}
		})
	}
}

// TestSaveProfileIniWriteFailure returns an error without a save outcome.
func TestSaveProfileIniWriteFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	result, err := d.SaveProfileIniFile("skyrimse", "Default", "unknown.ini", "settings")
	if err == nil || result != nil {
		t.Fatalf("save = %+v, err = %v", result, err)
	}
}

// TestSaveProfileIniWithUnreadableProfile reports a successful write and failed application.
func TestSaveProfileIniWithUnreadableProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	if err := os.WriteFile(filepath.Join(d.profileMgr.ProfileDir("skyrimse", "Default"), "profile.json"), []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := d.SaveProfileIniFile("skyrimse", "Default", "SkyrimPrefs.ini", "new contents")
	if err != nil || result.Outcome != dto.IniSaveSavedApplyFailed || !strings.Contains(result.ApplyError, "loading profile") {
		t.Fatalf("save = %+v, err = %v", result, err)
	}
	content, err := os.ReadFile(d.iniMgr.IniPath("skyrimse", "Default", "SkyrimPrefs.ini"))
	if err != nil || string(content) != "new contents" {
		t.Fatalf("profile file = %q, err = %v", content, err)
	}
}

// TestApplyProfileIniFilesDoesNotChangeFlag checks one-shot application of a disabled profile.
func TestApplyProfileIniFilesDoesNotChangeFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	if _, err := d.SaveProfileIniFile("skyrimse", "Default", "SkyrimPrefs.ini", "[Display]\niWidth=1440\n"); err != nil {
		t.Fatal(err)
	}
	docs := iniTestDocuments(t)
	if err := os.MkdirAll(docs, 0700); err != nil {
		t.Fatal(err)
	}
	count, err := d.ApplyProfileIniFiles("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("applied file count = %d, want 1", count)
	}
	content, err := os.ReadFile(filepath.Join(docs, "SkyrimPrefs.ini"))
	if err != nil || string(content) != "[Display]\niWidth=1440\n" {
		t.Fatalf("game INI = %q, err = %v", content, err)
	}
	status, err := d.GetProfileIniStatus("skyrimse", "Default")
	if err != nil || status.UseCustomIni {
		t.Fatalf("profile setting changed: %+v, err = %v", status, err)
	}
}

// TestApplyProfileIniFilesPropagatesErrors checks missing profiles and blocked game folders.
func TestApplyProfileIniFilesPropagatesErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		blocked bool
		want    string
	}{
		{name: "corrupt profile", profile: "Default", want: "loading profile"},
		{name: "blocked Documents", profile: "Default", blocked: true, want: "Documents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			d := newProfileDaemon(t, "Default")
			if tc.blocked {
				if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "Documents"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(d.profileMgr.ProfileDir("skyrimse", "Default"), "profile.json"), []byte("invalid json"), 0600); err != nil {
				t.Fatal(err)
			}
			count, err := d.ApplyProfileIniFiles("skyrimse", tc.profile)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("count = %d, error = %v, want %q", count, err, tc.want)
			}
		})
	}
}
