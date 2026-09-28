package ipc

import (
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

// runEnumTable asserts each named constant has its expected numeric value.
func runEnumTable(t *testing.T, cases []struct {
	name string
	got  int
	want int
}) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestDownloadStatusValues locks the DTO and proto DownloadStatus numbering.
func TestDownloadStatusValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.DownloadStatusUnknown", int(dto.DownloadStatusUnknown), 0},
		{"dto.DownloadStatusQueued", int(dto.DownloadStatusQueued), 1},
		{"dto.DownloadStatusDownloading", int(dto.DownloadStatusDownloading), 2},
		{"dto.DownloadStatusDownloaded", int(dto.DownloadStatusDownloaded), 3},
		{"dto.DownloadStatusInstalling", int(dto.DownloadStatusInstalling), 4},
		{"dto.DownloadStatusInstalled", int(dto.DownloadStatusInstalled), 5},
		{"dto.DownloadStatusUninstalled", int(dto.DownloadStatusUninstalled), 6},
		{"dto.DownloadStatusCancelled", int(dto.DownloadStatusCancelled), 7},
		{"dto.DownloadStatusFailed", int(dto.DownloadStatusFailed), 8},
		{"pb.DOWNLOAD_STATUS_UNKNOWN", int(pb.DownloadStatus_DOWNLOAD_STATUS_UNKNOWN), 0},
		{"pb.DOWNLOAD_STATUS_QUEUED", int(pb.DownloadStatus_DOWNLOAD_STATUS_QUEUED), 1},
		{"pb.DOWNLOAD_STATUS_DOWNLOADING", int(pb.DownloadStatus_DOWNLOAD_STATUS_DOWNLOADING), 2},
		{"pb.DOWNLOAD_STATUS_DOWNLOADED", int(pb.DownloadStatus_DOWNLOAD_STATUS_DOWNLOADED), 3},
		{"pb.DOWNLOAD_STATUS_INSTALLING", int(pb.DownloadStatus_DOWNLOAD_STATUS_INSTALLING), 4},
		{"pb.DOWNLOAD_STATUS_INSTALLED", int(pb.DownloadStatus_DOWNLOAD_STATUS_INSTALLED), 5},
		{"pb.DOWNLOAD_STATUS_UNINSTALLED", int(pb.DownloadStatus_DOWNLOAD_STATUS_UNINSTALLED), 6},
		{"pb.DOWNLOAD_STATUS_CANCELLED", int(pb.DownloadStatus_DOWNLOAD_STATUS_CANCELLED), 7},
		{"pb.DOWNLOAD_STATUS_FAILED", int(pb.DownloadStatus_DOWNLOAD_STATUS_FAILED), 8},
	})
}

// TestDepKindValues locks the DTO and proto DepKind numbering.
func TestDepKindValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.DepKindOK", int(dto.DepKindOK), 0},
		{"dto.DepKindMasterAbsent", int(dto.DepKindMasterAbsent), 1},
		{"dto.DepKindMasterDisabled", int(dto.DepKindMasterDisabled), 2},
		{"dto.DepKindMasterOutOfOrder", int(dto.DepKindMasterOutOfOrder), 3},
		{"dto.DepKindSoftMissing", int(dto.DepKindSoftMissing), 4},
		{"pb.DEP_OK", int(pb.DepKind_DEP_OK), 0},
		{"pb.DEP_MASTER_ABSENT", int(pb.DepKind_DEP_MASTER_ABSENT), 1},
		{"pb.DEP_MASTER_DISABLED", int(pb.DepKind_DEP_MASTER_DISABLED), 2},
		{"pb.DEP_MASTER_OUT_OF_ORDER", int(pb.DepKind_DEP_MASTER_OUT_OF_ORDER), 3},
		{"pb.DEP_SOFT_MISSING", int(pb.DepKind_DEP_SOFT_MISSING), 4},
	})
}

