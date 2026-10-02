package ipc

import (
	"context"
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUpdateOutcomeMapping freezes every DTO update outcome and the unknown-value fallback.
func TestUpdateOutcomeMapping(t *testing.T) {
	for _, tc := range []struct {
		outcome dto.UpdateCheckOutcome
		want    pb.UpdateCheckOutcome
	}{
		{dto.UpdateCheckUnspecified, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UNSPECIFIED},
		{dto.UpdateCheckUpToDate, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UP_TO_DATE},
		{dto.UpdateCheckUpdateAvailable, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UPDATE_AVAILABLE},
		{dto.UpdateCheckOffline, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_OFFLINE},
		{dto.UpdateCheckUnavailable, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UNAVAILABLE},
		{dto.UpdateCheckNotSupported, pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_NOT_SUPPORTED},
		{dto.UpdateCheckOutcome(-1), pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UNSPECIFIED},
	} {
		if got := updateOutcomeToProto(tc.outcome); got != tc.want {
			t.Errorf("updateOutcomeToProto(%d) = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

// TestCheckForUpdateRPCMapping checks the forwarded version, response fields and context error mapping.
func TestCheckForUpdateRPCMapping(t *testing.T) {
	fake := &fakeController{updateResult: dto.UpdateCheckResult{
		Outcome: dto.UpdateCheckUpdateAvailable, LatestVersion: "1.2.3",
		NotesURL: "https://github.com/parka5040/gorganizer/releases/tag/v1.2.3", Detail: "details",
	}}
	client := newTestClient(t, fake)
	response, err := client.CheckForUpdate(t.Context(), &pb.CheckForUpdateRequest{RunningVersion: "1.0.0+abc"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.updateVersion != "1.0.0+abc" {
		t.Errorf("running version = %q", fake.updateVersion)
	}
	if response.GetOutcome() != pb.UpdateCheckOutcome_UPDATE_CHECK_OUTCOME_UPDATE_AVAILABLE || response.GetLatestVersion() != "1.2.3" || response.GetNotesUrl() != fake.updateResult.NotesURL || response.GetDetail() != "details" {
		t.Errorf("response = %+v", response)
	}

	fake.updateErr = context.DeadlineExceeded
	_, err = client.CheckForUpdate(t.Context(), &pb.CheckForUpdateRequest{RunningVersion: "1.0.0"})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("deadline code = %v, want DeadlineExceeded", status.Code(err))
	}
	fake.updateErr = context.Canceled
	_, err = client.CheckForUpdate(t.Context(), &pb.CheckForUpdateRequest{RunningVersion: "1.0.0"})
	if status.Code(err) != codes.Canceled {
		t.Errorf("cancellation code = %v, want Canceled", status.Code(err))
	}
}
