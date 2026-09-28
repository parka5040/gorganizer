package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

type farmMatrixFixture struct {
	d        *Daemon
	game     string
	data     string
	manifest string
	modFile  string
	original map[string]string
}

// newFarmMatrixFixture creates a mounted-ready Steam game and mod entirely in disposable directories.
func newFarmMatrixFixture(t *testing.T, shared bool) *farmMatrixFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	game := "skyrimse"
	appID := 489830
	name := "Skyrim Special Edition"
	if shared {
		game, appID, name = "falloutnv", 22380, "Fallout New Vegas"
	}
	install, manifest := steamFixture(t, appID, name)
	games := map[string]config.GameConfig{
		game: {InstallPath: install, DataSubpath: "Data", SteamAppID: appID},
	}
	if shared {
		games["ttw"] = config.GameConfig{InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: game}
	}
	d := newIsolatedDaemon(t, games)
	d.readSteamAppState = steam.ReadAppState
	modFile := filepath.Join(config.ModsDir(game), "A", "a.esp")
	writeFileContent(t, modFile, "original mod bytes")
	if err := d.SetModList(game, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(install, "Data")
	return &farmMatrixFixture{d: d, game: game, data: data, manifest: manifest, modFile: modFile, original: snapshotTree(t, data)}
}

// matrixIdentity returns the filesystem identity used by the durable farm records.
func matrixIdentity(t *testing.T, path string) map[string]uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory identity of %s: %v", path, err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return map[string]uint64{"dev": uint64(stat.Dev), "ino": stat.Ino}
}

