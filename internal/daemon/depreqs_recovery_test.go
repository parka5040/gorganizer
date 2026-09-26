package daemon

import (
	"context"
	"errors"
	"os"
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

// waitDepState polls until every entry for uniqueID reaches state, failing after a timeout.
func waitDepState(t *testing.T, uniqueID, state string) []depEntry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries := depEntries(t, uniqueID)
		settled := len(entries) > 0
		for _, entry := range entries {
			if entry.State != state {
				settled = false
			}
		}
		if settled {
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("entries for %s = %+v, want all %s", uniqueID, entries, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertConvergesOnARequeuedDownload fetches again, proving the dead download is not reused, then lands the fresh one and expects every request installed.
func assertConvergesOnARequeuedDownload(t *testing.T, d *Daemon, downloader *fakeDepDownloader, deadID string) {
	t.Helper()
	again := fetchOne(t, d, "Default", depUniqueID)
	if again.Outcome != dto.FetchOutcomeQueued || again.DownloadID == "" || again.DownloadID == deadID || again.Reason != "" {
		t.Fatalf("second fetch = %+v, want a freshly queued download instead of dead %s", again, deadID)
	}
	if got := len(downloader.queued()); got != 2 {
		t.Fatalf("queued downloads = %d, want the dead one re-queued once", got)
	}
	for _, entry := range depEntries(t, depUniqueID) {
		if entry.State != depStateDownloading || entry.DownloadID != again.DownloadID {
			t.Fatalf("entry = %+v, want every request re-pointed to %s", entry, again.DownloadID)
		}
	}
	downloader.forget(again.DownloadID)
	landArchive(t, d, again.DownloadID, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	for _, entry := range depEntries(t, depUniqueID) {
		if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
			t.Fatalf("entry after the fresh download landed = %+v, want enable_pending", entry)
		}
	}
}

func TestFetchRequeuesADownloadTheManagerNoLongerKnows(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	first := fetchOne(t, d, "Default", depUniqueID)
	downloader.forget(first.DownloadID)
	assertConvergesOnARequeuedDownload(t, d, downloader, first.DownloadID)
}

func TestRestartConsumesAnArchiveThatLandedDuringAnInterruptedContentCheck(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	first := fetchOne(t, d, "Default", depUniqueID)
	writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())

	restarted := restartWithoutNexusKey(t, d)
	entry := onlyDepEntry(t, depUniqueID)
	if entry.State != depStateEnablePending || entry.ModName != "Dep Core" || entry.DownloadID != first.DownloadID {
		t.Fatalf("entry after recovery = %+v, want the landed archive consumed", entry)
	}
	if state := modListState(t, restarted, "Default"); state["Dep Core"] {
		t.Fatalf("modlist = %v, want the recovered dependency appended disabled", state)
	}
	if report := depReport(t, restarted, "Default"); len(report.PendingEnables) != 1 || report.PendingEnables[0].BatchID != first.BatchID {
		t.Fatalf("pending enables = %+v, want the original batch", report.PendingEnables)
	}
}

func TestRestartAdoptsAnArchiveAlreadyInstalledOutsideTheRequest(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ArchiveRelPath: relFromDownloads(depsGame, abs), Mode: dto.InstallAsNewMod, TargetMod: "Manual Dep"}); err != nil {
		t.Fatalf("StartInstall: %v", err)
	}

	restartWithoutNexusKey(t, d)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Manual Dep" {
		t.Fatalf("entry after recovery = %+v, want the existing install adopted", entry)
	}
	if _, err := os.Stat(storeMod("Dep Core")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery installed the archive a second time: %v", err)
	}
}

