package plugins

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeFutureTime() time.Time { return time.Now().Add(2 * time.Second) }

// buildTES4 builds a synthetic TES4 record header for tests. flags is the
// 16-bit field at byte 8 (low 16 bits of the 32-bit on-disk word). Each
// master in masters is emitted as MAST + DATA pair, matching what xEdit and
// the Bethesda engines produce.
func buildTES4(flags uint16, masters []string) []byte {
	var sub []byte
	// HEDR (12 bytes payload) — version float, numRecords int, nextObjectID int
	hedr := make([]byte, 12)
	sub = append(sub, 'H', 'E', 'D', 'R')
	sub = append(sub, byte(len(hedr)), byte(len(hedr)>>8))
	sub = append(sub, hedr...)

	// CNAM (author)
	auth := []byte("test\x00")
	sub = append(sub, 'C', 'N', 'A', 'M')
	sub = append(sub, byte(len(auth)), byte(len(auth)>>8))
	sub = append(sub, auth...)

	// MAST + DATA pairs.
	for _, m := range masters {
		mb := append([]byte(m), 0)
		sub = append(sub, 'M', 'A', 'S', 'T')
		sub = append(sub, byte(len(mb)), byte(len(mb)>>8))
		sub = append(sub, mb...)

		// DATA — 8 bytes filesize, ignored.
		sub = append(sub, 'D', 'A', 'T', 'A')
		sub = append(sub, 8, 0)
		sub = append(sub, 0, 0, 0, 0, 0, 0, 0, 0)
	}

	// 24-byte TES4 record header, then sub.
	out := make([]byte, 24)
	copy(out[:4], "TES4")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(sub)))
	binary.LittleEndian.PutUint32(out[8:12], uint32(flags))
	out = append(out, sub...)
	return out
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseHeader_MastersAndFlags(t *testing.T) {
	cases := []struct {
		name        string
		flags       uint16
		masters     []string
		wantMasters []string
		wantLight   bool
	}{
		{"plain esp", 0, []string{"Skyrim.esm", "Update.esm"}, []string{"Skyrim.esm", "Update.esm"}, false},
		{"esm flag", 0x0001, []string{}, nil, false},
		{"esl flag", 0x0200, []string{"Skyrim.esm"}, []string{"Skyrim.esm"}, true},
		{"esm + esl", 0x0201, []string{}, nil, true},
		{"no masters", 0, []string{}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := buildTES4(tc.flags, tc.masters)
			path := writeTemp(t, "test.esp", data)
			h, err := ParseHeader(context.Background(), path)
			if err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if h.IsLight != tc.wantLight {
				t.Errorf("IsLight = %v, want %v", h.IsLight, tc.wantLight)
			}
			if len(h.Masters) != len(tc.wantMasters) {
				t.Fatalf("Masters = %v, want %v", h.Masters, tc.wantMasters)
			}
			for i, m := range tc.wantMasters {
				if h.Masters[i] != m {
					t.Errorf("Masters[%d] = %q, want %q", i, h.Masters[i], m)
				}
			}
		})
	}
}

func TestParseHeader_BadMagic(t *testing.T) {
	data := buildTES4(0, nil)
	copy(data[:4], "TES3")
	path := writeTemp(t, "morrowind.esp", data)
	if _, err := ParseHeader(context.Background(), path); err == nil {
		t.Error("expected error on bad magic")
	}
}

func TestParseHeader_TooSmall(t *testing.T) {
	path := writeTemp(t, "tiny.esp", []byte("TES4xx"))
	if _, err := ParseHeader(context.Background(), path); err == nil {
		t.Error("expected error on too-small file")
	}
}

func TestParseHeader_OversizeSubrecord(t *testing.T) {
	// Construct a malicious MAST claiming a 60000-byte payload — exceeds
	// maxMasterLen (1024) and must be rejected.
	var sub []byte
	sub = append(sub, 'M', 'A', 'S', 'T')
	sub = append(sub, 0x60, 0xEA) // 60000 LE
	out := make([]byte, 24)
	copy(out[:4], "TES4")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(sub)+1024))
	out = append(out, sub...)
	out = append(out, make([]byte, 1024)...)
	path := writeTemp(t, "evil.esp", out)
	if _, err := ParseHeader(context.Background(), path); err == nil {
		t.Error("expected error on oversize subrecord payload")
	}
}

func TestHeaderCache_HitOnRepeatedGet(t *testing.T) {
	data := buildTES4(0, []string{"Skyrim.esm"})
	path := writeTemp(t, "x.esp", data)
	c := NewHeaderCache(0)
	h1, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Error("expected cache to return same Header pointer on hit")
	}
}

func TestHeaderCache_InvalidatesOnMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.esp")
	if err := os.WriteFile(path, buildTES4(0, []string{"Skyrim.esm"}), 0644); err != nil {
		t.Fatal(err)
	}
	c := NewHeaderCache(0)
	h1, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite with different masters and bumped mtime.
	if err := os.WriteFile(path, buildTES4(0, []string{"Update.esm"}), 0644); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly: some filesystems coalesce sub-second writes.
	if err := os.Chtimes(path, fakeFutureTime(), fakeFutureTime()); err != nil {
		t.Fatal(err)
	}
	h2, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("expected fresh parse after mtime change")
	}
	if len(h2.Masters) != 1 || h2.Masters[0] != "Update.esm" {
		t.Errorf("Masters = %v, want [Update.esm]", h2.Masters)
	}
}
