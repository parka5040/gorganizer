package download

import "testing"

func TestParseNXM(t *testing.T) {
	uri := "nxm://skyrimspecialedition/mods/12345/files/67890?key=abc123&expires=1234567890"
	link, err := ParseNXM(uri)
	if err != nil {
		t.Fatalf("ParseNXM: %v", err)
	}
	if link.GameSlug != "skyrimspecialedition" {
		t.Errorf("GameSlug = %q", link.GameSlug)
	}
	if link.ModID != 12345 {
		t.Errorf("ModID = %d", link.ModID)
	}
	if link.FileID != 67890 {
		t.Errorf("FileID = %d", link.FileID)
	}
	if link.Key != "abc123" {
		t.Errorf("Key = %q", link.Key)
	}
	if link.Expires != 1234567890 {
		t.Errorf("Expires = %d", link.Expires)
	}
}

func TestParseNXM_GameID(t *testing.T) {
	tests := []struct {
		slug   string
		gameID string
	}{
		{"skyrimspecialedition", "skyrimse"},
		{"skyrim", "skyrim"},
		{"newvegas", "falloutnv"},
		{"fallout3", "fallout3"},
		{"fallout4", "fallout4"},
		{"oblivion", "oblivion"},
		{"morrowind", "morrowind"},
		{"starfield", "starfield"},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			uri := "nxm://" + tt.slug + "/mods/1/files/1"
			link, err := ParseNXM(uri)
			if err != nil {
				t.Fatalf("ParseNXM: %v", err)
			}
			gameID, err := link.GameID()
			if err != nil {
				t.Fatalf("GameID: %v", err)
			}
			if gameID != tt.gameID {
				t.Errorf("GameID = %q, want %q", gameID, tt.gameID)
			}
		})
	}
}

func TestParseNXM_InvalidScheme(t *testing.T) {
	_, err := ParseNXM("http://example.com")
	if err == nil {
		t.Error("expected error for non-nxm scheme")
	}
}

func TestParseNXM_BadPath(t *testing.T) {
	_, err := ParseNXM("nxm://skyrim/bad/path")
	if err == nil {
		t.Error("expected error for bad path format")
	}
}

func TestParseNXM_UnknownSlug(t *testing.T) {
	link, _ := ParseNXM("nxm://unknowngame/mods/1/files/1")
	_, err := link.GameID()
	if err == nil {
		t.Error("expected error for unknown game slug")
	}
}
