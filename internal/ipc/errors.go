package ipc

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ArchiveMissingError is returned when an operation references an archive
type ArchiveMissingError struct {
	GameID string
	Path   string
}

func (e *ArchiveMissingError) Error() string {
	return fmt.Sprintf("archive %q not found for game %s", e.Path, e.GameID)
}

// FomodRequiredError is returned by StartInstall when the archive is a FOMOD
type FomodRequiredError struct {
	GameID    string
	Path      string
	PreviewID string
}

func (e *FomodRequiredError) Error() string {
	return fmt.Sprintf("archive %q is a FOMOD package; call PreviewInstall first", e.Path)
}

// ModCollisionError is returned by StartInstall when mode=NEW_MOD and the
// target mod folder already exists.
type ModCollisionError struct {
	Name         string
	ExistingMods []string
}

func (e *ModCollisionError) Error() string {
	return fmt.Sprintf("mod folder %q already exists", e.Name)
}

// ModNotFoundError is returned by mod operations when the named mod folder
// doesn't exist under ModsDir.
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

// NXMExpiredError is returned by StartDownload and by the ledger resume path
// when the NXM URI's signed token has expired. The UI should tell the user
type NXMExpiredError struct {
	URI string
}

func (e *NXMExpiredError) Error() string {
	return fmt.Sprintf("NXM download link expired: %s", e.URI)
}

// DownloadNotFoundError is returned by CancelDownload and RetryDownload for
// IDs the manager doesn't recognize (already terminal, never existed).
type DownloadNotFoundError struct {
	ID string
}

func (e *DownloadNotFoundError) Error() string {
	return fmt.Sprintf("download %q not found", e.ID)
}

// PreviewNotFoundError is returned by StartInstall when preview_id refers to
// a cache entry that has expired or was discarded.
type PreviewNotFoundError struct {
	PreviewID string
}

func (e *PreviewNotFoundError) Error() string {
	return fmt.Sprintf("install preview %q not found (expired or discarded)", e.PreviewID)
}

// VFSMutexError is returned by MountVFS / LaunchGame when the requested
type VFSMutexError struct {
	GameID      string
	Conflicting string
	Group       string
}

func (e *VFSMutexError) Error() string {
	return fmt.Sprintf("cannot mount %s: %s is currently mounted (mutex group %q)",
		e.GameID, e.Conflicting, e.Group)
}

// ErrLinkedParentMissing is returned when a synthetic game references a
// parent gameID that isn't configured. Only happens if the user manually
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

// ErrPrefixMissing is returned by Wine-backed installer launches when the
// Proton prefix for the parent game does not yet exist. The dialog's
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

// ErrTTWRequiresVanillaFNV is returned by the TTW installer pre-flight
// check when FNV's VFS is currently mounted. The TTW installer reads
type ErrTTWRequiresVanillaFNV struct{}

func (e *ErrTTWRequiresVanillaFNV) Error() string {
	return "TTW installer requires Fallout: New Vegas's vanilla Data/ — unmount its VFS first"
}

// ErrXNVSEMissingForTTW is returned by the TTW launch path when no xNVSE
type ErrXNVSEMissingForTTW struct {
	InstallPath string
}

func (e *ErrXNVSEMissingForTTW) Error() string {
	return fmt.Sprintf("xNVSE DLLs not found in %s — install xNVSE for FNV before launching TTW",
		e.InstallPath)
}

// ErrFNV4GBNotAppliedForTTW is returned by the TTW launch pre-flight when
// FalloutNV.exe still has the 32-bit address cap. TTW's merged data set
type ErrFNV4GBNotAppliedForTTW struct {
	InstallPath string
}

func (e *ErrFNV4GBNotAppliedForTTW) Error() string {
	return fmt.Sprintf("FalloutNV.exe is not LAA-patched — TTW will CTD on the main menu. Apply the 4GB patcher in Tools → \"Patch FalloutNV.exe to 4GB\" before launching (install: %s)",
		e.InstallPath)
}

