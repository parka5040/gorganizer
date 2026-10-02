package ipc

import (
	"context"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

// CheckForUpdate forwards the running version and returns the update check result.
func (s *gorganizerServer) CheckForUpdate(ctx context.Context, req *pb.CheckForUpdateRequest) (*pb.UpdateCheck, error) {
	result, err := s.ctrl.CheckForUpdate(ctx, req.GetRunningVersion())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.UpdateCheck{
		Outcome:       updateOutcomeToProto(result.Outcome),
		LatestVersion: result.LatestVersion,
		NotesUrl:      result.NotesURL,
		Detail:        result.Detail,
	}, nil
}

// updateOutcomeToProto maps known DTO update outcomes to their wire values.
func updateOutcomeToProto(outcome dto.UpdateCheckOutcome) pb.UpdateCheckOutcome {
	switch outcome {
	case dto.UpdateCheckUpToDate:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UP_TO_DATE
	case dto.UpdateCheckUpdateAvailable:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UPDATE_AVAILABLE
	case dto.UpdateCheckOffline:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_OFFLINE
	case dto.UpdateCheckUnavailable:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UNAVAILABLE
	case dto.UpdateCheckNotSupported:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_NOT_SUPPORTED
	default:
		return pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UNSPECIFIED
	}
}