// TestInstallStepValues locks the DTO and proto InstallProgress.Step numbering.
func TestInstallStepValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.InstallStepIdle", int(dto.InstallStepIdle), 0},
		{"dto.InstallStepExtracting", int(dto.InstallStepExtracting), 1},
		{"dto.InstallStepCopying", int(dto.InstallStepCopying), 2},
		{"dto.InstallStepFinalizing", int(dto.InstallStepFinalizing), 3},
		{"dto.InstallStepComplete", int(dto.InstallStepComplete), 4},
		{"dto.InstallStepFailed", int(dto.InstallStepFailed), 5},
		{"pb.STEP_IDLE", int(pb.InstallProgress_STEP_IDLE), 0},
		{"pb.STEP_EXTRACTING", int(pb.InstallProgress_STEP_EXTRACTING), 1},
		{"pb.STEP_COPYING", int(pb.InstallProgress_STEP_COPYING), 2},
		{"pb.STEP_FINALIZING", int(pb.InstallProgress_STEP_FINALIZING), 3},
		{"pb.STEP_COMPLETE", int(pb.InstallProgress_STEP_COMPLETE), 4},
		{"pb.STEP_FAILED", int(pb.InstallProgress_STEP_FAILED), 5},
	})
}

// TestInstallModeValues locks the DTO and proto InstallMode numbering.
func TestInstallModeValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.InstallAsNewMod", int(dto.InstallAsNewMod), 0},
		{"dto.InstallMergeIntoMod", int(dto.InstallMergeIntoMod), 1},
		{"dto.InstallReplaceMod", int(dto.InstallReplaceMod), 2},
		{"pb.INSTALL_MODE_NEW_MOD", int(pb.InstallMode_INSTALL_MODE_NEW_MOD), 0},
		{"pb.INSTALL_MODE_MERGE_INTO", int(pb.InstallMode_INSTALL_MODE_MERGE_INTO), 1},
		{"pb.INSTALL_MODE_REPLACE", int(pb.InstallMode_INSTALL_MODE_REPLACE), 2},
	})
}

// TestCollisionPolicyValues locks the DTO and proto TransferCollisionPolicy numbering.
func TestCollisionPolicyValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.PolicyAbort", int(dto.PolicyAbort), 0},
		{"dto.PolicySkip", int(dto.PolicySkip), 1},
		{"dto.PolicyRename", int(dto.PolicyRename), 2},
		{"dto.PolicyOverwrite", int(dto.PolicyOverwrite), 3},
		{"pb.TRANSFER_POLICY_ABORT", int(pb.TransferCollisionPolicy_TRANSFER_POLICY_ABORT), 0},
		{"pb.TRANSFER_POLICY_SKIP", int(pb.TransferCollisionPolicy_TRANSFER_POLICY_SKIP), 1},
		{"pb.TRANSFER_POLICY_RENAME", int(pb.TransferCollisionPolicy_TRANSFER_POLICY_RENAME), 2},
		{"pb.TRANSFER_POLICY_OVERWRITE", int(pb.TransferCollisionPolicy_TRANSFER_POLICY_OVERWRITE), 3},
	})
}

// TestBulkHideScopeValues locks the DTO and proto SetArchivesHiddenBulkRequest.Scope numbering.
func TestBulkHideScopeValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.BulkHideAll", int(dto.BulkHideAll), 0},
		{"dto.BulkHideInstalled", int(dto.BulkHideInstalled), 1},
		{"dto.BulkHideUninstalled", int(dto.BulkHideUninstalled), 2},
		{"pb.Scope_ALL", int(pb.SetArchivesHiddenBulkRequest_ALL), 0},
		{"pb.Scope_INSTALLED", int(pb.SetArchivesHiddenBulkRequest_INSTALLED), 1},
		{"pb.Scope_UNINSTALLED", int(pb.SetArchivesHiddenBulkRequest_UNINSTALLED), 2},
	})
}

// TestModLoaderKindValues locks the DTO and proto ModLoaderKind numbering.
func TestModLoaderKindValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.ModLoaderKindNone", int(dto.ModLoaderKindNone), 0},
		{"dto.ModLoaderKindSMAPI", int(dto.ModLoaderKindSMAPI), 1},
		{"pb.MOD_LOADER_KIND_NONE", int(pb.ModLoaderKind_MOD_LOADER_KIND_NONE), 0},
		{"pb.MOD_LOADER_KIND_SMAPI", int(pb.ModLoaderKind_MOD_LOADER_KIND_SMAPI), 1},
	})
}

