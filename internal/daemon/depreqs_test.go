package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

const (
	depNexusID  = 555
	depFileID   = 9001
	depUniqueID = "Dep.Core"
)

type fakeNexusFiles struct {
	mu    sync.Mutex
	files map[int][]download.NexusFileDetails
	calls []int
}

type fakeDepDownloader struct {
	mu      sync.Mutex
	uris    []string
	err     error
	live    map[string]bool
	onStart func(id string)
}

type depClock struct {
	mu  sync.Mutex
	now time.Time
}

type fakeDepResolver struct {
	cdnURL string
}

// ListModFilesContext returns the configured files of modID.
func (f *fakeNexusFiles) ListModFilesContext(_ context.Context, _ string, modID int) (*download.NexusFileList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, modID)
	return &download.NexusFileList{Files: append([]download.NexusFileDetails(nil), f.files[modID]...)}, nil
}

// callCount returns how many file listings were requested.
func (f *fakeNexusFiles) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// StartDownloadForGame records the queued URI and returns a sequential download ID that stays live until forgotten.
func (f *fakeDepDownloader) StartDownloadForGame(uri, _ string) (string, int, error) {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return "", 0, f.err
	}
	f.uris = append(f.uris, uri)
	id := fmt.Sprintf("dl-%d", len(f.uris))
	if f.live == nil {
		f.live = map[string]bool{}
	}
	f.live[id] = true
	onStart := f.onStart
	f.mu.Unlock()
	if onStart != nil {
		onStart(id)
	}
	return id, 0, nil
}

// GetProgress reports a queued download until it is forgotten.
func (f *fakeDepDownloader) GetProgress(downloadID string) (*download.DownloadSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[downloadID] {
		return nil, &download.DownloadNotFoundError{ID: downloadID}
	}
	return &download.DownloadSnapshot{ID: downloadID, GameID: depsGame, Status: download.StatusQueued}, nil
}

// forget drops a download from the fake queue, as when the manager finished or lost it.
func (f *fakeDepDownloader) forget(downloadID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, downloadID)
}

// queued returns the URIs queued so far.
func (f *fakeDepDownloader) queued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uris...)
}

