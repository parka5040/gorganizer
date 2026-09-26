package ipc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/transfer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// registeredTokenSamples returns one error per registered token that MapError must encode with that token.
func registeredTokenSamples() map[string]error {
	return map[string]error{
		tokenDaemonShuttingDown:         &dto.ShuttingDownError{Operation: "mount"},
		tokenGameRunning:                &dto.GameRunningError{GameID: "stardewvalley", Operation: dto.GameRunningOperationLaunch},
		tokenModRegistrationFailed:      &download.ModRegistrationError{Mod: "SkyUI"},
		tokenInvalidTargetMod:           &download.InvalidTargetModError{Name: ".."},
		tokenNotAMod:                    &smapi.NotAModError{Reason: smapi.ReasonFolderCollision, Detail: "Mod,mod"},
		tokenFomodUnsupported:           download.ErrFomodNotSupportedForLayout,
		tokenManifestLayoutInvalid:      &download.ManifestLayoutError{Mod: "CP", Reason: "root_manifest"},
		tokenModMounted:                 &download.ModMountedError{Mod: "SkyUI"},
		tokenFomodReinstallUnsupported:  &download.FomodReinstallUnsupportedError{Mod: "SkyUI"},
		tokenReinstallSourceMissing:     &download.ReinstallSourceMissingError{Mod: "SkyUI", Path: "Downloads/a.7z"},
		tokenModLoaderBusy:              &dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationLaunch},
		tokenModLoaderUnavailable:       &smapi.UnavailableError{GameID: "stardewvalley", State: smapi.StateNotInstalled},
		tokenModLoaderUnsupported:       &dto.ModLoaderUnsupportedError{GameID: "skyrimse"},
		tokenModDependenciesUnsupported: &dto.ModDependenciesUnsupportedError{GameID: "skyrimse"},
		tokenModLoaderFailed:            smapi.ErrStageIncomplete,
		tokenLoaderMissing:              &tools.LoaderMissingError{GameID: "skyrimse", Reason: "missing"},
		tokenArchiveMissing:             &daemon.ArchiveMissingError{GameID: "skyrimse", Path: "a.7z"},
		tokenFomodRequired:              &daemon.FomodRequiredError{GameID: "skyrimse", Path: "a.7z"},
		tokenModCollision:               &daemon.ModCollisionError{Name: "SkyUI"},
		tokenLayoutUnsupported:          &download.LayoutUnsupportedError{GameID: "stardewvalley", Layout: "x"},
		tokenUnsafePath:                 &daemon.UnsafePathError{Field: "mod_name"},
		tokenModNotFound:                &daemon.ModNotFoundError{GameID: "skyrimse", Name: "SkyUI"},
		tokenModInUse:                   &daemon.ModInUseError{Name: "SkyUI"},
		tokenNXMExpired:                 &download.NXMExpiredError{URI: "nxm://x"},
		tokenDownloadNotFound:           &download.DownloadNotFoundError{ID: "dl-1"},
		tokenPreviewNotFound:            &daemon.PreviewNotFoundError{PreviewID: "pv-1"},
		tokenVFSMutex:                   &daemon.VFSMutexError{GameID: "ttw", Conflicting: "falloutnv", Group: "fnv-data"},
		tokenLinkedParentMissing:        &daemon.ErrLinkedParentMissing{GameID: "ttw", ParentGameID: "falloutnv"},
		tokenTTWDrift:                   &daemon.TTWDriftError{InstallPath: "/games/FNV", Reason: "exe changed"},
		tokenPrefixMissing:              &tools.ErrPrefixMissing{GameID: "falloutnv", ExpectedPath: "/pfx"},
		tokenSteamNotRunning:            &tools.ErrSteamNotRunning{},
		tokenTTWRequiresVanillaFNV:      &daemon.ErrTTWRequiresVanillaFNV{},
		tokenXNVSEMissingForTTW:         &daemon.ErrXNVSEMissingForTTW{InstallPath: "/games/FNV"},
		tokenFNV4GBNotAppliedForTTW:     &daemon.ErrFNV4GBNotAppliedForTTW{InstallPath: "/games/FNV"},
		tokenTransferGameMismatch:       &transfer.TransferGameMismatchError{Want: "skyrimse", Got: "falloutnv"},
		tokenTransferSchema:             &transfer.TransferSchemaError{Version: 2},
		tokenTransferPath:               &transfer.TransferPathError{Entry: "../evil"},
		tokenTransferCollision:          &transfer.TransferCollisionError{Name: "SkyUI"},
		tokenTransferOverwriteMounted:   &daemon.TransferOverwriteMountedError{Name: "SkyUI"},
	}
}

