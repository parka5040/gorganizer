package atomicfile

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncFilesystemRequiresRealDirectory checks that only a real staging directory can be flushed.
func TestSyncFilesystemRequiresRealDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "stage")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"directory", dir, false},
		{"file", file, true},
		{"symlink", link, true},
		{"missing", filepath.Join(root, "missing"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := SyncFilesystem(tc.path); (err != nil) != tc.wantErr {
				t.Errorf("SyncFilesystem = %v, want error %v", err, tc.wantErr)
			}
		})
	}
	identity, err := Identity(dir)
	if err != nil || identity.Ino == 0 {
		t.Errorf("stage identity = %+v, %v", identity, err)
	}
	if _, err := Identity(link); err == nil {
		t.Error("symlink has a real-directory identity")
	}
}

// TestWriteFileDurableOutcomes checks publication outcomes and destination bytes on each failure.
func TestWriteFileDurableOutcomes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("injected failure")
	cases := []struct {
		name    string
		inject  func(t *testing.T)
		outcome Outcome
		want    string
	}{
		{name: "success", outcome: Durable, want: "new"},
		{name: "directory open", inject: func(t *testing.T) {
			old := openDir
			openDir = func(string) (*os.File, error) { return nil, failure }
			t.Cleanup(func() { openDir = old })
		}, outcome: NotPublished, want: "old"},
		{name: "rename", inject: func(t *testing.T) {
			old := renameFile
			renameFile = func(string, string) error { return failure }
			t.Cleanup(func() { renameFile = old })
		}, outcome: NotPublished, want: "old"},
		{name: "directory sync", inject: func(t *testing.T) {
			old := syncDir
			syncDir = func(*os.File) error { return failure }
			t.Cleanup(func() { syncDir = old })
		}, outcome: PublishedUncertain, want: "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.inject != nil {
				tc.inject(t)
			}
			outcome, err := WriteFileDurable(path, []byte("new"), 0600)
			if outcome != tc.outcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.outcome)
			}
			if (err != nil) != (tc.outcome != Durable) {
				t.Errorf("error = %v, want error = %v", err, tc.outcome != Durable)
			}
			if tc.outcome != Durable && !errors.Is(err, failure) {
				t.Errorf("error = %v, want injected failure", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != tc.want {
				t.Errorf("destination = %q, %v; want %q", got, readErr, tc.want)
			}
			assertNoTemps(t, dir, 1)
		})
	}
}

// TestWriteFileKeepsContractOnDirSyncFailure checks that a published write succeeds and logs a warning.
func TestWriteFileKeepsContractOnDirSyncFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("injected directory error")
	for _, tc := range []struct {
		name   string
		inject func(t *testing.T)
	}{
		{name: "open", inject: func(t *testing.T) {
			old := openDir
			openDir = func(string) (*os.File, error) { return nil, failure }
			t.Cleanup(func() { openDir = old })
		}},
		{name: "sync", inject: func(t *testing.T) {
			old := syncDir
			syncDir = func(*os.File) error { return failure }
			t.Cleanup(func() { syncDir = old })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state")
			tc.inject(t)
			oldLogger := slog.Default()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(oldLogger) })
			if err := WriteFile(path, []byte("new"), 0600); err != nil {
				t.Fatalf("WriteFile = %v, want nil", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "new" {
				t.Fatalf("destination = %q, %v; want new", got, err)
			}
			if strings.Count(logs.String(), "atomicfile: directory sync failed after publishing") != 1 || !strings.Contains(logs.String(), path) || !strings.Contains(logs.String(), failure.Error()) {
				t.Errorf("unexpected warning: %s", logs.String())
			}
			assertNoTemps(t, dir, 1)
		})
	}
}

// TestCopyFileDurableWithProgress checks that a copy reports every byte written.
func TestCopyFileDurableWithProgress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	body := make([]byte, 1<<20)
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var written int64
	outcome, err := CopyFileDurableWithProgress(src, dst, 0o600, false, func(n int64) { written += n })
	if err != nil || outcome != Durable || written != int64(len(body)) {
		t.Fatalf("copy = %v, %v; reported %d bytes, want %d", outcome, err, written, len(body))
	}
	if info, err := os.Stat(dst); err != nil || info.Size() != written {
		t.Fatalf("destination = %v, %v", info, err)
	}
}

