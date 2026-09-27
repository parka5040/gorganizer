package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

const daemonLandingID = "dl-00000000-0000-4000-8000-000000000011"

// seedDaemonLanding creates an expired-link landing for the configured Skyrim test game.
func seedDaemonLanding(t *testing.T, partOnly bool) (string, string) {
	t.Helper()
	return seedDaemonLandingForGame(t, "skyrimse", "7_Example/archive.zip", 7, 8, "Example", "skyrimspecialedition", map[string]string{"readme.txt": "example"}, partOnly)
}

// seedDaemonLandingForGame creates a landing record for an isolated game without network access.
func seedDaemonLandingForGame(t *testing.T, gameID, rel string, modID, fileID int, modName, domain string, files map[string]string, partOnly bool) (string, string) {
	t.Helper()
	archive := filepath.Join(config.DownloadsDir(gameID), rel)
	part := download.PartPath(archive)
	if err := os.MkdirAll(filepath.Dir(part), 0755); err != nil {
		t.Fatal(err)
	}
	writeZipFiles(t, part, files)
	info, err := os.Lstat(part)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if !partOnly {
		if err := os.Rename(part, archive); err != nil {
			t.Fatal(err)
		}
	}
	if err := download.UpsertLedgerEntry(download.LedgerEntry{
		ID: daemonLandingID, GameID: gameID, ArchiveRelPath: rel, GameSlug: domain,
		ModID: modID, FileID: fileID, Status: download.LedgerDownloading,
		NXMURI:    fmt.Sprintf("nxm://%s/mods/%d/files/%d?key=expired&expires=1", domain, modID, fileID),
		BytesDone: info.Size(), BytesTotal: info.Size(),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DownloadsDir(gameID), ".gorganizer-landing", daemonLandingID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	record := struct {
		SchemaVersion int                     `json:"schema_version"`
		ID            string                  `json:"id"`
		GameID        string                  `json:"game_id"`
		ArchiveRel    string                  `json:"archive_rel"`
		Size          int64                   `json:"size"`
		PartDev       uint64                  `json:"part_dev"`
		PartIno       uint64                  `json:"part_ino"`
		Sidecar       download.ArchiveSidecar `json:"sidecar"`
		IndexEntry    struct {
			Path   string `json:"path"`
			ModID  int    `json:"mod_id"`
			FileID int    `json:"file_id"`
		} `json:"index_entry"`
		CreatedAt time.Time `json:"created_at"`
	}{
		SchemaVersion: 1, ID: daemonLandingID, GameID: gameID,
		ArchiveRel: rel, Size: info.Size(), PartDev: uint64(stat.Dev), PartIno: stat.Ino,
		Sidecar:   download.ArchiveSidecar{ModID: modID, ModName: modName, FileID: fileID, GameDomain: domain, FileArchiveName: filepath.Base(archive), SizeBytes: info.Size()},
		CreatedAt: time.Now().UTC(),
	}
	record.IndexEntry.Path, record.IndexEntry.ModID, record.IndexEntry.FileID = rel, modID, fileID
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atomicfile.WriteFileDurable(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return archive, path
}

// requireFinishedDaemonLanding checks the index, sidecar, ledger, archive and record after recovery.
func requireFinishedDaemonLanding(t *testing.T, archive, record string) {
	t.Helper()
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("finished archive: %v", err)
	}
	if _, err := download.LoadSidecar(archive); err != nil {
		t.Fatalf("finished sidecar: %v", err)
	}
	idx, err := download.LoadIndex("skyrimse")
	if err != nil || len(idx.Archives) != 1 || idx.Archives[0].Path != "7_Example/archive.zip" {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	if entries, err := download.LoadLedger("skyrimse"); err != nil || len(entries) != 0 {
		t.Fatalf("ledger = %+v, %v", entries, err)
	}
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record after finish: %v", err)
	}
}

// TestLandingRecoveryWithoutKeyOrNetwork checks startup finishes a recorded archive without constructing a download manager.
func TestLandingRecoveryWithoutKeyOrNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateDaemonState(t)
	games := newSkyrimGames(t)
	archive, record := seedDaemonLanding(t, true)
	cfg := config.DefaultConfig()
	for id, gc := range games {
		cfg.Games[id] = gc
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	if state := d.downloadStateSnapshot(); state.manager != nil || state.key != "" {
		t.Fatalf("download manager without key = %+v", state)
	}
	requireFinishedDaemonLanding(t, archive, record)
	rows, err := d.ListArchives("skyrimse")
	if err != nil || len(rows) != 1 || rows[0].Status != dto.DownloadStatusDownloaded {
		t.Fatalf("recovered rows = %+v, %v", rows, err)
	}
}

// TestUnrecoverableLandingCanBeRemovedWithoutKey checks a damaged landing stays removable after startup.
func TestUnrecoverableLandingCanBeRemovedWithoutKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateDaemonState(t)
	games := newSkyrimGames(t)
	archive, record := seedDaemonLanding(t, false)
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	for id, gc := range games {
		cfg.Games[id] = gc
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	if entries, err := download.LoadLedger("skyrimse"); err != nil || len(entries) != 1 || entries[0].Status != download.LedgerFailed {
		t.Fatalf("unrecoverable landing ledger = %+v, %v", entries, err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("unrecoverable record was discarded: %v", err)
	}
	if err := d.RemoveArchive("skyrimse", "7_Example/archive.zip", daemonLandingID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record after removal: %v", err)
	}
}

// TestExpiredNXMCanFinishLanding checks Retry uses the landing record without resolving the expired link.
func TestExpiredNXMCanFinishLanding(t *testing.T) {
	d := downloadsDaemon(t)
	archive, record := seedDaemonLanding(t, false)
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "7_Example/archive.zip", ModID: 7, FileID: 8}); err != nil {
		t.Fatal(err)
	}
	entries, err := download.LoadLedger("skyrimse")
	if err != nil || len(entries) != 1 {
		t.Fatalf("unfinished ledger = %+v, %v", entries, err)
	}
	entries[0].Status = download.LedgerFailed
	entries[0].Error = (&download.ArchiveInformationSaveError{}).Error()
	if err := download.UpsertLedgerEntry(entries[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListArchives("skyrimse")
	if err != nil || len(rows) != 1 || rows[0].DownloadID != daemonLandingID || rows[0].Status != dto.DownloadStatusFailed {
		t.Fatalf("unfinished rows = %+v, %v", rows, err)
	}
	stream, err := d.StreamArchiveEvents(t.Context(), "skyrimse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RetryDownload(daemonLandingID); err != nil {
		t.Fatalf("retry with expired NXM and no key: %v", err)
	}
	requireFinishedDaemonLanding(t, archive, record)
	select {
	case evt := <-stream:
		if evt.RowChanged == nil || evt.RowChanged.ArchiveRelPath != "7_Example/archive.zip" {
			t.Fatalf("archive event = %+v", evt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not publish the archive row")
	}
	d.background.wait()
}

// TestRemovingFailedLandingDiscardsRecord checks removing a failed download clears its recovery record.
func TestRemovingFailedLandingDiscardsRecord(t *testing.T) {
	d := downloadsDaemon(t)
	archive, record := seedDaemonLanding(t, false)
	entries, err := download.LoadLedger("skyrimse")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger = %+v, %v", entries, err)
	}
	entries[0].Status = download.LedgerFailed
	if err := download.UpsertLedgerEntry(entries[0]); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveArchive("skyrimse", "7_Example/archive.zip", daemonLandingID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded record still exists: %v", err)
	}
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed archive still exists: %v", err)
	}
}

// TestRetryLandingConsumesDependencyOnly checks retry installs a requested dependency without a Nexus key.
func TestRetryLandingConsumesDependencyOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, newStardewGames(newStardewInstall(t)))
	if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveDependencyRequests(depsGame, &depRequestsDoc{
		SchemaVersion: 1, Batches: []depBatch{{
			BatchID: "landing", Profile: "Default", CreatedAt: time.Now().UTC(),
			Entries: []depEntry{{UniqueID: depUniqueID, NexusModID: depNexusID, FileID: depFileID, DownloadID: daemonLandingID, State: depStateDownloading}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	archive, record := seedDaemonLandingForGame(t, depsGame, fmt.Sprintf("%d_Dep/dep-%d.zip", depNexusID, depFileID), depNexusID, depFileID, "Dep Core", "stardewvalley", depArchiveFiles(), false)
	installs := subscribeInstalls(t, d)
	if _, err := d.RetryDownload(daemonLandingID); err != nil {
		t.Fatal(err)
	}
	if result := waitCompleted(t, installs); result.ModName != "Dep Core" {
		t.Fatalf("dependency install = %+v", result)
	}
	d.background.wait()
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record after dependency install: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive after dependency install: %v", err)
	}
	if state := onlyDepEntry(t, depUniqueID).State; state != depStateEnablePending {
		t.Fatalf("dependency after retry = %s", state)
	}
	select {
	case event := <-installs:
		if event.Completed != nil {
			t.Fatalf("archive installed twice: %+v", event.Completed)
		}
	default:
	}
}

// TestRecoveryDoesNotDuplicateAutoInstall checks startup recovery and retry-finish cannot start automatic installation.
func TestRecoveryDoesNotDuplicateAutoInstall(t *testing.T) {
	for _, mode := range []string{"startup", "retry"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			isolateDaemonState(t)
			games := newSkyrimGames(t)
			if err := config.SaveGameSettings("skyrimse", config.GameSettings{AutoInstall: true}); err != nil {
				t.Fatal(err)
			}
			var archive, record string
			if mode == "startup" {
				archive, record = seedDaemonLanding(t, true)
			}
			cfg := config.DefaultConfig()
			for id, gc := range games {
				cfg.Games[id] = gc
			}
			d, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Shutdown)
			d.RecoverAll()
			if mode == "retry" {
				archive, record = seedDaemonLanding(t, false)
				if _, err := d.RetryDownload(daemonLandingID); err != nil {
					t.Fatal(err)
				}
			}
			requireFinishedDaemonLanding(t, archive, record)
			d.background.wait()
			if _, err := os.Lstat(filepath.Join(config.ModsDir("skyrimse"), "Example")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("landing was automatically installed: %v", err)
			}
		})
	}
}
