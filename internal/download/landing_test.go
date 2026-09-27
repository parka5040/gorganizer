package download

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
)

const landingTestID = "dl-00000000-0000-4000-8000-000000000001"

// landingTestManager returns a pipeline whose only HTTP response is in memory.
func landingTestManager(t *testing.T) (*Manager, *Download) {
	t.Helper()
	isolatedDownloadRoot(t)
	m := &Manager{
		nexus: destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"},
		httpClient: &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, ContentLength: 7, Body: io.NopCloser(strings.NewReader("archive")), Header: make(http.Header), Request: req}, nil
		})},
	}
	return m, &Download{ID: landingTestID, GameID: "skyrimse", NXMURI: pipelineURI}
}

// seedLandingRecord saves a verified part, ledger entry, and record in the test Downloads folder.
func seedLandingRecord(t *testing.T) (landingRecord, string, string) {
	t.Helper()
	archive, part := downloadPartPaths()
	if err := os.MkdirAll(filepath.Dir(part), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(part)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	record := landingRecord{
		SchemaVersion: landingSchemaVersion, ID: landingTestID, GameID: "skyrimse",
		ArchiveRel: "7_Example/archive.zip", Size: info.Size(), PartDev: uint64(stat.Dev), PartIno: stat.Ino,
		Sidecar:    ArchiveSidecar{ModID: 7, FileID: 8, ModName: "Example", SizeBytes: info.Size(), DownloadedAt: time.Now().UTC().Format(time.RFC3339)},
		IndexEntry: landingIndexEntry{Path: "7_Example/archive.zip", ModID: 7, FileID: 8}, CreatedAt: time.Now().UTC(),
	}
	if err := UpsertLedgerEntry(LedgerEntry{ID: record.ID, GameID: record.GameID, ArchiveRelPath: record.ArchiveRel, Status: LedgerDownloading}); err != nil {
		t.Fatal(err)
	}
	if err := writeLanding(record); err != nil {
		t.Fatal(err)
	}
	return record, archive, part
}

// assertLandingRetained checks that failed finalization leaves a retryable record and ledger entry.
func assertLandingRetained(t *testing.T, record landingRecord, archive string) {
	t.Helper()
	info, err := os.Stat(archive)
	if err != nil || info.Size() != record.Size {
		t.Fatalf("retained archive: %v, %v", info, err)
	}
	present, err := HasLanding(record.GameID, record.ID)
	if err != nil || !present {
		t.Fatalf("landing record = %v, %v", present, err)
	}
	entries, err := LoadLedger(record.GameID)
	if err != nil || len(entries) != 1 || entries[0].ID != record.ID || entries[0].Status != LedgerFailed {
		t.Fatalf("retryable ledger = %+v, %v", entries, err)
	}
}

// TestLandingOrderAndNoEarlyHook verifies the record precedes rename and the hook follows all durable removals.
func TestLandingOrderAndNoEarlyHook(t *testing.T) {
	m, dl := landingTestManager(t)
	archive, part := downloadPartPaths()
	calledRename, calledHook := false, false
	m.landing.rename = func(from, to string) error {
		calledRename = true
		if from != part || to != archive {
			t.Errorf("rename = %s -> %s", from, to)
		}
		present, err := HasLanding(dl.GameID, dl.ID)
		if err != nil || !present {
			t.Errorf("record before rename = %t, %v", present, err)
		}
		data, err := os.ReadFile(archive)
		if !errors.Is(err, os.ErrNotExist) || len(data) != 0 {
			t.Errorf("archive before rename = %q, %v", data, err)
		}
		return os.Rename(from, to)
	}
	m.hooks.OnArchiveLanded = func(snap DownloadSnapshot, path string, sidecar ArchiveSidecar) {
		calledHook = true
		if !calledRename || snap.ID != dl.ID || path != archive || sidecar.ModName != "Example" {
			t.Errorf("premature or wrong hook: %+v, %q, %+v", snap, path, sidecar)
		}
		if present, err := HasLanding(dl.GameID, dl.ID); present || err != nil {
			t.Errorf("landing record at hook = %t, %v", present, err)
		}
		if entries, err := LoadLedger(dl.GameID); len(entries) != 0 || err != nil {
			t.Errorf("ledger at hook = %+v, %v", entries, err)
		}
		if sc, err := LoadSidecar(archive); err != nil || sc.ModName != sidecar.ModName {
			t.Errorf("sidecar at hook = %+v, %v", sc, err)
		}
		if idx, err := LoadIndex(dl.GameID); err != nil || len(idx.Archives) != 1 {
			t.Errorf("index at hook = %+v, %v", idx, err)
		}
	}
	m.runPipeline(context.Background(), dl)
	if !calledRename || !calledHook || m.snapshot(dl).Status != StatusDownloaded {
		t.Fatalf("pipeline = %+v; rename = %t; hook = %t", m.snapshot(dl), calledRename, calledHook)
	}
	path, _ := landingPath(dl.GameID, dl.ID)
	if stat, err := os.Stat(filepath.Dir(path)); err != nil || stat.Mode().Perm() != 0700 {
		t.Fatalf("landing directory mode = %v, %v", stat, err)
	}
}

// TestSidecarFailureRetainsLanding checks that a metadata failure leaves the full archive and retry state without calling the hook.
func TestSidecarFailureRetainsLanding(t *testing.T) {
	m, dl := landingTestManager(t)
	archive, _ := downloadPartPaths()
	cause := errors.New("injected sidecar failure")
	m.landing.saveSidecar = func(string, ArchiveSidecar, time.Time) error { return cause }
	called := false
	m.hooks.OnArchiveLanded = func(DownloadSnapshot, string, ArchiveSidecar) { called = true }
	m.runPipeline(context.Background(), dl)
	if m.snapshot(dl).Status != StatusFailed || m.snapshot(dl).Error != (&ArchiveInformationSaveError{}).Error() || called {
		t.Fatalf("failed download = %+v; hook called = %t", m.snapshot(dl), called)
	}
	path, _ := landingPath(dl.GameID, dl.ID)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("landing record mode = %v, %v", info, err)
	}
	assertLandingRetained(t, landingRecord{ID: dl.ID, GameID: dl.GameID, Size: 7}, archive)
	if _, err := LoadSidecar(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar after failure = %v", err)
	}
	record, present, err := loadLanding(dl.GameID, dl.ID)
	if err != nil || !present {
		t.Fatalf("loading retained landing = %t, %v", present, err)
	}
	_, err = finishLandingLocked(record, m.landing)
	var informationErr *ArchiveInformationSaveError
	if !errors.As(err, &informationErr) || !errors.Is(err, cause) {
		t.Fatalf("sidecar failure did not unwrap: %v", err)
	}
	if finished, present, err := FinishLanding(dl.GameID, dl.ID); err != nil || !present || finished.Sidecar.ModName != "Example" {
		t.Fatalf("retry finish = %+v, %t, %v", finished, present, err)
	}
}

