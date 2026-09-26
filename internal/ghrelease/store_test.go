package ghrelease

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStoreInstallKeepReplaceAndRestore verifies keep, exchange-replace, and failed-replace semantics of Install.
func TestStoreInstallKeepReplaceAndRestore(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	prepare := func(contents string) (string, func()) {
		t.Helper()
		stage, cleanup, err := store.NewStage()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "marker"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return stage, cleanup
	}
	first, firstCleanup := prepare("first")
	defer firstCleanup()
	if err := store.Install("1.0.0", first, false); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, first)
	kept, keptCleanup := prepare("kept")
	defer keptCleanup()
	if err := store.Install("1.0.0", kept, false); err != nil {
		t.Fatal(err)
	}
	assertMarker(t, store, "1.0.0", "first")
	if _, err := os.Stat(filepath.Join(kept, "marker")); err != nil {
		t.Fatalf("kept prepared directory was moved: %v", err)
	}
	keptCleanup()
	replacement, replacementCleanup := prepare("replacement")
	defer replacementCleanup()
	if err := store.Install("1.0.0", replacement, true); err != nil {
		t.Fatal(err)
	}
	assertMarker(t, store, "1.0.0", "replacement")
	assertMissing(t, replacement)
	if got, want := rootEntries(t, store), []string{"1.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("root entries after replace = %q; want %q", got, want)
	}
	err := store.Install("1.0.0", filepath.Join(store.Root, "missing"), true)
	if err == nil || !strings.HasPrefix(err.Error(), "activating managed release version directory: ") {
		t.Fatalf("Install with missing prepared directory error = %v", err)
	}
	assertMarker(t, store, "1.0.0", "replacement")
	fresh, freshCleanup := prepare("fresh")
	defer freshCleanup()
	if err := store.Install("2.0.0", fresh, true); err != nil {
		t.Fatal(err)
	}
	assertMarker(t, store, "2.0.0", "fresh")
}

