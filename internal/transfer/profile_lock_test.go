package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestImportFinalizesProfilesUnderTheProfileLock locks that each profile moves into place only while LockProfiles is held.
func TestImportFinalizesProfilesUnderTheProfileLock(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	held := false
	calls := 0
	var sawUnlocked []string
	_, err := Import(context.Background(), ImportOptions{
		GameID:      testGame,
		ArchivePath: archive,
		Policy:      dto.PolicyAbort,
		LockMod: func(name string) func() {
			if held {
				sawUnlocked = append(sawUnlocked, "mod lock taken under the profile lock: "+name)
			}
			return func() {}
		},
		LockProfiles: func() func() {
			calls++
			held = true
			return func() {
				entries, _ := os.ReadDir(config.ProfilesDir(testGame))
				for _, e := range entries {
					if _, err := os.Stat(filepath.Join(config.ProfilesDir(testGame), e.Name(), "profile.json")); err == nil && e.Name()[0] != '.' {
						held = false
						return
					}
				}
				sawUnlocked = append(sawUnlocked, "profile lock released before any profile was in place")
				held = false
			}
		},
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if calls != 1 {
		t.Errorf("LockProfiles calls = %d, want one per imported profile", calls)
	}
	if held {
		t.Error("profile lock still held after Import")
	}
	for _, problem := range sawUnlocked {
		t.Error(problem)
	}
}
