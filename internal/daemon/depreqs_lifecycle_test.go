package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
)

// markInstalling moves every dependency request entry to installing under installID, as a landing does before its install.
func markInstalling(t *testing.T, d *Daemon, installID string) {
	t.Helper()
	err := d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
		doc.each(func(_ *depBatch, entry *depEntry) {
			entry.Install = installID
			entry.set(depStateInstalling, "", now)
		})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRestartResumesAnInterruptedDependencyInstall locks that an install the daemon did not finish is consumed again from its indexed archive or adopted when its mod exists.
func TestRestartResumesAnInterruptedDependencyInstall(t *testing.T) {
	t.Run("archive indexed", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		first := fetchOne(t, d, "Default", depUniqueID)
		writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		markInstalling(t, d, "install-crashed")

		restarted := restartWithoutNexusKey(t, d)
		entry := onlyDepEntry(t, depUniqueID)
		if entry.State != depStateEnablePending || entry.ModName != "Dep Core" || entry.DownloadID != first.DownloadID {
			t.Fatalf("entry after recovery = %+v, want the interrupted install consumed again", entry)
		}
		if _, ok := modListState(t, restarted, "Default")["Dep Core"]; !ok {
			t.Fatal("the resumed install was not registered in the modlist")
		}
	})
	t.Run("mod already installed", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		fetchOne(t, d, "Default", depUniqueID)
		abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
		if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ArchiveRelPath: relFromDownloads(depsGame, abs), Mode: dto.InstallAsNewMod, TargetMod: "Installed Dep"}); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		markInstalling(t, d, "install-crashed")

		restartWithoutNexusKey(t, d)
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Installed Dep" {
			t.Fatalf("entry after recovery = %+v, want the installed mod adopted", entry)
		}
		if _, err := os.Stat(storeMod("Dep Core")); !os.IsNotExist(err) {
			t.Fatalf("recovery installed the archive a second time: %v", err)
		}
	})
	t.Run("no archive", func(t *testing.T) {
		d, _, _, _ := newFetchDaemon(t, premiumTestKey)
		fetchOne(t, d, "Default", depUniqueID)
		markInstalling(t, d, "install-crashed")
		restartWithoutNexusKey(t, d)
		if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateFailed || !strings.HasPrefix(entry.Detail, detailInstallInterrupted) {
			t.Fatalf("entry after recovery = %+v, want install_interrupted", entry)
		}
	})
}

// TestInstallCompletedCarriesNoBatchWhenTheRequestUpdateFails locks that an install announcement never names batches whose enable was not durably recorded.
func TestInstallCompletedCarriesNoBatchWhenTheRequestUpdateFails(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	installs := subscribeInstalls(t, d)
	fetchOne(t, d, "Default", depUniqueID)
	abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	check, err := d.svc.modDeps.checkLandedArchive(abs)
	if err != nil {
		t.Fatal(err)
	}
	markInstalling(t, d, "install-1")
	snapshot, err := loadDependencyRequests(depsGame)
	if err != nil {
		t.Fatal(err)
	}
	d.svc.modDeps.loadRequests = func(string) (*depRequestsDoc, error) {
		copied := *snapshot
		copied.Batches = append([]depBatch(nil), snapshot.Batches...)
		for i := range copied.Batches {
			copied.Batches[i].Entries = append([]depEntry(nil), snapshot.Batches[i].Entries...)
		}
		return &copied, nil
	}
	requests := dependencyRequestsPath(depsGame)
	if err := os.Remove(requests); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(requests, 0o755); err != nil {
		t.Fatal(err)
	}

	d.svc.modDeps.installForRequests(depsGame, relFromDownloads(depsGame, abs), check.root, "install-1", check.planned)
	completed := waitCompleted(t, installs)
	if completed.ModName != "Dep Core" || completed.BatchID != "" || len(completed.BatchIDs) != 0 {
		t.Fatalf("InstallCompleted = %+v, want the install announced without unrecorded batches", completed)
	}
	if _, err := os.Stat(check.root); !os.IsNotExist(err) {
		t.Errorf("the content-check extraction survived the install: %v", err)
	}
}

