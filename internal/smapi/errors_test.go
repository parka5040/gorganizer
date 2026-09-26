package smapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUnavailableErrorTextMatchesTheRefusedAction(t *testing.T) {
	for _, tc := range []struct {
		err       *UnavailableError
		want      string
		forbidden string
	}{
		{&UnavailableError{GameID: "stardewvalley", State: StateNotInstalled}, "stardewvalley has SMAPI mods enabled but SMAPI is not installed", ""},
		{&UnavailableError{GameID: "stardewvalley", State: StateUnsupportedBuild, Context: UnavailableForLoaderChange}, "this game build is not supported", "mods enabled"},
		{&UnavailableError{GameID: "stardewvalley", State: StateIncomplete, Context: UnavailableForLoaderChange}, "while it is incomplete", "mods enabled"},
		{&UnavailableError{GameID: "stardewvalley", State: StateInterrupted, Context: UnavailableForMount}, "interrupted SMAPI transaction", "mods enabled"},
		{&UnavailableError{GameID: "stardewvalley", State: StateIncomplete, Context: UnavailableForMount}, "cannot be mounted or launched while SMAPI is incomplete", "mods enabled"},
	} {
		text := tc.err.Error()
		if !strings.Contains(text, tc.want) || (tc.forbidden != "" && strings.Contains(text, tc.forbidden)) {
			t.Errorf("%+v.Error() = %q, want %q without %q", *tc.err, text, tc.want, tc.forbidden)
		}
	}
}

func TestRollbackFailedErrorKeepsTheFormatAndChainOfAWrappedRollbackFailure(t *testing.T) {
	cause := fmt.Errorf("applying: %w", ErrGameChanged)
	rollback := errors.New("device busy")
	typed := &RollbackFailedError{Cause: cause, RollbackErr: rollback}
	legacy := fmt.Errorf("%w; rolling back failed: %w: %w", cause, rollback, ErrInterrupted)
	if typed.Error() != legacy.Error() {
		t.Errorf("Error() = %q, want %q", typed.Error(), legacy.Error())
	}
	for _, target := range []error{cause, rollback, ErrInterrupted, ErrGameChanged} {
		if !errors.Is(typed, target) {
			t.Errorf("RollbackFailedError does not wrap %v", target)
		}
	}
	if reason, ok := FailureReason(typed); !ok || reason != FailureInterrupted {
		t.Errorf("FailureReason = %q, %v; want interrupted while the rollback is unresolved", reason, ok)
	}
	if reason, ok := FailureReason(fmt.Errorf("repair: %w", ErrNoArtifact)); !ok || reason != FailureNoArtifact {
		t.Errorf("FailureReason(ErrNoArtifact) = %q, %v; want no_artifact", reason, ok)
	}
}
