package ipc

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Structured error types returned by daemon methods. Each maps to a gRPC
// FailedPrecondition with a machine-parseable prefix so the Qt UI can branch
// into a specific dialog instead of showing a raw error string.
//
// Wire format:
//
//	<kind>:<k1>=<v1>:<k2>=<v2>:...
//
// Colons inside values are legal if the value is the last key (the frontend
// parses left-to-right and keeps the tail). The prefixes also serve as the
// error's discriminant — the frontend matches on `strings.HasPrefix(msg,
// "<kind>:")`.

// ArchiveMissingError is returned when an operation references an archive
// that isn't on disk under the Downloads dir — either because it was
// manually deleted or never fully downloaded.
type ArchiveMissingError struct {
	GameID string
	Path   string
}

func (e *ArchiveMissingError) Error() string {
	return fmt.Sprintf("archive %q not found for game %s", e.Path, e.GameID)
}

// FomodRequiredError is returned by StartInstall when the archive is a FOMOD
// package and the caller did not pre-select files via PreviewInstall +
// fomod_selected_files. The PreviewID, if non-empty, is the daemon-cached
// extraction the frontend can reuse when it posts the user's selections.
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

// ModInUseError is returned by UninstallMod without force=true when the mod
// is currently enabled in one or more profiles. The frontend uses the
// Profiles list to build a "Still uninstall?" prompt and retries with
// force=true on confirmation.
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
// to re-click "Download with Manager" on the Nexus page.
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
	return nil, false
}
