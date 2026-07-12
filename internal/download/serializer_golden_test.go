package download

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// setDownloadRoot points GORGANIZER_ROOT at a temp dir and returns the downloads dir for gameID.
func setDownloadRoot(t *testing.T, gameID string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GORGANIZER_ROOT", root)
	return filepath.Join(root, gameID+"_Mods", "Downloads")
}

func TestSaveIndexGoldenBytes(t *testing.T) {
	tests := []struct {
		name       string
		idx        *DownloadsIndex
		want       string
		wantLoaded *DownloadsIndex
	}{
		{
			name: "empty index",
			idx:  &DownloadsIndex{},
			want: `# Gorganizer downloads index — auto-generated
archives:
`,
			wantLoaded: &DownloadsIndex{},
		},
		{
			name: "entries with quirky paths",
			idx: &DownloadsIndex{Archives: []IndexEntry{
				{Path: "266_Unofficial Patch/Unofficial Patch-266-4-3-8a.7z", ModID: 266, FileID: 733846},
				{Path: `Weird "Quotes"/mod.zip`, ModID: 1, FileID: 2, Hidden: true, Uninstalled: true},
				{Path: "héllo — ★/アーカイブ.rar", ModID: 0, FileID: 0},
			}},
			want: `# Gorganizer downloads index — auto-generated
archives:
  - path: "266_Unofficial Patch/Unofficial Patch-266-4-3-8a.7z"
    mod_id: 266
    file_id: 733846
    hidden: false
    uninstalled: false
  - path: "Weird \"Quotes\"/mod.zip"
    mod_id: 1
    file_id: 2
    hidden: true
    uninstalled: true
  - path: "héllo — ★/アーカイブ.rar"
    mod_id: 0
    file_id: 0
    hidden: false
    uninstalled: false
`,
			wantLoaded: &DownloadsIndex{Archives: []IndexEntry{
				{Path: "266_Unofficial Patch/Unofficial Patch-266-4-3-8a.7z", ModID: 266, FileID: 733846},
				{Path: `Weird \"Quotes\"/mod.zip`, ModID: 1, FileID: 2, Hidden: true, Uninstalled: true},
				{Path: "héllo — ★/アーカイブ.rar", ModID: 0, FileID: 0},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dlDir := setDownloadRoot(t, "testgame")
			if err := SaveIndex("testgame", tt.idx); err != nil {
				t.Fatalf("SaveIndex: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dlDir, "metadata.yaml"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("bytes = %q, want %q", got, tt.want)
			}
			loaded, err := LoadIndex("testgame")
			if err != nil {
				t.Fatalf("LoadIndex: %v", err)
			}
			if !reflect.DeepEqual(loaded, tt.wantLoaded) {
				t.Errorf("loaded = %+v, want %+v", loaded, tt.wantLoaded)
			}
		})
	}
}

func TestIndexPathRoundTripQuirks(t *testing.T) {
	tests := []struct {
		name  string
		saved string
		want  string
	}{
		{name: "plain", saved: "plain.zip", want: "plain.zip"},
		{name: "inner quotes keep escapes", saved: `a"b.zip`, want: `a\"b.zip`},
		{name: "trailing quote becomes backslash", saved: `ends"`, want: `ends\`},
		{name: "outer spaces dropped", saved: "  spaced.zip  ", want: "spaced.zip"},
		{name: "empty stays empty", saved: "", want: ""},
		{name: "colon preserved", saved: "colon: name.zip", want: "colon: name.zip"},
		{name: "hash preserved", saved: "hash # tag.zip", want: "hash # tag.zip"},
		{name: "unicode preserved", saved: "ünïcode ★.7z", want: "ünïcode ★.7z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDownloadRoot(t, "testgame")
			in := &DownloadsIndex{Archives: []IndexEntry{{Path: tt.saved, ModID: 7, FileID: 8}}}
			if err := SaveIndex("testgame", in); err != nil {
				t.Fatalf("SaveIndex: %v", err)
			}
			loaded, err := LoadIndex("testgame")
			if err != nil {
				t.Fatalf("LoadIndex: %v", err)
			}
			if len(loaded.Archives) != 1 || loaded.Archives[0].Path != tt.want {
				t.Errorf("loaded = %+v, want single entry with path %q", loaded.Archives, tt.want)
			}
		})
	}
}

func TestIndexByteStability(t *testing.T) {
	fixture := `# Gorganizer downloads index — auto-generated
archives:
  - path: "266_Unofficial Patch/Unofficial Patch-266-4-3-8a-1774132896.7z"
    mod_id: 266
    file_id: 733846
    hidden: false
    uninstalled: false
  - path: "39878_Dragon Follower/Dragon Follower-39878-1-5-1684787099.zip"
    mod_id: 39878
    file_id: 390989
    hidden: true
    uninstalled: true
`
	dlDir := setDownloadRoot(t, "testgame")
	if err := os.MkdirAll(dlDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dlDir, "metadata.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadIndex("testgame")
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if err := SaveIndex("testgame", loaded); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("Save(Load(fixture)) = %q, want %q", got, fixture)
	}
}

func TestLoadIndexMissingFileReturnsEmpty(t *testing.T) {
	setDownloadRoot(t, "testgame")
	got, err := LoadIndex("testgame")
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if !reflect.DeepEqual(got, &DownloadsIndex{}) {
		t.Errorf("got %+v, want empty index", got)
	}
}

func TestSaveSidecarGoldenBytes(t *testing.T) {
	s := ArchiveSidecar{
		ModID:           65906,
		ModName:         `Console "Paste" Support`,
		GameDomain:      "newvegas",
		ThumbnailURL:    "https://staticdelivery.example.invalid/mods/130/images/65906.jpg",
		AdultContent:    true,
		FileID:          1000122282,
		FileName:        "Console Paste Support 2.3",
		FileArchiveName: "Console Paste-65906-2-3-1705873242.zip",
		Version:         "2.3",
		Category:        "main",
		UploadedAt:      "2024-01-21T21:40:42Z",
		DownloadedAt:    "2026-05-01T18:39:48Z",
		SizeBytes:       3861512192,
	}
	want := `# Gorganizer archive metadata — auto-generated
mod_id: 65906
mod_name: "Console \"Paste\" Support"
game_domain: "newvegas"
thumbnail_url: "https://staticdelivery.example.invalid/mods/130/images/65906.jpg"
adult_content: true
file_id: 1000122282
file_name: "Console Paste Support 2.3"
file_archive_name: "Console Paste-65906-2-3-1705873242.zip"
version: "2.3"
category: "main"
uploaded_at: "2024-01-21T21:40:42Z"
downloaded_at: "2026-05-01T18:39:48Z"
size_bytes: 3861512192
`
	archive := filepath.Join(t.TempDir(), "archive.zip")
	if err := SaveSidecar(archive, s, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("SaveSidecar: %v", err)
	}
	got, err := os.ReadFile(SidecarPath(archive))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
	loaded, err := LoadSidecar(archive)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	wantLoaded := s
	wantLoaded.ModName = `Console \"Paste\" Support`
	if *loaded != wantLoaded {
		t.Errorf("loaded = %+v, want %+v", *loaded, wantLoaded)
	}
}

func TestSaveSidecarFillsEmptyDownloadedAt(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "archive.zip")
	at := time.Date(2026, 7, 11, 3, 4, 5, 987654321, time.UTC)
	if err := SaveSidecar(archive, ArchiveSidecar{}, at); err != nil {
		t.Fatalf("SaveSidecar: %v", err)
	}
	loaded, err := LoadSidecar(archive)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if loaded.DownloadedAt != "2026-07-11T03:04:05Z" {
		t.Errorf("DownloadedAt = %q, want %q", loaded.DownloadedAt, "2026-07-11T03:04:05Z")
	}
}

func TestSidecarByteStability(t *testing.T) {
	fixture := `# Gorganizer archive metadata — auto-generated
mod_id: 39878
mod_name: "Dragon Follower"
game_domain: "skyrimspecialedition"
thumbnail_url: "https://staticdelivery.example.invalid/mods/1704/images/39878.jpg"
adult_content: false
file_id: 390989
file_name: "Dragon Follower 1.5"
file_archive_name: "Dragon Follower-39878-1-5-1684787099.zip"
version: "1.5"
category: "main"
uploaded_at: "2023-05-22T20:24:59Z"
downloaded_at: "2026-06-01T10:00:00Z"
size_bytes: 104857600
`
	archive := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(SidecarPath(archive), []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadSidecar(archive)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if err := SaveSidecar(archive, *loaded, time.Now()); err != nil {
		t.Fatalf("SaveSidecar: %v", err)
	}
	got, err := os.ReadFile(SidecarPath(archive))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("Save(Load(fixture)) = %q, want %q", got, fixture)
	}
}

// ledgerEntryEqual compares two ledger entries, using time.Equal for timestamps.
func ledgerEntryEqual(a, b LedgerEntry) bool {
	return a.ID == b.ID && a.NXMURI == b.NXMURI && a.GameSlug == b.GameSlug &&
		a.GameID == b.GameID && a.ModID == b.ModID && a.FileID == b.FileID &&
		a.ArchiveRelPath == b.ArchiveRelPath && a.BytesDone == b.BytesDone &&
		a.BytesTotal == b.BytesTotal && a.StartedAt.Equal(b.StartedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt) && a.Status == b.Status && a.Error == b.Error
}

func TestSaveLedgerGoldenBytes(t *testing.T) {
	entries := []LedgerEntry{
		{
			ID:             "f47ac10b-58cc-4372-a567-0e02b2c3d479",
			NXMURI:         `nxm://SkyrimSE/mods/266/files/733846?key=a"b&expires=1`,
			GameSlug:       "skyrimse",
			GameID:         "testgame",
			ModID:          266,
			FileID:         733846,
			ArchiveRelPath: "266_Unofficial Patch/patch.7z",
			BytesDone:      1024,
			BytesTotal:     4096,
			StartedAt:      time.Date(2026, 5, 1, 18, 39, 48, 0, time.UTC),
			UpdatedAt:      time.Date(2026, 5, 1, 18, 40, 2, 123456789, time.UTC),
			Status:         LedgerDownloading,
		},
		{
			ID:     "dl-0002",
			GameID: "testgame",
			Status: LedgerFailed,
			Error:  `server said: "no"`,
		},
	}
	want := `# Gorganizer in-flight downloads ledger — auto-generated
inflight:
  - id: "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    nxm_uri: "nxm://SkyrimSE/mods/266/files/733846?key=a\"b&expires=1"
    game_slug: "skyrimse"
    mod_id: 266
    file_id: 733846
    archive_rel: "266_Unofficial Patch/patch.7z"
    bytes_done: 1024
    bytes_total: 4096
    started_at: "2026-05-01T18:39:48Z"
    updated_at: "2026-05-01T18:40:02.123456789Z"
    status: "downloading"
  - id: "dl-0002"
    nxm_uri: ""
    game_slug: ""
    mod_id: 0
    file_id: 0
    archive_rel: ""
    bytes_done: 0
    bytes_total: 0
    status: "failed"
    error: "server said: \"no\""
`
	dlDir := setDownloadRoot(t, "testgame")
	if err := SaveLedger("testgame", entries); err != nil {
		t.Fatalf("SaveLedger: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dlDir, "inflight.yaml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
	loaded, err := LoadLedger("testgame")
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	wantLoaded := entries
	wantLoaded[0].NXMURI = `nxm://SkyrimSE/mods/266/files/733846?key=a\"b&expires=1`
	wantLoaded[1].Error = `server said: \"no\`
	if len(loaded) != len(wantLoaded) {
		t.Fatalf("loaded %d entries, want %d", len(loaded), len(wantLoaded))
	}
	for i := range wantLoaded {
		if !ledgerEntryEqual(loaded[i], wantLoaded[i]) {
			t.Errorf("entry %d = %+v, want %+v", i, loaded[i], wantLoaded[i])
		}
	}
}

func TestLedgerByteStability(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{
			name: "empty ledger",
			fixture: `# Gorganizer in-flight downloads ledger — auto-generated
inflight:
`,
		},
		{
			name: "single inflight entry",
			fixture: `# Gorganizer in-flight downloads ledger — auto-generated
inflight:
  - id: "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    nxm_uri: "nxm://FalloutNV/mods/65906/files/1000122282?key=abc&expires=1746124788&user_id=1234567"
    game_slug: "newvegas"
    mod_id: 65906
    file_id: 1000122282
    archive_rel: "65906_Console Paste Support/Console Paste-65906-2-3-1705873242.zip"
    bytes_done: 524288
    bytes_total: 1048576
    started_at: "2026-05-01T18:39:40Z"
    updated_at: "2026-05-01T18:39:48.5Z"
    status: "downloading"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dlDir := setDownloadRoot(t, "testgame")
			if err := os.MkdirAll(dlDir, 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			path := filepath.Join(dlDir, "inflight.yaml")
			if err := os.WriteFile(path, []byte(tt.fixture), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			loaded, err := LoadLedger("testgame")
			if err != nil {
				t.Fatalf("LoadLedger: %v", err)
			}
			if err := SaveLedger("testgame", loaded); err != nil {
				t.Fatalf("SaveLedger: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.fixture {
				t.Errorf("Save(Load(fixture)) = %q, want %q", got, tt.fixture)
			}
		})
	}
}

func TestLoadLedgerMissingFileReturnsNil(t *testing.T) {
	setDownloadRoot(t, "testgame")
	got, err := LoadLedger("testgame")
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestSaveModMetadataGoldenBytes(t *testing.T) {
	tests := []struct {
		name       string
		meta       *ModMetadata
		want       string
		wantLoaded *ModMetadata
	}{
		{
			name: "minimal metadata omits optional keys",
			meta: &ModMetadata{Name: "Bare", Folder: "Bare"},
			want: `# Gorganizer mod metadata — auto-generated
name: "Bare"
folder: "Bare"
installed: ""
category: ""
version: ""
enabled: false
file_count: 0
source_archives:
files:
`,
			wantLoaded: &ModMetadata{Name: "Bare", Folder: "Bare"},
		},
		{
			name: "full metadata with quirky values",
			meta: &ModMetadata{
				Name:        "Console Paste Support",
				Folder:      "Console Paste Support",
				Installed:   "2026-05-01T18:39:48Z",
				Category:    "main",
				Version:     "2.3",
				Enabled:     true,
				FileCount:   2,
				ModPage:     "https://www.nexusmods.com/newvegas/mods/65906",
				TrueIndex:   "0000000000000020",
				VisualIndex: "0000000000000030",
				Separator:   "Core",
				SourceArchives: []SourceArchiveRef{
					{Path: "Downloads/65906_Console Paste Support/Console Paste-65906-2-3-1705873242.zip", ModID: 65906, FileID: 1000122282, InstalledAt: "2026-05-01T18:39:48Z"},
					{Path: `merged "extra".zip`, ModID: 1, FileID: 2, Merged: true},
				},
				Files: []string{"NVSE/Plugins/nvse_console_clipboard.dll", `docs/read "me".txt`},
			},
			want: `# Gorganizer mod metadata — auto-generated
name: "Console Paste Support"
folder: "Console Paste Support"
installed: "2026-05-01T18:39:48Z"
category: "main"
version: "2.3"
enabled: true
file_count: 2
mod_page: "https://www.nexusmods.com/newvegas/mods/65906"
true_index: "0000000000000020"
visual_index: "0000000000000030"
separator: "Core"
source_archives:
  - path: "Downloads/65906_Console Paste Support/Console Paste-65906-2-3-1705873242.zip"
    mod_id: 65906
    file_id: 1000122282
    installed_at: "2026-05-01T18:39:48Z"
  - path: "merged \"extra\".zip"
    mod_id: 1
    file_id: 2
    installed_at: ""
    merged: true
files:
  - "NVSE/Plugins/nvse_console_clipboard.dll"
  - "docs/read \"me\".txt"
`,
			wantLoaded: &ModMetadata{
				Name:        "Console Paste Support",
				Folder:      "Console Paste Support",
				Installed:   "2026-05-01T18:39:48Z",
				Category:    "main",
				Version:     "2.3",
				Enabled:     true,
				FileCount:   2,
				ModPage:     "https://www.nexusmods.com/newvegas/mods/65906",
				TrueIndex:   "0000000000000020",
				VisualIndex: "0000000000000030",
				Separator:   "Core",
				SourceArchives: []SourceArchiveRef{
					{Path: "Downloads/65906_Console Paste Support/Console Paste-65906-2-3-1705873242.zip", ModID: 65906, FileID: 1000122282, InstalledAt: "2026-05-01T18:39:48Z"},
					{Path: `merged \"extra\".zip`, ModID: 1, FileID: 2, Merged: true},
				},
				Files: []string{"NVSE/Plugins/nvse_console_clipboard.dll", `docs/read \"me\".txt`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modDir := t.TempDir()
			if err := SaveModMetadata(modDir, tt.meta); err != nil {
				t.Fatalf("SaveModMetadata: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(modDir, "metadata.yaml"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("bytes = %q, want %q", got, tt.want)
			}
			loaded, err := LoadModMetadata(modDir)
			if err != nil {
				t.Fatalf("LoadModMetadata: %v", err)
			}
			if !reflect.DeepEqual(loaded, tt.wantLoaded) {
				t.Errorf("loaded = %+v, want %+v", loaded, tt.wantLoaded)
			}
		})
	}
}

func TestModMetadataNameRoundTripQuirks(t *testing.T) {
	tests := []struct {
		name  string
		saved string
		want  string
	}{
		{name: "plain", saved: "Plain Mod", want: "Plain Mod"},
		{name: "inner quotes keep escapes", saved: `a"b`, want: `a\"b`},
		{name: "trailing quote becomes backslash", saved: `x"`, want: `x\`},
		{name: "outer spaces dropped", saved: "  padded  ", want: "padded"},
		{name: "empty stays empty", saved: "", want: ""},
		{name: "colon preserved", saved: "has: colon", want: "has: colon"},
		{name: "leading hash preserved", saved: "#hashtag", want: "#hashtag"},
		{name: "unicode preserved", saved: "uni — ★ 日本語", want: "uni — ★ 日本語"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modDir := t.TempDir()
			if err := SaveModMetadata(modDir, &ModMetadata{Name: tt.saved}); err != nil {
				t.Fatalf("SaveModMetadata: %v", err)
			}
			loaded, err := LoadModMetadata(modDir)
			if err != nil {
				t.Fatalf("LoadModMetadata: %v", err)
			}
			if loaded.Name != tt.want {
				t.Errorf("Name = %q, want %q", loaded.Name, tt.want)
			}
		})
	}
}

func TestModMetadataFilesRoundTripQuirks(t *testing.T) {
	tests := []struct {
		name  string
		saved string
		want  string
	}{
		{name: "plain", saved: "textures/sword.dds", want: "textures/sword.dds"},
		{name: "leading spaces preserved", saved: "  lead.txt", want: "  lead.txt"},
		{name: "trailing spaces preserved", saved: "trail.txt  ", want: "trail.txt  "},
		{name: "inner quotes keep escapes", saved: `a"b.txt`, want: `a\"b.txt`},
		{name: "leading hash preserved", saved: "#hash.txt", want: "#hash.txt"},
		{name: "colon preserved", saved: "dir/a: b.txt", want: "dir/a: b.txt"},
		{name: "unicode preserved", saved: "méshes/★.nif", want: "méshes/★.nif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modDir := t.TempDir()
			if err := SaveModMetadata(modDir, &ModMetadata{Name: "m", Files: []string{tt.saved}}); err != nil {
				t.Fatalf("SaveModMetadata: %v", err)
			}
			loaded, err := LoadModMetadata(modDir)
			if err != nil {
				t.Fatalf("LoadModMetadata: %v", err)
			}
			if len(loaded.Files) != 1 || loaded.Files[0] != tt.want {
				t.Errorf("Files = %q, want [%q]", loaded.Files, tt.want)
			}
		})
	}
}

func TestLoadModMetadataLegacySourceArchiveKey(t *testing.T) {
	content := `name: "Old Mod"
folder: "Old Mod"
enabled: true
source_archive: "Downloads/old.zip"
`
	modDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(modDir, "metadata.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadModMetadata(modDir)
	if err != nil {
		t.Fatalf("LoadModMetadata: %v", err)
	}
	want := []SourceArchiveRef{{Path: "Downloads/old.zip"}}
	if !reflect.DeepEqual(loaded.SourceArchives, want) {
		t.Errorf("SourceArchives = %+v, want %+v", loaded.SourceArchives, want)
	}
}

func TestModMetadataByteStability(t *testing.T) {
	fixture := `# Gorganizer mod metadata — auto-generated
name: "Iron Sword Retexture"
folder: "Iron Sword Retexture"
installed: "2026-06-12T09:15:30Z"
category: "optional"
version: "1.4"
enabled: true
file_count: 2
mod_page: "https://www.nexusmods.com/skyrimse/mods/12345"
true_index: "0000000000000040"
visual_index: "0000000000000050"
separator: "Textures"
source_archives:
  - path: "Downloads/12345_Iron Sword Retexture/Iron Sword-12345-1-4-1679607226.7z"
    mod_id: 12345
    file_id: 67890
    installed_at: "2026-06-12T09:15:30Z"
files:
  - "textures/weapons/iron/sword.dds"
  - "textures/weapons/iron/sword_n.dds"
`
	modDir := t.TempDir()
	path := filepath.Join(modDir, "metadata.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadModMetadata(modDir)
	if err != nil {
		t.Fatalf("LoadModMetadata: %v", err)
	}
	if err := SaveModMetadata(modDir, loaded); err != nil {
		t.Fatalf("SaveModMetadata: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("Save(Load(fixture)) = %q, want %q", got, fixture)
	}
}

func TestLoadModMetadataMissingFileReturnsZero(t *testing.T) {
	loaded, err := LoadModMetadata(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("LoadModMetadata: %v", err)
	}
	if !reflect.DeepEqual(loaded, &ModMetadata{}) {
		t.Errorf("got %+v, want zero ModMetadata", loaded)
	}
}