// Now returns the fake time.
func (c *depClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake time forward.
func (c *depClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ResolveDownloadURL returns the fake CDN URL.
func (r fakeDepResolver) ResolveDownloadURL(*download.NXMLink) (string, error) {
	return r.cdnURL, nil
}

// GetModInfo names every mod Dep Core.
func (r fakeDepResolver) GetModInfo(string, int) (*download.NexusModInfo, error) {
	return &download.NexusModInfo{Name: "Dep Core"}, nil
}

// GetFileDetails describes every file as the main archive.
func (r fakeDepResolver) GetFileDetails(string, int, int) (*download.NexusFileDetails, error) {
	return &download.NexusFileDetails{FileName: "DepCore.zip", Name: "Main", Version: "1.0.0", CategoryName: "MAIN"}, nil
}

// newFetchDaemon builds a Stardew daemon whose enabled Needy mod lacks Dep.Core, with fake smapi.io, Nexus and download seams for key.
func newFetchDaemon(t *testing.T, key string) (*Daemon, *fakeSMAPIIO, *fakeNexusFiles, *fakeDepDownloader) {
	t.Helper()
	d, _, api := newDepsDaemon(t)
	api.set(depUniqueID, fakeSMAPIMod{name: "Dep Core", nexusID: depNexusID})
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", depUniqueID)
	setOrderedModList(t, d, "Default", enabled("Needy"))
	d.mu.Lock()
	d.config.NexusAPIKey = key
	d.mu.Unlock()
	d.nexusUsers = &fakeNexusUsers{errs: map[string]error{"revoked-key": download.ErrInvalidKey}}
	files := &fakeNexusFiles{files: map[int][]download.NexusFileDetails{
		depNexusID: {{FileID: depFileID, CategoryName: "MAIN", IsPrimary: true, FileName: "DepCore.zip"}, {FileID: 12, CategoryName: "OPTIONAL"}},
	}}
	d.svc.modDeps.nexusFiles = func(string) nexusFileLister { return files }
	downloader := &fakeDepDownloader{}
	d.svc.modDeps.downloads = func() dependencyDownloader { return downloader }
	return d, api, files, downloader
}

// depArchiveFiles returns an archive holding the Dep.Core mod folder.
func depArchiveFiles() map[string]string {
	return map[string]string{"DepCore/manifest.json": depManifest("Dep Core", depUniqueID), "DepCore/Mod.dll": "dll"}
}

// writeLandedArchive writes a downloaded archive, its sidecar and index entry, returning the absolute path and sidecar.
func writeLandedArchive(t *testing.T, modID, fileID int, modName string, files map[string]string) (string, download.ArchiveSidecar) {
	t.Helper()
	rel := filepath.Join(fmt.Sprintf("%d_Dep", modID), fmt.Sprintf("dep-%d.zip", fileID))
	abs := filepath.Join(config.DownloadsDir(depsGame), rel)
	writeZipFiles(t, abs, files)
	sidecar := download.ArchiveSidecar{ModID: modID, FileID: fileID, ModName: modName, GameDomain: "stardewvalley"}
	if err := download.SaveSidecar(abs, sidecar, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := download.UpsertEntry(depsGame, download.IndexEntry{Path: rel, ModID: modID, FileID: fileID}); err != nil {
		t.Fatal(err)
	}
	return abs, sidecar
}

// landArchive writes a downloaded archive and runs the landed-archive handling synchronously.
func landArchive(t *testing.T, d *Daemon, downloadID string, modID, fileID int, modName string, files map[string]string) {
	t.Helper()
	abs, sidecar := writeLandedArchive(t, modID, fileID, modName, files)
	d.svc.archives.handleLandedArchive(download.DownloadSnapshot{ID: downloadID, GameID: depsGame}, abs, sidecar)
}

// queueLedgerDownload records downloadID in the Stardew download ledger with status, as the download manager does.
func queueLedgerDownload(t *testing.T, downloadID string, status download.LedgerStatus, errText string) {
	t.Helper()
	entry := download.LedgerEntry{
		ID: downloadID, GameID: depsGame, GameSlug: "stardewvalley", ModID: depNexusID, FileID: depFileID,
		NXMURI: fmt.Sprintf("nxm://stardewvalley/mods/%d/files/%d", depNexusID, depFileID), Status: status, Error: errText,
	}
	if err := download.UpsertLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
}

// restartWithoutNexusKey restarts the daemon without a Nexus API key so no real download manager rehydrates the ledger, waiting for its dependency recovery.
func restartWithoutNexusKey(t *testing.T, d *Daemon) *Daemon {
	t.Helper()
	d.mu.Lock()
	d.config.NexusAPIKey = ""
	d.mu.Unlock()
	restarted := restartDaemon(t, d)
	restarted.RecoverAll()
	restarted.background.wait()
	return restarted
}

// readDepRequests parses the Stardew dependency request file.
func readDepRequests(t *testing.T) depRequestsDoc {
	t.Helper()
	data, err := os.ReadFile(dependencyRequestsPath(depsGame))
	if err != nil {
		t.Fatalf("reading dependency requests: %v", err)
	}
	var doc depRequestsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing dependency requests: %v", err)
	}
	if doc.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", doc.SchemaVersion)
	}
	return doc
}

// depEntries returns every request entry for uniqueID across batches.
func depEntries(t *testing.T, uniqueID string) []depEntry {
	t.Helper()
	var entries []depEntry
	for _, batch := range readDepRequests(t).Batches {
		for _, entry := range batch.Entries {
			if strings.EqualFold(entry.UniqueID, uniqueID) {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

// onlyDepEntry returns the single request entry for uniqueID.
func onlyDepEntry(t *testing.T, uniqueID string) depEntry {
	t.Helper()
	entries := depEntries(t, uniqueID)
	if len(entries) != 1 {
		t.Fatalf("entries for %s = %+v, want exactly one", uniqueID, entries)
	}
	return entries[0]
}

// fetchOne fetches uniqueIDs for profileName and returns the single result.
func fetchOne(t *testing.T, d *Daemon, profileName string, uniqueIDs ...string) dto.DependencyFetchResult {
	t.Helper()
	results, err := d.FetchModDependencies(context.Background(), depsGame, profileName, uniqueIDs)
	if err != nil {
		t.Fatalf("FetchModDependencies: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	return results[0]
}

// subscribeInstalls subscribes to the Stardew install stream for the rest of the test.
func subscribeInstalls(t *testing.T, d *Daemon) <-chan dto.InstallEventResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, _ := d.installBus.Subscribe(ctx, depsGame)
	return ch
}

// waitCompleted returns the next InstallCompleted event, failing after a timeout.
func waitCompleted(t *testing.T, ch <-chan dto.InstallEventResult) dto.InstallCompletedResult {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				t.Fatal("install stream closed")
			}
			if evt.Completed != nil {
				return *evt.Completed
			}
		case <-deadline:
			t.Fatal("no InstallCompleted event")
		}
	}
}

// modListState returns the enabled flag of every modlist entry of profileName.
func modListState(t *testing.T, d *Daemon, profileName string) map[string]bool {
	t.Helper()
	entries, err := d.GetModList(depsGame, profileName)
	if err != nil {
		t.Fatalf("GetModList: %v", err)
	}
	state := map[string]bool{}
	for _, entry := range entries {
		state[entry.ModName] = entry.Enabled
	}
	return state
}

// zipBytes returns an in-memory zip archive of files.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchPremiumQueuesAndLandingInstallsForAPendingEnable(t *testing.T) {
	d, _, files, downloader := newFetchDaemon(t, premiumTestKey)
	installs := subscribeInstalls(t, d)

	result := fetchOne(t, d, "Default", depUniqueID)
	if result.Outcome != dto.FetchOutcomeQueued || result.DownloadID != "dl-1" || result.BatchID == "" || result.URL != "" {
		t.Fatalf("result = %+v, want a queued premium download", result)
	}
	if got := downloader.queued(); !reflect.DeepEqual(got, []string{"nxm://stardewvalley/mods/555/files/9001"}) {
		t.Fatalf("queued URIs = %v, want the primary MAIN file without a key", got)
	}
	if files.callCount() != 1 {
		t.Fatalf("file listings = %d, want 1", files.callCount())
	}
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateDownloading || entry.DownloadID != "dl-1" || entry.FileID != depFileID || entry.NexusModID != depNexusID || entry.ExpiresAt != nil {
		t.Fatalf("entry = %+v, want a downloading premium request", entry)
	}
	data, err := os.ReadFile(dependencyRequestsPath(depsGame))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(premiumTestKey)) {
		t.Fatal("the dependency request file contains the Nexus API key")
	}
	if info, err := os.Stat(dependencyRequestsPath(depsGame)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("request file mode = %v, %v; want 0600", info, err)
	}

	landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
	completed := waitCompleted(t, installs)
	if completed.ModName != "Dep Core" || completed.BatchID != result.BatchID || completed.GameID != depsGame {
		t.Fatalf("InstallCompleted = %+v, want Dep Core for batch %s", completed, result.BatchID)
	}
	entry = onlyDepEntry(t, depUniqueID)
	if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after landing = %+v, want enable_pending Dep Core", entry)
	}
	if state := modListState(t, d, "Default"); state["Dep Core"] || !state["Needy"] {
		t.Fatalf("modlist = %v, want Dep Core appended disabled", state)
	}
	report := depReport(t, d, "Default")
	want := []dto.PendingEnableResult{{BatchID: result.BatchID, ProfileName: "Default", ModName: "Dep Core", UniqueID: depUniqueID}}
	if !reflect.DeepEqual(report.PendingEnables, want) {
		t.Fatalf("pending enables = %+v, want %+v", report.PendingEnables, want)
	}
	if missing, ok := missingDep(report, depUniqueID); !ok || !reflect.DeepEqual(missing.DisabledProviders, []string{"Dep Core"}) {
		t.Fatalf("missing = %+v, want Dep.Core satisfiable by the disabled Dep Core", report.Missing)
	}

	setOrderedModList(t, d, "Default", enabled("Needy"), enabled("Dep Core"))
	if report := depReport(t, d, "Default"); len(report.Missing) != 0 || len(report.PendingEnables) != 1 {
		t.Fatalf("after enabling: missing %+v, pending %+v; want no missing and the enable still unacknowledged", report.Missing, report.PendingEnables)
	}
	acked, err := d.AckDependencyEnable(context.Background(), depsGame, result.BatchID, []string{"dep.core"})
	if err != nil || acked != 1 {
		t.Fatalf("AckDependencyEnable = %d, %v; want 1", acked, err)
	}
	if again, err := d.AckDependencyEnable(context.Background(), depsGame, result.BatchID, nil); err != nil || again != 0 {
		t.Fatalf("second AckDependencyEnable = %d, %v; want an idempotent 0", again, err)
	}
	if report := depReport(t, d, "Default"); len(report.PendingEnables) != 0 {
		t.Fatalf("pending enables after ack = %+v, want none", report.PendingEnables)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDone {
		t.Fatalf("entry after ack = %+v, want done", entry)
	}
}

func TestPremiumDependencyDownloadInstallsThroughTheDownloadManager(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	archive := zipBytes(t, depArchiveFiles())
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(cdn.Close)
	manager := download.NewManager(fakeDepResolver{cdnURL: cdn.URL + "/DepCore.zip"}, d.config, 1, d.svc.archives.managerHooks())
	t.Cleanup(manager.Stop)
	d.svc.modDeps.downloads = nil
	d.mu.Lock()
	d.downloadMgr = manager
	d.mu.Unlock()
	installs := subscribeInstalls(t, d)

	result := fetchOne(t, d, "Default", depUniqueID)
	if result.Outcome != dto.FetchOutcomeQueued || !strings.HasPrefix(result.DownloadID, "dl-") {
		t.Fatalf("result = %+v, want a queued download", result)
	}
	completed := waitCompleted(t, installs)
	if completed.BatchID != result.BatchID || completed.ModName != "Dep Core" {
		t.Fatalf("InstallCompleted = %+v, want Dep Core for the batch", completed)
	}
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateEnablePending || entry.DownloadID != result.DownloadID {
		t.Fatalf("entry = %+v, want enable_pending for download %s", entry, result.DownloadID)
	}
	if state := modListState(t, d, "Default"); state["Dep Core"] {
		t.Fatalf("modlist = %v, want the dependency appended disabled", state)
	}
}

func TestFetchForAFreeAccountOpensTheFilesPageAndABrowserDownloadInstalls(t *testing.T) {
	d, _, files, downloader := newFetchDaemon(t, "regular-key")
	clock := &depClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	d.now = clock.Now

	result := fetchOne(t, d, "Default", depUniqueID)
	if result.Outcome != dto.FetchOutcomeOpenURL || result.URL != "https://www.nexusmods.com/stardewvalley/mods/555?tab=files" || result.Reason != fetchReasonNotPremium || result.BatchID == "" {
		t.Fatalf("result = %+v, want the Nexus files page for a free account", result)
	}
	if len(downloader.queued()) != 0 || files.callCount() != 0 {
		t.Fatalf("free account queued %v and listed files %d times, want neither", downloader.queued(), files.callCount())
	}
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateAwaitingDownload || entry.ExpiresAt == nil || !entry.ExpiresAt.Equal(clock.Now().Add(browserRequestTTL)) {
		t.Fatalf("entry = %+v, want an awaiting browser request expiring in 24h", entry)
	}

	landArchive(t, d, "dl-browser", depNexusID, 777, "Dep Core", depArchiveFiles())
	entry = onlyDepEntry(t, depUniqueID)
	if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after the browser download = %+v, want enable_pending", entry)
	}
	if report := depReport(t, d, "Default"); len(report.PendingEnables) != 1 {
		t.Fatalf("pending enables = %+v, want one", report.PendingEnables)
	}
}