func TestLostFailureEventIsSettledFromTheLedger(t *testing.T) {
	t.Run("next fetch re-queues", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		queueLedgerDownload(t, first.DownloadID, download.LedgerFailed, "HTTP 500")
		downloader.forget(first.DownloadID)
		assertConvergesOnARequeuedDownload(t, d, downloader, first.DownloadID)
	})
	t.Run("restart fails and a retry revives", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		queueLedgerDownload(t, first.DownloadID, download.LedgerFailed, "HTTP 500")
		restarted := restartWithoutNexusKey(t, d)
		entry := onlyDepEntry(t, depUniqueID)
		if entry.State != depStateFailed || entry.Detail != "download_failed: HTTP 500" {
			t.Fatalf("entry after recovery = %+v, want the ledger failure recorded", entry)
		}
		landArchive(t, restarted, first.DownloadID, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending {
			t.Fatalf("entry after the retried download landed = %+v, want enable_pending", entry)
		}
	})
	t.Run("restart without any trace", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		fetchOne(t, d, "Default", depUniqueID)
		restartWithoutNexusKey(t, d)
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateFailed || !strings.HasPrefix(entry.Detail, detailDownloadLost) {
			t.Fatalf("entry after recovery = %+v, want download_lost", entry)
		}
	})
	t.Run("restart adopts a pending download of the same file", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		fetchOne(t, d, "Default", depUniqueID)
		queueLedgerDownload(t, "dl-manual", download.LedgerQueued, "")
		restartWithoutNexusKey(t, d)
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDownloading || entry.DownloadID != "dl-manual" {
			t.Fatalf("entry after recovery = %+v, want the pending ledger download adopted", entry)
		}
	})
}

func TestFailureThatRacesAheadOfTheRecordedIDFailsTheRequest(t *testing.T) {
	cases := []struct {
		name string
		fail func(d *Daemon, id string)
	}{
		{name: "failure event", fail: func(d *Daemon, id string) {
			d.svc.archives.managerHooks().OnDownloadProgress(download.DownloadSnapshot{ID: id, GameID: depsGame, Status: download.StatusFailed, Error: "HTTP 404"})
		}},
		{name: "recorded failure", fail: func(d *Daemon, id string) {
			d.svc.modDeps.failDownload(depsGame, id, "HTTP 404")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
			downloader.onStart = func(id string) {
				downloader.forget(id)
				tc.fail(d, id)
			}
			first := fetchOne(t, d, "Default", depUniqueID)
			if first.Outcome != dto.FetchOutcomeQueued {
				t.Fatalf("result = %+v, want queued", first)
			}
			entry := waitDepState(t, depUniqueID, depStateFailed)[0]
			if entry.DownloadID != first.DownloadID || entry.Detail != "download_failed: HTTP 404" {
				t.Fatalf("entry = %+v, want the early failure applied when its ID was recorded", entry)
			}
			downloader.onStart = nil
			again := fetchOne(t, d, "Default", depUniqueID)
			if again.Outcome != dto.FetchOutcomeQueued || again.DownloadID == first.DownloadID || again.Reason != "" {
				t.Fatalf("second fetch = %+v, want a fresh download", again)
			}
		})
	}
}

func TestUnreadableRequestsDuringALandingAreReconciled(t *testing.T) {
	failingLoads := func(d *Daemon, count *int, remaining int) {
		d.svc.modDeps.loadRequests = func(gameID string) (*depRequestsDoc, error) {
			if remaining > 0 {
				remaining--
				*count++
				return nil, errors.New("input/output error")
			}
			return loadDependencyRequests(gameID)
		}
	}
	t.Run("next fetch re-queues", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		failed := 0
		failingLoads(d, &failed, 2)
		abs, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		if d.svc.modDeps.consumeLandedArchive(depsGame, first.DownloadID, abs, sidecar) {
			t.Fatal("an unreadable request file consumed the landing")
		}
		if failed != 2 {
			t.Fatalf("failed reads = %d, want the read retried once", failed)
		}
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDownloading {
			t.Fatalf("entry = %+v, want it left for reconciliation", entry)
		}
		assertConvergesOnARequeuedDownload(t, d, downloader, first.DownloadID)
	})
	t.Run("restart consumes the landed archive", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		failed := 0
		failingLoads(d, &failed, 2)
		landArchive(t, d, first.DownloadID, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		restartWithoutNexusKey(t, d)
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
			t.Fatalf("entry after recovery = %+v, want the landed archive consumed", entry)
		}
	})
}

