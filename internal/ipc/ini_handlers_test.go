package ipc

import (
	"context"
	"errors"
	"testing"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

type iniHandlerController struct {
	IniController
	result      *dto.ProfileIniSaveResult
	saveErr     error
	count       int
	applyErr    error
	gameID      string
	profileName string
}

// SaveProfileIniFile returns the configured result for a transport conversion test.
func (c *iniHandlerController) SaveProfileIniFile(gameID, profileName, filename, content string) (*dto.ProfileIniSaveResult, error) {
	c.gameID, c.profileName = gameID, profileName
	return c.result, c.saveErr
}

// ApplyProfileIniFiles returns the configured count for a transport conversion test.
func (c *iniHandlerController) ApplyProfileIniFiles(gameID, profileName string) (int, error) {
	c.gameID, c.profileName = gameID, profileName
	return c.count, c.applyErr
}

// TestProfileIniHandlers checks save outcomes, apply count, and propagated errors.
func TestProfileIniHandlers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome dto.IniSaveOutcome
		want    pb.IniSaveOutcome
	}{
		{"saved", dto.IniSaveSaved, pb.IniSaveOutcome_INI_SAVE_OUTCOME_SAVED},
		{"applied", dto.IniSaveSavedAndApplied, pb.IniSaveOutcome_INI_SAVE_OUTCOME_SAVED_AND_APPLIED},
		{"saved but not applied", dto.IniSaveSavedApplyFailed, pb.IniSaveOutcome_INI_SAVE_OUTCOME_SAVED_APPLY_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &iniHandlerController{result: &dto.ProfileIniSaveResult{Outcome: tc.outcome, ApplyError: "Documents unavailable"}, count: 2}
			server := &gorganizerServer{ctrl: &fakeController{IniController: ctrl}}
			result, err := server.SaveProfileIniFile(context.Background(), &pb.SaveProfileIniFileRequest{
				GameId: "skyrimse", ProfileName: "Default", Filename: "Skyrim.ini", Content: "test",
			})
			if err != nil || result.GetOutcome() != tc.want || result.GetApplyError() != "Documents unavailable" {
				t.Fatalf("save = %+v, err = %v, want %v", result, err, tc.want)
			}
			applied, err := server.ApplyProfileIniFiles(context.Background(), &pb.ApplyProfileIniFilesRequest{
				GameId: "skyrimse", ProfileName: "Default",
			})
			if err != nil || applied.GetAppliedFileCount() != 2 || ctrl.gameID != "skyrimse" || ctrl.profileName != "Default" {
				t.Fatalf("apply = %+v, err = %v, arguments = %q/%q", applied, err, ctrl.gameID, ctrl.profileName)
			}
			ctrl.applyErr = errors.New("could not apply")
			if _, err := server.ApplyProfileIniFiles(context.Background(), &pb.ApplyProfileIniFilesRequest{}); err == nil {
				t.Fatal("expected apply error")
			}
			ctrl.saveErr = errors.New("could not save")
			if _, err := server.SaveProfileIniFile(context.Background(), &pb.SaveProfileIniFileRequest{}); err == nil {
				t.Fatal("expected save error")
			}
		})
	}
}
