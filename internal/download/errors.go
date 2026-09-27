package download

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidNXMURI          = errors.New("download: invalid NXM URI")
	ErrUnknownSlug            = errors.New("download: unknown game slug")
	ErrDownloadFailed         = errors.New("download: HTTP download failed")
	ErrUnsupportedArchive     = errors.New("download: unsupported archive format")
	ErrUnsafeArchive          = errors.New("download: unsafe archive")
	ErrNoMainFile             = errors.New("download: no MAIN-category file")
	ErrAmbiguousMainFile      = errors.New("download: ambiguous MAIN-category files")
	ErrInvalidMainFileOptions = errors.New("download: invalid MAIN-file selection options")
	ErrCrossHostRedirect      = errors.New("download: refusing redirect to a different host")

	ErrFomodNotSupportedForLayout = errors.New("download: FOMOD selections are not supported for this install layout")
	ErrEmptyInstallSelection      = errors.New("download: the installer selection contains no files")
)

type NXMExpiredError struct {
	URI string
}

func (e *NXMExpiredError) Error() string {
	return fmt.Sprintf("NXM download link expired: %s", e.URI)
}

type DownloadNotFoundError struct {
	ID string
}

func (e *DownloadNotFoundError) Error() string {
	return fmt.Sprintf("download %q not found", e.ID)
}

type LayoutUnsupportedError struct {
	GameID string
	Layout string
}

// Error returns the unsupported install layout message.
func (e *LayoutUnsupportedError) Error() string {
	return fmt.Sprintf("game %s uses the %s install layout, which this build cannot install yet", e.GameID, e.Layout)
}

type InvalidTargetModError struct {
	Name   string
	Reason string
}

// Error returns the refused target mod folder name and why it was refused.
func (e *InvalidTargetModError) Error() string {
	return fmt.Sprintf("invalid target mod folder %q: %s", e.Name, e.Reason)
}

type ManifestLayoutError struct {
	Mod    string
	Reason string
}

// Error returns the manifest-layout refusal for a registered mod folder.
func (e *ManifestLayoutError) Error() string {
	return fmt.Sprintf("mod %q does not have a valid SMAPI manifest-folder layout: %s", e.Mod, e.Reason)
}

type ReinstallSourceMissingError struct {
	Mod  string
	Path string
}

// Error returns the missing reinstall source message.
func (e *ReinstallSourceMissingError) Error() string {
	return fmt.Sprintf("cannot reinstall mod %q: source archive %q is missing or unreadable", e.Mod, e.Path)
}

type ModRegistrationError struct {
	Mod string
	Err error
}

// Error returns the failed mod-list registration message.
func (e *ModRegistrationError) Error() string {
	return fmt.Sprintf("mod %q is installed but could not be added to the mod list: %v", e.Mod, e.Err)
}

// Unwrap returns the underlying registration failure.
func (e *ModRegistrationError) Unwrap() error {
	return e.Err
}

type ModMountedError struct {
	Mod string
}

// Error returns the refusal to rebuild a mod that the mounted profile has enabled.
func (e *ModMountedError) Error() string {
	return fmt.Sprintf("mod %q is enabled in the mounted profile; unmount the game before reinstalling it", e.Mod)
}

type FomodReinstallUnsupportedError struct {
	Mod string
}

// Error returns the refusal to replay a mod whose source archive needs the FOMOD installer.
func (e *FomodReinstallUnsupportedError) Error() string {
	return fmt.Sprintf("mod %q was installed through a FOMOD installer and cannot be reinstalled automatically; reinstall it from its archive", e.Mod)
}

const (
	ArchiveRejectedUnsafeEntry     = "unsafe_entry"
	ArchiveRejectedNestedInstaller = "nested_installer"
	ArchiveRejectedLimit           = "limit"
	ArchiveRejectedDestination     = "destination"
	ArchiveRejectedUnsupported     = "unsupported"
)

type ArchiveRejectedError struct {
	Reason string
	Detail string
}

// Error returns the reason the archive was refused and the offending detail.
func (e *ArchiveRejectedError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("archive rejected: %s", e.Reason)
	}
	return fmt.Sprintf("archive rejected: %s: %s", e.Reason, e.Detail)
}

// Unwrap reports ErrUnsafeArchive so callers matching the sentinel keep working.
func (e *ArchiveRejectedError) Unwrap() error {
	return ErrUnsafeArchive
}