func TestReportListsRecentFailedAndExpiredRequests(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	clock := &depClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	d.now = clock.Now
	if _, err := d.CreateProfile(depsGame, "Alt"); err != nil {
		t.Fatal(err)
	}
	old := clock.Now().Add(-8 * 24 * time.Hour)
	recent := clock.Now().Add(-time.Hour)
	older := clock.Now().Add(-2 * time.Hour)
	err := d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
		doc.Batches = append(doc.Batches,
			depBatch{BatchID: "dep-a", Profile: "Default", CreatedAt: now, Entries: []depEntry{
				{UniqueID: "Fail.New", State: depStateFailed, Detail: "archive_mismatch: x", UpdatedAt: &recent},
				{UniqueID: "Fail.Old", State: depStateFailed, Detail: "download_failed: y", UpdatedAt: &old},
				{UniqueID: "Still.Waiting", State: depStateDownloading, FileID: 1, DownloadID: "dl-9"},
			}},
			depBatch{BatchID: "dep-b", Profile: "Default", CreatedAt: now, Entries: []depEntry{
				{UniqueID: "Gone.Expired", State: depStateExpired, Detail: fetchReasonNotPremium, UpdatedAt: &older},
			}},
			depBatch{BatchID: "dep-c", Profile: "Alt", CreatedAt: now, Entries: []depEntry{
				{UniqueID: "Other.Profile", State: depStateFailed, Detail: "install_failed: z", UpdatedAt: &recent},
			}},
		)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	report := depReport(t, d, "Default")
	want := []dto.DependencyRequestIssueResult{
		{UniqueID: "Fail.New", BatchID: "dep-a", State: depStateFailed, Detail: "archive_mismatch: x", UpdatedAt: recent},
		{UniqueID: "Gone.Expired", BatchID: "dep-b", State: depStateExpired, Detail: fetchReasonNotPremium, UpdatedAt: older},
	}
	if len(report.RecentFailures) != len(want) {
		t.Fatalf("recent failures = %+v, want %+v", report.RecentFailures, want)
	}
	for i, got := range report.RecentFailures {
		expect := want[i]
		if got.UniqueID != expect.UniqueID || got.BatchID != expect.BatchID || got.State != expect.State || got.Detail != expect.Detail || !got.UpdatedAt.Equal(expect.UpdatedAt) {
			t.Fatalf("recent failure %d = %+v, want %+v", i, got, expect)
		}
	}

	fetchOne(t, d, "Default", depUniqueID)
	clock.Advance(browserRequestTTL + time.Minute)
	report = depReport(t, d, "Default")
	if len(report.RecentFailures) != 3 || report.RecentFailures[0].UniqueID != depUniqueID || report.RecentFailures[0].State != depStateExpired || !report.RecentFailures[0].UpdatedAt.Equal(clock.Now()) {
		t.Fatalf("recent failures after expiry = %+v, want the expired request first", report.RecentFailures)
	}
}

func TestPremiumFetchAfterAMismatchedMainFileOpensTheBrowser(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Other Mod", map[string]string{"Other/manifest.json": depManifest("Other", "Other.Mod")})
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateFailed {
		t.Fatalf("entry = %+v, want the mismatched download failed", entry)
	}
	again := fetchOne(t, d, "Default", depUniqueID)
	if again.Outcome != dto.FetchOutcomeOpenURL || again.Reason != fetchReasonPremiumMismatch || !strings.HasSuffix(again.URL, "/mods/555?tab=files") {
		t.Fatalf("fetch after mismatch = %+v, want the files page", again)
	}
	if got := len(downloader.queued()); got != 1 {
		t.Fatalf("queued downloads = %d, want the mismatched file not downloaded again", got)
	}
	if report := depReport(t, d, "Default"); len(report.RecentFailures) != 1 || !strings.HasPrefix(report.RecentFailures[0].Detail, detailArchiveMismatch) {
		t.Fatalf("recent failures = %+v, want the mismatch surfaced", report.RecentFailures)
	}
}

func TestContentCheckIOErrorsLeaveRequestsWaitingWithoutAutoInstall(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	fetchOne(t, d, "Default", depUniqueID)
	abs, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	writeFileContent(t, abs, "this is not a zip archive")
	d.svc.archives.handleLandedArchive(download.DownloadSnapshot{ID: "dl-1", GameID: depsGame}, abs, sidecar)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDownloading || entry.Detail != "" {
		t.Fatalf("entry = %+v, want it left downloading for reconciliation", entry)
	}
	if _, err := os.Stat(storeMod("Dep Core")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unreadable dependency archive fell through to auto-install: %v", err)
	}
}