func TestLandingOfAnArchiveWithoutTheRequestedIDIsNotConsumed(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	fetchOne(t, d, "Default", depUniqueID)
	other := map[string]string{"Other/manifest.json": depManifest("Other", "Other.Mod"), "Other/Mod.dll": "dll"}

	landArchive(t, d, "dl-x", depNexusID, 701, "Other Mod", other)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload {
		t.Fatalf("entry = %+v, want the browser request still waiting", entry)
	}
	if _, err := os.Stat(storeMod("Other Mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the wrong archive was installed without auto-install: %v", err)
	}

	if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	landArchive(t, d, "dl-y", depNexusID, 702, "Other Mod", other)
	if _, err := os.Stat(storeMod("Other Mod")); err != nil {
		t.Fatalf("an unconsumed archive skipped the normal auto-install: %v", err)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload {
		t.Fatalf("entry after auto-install = %+v, want still waiting", entry)
	}
}

func TestPremiumDownloadOfTheWrongArchiveFailsTheRequest(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Other Mod", map[string]string{"Other/manifest.json": depManifest("Other", "Other.Mod")})
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateFailed || !strings.Contains(entry.Detail, "archive_mismatch") {
		t.Fatalf("entry = %+v, want a failed premium request", entry)
	}
	if _, err := os.Stat(storeMod("Other Mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the mismatched archive was installed: %v", err)
	}
}

func TestTwoProfilesWaitingOnOnePageShareOneInstall(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	if _, err := d.CreateProfile(depsGame, "Alt"); err != nil {
		t.Fatal(err)
	}
	setOrderedModList(t, d, "Alt", enabled("Needy"))
	first := fetchOne(t, d, "Default", depUniqueID)
	second := fetchOne(t, d, "Alt", depUniqueID)
	if first.BatchID == second.BatchID {
		t.Fatalf("both profiles share batch %s", first.BatchID)
	}
	installs := subscribeInstalls(t, d)

	landArchive(t, d, "dl-browser", depNexusID, 777, "Dep Core", depArchiveFiles())
	completed := waitCompleted(t, installs)
	gotBatches := append([]string(nil), completed.BatchIDs...)
	sort.Strings(gotBatches)
	wantBatches := []string{first.BatchID, second.BatchID}
	sort.Strings(wantBatches)
	if !reflect.DeepEqual(gotBatches, wantBatches) || completed.BatchID != completed.BatchIDs[0] {
		t.Fatalf("InstallCompleted = %+v, want every served batch %v with batch_id the first", completed, wantBatches)
	}
	for _, entry := range depEntries(t, depUniqueID) {
		if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
			t.Fatalf("entry = %+v, want every waiting profile enable_pending on the one install", entry)
		}
	}
	for profileName, batchID := range map[string]string{"Default": first.BatchID, "Alt": second.BatchID} {
		report := depReport(t, d, profileName)
		if len(report.PendingEnables) != 1 || report.PendingEnables[0].BatchID != batchID || report.PendingEnables[0].ModName != "Dep Core" {
			t.Fatalf("%s pending enables = %+v, want its own batch", profileName, report.PendingEnables)
		}
		if state := modListState(t, d, profileName); state["Dep Core"] {
			t.Fatalf("%s modlist = %v, want Dep Core disabled", profileName, state)
		}
	}
	entries, err := os.ReadDir(config.ModsDir(depsGame))
	if err != nil {
		t.Fatal(err)
	}
	var mods []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "Dep") {
			mods = append(mods, entry.Name())
		}
	}
	if !reflect.DeepEqual(mods, []string{"Dep Core"}) {
		t.Fatalf("installed dependency mods = %v, want one", mods)
	}
}

