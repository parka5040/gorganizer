package vfs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// teardownFixture creates a mounted farm with an original tree and a new write to capture.
func teardownFixture(t *testing.T) (string, string, *MountManager) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Game", "Data")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "original master\x00bytes")
	mustFile(t, filepath.Join(data, "Meshes", "iron.nif"), "original mesh\n")
	overwrite := filepath.Join(filepath.Dir(filepath.Dir(data)), "Overwrite")
	mustDir(t, overwrite)
	mm := NewMountManager(data, overwrite, "testgame")
	if err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	mustFile(t, filepath.Join(data, "Saves", "new.ess"), "new save")
	return data, overwrite, mm
}

// assertTeardownRestored checks original bytes, captured writes, and removal of transition state.
func assertTeardownRestored(t *testing.T, data, overwrite string) {
	t.Helper()
	wantFiles := map[string]string{"Skyrim.esm": "original master\x00bytes", "Meshes/iron.nif": "original mesh\n"}
	seen := make(map[string]bool)
	if err := filepath.WalkDir(data, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(data, path)
		if err != nil {
			return err
		}
		seen[rel] = true
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if want, ok := wantFiles[rel]; !ok || string(body) != want {
			t.Errorf("restored file %s = %q; want %q (present %t)", rel, body, want, ok)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for rel := range wantFiles {
		if !seen[rel] {
			t.Errorf("original file %s is missing", rel)
		}
	}
	if got := mustRead(t, filepath.Join(overwrite, "Saves", "new.ess")); got != "new save" {
		t.Errorf("captured save = %q, want new save", got)
	}
	for _, path := range []string{data + farmBackupSuffix, retiredFarmPath(data), deactivationJournalPath(data), filepath.Join(data, SentinelFilename), filepath.Join(data, "Saves")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("transition path %s remains: %v", path, err)
		}
	}
}

// stopDeactivationAt returns an injected interruption immediately after a teardown step.
func stopDeactivationAt(t *testing.T, step int) error {
	t.Helper()
	stopped := errors.New("simulated power loss")
	original := deactivationStep
	deactivationStep = func(at int) error {
		if at == step {
			return stopped
		}
		return nil
	}
	t.Cleanup(func() { deactivationStep = original })
	return stopped
}

// TestDeactivateCrashMatrix checks recovery after every durable step of normal and forced teardown.
func TestDeactivateCrashMatrix(t *testing.T) {
	for _, force := range []bool{false, true} {
		for step := 1; step <= 5; step++ {
			name := "normal"
			if force {
				name = "force"
			}
			t.Run(name+"/step"+string(rune('0'+step)), func(t *testing.T) {
				data, overwrite, mm := teardownFixture(t)
				stopped := stopDeactivationAt(t, step)
				var err error
				if force {
					err = mm.ForceDeactivate()
				} else {
					err = mm.Deactivate()
				}
				if !errors.Is(err, stopped) {
					t.Fatalf("Deactivate error = %v, want interruption", err)
				}
				deactivationStep = func(int) error { return nil }
				if step < 5 {
					journal, err := readDeactivationJournal(deactivationJournalPath(data))
					if err != nil || journal.SchemaVersion != 1 || journal.Magic != deactivationMagic || journal.GameID != "testgame" || journal.FarmID == "" {
						t.Fatalf("record = %+v, %v", journal, err)
					}
				}
				outcome, err := CleanupStale(data)
				if err != nil || outcome.Pending != nil || (step < 5 && !outcome.Restored) {
					t.Fatalf("CleanupStale = %+v, %v; want restoration without prompt", outcome, err)
				}
				assertTeardownRestored(t, data, overwrite)
			})
		}
	}
}

// TestPartialRetiredFarmRemovalRecoversWithoutPrompt checks a partly removed farm keeps its recorded directory identity.
func TestPartialRetiredFarmRemovalRecoversWithoutPrompt(t *testing.T) {
	data, overwrite, mm := teardownFixture(t)
	original := removeRetiredFarm
	failed := errors.New("simulated incomplete removal")
	removeRetiredFarm = func(path string) error {
		if err := os.Remove(filepath.Join(path, SentinelFilename)); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(path, "Skyrim.esm")); err != nil {
			return err
		}
		return failed
	}
	t.Cleanup(func() { removeRetiredFarm = original })
	if err := mm.Deactivate(); !errors.Is(err, failed) {
		t.Fatalf("Deactivate = %v, want interrupted removal", err)
	}
	removeRetiredFarm = original
	if outcome, err := CleanupStale(data); err != nil || !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("CleanupStale = %+v, %v; want restored without prompt", outcome, err)
	}
	assertTeardownRestored(t, data, overwrite)
}