// TestInstallLayoutValues locks the DTO and proto InstallLayout numbering.
func TestInstallLayoutValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.InstallLayoutUnspecified", int(dto.InstallLayoutUnspecified), 0},
		{"dto.InstallLayoutDataRoot", int(dto.InstallLayoutDataRoot), 1},
		{"dto.InstallLayoutSMAPIManifest", int(dto.InstallLayoutSMAPIManifest), 2},
		{"pb.INSTALL_LAYOUT_UNSPECIFIED", int(pb.InstallLayout_INSTALL_LAYOUT_UNSPECIFIED), 0},
		{"pb.INSTALL_LAYOUT_DATA_ROOT", int(pb.InstallLayout_INSTALL_LAYOUT_DATA_ROOT), 1},
		{"pb.INSTALL_LAYOUT_SMAPI_MANIFEST", int(pb.InstallLayout_INSTALL_LAYOUT_SMAPI_MANIFEST), 2},
	})
}

// TestInstallLayoutToProtoNeverDefaultsToDataRoot checks only DataRoot maps to the DATA_ROOT wire value.
func TestInstallLayoutToProtoNeverDefaultsToDataRoot(t *testing.T) {
	for _, tc := range []struct {
		in   dto.InstallLayoutResult
		want pb.InstallLayout
	}{
		{dto.InstallLayoutUnspecified, pb.InstallLayout_INSTALL_LAYOUT_UNSPECIFIED},
		{dto.InstallLayoutDataRoot, pb.InstallLayout_INSTALL_LAYOUT_DATA_ROOT},
		{dto.InstallLayoutSMAPIManifest, pb.InstallLayout_INSTALL_LAYOUT_SMAPI_MANIFEST},
		{dto.InstallLayoutResult(99), pb.InstallLayout_INSTALL_LAYOUT_UNSPECIFIED},
	} {
		if got := installLayoutToProto(tc.in); got != tc.want {
			t.Errorf("installLayoutToProto(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestModLoaderStateValues locks the DTO and proto ModLoaderState numbering.
func TestModLoaderStateValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.ModLoaderStateUnspecified", int(dto.ModLoaderStateUnspecified), 0},
		{"dto.ModLoaderStateNotInstalled", int(dto.ModLoaderStateNotInstalled), 1},
		{"dto.ModLoaderStateOK", int(dto.ModLoaderStateOK), 2},
		{"dto.ModLoaderStateLauncherReverted", int(dto.ModLoaderStateLauncherReverted), 3},
		{"dto.ModLoaderStateIncomplete", int(dto.ModLoaderStateIncomplete), 4},
		{"dto.ModLoaderStateUnsupportedBuild", int(dto.ModLoaderStateUnsupportedBuild), 5},
		{"dto.ModLoaderStateInterrupted", int(dto.ModLoaderStateInterrupted), 6},
		{"pb.MOD_LOADER_STATE_UNSPECIFIED", int(pb.ModLoaderState_MOD_LOADER_STATE_UNSPECIFIED), 0},
		{"pb.MOD_LOADER_STATE_NOT_INSTALLED", int(pb.ModLoaderState_MOD_LOADER_STATE_NOT_INSTALLED), 1},
		{"pb.MOD_LOADER_STATE_OK", int(pb.ModLoaderState_MOD_LOADER_STATE_OK), 2},
		{"pb.MOD_LOADER_STATE_LAUNCHER_REVERTED", int(pb.ModLoaderState_MOD_LOADER_STATE_LAUNCHER_REVERTED), 3},
		{"pb.MOD_LOADER_STATE_INCOMPLETE", int(pb.ModLoaderState_MOD_LOADER_STATE_INCOMPLETE), 4},
		{"pb.MOD_LOADER_STATE_UNSUPPORTED_BUILD", int(pb.ModLoaderState_MOD_LOADER_STATE_UNSUPPORTED_BUILD), 5},
		{"pb.MOD_LOADER_STATE_INTERRUPTED", int(pb.ModLoaderState_MOD_LOADER_STATE_INTERRUPTED), 6},
	})
}

// TestModLoaderStateToProtoMapsEveryState checks each DTO state reaches its proto value and unknown values map to UNSPECIFIED.
func TestModLoaderStateToProtoMapsEveryState(t *testing.T) {
	for in := dto.ModLoaderStateUnspecified; in <= dto.ModLoaderStateInterrupted; in++ {
		if got := modLoaderStateToProto(in); int32(got) != int32(in) {
			t.Errorf("modLoaderStateToProto(%d) = %v, want %d", in, got, in)
		}
	}
	if got := modLoaderStateToProto(dto.ModLoaderStateResult(99)); got != pb.ModLoaderState_MOD_LOADER_STATE_UNSPECIFIED {
		t.Errorf("modLoaderStateToProto(99) = %v, want UNSPECIFIED", got)
	}
}

// TestModIssueKindValues locks the DTO and proto ModIssueKind numbering.
func TestModIssueKindValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.ModIssueUnspecified", int(dto.ModIssueUnspecified), 0},
		{"dto.ModIssueMissing", int(dto.ModIssueMissing), 1},
		{"dto.ModIssueDisabled", int(dto.ModIssueDisabled), 2},
		{"dto.ModIssueVersionTooLow", int(dto.ModIssueVersionTooLow), 3},
		{"dto.ModIssueDuplicateID", int(dto.ModIssueDuplicateID), 4},
		{"dto.ModIssueInvalidManifest", int(dto.ModIssueInvalidManifest), 5},
		{"dto.ModIssueNeedsNewerLoader", int(dto.ModIssueNeedsNewerLoader), 6},
		{"dto.ModIssueNeedsNewerGame", int(dto.ModIssueNeedsNewerGame), 7},
		{"dto.ModIssueCircular", int(dto.ModIssueCircular), 8},
		{"dto.ModIssueFolderCollision", int(dto.ModIssueFolderCollision), 9},
		{"dto.ModIssueDependencyFailed", int(dto.ModIssueDependencyFailed), 10},
		{"pb.MOD_ISSUE_KIND_UNSPECIFIED", int(pb.ModIssueKind_MOD_ISSUE_KIND_UNSPECIFIED), 0},
		{"pb.MOD_ISSUE_KIND_MISSING", int(pb.ModIssueKind_MOD_ISSUE_KIND_MISSING), 1},
		{"pb.MOD_ISSUE_KIND_DISABLED", int(pb.ModIssueKind_MOD_ISSUE_KIND_DISABLED), 2},
		{"pb.MOD_ISSUE_KIND_VERSION_TOO_LOW", int(pb.ModIssueKind_MOD_ISSUE_KIND_VERSION_TOO_LOW), 3},
		{"pb.MOD_ISSUE_KIND_DUPLICATE_ID", int(pb.ModIssueKind_MOD_ISSUE_KIND_DUPLICATE_ID), 4},
		{"pb.MOD_ISSUE_KIND_INVALID_MANIFEST", int(pb.ModIssueKind_MOD_ISSUE_KIND_INVALID_MANIFEST), 5},
		{"pb.MOD_ISSUE_KIND_NEEDS_NEWER_LOADER", int(pb.ModIssueKind_MOD_ISSUE_KIND_NEEDS_NEWER_LOADER), 6},
		{"pb.MOD_ISSUE_KIND_NEEDS_NEWER_GAME", int(pb.ModIssueKind_MOD_ISSUE_KIND_NEEDS_NEWER_GAME), 7},
		{"pb.MOD_ISSUE_KIND_CIRCULAR", int(pb.ModIssueKind_MOD_ISSUE_KIND_CIRCULAR), 8},
		{"pb.MOD_ISSUE_KIND_FOLDER_COLLISION", int(pb.ModIssueKind_MOD_ISSUE_KIND_FOLDER_COLLISION), 9},
		{"pb.MOD_ISSUE_KIND_DEPENDENCY_FAILED", int(pb.ModIssueKind_MOD_ISSUE_KIND_DEPENDENCY_FAILED), 10},
	})
}