func TestFetchFallsBackToTheBrowser(t *testing.T) {
	ambiguous := []download.NexusFileDetails{{FileID: 1, CategoryName: "MAIN"}, {FileID: 2, CategoryName: "MAIN"}}
	optionalOnly := []download.NexusFileDetails{{FileID: 3, CategoryName: "OPTIONAL"}}
	cases := []struct {
		name         string
		key          string
		files        []download.NexusFileDetails
		noManager    bool
		wantReason   string
		unregistered bool
	}{
		{name: "ambiguous main file", key: premiumTestKey, files: ambiguous, wantReason: fetchReasonAmbiguousMain},
		{name: "no main file", key: premiumTestKey, files: optionalOnly, wantReason: fetchReasonNoMainFile},
		{name: "no api key", key: "", wantReason: fetchReasonNoAPIKey, unregistered: true},
		{name: "no download manager", key: premiumTestKey, noManager: true, wantReason: fetchReasonNoDownloads, unregistered: true},
		{name: "premium check failed", key: "revoked-key", wantReason: fetchReasonPremiumCheck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, files, downloader := newFetchDaemon(t, tc.key)
			if tc.files != nil {
				files.files[depNexusID] = tc.files
			}
			if tc.noManager {
				d.svc.modDeps.downloads = func() dependencyDownloader { return nil }
			}
			result := fetchOne(t, d, "Default", depUniqueID)
			if result.Outcome != dto.FetchOutcomeOpenURL || result.Reason != tc.wantReason || !strings.HasSuffix(result.URL, "/mods/555?tab=files") {
				t.Fatalf("result = %+v, want OPEN_URL with reason %s", result, tc.wantReason)
			}
			if len(downloader.queued()) != 0 {
				t.Fatalf("queued %v, want nothing", downloader.queued())
			}
			if tc.unregistered {
				if result.BatchID != "" {
					t.Fatalf("batch = %q, want no request that could never complete", result.BatchID)
				}
				if _, err := os.Stat(dependencyRequestsPath(depsGame)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a request was registered without a way to land it: %v", err)
				}
				return
			}
			if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload || entry.Detail != tc.wantReason || result.BatchID == "" {
				t.Fatalf("entry = %+v (batch %q), want an awaiting browser request", entry, result.BatchID)
			}
		})
	}
}

