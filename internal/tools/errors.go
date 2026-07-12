package tools

import "fmt"

type LoaderMissingError struct {
	GameID        string
	ConfiguredExe string
	InstallPath   string
	Reason        string
}

func (e *LoaderMissingError) Error() string {
	if e.ConfiguredExe == "" {
		return fmt.Sprintf("script extender loader not found for %s in %s (%s)",
			e.GameID, e.InstallPath, e.Reason)
	}
	return fmt.Sprintf("script extender loader %q not found in %s (%s)",
		e.ConfiguredExe, e.InstallPath, e.Reason)
}

type ErrPrefixMissing struct {
	GameID       string
	ExpectedPath string
}

func (e *ErrPrefixMissing) Error() string {
	return fmt.Sprintf("proton prefix for %s does not exist at %s — bootstrap it first",
		e.GameID, e.ExpectedPath)
}

type ErrSteamNotRunning struct{}

func (e *ErrSteamNotRunning) Error() string {
	return "steam is not running — Backend A requires Steam to be running before launching the TTW installer"
}