// TestIndexFailureRetainsLedger checks that an indexed archive is not reported complete until its index is saved.
func TestIndexFailureRetainsLedger(t *testing.T) {
	m, dl := landingTestManager(t)
	archive, _ := downloadPartPaths()
	m.landing.upsertEntry = func(string, IndexEntry) error { return errors.New("injected index failure") }
	called := false
	m.hooks.OnArchiveLanded = func(DownloadSnapshot, string, ArchiveSidecar) { called = true }
	m.runPipeline(context.Background(), dl)
	if m.snapshot(dl).Status != StatusFailed || called {
		t.Fatalf("pipeline = %+v, hook = %t", m.snapshot(dl), called)
	}
	assertLandingRetained(t, landingRecord{ID: dl.ID, GameID: dl.GameID, Size: 7}, archive)
	if _, err := LoadSidecar(archive); err != nil {
		t.Fatal(err)
	}
	if idx, err := LoadIndex(dl.GameID); err != nil || len(idx.Archives) != 0 {
		t.Fatalf("index after failure = %+v, %v", idx, err)
	}
}

// TestFinishLandingIsIdempotent checks that repeated recovery does not create duplicate index rows or ledger entries.
func TestFinishLandingIsIdempotent(t *testing.T) {
	isolatedDownloadRoot(t)
	record, archive, part := seedLandingRecord(t)
	if err := os.Rename(part, archive); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		finished, present, err := FinishLanding(record.GameID, record.ID)
		if err != nil || present != want {
			t.Fatalf("finish = %+v, %t, %v; present want %t", finished, present, err, want)
		}
		if present && (finished.ArchivePath != archive || finished.Snapshot.ID != record.ID || finished.Sidecar != record.Sidecar) {
			t.Fatalf("finished wrong archive: %+v", finished)
		}
	}
	idx, err := LoadIndex(record.GameID)
	if err != nil || len(idx.Archives) != 1 || idx.Archives[0].Path != record.ArchiveRel {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	if entries, err := LoadLedger(record.GameID); err != nil || len(entries) != 0 {
		t.Fatalf("ledger = %+v, %v", entries, err)
	}
}

