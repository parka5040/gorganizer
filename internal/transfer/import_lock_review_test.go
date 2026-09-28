package transfer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestImportStateLockIsNeverNested checks that each replacement check holds the state lock exactly once under the profile lock.
func TestImportStateLockIsNeverNested(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "profile.json"), "existing profile")
	stateDepth := 0
	profileHeld := false
	checks := 0
	_, err := Import(context.Background(), ImportOptions{
		GameID:      testGame,
		ArchivePath: archive,
		Policy:      dto.PolicyRename,
		LockProfiles: func() func() {
			if profileHeld {
				t.Error("nested profile lock")
			}
			profileHeld = true
			return func() { profileHeld = false }
		},
		LockState: func() func() {
			stateDepth++
			if stateDepth != 1 {
				t.Errorf("nested state lock depth = %d", stateDepth)
			}
			return func() { stateDepth-- }
		},
		CheckReplacement: func(root, name string) error {
			checks++
			if !profileHeld || stateDepth != 1 {
				t.Errorf("checking %s/%s with profile lock %v and state depth %d", root, name, profileHeld, stateDepth)
			}
			return nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stateDepth != 0 || profileHeld || checks < 5 {
		t.Fatalf("final state: depth %d, profile lock %v, checks %d", stateDepth, profileHeld, checks)
	}
}
