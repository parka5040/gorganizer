package transfer

import "fmt"

type TransferGameMismatchError struct {
	Want string
	Got  string
}

func (e *TransferGameMismatchError) Error() string {
	return fmt.Sprintf("transfer_game_mismatch:want=%s:got=%s", e.Want, e.Got)
}

type TransferSchemaError struct {
	Version int
}

func (e *TransferSchemaError) Error() string {
	return fmt.Sprintf("transfer_schema:version=%d", e.Version)
}

type TransferPathError struct {
	Entry string
}

func (e *TransferPathError) Error() string {
	return fmt.Sprintf("transfer_path:entry=%s", e.Entry)
}

type TransferCollisionError struct {
	Name string
}

func (e *TransferCollisionError) Error() string {
	return fmt.Sprintf("transfer_collision:name=%s", e.Name)
}

const (
	BundleRejectedLink        = "link"
	BundleRejectedSpecial     = "special_entry"
	BundleRejectedProfileName = "profile_name"
	BundleRejectedDuplicate   = "duplicate"
	BundleRejectedLimit       = "limit"
	BundleRejectedManifest    = "manifest"
	BundleRejectedChanged     = "changed"
)

type TransferTooLargeError struct {
	Reason string
	Item   string
}

// Error describes an export that cannot fit within import limits.
func (e *TransferTooLargeError) Error() string {
	return fmt.Sprintf("This export is larger than Gorganizer can import again (%s). Export fewer mods at a time.", e.Reason)
}

type BundleRejectedError struct {
	Reason string
	Item   string
}

// Error returns the reason the export bundle was refused and the offending item.
func (e *BundleRejectedError) Error() string {
	return fmt.Sprintf("export bundle rejected: %s: %q", e.Reason, e.Item)
}

type transferCommitError struct {
	committed bool
	pending   bool
	err       error
}

// Error describes a failed replacement and whether its journal remains for recovery.
func (e *transferCommitError) Error() string { return e.err.Error() }

// Unwrap returns the filesystem error that stopped the replacement.
func (e *transferCommitError) Unwrap() error { return e.err }

type BundleIncompleteError struct {
	Items    int
	Recovery string
	Err      error
}

// Error reports how many items an interrupted import committed and whether a replacement still needs recovery.
func (e *BundleIncompleteError) Error() string {
	return fmt.Sprintf("import stopped after %d items (recovery %s): %v", e.Items, e.Recovery, e.Err)
}

// Unwrap returns the failure that stopped the import.
func (e *BundleIncompleteError) Unwrap() error {
	return e.Err
}
