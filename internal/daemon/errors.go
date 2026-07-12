package daemon

import (
	"fmt"
	"strings"
)

type ArchiveMissingError struct {
	GameID string
	Path   string
}

func (e *ArchiveMissingError) Error() string {
	return fmt.Sprintf("archive %q not found for game %s", e.Path, e.GameID)
}

type FomodRequiredError struct {
	GameID    string
	Path      string
	PreviewID string
}

func (e *FomodRequiredError) Error() string {
	return fmt.Sprintf("archive %q is a FOMOD package; call PreviewInstall first", e.Path)
}

type ModCollisionError struct {
	Name         string
	ExistingMods []string
}

func (e *ModCollisionError) Error() string {
	return fmt.Sprintf("mod folder %q already exists", e.Name)
}

type ModNotFoundError struct {
	GameID string
	Name   string
}

func (e *ModNotFoundError) Error() string {
	return fmt.Sprintf("mod %q not found for game %s", e.Name, e.GameID)
}

type ModInUseError struct {
	Name     string
	Profiles []string
}

func (e *ModInUseError) Error() string {
	return fmt.Sprintf("mod %q is enabled in profile(s): %s",
		e.Name, strings.Join(e.Profiles, ", "))
}

type PreviewNotFoundError struct {
	PreviewID string
}

func (e *PreviewNotFoundError) Error() string {
	return fmt.Sprintf("install preview %q not found (expired or discarded)", e.PreviewID)
}

type VFSMutexError struct {
	GameID      string
	Conflicting string
	Group       string
}

func (e *VFSMutexError) Error() string {
	return fmt.Sprintf("cannot mount %s: %s is currently mounted (mutex group %q)",
		e.GameID, e.Conflicting, e.Group)
}

type ErrLinkedParentMissing struct {
	GameID       string
	ParentGameID string
}

func (e *ErrLinkedParentMissing) Error() string {
	return fmt.Sprintf("synthetic game %s requires parent %s to be configured first",
		e.GameID, e.ParentGameID)
}

type TTWDriftError struct {
	InstallPath string
	Reason      string
}

func (e *TTWDriftError) Error() string {
	return fmt.Sprintf("TTW integrity drift at %s: %s", e.InstallPath, e.Reason)
}

type ErrTTWRequiresVanillaFNV struct{}

func (e *ErrTTWRequiresVanillaFNV) Error() string {
	return "TTW installer requires Fallout: New Vegas's vanilla Data/ — unmount its VFS first"
}

type ErrXNVSEMissingForTTW struct {
	InstallPath string
}

func (e *ErrXNVSEMissingForTTW) Error() string {
	return fmt.Sprintf("xNVSE DLLs not found in %s — install xNVSE for FNV before launching TTW",
		e.InstallPath)
}

type TransferOverwriteMountedError struct {
	Name string
}

func (e *TransferOverwriteMountedError) Error() string {
	return fmt.Sprintf("transfer_overwrite_mounted:name=%s", e.Name)
}

type ErrFNV4GBNotAppliedForTTW struct {
	InstallPath string
}

func (e *ErrFNV4GBNotAppliedForTTW) Error() string {
	return fmt.Sprintf("FalloutNV.exe is not LAA-patched — TTW will CTD on the main menu. Apply the 4GB patcher in Tools → \"Patch FalloutNV.exe to 4GB\" before launching (install: %s)",
		e.InstallPath)
}
