package tools

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeVersionedPE(t *testing.T, path string, version PEFileVersion) {
	t.Helper()
	data := make([]byte, 64)
	offset := 16
	binary.LittleEndian.PutUint32(data[offset:], 0xfeef04bd)
	binary.LittleEndian.PutUint32(data[offset+4:], 0x00010000)
	binary.LittleEndian.PutUint32(data[offset+8:], uint32(version.Major<<16|version.Minor))
	binary.LittleEndian.PutUint32(data[offset+12:], uint32(version.Patch<<16|version.Build))
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSKSERuntime(t *testing.T) {
	dir := t.TempDir()
	writeVersionedPE(t, filepath.Join(dir, "SkyrimSE.exe"), PEFileVersion{Major: 1, Minor: 6, Patch: 1170})
	if err := os.WriteFile(filepath.Join(dir, "skse64_1_6_1170.dll"), []byte("dll"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSKSERuntime("skyrimse", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "skse64_1_6_1170.dll")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSKSERuntime("skyrimse", dir); err == nil {
		t.Fatal("mismatched SKSE DLL was accepted")
	}
}