// TestRecoveryCrashDuringTeardownResumes checks every recovery teardown step resumes without capturing the retired farm again.
func TestRecoveryCrashDuringTeardownResumes(t *testing.T) {
	for step := 1; step <= 5; step++ {
		t.Run(string(rune('0'+step)), func(t *testing.T) {
			data, overwrite, _ := teardownFixture(t)
			stopped := stopDeactivationAt(t, step)
			if _, err := CleanupStale(data); !errors.Is(err, stopped) {
				t.Fatalf("first CleanupStale = %v, want interruption", err)
			}
			deactivationStep = func(int) error { return nil }
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending != nil || (step < 5 && !outcome.Restored) {
				t.Fatalf("resumed CleanupStale = %+v, %v", outcome, err)
			}
			assertTeardownRestored(t, data, overwrite)
		})
	}
}

// TestFailedCaptureNeverStartsTeardown checks a failed non-force capture leaves the farm mounted without a journal.
func TestFailedCaptureNeverStartsTeardown(t *testing.T) {
	data, overwrite, mm := teardownFixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(overwrite, "Saves")); err != nil {
		t.Fatal(err)
	}
	if err := mm.Deactivate(); !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("Deactivate = %v, want capture failure", err)
	}
	if !mm.IsMounted() {
		t.Error("capture failure unmounted the farm")
	}
	if _, err := ReadSentinel(data); err != nil {
		t.Errorf("farm sentinel missing: %v", err)
	}
	if got := mustRead(t, filepath.Join(data, "Saves", "new.ess")); got != "new save" {
		t.Errorf("uncaptured save = %q", got)
	}
	for _, path := range []string{deactivationJournalPath(data), retiredFarmPath(data)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("teardown started at %s: %v", path, err)
		}
	}
}

