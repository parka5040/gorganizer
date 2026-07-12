package ipc

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/transfer"
	"github.com/parka/gorganizer/internal/vfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MapError turns a structured error into a gRPC status; unrecognized errors pass through with ok=false.
func MapError(err error) (error, bool) {
	if err == nil {
		return nil, true
	}
	var loader *tools.LoaderMissingError
	if errors.As(err, &loader) {
		msg := fmt.Sprintf("loader_missing:reason=%s:exe=%s:install_path=%s:game=%s",
			loader.Reason, loader.ConfiguredExe, loader.InstallPath, loader.GameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var archive *daemon.ArchiveMissingError
	if errors.As(err, &archive) {
		msg := fmt.Sprintf("archive_missing:game=%s:path=%s", archive.GameID, archive.Path)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fomod *daemon.FomodRequiredError
	if errors.As(err, &fomod) {
		msg := fmt.Sprintf("fomod_required:game=%s:path=%s:preview_id=%s",
			fomod.GameID, fomod.Path, fomod.PreviewID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var collision *daemon.ModCollisionError
	if errors.As(err, &collision) {
		msg := fmt.Sprintf("mod_collision:name=%s:existing=%s",
			collision.Name, strings.Join(collision.ExistingMods, ","))
		return status.Error(codes.AlreadyExists, msg), true
	}
	var notFound *daemon.ModNotFoundError
	if errors.As(err, &notFound) {
		msg := fmt.Sprintf("mod_not_found:game=%s:name=%s", notFound.GameID, notFound.Name)
		return status.Error(codes.NotFound, msg), true
	}
	var inUse *daemon.ModInUseError
	if errors.As(err, &inUse) {
		msg := fmt.Sprintf("mod_in_use:name=%s:profiles=%s",
			inUse.Name, strings.Join(inUse.Profiles, ","))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var nxm *download.NXMExpiredError
	if errors.As(err, &nxm) {
		msg := fmt.Sprintf("nxm_expired:uri=%s", nxm.URI)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var dl *download.DownloadNotFoundError
	if errors.As(err, &dl) {
		msg := fmt.Sprintf("download_not_found:id=%s", dl.ID)
		return status.Error(codes.NotFound, msg), true
	}
	var prev *daemon.PreviewNotFoundError
	if errors.As(err, &prev) {
		msg := fmt.Sprintf("preview_not_found:id=%s", prev.PreviewID)
		return status.Error(codes.NotFound, msg), true
	}
	var mutex *daemon.VFSMutexError
	if errors.As(err, &mutex) {
		msg := fmt.Sprintf("vfs_mutex:game=%s:conflicting=%s:group=%s",
			mutex.GameID, mutex.Conflicting, mutex.Group)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var linkedParent *daemon.ErrLinkedParentMissing
	if errors.As(err, &linkedParent) {
		msg := fmt.Sprintf("linked_parent_missing:game=%s:parent=%s",
			linkedParent.GameID, linkedParent.ParentGameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var drift *daemon.TTWDriftError
	if errors.As(err, &drift) {
		msg := fmt.Sprintf("ttw_drift:reason=%s:install_path=%s",
			drift.Reason, drift.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var prefix *tools.ErrPrefixMissing
	if errors.As(err, &prefix) {
		msg := fmt.Sprintf("prefix_missing:game=%s:expected=%s",
			prefix.GameID, prefix.ExpectedPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var steamNotRunning *tools.ErrSteamNotRunning
	if errors.As(err, &steamNotRunning) {
		return status.Error(codes.FailedPrecondition, "steam_not_running:"), true
	}
	var ttwVanilla *daemon.ErrTTWRequiresVanillaFNV
	if errors.As(err, &ttwVanilla) {
		return status.Error(codes.FailedPrecondition, "ttw_requires_vanilla_fnv:"), true
	}
	var xnvse *daemon.ErrXNVSEMissingForTTW
	if errors.As(err, &xnvse) {
		msg := fmt.Sprintf("xnvse_missing_for_ttw:install_path=%s", xnvse.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fnv4gb *daemon.ErrFNV4GBNotAppliedForTTW
	if errors.As(err, &fnv4gb) {
		msg := fmt.Sprintf("fnv4gb_not_applied_for_ttw:install_path=%s", fnv4gb.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var gameMismatch *transfer.TransferGameMismatchError
	if errors.As(err, &gameMismatch) {
		msg := fmt.Sprintf("transfer_game_mismatch:want=%s:got=%s", gameMismatch.Want, gameMismatch.Got)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var schema *transfer.TransferSchemaError
	if errors.As(err, &schema) {
		msg := fmt.Sprintf("transfer_schema:version=%d", schema.Version)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var transferPath *transfer.TransferPathError
	if errors.As(err, &transferPath) {
		msg := fmt.Sprintf("transfer_path:entry=%s", transferPath.Entry)
		return status.Error(codes.InvalidArgument, msg), true
	}
	var transferCollision *transfer.TransferCollisionError
	if errors.As(err, &transferCollision) {
		msg := fmt.Sprintf("transfer_collision:name=%s", transferCollision.Name)
		return status.Error(codes.AlreadyExists, msg), true
	}
	var overwriteMounted *daemon.TransferOverwriteMountedError
	if errors.As(err, &overwriteMounted) {
		msg := fmt.Sprintf("transfer_overwrite_mounted:name=%s", overwriteMounted.Name)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	return nil, false
}

var sentinelCodes = []struct {
	sentinel error
	code     codes.Code
}{
	{vfs.ErrAlreadyMounted, codes.AlreadyExists},
	{vfs.ErrNotMounted, codes.FailedPrecondition},
	{vfs.ErrBackupExists, codes.FailedPrecondition},
	{vfs.ErrDataDirMissing, codes.NotFound},
	{config.ErrInvalidGameID, codes.InvalidArgument},
	{config.ErrNoAPIKey, codes.FailedPrecondition},
	{os.ErrNotExist, codes.NotFound},
}

// grpcError maps errors to gRPC status codes: typed errors via MapError, then sentinels via the table, then codes.Internal.
func grpcError(err error) error {
	if mapped, ok := MapError(err); ok {
		return mapped
	}
	for _, entry := range sentinelCodes {
		if errors.Is(err, entry.sentinel) {
			return status.Error(entry.code, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}