// ownToken returns the registered token msg starts with, or "".
func ownToken(msg string) string {
	for _, token := range errorTokens {
		if strings.HasPrefix(msg, token) {
			return token
		}
	}
	return ""
}

// assertOnlyOwnToken fails unless msg starts with a registered token and embeds no other one.
func assertOnlyOwnToken(t *testing.T, name, msg string) {
	t.Helper()
	own := ownToken(msg)
	if own == "" {
		t.Errorf("%s message %q starts with no registered token", name, msg)
		return
	}
	for _, token := range errorTokens {
		if token != own && strings.Contains(msg, token) {
			t.Errorf("%s message %q contains registered token %q", name, msg, token)
		}
	}
}

// TestErrorTokenRegistryIsUniqueAndUnnested locks that every registered token is a distinct key: token that no other token contains.
func TestErrorTokenRegistryIsUniqueAndUnnested(t *testing.T) {
	seen := map[string]bool{}
	for _, token := range errorTokens {
		if seen[token] {
			t.Errorf("token %q registered twice", token)
		}
		seen[token] = true
		if !strings.HasSuffix(token, ":") || strings.Count(token, ":") != 1 || strings.Contains(token, "=") {
			t.Errorf("token %q is not a bare key followed by one colon", token)
		}
		for _, other := range errorTokens {
			if other != token && strings.Contains(other, token) {
				t.Errorf("token %q contains registered token %q", other, token)
			}
		}
	}
}

// TestMapErrorEmitsExactlyTheRegisteredTokens locks that every registered token is emitted by MapError and every emitted message carries only its own token.
func TestMapErrorEmitsExactlyTheRegisteredTokens(t *testing.T) {
	samples := registeredTokenSamples()
	if len(samples) != len(errorTokens) {
		t.Fatalf("%d token samples for %d registered tokens", len(samples), len(errorTokens))
	}
	for _, token := range errorTokens {
		sample, ok := samples[token]
		if !ok {
			t.Errorf("registered token %q has no sample error", token)
			continue
		}
		mapped, handled := MapError(fmt.Errorf("wrapped: %w", sample))
		if !handled {
			t.Errorf("MapError(%v) not handled", sample)
			continue
		}
		msg := status.Convert(mapped).Message()
		if !strings.HasPrefix(msg, token) {
			t.Errorf("sample for %q mapped to %q", token, msg)
		}
		assertOnlyOwnToken(t, token, msg)
	}
}

// TestMapErrorShuttingDownIsUnavailable locks the code and bare token of a refusal during shutdown.
func TestMapErrorShuttingDownIsUnavailable(t *testing.T) {
	mapped, handled := MapError(fmt.Errorf("mounting: %w", &dto.ShuttingDownError{Operation: "mount"}))
	if !handled {
		t.Fatal("shutting-down error not handled")
	}
	assertStatus(t, mapped, codes.Unavailable, "daemon_shutting_down:")
}

// TestMapErrorGameRunningEscapesItsValues locks the code, message and escaping of the pending-changes refusal.
func TestMapErrorGameRunningEscapesItsValues(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&dto.GameRunningError{GameID: "stardewvalley", Operation: dto.GameRunningOperationLaunch}, "game_running:game=stardewvalley:operation=launch"},
		{fmt.Errorf("apply: %w", &dto.GameRunningError{GameID: "skyrimse", Operation: dto.GameRunningOperationApply}), "game_running:game=skyrimse:operation=apply"},
		{&dto.GameRunningError{GameID: "a:b=c", Operation: "x y"}, "game_running:game=a%3Ab%3Dc:operation=x%20y"},
	} {
		mapped, handled := MapError(tc.err)
		if !handled {
			t.Fatalf("MapError(%v) not handled", tc.err)
		}
		assertStatus(t, mapped, codes.FailedPrecondition, tc.want)
	}
}
