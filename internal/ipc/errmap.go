package ipc

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/transfer"
	"github.com/parka/gorganizer/internal/vfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tokenDaemonShuttingDown         = "daemon_shutting_down:"
	tokenGameRunning                = "game_running:"
	tokenModRegistrationFailed      = "mod_registration_failed:"
	tokenInvalidTargetMod           = "invalid_target_mod:"
	tokenNotAMod                    = "not_a_mod:"
	tokenFomodUnsupported           = "fomod_unsupported:"
	tokenManifestLayoutInvalid      = "manifest_layout_invalid:"
	tokenModMounted                 = "mod_mounted:"
	tokenFomodReinstallUnsupported  = "fomod_reinstall_unsupported:"
	tokenReinstallSourceMissing     = "reinstall_source_missing:"
	tokenModLoaderBusy              = "modloader_busy:"
	tokenModLoaderUnavailable       = "modloader_unavailable:"
	tokenModLoaderUnsupported       = "modloader_unsupported:"
	tokenModDependenciesUnsupported = "mod_dependencies_unsupported:"
	tokenModLoaderFailed            = "modloader_failed:"
	tokenLoaderMissing              = "loader_missing:"
	tokenArchiveMissing             = "archive_missing:"
	tokenFomodRequired              = "fomod_required:"
	tokenModCollision               = "mod_collision:"
	tokenLayoutUnsupported          = "layout_unsupported:"
	tokenUnsafePath                 = "unsafe_path:"
	tokenModNotFound                = "mod_not_found:"
	tokenModInUse                   = "mod_in_use:"
	tokenNXMExpired                 = "nxm_expired:"
	tokenDownloadNotFound           = "download_not_found:"
	tokenPreviewNotFound            = "preview_not_found:"
	tokenVFSMutex                   = "vfs_mutex:"
	tokenLinkedParentMissing        = "linked_parent_missing:"
	tokenTTWDrift                   = "ttw_drift:"
	tokenPrefixMissing              = "prefix_missing:"
	tokenSteamNotRunning            = "steam_not_running:"
	tokenTTWRequiresVanillaFNV      = "ttw_requires_vanilla_fnv:"
	tokenXNVSEMissingForTTW         = "xnvse_missing_for_ttw:"
	tokenFNV4GBNotAppliedForTTW     = "fnv4gb_not_applied_for_ttw:"
	tokenTransferGameMismatch       = "transfer_game_mismatch:"
	tokenTransferSchema             = "transfer_schema:"
	tokenTransferPath               = "transfer_path:"
	tokenTransferCollision          = "transfer_collision:"
	tokenTransferOverwriteMounted   = "transfer_overwrite_mounted:"
	tokenArchiveRejected            = "archive_rejected:"
	tokenBundleRejected             = "bundle_rejected:"
	tokenProfileIdentityInvalid     = "profile_identity_invalid:"
	tokenInstallSelectionEmpty      = "install_selection_empty:"
	tokenPluginStateFailed          = "plugin_state_failed:"
	tokenFarmRecoveryDeferred       = "farm_recovery_deferred:"
	tokenRecoveryStale              = "recovery_stale:"
	tokenInstallRecordFailed        = "install_record_failed:"
	tokenSteamMaintenanceRequired   = "steam_maintenance_required:"
	tokenBundleIncomplete           = "bundle_incomplete:"
)

var errorTokens = []string{
	tokenDaemonShuttingDown, tokenGameRunning, tokenModRegistrationFailed, tokenInvalidTargetMod, tokenNotAMod, tokenFomodUnsupported,
	tokenManifestLayoutInvalid, tokenModMounted, tokenFomodReinstallUnsupported, tokenReinstallSourceMissing,
	tokenModLoaderBusy, tokenModLoaderUnavailable, tokenModLoaderUnsupported, tokenModDependenciesUnsupported,
	tokenModLoaderFailed, tokenLoaderMissing, tokenArchiveMissing, tokenFomodRequired, tokenModCollision,
	tokenLayoutUnsupported, tokenUnsafePath, tokenModNotFound, tokenModInUse, tokenNXMExpired, tokenDownloadNotFound,
	tokenPreviewNotFound, tokenVFSMutex, tokenLinkedParentMissing, tokenTTWDrift, tokenPrefixMissing,
	tokenSteamNotRunning, tokenTTWRequiresVanillaFNV, tokenXNVSEMissingForTTW, tokenFNV4GBNotAppliedForTTW,
	tokenTransferGameMismatch, tokenTransferSchema, tokenTransferPath, tokenTransferCollision, tokenTransferOverwriteMounted,
	tokenArchiveRejected, tokenBundleRejected, tokenProfileIdentityInvalid, tokenInstallSelectionEmpty,
	tokenPluginStateFailed, tokenFarmRecoveryDeferred, tokenRecoveryStale,
	tokenInstallRecordFailed, tokenSteamMaintenanceRequired, tokenBundleIncomplete,
}