// TestCopyFileDurable checks copying, exclusive publication, validation, and cleanup on failures.
func TestCopyFileDurable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("injected copy failure")
	cases := []struct {
		name    string
		replace bool
		exists  bool
		symlink bool
		inject  func(t *testing.T, src string)
		outcome Outcome
		want    string
		wantErr error
	}{
		{name: "replace", replace: true, exists: true, outcome: Durable, want: "new"},
		{name: "create exclusively", outcome: Durable, want: "new"},
		{name: "existing destination", exists: true, outcome: NotPublished, want: "old", wantErr: fs.ErrExist},
		{name: "symlink source", symlink: true, exists: true, replace: true, outcome: NotPublished, want: "old"},
		{name: "directory open fails", replace: true, exists: true, outcome: NotPublished, want: "old", wantErr: failure, inject: func(t *testing.T, _ string) {
			old := openDir
			openDir = func(string) (*os.File, error) { return nil, failure }
			t.Cleanup(func() { openDir = old })
		}},
		{name: "rename fails", replace: true, exists: true, outcome: NotPublished, want: "old", wantErr: failure, inject: func(t *testing.T, _ string) {
			old := renameFile
			renameFile = func(string, string) error { return failure }
			t.Cleanup(func() { renameFile = old })
		}},
		{name: "directory sync fails", replace: true, exists: true, outcome: PublishedUncertain, want: "new", wantErr: failure, inject: func(t *testing.T, _ string) {
			old := syncDir
			syncDir = func(*os.File) error { return failure }
			t.Cleanup(func() { syncDir = old })
		}},
		{name: "exclusive directory sync fails", exists: true, outcome: NotPublished, want: "old", wantErr: fs.ErrExist, inject: func(t *testing.T, _ string) {
			old := syncDir
			syncDir = func(*os.File) error { return failure }
			t.Cleanup(func() { syncDir = old })
		}},
		{name: "exclusive publish sync fails", outcome: PublishedUncertain, want: "new", wantErr: failure, inject: func(t *testing.T, _ string) {
			old := syncDir
			syncDir = func(*os.File) error { return failure }
			t.Cleanup(func() { syncDir = old })
		}},
		{name: "copy fails", replace: true, exists: true, outcome: NotPublished, want: "old", wantErr: failure, inject: func(t *testing.T, _ string) {
			old := copyData
			copyData = func(dst io.Writer, src io.Reader) (int64, error) {
				_, _ = dst.Write([]byte("partial"))
				return 0, failure
			}
			t.Cleanup(func() { copyData = old })
		}},
		{name: "source close fails", replace: true, exists: true, outcome: NotPublished, want: "old", wantErr: failure, inject: func(t *testing.T, src string) {
			old := closeFile
			closeFile = func(file *os.File) error {
				if file.Name() == src {
					_ = file.Close()
					return failure
				}
				return file.Close()
			}
			t.Cleanup(func() { closeFile = old })
		}},
		{name: "temp close fails", replace: true, exists: true, outcome: NotPublished, want: "old", wantErr: failure, inject: func(t *testing.T, src string) {
			old := closeFile
			closeFile = func(file *os.File) error {
				if file.Name() != src {
					_ = file.Close()
					return failure
				}
				return file.Close()
			}
			t.Cleanup(func() { closeFile = old })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "source")
			dst := filepath.Join(dir, "destination")
			if err := os.WriteFile(src, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.symlink {
				link := filepath.Join(dir, "link")
				if err := os.Symlink(src, link); err != nil {
					t.Fatal(err)
				}
				src = link
			}
			if tc.exists {
				if err := os.WriteFile(dst, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.inject != nil {
				tc.inject(t, src)
			}
			outcome, err := CopyFileDurable(src, dst, 0644, tc.replace)
			if outcome != tc.outcome || (err != nil) != (tc.outcome != Durable) {
				t.Errorf("result = %v, %v; want %v, error = %v", outcome, err, tc.outcome, tc.outcome != Durable)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
			got, readErr := os.ReadFile(dst)
			if readErr != nil || string(got) != tc.want {
				t.Errorf("destination = %q, %v; want %q", got, readErr, tc.want)
			}
			if tc.outcome == Durable {
				info, statErr := os.Stat(dst)
				if statErr != nil {
					t.Errorf("stat destination: %v", statErr)
				} else if info.Mode().Perm() != 0644 {
					t.Errorf("destination mode = %o; want 0644", info.Mode().Perm())
				}
			}
			count := 1
			if tc.symlink {
				count++
			}
			if tc.want != "" {
				count++
			}
			assertNoTemps(t, dir, count)
		})
	}
}

// TestRemoveDurable checks idempotent removal and directory sync errors after removal.
func TestRemoveDurable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("injected directory sync failure")
	for _, tc := range []struct {
		name    string
		exists  bool
		fail    bool
		wantErr bool
	}{
		{name: "existing file", exists: true},
		{name: "missing file"},
		{name: "sync failure", exists: true, fail: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			if tc.exists {
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fail {
				old := syncDir
				syncDir = func(*os.File) error { return failure }
				t.Cleanup(func() { syncDir = old })
			}
			err := RemoveDurable(path)
			if (err != nil) != tc.wantErr || tc.wantErr && !errors.Is(err, failure) {
				t.Errorf("RemoveDurable = %v, want error = %v", err, tc.wantErr)
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("destination still exists: %v", err)
			}
		})
	}
}

// assertNoTemps checks that a directory has no leftover temporary files.
func assertNoTemps(t *testing.T, dir string, count int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Errorf("directory has %d entries, want %d: %v", len(entries), count, entries)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}
