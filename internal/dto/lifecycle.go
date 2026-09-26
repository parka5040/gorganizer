package dto

import "fmt"

const (
	GameRunningOperationLaunch = "launch"
	GameRunningOperationApply  = "apply"
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

// Error reports that pending mod changes cannot be applied because the game is running or was just started.
func (e *GameRunningError) Error() string {
	return fmt.Sprintf("%s is still running, or was started less than two minutes ago, so its pending mod changes cannot be applied (%s); close it first", e.GameID, e.Operation)
}
