package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

type downloadsFakeNexus struct {
	key            string
	entered        chan<- string
	release        <-chan struct{}
	resolveEntered chan<- struct{}
	resolveRelease <-chan struct{}
}

func (n *downloadsFakeNexus) ValidateAPIKey(context.Context) error { return nil }
func (n *downloadsFakeNexus) ResolveDownloadURL(*download.NXMLink) (string, error) {
	if n.resolveEntered != nil {
		select {
		case n.resolveEntered <- struct{}{}:
		default:
		}
		<-n.resolveRelease
	}
	return "", errors.New("downloads are disabled in this test")
}
func (n *downloadsFakeNexus) GetModInfo(string, int) (*download.NexusModInfo, error) {
	if n.entered != nil {
		n.entered <- n.key
		<-n.release
	}
	return &download.NexusModInfo{Name: n.key}, nil
}
func (n *downloadsFakeNexus) GetFileDetails(string, int, int) (*download.NexusFileDetails, error) {
	return &download.NexusFileDetails{Name: n.key}, nil
}

// downloadsDaemon creates a recovered daemon without any real user directories or launchers.
func downloadsDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, newSkyrimGames(t))
	t.Cleanup(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.downloadMgr != nil {
			d.downloadMgr.Stop()
		}
	})
	return d
}

// downloadsInstallFakeNexus substitutes an in-memory Nexus client until this test and its workers finish.
func downloadsInstallFakeNexus(t *testing.T, build func(string) nexusDownloadClient) {
	t.Helper()
	old := newNexusDownloadClient
	newNexusDownloadClient = build
	t.Cleanup(func() { newNexusDownloadClient = old })
}

// downloadsSetKey validates a test key using the in-memory client and installs its download manager.
func downloadsSetKey(t *testing.T, d *Daemon, key string) {
	t.Helper()
	result, err := d.SetNexusAPIKey(context.Background(), key)
	if err != nil || result == nil || !result.Valid {
		t.Fatalf("SetNexusAPIKey(%q) = %+v, %v", key, result, err)
	}
}

// downloadsWaitForActiveGameOverride waits until StartDownload is blocked resolving the active-game override.
func downloadsWaitForActiveGameOverride(t *testing.T) {
	t.Helper()
	stack := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.Stack(stack, true)
		if strings.Contains(string(stack[:n]), "(*ArchiveService).resolveActiveGameOverride") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("StartDownload did not block resolving the active-game override")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// downloadsConfigureRace runs bounded real ConfigureGame writes against a reader and joins both workers.
func downloadsConfigureRace(t *testing.T, d *Daemon, read func() error) {
	t.Helper()
	install := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorkers := func() { stopOnce.Do(func() { close(stop) }) }
	firstWrite := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for i := 0; i < 16; i++ {
			select {
			case <-stop:
				writerDone <- nil
				return
			default:
			}
			if err := d.ConfigureGame("downloads-scratch", fmt.Sprintf("Scratch %d", i), 0, install, "Data"); err != nil {
				if i == 0 {
					close(firstWrite)
				}
				writerDone <- err
				return
			}
			if i == 0 {
				close(firstWrite)
			}
		}
		writerDone <- nil
	}()
	select {
	case <-firstWrite:
	case <-time.After(5 * time.Second):
		stopWorkers()
		select {
		case <-writerDone:
		case <-time.After(5 * time.Second):
		}
		t.Fatal("ConfigureGame did not complete its first write")
	}
	readerDone := make(chan error, 1)
	go func() {
		for i := 0; i < 120; i++ {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if err := read(); err != nil {
				readerDone <- err
				return
			}
		}
		readerDone <- nil
	}()
	var readerErr error
	select {
	case readerErr = <-readerDone:
	case <-time.After(30 * time.Second):
		stopWorkers()
		select {
		case <-readerDone:
		case <-time.After(5 * time.Second):
		}
		select {
		case <-writerDone:
		case <-time.After(5 * time.Second):
		}
		t.Fatal("reader did not finish within 30 seconds")
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("ConfigureGame: %v", err)
		}
	case <-time.After(30 * time.Second):
		stopWorkers()
		select {
		case <-writerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ConfigureGame did not stop after cancellation")
		}
		t.Fatal("ConfigureGame did not finish within 30 seconds")
	}
	if readerErr != nil {
		t.Fatal(readerErr)
	}
}