// TestReportFailsPendingEnablesOutsideTheModlist locks that an installed dependency removed from the profile's modlist stops waiting as not_in_modlist.
func TestReportFailsPendingEnablesOutsideTheModlist(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if report := depReport(t, d, "Default"); len(report.PendingEnables) != 1 {
		t.Fatalf("pending enables before the modlist changed = %+v, want Dep Core", report.PendingEnables)
	}
	loaded, _, err := d.profileMgr.Load(depsGame, "Default")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.profileMgr.Save(loaded, []mod.ModListEntry{{Name: "Needy", Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	report := depReport(t, d, "Default")
	if len(report.PendingEnables) != 0 {
		t.Fatalf("pending enables = %+v, want none once Dep Core left the modlist", report.PendingEnables)
	}
	if len(report.RecentFailures) != 1 || !strings.HasPrefix(report.RecentFailures[0].Detail, detailNotInModlist) {
		t.Fatalf("recent failures = %+v, want a not_in_modlist failure", report.RecentFailures)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateFailed {
		t.Fatalf("entry = %+v, want failed", entry)
	}
}

// TestShutdownLeavesAnInterruptedDependencyInstallForRecovery locks that an install refused by shutdown keeps its entries installing instead of failing them.
func TestShutdownLeavesAnInterruptedDependencyInstallForRecovery(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	check, err := d.svc.modDeps.checkLandedArchive(abs)
	if err != nil {
		t.Fatal(err)
	}
	markInstalling(t, d, "install-1")
	d.beginShutdown()
	d.svc.modDeps.installForRequests(depsGame, relFromDownloads(depsGame, abs), check.root, "install-1", check.planned)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateInstalling {
		t.Fatalf("entry = %+v, want it left installing for startup recovery", entry)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(depsGame), "Dep Core")); !os.IsNotExist(err) {
		t.Fatalf("an install ran during shutdown: %v", err)
	}
}

// TestShutdownKeepsContentChecksAnAbandonedInstallStillUses locks that shutdown never deletes an extraction a running landing may still read, and that the next start sweeps what it left.
func TestShutdownKeepsContentChecksAnAbandonedInstallStillUses(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	check, err := d.svc.modDeps.checkLandedArchive(abs)
	if err != nil {
		t.Fatal(err)
	}
	d.shutdownAll(nil)
	if _, err := os.Stat(check.root); err != nil {
		t.Fatalf("shutdown deleted an extraction an abandoned landing still uses: %v", err)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(check.root, old, old); err != nil {
		t.Fatal(err)
	}
	restarted := restartDaemon(t, d)
	restarted.RecoverAll()
	if _, err := os.Stat(check.root); !os.IsNotExist(err) {
		t.Fatalf("the next start kept a leftover extraction: %v", err)
	}
	root, err := extractionRoot()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := os.MkdirTemp(root, extractionPrefix)
	if err != nil {
		t.Fatal(err)
	}
	restarted.sweepStaleExtractions()
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the sweep removed an extraction made since the daemon started: %v", err)
	}
}

// TestShutdownDropsUnleasedPreviews locks that shutdown removes preview extractions no install holds and keeps a leased one.
func TestShutdownDropsUnleasedPreviews(t *testing.T) {
	d := newIsolatedDaemon(t, newSkyrimGames(t))
	root, err := extractionRoot()
	if err != nil {
		t.Fatal(err)
	}
	idle, _ := os.MkdirTemp(root, extractionPrefix)
	held, _ := os.MkdirTemp(root, extractionPrefix)
	d.previews.put(&previewEntry{GameID: "skyrimse", ExtractRoot: idle})
	heldID := d.previews.put(&previewEntry{GameID: "skyrimse", ExtractRoot: held})
	d.previews.acquire(heldID)
	d.shutdownAll(nil)
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Errorf("an unleased preview survived shutdown: %v", err)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("shutdown removed a preview an install still holds: %v", err)
	}
	d.previews.release(heldID)
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Errorf("the held preview was not removed on its last release: %v", err)
	}
}

// TestExtractionRootRefusesAForeignPath locks that a pre-created symlink in place of the extraction root is refused instead of swept through.
func TestExtractionRootRefusesAForeignPath(t *testing.T) {
	isolateDaemonState(t)
	target := t.TempDir()
	sum := sha256.Sum256([]byte(config.RuntimeDir()))
	root := filepath.Join(os.TempDir(), fmt.Sprintf("gorganizer-extract-%d-%s", os.Getuid(), hex.EncodeToString(sum[:6])))
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, err := extractionRoot(); err == nil {
		t.Fatal("extractionRoot accepted a symlink")
	}
}

// dropFromModList rewrites profileName's modlist without modName, as an install killed before its modlist registration leaves it.
func dropFromModList(t *testing.T, d *Daemon, profileName, modName string) {
	t.Helper()
	loaded, entries, err := d.profileMgr.Load(depsGame, profileName)
	if err != nil {
		t.Fatal(err)
	}
	kept := entries[:0]
	for _, entry := range entries {
		if entry.Name != modName {
			kept = append(kept, entry)
		}
	}
	if err := d.profileMgr.Save(loaded, kept); err != nil {
		t.Fatal(err)
	}
}

// TestAdoptionListsTheModInTheRequestingProfile locks that adopting an installed archive registers the mod, disabled, in each adopting entry's profile so its enable never fails as not_in_modlist.
func TestAdoptionListsTheModInTheRequestingProfile(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	abs, _ := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ArchiveRelPath: relFromDownloads(depsGame, abs), Mode: dto.InstallAsNewMod, TargetMod: "Killed Dep"}); err != nil {
		t.Fatalf("StartInstall: %v", err)
	}
	dropFromModList(t, d, "Default", "Killed Dep")

	restarted := restartWithoutNexusKey(t, d)
	if enabled, listed := modListState(t, restarted, "Default")["Killed Dep"]; !listed || enabled {
		t.Fatalf("modlist = %v, want the adopted mod listed disabled", modListState(t, restarted, "Default"))
	}
	report := depReport(t, restarted, "Default")
	if len(report.PendingEnables) != 1 || report.PendingEnables[0].ModName != "Killed Dep" {
		t.Fatalf("pending enables = %+v, want the adopted mod waiting to be enabled", report.PendingEnables)
	}
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending {
		t.Fatalf("entry = %+v, want enable_pending", entry)
	}
}

