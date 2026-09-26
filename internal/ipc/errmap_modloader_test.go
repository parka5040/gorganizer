package ipc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/ghrelease"
	"github.com/parka/gorganizer/internal/smapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var busyOperations = []string{
	"modloader", "mounted", "running", "root_deployment", "transaction", "launch", "tool", "mount",
	"unmount", "apply", "configure", "script_extender", "import", "reinstall",
}

// TestBusyOperationValuesAreFrozen locks the registered modloader_busy operation values.
func TestBusyOperationValuesAreFrozen(t *testing.T) {
	got := []string{
		dto.BusyOperationModLoader, dto.BusyOperationMounted, dto.BusyOperationRunning, dto.BusyOperationRootDeployment,
		dto.BusyOperationTransaction, dto.BusyOperationLaunch, dto.BusyOperationTool, dto.BusyOperationMount,
		dto.BusyOperationUnmount, dto.BusyOperationApply, dto.BusyOperationConfigure, dto.BusyOperationScriptExtender,
		dto.BusyOperationImport, dto.BusyOperationReinstall,
	}
	if len(got) != len(busyOperations) {
		t.Fatalf("busy operations = %v, want %v", got, busyOperations)
	}
	for i := range got {
		if got[i] != busyOperations[i] {
			t.Errorf("busy operation %d = %q, want %q", i, got[i], busyOperations[i])
		}
	}
}

// TestBusyErrorWithoutHolderOmitsTheHolderField locks that an unknown holder leaves the optional field out.
func TestBusyErrorWithoutHolderOmitsTheHolderField(t *testing.T) {
	mapped, handled := MapError(&dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationTransaction})
	if !handled {
		t.Fatal("busy error not handled")
	}
	assertStatus(t, mapped, codes.FailedPrecondition, "modloader_busy:game=stardewvalley:operation=transaction")
}