// TestStoreActivationRollbackAndPruning verifies previous-version computation, pruning, and rollback.
func TestStoreActivationRollbackAndPruning(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	for _, name := range []string{"1.0.0", "stale", ".stage-retained"} {
		if err := os.MkdirAll(filepath.Join(store.Root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Activate("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if current != (Current{SchemaVersion: CurrentSchema, ActiveVersion: "1.0.0"}) {
		t.Fatalf("first current = %+v", current)
	}
	assertMissing(t, filepath.Join(store.Root, "stale"))
	if _, err := os.Stat(filepath.Join(store.Root, ".stage-retained")); err != nil {
		t.Fatalf("stage directory was pruned: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(store.Root, "2.0.0"), 0755); err != nil {
		t.Fatal(err)
	}
	current, err = store.Activate("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if current.ActiveVersion != "2.0.0" || current.PreviousVersion != "1.0.0" {
		t.Fatalf("upgrade current = %+v", current)
	}
	current, err = store.Activate("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if current.ActiveVersion != "2.0.0" || current.PreviousVersion != "1.0.0" {
		t.Fatalf("same-version current = %+v", current)
	}
	current, err = store.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if current.ActiveVersion != "1.0.0" || current.PreviousVersion != "2.0.0" {
		t.Fatalf("rolled current = %+v", current)
	}
	read, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if read != current {
		t.Fatalf("ReadCurrent = %+v; want %+v", read, current)
	}
}

// TestStoreRollbackFailures verifies the typed errors for a missing previous version and a missing previous directory.
func TestStoreRollbackFailures(t *testing.T) {
	store := &Store{Root: t.TempDir(), Label: "SMAPI"}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if err := os.MkdirAll(filepath.Join(store.Root, version), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Activate("1.0.0"); err != nil {
		t.Fatal(err)
	}
	_, err := store.Rollback()
	if !errors.Is(err, ErrNoPrevious) || err.Error() != "SMAPI has no previous version to roll back to" {
		t.Fatalf("Rollback error = %v; want no previous", err)
	}
	if _, err := store.Activate("2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(store.Root, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(store.Root, currentFileName))
	_, err = store.Rollback()
	if !errors.Is(err, ErrPreviousUnavailable) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Rollback error = %v; want previous unavailable wrapping not-exist", err)
	}
	if !strings.HasPrefix(err.Error(), "previous SMAPI version is unavailable: ") {
		t.Fatalf("Rollback error = %q; want labeled message", err)
	}
	if after := readFile(t, filepath.Join(store.Root, currentFileName)); after != before {
		t.Fatalf("failed Rollback rewrote current.json: %q", after)
	}
}

// TestStoreReadCurrentValidation verifies ReadCurrent accepts only schema 1 manifests with safe version names.
func TestStoreReadCurrentValidation(t *testing.T) {
	tests := map[string]string{
		"bad json":         `{`,
		"schema zero":      `{"active_version":"1.0.0"}`,
		"schema two":       `{"schema_version":2,"active_version":"1.0.0"}`,
		"empty active":     `{"schema_version":1,"active_version":""}`,
		"traversal active": `{"schema_version":1,"active_version":"../x"}`,
		"reserved active":  `{"schema_version":1,"active_version":"current.json"}`,
		"separator prev":   `{"schema_version":1,"active_version":"1.0.0","previous_version":"a/b"}`,
		"hidden prev":      `{"schema_version":1,"active_version":"1.0.0","previous_version":".stage-x"}`,
		"control prev":     `{"schema_version":1,"active_version":"1.0.0","previous_version":"1.0\u0001"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			store := &Store{Root: t.TempDir(), Label: "LOOT"}
			if err := os.WriteFile(filepath.Join(store.Root, currentFileName), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := store.ReadCurrent()
			if !errors.Is(err, ErrInvalidCurrent) || !strings.HasPrefix(err.Error(), "LOOT current manifest is invalid or unsupported") {
				t.Fatalf("ReadCurrent error = %v; want invalid current", err)
			}
		})
	}
	store := &Store{Root: t.TempDir()}
	if _, err := store.ReadCurrent(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadCurrent without current.json error = %v; want not-exist", err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, currentFileName), []byte(`{"schema_version":1,"active_version":"1.0.0","previous_version":"0.9.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if current != (Current{SchemaVersion: 1, ActiveVersion: "1.0.0", PreviousVersion: "0.9.0"}) {
		t.Fatalf("ReadCurrent = %+v", current)
	}
}

// TestStoreActivateFailsClosedOnUnreadableCurrent verifies Activate neither writes nor prunes when current.json is not merely missing.
func TestStoreActivateFailsClosedOnUnreadableCurrent(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, path string){
		"corrupt json": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{`), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"unsupported schema": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"schema_version":2,"active_version":"1.0.0"}`), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &Store{Root: t.TempDir()}
			for _, version := range []string{"1.0.0", "stale", "2.0.0"} {
				if err := os.MkdirAll(filepath.Join(store.Root, version), 0755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(store.Root, currentFileName)
			setup(t, path)
			before, _ := os.ReadFile(path)
			_, err := store.Activate("2.0.0")
			if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Activate error = %v; want a non-missing read failure", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatalf("Activate rewrote current.json to %q", after)
			}
			if got, want := rootEntries(t, store), []string{"1.0.0", "2.0.0", currentFileName, "stale"}; !slices.Equal(got, want) {
				t.Fatalf("root entries after failed Activate = %q; want %q", got, want)
			}
		})
	}
}

// TestStoreRejectsUnsafeVersionNames verifies VersionDir, Install, and Activate refuse unsafe version names without side effects.
func TestStoreRejectsUnsafeVersionNames(t *testing.T) {
	for _, version := range unsafeVersions {
		t.Run(strconv.Quote(version), func(t *testing.T) {
			store := &Store{Root: t.TempDir()}
			if _, err := store.VersionDir(version); !errors.Is(err, ErrInvalidRelease) {
				t.Fatalf("VersionDir error = %v; want invalid release", err)
			}
			stage, cleanup, err := store.NewStage()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if err := store.Install(version, stage, true); !errors.Is(err, ErrInvalidRelease) {
				t.Fatalf("Install error = %v; want invalid release", err)
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatalf("rejected Install moved the prepared directory: %v", err)
			}
			if _, err := store.Activate(version); !errors.Is(err, ErrInvalidRelease) {
				t.Fatalf("Activate error = %v; want invalid release", err)
			}
			assertMissing(t, filepath.Join(store.Root, currentFileName))
		})
	}
	if _, err := (&Store{Root: t.TempDir()}).VersionDir(""); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("VersionDir(\"\") error = %v; want invalid release", err)
	}
}

// TestStorePruneIgnoresInvalidCurrent verifies Prune never deletes versions when given an unusable manifest.
func TestStorePruneIgnoresInvalidCurrent(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(store.Root, "1.0.0"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, current := range []Current{{}, {ActiveVersion: "../x"}, {ActiveVersion: "2.0.0", PreviousVersion: "a/b"}} {
		store.Prune(current)
		if _, err := os.Stat(filepath.Join(store.Root, "1.0.0")); err != nil {
			t.Fatalf("Prune(%+v) removed a version: %v", current, err)
		}
	}
}

// TestStoreLabelsErrors verifies Store error messages carry its Label.
func TestStoreLabelsErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Store{Root: filepath.Join(blocker, "loot"), Label: "LOOT"}).NewStage(); err == nil || !strings.HasPrefix(err.Error(), "creating LOOT tools directory: ") {
		t.Fatalf("NewStage error = %v; want labeled tools directory error", err)
	}
	store := &Store{Root: t.TempDir(), Label: "LOOT"}
	if err := store.Install("1.0.0", filepath.Join(store.Root, "missing"), false); err == nil || !strings.HasPrefix(err.Error(), "activating LOOT version directory: ") {
		t.Fatalf("Install error = %v; want labeled activation error", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	if err := os.Chmod(store.Root, 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(store.Root, 0700) }()
	if _, _, err := store.NewStage(); err == nil || !strings.HasPrefix(err.Error(), "creating LOOT staging directory: ") {
		t.Fatalf("NewStage error = %v; want labeled staging error", err)
	}
}

// TestStoreSerializesMutationsPerRoot verifies mutations wait for the lock of an equivalent root path and not for other roots.
func TestStoreSerializesMutationsPerRoot(t *testing.T) {
	root := t.TempDir()
	other := &Store{Root: t.TempDir()}
	for _, dir := range []string{filepath.Join(root, "1.0.0"), filepath.Join(other.Root, "1.0.0")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	unlock := lockRoot(root + string(filepath.Separator) + ".")
	if _, err := other.Activate("1.0.0"); err != nil {
		unlock()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := (&Store{Root: root}).Activate("1.0.0")
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("Activate did not wait for the root lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Activate did not resume after the root lock was released")
	}
}

// assertMarker fails the test unless the marker file of version holds want.
func assertMarker(t *testing.T, store *Store, version, want string) {
	t.Helper()
	dir, err := store.VersionDir(version)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "marker")); got != want {
		t.Fatalf("%s marker = %q; want %q", version, got, want)
	}
}

// readFile returns the contents of path or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// rootEntries returns the sorted names directly under the store root.
func rootEntries(t *testing.T, store *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
