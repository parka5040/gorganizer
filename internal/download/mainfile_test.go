package download

import (
	"errors"
	"testing"
)

func TestSelectMainFile(t *testing.T) {
	tests := []struct {
		name    string
		files   []NexusFileDetails
		opts    MainFileOptions
		wantID  int
		wantErr error
	}{
		{
			name: "rejects GOG mention in Name",
			files: []NexusFileDetails{
				{FileID: 100, Name: "GOG build", CategoryName: "MAIN"},
				{FileID: 40, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RejectMentions: []string{"gog"}},
			wantID: 40,
		},
		{
			name: "rejects GOG mention in FileName",
			files: []NexusFileDetails{
				{FileID: 100, FileName: "loader-gog.7z", CategoryName: "MAIN"},
				{FileID: 40, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RejectMentions: []string{"gog"}},
			wantID: 40,
		},
		{
			name: "rejects GOG mention in Description",
			files: []NexusFileDetails{
				{FileID: 100, Description: "For GOG users", CategoryName: "MAIN"},
				{FileID: 40, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RejectMentions: []string{"gog"}},
			wantID: 40,
		},
		{
			name: "prefers Steam mention over newer non-Steam file",
			files: []NexusFileDetails{
				{FileID: 20, Name: "Steam build", CategoryName: "MAIN"},
				{FileID: 30, Name: "Universal build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{PreferMentions: []string{"steam"}},
			wantID: 20,
		},
		{
			name: "prefers later Steam file over earlier non-Steam file with higher file ID",
			files: []NexusFileDetails{
				{FileID: 50, Name: "Universal build", CategoryName: "MAIN"},
				{FileID: 20, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{PreferMentions: []string{"steam"}},
			wantID: 20,
		},
		{
			name: "applies script-extender policy of reject, runtime filter, preference, and newest file ID",
			files: []NexusFileDetails{
				{FileID: 99, Name: "Steam 1.6.1170 extras", CategoryName: "OPTIONAL"},
				{FileID: 90, Name: "Steam/GOG 1.6.1170", CategoryName: "MAIN"},
				{FileID: 80, Name: "Steam build 1.6.640", CategoryName: "MAIN"},
				{FileID: 70, Name: "Universal build 1.6.1170", CategoryName: "MAIN"},
				{FileID: 30, Name: "Steam build", FileName: "se_1_6_1170.7z", CategoryName: "MAIN"},
				{FileID: 40, Name: "Steam update", Description: "runtime 1-6-1170", CategoryName: "MAIN"},
			},
			opts: MainFileOptions{
				RejectMentions:      []string{"gog"},
				PreferMentions:      []string{"steam"},
				RequireVersionAnyOf: []string{"1.6.1170"},
			},
			wantID: 40,
		},
		{
			name: "uses newest file ID among equal preferences",
			files: []NexusFileDetails{
				{FileID: 20, Name: "Steam build", CategoryName: "MAIN"},
				{FileID: 30, Name: "Steam update", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{PreferMentions: []string{"steam"}},
			wantID: 30,
		},
		{
			name: "ignores non-MAIN files",
			files: []NexusFileDetails{
				{FileID: 30, Name: "Steam optional", CategoryName: "OPTIONAL"},
				{FileID: 20, Name: "Steam main", CategoryName: "main"},
			},
			opts:   MainFileOptions{PreferMentions: []string{"steam"}},
			wantID: 20,
		},
		{
			name: "runtime needle matches underscore form",
			files: []NexusFileDetails{
				{FileID: 10, Name: "runtime 1_6_1170", CategoryName: "MAIN"},
				{FileID: 30, Name: "runtime 1.6.640", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RequireVersionAnyOf: []string{"1.6.1170"}},
			wantID: 10,
		},
		{
			name: "runtime needle matches hyphen form and filters out others",
			files: []NexusFileDetails{
				{FileID: 20, Name: "runtime 1-6-1170", CategoryName: "MAIN"},
				{FileID: 30, Name: "runtime 1.6.640", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RequireVersionAnyOf: []string{"1.6.1170"}},
			wantID: 20,
		},
		{
			name: "ignores blank reject needles",
			files: []NexusFileDetails{
				{FileID: 10, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RejectMentions: []string{"", "  "}},
			wantID: 10,
		},
		{
			name: "ignores blank prefer needles",
			files: []NexusFileDetails{
				{FileID: 30, Name: "Universal build", CategoryName: "MAIN"},
				{FileID: 20, Name: "Steam build", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{PreferMentions: []string{"", "steam"}},
			wantID: 20,
		},
		{
			name: "ignores blank version needles alongside a real version",
			files: []NexusFileDetails{
				{FileID: 30, Name: "runtime 1.6.640", CategoryName: "MAIN"},
				{FileID: 20, Name: "runtime 1_6_1170", CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{RequireVersionAnyOf: []string{"", "1.6.1170"}},
			wantID: 20,
		},
		{
			name: "blank version needle alone matches nothing",
			files: []NexusFileDetails{
				{FileID: 10, Name: "runtime 1.6.640", CategoryName: "MAIN"},
			},
			opts:    MainFileOptions{RequireVersionAnyOf: []string{""}},
			wantErr: ErrNoMainFile,
		},
		{
			name: "rejects preference combined with unambiguous mode",
			files: []NexusFileDetails{
				{FileID: 10, Name: "Steam build", CategoryName: "MAIN", IsPrimary: true},
			},
			opts:    MainFileOptions{PreferMentions: []string{"steam"}, FailOnAmbiguous: true},
			wantErr: ErrInvalidMainFileOptions,
		},
		{
			name:    "returns no-main-file error when no candidates remain",
			files:   []NexusFileDetails{{FileID: 10, CategoryName: "OPTIONAL"}},
			wantErr: ErrNoMainFile,
		},
		{
			name: "allows a single MAIN file in unambiguous mode",
			files: []NexusFileDetails{
				{FileID: 10, CategoryName: "MAIN"},
			},
			opts:   MainFileOptions{FailOnAmbiguous: true},
			wantID: 10,
		},
		{
			name: "uses the sole primary file in unambiguous mode",
			files: []NexusFileDetails{
				{FileID: 10, CategoryName: "MAIN"},
				{FileID: 20, CategoryName: "MAIN", IsPrimary: true},
			},
			opts:   MainFileOptions{FailOnAmbiguous: true},
			wantID: 20,
		},
		{
			name: "rejects multiple non-primary files in unambiguous mode",
			files: []NexusFileDetails{
				{FileID: 10, CategoryName: "MAIN"},
				{FileID: 20, CategoryName: "MAIN"},
			},
			opts:    MainFileOptions{FailOnAmbiguous: true},
			wantErr: ErrAmbiguousMainFile,
		},
		{
			name: "rejects multiple primary files in unambiguous mode",
			files: []NexusFileDetails{
				{FileID: 10, CategoryName: "MAIN", IsPrimary: true},
				{FileID: 20, CategoryName: "MAIN", IsPrimary: true},
			},
			opts:    MainFileOptions{FailOnAmbiguous: true},
			wantErr: ErrAmbiguousMainFile,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			got, err := SelectMainFile(tc.files, tc.opts)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SelectMainFile() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if got == nil || got.FileID != tc.wantID {
				t.Fatalf("SelectMainFile() = %#v, want file ID %d", got, tc.wantID)
			}
		})
	}
}
