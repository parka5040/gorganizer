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
		{"pb.INSTALL_MODE_NEW_MOD", int(pb.InstallMode_INSTALL_MODE_NEW_MOD), 0},
		{"pb.INSTALL_MODE_MERGE_INTO", int(pb.InstallMode_INSTALL_MODE_MERGE_INTO), 1},
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