func TestDependencyInstallReusesTheCheckedExtraction(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	installs := subscribeInstalls(t, d)
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
	for {
		select {
		case evt := <-installs:
			if evt.Progress != nil && evt.Progress.Step == dto.InstallStep(download.StageExtracting) {
				t.Fatalf("the dependency install extracted the archive again: %+v", evt.Progress)
			}
			if evt.Completed != nil {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no InstallCompleted event")
		}
	}
}

func TestFetchLooksUpStaleUnknownCachedMetadata(t *testing.T) {
	d, api, _, _ := newFetchDaemon(t, "regular-key")
	clock := &depClock{now: time.Now()}
	d.svc.modDeps.client.Now = clock.Now
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Late.Page")
	setOrderedModList(t, d, "Default", enabled("Needy"))

	if result := fetchOne(t, d, "Default", "Late.Page"); result.Outcome != dto.FetchOutcomeUnresolved || result.Reason != fetchReasonNoNexusPage {
		t.Fatalf("first fetch = %+v, want no Nexus page yet", result)
	}
	api.set("Late.Page", fakeSMAPIMod{name: "Late Page", nexusID: 808})
	clock.Advance(7 * time.Hour)
	result := fetchOne(t, d, "Default", "Late.Page")
	if result.Outcome != dto.FetchOutcomeOpenURL || !strings.Contains(result.URL, "/mods/808") {
		t.Fatalf("fetch with a stale negative cache = %+v, want a fresh lookup finding the page", result)
	}
	if api.requestCount() != 2 {
		t.Fatalf("lookups = %d, want the stale entry looked up again", api.requestCount())
	}

	api.fail(503)
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Never.Seen")
	if result := fetchOne(t, d, "Default", "Never.Seen"); result.Outcome != dto.FetchOutcomeUnresolved || result.Reason != fetchReasonLookupFailed {
		t.Fatalf("fetch while smapi.io is down = %+v, want lookup_failed", result)
	}
}

func TestFetchAttachesToInFlightWork(t *testing.T) {
	t.Run("live download", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		again := fetchOne(t, d, "Default", depUniqueID)
		if again.Outcome != dto.FetchOutcomeQueued || again.DownloadID != first.DownloadID || again.Reason != fetchReasonInFlight || again.BatchID == first.BatchID {
			t.Fatalf("second fetch = %+v, want it attached to %s", again, first.DownloadID)
		}
		if got := len(downloader.queued()); got != 1 {
			t.Fatalf("queued downloads = %d, want one", got)
		}
	})
	t.Run("download not yet recorded", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		clock := &depClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
		d.now = clock.Now
		err := d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
			entry := depEntry{UniqueID: depUniqueID, NexusModID: depNexusID, FileID: depFileID}
			entry.set(depStateDownloading, "", now)
			doc.Batches = append(doc.Batches, depBatch{BatchID: "dep-racing", Profile: "Default", CreatedAt: now, Entries: []depEntry{entry}})
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
		attached := fetchOne(t, d, "Default", depUniqueID)
		if attached.Outcome != dto.FetchOutcomeQueued || attached.DownloadID != "" || attached.Reason != fetchReasonInFlight || len(downloader.queued()) != 0 {
			t.Fatalf("fetch = %+v (queued %v), want it attached to the pending registration", attached, downloader.queued())
		}
		clock.Advance(pendingDownloadGrace + time.Second)
		fresh := fetchOne(t, d, "Default", depUniqueID)
		if fresh.Outcome != dto.FetchOutcomeQueued || fresh.DownloadID != "dl-1" || fresh.Reason != "" {
			t.Fatalf("fetch after the grace window = %+v, want a fresh download", fresh)
		}
		for _, entry := range depEntries(t, depUniqueID) {
			if entry.DownloadID != "dl-1" {
				t.Fatalf("entry = %+v, want every waiting request pointed at dl-1", entry)
			}
		}
	})
	t.Run("install in progress", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		installs := subscribeInstalls(t, d)
		first := fetchOne(t, d, "Default", depUniqueID)
		abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		check, err := d.svc.modDeps.checkLandedArchive(abs)
		if err != nil {
			t.Fatal(err)
		}
		err = d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
			doc.each(func(_ *depBatch, entry *depEntry) {
				entry.Install = "install-1"
				entry.set(depStateInstalling, "", now)
			})
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
		attached := fetchOne(t, d, "Default", depUniqueID)
		if attached.Outcome != dto.FetchOutcomeQueued || attached.Reason != fetchReasonInstalling || attached.DownloadID != first.DownloadID || len(downloader.queued()) != 1 {
			t.Fatalf("fetch during the install = %+v, want it attached to the running install", attached)
		}
		d.svc.modDeps.installForRequests(depsGame, relFromDownloads(depsGame, abs), check.root, "install-1", check.planned)
		completed := waitCompleted(t, installs)
		sort.Strings(completed.BatchIDs)
		want := []string{first.BatchID, attached.BatchID}
		sort.Strings(want)
		if !reflect.DeepEqual(completed.BatchIDs, want) || completed.BatchID == "" {
			t.Fatalf("InstallCompleted = %+v, want both batches %v", completed, want)
		}
		for _, entry := range depEntries(t, depUniqueID) {
			if entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
				t.Fatalf("entry = %+v, want every attached request enable_pending", entry)
			}
		}
	})
	t.Run("pending enable in another profile", func(t *testing.T) {
		d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
		fetchOne(t, d, "Default", depUniqueID)
		landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
		if _, err := d.CreateProfile(depsGame, "Fresh"); err != nil {
			t.Fatal(err)
		}
		setOrderedModList(t, d, "Fresh", enabled("Needy"))
		if state := modListState(t, d, "Fresh"); len(state) != 1 {
			t.Fatalf("Fresh modlist = %v, want only Needy", state)
		}
		result := fetchOne(t, d, "Fresh", depUniqueID)
		if result.Outcome != dto.FetchOutcomeAlreadyPresent || result.Reason != fetchReasonPendingEnable+": Dep Core" || result.BatchID == "" {
			t.Fatalf("fetch = %+v, want the installed dependency offered for enabling", result)
		}
		if len(downloader.queued()) != 1 {
			t.Fatalf("queued downloads = %v, want no second download", downloader.queued())
		}
		report := depReport(t, d, "Fresh")
		if len(report.PendingEnables) != 1 || report.PendingEnables[0].ModName != "Dep Core" || report.PendingEnables[0].BatchID != result.BatchID {
			t.Fatalf("Fresh pending enables = %+v, want Dep Core", report.PendingEnables)
		}
	})
}