// TestFinishLandingPreservesSidecarFields checks recovery writes every archived metadata field from the record.
func TestFinishLandingPreservesSidecarFields(t *testing.T) {
	isolatedDownloadRoot(t)
	record, archive, _ := seedLandingRecord(t)
	record.Sidecar = ArchiveSidecar{
		ModID: 7, ModName: "Example", GameDomain: "skyrimspecialedition", ThumbnailURL: "https://example.invalid/image.png",
		AdultContent: true, FileID: 8, FileName: "Main File", FileArchiveName: "archive.zip",
		Version: "1.2", Category: "main", UploadedAt: "2026-09-01T12:00:00Z",
		DownloadedAt: "2026-09-27T12:00:00Z", SizeBytes: record.Size,
	}
	if err := writeLanding(record); err != nil {
		t.Fatal(err)
	}
	path, _ := landingPath(record.GameID, record.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Sidecar map[string]any `json:"sidecar"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Sidecar["thumbnail_url"] != record.Sidecar.ThumbnailURL || decoded.Sidecar["adult_content"] != true {
		t.Fatalf("recorded sidecar = %+v, %v", decoded.Sidecar, err)
	}
	if _, present, err := FinishLanding(record.GameID, record.ID); err != nil || !present {
		t.Fatalf("finishing full sidecar = %t, %v", present, err)
	}
	actual, err := LoadSidecar(archive)
	if err != nil || *actual != record.Sidecar {
		t.Fatalf("landed sidecar = %+v, %v; want %+v", actual, err, record.Sidecar)
	}
}

// TestFinishLandingRenamesSurvivingPart checks recovery can finish before the pipeline's rename.
func TestFinishLandingRenamesSurvivingPart(t *testing.T) {
	isolatedDownloadRoot(t)
	record, archive, part := seedLandingRecord(t)
	finished, present, err := FinishLanding(record.GameID, record.ID)
	if err != nil || !present || finished.ArchivePath != archive {
		t.Fatalf("finish = %+v, %t, %v", finished, present, err)
	}
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part still exists: %v", err)
	}
	if info, err := os.Stat(archive); err != nil || info.Size() != record.Size {
		t.Fatalf("archive = %v, %v", info, err)
	}
}

// TestFinishLandingClaimsManagerDestination checks retry cannot finalize a path still owned by a pipeline.
func TestFinishLandingClaimsManagerDestination(t *testing.T) {
	isolatedDownloadRoot(t)
	record, archive, _ := seedLandingRecord(t)
	manager := &Manager{}
	if !manager.claimDestination(record.GameID, record.ArchiveRel, "pipeline") {
		t.Fatal("pipeline could not claim destination")
	}
	if _, present, err := FinishLandingWithManager(record.GameID, record.ID, manager); !present || !errors.Is(err, ErrArchiveDownloadBusy) {
		t.Fatalf("finishing a claimed destination = %t, %v", present, err)
	}
	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive renamed during claim: %v", err)
	}
	manager.releaseDestination(record.GameID, record.ArchiveRel, "pipeline")
	if finished, present, err := FinishLandingWithManager(record.GameID, record.ID, manager); !present || err != nil || finished.ArchivePath != archive {
		t.Fatalf("finish after release = %+v, %t, %v", finished, present, err)
	}
}

// TestConcurrentFinishLandingHasOneCommit checks per-game serialization publishes an archive only once.
func TestConcurrentFinishLandingHasOneCommit(t *testing.T) {
	isolatedDownloadRoot(t)
	record, _, _ := seedLandingRecord(t)
	start := make(chan struct{})
	results := make(chan bool, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, present, err := FinishLanding(record.GameID, record.ID)
			if err != nil {
				t.Errorf("concurrent finish: %v", err)
			}
			results <- present
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	count := 0
	for present := range results {
		if present {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("finishes = %d, want 1", count)
	}
}

// TestLandingRecoveryRejectsWrongArchive checks that changed archive identities and lengths cannot be indexed.
func TestLandingRecoveryRejectsWrongArchive(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
	}{
		{name: "wrong size", contents: "short"},
		{name: "wrong inode", contents: "archive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			record, archive, part := seedLandingRecord(t)
			if err := os.WriteFile(archive, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(part); err != nil {
				t.Fatal(err)
			}
			if _, present, err := FinishLanding(record.GameID, record.ID); err == nil || !present || !strings.Contains(err.Error(), "archive missing") {
				t.Fatalf("wrong archive finish = %t, %v", present, err)
			}
			if present, err := HasLanding(record.GameID, record.ID); err != nil || !present {
				t.Fatalf("record after rejection = %t, %v", present, err)
			}
			if idx, err := LoadIndex(record.GameID); err != nil || len(idx.Archives) != 0 {
				t.Fatalf("wrong archive was indexed = %+v, %v", idx, err)
			}
		})
	}
}

// TestCorruptLandingRecordIsKept checks unknown versions, corrupt JSON and invalid destinations without changing the record.
func TestCorruptLandingRecordIsKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(landingRecord) []byte
	}{
		{name: "unknown schema", change: func(r landingRecord) []byte { r.SchemaVersion = 2; data, _ := json.Marshal(r); return data }},
		{name: "corrupt json", change: func(landingRecord) []byte { return []byte("{") }},
		{name: "escaping path", change: func(r landingRecord) []byte {
			r.ArchiveRel = "../escape"
			r.IndexEntry.Path = r.ArchiveRel
			data, _ := json.Marshal(r)
			return data
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			record, _, _ := seedLandingRecord(t)
			path, _ := landingPath(record.GameID, record.ID)
			data := tc.change(record)
			if _, err := atomicfile.WriteFileDurable(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, present, err := FinishLanding(record.GameID, record.ID); !present || err == nil {
				t.Fatalf("corrupt landing = %t, %v", present, err)
			}
			kept, err := os.ReadFile(path)
			if err != nil || string(kept) != string(data) {
				t.Fatalf("record changed = %q, %v", kept, err)
			}
		})
	}
	if _, _, err := FinishLanding("skyrimse", "../../outside"); err == nil {
		t.Fatal("unsafe download ID accepted")
	}
}

// TestRehydrateKeepsFailedLandingForRetry checks a corrupt landing cannot trigger a new download on startup.
func TestRehydrateKeepsFailedLandingForRetry(t *testing.T) {
	isolatedDownloadRoot(t)
	record, _, _ := seedLandingRecord(t)
	path, _ := landingPath(record.GameID, record.ID)
	if _, err := atomicfile.WriteFileDurable(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{}
	manager.RehydrateLedger([]string{record.GameID})
	if len(manager.queued) != 0 || len(manager.active) != 0 {
		t.Fatalf("a corrupt landing queued a download: %+v", manager.queued)
	}
	entries, err := LoadLedger(record.GameID)
	if err != nil || len(entries) != 1 || entries[0].Status != LedgerFailed || entries[0].Error != (&ArchiveInformationSaveError{}).Error() {
		t.Fatalf("retryable ledger after startup = %+v, %v", entries, err)
	}
	if present, err := HasLanding(record.GameID, record.ID); err != nil || !present {
		t.Fatalf("corrupt record after startup = %t, %v", present, err)
	}
}

// TestRecoverLandingsSkipsCorruptRecords checks recovery finishes good records while leaving malformed records for inspection.
func TestRecoverLandingsSkipsCorruptRecords(t *testing.T) {
	isolatedDownloadRoot(t)
	record, archive, _ := seedLandingRecord(t)
	dir := filepath.Join(config.DownloadsDir(record.GameID), ".gorganizer-landing")
	bad := filepath.Join(dir, "dl-00000000-0000-4000-8000-000000000002.json")
	if _, err := atomicfile.WriteFileDurable(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	results := RecoverLandings([]string{record.GameID})
	if len(results) != 1 || results[0].ArchivePath != archive {
		t.Fatalf("recovered = %+v", results)
	}
	if _, err := os.Lstat(bad); err != nil {
		t.Fatalf("corrupt record removed: %v", err)
	}
}