// TestDeactivationJournalMismatchIsPending checks mismatched and unreadable journals never remove any siblings.
func TestDeactivationJournalMismatchIsPending(t *testing.T) {
	for _, change := range []string{"different backup", "unknown schema", "unreadable journal", "missing backup"} {
		t.Run(change, func(t *testing.T) {
			data, _, mm := teardownFixture(t)
			stopped := stopDeactivationAt(t, 1)
			if err := mm.Deactivate(); !errors.Is(err, stopped) {
				t.Fatalf("Deactivate = %v, want interruption", err)
			}
			deactivationStep = func(int) error { return nil }
			backup := data + farmBackupSuffix
			switch change {
			case "different backup":
				if err := os.Rename(backup, backup+".saved"); err != nil {
					t.Fatal(err)
				}
				mustFile(t, filepath.Join(backup, "replacement"), "unrelated")
			case "unknown schema", "unreadable journal":
				path := deactivationJournalPath(data)
				if change == "unknown schema" {
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var j deactivationJournal
					if err := json.Unmarshal(body, &j); err != nil {
						t.Fatal(err)
					}
					j.SchemaVersion = 2
					body, err = json.Marshal(j)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, body, 0644); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing backup":
				if err := os.Rename(backup, backup+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending == nil || outcome.Restored || !strings.Contains(outcome.Pending.Reason, "Data.gorganizer-retired") {
				t.Fatalf("CleanupStale = %+v, %v; want plain-language pending", outcome, err)
			}
			if _, err := ReadSentinel(data); err != nil {
				t.Errorf("farm changed: %v", err)
			}
			if got := mustRead(t, filepath.Join(data, "Skyrim.esm")); got != "original master\x00bytes" {
				t.Errorf("farm master changed: %q", got)
			}
			if _, err := os.Lstat(deactivationJournalPath(data)); err != nil {
				t.Errorf("journal removed: %v", err)
			}
			if _, err := os.Lstat(retiredFarmPath(data)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("retired folder unexpectedly created: %v", err)
			}
			if change == "different backup" && mustRead(t, filepath.Join(backup, "replacement")) != "unrelated" {
				t.Error("replacement backup changed")
			}
		})
	}
}

// TestRetiredFarmIdentityMismatchIsPending checks recovery never removes an unrelated retired directory.
func TestRetiredFarmIdentityMismatchIsPending(t *testing.T) {
	for _, step := range []int{2, 3} {
		t.Run(string(rune('0'+step)), func(t *testing.T) {
			data, _, mm := teardownFixture(t)
			stopped := stopDeactivationAt(t, step)
			if err := mm.Deactivate(); !errors.Is(err, stopped) {
				t.Fatalf("Deactivate = %v, want interruption", err)
			}
			deactivationStep = func(int) error { return nil }
			retired := retiredFarmPath(data)
			if err := os.Rename(retired, retired+".saved"); err != nil {
				t.Fatal(err)
			}
			mustFile(t, filepath.Join(retired, "unrelated"), "keep me")
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending == nil || outcome.Restored {
				t.Fatalf("CleanupStale = %+v, %v; want pending", outcome, err)
			}
			if got := mustRead(t, filepath.Join(retired, "unrelated")); got != "keep me" {
				t.Errorf("unrelated retired content = %q", got)
			}
			if _, err := ReadSentinel(retired + ".saved"); err != nil {
				t.Errorf("original retired farm changed: %v", err)
			}
			if _, err := os.Lstat(deactivationJournalPath(data)); err != nil {
				t.Errorf("journal removed: %v", err)
			}
			if step == 2 {
				if _, err := os.Lstat(data); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Data unexpectedly restored: %v", err)
				}
				if got := mustRead(t, filepath.Join(data+farmBackupSuffix, "Skyrim.esm")); got != "original master\x00bytes" {
					t.Errorf("backup changed: %q", got)
				}
			} else if got := mustRead(t, filepath.Join(data, "Skyrim.esm")); got != "original master\x00bytes" {
				t.Errorf("restored Data changed: %q", got)
			}
		})
	}
}

// TestForceDeactivateAfterFailedCaptureUsesJournal checks forced teardown can resume when capture was refused.
func TestForceDeactivateAfterFailedCaptureUsesJournal(t *testing.T) {
	data, overwrite, mm := teardownFixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(overwrite, "Saves")); err != nil {
		t.Fatal(err)
	}
	stopped := stopDeactivationAt(t, 2)
	if err := mm.ForceDeactivate(); !errors.Is(err, stopped) {
		t.Fatalf("ForceDeactivate = %v, want interruption after farm rename", err)
	}
	deactivationStep = func(int) error { return nil }
	outcome, err := CleanupStale(data)
	if err != nil || !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("CleanupStale = %+v, %v; want restored without prompt", outcome, err)
	}
	if got := mustRead(t, filepath.Join(data, "Skyrim.esm")); got != "original master\x00bytes" {
		t.Errorf("original master = %q", got)
	}
	for _, path := range []string{retiredFarmPath(data), deactivationJournalPath(data), filepath.Join(data, "Saves", "new.ess")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("forced teardown left %s: %v", path, err)
		}
	}
}

