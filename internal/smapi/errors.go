package smapi

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/parka/gorganizer/internal/ghrelease"
)

var (
	ErrInvalidVersion    = errors.New("invalid SMAPI version")
	ErrInvalidManifest   = errors.New("invalid SMAPI manifest")
	ErrNotAMod           = errors.New("not a SMAPI mod")
	ErrNoVanillaLauncher = errors.New("no vanilla game launcher found")
	ErrStageIncomplete   = errors.New("mod-loader stage is incomplete")
	ErrStageUnexpected   = errors.New("mod-loader stage contains unexpected entries")
	ErrFarmMounted       = errors.New("mods folder is a mounted gorganizer farm")
	ErrInterrupted       = errors.New("an interrupted mod-loader transaction needs recovery")
	ErrUnsupportedBuild  = errors.New("unsupported game build")
	ErrUnsafeTarget      = errors.New("unsafe mod-loader target")
	ErrCrossDevice       = errors.New("mod-loader paths are not on the game's filesystem mount")
	ErrRenameUnsupported = errors.New("the game filesystem does not support no-replace renames")
	ErrGameChanged       = errors.New("the game changed while the mod-loader installer ran; retry")
	ErrTransactionActive = errors.New("another mod-loader transaction is in progress")
	ErrNoArtifact        = errors.New("no usable retained mod-loader artifact")
)

const (
	ReasonNoManifest        = "no_manifest"
	ReasonLoaderInstaller   = "loader_installer"
	ReasonUnsafeDestination = "unsafe_destination"
	ReasonFolderCollision   = "folder_collision"
	ReasonNestedMods        = "nested_mods"
	ReasonDuplicateIDs      = "duplicate_ids"
)

const (
	FailureInterrupted       = "interrupted"
	FailureGameChanged       = "game_changed"
	FailureNoVanillaLauncher = "no_vanilla_launcher"
	FailureUnsafeTarget      = "unsafe_target"
	FailureFarmMounted       = "farm_mounted"
	FailureStageIncomplete   = "stage_incomplete"
	FailureStageUnexpected   = "stage_unexpected"
	FailureCrossDevice       = "cross_device"
	FailureRenameUnsupported = "rename_unsupported"
	FailureDigestMismatch    = "digest_mismatch"
	FailureNotStable         = "not_stable"
	FailureNoDigest          = "no_digest"
	FailureNoPrevious        = "no_previous"
	FailureNoArtifact        = "no_artifact"
)

type UnavailableContext uint8

const (
	UnavailableForLaunch UnavailableContext = iota
	UnavailableForLoaderChange
	UnavailableForMount
)

var tokenSeparatorEscaper = strings.NewReplacer(":", "%3A", "=", "%3D")

var loaderFailureReasons = []struct {
	sentinel error
	reason   string
}{
	{ErrInterrupted, FailureInterrupted},
	{ErrGameChanged, FailureGameChanged},
	{ErrNoVanillaLauncher, FailureNoVanillaLauncher},
	{ErrUnsafeTarget, FailureUnsafeTarget},
	{ErrFarmMounted, FailureFarmMounted},
	{ErrStageIncomplete, FailureStageIncomplete},
	{ErrStageUnexpected, FailureStageUnexpected},
	{ErrCrossDevice, FailureCrossDevice},
	{ErrRenameUnsupported, FailureRenameUnsupported},
	{ErrNoArtifact, FailureNoArtifact},
}

var releaseFailureReasons = []struct {
	sentinel error
	reason   string
}{
	{ghrelease.ErrDigestMismatch, FailureDigestMismatch},
	{ghrelease.ErrNotStable, FailureNotStable},
	{ghrelease.ErrNoDigest, FailureNoDigest},
	{ghrelease.ErrNoPrevious, FailureNoPrevious},
	{ghrelease.ErrPreviousUnavailable, FailureNoPrevious},
}

type NotAModError struct {
	Reason string
	Detail string
}