func TestSharedConfigReadsConcurrentConfigureGame(t *testing.T) {
	readers := []struct {
		name string
		read func(*Daemon) error
	}{
		{"ListArchives", func(d *Daemon) error { _, err := d.ListArchives("skyrimse"); return err }},
		{"RemoveArchive", func(d *Daemon) error { return d.RemoveArchive("skyrimse", "absent.zip") }},
		{"SetArchiveHidden", func(d *Daemon) error {
			if err := d.SetArchiveHidden("skyrimse", "absent.zip", true); err == nil || err.Error() != "archive \"absent.zip\" not in index" {
				return fmt.Errorf("SetArchiveHidden missing archive: %v", err)
			}
			return nil
		}},
		{"SetArchivesHiddenBulk", func(d *Daemon) error {
			_, err := d.SetArchivesHiddenBulk("skyrimse", true, dto.BulkHideAll)
			return err
		}},
		{"RefreshArchiveMetadata", func(d *Daemon) error {
			_, err := d.RefreshArchiveMetadata("skyrimse", "absent.zip")
			if err == nil || err.Error() != "nexus API key required — paste one in Tools → Settings" {
				return fmt.Errorf("refresh without a key: %v", err)
			}
			return nil
		}},
		{"StreamArchiveEvents", func(d *Daemon) error {
			ctx, cancel := context.WithCancel(context.Background())
			_, err := d.StreamArchiveEvents(ctx, "skyrimse")
			cancel()
			return err
		}},
		{"RegisterManualInstall", func(d *Daemon) error {
			_, err := d.RegisterManualInstall("skyrimse", "", "")
			if err == nil || err.Error() != "mod_name required" {
				return fmt.Errorf("manual install without mod: %v", err)
			}
			return nil
		}},
		{"ListOverwriteFiles", func(d *Daemon) error { _, _, err := d.ListOverwriteFiles("skyrimse"); return err }},
		{"ExtractOverwriteToMod", func(d *Daemon) error {
			_, err := d.ExtractOverwriteToMod("skyrimse", "", nil, true)
			if err == nil || err.Error() != "mod_name required" {
				return fmt.Errorf("extract without mod: %v", err)
			}
			return nil
		}},
		{"GetGameSettings", func(d *Daemon) error { _, err := d.GetGameSettings("skyrimse"); return err }},
		{"SetGameSettings", func(d *Daemon) error { _, err := d.SetGameSettings("skyrimse", false); return err }},
	}
	for _, tc := range readers {
		t.Run(tc.name, func(t *testing.T) {
			d := downloadsDaemon(t)
			downloadsConfigureRace(t, d, func() error { return tc.read(d) })
		})
	}
}

