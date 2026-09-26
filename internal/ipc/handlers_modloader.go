package ipc

import (
	"context"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

// GetModLoaderStatus reports a game's managed mod-loader state.
func (s *gorganizerServer) GetModLoaderStatus(ctx context.Context, req *pb.ModLoaderRequest) (*pb.ModLoaderStatus, error) {
	status, err := s.ctrl.GetModLoaderStatus(ctx, req.GetGameId(), req.GetCheckLatest())
	if err != nil {
		return nil, grpcError(err)
	}
	return modLoaderStatusToProto(status), nil
}

// InstallModLoader installs, updates, or repairs a game's managed mod loader.
func (s *gorganizerServer) InstallModLoader(ctx context.Context, req *pb.ModLoaderRequest) (*pb.ModLoaderStatus, error) {
	status, err := s.ctrl.InstallModLoader(ctx, req.GetGameId(), req.GetRepairOnly())
	if err != nil {
		return nil, grpcError(err)
	}
	return modLoaderStatusToProto(status), nil
}

// UninstallModLoader removes a game's managed mod loader.
func (s *gorganizerServer) UninstallModLoader(ctx context.Context, req *pb.ModLoaderRequest) (*pb.ModLoaderStatus, error) {
	status, err := s.ctrl.UninstallModLoader(ctx, req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return modLoaderStatusToProto(status), nil
}

// RollbackModLoader reinstalls a game's retained previous mod-loader release.
func (s *gorganizerServer) RollbackModLoader(ctx context.Context, req *pb.ModLoaderRequest) (*pb.ModLoaderStatus, error) {
	status, err := s.ctrl.RollbackModLoader(ctx, req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return modLoaderStatusToProto(status), nil
}

// modLoaderStatusToProto maps a daemon mod-loader status to the wire message.
func modLoaderStatusToProto(status dto.ModLoaderStatusResult) *pb.ModLoaderStatus {
	return &pb.ModLoaderStatus{
		GameId:           status.GameID,
		Kind:             modLoaderKindToProto(status.Kind),
		State:            modLoaderStateToProto(status.State),
		Managed:          status.Managed,
		InstalledVersion: status.InstalledVersion,
		ActiveVersion:    status.ActiveVersion,
		PreviousVersion:  status.PreviousVersion,
		LatestVersion:    status.LatestVersion,
		UpdateAvailable:  status.UpdateAvailable,
		Busy:             status.Busy,
		Detail:           status.Detail,
	}
}

// modLoaderStateToProto maps a DTO mod-loader state to its proto enum value, unknown values to UNSPECIFIED.
func modLoaderStateToProto(state dto.ModLoaderStateResult) pb.ModLoaderState {
	switch state {
	case dto.ModLoaderStateNotInstalled:
		return pb.ModLoaderState_MOD_LOADER_STATE_NOT_INSTALLED
	case dto.ModLoaderStateOK:
		return pb.ModLoaderState_MOD_LOADER_STATE_OK
	case dto.ModLoaderStateLauncherReverted:
		return pb.ModLoaderState_MOD_LOADER_STATE_LAUNCHER_REVERTED
	case dto.ModLoaderStateIncomplete:
		return pb.ModLoaderState_MOD_LOADER_STATE_INCOMPLETE
	case dto.ModLoaderStateUnsupportedBuild:
		return pb.ModLoaderState_MOD_LOADER_STATE_UNSUPPORTED_BUILD
	case dto.ModLoaderStateInterrupted:
		return pb.ModLoaderState_MOD_LOADER_STATE_INTERRUPTED
	default:
		return pb.ModLoaderState_MOD_LOADER_STATE_UNSPECIFIED
	}
}