// MapError turns a structured error into a gRPC status. Any error it doesn't
// recognize passes through to the caller's default handler.
func MapError(err error) (error, bool) {
	if err == nil {
		return nil, true
	}
	var loader *LoaderMissingError
	if errors.As(err, &loader) {
		msg := fmt.Sprintf("loader_missing:reason=%s:exe=%s:install_path=%s:game=%s",
			loader.Reason, loader.ConfiguredExe, loader.InstallPath, loader.GameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var archive *ArchiveMissingError
	if errors.As(err, &archive) {
		msg := fmt.Sprintf("archive_missing:game=%s:path=%s", archive.GameID, archive.Path)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fomod *FomodRequiredError
	if errors.As(err, &fomod) {
		msg := fmt.Sprintf("fomod_required:game=%s:path=%s:preview_id=%s",
			fomod.GameID, fomod.Path, fomod.PreviewID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var collision *ModCollisionError
	if errors.As(err, &collision) {
		msg := fmt.Sprintf("mod_collision:name=%s:existing=%s",
			collision.Name, strings.Join(collision.ExistingMods, ","))
		return status.Error(codes.AlreadyExists, msg), true
	}
	var notFound *ModNotFoundError
	if errors.As(err, &notFound) {
		msg := fmt.Sprintf("mod_not_found:game=%s:name=%s", notFound.GameID, notFound.Name)
		return status.Error(codes.NotFound, msg), true
	}
	var inUse *ModInUseError
	if errors.As(err, &inUse) {
		msg := fmt.Sprintf("mod_in_use:name=%s:profiles=%s",
			inUse.Name, strings.Join(inUse.Profiles, ","))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var nxm *NXMExpiredError
	if errors.As(err, &nxm) {
		msg := fmt.Sprintf("nxm_expired:uri=%s", nxm.URI)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var dl *DownloadNotFoundError
	if errors.As(err, &dl) {
		msg := fmt.Sprintf("download_not_found:id=%s", dl.ID)
		return status.Error(codes.NotFound, msg), true
	}
	var prev *PreviewNotFoundError
	if errors.As(err, &prev) {
		msg := fmt.Sprintf("preview_not_found:id=%s", prev.PreviewID)
		return status.Error(codes.NotFound, msg), true
	}
	var mutex *VFSMutexError
	if errors.As(err, &mutex) {
		msg := fmt.Sprintf("vfs_mutex:game=%s:conflicting=%s:group=%s",
			mutex.GameID, mutex.Conflicting, mutex.Group)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var linkedParent *ErrLinkedParentMissing
	if errors.As(err, &linkedParent) {
		msg := fmt.Sprintf("linked_parent_missing:game=%s:parent=%s",
			linkedParent.GameID, linkedParent.ParentGameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var drift *TTWDriftError
	if errors.As(err, &drift) {
		msg := fmt.Sprintf("ttw_drift:reason=%s:install_path=%s",
			drift.Reason, drift.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var prefix *ErrPrefixMissing
	if errors.As(err, &prefix) {
		msg := fmt.Sprintf("prefix_missing:game=%s:expected=%s",
			prefix.GameID, prefix.ExpectedPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var steam *ErrSteamNotRunning
	if errors.As(err, &steam) {
		return status.Error(codes.FailedPrecondition, "steam_not_running:"), true
	}
	var ttwVanilla *ErrTTWRequiresVanillaFNV
	if errors.As(err, &ttwVanilla) {
		return status.Error(codes.FailedPrecondition, "ttw_requires_vanilla_fnv:"), true
	}
	var xnvse *ErrXNVSEMissingForTTW
	if errors.As(err, &xnvse) {
		msg := fmt.Sprintf("xnvse_missing_for_ttw:install_path=%s", xnvse.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fnv4gb *ErrFNV4GBNotAppliedForTTW
	if errors.As(err, &fnv4gb) {
		msg := fmt.Sprintf("fnv4gb_not_applied_for_ttw:install_path=%s", fnv4gb.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	return nil, false
}
