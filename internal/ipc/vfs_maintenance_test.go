package ipc

import (
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

// TestVFSStatusToProtoIncludesSteamMaintenance checks the existing wire fields for retained Steam output.
func TestVFSStatusToProtoIncludesSteamMaintenance(t *testing.T) {
	status := vfsStatusToProto(&dto.VFSStatusResult{
		GameID: "skyrimse", SteamMaintenance: dto.SteamMaintenanceVerify,
		PreservedBatches: []dto.PreservedBatchResult{{BatchID: "batch", CreatedAt: "time", FileCount: 2, Reason: "steam_changed", Path: "preserved"}},
	})
	if status.GetSteamMaintenance() != pb.SteamMaintenanceState_STEAM_MAINTENANCE_STATE_VERIFY_REQUIRED ||
		len(status.GetPreservedBatches()) != 1 || status.GetPreservedBatches()[0].GetFileCount() != 2 ||
		status.GetPreservedBatches()[0].GetPath() != "preserved" {
		t.Fatalf("wire status = %+v", status)
	}
}
