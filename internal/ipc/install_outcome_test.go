package ipc

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"google.golang.org/grpc/codes"
)

// TestGetInstallOutcomeMapsFailedError confirms that status polling uses the original token and gRPC code.
func TestGetInstallOutcomeMapsFailedError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		text string
		code codes.Code
	}{
		{name: "registration failed", err: &download.ModRegistrationError{Mod: "Test Mod", Err: fmt.Errorf("save failed")}, text: "mod_registration_failed:mod=Test%20Mod", code: codes.Internal},
		{name: "deadline", err: fmt.Errorf("install: %w", context.DeadlineExceeded), text: "install: context deadline exceeded", code: codes.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &gorganizerServer{ctrl: &fakeController{installOutcome: dto.InstallOutcome{
				State: dto.InstallOutcomeFailed, Err: tc.err,
			}}}
			result, err := server.GetInstallOutcome(context.Background(), &pb.GetInstallOutcomeRequest{GameId: "skyrimse", ClientRequestId: "request"})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != pb.InstallOutcomeState_INSTALL_OUTCOME_STATE_FAILED || result.Error != tc.text || result.ErrorCode != int32(tc.code) {
				t.Fatalf("result = %+v, want %q and %d", result, tc.text, tc.code)
			}
		})
	}
}
