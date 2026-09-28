package dto

import "fmt"

type ModLoaderStateResult int32

const (
	ModLoaderStateUnspecified      ModLoaderStateResult = 0
	ModLoaderStateNotInstalled     ModLoaderStateResult = 1
	ModLoaderStateOK               ModLoaderStateResult = 2
	ModLoaderStateLauncherReverted ModLoaderStateResult = 3
	ModLoaderStateIncomplete       ModLoaderStateResult = 4
	ModLoaderStateUnsupportedBuild ModLoaderStateResult = 5
	ModLoaderStateInterrupted      ModLoaderStateResult = 6
)

type ModLoaderStatusResult struct {
	GameID           string
	Kind             ModLoaderKindResult
	State            ModLoaderStateResult
	Managed          bool
	InstalledVersion string
	ActiveVersion    string
	PreviousVersion  string
	LatestVersion    string
	UpdateAvailable  bool
	Busy             bool
	Detail           string
}

const (
	BusyOperationModLoader        = "modloader"
	BusyOperationMounted          = "mounted"
	BusyOperationRunning          = "running"
	BusyOperationRootDeployment   = "root_deployment"
	BusyOperationTransaction      = "transaction"
	BusyOperationLaunch           = "launch"
	BusyOperationTool             = "tool"
	BusyOperationMount            = "mount"
	BusyOperationUnmount          = "unmount"
	BusyOperationApply            = "apply"
	BusyOperationConfigure        = "configure"
	BusyOperationScriptExtender   = "script_extender"
	BusyOperationImport           = "import"
	BusyOperationReinstall        = "reinstall"
	BusyOperationInstall          = "install"
	BusyOperationRegisterInstall  = "register_install"
	BusyOperationExtractOverwrite = "extract_overwrite"
	BusyOperationRecovery         = "recovery"
)

type OperationBusyError struct {
	GameID    string
	Operation string
	Holder    string
}

// Error names the operation and the game that hold the install and what the user must do before retrying.
func (e *OperationBusyError) Error() string {
	holder := e.Holder
	if holder == "" {
		holder = e.GameID
	}
	switch e.Operation {
	case BusyOperationModLoader:
		return fmt.Sprintf("a mod-loader operation is in progress for %s; wait for it to finish", holder)
	case BusyOperationMounted:
		return fmt.Sprintf("the mods of %s are mounted; unmount the mods first", holder)
	case BusyOperationRunning:
		return fmt.Sprintf("%s or one of its tools is running; close it first", holder)
	case BusyOperationRootDeployment:
		return fmt.Sprintf("a game-root deployment is active for %s; unmount the mods first", holder)
	case BusyOperationTransaction:
		return fmt.Sprintf("another mod-loader transaction holds the %s game directory", holder)
	default:
		if holder != e.GameID {
			return fmt.Sprintf("%s is busy: %s of %s is in progress on the same install; try again when it finishes", e.GameID, e.Operation, holder)
		}
		return fmt.Sprintf("%s is busy: %s is in progress; try again when it finishes", e.GameID, e.Operation)
	}
}

type ModLoaderUnsupportedError struct {
	GameID string
}

// Error reports that the game has no managed mod loader.
func (e *ModLoaderUnsupportedError) Error() string {
	return fmt.Sprintf("%s has no managed mod loader", e.GameID)
}
