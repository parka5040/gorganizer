package dto

import "errors"

type InstallOutcomeState int32

const (
	InstallOutcomeUnknown   InstallOutcomeState = 1
	InstallOutcomeRunning   InstallOutcomeState = 2
	InstallOutcomeSucceeded InstallOutcomeState = 3
	InstallOutcomeFailed    InstallOutcomeState = 4
	InstallOutcomeCancelled InstallOutcomeState = 5
)

type InstallOutcome struct {
	State            InstallOutcomeState
	ModFolder        string
	FileCount        int
	Err              error
	ArchivesReplayed int
	ArchivesSkipped  int
}

var ErrDuplicateClientRequestID = errors.New("duplicate client request id")
var ErrInvalidClientRequestID = errors.New("invalid client request id")
var ErrInstallOutcomeGameMismatch = errors.New("client request id belongs to a different game")
var ErrInstallOutcomeFull = errors.New("too many installations in progress")