// Error formats the refusal as a machine-readable reason with an optional percent-escaped detail.
func (e NotAModError) Error() string {
	text := "not_a_mod:reason=" + e.Reason
	if e.Detail == "" {
		return text
	}
	return text + ":detail=" + escapeDetail(e.Detail)
}

// Is reports whether target is ErrNotAMod.
func (e NotAModError) Is(target error) bool {
	return target == ErrNotAMod
}

// escapeDetail percent-escapes an archive-controlled value so it cannot inject token separators or another error token.
func escapeDetail(detail string) string {
	return tokenSeparatorEscaper.Replace(url.PathEscape(detail))
}

type UnsafeTargetError struct {
	Path   string
	Reason string
}

// Error describes the refused game-root path and why it is unsafe.
func (e *UnsafeTargetError) Error() string {
	return fmt.Sprintf("unsafe mod-loader target %q: %s", e.Path, e.Reason)
}

// Is reports whether target is ErrUnsafeTarget.
func (e *UnsafeTargetError) Is(target error) bool {
	return target == ErrUnsafeTarget
}

type UnavailableError struct {
	GameID  string
	State   LoaderState
	Context UnavailableContext
}

// Error explains, for the refused action, why the game's current loader state does not allow it.
func (e *UnavailableError) Error() string {
	state := strings.ReplaceAll(e.State.String(), "_", " ")
	switch e.Context {
	case UnavailableForLoaderChange:
		if e.State == StateUnsupportedBuild {
			return fmt.Sprintf("SMAPI cannot be installed, updated, or removed for %s: this game build is not supported", e.GameID)
		}
		return fmt.Sprintf("SMAPI cannot be installed, updated, or removed for %s while it is %s", e.GameID, state)
	case UnavailableForMount:
		if e.State == StateInterrupted {
			return fmt.Sprintf("%s has an interrupted SMAPI transaction; repair SMAPI or restart gorganizer to recover it before mounting or launching", e.GameID)
		}
		return fmt.Sprintf("%s cannot be mounted or launched while SMAPI is %s", e.GameID, state)
	default:
		return fmt.Sprintf("%s has SMAPI mods enabled but SMAPI is %s; install or repair SMAPI first", e.GameID, state)
	}
}

type RollbackFailedError struct {
	Cause       error
	RollbackErr error
}

// Error reports the failure that aborted a transaction and why rolling it back failed.
func (e *RollbackFailedError) Error() string {
	return fmt.Sprintf("%v; rolling back failed: %v: %v", e.Cause, e.RollbackErr, ErrInterrupted)
}

// Unwrap returns the aborting failure, the rollback failure, and ErrInterrupted.
func (e *RollbackFailedError) Unwrap() []error {
	return []error{e.Cause, e.RollbackErr, ErrInterrupted}
}

type FailedError struct {
	GameID string
	Reason string
	Err    error
}

// Error returns the message of the underlying mod-loader failure.
func (e *FailedError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying mod-loader failure.
func (e *FailedError) Unwrap() error {
	return e.Err
}

// FailureReason returns the machine-readable reason of an explicit FailedError or of a mod-loader sentinel in err.
func FailureReason(err error) (string, bool) {
	var failed *FailedError
	if errors.As(err, &failed) && failed != nil {
		return failed.Reason, true
	}
	return matchReason(err, loaderFailureReasons)
}

// ClassifyFailure wraps err in a FailedError when a mod-loader or release sentinel explains it, and returns other errors unchanged.
func ClassifyFailure(gameID string, err error) error {
	if err == nil {
		return nil
	}
	var failed *FailedError
	if errors.As(err, &failed) {
		return err
	}
	reason, ok := matchReason(err, loaderFailureReasons)
	if !ok {
		reason, ok = matchReason(err, releaseFailureReasons)
	}
	if !ok {
		return err
	}
	return &FailedError{GameID: gameID, Reason: reason, Err: err}
}

// matchReason returns the reason of the first sentinel in table that err wraps.
func matchReason(err error, table []struct {
	sentinel error
	reason   string
}) (string, bool) {
	for _, entry := range table {
		if errors.Is(err, entry.sentinel) {
			return entry.reason, true
		}
	}
	return "", false
}