func TestFetchFallsBackToTheBrowserWhenQueueingFails(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	downloader.err = errors.New("queue refused")
	result := fetchOne(t, d, "Default", depUniqueID)
	if result.Outcome != dto.FetchOutcomeOpenURL || result.Reason != fetchReasonQueueFailed {
		t.Fatalf("result = %+v, want the browser fallback", result)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload || entry.ExpiresAt == nil {
		t.Fatalf("entry = %+v, want an awaiting browser request", entry)
	}
}

func TestBrowserDependencyRequestsExpire(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	clock := &depClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	d.now = clock.Now
	fetchOne(t, d, "Default", depUniqueID)

	clock.Advance(browserRequestTTL - time.Minute)
	depReport(t, d, "Default")
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload {
		t.Fatalf("entry before the deadline = %+v, want still waiting", entry)
	}
	clock.Advance(2 * time.Minute)
	abs, sidecar := writeLandedArchive(t, depNexusID, 777, "Dep Core", depArchiveFiles())
	if d.svc.modDeps.consumeLandedArchive(depsGame, "dl-late", abs, sidecar) {
		t.Fatal("an expired browser request consumed a late download")
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateExpired {
		t.Fatalf("entry after the deadline = %+v, want expired", entry)
	}
	if _, err := os.Stat(storeMod("Dep Core")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an expired request installed the archive: %v", err)
	}
}

func TestPremiumRequestSurvivesADaemonRestart(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	result := fetchOne(t, d, "Default", depUniqueID)
	err := d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
		doc.Batches = append(doc.Batches, depBatch{BatchID: "dep-interrupted", Profile: "Default", CreatedAt: now, Entries: []depEntry{{UniqueID: "Half.Done", NexusModID: 9, State: depStateInstalling}}})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	queueLedgerDownload(t, result.DownloadID, download.LedgerDownloading, "")
	restarted := restartWithoutNexusKey(t, d)
	restarted.svc.modDeps.client = d.svc.modDeps.client
	if entry := onlyDepEntry(t, "Half.Done"); entry.State != depStateFailed || !strings.Contains(entry.Detail, "install_interrupted") {
		t.Fatalf("interrupted entry after restart = %+v, want failed", entry)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDownloading || entry.DownloadID != result.DownloadID {
		t.Fatalf("entry after restart = %+v, want the pending download kept", entry)
	}
	landArchive(t, restarted, result.DownloadID, depNexusID+1, depFileID, "Dep Core", depArchiveFiles())
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after landing on the restarted daemon = %+v, want enable_pending", entry)
	}
	if report := depReport(t, restarted, "Default"); len(report.PendingEnables) != 1 || report.PendingEnables[0].BatchID != result.BatchID {
		t.Fatalf("pending enables after restart = %+v, want the original batch", report.PendingEnables)
	}
}

func TestFetchSettlesIDsThatAreNotFetchable(t *testing.T) {
	d, api, _, _ := newFetchDaemon(t, "regular-key")
	writeDepMod(t, storeMod("Provider"), "Provider", "Test.Provided")
	writeDepMod(t, storeMod("Off"), "Off", "Test.Off")
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", depUniqueID, "Test.Off", "Test.NoPage")
	setOrderedModList(t, d, "Default", enabled("Needy"), enabled("Provider"), disabled("Off"))

	results, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{"Test.Provided", "test.off", "Unknown.Thing", "Test.NoPage", depUniqueID, "DEP.CORE", " "})
	if err != nil {
		t.Fatalf("FetchModDependencies: %v", err)
	}
	got := map[string]dto.DependencyFetchResult{}
	for _, result := range results {
		got[result.UniqueID] = result
	}
	want := map[string]struct {
		outcome dto.FetchOutcome
		reason  string
	}{
		"Test.Provided": {dto.FetchOutcomeAlreadyPresent, "provided: Provider"},
		"test.off":      {dto.FetchOutcomeAlreadyPresent, "disabled: Off"},
		"Unknown.Thing": {dto.FetchOutcomeUnresolved, fetchReasonNotMissing},
		"Test.NoPage":   {dto.FetchOutcomeUnresolved, fetchReasonNoNexusPage},
		depUniqueID:     {dto.FetchOutcomeOpenURL, fetchReasonNotPremium},
	}
	if len(results) != len(want) {
		t.Fatalf("results = %+v, want %d deduplicated results", results, len(want))
	}
	for id, expect := range want {
		if got[id].Outcome != expect.outcome || got[id].Reason != expect.reason {
			t.Errorf("%s = %+v, want outcome %d reason %q", id, got[id], expect.outcome, expect.reason)
		}
		if (got[id].BatchID != "") != (expect.outcome == dto.FetchOutcomeOpenURL) {
			t.Errorf("%s batch = %q, want a batch only for registered requests", id, got[id].BatchID)
		}
	}
	if api.requestCount() != 1 {
		t.Fatalf("smapi.io lookups = %d, want one for the uncached IDs", api.requestCount())
	}
	if _, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{depUniqueID, "Test.NoPage"}); err != nil {
		t.Fatal(err)
	}
	if api.requestCount() != 1 {
		t.Fatalf("smapi.io lookups after a cached fetch = %d, want still one", api.requestCount())
	}
	if batches := readDepRequests(t).Batches; len(batches) != 2 || len(batches[0].Entries) != 1 {
		t.Fatalf("batches = %+v, want one single-entry batch per fetch", batches)
	}
}

