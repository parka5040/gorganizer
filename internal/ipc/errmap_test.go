package ipc

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/transfer"
	"github.com/parka/gorganizer/internal/vfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// assertStatus fails unless err is a gRPC status with the exact code and message.
func assertStatus(t *testing.T, err error, wantCode codes.Code, wantMsg string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a gRPC status error: %v", err)
	}
	if st.Code() != wantCode {
		t.Errorf("code = %v, want %v", st.Code(), wantCode)
	}
	if st.Message() != wantMsg {
		t.Errorf("message = %q, want %q", st.Message(), wantMsg)
	}
}

// TestMapErrorTypedErrors locks the exact code and message for every typed error MapError handles.
func TestMapErrorTypedErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{
			"loader_missing",
			&tools.LoaderMissingError{GameID: "skyrimse", ConfiguredExe: "skse64_loader.exe", InstallPath: "/games/SkyrimSE", Reason: "loader exe not found"},
			codes.FailedPrecondition,
			"loader_missing:reason=loader exe not found:exe=skse64_loader.exe:install_path=/games/SkyrimSE:game=skyrimse",
		},
		{
			"loader_missing_empty_exe",
			&tools.LoaderMissingError{GameID: "falloutnv", ConfiguredExe: "", InstallPath: "/games/FNV", Reason: "no candidates"},
			codes.FailedPrecondition,
			"loader_missing:reason=no candidates:exe=:install_path=/games/FNV:game=falloutnv",
		},
		{
			"archive_missing",
			&daemon.ArchiveMissingError{GameID: "skyrimse", Path: "mods/archive.7z"},
			codes.FailedPrecondition,
			"archive_missing:game=skyrimse:path=mods/archive.7z",
		},
		{
			"fomod_required",
			&daemon.FomodRequiredError{GameID: "skyrimse", Path: "SkyUI.7z", PreviewID: "pv-1"},
			codes.FailedPrecondition,
			"fomod_required:game=skyrimse:path=SkyUI.7z:preview_id=pv-1",
		},
		{
			"mod_collision",
			&daemon.ModCollisionError{Name: "SkyUI", ExistingMods: []string{"SkyUI", "SkyUI (1)"}},
			codes.AlreadyExists,
			"mod_collision:name=SkyUI:existing=SkyUI,SkyUI (1)",
		},
		{
			"mod_collision_no_existing",
			&daemon.ModCollisionError{Name: "SkyUI"},
			codes.AlreadyExists,
			"mod_collision:name=SkyUI:existing=",
		},
		{
			"mod_not_found",
			&daemon.ModNotFoundError{GameID: "skyrimse", Name: "SkyUI"},
			codes.NotFound,
			"mod_not_found:game=skyrimse:name=SkyUI",
		},
		{
			"mod_in_use",
			&daemon.ModInUseError{Name: "SkyUI", Profiles: []string{"Default", "Survival"}},
			codes.FailedPrecondition,
			"mod_in_use:name=SkyUI:profiles=Default,Survival",
		},
		{
			"nxm_expired",
			&download.NXMExpiredError{URI: "nxm://skyrimspecialedition/mods/12604/files/35407?key=abc&expires=1"},
			codes.FailedPrecondition,
			"nxm_expired:uri=nxm://skyrimspecialedition/mods/12604/files/35407?key=abc&expires=1",
		},
		{
			"download_not_found",
			&download.DownloadNotFoundError{ID: "dl-7"},
			codes.NotFound,
			"download_not_found:id=dl-7",
		},
		{
			"preview_not_found",
			&daemon.PreviewNotFoundError{PreviewID: "pv-9"},
			codes.NotFound,
			"preview_not_found:id=pv-9",
		},
		{
			"vfs_mutex",
			&daemon.VFSMutexError{GameID: "ttw", Conflicting: "falloutnv", Group: "fnv"},
			codes.FailedPrecondition,
			"vfs_mutex:game=ttw:conflicting=falloutnv:group=fnv",
		},
		{
			"linked_parent_missing",
			&daemon.ErrLinkedParentMissing{GameID: "ttw", ParentGameID: "falloutnv"},
			codes.FailedPrecondition,
			"linked_parent_missing:game=ttw:parent=falloutnv",
		},
		{
			"ttw_drift",
			&daemon.TTWDriftError{InstallPath: "/games/FNV", Reason: "exe changed"},
			codes.FailedPrecondition,
			"ttw_drift:reason=exe changed:install_path=/games/FNV",
		},
		{
			"prefix_missing",
			&tools.ErrPrefixMissing{GameID: "falloutnv", ExpectedPath: "/steam/compatdata/22380/pfx"},
			codes.FailedPrecondition,
			"prefix_missing:game=falloutnv:expected=/steam/compatdata/22380/pfx",
		},
		{
			"steam_not_running",
			&tools.ErrSteamNotRunning{},
			codes.FailedPrecondition,
			"steam_not_running:",
		},
		{
			"ttw_requires_vanilla_fnv",
			&daemon.ErrTTWRequiresVanillaFNV{},
			codes.FailedPrecondition,
			"ttw_requires_vanilla_fnv:",
		},
		{
			"xnvse_missing_for_ttw",
			&daemon.ErrXNVSEMissingForTTW{InstallPath: "/games/FNV"},
			codes.FailedPrecondition,
			"xnvse_missing_for_ttw:install_path=/games/FNV",
		},
		{
			"fnv4gb_not_applied_for_ttw",
			&daemon.ErrFNV4GBNotAppliedForTTW{InstallPath: "/games/FNV"},
			codes.FailedPrecondition,
			"fnv4gb_not_applied_for_ttw:install_path=/games/FNV",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, handled := MapError(tc.err)
			if !handled {
				t.Fatalf("MapError(%T) not handled", tc.err)
			}
			assertStatus(t, mapped, tc.wantCode, tc.wantMsg)
		})
	}
}

