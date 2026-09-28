package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/transfer"
)

// TestInitialMountWaitsForImportPublication checks that mounting cannot slip between a deployment check and mod publication.
func TestInitialMountWaitsForImportPublication(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	modFile := filepath.Join(config.ModsDir("skyrimse"), "A", "a.esp")
	writeFixture(t, modFile)
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateProfile("skyrimse", "Imported"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{GameID: "skyrimse", OutputPath: archive, ModFolders: []string{"A"}, ProfileNames: []string{"Imported"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modFile, []byte("old state"), 0644); err != nil {
		t.Fatal(err)
	}
	checked := make(chan struct{})
	release := make(chan struct{})
	var blocked sync.Once
	importDone := make(chan error, 1)
	go func() {
		_, err := transfer.Import(context.Background(), transfer.ImportOptions{
			GameID:      "skyrimse",
			ArchivePath: archive,
			Policy:      dto.PolicyOverwrite,
			LockMod: func(name string) func() {
				return d.lockMods("skyrimse", name)
			},
			LockProfiles: func() func() {
				return d.lockProfiles("skyrimse")
			},
			LockState: func() func() {
				d.mu.RLock()
				return func() {
					d.mu.RUnlock()
					blocked.Do(func() {
						close(checked)
						<-release
					})
				}
			},
			CheckReplacement: func(root, name string) error {
				if root != config.ModsDir("skyrimse") || name != "A" {
					return nil
				}
				used, err := d.svc.mods.mountedModUsedLocked("skyrimse", name)
				if err != nil {
					return err
				}
				if used {
					return &TransferOverwriteMountedError{Name: name}
				}
				return nil
			},
		}, nil)
		importDone <- err
	}()
	select {
	case <-checked:
	case err := <-importDone:
		t.Fatalf("import ended before publication barrier: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("import did not reach the deployment check")
	}
	mountStarted := make(chan struct{})
	mountDone := make(chan error, 1)
	go func() {
		close(mountStarted)
		_, err := d.MountVFS("skyrimse", "Default")
		mountDone <- err
	}()
	<-mountStarted
	var earlyMount error
	mountedEarly := false
	select {
	case earlyMount = <-mountDone:
		mountedEarly = true
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-importDone:
		if err != nil {
			t.Fatalf("import after publication barrier: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish")
	}
	if mountedEarly {
		t.Fatalf("initial mount finished before the import published its mod: %v", earlyMount)
	}
	select {
	case err := <-mountDone:
		if err != nil {
			t.Fatalf("mount after import: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mount did not resume after import")
	}
	for _, path := range []string{modFile, filepath.Join(data, "a.esp")} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "fixture" {
			t.Errorf("%s did not contain the imported bytes: %q, %v", path, content, err)
		}
	}
}
