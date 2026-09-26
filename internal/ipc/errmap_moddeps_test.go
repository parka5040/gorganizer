package ipc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/dto"
	"google.golang.org/grpc/codes"
)

// modDependencyErrorCases lists every dependency error MapError encodes with its exact code and message.
func modDependencyErrorCases() []struct {
	name     string
	err      error
	wantCode codes.Code
	wantMsg  string
} {
	return []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{"unsupported", &dto.ModDependenciesUnsupportedError{GameID: "skyrimse"}, codes.InvalidArgument, "mod_dependencies_unsupported:game=skyrimse"},
		{"unsupported_wrapped", fmt.Errorf("report: %w", &dto.ModDependenciesUnsupportedError{GameID: "skyrimse"}), codes.InvalidArgument, "mod_dependencies_unsupported:game=skyrimse"},
		{"unsupported_escaped", &dto.ModDependenciesUnsupportedError{GameID: "loader_missing:x=y"}, codes.InvalidArgument, "mod_dependencies_unsupported:game=loader_missing%3Ax%3Dy"},
	}
}

// TestMapErrorModDependencyErrors locks the code and message of every dependency token.
func TestMapErrorModDependencyErrors(t *testing.T) {
	for _, tc := range modDependencyErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			mapped, handled := MapError(tc.err)
			if !handled {
				t.Fatalf("MapError(%v) not handled", tc.err)
			}
			assertStatus(t, mapped, tc.wantCode, tc.wantMsg)
		})
	}
}

// TestModDependencyTokensContainNoExistingToken locks that no dependency message embeds another registered token such as loader_missing: or fomod_required:.
func TestModDependencyTokensContainNoExistingToken(t *testing.T) {
	for _, tc := range modDependencyErrorCases() {
		assertOnlyOwnToken(t, tc.name, tc.wantMsg)
		if !strings.HasPrefix(tc.wantMsg, tokenModDependenciesUnsupported) {
			t.Errorf("%s message %q does not start with its own token", tc.name, tc.wantMsg)
		}
	}
}