// TestModComponentKindValues locks the DTO and proto ModComponentKind numbering.
func TestModComponentKindValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.ModComponentUnspecified", int(dto.ModComponentUnspecified), 0},
		{"dto.ModComponentCode", int(dto.ModComponentCode), 1},
		{"dto.ModComponentContentPack", int(dto.ModComponentContentPack), 2},
		{"dto.ModComponentInvalid", int(dto.ModComponentInvalid), 3},
		{"pb.MOD_COMPONENT_KIND_UNSPECIFIED", int(pb.ModComponentKind_MOD_COMPONENT_KIND_UNSPECIFIED), 0},
		{"pb.MOD_COMPONENT_KIND_CODE", int(pb.ModComponentKind_MOD_COMPONENT_KIND_CODE), 1},
		{"pb.MOD_COMPONENT_KIND_CONTENT_PACK", int(pb.ModComponentKind_MOD_COMPONENT_KIND_CONTENT_PACK), 2},
		{"pb.MOD_COMPONENT_KIND_INVALID", int(pb.ModComponentKind_MOD_COMPONENT_KIND_INVALID), 3},
	})
}

// TestFetchOutcomeValues locks the DTO and proto FetchOutcome numbering.
func TestFetchOutcomeValues(t *testing.T) {
	runEnumTable(t, []struct {
		name string
		got  int
		want int
	}{
		{"dto.FetchOutcomeUnspecified", int(dto.FetchOutcomeUnspecified), 0},
		{"dto.FetchOutcomeQueued", int(dto.FetchOutcomeQueued), 1},
		{"dto.FetchOutcomeOpenURL", int(dto.FetchOutcomeOpenURL), 2},
		{"dto.FetchOutcomeUnresolved", int(dto.FetchOutcomeUnresolved), 3},
		{"dto.FetchOutcomeAlreadyPresent", int(dto.FetchOutcomeAlreadyPresent), 4},
		{"pb.FETCH_OUTCOME_UNSPECIFIED", int(pb.FetchOutcome_FETCH_OUTCOME_UNSPECIFIED), 0},
		{"pb.FETCH_OUTCOME_QUEUED", int(pb.FetchOutcome_FETCH_OUTCOME_QUEUED), 1},
		{"pb.FETCH_OUTCOME_OPEN_URL", int(pb.FetchOutcome_FETCH_OUTCOME_OPEN_URL), 2},
		{"pb.FETCH_OUTCOME_UNRESOLVED", int(pb.FetchOutcome_FETCH_OUTCOME_UNRESOLVED), 3},
		{"pb.FETCH_OUTCOME_ALREADY_PRESENT", int(pb.FetchOutcome_FETCH_OUTCOME_ALREADY_PRESENT), 4},
	})
}