// MapError turns a structured error into a gRPC status; unrecognized errors pass through with ok=false.
func MapError(err error) (error, bool) {
	if err == nil {
		return nil, true
	}
	var shuttingDown *dto.ShuttingDownError
	if errors.As(err, &shuttingDown) {
		return status.Error(codes.Unavailable, tokenDaemonShuttingDown), true
	}
	var pluginState *dto.PluginStateError
	if errors.As(err, &pluginState) {
		return status.Error(codes.FailedPrecondition, tokenPluginStateFailed+"game="+escapeTokenValue(pluginState.GameID)), true
	}
	var deferred *dto.RecoveryDeferredError
	if errors.As(err, &deferred) {
		msg := tokenFarmRecoveryDeferred + fmt.Sprintf("game=%s:operation=%s", escapeTokenValue(deferred.GameID), escapeTokenValue(deferred.Operation))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var incomplete *transfer.BundleIncompleteError
	if errors.As(err, &incomplete) {
		msg := tokenBundleIncomplete + fmt.Sprintf("items=%d:recovery=%s", incomplete.Items, escapeTokenValue(incomplete.Recovery))
		return status.Error(codes.Aborted, msg), true
	}
	var maintenance *dto.SteamMaintenanceError
	if errors.As(err, &maintenance) {
		msg := tokenSteamMaintenanceRequired + fmt.Sprintf("game=%s:reason=%s", escapeTokenValue(maintenance.GameID), escapeTokenValue(maintenance.Reason))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var stale *dto.RecoveryStaleError
	if errors.As(err, &stale) {
		return status.Error(codes.FailedPrecondition, tokenRecoveryStale+"game="+escapeTokenValue(stale.GameID)), true
	}
	var running *dto.GameRunningError
	if errors.As(err, &running) {
		msg := tokenGameRunning + fmt.Sprintf("game=%s:operation=%s", escapeTokenValue(running.GameID), escapeTokenValue(running.Operation))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var registration *download.ModRegistrationError
	if errors.As(err, &registration) {
		msg := tokenModRegistrationFailed + "mod=" + escapeTokenValue(registration.Mod)
		return status.Error(codes.Internal, msg), true
	}
	var installRecord *download.InstallRecordError
	if errors.As(err, &installRecord) {
		return status.Error(codes.Internal, tokenInstallRecordFailed+"mod="+escapeTokenValue(installRecord.Mod)), true
	}
	var invalidTarget *download.InvalidTargetModError
	if errors.As(err, &invalidTarget) {
		msg := tokenInvalidTargetMod + "name=" + escapeTokenValue(invalidTarget.Name)
		return status.Error(codes.InvalidArgument, msg), true
	}
	if notAMod, ok := asNotAModError(err); ok {
		msg := tokenNotAMod + fmt.Sprintf("reason=%s:detail=%s", escapeTokenValue(notAMod.Reason), escapeTokenValue(notAMod.Detail))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	if errors.Is(err, download.ErrFomodNotSupportedForLayout) {
		return status.Error(codes.InvalidArgument, tokenFomodUnsupported+"layout=manifest"), true
	}
	var manifestLayout *download.ManifestLayoutError
	if errors.As(err, &manifestLayout) {
		msg := tokenManifestLayoutInvalid + fmt.Sprintf("mod=%s:reason=%s", escapeTokenValue(manifestLayout.Mod), escapeTokenValue(manifestLayout.Reason))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var mounted *download.ModMountedError
	if errors.As(err, &mounted) {
		return status.Error(codes.FailedPrecondition, tokenModMounted+"mod="+escapeTokenValue(mounted.Mod)), true
	}
	var fomodReinstall *download.FomodReinstallUnsupportedError
	if errors.As(err, &fomodReinstall) {
		return status.Error(codes.InvalidArgument, tokenFomodReinstallUnsupported+"mod="+escapeTokenValue(fomodReinstall.Mod)), true
	}
	var reinstallSource *download.ReinstallSourceMissingError
	if errors.As(err, &reinstallSource) {
		msg := tokenReinstallSourceMissing + fmt.Sprintf("mod=%s:path=%s", escapeTokenValue(reinstallSource.Mod), escapeTokenValue(reinstallSource.Path))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var busy *dto.OperationBusyError
	if errors.As(err, &busy) {
		msg := tokenModLoaderBusy + fmt.Sprintf("game=%s:operation=%s", escapeTokenValue(busy.GameID), escapeTokenValue(busy.Operation))
		if busy.Holder != "" {
			msg += ":holder=" + escapeTokenValue(busy.Holder)
		}
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var unavailable *smapi.UnavailableError
	if errors.As(err, &unavailable) {
		msg := tokenModLoaderUnavailable + fmt.Sprintf("reason=%s:loader=smapi:game=%s", unavailable.State, escapeTokenValue(unavailable.GameID))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var unsupported *dto.ModLoaderUnsupportedError
	if errors.As(err, &unsupported) {
		return status.Error(codes.InvalidArgument, tokenModLoaderUnsupported+"game="+escapeTokenValue(unsupported.GameID)), true
	}
	var depsUnsupported *dto.ModDependenciesUnsupportedError
	if errors.As(err, &depsUnsupported) {
		return status.Error(codes.InvalidArgument, tokenModDependenciesUnsupported+"game="+escapeTokenValue(depsUnsupported.GameID)), true
	}
	if reason, ok := smapi.FailureReason(err); ok {
		return status.Error(codes.FailedPrecondition, tokenModLoaderFailed+"reason="+escapeTokenValue(reason)), true
	}
	var loader *tools.LoaderMissingError
	if errors.As(err, &loader) {
		msg := tokenLoaderMissing + fmt.Sprintf("reason=%s:exe=%s:install_path=%s:game=%s",
			loader.Reason, loader.ConfiguredExe, loader.InstallPath, loader.GameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var archive *daemon.ArchiveMissingError
	if errors.As(err, &archive) {
		msg := tokenArchiveMissing + fmt.Sprintf("game=%s:path=%s", archive.GameID, archive.Path)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fomod *daemon.FomodRequiredError
	if errors.As(err, &fomod) {
		msg := tokenFomodRequired + fmt.Sprintf("game=%s:path=%s:preview_id=%s",
			fomod.GameID, fomod.Path, fomod.PreviewID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var collision *daemon.ModCollisionError
	if errors.As(err, &collision) {
		msg := tokenModCollision + fmt.Sprintf("name=%s:existing=%s",
			collision.Name, strings.Join(collision.ExistingMods, ","))
		return status.Error(codes.AlreadyExists, msg), true
	}
	var layout *download.LayoutUnsupportedError
	if errors.As(err, &layout) {
		msg := tokenLayoutUnsupported + fmt.Sprintf("game=%s:layout=%s", layout.GameID, layout.Layout)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var unsafePath *daemon.UnsafePathError
	if errors.As(err, &unsafePath) {
		msg := tokenUnsafePath + fmt.Sprintf("field=%s", unsafePath.Field)
		return status.Error(codes.InvalidArgument, msg), true
	}
	var notFound *daemon.ModNotFoundError
	if errors.As(err, &notFound) {
		msg := tokenModNotFound + fmt.Sprintf("game=%s:name=%s", notFound.GameID, notFound.Name)
		return status.Error(codes.NotFound, msg), true
	}
	var inUse *daemon.ModInUseError
	if errors.As(err, &inUse) {
		msg := tokenModInUse + fmt.Sprintf("name=%s:profiles=%s",
			inUse.Name, strings.Join(inUse.Profiles, ","))
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var nxm *download.NXMExpiredError
	if errors.As(err, &nxm) {
		msg := tokenNXMExpired + fmt.Sprintf("uri=%s", nxm.URI)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var dl *download.DownloadNotFoundError
	if errors.As(err, &dl) {
		msg := tokenDownloadNotFound + fmt.Sprintf("id=%s", dl.ID)
		return status.Error(codes.NotFound, msg), true
	}
	var prev *daemon.PreviewNotFoundError
	if errors.As(err, &prev) {
		msg := tokenPreviewNotFound + fmt.Sprintf("id=%s", prev.PreviewID)
		return status.Error(codes.NotFound, msg), true
	}
	var mutex *daemon.VFSMutexError
	if errors.As(err, &mutex) {
		msg := tokenVFSMutex + fmt.Sprintf("game=%s:conflicting=%s:group=%s",
			mutex.GameID, mutex.Conflicting, mutex.Group)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var linkedParent *daemon.ErrLinkedParentMissing
	if errors.As(err, &linkedParent) {
		msg := tokenLinkedParentMissing + fmt.Sprintf("game=%s:parent=%s",
			linkedParent.GameID, linkedParent.ParentGameID)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var drift *daemon.TTWDriftError
	if errors.As(err, &drift) {
		msg := tokenTTWDrift + fmt.Sprintf("reason=%s:install_path=%s",
			drift.Reason, drift.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var prefix *tools.ErrPrefixMissing
	if errors.As(err, &prefix) {
		msg := tokenPrefixMissing + fmt.Sprintf("game=%s:expected=%s",
			prefix.GameID, prefix.ExpectedPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var steamNotRunning *tools.ErrSteamNotRunning
	if errors.As(err, &steamNotRunning) {
		return status.Error(codes.FailedPrecondition, tokenSteamNotRunning), true
	}
	var ttwVanilla *daemon.ErrTTWRequiresVanillaFNV
	if errors.As(err, &ttwVanilla) {
		return status.Error(codes.FailedPrecondition, tokenTTWRequiresVanillaFNV), true
	}
	var xnvse *daemon.ErrXNVSEMissingForTTW
	if errors.As(err, &xnvse) {
		msg := tokenXNVSEMissingForTTW + fmt.Sprintf("install_path=%s", xnvse.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var fnv4gb *daemon.ErrFNV4GBNotAppliedForTTW
	if errors.As(err, &fnv4gb) {
		msg := tokenFNV4GBNotAppliedForTTW + fmt.Sprintf("install_path=%s", fnv4gb.InstallPath)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var gameMismatch *transfer.TransferGameMismatchError
	if errors.As(err, &gameMismatch) {
		msg := tokenTransferGameMismatch + fmt.Sprintf("want=%s:got=%s", gameMismatch.Want, gameMismatch.Got)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var schema *transfer.TransferSchemaError
	if errors.As(err, &schema) {
		msg := tokenTransferSchema + fmt.Sprintf("version=%d", schema.Version)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	var transferPath *transfer.TransferPathError
	if errors.As(err, &transferPath) {
		msg := tokenTransferPath + fmt.Sprintf("entry=%s", transferPath.Entry)
		return status.Error(codes.InvalidArgument, msg), true
	}
	var transferCollision *transfer.TransferCollisionError
	if errors.As(err, &transferCollision) {
		msg := tokenTransferCollision + fmt.Sprintf("name=%s", transferCollision.Name)
		return status.Error(codes.AlreadyExists, msg), true
	}
	var overwriteMounted *daemon.TransferOverwriteMountedError
	if errors.As(err, &overwriteMounted) {
		msg := tokenTransferOverwriteMounted + fmt.Sprintf("name=%s", overwriteMounted.Name)
		return status.Error(codes.FailedPrecondition, msg), true
	}
	if errors.Is(err, download.ErrEmptyInstallSelection) {
		return status.Error(codes.InvalidArgument, tokenInstallSelectionEmpty), true
	}
	var archiveRejected *download.ArchiveRejectedError
	if errors.As(err, &archiveRejected) {
		return status.Error(codes.InvalidArgument, tokenArchiveRejected+"reason="+escapeTokenValue(archiveRejected.Reason)), true
	}
	if errors.Is(err, download.ErrUnsafeArchive) {
		return status.Error(codes.InvalidArgument, tokenArchiveRejected+"reason="+download.ArchiveRejectedUnsafeEntry), true
	}
	var bundleRejected *transfer.BundleRejectedError
	if errors.As(err, &bundleRejected) {
		msg := tokenBundleRejected + fmt.Sprintf("reason=%s:item=%s", escapeTokenValue(bundleRejected.Reason), escapeTokenValue(bundleRejected.Item))
		return status.Error(codes.InvalidArgument, msg), true
	}
	var profileIdentity *profile.IdentityInvalidError
	if errors.As(err, &profileIdentity) {
		return status.Error(codes.InvalidArgument, tokenProfileIdentityInvalid+"name="+escapeTokenValue(profileIdentity.Name)), true
	}
	return nil, false
}

var tokenSeparatorEscaper = strings.NewReplacer(":", "%3A", "=", "%3D")

// escapeTokenValue percent-escapes a user-controlled token value, including the grammar's ':' and '=' separators.
func escapeTokenValue(value string) string {
	return tokenSeparatorEscaper.Replace(url.PathEscape(value))
}

// asNotAModError extracts a SMAPI not-a-mod refusal whether it travels by value or by pointer.
func asNotAModError(err error) (smapi.NotAModError, bool) {
	var byPointer *smapi.NotAModError
	if errors.As(err, &byPointer) && byPointer != nil {
		return *byPointer, true
	}
	var byValue smapi.NotAModError
	if errors.As(err, &byValue) {
		return byValue, true
	}
	return smapi.NotAModError{}, false
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
	if errors.Is(err, daemon.ErrVerificationConfirmationRequired) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	for _, entry := range sentinelCodes {
		if errors.Is(err, entry.sentinel) {
			return status.Error(entry.code, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}