// matrixRecord writes a disposable transaction record with the same durable write path as production.
func matrixRecord(t *testing.T, path string, record any) {
	t.Helper()
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atomicfile.WriteFileDurable(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

// matrixCopyFarm makes a hardlinked snapshot of a former farm without following any symlinks.
func matrixCopyFarm(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Mkdir(dst, 0755); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.Mkdir(target, 0755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return os.Link(path, target)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// matrixRequireBytes checks exact bytes without accepting a missing file as an empty file.
func matrixRequireBytes(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s = %q, %v; want %q", path, body, err, want)
	}
}

// matrixNoTransitions checks all pending farm siblings while allowing retained Steam records.
func matrixNoTransitions(t *testing.T, data string) {
	t.Helper()
	for _, suffix := range vfs.FarmSiblingSuffixes() {
		if _, err := os.Lstat(data + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unfinished farm sibling %s: %v", data+suffix, err)
		}
	}
}

// TestFarmLifecycleEndToEndCrashMatrix checks recovery across farm, Steam, mod, transfer, and download interruptions.
func TestFarmLifecycleEndToEndCrashMatrix(t *testing.T) {
	cases := []struct {
		name      string
		shared    bool
		unmounted bool
		pending   bool
		deferred  bool
		verify    bool
		removed   bool
		cut       func(*testing.T, *farmMatrixFixture)
		check     func(*testing.T, *farmMatrixFixture, *Daemon)
	}{
		{
			name:      "01 activation after original rename",
			unmounted: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				matrixRecord(t, f.data+".gorganizer-activating", map[string]any{
					"schema_version": 2, "magic": vfs.IntentMagic, "kind": vfs.IntentActivating,
					"operation_id": uuid.NewString(), "game_id": f.game, "data_path": f.data,
					"backup_path": f.data + ".orig", "original": matrixIdentity(t, f.data),
				})
				if err := os.Rename(f.data, f.data+".orig"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "02 apply after directory exchange",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				before, err := vfs.ReadSentinel(f.data)
				if err != nil {
					t.Fatal(err)
				}
				former := filepath.Join(t.TempDir(), "former-farm")
				matrixCopyFarm(t, f.data, former)
				writeFileContent(t, filepath.Join(config.ModsDir(f.game), "B", "b.esp"), "second mod bytes")
				if err := f.d.SetModList(f.game, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}, {ModName: "B", Enabled: true}}); err != nil {
					t.Fatal(err)
				}
				if err := f.d.RebuildVFS(f.game); err != nil {
					t.Fatal(err)
				}
				after, err := vfs.ReadSentinel(f.data)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(former, f.data+".gorganizer-staging"); err != nil {
					t.Fatal(err)
				}
				if err := vfs.WriteIntent(f.data+".gorganizer-applying", &vfs.ActivationIntent{
					SchemaVersion: 2, Magic: vfs.IntentMagic, Kind: vfs.IntentApplying, OperationID: uuid.NewString(),
					GameID: f.game, DataPath: f.data, BackupPath: f.data + ".orig",
					StagingPath: f.data + ".gorganizer-staging", LiveFarmID: before.FarmID, StagingFarmID: after.FarmID,
				}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "B", "b.esp"), "second mod bytes")
			},
		},
		{
			name: "03 unmount after partial Overwrite capture",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				for _, rel := range []string{"a-output.txt", "z-output.txt"} {
					writeFileContent(t, filepath.Join(f.data, rel), rel)
				}
				blocked := filepath.Join(config.ModsDir(f.game), "Overwrite", "z-output.txt")
				if err := os.MkdirAll(filepath.Dir(blocked), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "unrelated"), blocked); err != nil {
					t.Fatal(err)
				}
				if err := f.d.UnmountVFS(f.game); !errors.Is(err, vfs.ErrCaptureFailed) {
					t.Fatalf("interrupted capture = %v, want ErrCaptureFailed", err)
				}
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				for _, rel := range []string{"a-output.txt", "z-output.txt"} {
					matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", rel), rel)
				}
			},
		},
		{
			name: "04 deactivation after original restore",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				writeFileContent(t, filepath.Join(f.data, "retired-output.txt"), "game bytes")
				sentinel, err := vfs.ReadSentinel(f.data)
				if err != nil {
					t.Fatal(err)
				}
				matrixRecord(t, f.data+".gorganizer-deactivating", map[string]any{
					"schema_version": 2, "magic": "gorganizer-deactivating", "game_id": f.game,
					"farm_id": sentinel.FarmID, "farm": matrixIdentity(t, f.data),
					"backup": matrixIdentity(t, f.data+".orig"), "created_at": time.Now().UTC(),
				})
				if err := os.Rename(f.data, f.data+".gorganizer-retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(f.data+".orig", f.data); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", "retired-output.txt"), "game bytes")
			},
		},
		{
			name:     "05 game process holds the farm",
			deferred: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				writeFileContent(t, filepath.Join(f.data, "live-output.txt"), "live bytes")
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				matrixRequireBytes(t, filepath.Join(f.data, "live-output.txt"), "live bytes")
				fakeProcesses(d, false, nil)
				if err := d.RetryDeferredRecovery(f.game); err != nil {
					t.Fatal(err)
				}
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", "live-output.txt"), "live bytes")
			},
		},
		{
			name:     "06 launch ticket defers teardown",
			deferred: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if err := f.d.writeLaunchTicketForGame(f.game, "Default", f.data); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				if _, err := os.Stat(f.data + sessionTicketSuffix); err != nil {
					t.Fatal(err)
				}
				d.now = func() time.Time { return time.Now().Add(steamLaunchGrace + time.Minute) }
				if err := d.RetryDeferredRecovery(f.game); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "07 interrupted confirmed restore",
			pending: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if _, err := vfs.CaptureNewFilesInto(f.data, filepath.Join(config.ModsDir(f.game), "Overwrite"), false, false); err != nil {
					t.Fatal(err)
				}
				matrixRecord(t, f.data+".gorganizer-restoring", map[string]any{
					"schema_version": 1, "data_path": f.data, "backup": matrixIdentity(t, f.data+".orig"),
					"farm": matrixIdentity(t, f.data),
				})
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				status, err := d.GetVFSStatus(f.game)
				if err != nil || status.PendingRecovery == nil {
					t.Fatalf("pending restore = %+v, %v", status, err)
				}
				if err := d.RestoreFromBackup(f.game, dto.RecoveryKindData, status.PendingRecovery.RecoveryID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "08 changed Steam during preservation",
			verify: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				writeFileContent(t, filepath.Join(f.data, "a-steam-output.txt"), "first Steam bytes")
				writeFileContent(t, filepath.Join(f.data, "z-steam-output.txt"), "second Steam bytes")
				changeSteamFixture(t, f.manifest, "buildid", "123", "124")
				if err := os.WriteFile(vfs.PreservedDir(f.data), []byte("block preservation"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := f.d.UnmountVFS(f.game); !errors.Is(err, vfs.ErrCaptureFailed) {
					t.Fatalf("interrupted preservation = %v, want ErrCaptureFailed", err)
				}
				if _, err := os.Stat(f.data + ".gorganizer-deactivating"); err != nil {
					t.Fatalf("preservation decision was not journaled: %v", err)
				}
				journal, err := os.ReadFile(f.data + ".gorganizer-deactivating")
				if err != nil {
					t.Fatal(err)
				}
				var decision struct {
					Capture struct {
						BatchID string `json:"batch_id"`
					} `json:"capture"`
				}
				if err := json.Unmarshal(journal, &decision); err != nil || uuid.Validate(decision.Capture.BatchID) != nil {
					t.Fatalf("journal preservation decision = %+v, %v", decision, err)
				}
				if err := os.Remove(vfs.PreservedDir(f.data)); err != nil {
					t.Fatal(err)
				}
				files := filepath.Join(vfs.PreservedDir(f.data), decision.Capture.BatchID, "files")
				if err := os.MkdirAll(files, 0755); err != nil {
					t.Fatal(err)
				}
				blocked := filepath.Join(files, "z-steam-output.txt")
				if err := os.Symlink(filepath.Join(t.TempDir(), "unrelated"), blocked); err != nil {
					t.Fatal(err)
				}
				outcome, err := vfs.CleanupStale(f.data)
				if err != nil || outcome.Pending == nil {
					t.Fatalf("partial preservation = %+v, %v, want pending", outcome, err)
				}
				matrixRequireBytes(t, filepath.Join(files, "a-steam-output.txt"), "first Steam bytes")
				matrixRequireBytes(t, filepath.Join(f.data, "z-steam-output.txt"), "second Steam bytes")
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				status, err := d.GetVFSStatus(f.game)
				if err != nil || status.PendingRecovery != nil || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
					t.Fatalf("preserved status = %+v, %v", status, err)
				}
				batch := status.PreservedBatches[0]
				if batch.FileCount != 2 {
					t.Fatalf("preserved files = %+v", batch)
				}
				matrixRequireBytes(t, filepath.Join(batch.Path, "files", "a-steam-output.txt"), "first Steam bytes")
				matrixRequireBytes(t, filepath.Join(batch.Path, "files", "z-steam-output.txt"), "second Steam bytes")
				if _, err := os.Lstat(filepath.Join(config.ModsDir(f.game), "Overwrite", "a-steam-output.txt")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Steam output entered Overwrite: %v", err)
				}
			},
		},
		{
			name: "09 deployed mod renamed before restart",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if err := f.d.RenameMod(f.game, "A", "Renamed"); err != nil {
					t.Fatal(err)
				}
				f.modFile = filepath.Join(config.ModsDir(f.game), "Renamed", "a.esp")
			},
		},
		{
			name: "10 deployed reinstall after swap",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				archive := filepath.Join(config.DownloadsDir(f.game), "Replacement.zip")
				writeZipFiles(t, archive, map[string]string{"a.esp": "replacement mod bytes"})
				if err := download.SaveModMetadata(filepath.Dir(f.modFile), &download.ModMetadata{
					Name: "A", Folder: "A", SourceArchives: []download.SourceArchiveRef{{Path: "Downloads/Replacement.zip"}},
				}); err != nil {
					t.Fatal(err)
				}
				f.d.reinstallFault = func(step string) error {
					if step == "swapped" {
						return errSimulatedCrash
					}
					return nil
				}
				if _, _, _, err := f.d.ReinstallMod(context.Background(), f.game, "A", ""); !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("interrupted reinstall = %v", err)
				}
				f.d.reinstallFault = nil
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				assertNoReinstallState(t, config.ModsDir(f.game))
				matrixRequireBytes(t, f.modFile, "replacement mod bytes")
			},
		},
		{
			name: "11 transfer swap before publication",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				id := strings.Repeat("c", 32)
				mods := config.ModsDir(f.game)
				old := filepath.Join(mods, ".transfer-old-"+id)
				if err := os.Rename(filepath.Dir(f.modFile), old); err != nil {
					t.Fatal(err)
				}
				matrixRecord(t, filepath.Join(mods, ".gorganizer-transfer-intent-"+id+".json"), map[string]any{
					"schema_version": 1, "op_id": id, "kind": "mod", "name": "A",
					"staged": ".transfer-stage-" + id, "old": ".transfer-old-" + id,
				})
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				for _, name := range installEntries(t, config.ModsDir(f.game)) {
					if strings.HasPrefix(name, ".transfer-") || strings.HasPrefix(name, ".gorganizer-transfer-intent-") {
						t.Errorf("transfer artifact remains: %s", name)
					}
				}
			},
		},
		{
			name: "12 download landing without network",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if f.game != "skyrimse" {
					t.Fatal("unexpected game")
				}
				seedDaemonLanding(t, true)
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				archive := filepath.Join(config.DownloadsDir(f.game), "7_Example", "archive.zip")
				record := filepath.Join(config.DownloadsDir(f.game), ".gorganizer-landing", daemonLandingID+".json")
				requireFinishedDaemonLanding(t, archive, record)
			},
		},
		{
			name:   "13 FNV and TTW share one Data recovery",
			shared: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				writeFileContent(t, filepath.Join(f.data, "shared-output.txt"), "shared bytes")
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				if a, b := d.recoveryPendingFor("falloutnv"), d.recoveryPendingFor("ttw"); a != nil || b != nil {
					t.Fatalf("shared recovery pending: FNV %+v, TTW %+v", a, b)
				}
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", "shared-output.txt"), "shared bytes")
				if _, err := d.MountVFS("ttw", "Default"); err != nil {
					t.Fatalf("mounting linked TTW after shared recovery: %v", err)
				}
				if err := d.UnmountVFS("ttw"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "14 truncated sentinel leaves farm pending",
			pending: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if err := os.WriteFile(filepath.Join(f.data, vfs.SentinelFilename), []byte("{"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "15 truncated deactivation journal leaves farm pending",
			pending: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if err := os.WriteFile(f.data+".gorganizer-deactivating", []byte("{"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "16 truncated restore record leaves farm pending",
			pending: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				if err := os.WriteFile(f.data+".gorganizer-restoring", []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "17 corrupt manifest uses legacy capture for new output",
			cut: func(t *testing.T, f *farmMatrixFixture) {
				writeFileContent(t, filepath.Join(f.data, "legacy-output.txt"), "new game bytes")
				sentinel, err := vfs.ReadSentinel(f.data)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.data, sentinel.Manifest), []byte("{"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", "legacy-output.txt"), "new game bytes")
			},
		},
		{
			name:    "18 deployed uninstall after trash rename",
			removed: true,
			cut: func(t *testing.T, f *farmMatrixFixture) {
				f.d.uninstallRename = func(from, to string) error {
					if err := os.Rename(from, to); err != nil {
						return err
					}
					return errSimulatedCrash
				}
				if _, err := f.d.UninstallMod(f.game, "A", true); !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("interrupted uninstall = %v", err)
				}
				f.d.uninstallRename = nil
			},
			check: func(t *testing.T, f *farmMatrixFixture, d *Daemon) {
				if _, err := os.Lstat(f.modFile); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("uninstalled mod reappeared: %v", err)
				}
				for _, name := range installEntries(t, config.ModsDir(f.game)) {
					if strings.HasPrefix(name, ".gorganizer-trash-") {
						t.Errorf("uninstalled mod trash remains: %s", name)
					}
				}
				if _, err := os.Lstat(filepath.Join(config.ModsDir(f.game), "Overwrite", "a.esp")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("uninstalled mod was captured as game output: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFarmMatrixFixture(t, tc.shared)
			if !tc.unmounted {
				if _, err := f.d.MountVFS(f.game, "Default"); err != nil {
					t.Fatalf("initial MountVFS: %v", err)
				}
				matrixRequireBytes(t, filepath.Join(f.data, "a.esp"), "original mod bytes")
				if tc.pending {
					writeFileContent(t, filepath.Join(f.data, "held-output.txt"), "game-written bytes")
				}
			}
			tc.cut(t, f)
			d := restartDaemon(t, f.d)
			d.readSteamAppState = steam.ReadAppState
			if tc.name == "05 game process holds the farm" {
				fakeProcesses(d, true, nil)
			}
			d.RecoverAll()
			status, err := d.GetVFSStatus(f.game)
			if err != nil {
				t.Fatal(err)
			}
			if tc.deferred {
				if status.LifecycleState != dto.VFSLifecycleStateRecoveryDeferred {
					t.Fatalf("lifecycle = %+v, want deferred", status)
				}
				if got := snapshotTree(t, f.data+".orig"); !reflect.DeepEqual(got, f.original) {
					t.Fatalf("deferred recovery changed original Data: got %v, want %v", got, f.original)
				}
				if _, err := d.MountVFS(f.game, "Default"); err == nil {
					t.Fatal("mount succeeded while recovery was deferred")
				}
			} else if tc.pending {
				if status.PendingRecovery == nil || status.PendingRecovery.Kind != dto.RecoveryKindData {
					t.Fatalf("lifecycle = %+v, want pending Data recovery", status)
				}
				if got := snapshotTree(t, f.data+".orig"); !reflect.DeepEqual(got, f.original) {
					t.Fatalf("pending recovery changed original Data: got %v, want %v", got, f.original)
				}
				output := filepath.Join(f.data, "held-output.txt")
				if tc.name == "07 interrupted confirmed restore" {
					output = filepath.Join(config.ModsDir(f.game), "Overwrite", "held-output.txt")
				}
				matrixRequireBytes(t, output, "game-written bytes")
				if _, err := d.MountVFS(f.game, "Default"); err == nil {
					t.Fatal("mount succeeded while recovery was pending")
				}
			} else if status.PendingRecovery != nil || status.LifecycleState != dto.VFSLifecycleStateReady {
				t.Fatalf("lifecycle = %+v, want ready", status)
			}
			if tc.check != nil {
				tc.check(t, f, d)
			}
			if tc.name == "07 interrupted confirmed restore" {
				matrixRequireBytes(t, filepath.Join(config.ModsDir(f.game), "Overwrite", "held-output.txt"), "game-written bytes")
			}
			if tc.pending && tc.name != "07 interrupted confirmed restore" {
				if got := snapshotTree(t, f.data+".orig"); !reflect.DeepEqual(got, f.original) {
					t.Fatalf("pending recovery changed original Data: got %v, want %v", got, f.original)
				}
				matrixRequireBytes(t, f.modFile, "original mod bytes")
				return
			}
			wantMod := "original mod bytes"
			if strings.HasPrefix(tc.name, "10 ") {
				wantMod = "replacement mod bytes"
			}
			if tc.verify {
				if _, err := d.MountVFS(f.game, "Default"); err == nil {
					t.Fatal("mount succeeded while Steam verification is required")
				}
			} else {
				if got := snapshotTree(t, f.data); !reflect.DeepEqual(got, f.original) {
					t.Fatalf("original Data bytes changed: got %v, want %v", got, f.original)
				}
				matrixNoTransitions(t, f.data)
				if _, err := d.MountVFS(f.game, "Default"); err != nil {
					t.Fatalf("MountVFS after recovery: %v", err)
				}
				if tc.removed {
					if _, err := os.Lstat(filepath.Join(f.data, "a.esp")); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("uninstalled mod returned to farm: %v", err)
					}
				} else {
					matrixRequireBytes(t, filepath.Join(f.data, "a.esp"), wantMod)
				}
				if err := d.RebuildVFS(f.game); err != nil {
					t.Fatalf("RebuildVFS after recovery: %v", err)
				}
				if err := d.UnmountVFS(f.game); err != nil {
					t.Fatalf("UnmountVFS after recovery: %v", err)
				}
				matrixNoTransitions(t, f.data)
			}
			if got := snapshotTree(t, f.data); !reflect.DeepEqual(got, f.original) {
				t.Fatalf("original Data bytes changed after next lifecycle: got %v, want %v", got, f.original)
			}
			if tc.removed {
				if _, err := os.Lstat(f.modFile); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("uninstalled mod returned after lifecycle: %v", err)
				}
			} else {
				matrixRequireBytes(t, f.modFile, wantMod)
			}
		})
	}
}

// TestLargeFarmRecoveryIsBounded recovers fifty thousand farm files and verifies their source and original bytes.
func TestLargeFarmRecoveryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("fifty-thousand-file recovery benchmark is not run under -short")
	}
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "Game")
	data := filepath.Join(install, "Data")
	writeFileContent(t, filepath.Join(data, "original.esm"), "vanilla bytes")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	modRoot := filepath.Join(config.ModsDir("skyrimse"), "Large")
	start := time.Now()
	for i := 0; i < 50_000; i++ {
		if i%1000 == 0 {
			if err := os.MkdirAll(filepath.Join(modRoot, fmt.Sprintf("%03d", i/1000)), 0755); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(modRoot, fmt.Sprintf("%03d", i/1000), fmt.Sprintf("%04d.dat", i%1000))
		if err := os.WriteFile(path, []byte("mod bytes"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "Large", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	sentinel, err := vfs.ReadSentinel(data)
	if err != nil || sentinel.ManifestEntries < 50_000 {
		t.Fatalf("large farm manifest entries = %+v, %v", sentinel, err)
	}
	recoveryStart := time.Now()
	restarted := restartDaemon(t, d)
	restarted.RecoverAll()
	if pending := restarted.recoveryPendingFor("skyrimse"); pending != nil {
		t.Fatalf("large farm recovery pending: %+v", pending)
	}
	if elapsed := time.Since(recoveryStart); elapsed > 2*time.Minute {
		t.Errorf("fifty-thousand-file recovery took %s, want at most two minutes", elapsed)
	}
	matrixRequireBytes(t, filepath.Join(data, "original.esm"), "vanilla bytes")
	matrixRequireBytes(t, filepath.Join(modRoot, "049", "0999.dat"), "mod bytes")
	matrixNoTransitions(t, data)
	t.Logf("created and mounted 50000 files in %s; recovered in %s", recoveryStart.Sub(start), time.Since(recoveryStart))
}
