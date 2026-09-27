package ipc

import (
	"context"
	"reflect"
	"strings"
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/dto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type maintenanceController struct {
	fakeController
	args   []any
	result *dto.VFSStatusResult
	name   string
	count  int
	err    error
}

// SetSteamMaintenance records the wire arguments and returns the canned status.
func (c *maintenanceController) SetSteamMaintenance(gameID string, enabled, confirmed bool) (*dto.VFSStatusResult, error) {
	c.args = []any{gameID, enabled, confirmed}
	return c.result, c.err
}

// ImportPreservedFiles records the selected paths and returns the canned mod result.
func (c *maintenanceController) ImportPreservedFiles(gameID, batchID, modName string, paths []string) (string, int, error) {
	c.args = []any{gameID, batchID, modName, paths}
	return c.name, c.count, c.err
}

// DeletePreservedBatch records the retained batch ID and returns the canned status.
func (c *maintenanceController) DeletePreservedBatch(gameID, batchID string) (*dto.VFSStatusResult, error) {
	c.args = []any{gameID, batchID}
	return c.result, c.err
}

// TestMaintenanceRPCConversions checks all three request and response conversions and the confirmation error code.
func TestMaintenanceRPCConversions(t *testing.T) {
	ctrl := &maintenanceController{result: &dto.VFSStatusResult{GameID: "skyrimse", SteamMaintenance: dto.SteamMaintenanceUser}, name: "Recovered", count: 2}
	server := &gorganizerServer{ctrl: ctrl}
	result, err := server.SetSteamMaintenance(context.Background(), &pb.SetSteamMaintenanceRequest{GameId: "skyrimse", Enabled: true, VerificationConfirmed: true})
	if err != nil || result.GetSteamMaintenance() != pb.SteamMaintenanceState_STEAM_MAINTENANCE_STATE_USER_REQUESTED || !reflect.DeepEqual(ctrl.args, []any{"skyrimse", true, true}) {
		t.Fatalf("enable = %+v, %v, args %+v", result, err, ctrl.args)
	}
	imported, err := server.ImportPreservedFiles(context.Background(), &pb.ImportPreservedFilesRequest{GameId: "skyrimse", BatchId: "batch", ModName: "Recovered", RelativePaths: []string{"a", "b"}})
	if err != nil || imported.GetModName() != "Recovered" || imported.GetFileCount() != 2 || !reflect.DeepEqual(ctrl.args, []any{"skyrimse", "batch", "Recovered", []string{"a", "b"}}) {
		t.Fatalf("import = %+v, %v, args %+v", imported, err, ctrl.args)
	}
	removed, err := server.DeletePreservedBatch(context.Background(), &pb.DeletePreservedBatchRequest{GameId: "skyrimse", BatchId: "batch"})
	if err != nil || removed.GetGameId() != "skyrimse" || !reflect.DeepEqual(ctrl.args, []any{"skyrimse", "batch"}) {
		t.Fatalf("delete = %+v, %v, args %+v", removed, err, ctrl.args)
	}
	ctrl.err = daemon.ErrVerificationConfirmationRequired
	if _, err := server.SetSteamMaintenance(context.Background(), &pb.SetSteamMaintenanceRequest{GameId: "skyrimse"}); status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "confirm that Steam finished verifying the game files") {
		t.Fatalf("confirmation refusal = %v", err)
	}
}