func TestFailedPremiumDownloadFailsItsRequests(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	d.svc.archives.managerHooks().OnDownloadProgress(download.DownloadSnapshot{ID: "dl-1", GameID: depsGame, Status: download.StatusFailed, Error: "HTTP 500"})
	deadline := time.Now().Add(10 * time.Second)
	for onlyDepEntry(t, depUniqueID).State != depStateFailed {
		if time.Now().After(deadline) {
			t.Fatalf("entry = %+v, want failed after the download failed", onlyDepEntry(t, depUniqueID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if entry := onlyDepEntry(t, depUniqueID); !strings.Contains(entry.Detail, "HTTP 500") {
		t.Fatalf("entry detail = %q, want the download error", entry.Detail)
	}
	abs, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if d.svc.modDeps.consumeLandedArchive(depsGame, "dl-other", abs, sidecar) {
		t.Fatal("a failed request consumed another download of its file")
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateFailed {
		t.Fatalf("entry after another download landed = %+v, want still failed", entry)
	}
	if !d.svc.modDeps.consumeLandedArchive(depsGame, "dl-1", abs, sidecar) {
		t.Fatal("a retried download of the failed request was not consumed")
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after the retried download landed = %+v, want it revived to enable_pending", entry)
	}
}

func TestDependencyRequestFileRecoversFromCorruption(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	writeFileContent(t, dependencyRequestsPath(depsGame), "{not json")
	if report := depReport(t, d, "Default"); len(report.PendingEnables) != 0 {
		t.Fatalf("pending enables from a corrupt file = %+v", report.PendingEnables)
	}
	if _, err := os.Stat(dependencyRequestsPath(depsGame) + ".corrupt"); err != nil {
		t.Fatalf("corrupt file was not moved aside: %v", err)
	}
	fetchOne(t, d, "Default", depUniqueID)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload {
		t.Fatalf("entry = %+v, want a fresh request", entry)
	}
	writeFileContent(t, dependencyRequestsPath(depsGame), `{"schema_version":2,"batches":[]}`)
	if _, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{depUniqueID}); err == nil {
		t.Fatal("a newer schema was overwritten")
	}
}

func TestStartInstallPublishesInstallCompleted(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	installs := subscribeInstalls(t, d)
	rel := filepath.Join("manual", "sample.zip")
	writeManifestArchive(t, filepath.Join(config.DownloadsDir(depsGame), rel))
	folder, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ArchiveRelPath: rel, Mode: dto.InstallAsNewMod, TargetMod: "Sample"})
	if err != nil {
		t.Fatalf("StartInstall: %v", err)
	}
	completed := waitCompleted(t, installs)
	if !reflect.DeepEqual(completed, dto.InstallCompletedResult{GameID: depsGame, ModName: folder, ArchiveRelPath: rel}) {
		t.Fatalf("InstallCompleted = %+v, want the registered mod without a batch", completed)
	}
	if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ArchiveRelPath: rel, Mode: dto.InstallAsNewMod, TargetMod: "Sample"}); err == nil {
		t.Fatal("a colliding install succeeded")
	}
	for {
		select {
		case evt := <-installs:
			if evt.Completed != nil {
				t.Fatalf("a failed install published %+v", evt.Completed)
			}
		default:
			return
		}
	}
}