// TestRestartResumesAnInstallFromItsRecordedArchive locks that an interrupted install whose Nexus file is unknown resumes from the archive it consumed.
func TestRestartResumesAnInstallFromItsRecordedArchive(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	fetchOne(t, d, "Default", depUniqueID)
	abs, _ := writeLandedArchive(t, depNexusID, 778, "Dep Core", depArchiveFiles())
	rel := relFromDownloads(depsGame, abs)
	err := d.svc.modDeps.updateRequests(depsGame, func(doc *depRequestsDoc, now time.Time) bool {
		doc.each(func(_ *depBatch, entry *depEntry) {
			entry.Install = "install-crashed"
			entry.Archive = rel
			entry.FileID = 0
			entry.set(depStateInstalling, "", now)
		})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	restartWithoutNexusKey(t, d)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after recovery = %+v, want the install resumed from its recorded archive", entry)
	}
}

// TestConsumptionRecordsTheArchive locks that a landing consumed for a request records its archive on the entry.
func TestConsumptionRecordsTheArchive(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, premiumTestKey)
	fetchOne(t, d, "Default", depUniqueID)
	landArchive(t, d, "dl-1", depNexusID, depFileID, "Dep Core", depArchiveFiles())
	want := filepath.Join(fmt.Sprintf("%d_Dep", depNexusID), fmt.Sprintf("dep-%d.zip", depFileID))
	if entry := onlyDepEntry(t, depUniqueID); entry.Archive != want {
		t.Fatalf("entry archive = %q, want %q", entry.Archive, want)
	}
}

// TestRestartConsumesABrowserLandingNobodyHandled locks that an archive of a browser request's mod that landed but was never handled is consumed at the next start.
func TestRestartConsumesABrowserLandingNobodyHandled(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	fetchOne(t, d, "Default", depUniqueID)
	writeLandedArchive(t, depNexusID, 779, "Dep Core", depArchiveFiles())
	other := map[string]string{"Other/manifest.json": depManifest("Other", "Other.Mod"), "Other/Mod.dll": "dll"}
	writeLandedArchive(t, 4040, 780, "Unrelated", other)

	restarted := restartWithoutNexusKey(t, d)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("entry after recovery = %+v, want the unhandled browser landing consumed", entry)
	}
	if _, listed := modListState(t, restarted, "Default")["Unrelated"]; listed {
		t.Fatal("recovery installed an archive of another mod")
	}
}

// TestRestartIgnoresBrowserArchivesOlderThanTheRequest locks that an archive of the same mod downloaded before the request is not consumed at startup.
func TestRestartIgnoresBrowserArchivesOlderThanTheRequest(t *testing.T) {
	d, _, _, _ := newFetchDaemon(t, "regular-key")
	abs, _ := writeLandedArchive(t, depNexusID, 781, "Dep Core", depArchiveFiles())
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(abs, old, old); err != nil {
		t.Fatal(err)
	}
	fetchOne(t, d, "Default", depUniqueID)
	restartWithoutNexusKey(t, d)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateAwaitingDownload {
		t.Fatalf("entry after recovery = %+v, want it still waiting", entry)
	}
}
