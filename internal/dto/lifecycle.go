package dto

import "fmt"

const (
	GameRunningOperationLaunch    = "launch"
	GameRunningOperationApply     = "apply"
	GameRunningOperationUnmount   = "unmount"
	GameRunningOperationUninstall = "uninstall"
	GameRunningOperationRename    = "rename"
	GameRunningOperationRetarget  = "retarget"
)

type ShuttingDownError struct {
	Operation string
}

// Error reports that the daemon refused the operation because it is shutting down.
func (e *ShuttingDownError) Error() string {
	if e.Operation == "" {
		return "daemon is shutting down"
	}
	return "daemon is shutting down; " + e.Operation + " was refused"
}

type GameRunningError struct {
	GameID    string
	Operation string
}

// Error reports that a game running or recently launched prevents changing its deployed mods.
func (e *GameRunningError) Error() string {
	if e.Operation == GameRunningOperationUnmount {
		return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so its mods cannot be unmounted; close it first", e.GameID)
	}
	if e.Operation == GameRunningOperationUninstall {
		return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so a mod cannot be uninstalled; close the game first", e.GameID)
	}
	if e.Operation == GameRunningOperationRename {
		return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so a mod cannot be renamed; close the game first", e.GameID)
	}
	if e.Operation == GameRunningOperationRetarget {
		return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so its deployed mods cannot be switched; close the game first", e.GameID)
	}
	return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so its pending mod changes cannot be applied (%s); close it first", e.GameID, e.Operation)
}

type PluginStateError struct {
	GameID string
	Cause  error
}

// Error reports that the game's plugin list could not be prepared, so the game was not started.
func (e *PluginStateError) Error() string {
	return fmt.Sprintf("the plugin list for %s could not be prepared, so the game was not started: %v", e.GameID, e.Cause)
}

// Unwrap returns the underlying plugin-list failure.
func (e *PluginStateError) Unwrap() error {
	return e.Cause
}

type RecoveryDeferredError struct {
	GameID    string
	Operation string
}

// Error reports that an interrupted mod deployment waits for the game to close before it can be repaired.
func (e *RecoveryDeferredError) Error() string {
	return fmt.Sprintf("%s still has an interrupted mod deployment that is waiting for the game to close (%s)", e.GameID, e.Operation)
}

type RecoveryStaleError struct {
	GameID string
}

// Error reports that a recovery confirmation no longer matches the game's current pending recovery.
func (e *RecoveryStaleError) Error() string {
	return fmt.Sprintf("the recovery confirmed for %s is no longer the pending one", e.GameID)
}
