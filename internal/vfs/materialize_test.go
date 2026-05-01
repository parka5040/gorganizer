package vfs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func walkRel(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestMaterialize_BasicHardlinkFarm(t *testing.T) {
	base := fixture(t, map[string]string{
		"FalloutNV.esm":         "base esm",
		"Meshes/armor/iron.nif": "base mesh",
		"Sound/fx/door.wav":     "base sound",
	})
	mod := fixture(t, map[string]string{
		"Meshes/armor/iron.nif": "mod overrides iron",
		"Meshes/armor/steel.nif": "mod adds steel",
	})

	tree := NewMergedTree()
	if err := tree.Build([]Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "MyMod", RootPath: mod, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "Data")
	stats, err := BuildInto(out, tree, tree.Layers(), "")
	if err != nil {
		t.Fatalf("BuildInto: %v", err)
	}
	if stats.FilesHardlinked == 0 {
		t.Errorf("expected hardlinks, got %+v", stats)
	}
	if stats.FilesSymlinked != 0 {
		t.Errorf("expected no cross-fs symlinks for tmpfs-on-tmpfs, got %+v", stats)
	}

	got := walkRel(t, out)
	want := []string{
		"FalloutNV.esm",
		"Meshes/",
		"Meshes/armor/",
		"Meshes/armor/iron.nif",
		"Meshes/armor/steel.nif",
		"Sound/",
		"Sound/fx/",
		"Sound/fx/door.wav",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("layout mismatch:\n got %v\nwant %v", got, want)
	}

	body, err := os.ReadFile(filepath.Join(out, "Meshes", "armor", "iron.nif"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "mod overrides iron" {
		t.Errorf("iron.nif content = %q, want %q (mod should win)", string(body), "mod overrides iron")
	}
}

func TestMaterialize_FilesAreReadOnly(t *testing.T) {
	base := fixture(t, map[string]string{"a.esp": "x"})
	tree := NewMergedTree()
	if err := tree.Build([]Layer{{Name: "__base__", RootPath: base, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Data")
	if _, err := BuildInto(out, tree, tree.Layers(), ""); err != nil {
		t.Fatalf("BuildInto: %v", err)
	}

	info, err := os.Stat(filepath.Join(out, "a.esp"))
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode&0222 != 0 {
		t.Errorf("expected file read-only, got mode %o", mode)
	}
}

func TestMaterialize_OverwriteModPreservesMode(t *testing.T) {
	base := fixture(t, map[string]string{"a.esp": "base"})
	overwrite := fixture(t, map[string]string{"b.esp": "overwrite"})

	tree := NewMergedTree()
	if err := tree.Build([]Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Data")
	if _, err := BuildInto(out, tree, tree.Layers(), "Overwrite"); err != nil {
		t.Fatalf("BuildInto: %v", err)
	}

	infoA, err := os.Stat(filepath.Join(out, "a.esp"))
	if err != nil {
		t.Fatal(err)
	}
	if infoA.Mode().Perm()&0222 != 0 {
		t.Errorf("a.esp expected read-only, got %o", infoA.Mode().Perm())
	}

	infoB, err := os.Stat(filepath.Join(out, "b.esp"))
	if err != nil {
		t.Fatal(err)
	}
	if infoB.Mode().Perm()&0222 == 0 {
		t.Errorf("b.esp expected writable (overwrite mod), got %o", infoB.Mode().Perm())
	}
}

func TestMaterialize_HardlinkSharesInodeWithSource(t *testing.T) {
	base := fixture(t, map[string]string{"a.esp": "x"})
	tree := NewMergedTree()
	if err := tree.Build([]Layer{{Name: "__base__", RootPath: base, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Data")
	if _, err := BuildInto(out, tree, tree.Layers(), ""); err != nil {
		t.Fatalf("BuildInto: %v", err)
	}

	srcInfo, err := os.Stat(filepath.Join(base, "a.esp"))
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(filepath.Join(out, "a.esp"))
	if err != nil {
		t.Fatal(err)
	}
	srcStat := srcInfo.Sys().(*syscall.Stat_t)
	dstStat := dstInfo.Sys().(*syscall.Stat_t)
	if srcStat.Ino != dstStat.Ino {
		t.Errorf("expected same inode (hardlinked), got src=%d dst=%d", srcStat.Ino, dstStat.Ino)
	}
	if dstStat.Nlink < 2 {
		t.Errorf("expected nlink >= 2, got %d", dstStat.Nlink)
	}
}

func TestCaptureNewFiles_MovesNewFileIntoOverwrite(t *testing.T) {
	base := fixture(t, map[string]string{"a.esp": "x"})
	overwriteSrc := fixture(t, map[string]string{})

	tree := NewMergedTree()
	if err := tree.Build([]Layer{{Name: "__base__", RootPath: base, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Data")
	if _, err := BuildInto(out, tree, tree.Layers(), ""); err != nil {
		t.Fatalf("BuildInto: %v", err)
	}

	if err := os.WriteFile(filepath.Join(out, "tool_output.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	moved, err := CaptureNewFiles(out, overwriteSrc)
	if err != nil {
		t.Fatalf("CaptureNewFiles: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 captured file, got %d", moved)
	}

	body, err := os.ReadFile(filepath.Join(overwriteSrc, "tool_output.txt"))
	if err != nil {
		t.Fatalf("captured file should be in overwrite root: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("captured body = %q, want %q", string(body), "hello")
	}

	if _, err := os.Stat(filepath.Join(out, "tool_output.txt")); !os.IsNotExist(err) {
		t.Errorf("captured file should be gone from Data/, got err = %v", err)
	}
}

func TestCaptureNewFiles_LeavesHardlinksAlone(t *testing.T) {
	base := fixture(t, map[string]string{"a.esp": "x"})
	overwriteSrc := fixture(t, map[string]string{})

	tree := NewMergedTree()
	if err := tree.Build([]Layer{{Name: "__base__", RootPath: base, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Data")
	if _, err := BuildInto(out, tree, tree.Layers(), ""); err != nil {
		t.Fatalf("BuildInto: %v", err)
	}

	moved, err := CaptureNewFiles(out, overwriteSrc)
	if err != nil {
		t.Fatalf("CaptureNewFiles: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected 0 captured files, got %d", moved)
	}
	if _, err := os.Stat(filepath.Join(out, "a.esp")); err != nil {
		t.Errorf("a.esp should still be hardlinked into Data/, got %v", err)
	}
}

func TestCaptureNewFiles_SkipsSentinel(t *testing.T) {
	out := t.TempDir()
	overwriteSrc := t.TempDir()

	if err := os.WriteFile(filepath.Join(out, SentinelFilename), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	moved, err := CaptureNewFiles(out, overwriteSrc)
	if err != nil {
		t.Fatalf("CaptureNewFiles: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected sentinel to be skipped, got moved=%d", moved)
	}
	if _, err := os.Stat(filepath.Join(out, SentinelFilename)); err != nil {
		t.Errorf("sentinel should still be in place, got %v", err)
	}
}

func TestCaptureNewFiles_NoOverwriteRootIsNoop(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "x"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	moved, err := CaptureNewFiles(out, "")
	if err != nil {
		t.Fatalf("CaptureNewFiles: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected no-op, got moved=%d", moved)
	}
	if _, err := os.Stat(filepath.Join(out, "x")); err != nil {
		t.Errorf("file should still be present (no escape hatch), got %v", err)
	}
}