func TestDownloadLedgerScansConcurrentConfigureGame(t *testing.T) {
	d := downloadsDaemon(t)
	manager := download.NewManager(&downloadsFakeNexus{}, 1, download.ManagerHooks{})
	t.Cleanup(manager.Stop)
	d.mu.Lock()
	d.downloadMgr = manager
	d.mu.Unlock()
	for _, tc := range []struct {
		name string
		read func() error
	}{
		{"Cancel", func() error {
			err := d.CancelDownload("absent")
			var notFound *download.DownloadNotFoundError
			if !errors.As(err, &notFound) {
				return fmt.Errorf("CancelDownload: %v", err)
			}
			return nil
		}},
		{"Retry", func() error {
			_, err := d.RetryDownload("absent")
			var notFound *download.DownloadNotFoundError
			if !errors.As(err, &notFound) {
				return fmt.Errorf("RetryDownload: %v", err)
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { downloadsConfigureRace(t, d, tc.read) })
	}
}

func TestDownloadLedgerScansIncludeGamesConfiguredAfterManagerCreation(t *testing.T) {
	d := downloadsDaemon(t)
	manager := download.NewManager(&downloadsFakeNexus{}, 1, download.ManagerHooks{})
	t.Cleanup(manager.Stop)
	d.mu.Lock()
	d.downloadMgr = manager
	d.mu.Unlock()
	install := filepath.Join(t.TempDir(), "scratch")
	if err := d.ConfigureGame("downloads-later", "Later", 0, install, "Data"); err != nil {
		t.Fatal(err)
	}
	uri := "nxm://skyrimspecialedition/mods/1/files/1?key=test&expires=1"
	for _, id := range []string{"downloads-expired", "downloads-cancel", "downloads-rehydrate"} {
		if err := download.UpsertLedgerEntry(download.LedgerEntry{ID: id, GameID: "downloads-later", NXMURI: uri, Status: download.LedgerQueued}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := d.RetryDownload("downloads-expired")
	var expired *download.NXMExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("RetryDownload = %v, want expired URI", err)
	}
	if err := d.CancelDownload("downloads-cancel"); err != nil {
		t.Fatal(err)
	}
	manager.RehydrateLedger(d.downloadStateSnapshot().gameIDs)
	entries, err := download.LoadLedger("downloads-later")
	if err != nil {
		t.Fatal(err)
	}
	status := make(map[string]download.LedgerStatus, len(entries))
	for _, entry := range entries {
		status[entry.ID] = entry.Status
	}
	if status["downloads-cancel"] != download.LedgerCancelled || status["downloads-rehydrate"] != download.LedgerFailed {
		t.Fatalf("later game ledger statuses = %v", status)
	}
}

func TestArchiveRefreshConcurrentSetNexusAPIKey(t *testing.T) {
	d := downloadsDaemon(t)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient { return &downloadsFakeNexus{key: key} })
	downloadsSetKey(t, d, "initial")
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "refresh.zip")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "refresh.zip", ModID: 1, FileID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := download.SaveSidecar(archive, download.ArchiveSidecar{ModID: 1, FileID: 2, GameDomain: "skyrimspecialedition"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			if _, err := d.SetNexusAPIKey(context.Background(), fmt.Sprintf("key-%d", i)); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	var refreshErr error
	for i := 0; i < 50; i++ {
		key, manager := d.svc.modDeps.nexusAccess()
		if key == "" || manager == nil {
			refreshErr = fmt.Errorf("dependency Nexus snapshot lost the key or manager")
			break
		}
		row, err := d.RefreshArchiveMetadata("skyrimse", "refresh.zip")
		if err != nil {
			refreshErr = err
			break
		}
		if row.ModName != row.FileName {
			refreshErr = fmt.Errorf("mixed client keys in row: %+v", row)
			break
		}
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if refreshErr != nil {
		t.Fatal(refreshErr)
	}
	d.stopDownloadManager()
}

func TestArchiveRefreshReleasesSessionLockBeforeRemoteWork(t *testing.T) {
	d := downloadsDaemon(t)
	entered := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient {
		return &downloadsFakeNexus{key: key, entered: entered, release: release}
	})
	downloadsSetKey(t, d, "before")
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "blocked.zip")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "blocked.zip", ModID: 1, FileID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := download.SaveSidecar(archive, download.ArchiveSidecar{ModID: 1, FileID: 2, GameDomain: "skyrimspecialedition"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() {
		row, err := d.RefreshArchiveMetadata("skyrimse", "blocked.zip")
		if err == nil && (row.ModName != "before" || row.FileName != "before") {
			err = fmt.Errorf("refresh mixed client keys: %+v", row)
		}
		refreshed <- err
	}()
	select {
	case key := <-entered:
		if key != "before" {
			t.Errorf("blocked client key = %q", key)
		}
	case <-time.After(5 * time.Second):
		once.Do(func() { close(release) })
		<-refreshed
		t.Fatal("refresh never entered remote call")
	}
	writers := make(chan error, 1)
	install := filepath.Join(t.TempDir(), "new")
	go func() {
		if err := d.ConfigureGame("downloads-new", "New", 0, install, "Data"); err != nil {
			writers <- err
			return
		}
		_, err := d.SetNexusAPIKey(context.Background(), "after")
		writers <- err
	}()
	writerFinished := false
	select {
	case err := <-writers:
		writerFinished = true
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("settings writers waited for a remote refresh")
	}
	once.Do(func() { close(release) })
	if err := <-refreshed; err != nil {
		t.Error(err)
	}
	if !writerFinished {
		if err := <-writers; err != nil {
			t.Error(err)
		}
	}
	d.stopDownloadManager()
}

func TestDownloadManagerReplacementReaders(t *testing.T) {
	d := downloadsDaemon(t)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient { return &downloadsFakeNexus{key: key} })
	downloadsSetKey(t, d, "initial")
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "missing.zip"}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			if _, err := d.SetNexusAPIKey(context.Background(), fmt.Sprintf("replace-%d", i)); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	var readErr error
	for i := 0; i < 100; i++ {
		if _, _, err := d.StartDownload("invalid"); err == nil {
			readErr = fmt.Errorf("malformed NXM accepted")
			break
		}
		if err := d.CancelDownload("missing"); err == nil {
			readErr = fmt.Errorf("missing download cancelled")
			break
		}
		if _, err := d.RetryDownload("missing"); err == nil {
			readErr = fmt.Errorf("missing download retried")
			break
		}
		if _, err := d.ListArchives("skyrimse"); err != nil {
			readErr = err
			break
		}
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	d.stopDownloadManager()
}

func TestDownloadStartsOnManagerCurrentAfterActiveGameResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	games := newSkyrimGames(t)
	games["linked"] = config.GameConfig{LinkedFromGameID: "skyrimse", DataSubpath: "Data"}
	d := newIsolatedDaemon(t, games)
	if err := d.SetActiveGame("linked"); err != nil {
		t.Fatal(err)
	}
	oldManager := download.NewManager(&downloadsFakeNexus{}, 1, d.svc.archives.managerHooks())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePipeline := func() { releaseOnce.Do(func() { close(release) }) }
	newManager := download.NewManager(&downloadsFakeNexus{resolveEntered: entered, resolveRelease: release}, 1, d.svc.archives.managerHooks())
	t.Cleanup(oldManager.Stop)
	t.Cleanup(newManager.Stop)
	t.Cleanup(releasePipeline)
	d.mu.Lock()
	d.downloadMgr = oldManager
	d.mu.Unlock()
	d.activeGameIDMu.Lock()
	finished := make(chan struct {
		id  string
		err error
	}, 1)
	uri := fmt.Sprintf("nxm://skyrimspecialedition/mods/1/files/1?key=test&expires=%d", time.Now().Add(time.Hour).Unix())
	go func() {
		id, _, err := d.StartDownload(uri)
		finished <- struct {
			id  string
			err error
		}{id: id, err: err}
	}()
	downloadsWaitForActiveGameOverride(t)
	d.mu.Lock()
	d.activeGameIDMu.Unlock()
	d.downloadMgr = newManager
	oldManager.Stop()
	d.mu.Unlock()
	var result struct {
		id  string
		err error
	}
	select {
	case result = <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartDownload did not resume after replacing the manager")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		releasePipeline()
		t.Fatal("download was not enqueued on the replacement manager")
	}
	if got := oldManager.ActiveDownloadIDByNXM(uri); got != "" {
		t.Fatalf("old manager retained download %q", got)
	}
	if got := newManager.ActiveDownloadIDByNXM(uri); got != result.id {
		t.Fatalf("new manager download = %q, want %q", got, result.id)
	}
	releasePipeline()
	deadline := time.After(5 * time.Second)
	for newManager.ActiveDownloadIDByNXM(uri) != "" {
		select {
		case <-deadline:
			t.Fatal("replacement download did not finish after release")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestProtectedScopeReadersConcurrentConfigureGame(t *testing.T) {
	d := downloadsDaemon(t)
	if err := d.SetActiveGame("skyrimse"); err != nil {
		t.Fatal(err)
	}
	downloadsConfigureRace(t, d, func() error {
		if got := d.svc.archives.resolveActiveGameOverride("nxm://fallout4/mods/1/files/2"); got != "" {
			return fmt.Errorf("unexpected game override %q", got)
		}
		return nil
	})
}

func TestConfigSaveConcurrentSettingsWriters(t *testing.T) {
	d := downloadsDaemon(t)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient { return &downloadsFakeNexus{key: key} })
	install := filepath.Join(t.TempDir(), "scratch")
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := d.ConfigureGame("downloads-save", fmt.Sprintf("Scratch %d", i), 0, install, "Data"); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	protonFinished := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := d.SetPreferredProton(fmt.Sprintf("proton-%d", i)); err != nil {
				protonFinished <- err
				return
			}
		}
		protonFinished <- nil
	}()
	var keyErr error
	for i := 0; i < 20; i++ {
		if _, err := d.SetNexusAPIKey(context.Background(), fmt.Sprintf("final-%d", i)); err != nil {
			keyErr = err
			break
		}
	}
	gameErr, protonErr := <-finished, <-protonFinished
	if gameErr != nil {
		t.Fatal(gameErr)
	}
	if protonErr != nil {
		t.Fatal(protonErr)
	}
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NexusAPIKey != "final-19" || cfg.Games["downloads-save"].Name != "Scratch 19" || cfg.PreferredProton != "proton-19" {
		t.Fatalf("persisted settings = %+v", cfg)
	}
	d.stopDownloadManager()
}

func TestLandedInstallConfigReadsConcurrentConfigureGame(t *testing.T) {
	d := downloadsDaemon(t)
	if err := config.SaveGameSettings("skyrimse", config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	snap := download.DownloadSnapshot{GameID: "skyrimse"}
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "missing.zip")
	downloadsConfigureRace(t, d, func() error {
		d.svc.archives.handleLandedArchive(snap, archive, download.ArchiveSidecar{ModName: "Absent"})
		return nil
	})
}