func TestFetchHonoursACancelledContext(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	if _, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", true, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.FetchModDependencies(ctx, depsGame, "Default", []string{depUniqueID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchModDependencies error = %v, want context.Canceled", err)
	}
	if len(downloader.queued()) != 0 {
		t.Fatalf("queued %v after cancellation", downloader.queued())
	}
	if _, err := os.Stat(dependencyRequestsPath(depsGame)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a cancelled fetch registered requests: %v", err)
	}
}

func TestFetchRefusesBundledLoaderMods(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "SMAPI.ConsoleCommands")
	result := fetchOne(t, d, "Default", "smapi.consolecommands")
	if result.Outcome != dto.FetchOutcomeUnresolved || !strings.HasPrefix(result.Reason, fetchReasonBundled+":") || result.BatchID != "" {
		t.Fatalf("result = %+v, want a bundled loader mod left to a loader repair", result)
	}
	if len(downloader.queued()) != 0 {
		t.Fatalf("queued %v, want nothing", downloader.queued())
	}
}

func TestReportSendsNoLoaderVersionWhenUnknown(t *testing.T) {
	d, _, api := newDepsDaemon(t)
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Core")
	setOrderedModList(t, d, "Default", enabled("Needy"))
	if _, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", true, false); err != nil {
		t.Fatal(err)
	}
	if versions := api.apiVersions(); len(versions) != 1 || versions[0] != "" {
		t.Fatalf("apiVersion sent = %q, want empty for an unmanaged loader", versions)
	}
}

func TestConcurrentPremiumFetchesShareOneDownloadAndAcks(t *testing.T) {
	d, _, _, downloader := newFetchDaemon(t, premiumTestKey)
	const fetches = 6
	var wg sync.WaitGroup
	batches := make(chan string, fetches)
	errs := make(chan error, 2*fetches)
	for i := 0; i < fetches; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			results, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{depUniqueID})
			if err != nil {
				errs <- err
				return
			}
			if len(results) != 1 || results[0].Outcome != dto.FetchOutcomeQueued {
				errs <- errors.New("a concurrent premium fetch was not queued")
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
	}
	wg.Wait()
	close(batches)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := downloader.queued(); len(got) != 1 {
		t.Fatalf("queued downloads = %v, want one shared download", got)
	}
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
	waitDepState(t, depUniqueID, depStateEnablePending)

	var ackWG sync.WaitGroup
	var mu sync.Mutex
	total := 0
	ackErrs := make(chan error, 2*fetches)
	for batchID := range batches {
		ackWG.Add(2)
		go func(batchID string) {
			defer ackWG.Done()
			acked, err := d.AckDependencyEnable(context.Background(), depsGame, batchID, nil)
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
}