// modLoaderErrorCases lists every mod-loader error MapError encodes with its exact code and message.
func modLoaderErrorCases() []struct {
	name     string
	err      error
	wantCode codes.Code
	wantMsg  string
} {
	type row = struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}
	cases := []row{
		{"busy_modloader", &dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationModLoader}, codes.FailedPrecondition, "modloader_busy:game=stardewvalley:operation=modloader"},
		{"busy_mounted", &dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationMounted}, codes.FailedPrecondition, "modloader_busy:game=stardewvalley:operation=mounted"},
		{"busy_shared_launch", fmt.Errorf("auto-mount failed: %w", &dto.OperationBusyError{GameID: "stardewvalley", Operation: "launch"}), codes.FailedPrecondition, "modloader_busy:game=stardewvalley:operation=launch"},
		{"busy_transaction", fmt.Errorf("%w: %v", &dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationTransaction}, smapi.ErrTransactionActive), codes.FailedPrecondition, "modloader_busy:game=stardewvalley:operation=transaction"},
		{"busy_escaped", &dto.OperationBusyError{GameID: "a:b=c", Operation: "x y", Holder: "d:e"}, codes.FailedPrecondition, "modloader_busy:game=a%3Ab%3Dc:operation=x%20y:holder=d%3Ae"},
		{"busy_sibling_holder", &dto.OperationBusyError{GameID: "falloutnv", Operation: dto.BusyOperationLaunch, Holder: "ttw"}, codes.FailedPrecondition, "modloader_busy:game=falloutnv:operation=launch:holder=ttw"},
		{"busy_sibling_loader", fmt.Errorf("mount: %w", &dto.OperationBusyError{GameID: "ttw", Operation: dto.BusyOperationModLoader, Holder: "falloutnv"}), codes.FailedPrecondition, "modloader_busy:game=ttw:operation=modloader:holder=falloutnv"},
		{"failed_no_artifact", &smapi.FailedError{GameID: "stardewvalley", Reason: smapi.FailureNoArtifact, Err: fmt.Errorf("repair: %w", smapi.ErrNoArtifact)}, codes.FailedPrecondition, "modloader_failed:reason=no_artifact"},
		{"failed_unrecovered_rollback", smapi.ClassifyFailure("stardewvalley", &smapi.RollbackFailedError{Cause: smapi.ErrGameChanged, RollbackErr: errors.New("busy")}), codes.FailedPrecondition, "modloader_failed:reason=interrupted"},
		{"unsupported", &dto.ModLoaderUnsupportedError{GameID: "skyrimse"}, codes.InvalidArgument, "modloader_unsupported:game=skyrimse"},
		{"failed_no_previous", smapi.ClassifyFailure("stardewvalley", fmt.Errorf("rollback: %w", ghrelease.ErrNoPrevious)), codes.FailedPrecondition, "modloader_failed:reason=no_previous"},
		{"failed_previous_unavailable", smapi.ClassifyFailure("stardewvalley", ghrelease.ErrPreviousUnavailable), codes.FailedPrecondition, "modloader_failed:reason=no_previous"},
		{"failed_digest_mismatch", smapi.ClassifyFailure("stardewvalley", fmt.Errorf("download: %w", ghrelease.ErrDigestMismatch)), codes.FailedPrecondition, "modloader_failed:reason=digest_mismatch"},
		{"failed_not_stable", smapi.ClassifyFailure("stardewvalley", ghrelease.ErrNotStable), codes.FailedPrecondition, "modloader_failed:reason=not_stable"},
		{"failed_no_digest", smapi.ClassifyFailure("stardewvalley", ghrelease.ErrNoDigest), codes.FailedPrecondition, "modloader_failed:reason=no_digest"},
		{"failed_interrupted_wins", fmt.Errorf("%w; rolling back failed: %w: %w", smapi.ErrStageUnexpected, errors.New("busy"), smapi.ErrInterrupted), codes.FailedPrecondition, "modloader_failed:reason=interrupted"},
		{"failed_unsafe_target_typed", &smapi.UnsafeTargetError{Path: "Mods", Reason: "game-owned"}, codes.FailedPrecondition, "modloader_failed:reason=unsafe_target"},
	}
	for _, op := range busyOperations {
		cases = append(cases, row{
			"busy_" + op,
			&dto.OperationBusyError{GameID: "stardewvalley", Operation: op, Holder: "stardewvalley"},
			codes.FailedPrecondition,
			"modloader_busy:game=stardewvalley:operation=" + op + ":holder=stardewvalley",
		})
	}
	for _, state := range []smapi.LoaderState{smapi.StateNotInstalled, smapi.StateLauncherReverted, smapi.StateIncomplete, smapi.StateUnsupportedBuild, smapi.StateInterrupted} {
		cases = append(cases, row{
			"unavailable_" + state.String(),
			fmt.Errorf("launch: %w", &smapi.UnavailableError{GameID: "stardewvalley", State: state}),
			codes.FailedPrecondition,
			"modloader_unavailable:reason=" + state.String() + ":loader=smapi:game=stardewvalley",
		})
	}
	for _, sentinel := range []struct {
		err    error
		reason string
	}{
		{smapi.ErrNoVanillaLauncher, "no_vanilla_launcher"},
		{smapi.ErrUnsafeTarget, "unsafe_target"},
		{smapi.ErrFarmMounted, "farm_mounted"},
		{smapi.ErrStageIncomplete, "stage_incomplete"},
		{smapi.ErrStageUnexpected, "stage_unexpected"},
		{smapi.ErrGameChanged, "game_changed"},
		{smapi.ErrCrossDevice, "cross_device"},
		{smapi.ErrRenameUnsupported, "rename_unsupported"},
		{smapi.ErrInterrupted, "interrupted"},
		{smapi.ErrNoArtifact, "no_artifact"},
	} {
		want := "modloader_failed:reason=" + sentinel.reason
		cases = append(cases,
			row{"failed_" + sentinel.reason, sentinel.err, codes.FailedPrecondition, want},
			row{"failed_" + sentinel.reason + "_wrapped", fmt.Errorf("installing SMAPI: %w", sentinel.err), codes.FailedPrecondition, want},
			row{"failed_" + sentinel.reason + "_classified", smapi.ClassifyFailure("stardewvalley", sentinel.err), codes.FailedPrecondition, want},
		)
	}
	return cases
}

// TestMapErrorModLoaderErrors locks the code and message of every mod-loader token.
func TestMapErrorModLoaderErrors(t *testing.T) {
	for _, tc := range modLoaderErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			mapped, handled := MapError(tc.err)
			if !handled {
				t.Fatalf("MapError(%v) not handled", tc.err)
			}
			assertStatus(t, mapped, tc.wantCode, tc.wantMsg)
		})
	}
}

// TestModLoaderTokensContainNoExistingToken locks that no mod-loader message embeds another registered token, since GUI matchers use substrings.
func TestModLoaderTokensContainNoExistingToken(t *testing.T) {
	for _, tc := range modLoaderErrorCases() {
		assertOnlyOwnToken(t, tc.name, tc.wantMsg)
	}
}

// TestMapErrorLeavesBareReleaseErrorsUnmapped locks that managed-LOOT release errors keep their pre-loader mapping.
func TestMapErrorLeavesBareReleaseErrorsUnmapped(t *testing.T) {
	for _, err := range []error{ghrelease.ErrDigestMismatch, ghrelease.ErrNotStable, ghrelease.ErrNoDigest, ghrelease.ErrNoPrevious, ghrelease.ErrPreviousUnavailable} {
		if mapped, handled := MapError(fmt.Errorf("LOOT: %w", err)); handled {
			t.Errorf("MapError(%v) = %v, want unhandled", err, mapped)
		}
		st, _ := status.FromError(grpcError(fmt.Errorf("LOOT: %w", err)))
		if st.Code() != codes.Internal {
			t.Errorf("grpcError(%v) code = %v, want Internal", err, st.Code())
		}
	}
}