func TestConcurrentLandingsConsumeABrowserRequestOnce(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	fetchOne(t, d, "Default", depUniqueID)
	type landing struct {
		path    string
		sidecar download.ArchiveSidecar
	}
	var landings []landing
	for i, name := range []string{"Dep Core A", "Dep Core B", "Dep Core C"} {
		abs, sidecar := writeLandedArchive(t, depNexusID, 800+i, name, depArchiveFiles())
		landings = append(landings, landing{abs, sidecar})
	}
	var wg sync.WaitGroup
	consumed := make([]bool, len(landings))
	for i, l := range landings {
		wg.Add(1)
		go func(i int, l landing) {
			defer wg.Done()
			consumed[i] = d.svc.modDeps.consumeLandedArchive(depsGame, fmt.Sprintf("dl-%d", i), l.path, l.sidecar)
		}(i, l)
	}
	wg.Wait()
	count := 0
	for _, ok := range consumed {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("consumed = %v, want exactly one landing to consume the request", consumed)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending {
		t.Fatalf("entry = %+v, want enable_pending", entry)
	}
}

func TestConcurrentFetchesReportsAndAcksKeepRequestsConsistent(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	const fetches = 6
	var wg sync.WaitGroup
	errs := make(chan error, 3*fetches)
	batches := make(chan string, fetches)
	for i := 0; i < fetches; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			results, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{depUniqueID})
			if err != nil {
				errs <- err
				return
			}
			batches <- results[0].BatchID
		}()
		go func() {
			defer wg.Done()
			if _, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", false, false); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := d.AckDependencyEnable(context.Background(), depsGame, "dep-unknown", nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(batches)
	for err := range errs {
		t.Fatal(err)
	}
	if entries := depEntries(t, depUniqueID); len(entries) != fetches {
		t.Fatalf("entries = %d, want one per concurrent fetch", len(entries))
	}

	landArchive(t, d, "dl-browser", depNexusID, 777, "Dep Core", depArchiveFiles())
	waitDepState(t, depUniqueID, depStateEnablePending)
	var ackWG sync.WaitGroup
	var mu sync.Mutex
	total := 0
	ackErrs := make(chan error, 3*fetches)
	for batchID := range batches {
		ackWG.Add(3)
		go func(batchID string) {
			defer ackWG.Done()
			acked, err := d.AckDependencyEnable(context.Background(), depsGame, batchID, []string{"DEP.CORE"})
			if err != nil {
				ackErrs <- err
				return
			}
			mu.Lock()
			total += acked
			mu.Unlock()
		}(batchID)
		go func() {
			defer ackWG.Done()
			results, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{depUniqueID})
			if err != nil {
				ackErrs <- err
				return
			}
			if results[0].Outcome != dto.FetchOutcomeAlreadyPresent {
				ackErrs <- fmt.Errorf("fetch after the install = %+v, want already present", results[0])
			}
		}()
		go func() {
			defer ackWG.Done()
			if _, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", false, false); err != nil {
				ackErrs <- err
			}
		}()
	}
	ackWG.Wait()
	close(ackErrs)
	for err := range ackErrs {
		t.Fatal(err)
	}
	if total != fetches {
		t.Fatalf("acknowledged = %d, want %d", total, fetches)
	}
	waitDepState(t, depUniqueID, depStateDone)
	if report := depReport(t, d, "Default"); len(report.PendingEnables) != 0 {
		t.Fatalf("pending enables after every ack = %+v", report.PendingEnables)
	}
}