// TestLegacyDeactivationJournalNoFarmID checks older sentinels can retire a farm without a farm ID.
func TestLegacyDeactivationJournalNoFarmID(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "Data")
			backup := data + farmBackupSuffix
			mustFile(t, filepath.Join(backup, "original"), "bytes")
			mustFile(t, filepath.Join(data, "mod"), "modded")
			s := &Sentinel{SchemaVersion: version, Magic: SentinelMagic, GameID: "testgame", BackupPath: backup}
			s.Hash = ComputeLayerHash(s.Layers)
			if err := WriteSentinel(data, s); err != nil {
				t.Fatal(err)
			}
			stopped := stopDeactivationAt(t, 1)
			if _, err := CleanupStale(data); !errors.Is(err, stopped) {
				t.Fatalf("CleanupStale = %v, want interruption", err)
			}
			deactivationStep = func(int) error { return nil }
			journal, err := readDeactivationJournal(deactivationJournalPath(data))
			if err != nil || journal.FarmID != "" {
				t.Fatalf("legacy journal = %+v, %v; want empty farm ID", journal, err)
			}
			outcome, err := CleanupStale(data)
			if err != nil || !outcome.Restored || outcome.Pending != nil {
				t.Fatalf("CleanupStale = %+v, %v", outcome, err)
			}
			if got := mustRead(t, filepath.Join(data, "original")); got != "bytes" {
				t.Errorf("original = %q", got)
			}
		})
	}
}

// TestStrandedFarmLogicIgnoresRetiredDir checks an unrecorded retired farm is never selected as Data or reaped.
func TestStrandedFarmLogicIgnoresRetiredDir(t *testing.T) {
	for _, dataExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "Data missing", true: "Data present"}[dataExists], func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "Data")
			backup := data + farmBackupSuffix
			mustFile(t, filepath.Join(backup, "Skyrim.esm"), "original")
			retired := retiredFarmPath(data)
			mustFile(t, filepath.Join(retired, "Saves", "new.ess"), "uncaptured")
			writeRecoverableFarm(t, retired, backup, "")
			if dataExists {
				mustFile(t, filepath.Join(data, "unrelated"), "mine")
			}
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending == nil || outcome.Restored {
				t.Fatalf("CleanupStale = %+v, %v; want pending", outcome, err)
			}
			if got := mustRead(t, filepath.Join(retired, "Saves", "new.ess")); got != "uncaptured" {
				t.Errorf("retired farm changed: %q", got)
			}
			if got := mustRead(t, filepath.Join(backup, "Skyrim.esm")); got != "original" {
				t.Errorf("backup changed: %q", got)
			}
			if !dataExists {
				if _, err := os.Lstat(data); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("retired farm selected as Data: %v", err)
				}
			}
		})
	}
}

// TestActivateRefusesLeftoverFarmState refuses activation over an unrecovered farm or an unfinished removal.
func TestActivateRefusesLeftoverFarmState(t *testing.T) {
	for _, leftover := range []string{"sentinel", "journal", "retired"} {
		t.Run(leftover, func(t *testing.T) {
			root := t.TempDir()
			dataPath := filepath.Join(root, "Data")
			if err := os.MkdirAll(dataPath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dataPath, "base.esm"), []byte("base"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch leftover {
			case "sentinel":
				if err := os.WriteFile(filepath.Join(dataPath, SentinelFilename), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "journal":
				if err := os.WriteFile(deactivationJournalPath(dataPath), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "retired":
				if err := os.MkdirAll(retiredFarmPath(dataPath), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			mm := NewMountManager(dataPath, filepath.Join(root, "overwrite"), "skyrimse")
			err := mm.Activate([]Layer{{Name: "__base__", RootPath: dataPath + ".orig", Enabled: true}}, "Default")
			if !errors.Is(err, ErrBackupExists) {
				t.Fatalf("Activate error = %v, want ErrBackupExists", err)
			}
			if _, statErr := os.Stat(dataPath + ".orig"); !os.IsNotExist(statErr) {
				t.Fatalf("Activate moved Data aside: %v", statErr)
			}
		})
	}
}