// TestMapErrorTransferErrors locks the exact code and message for the transfer typed errors.
func TestMapErrorTransferErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{
			"transfer_game_mismatch",
			&transfer.TransferGameMismatchError{Want: "skyrimse", Got: "falloutnv"},
			codes.FailedPrecondition,
			"transfer_game_mismatch:want=skyrimse:got=falloutnv",
		},
		{
			"transfer_schema",
			&transfer.TransferSchemaError{Version: 2},
			codes.FailedPrecondition,
			"transfer_schema:version=2",
		},
		{
			"transfer_path",
			&transfer.TransferPathError{Entry: "../evil"},
			codes.InvalidArgument,
			"transfer_path:entry=../evil",
		},
		{
			"transfer_collision",
			&transfer.TransferCollisionError{Name: "SkyUI"},
			codes.AlreadyExists,
			"transfer_collision:name=SkyUI",
		},
		{
			"transfer_overwrite_mounted",
			&daemon.TransferOverwriteMountedError{Name: "SkyUI"},
			codes.FailedPrecondition,
			"transfer_overwrite_mounted:name=SkyUI",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, handled := MapError(tc.err)
			if !handled {
				t.Fatalf("MapError(%T) not handled", tc.err)
			}
			assertStatus(t, mapped, tc.wantCode, tc.wantMsg)
		})
	}
}

// TestMapErrorUnwrapsWrappedErrors locks that typed errors map even when wrapped.
func TestMapErrorUnwrapsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("launch failed: %w", &daemon.ModNotFoundError{GameID: "skyrimse", Name: "SkyUI"})
	mapped, handled := MapError(wrapped)
	if !handled {
		t.Fatalf("MapError did not handle wrapped typed error")
	}
	assertStatus(t, mapped, codes.NotFound, "mod_not_found:game=skyrimse:name=SkyUI")
}

// TestMapErrorNil locks that nil maps to (nil, true).
func TestMapErrorNil(t *testing.T) {
	mapped, handled := MapError(nil)
	if !handled {
		t.Errorf("handled = false, want true")
	}
	if mapped != nil {
		t.Errorf("mapped = %v, want nil", mapped)
	}
}

// TestMapErrorUnknownPassesThrough locks that unrecognized errors return (nil, false).
func TestMapErrorUnknownPassesThrough(t *testing.T) {
	mapped, handled := MapError(errors.New("boom"))
	if handled {
		t.Errorf("handled = true, want false")
	}
	if mapped != nil {
		t.Errorf("mapped = %v, want nil", mapped)
	}
}

// TestGrpcErrorSentinels locks the code and message for every sentinel grpcError handles.
func TestGrpcErrorSentinels(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{"vfs_already_mounted", vfs.ErrAlreadyMounted, codes.AlreadyExists, "vfs: already mounted"},
		{"vfs_not_mounted", vfs.ErrNotMounted, codes.FailedPrecondition, "vfs: not mounted"},
		{"vfs_backup_exists", vfs.ErrBackupExists, codes.FailedPrecondition, "vfs: backup directory already exists (possible crash recovery needed)"},
		{"vfs_data_dir_missing", vfs.ErrDataDirMissing, codes.NotFound, "vfs: game Data directory does not exist"},
		{"config_invalid_game_id", config.ErrInvalidGameID, codes.InvalidArgument, "config: unknown game ID"},
		{"config_invalid_game_id_wrapped", fmt.Errorf("%w: %s", config.ErrInvalidGameID, "nogame"), codes.InvalidArgument, "config: unknown game ID: nogame"},
		{"config_no_api_key", config.ErrNoAPIKey, codes.FailedPrecondition, "config: nexus API key not set"},
		{"os_not_exist", os.ErrNotExist, codes.NotFound, "file does not exist"},
		{"os_not_exist_wrapped", fmt.Errorf("open failed: %w", os.ErrNotExist), codes.NotFound, "open failed: file does not exist"},
		{"default_internal", errors.New("boom"), codes.Internal, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStatus(t, grpcError(tc.err), tc.wantCode, tc.wantMsg)
		})
	}
}

// TestGrpcErrorRoutesTypedThroughMapError locks that grpcError delegates typed errors to MapError.
func TestGrpcErrorRoutesTypedThroughMapError(t *testing.T) {
	err := grpcError(&daemon.ModCollisionError{Name: "SkyUI", ExistingMods: []string{"SkyUI"}})
	assertStatus(t, err, codes.AlreadyExists, "mod_collision:name=SkyUI:existing=SkyUI")
}