func TestLandedMatchRules(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Second)
	sidecar := download.ArchiveSidecar{ModID: depNexusID, FileID: depFileID}
	cases := []struct {
		name  string
		entry depEntry
		dlID  string
		want  bool
	}{
		{"premium by download id", depEntry{State: depStateDownloading, NexusModID: 1, FileID: depFileID, DownloadID: "dl-1"}, "dl-1", true},
		{"premium by nexus file", depEntry{State: depStateDownloading, NexusModID: depNexusID, FileID: depFileID}, "dl-other", true},
		{"premium other file", depEntry{State: depStateDownloading, NexusModID: depNexusID, FileID: 1, DownloadID: "dl-1"}, "dl-1", false},
		{"premium without file", depEntry{State: depStateDownloading, NexusModID: depNexusID, DownloadID: "dl-1"}, "dl-1", false},
		{"browser same page", depEntry{State: depStateAwaitingDownload, NexusModID: depNexusID, ExpiresAt: &future}, "dl-x", true},
		{"browser other page", depEntry{State: depStateAwaitingDownload, NexusModID: 1, ExpiresAt: &future}, "dl-x", false},
		{"browser expired", depEntry{State: depStateAwaitingDownload, NexusModID: depNexusID, ExpiresAt: &past}, "dl-x", false},
		{"installing", depEntry{State: depStateInstalling, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1"}, "dl-1", false},
		{"failed mismatch", depEntry{State: depStateFailed, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", Detail: "archive_mismatch: x"}, "dl-1", false},
		{"failed download retried", depEntry{State: depStateFailed, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", Detail: "download_failed: HTTP 500"}, "dl-1", true},
		{"failed download other id", depEntry{State: depStateFailed, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", Detail: "download_failed: HTTP 500"}, "dl-2", false},
		{"failed download other file", depEntry{State: depStateFailed, NexusModID: depNexusID, FileID: 1, DownloadID: "dl-1", Detail: "download_failed: HTTP 500"}, "dl-1", false},
		{"enable pending", depEntry{State: depStateEnablePending, NexusModID: depNexusID}, "dl-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := landedMatch(tc.entry, tc.dlID, sidecar, now); got != tc.want {
				t.Fatalf("landedMatch = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestDependencyRequestsExpireAndPrune(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Minute)
	later := now.Add(time.Hour)
	doc := &depRequestsDoc{SchemaVersion: 1, Batches: []depBatch{
		{BatchID: "expiring", CreatedAt: now.Add(-25 * time.Hour), Entries: []depEntry{{UniqueID: "a", State: depStateAwaitingDownload, ExpiresAt: &deadline}}},
		{BatchID: "waiting", CreatedAt: now.Add(-time.Hour), Entries: []depEntry{{UniqueID: "b", State: depStateAwaitingDownload, ExpiresAt: &later}}},
		{BatchID: "old-finished", CreatedAt: now.Add(-8 * 24 * time.Hour), Entries: []depEntry{{UniqueID: "c", State: depStateDone}}},
		{BatchID: "old-pending", CreatedAt: now.Add(-8 * 24 * time.Hour), Entries: []depEntry{{UniqueID: "d", State: depStateEnablePending}}},
		{BatchID: "ancient", CreatedAt: now.Add(-31 * 24 * time.Hour), Entries: []depEntry{{UniqueID: "e", State: depStateEnablePending}}},
	}}
	if !expireDependencyRequests(doc, now) {
		t.Fatal("expireDependencyRequests reported no change")
	}
	if doc.Batches[0].Entries[0].State != depStateExpired || doc.Batches[1].Entries[0].State != depStateAwaitingDownload {
		t.Fatalf("states after expiry = %+v", doc.Batches)
	}
	pruneDependencyRequests(doc, now)
	var kept []string
	for _, batch := range doc.Batches {
		kept = append(kept, batch.BatchID)
	}
	if want := []string{"expiring", "waiting", "old-pending"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept batches = %v, want %v", kept, want)
	}
}