// TestModDependencyEnumsMapEveryValue checks each DTO dependency enum value reaches its proto value and unknown values map to UNSPECIFIED.
func TestModDependencyEnumsMapEveryValue(t *testing.T) {
	for in := dto.ModIssueUnspecified; in <= dto.ModIssueDependencyFailed; in++ {
		if got := modIssueKindToProto(in); int32(got) != int32(in) {
			t.Errorf("modIssueKindToProto(%d) = %v, want %d", in, got, in)
		}
	}
	for in := dto.ModComponentUnspecified; in <= dto.ModComponentInvalid; in++ {
		if got := modComponentKindToProto(in); int32(got) != int32(in) {
			t.Errorf("modComponentKindToProto(%d) = %v, want %d", in, got, in)
		}
	}
	for in := dto.FetchOutcomeUnspecified; in <= dto.FetchOutcomeAlreadyPresent; in++ {
		if got := fetchOutcomeToProto(in); int32(got) != int32(in) {
			t.Errorf("fetchOutcomeToProto(%d) = %v, want %d", in, got, in)
		}
	}
	if got := modIssueKindToProto(dto.ModIssueKind(99)); got != pb.ModIssueKind_MOD_ISSUE_KIND_UNSPECIFIED {
		t.Errorf("modIssueKindToProto(99) = %v, want UNSPECIFIED", got)
	}
	if got := modComponentKindToProto(dto.ModComponentKind(99)); got != pb.ModComponentKind_MOD_COMPONENT_KIND_UNSPECIFIED {
		t.Errorf("modComponentKindToProto(99) = %v, want UNSPECIFIED", got)
	}
	if got := fetchOutcomeToProto(dto.FetchOutcome(99)); got != pb.FetchOutcome_FETCH_OUTCOME_UNSPECIFIED {
		t.Errorf("fetchOutcomeToProto(99) = %v, want UNSPECIFIED", got)
	}
}
