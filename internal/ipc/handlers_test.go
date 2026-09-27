package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/transfer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type fakeController struct {
	GameController
	ModController
	ProfileController
	VFSController
	ConflictController
	PluginStatusController
	ArchiveController
	InstallController
	LaunchController
	FNV4GBController
	SettingsController
	IniController
	LifecycleController
	TTWController
	ExecutableController
	TransferController
	ModLoaderController
	ModDependencyController

	games             []dto.GameInfo
	configureArgs     []any
	mod               *dto.ModInfoResult
	modErr            error
	getModArgs        []string
	reinstallCounts   [3]int
	reinstallArgs     []string
	modList           []dto.ModListEntryResult
	getModListArgs    []string
	setModListArgs    []any
	vfsStatus         *dto.VFSStatusResult
	mountCalled       string
	mountArgs         []string
	downloadID        string
	queuedAhead       int
	startDownloadURI  string
	archives          []dto.ArchiveRowResult
	listArchivesGame  string
	bulkAffected      int
	bulkArgs          []any
	installFolder     string
	installCount      int
	installReq        dto.StartInstallRequest
	pluginOrderArgs   []any
	pluginLoadoutArgs []any
	launchPid         int
	launchArgs        []any
	readiness         dto.ReadinessResult
	statusCh          chan dto.StatusEventResult
	exportReq         dto.ExportRequest
	importReq         dto.ImportRequest
	previewArgs       []string
	preview           dto.ImportPreview
	transferProgress  []dto.TransferProgress
	transferSummary   dto.TransferSummary
	transferErr       error
	pluginEvents      []dto.PluginStatusEventResult
	modLoaderStatus   dto.ModLoaderStatusResult
	modLoaderErr      error
	modLoaderArgs     []any
	depReport         dto.ModDependencyReportResult
	depFetch          []dto.DependencyFetchResult
	depAcked          int
	depErr            error
	depArgs           []any
	installEvents     []dto.InstallEventResult
}

// GetModDependencyReport records its arguments and returns the canned dependency report.
func (f *fakeController) GetModDependencyReport(_ context.Context, gameID, profileName string, refreshRemote, forceRemote bool) (dto.ModDependencyReportResult, error) {
	f.depArgs = []any{"report", gameID, profileName, refreshRemote, forceRemote}
	return f.depReport, f.depErr
}

// FetchModDependencies records its arguments and returns the canned fetch results.
func (f *fakeController) FetchModDependencies(_ context.Context, gameID, profileName string, uniqueIDs []string) ([]dto.DependencyFetchResult, error) {
	f.depArgs = []any{"fetch", gameID, profileName, uniqueIDs}
	return f.depFetch, f.depErr
}

// AckDependencyEnable records its arguments and returns the canned acknowledgement count.
func (f *fakeController) AckDependencyEnable(_ context.Context, gameID, batchID string, uniqueIDs []string) (int, error) {
	f.depArgs = []any{"ack", gameID, batchID, uniqueIDs}
	return f.depAcked, f.depErr
}

// StreamInstallEvents replays the canned install events and closes the stream.
func (f *fakeController) StreamInstallEvents(_ context.Context, _ string) (<-chan dto.InstallEventResult, error) {
	ch := make(chan dto.InstallEventResult, len(f.installEvents))
	for _, evt := range f.installEvents {
		ch <- evt
	}
	close(ch)
	return ch, nil
}

// GetModLoaderStatus records its arguments and returns the canned loader status.
func (f *fakeController) GetModLoaderStatus(_ context.Context, gameID string, checkLatest bool) (dto.ModLoaderStatusResult, error) {
	f.modLoaderArgs = []any{"status", gameID, checkLatest}
	return f.modLoaderStatus, f.modLoaderErr
}

// InstallModLoader records its arguments and returns the canned loader status.
func (f *fakeController) InstallModLoader(_ context.Context, gameID string, repairOnly bool) (dto.ModLoaderStatusResult, error) {
	f.modLoaderArgs = []any{"install", gameID, repairOnly}
	return f.modLoaderStatus, f.modLoaderErr
}

// UninstallModLoader records its arguments and returns the canned loader status.
func (f *fakeController) UninstallModLoader(_ context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	f.modLoaderArgs = []any{"uninstall", gameID}
	return f.modLoaderStatus, f.modLoaderErr
}

// RollbackModLoader records its arguments and returns the canned loader status.
func (f *fakeController) RollbackModLoader(_ context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	f.modLoaderArgs = []any{"rollback", gameID}
	return f.modLoaderStatus, f.modLoaderErr
}

func (f *fakeController) ListConfiguredGames() ([]dto.GameInfo, error) {
	return f.games, nil
}

func (f *fakeController) ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error {
	f.configureArgs = []any{gameID, name, steamAppID, installPath, dataSubpath}
	return nil
}

func (f *fakeController) GetMod(gameID, modName string) (*dto.ModInfoResult, error) {
	f.getModArgs = []string{gameID, modName}
	return f.mod, f.modErr
}

func (f *fakeController) ReinstallMod(gameID, modName string) (int, int, int, error) {
	f.reinstallArgs = []string{gameID, modName}
	return f.reinstallCounts[0], f.reinstallCounts[1], f.reinstallCounts[2], nil
}

func (f *fakeController) GetModList(gameID, profileName string) ([]dto.ModListEntryResult, error) {
	f.getModListArgs = []string{gameID, profileName}
	return f.modList, nil
}

func (f *fakeController) SetModList(gameID, profileName string, entries []dto.ModListEntryResult) error {
	f.setModListArgs = []any{gameID, profileName, entries}
	return nil
}

func (f *fakeController) MountVFS(gameID, profileName string) (*dto.VFSStatusResult, error) {
	f.mountCalled = "MountVFS"
	f.mountArgs = []string{gameID, profileName}
	return f.vfsStatus, nil
}

func (f *fakeController) MountVFSWithSwap(gameID, profileName string) (*dto.VFSStatusResult, error) {
	f.mountCalled = "MountVFSWithSwap"
	f.mountArgs = []string{gameID, profileName}
	return f.vfsStatus, nil
}

func (f *fakeController) StartDownload(nxmURI string) (string, int, error) {
	f.startDownloadURI = nxmURI
	return f.downloadID, f.queuedAhead, nil
}

func (f *fakeController) ListArchives(gameID string) ([]dto.ArchiveRowResult, error) {
	f.listArchivesGame = gameID
	return f.archives, nil
}

func (f *fakeController) SetArchivesHiddenBulk(gameID string, hidden bool, scope dto.BulkHideScope) (int, error) {
	f.bulkArgs = []any{gameID, hidden, scope}
	return f.bulkAffected, nil
}

func (f *fakeController) StreamPluginStatus(_ context.Context, _, _ string) (<-chan dto.PluginStatusEventResult, error) {
	out := make(chan dto.PluginStatusEventResult, len(f.pluginEvents))
	for _, evt := range f.pluginEvents {
		out <- evt
	}
	close(out)
	return out, nil
}

func (f *fakeController) StartInstall(req dto.StartInstallRequest) (string, int, error) {
	f.installReq = req
	return f.installFolder, f.installCount, nil
}

func (f *fakeController) SetPluginOrder(gameID, profileName string, filenames []string) error {
	f.pluginOrderArgs = []any{gameID, profileName, filenames}
	return nil
}

func (f *fakeController) SetPluginLoadout(gameID, profileName string, entries []dto.PluginLoadoutEntryResult) error {
	f.pluginLoadoutArgs = []any{gameID, profileName, entries}
	return nil
}

func (f *fakeController) LaunchGame(gameID string, useTool bool, profileName string) (int, error) {
	f.launchArgs = []any{gameID, useTool, profileName}
	return f.launchPid, nil
}

func (f *fakeController) Health() dto.ReadinessResult {
	return f.readiness
}

func (f *fakeController) WatchStatus() <-chan dto.StatusEventResult {
	return f.statusCh
}

func (f *fakeController) ExportInstance(_ context.Context, req dto.ExportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	f.exportReq = req
	for _, p := range f.transferProgress {
		emit(p)
	}
	return f.transferSummary, f.transferErr
}

func (f *fakeController) PreviewImport(_ context.Context, gameID, archivePath string) (dto.ImportPreview, error) {
	f.previewArgs = []string{gameID, archivePath}
	return f.preview, f.transferErr
}

func (f *fakeController) ImportInstance(_ context.Context, req dto.ImportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	f.importReq = req
	for _, p := range f.transferProgress {
		emit(p)
	}
	return f.transferSummary, f.transferErr
}

// newTestClient serves the current handlers over bufconn and returns a connected client.
func newTestClient(t *testing.T, ctrl DaemonController) pb.GorganizerClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterGorganizerServer(srv, &gorganizerServer{ctrl: ctrl})
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
	})
	return pb.NewGorganizerClient(conn)
}

// mustEqualProto fails the test unless got and want are proto-equal.
func mustEqualProto(t *testing.T, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("proto mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestListGamesFieldMapping locks dto.GameInfo → pb.Game field-by-field conversion.
func TestListGamesFieldMapping(t *testing.T) {
	fake := &fakeController{games: []dto.GameInfo{
		{
			GameID: "falloutnv", Name: "Fallout: New Vegas", SteamAppID: 22380,
			InstallPath: "/games/FNV", DataPath: "/games/FNV/Data",
			Synthetic: false, LinkedFromGameID: "", VFSActive: true,
		},
		{
			GameID: "ttw", Name: "Tale of Two Wastelands", SteamAppID: 0,
			InstallPath: "/games/FNV", DataPath: "/games/FNV/Data",
			Synthetic: true, LinkedFromGameID: "falloutnv", VFSActive: false,
		},
		{
			GameID: "stardewvalley", Name: "Stardew Valley", SteamAppID: 413150,
			InstallPath: "/games/SDV", DataPath: "/games/SDV/Mods",
			Capabilities: &dto.GameCapabilities{
				Plugins: false, Ini: false, Loot: false,
				ModLoader:            dto.ModLoaderKindSMAPI,
				InstallLayout:        dto.InstallLayoutSMAPIManifest,
				ManifestDependencies: true,
			},
		},
		{
			GameID: "skyrimse", Name: "Skyrim Special Edition", SteamAppID: 489830,
			InstallPath: "/games/SSE", DataPath: "/games/SSE/Data",
			Capabilities: &dto.GameCapabilities{
				Plugins: true, Ini: true, Loot: true,
				ModLoader:     dto.ModLoaderKindNone,
				InstallLayout: dto.InstallLayoutDataRoot,
			},
		},
	}}
	client := newTestClient(t, fake)
	resp, err := client.ListGames(t.Context(), &pb.ListGamesRequest{})
	if err != nil {
		t.Fatalf("ListGames: %v", err)
	}
	want := []*pb.Game{
		{
			GameId: "falloutnv", Name: "Fallout: New Vegas", SteamAppId: 22380,
			InstallPath: "/games/FNV", DataPath: "/games/FNV/Data",
			Synthetic: false, LinkedFromGameId: "", VfsActive: true,
		},
		{
			GameId: "ttw", Name: "Tale of Two Wastelands", SteamAppId: 0,
			InstallPath: "/games/FNV", DataPath: "/games/FNV/Data",
			Synthetic: true, LinkedFromGameId: "falloutnv", VfsActive: false,
		},
		{
			GameId: "stardewvalley", Name: "Stardew Valley", SteamAppId: 413150,
			InstallPath: "/games/SDV", DataPath: "/games/SDV/Mods",
			Capabilities: &pb.GameCapabilities{
				Plugins: false, Ini: false, Loot: false,
				ModLoader:            pb.ModLoaderKind_MOD_LOADER_KIND_SMAPI,
				InstallLayout:        pb.InstallLayout_INSTALL_LAYOUT_SMAPI_MANIFEST,
				ManifestDependencies: true,
			},
		},
		{
			GameId: "skyrimse", Name: "Skyrim Special Edition", SteamAppId: 489830,
			InstallPath: "/games/SSE", DataPath: "/games/SSE/Data",
			Capabilities: &pb.GameCapabilities{
				Plugins: true, Ini: true, Loot: true,
				ModLoader:     pb.ModLoaderKind_MOD_LOADER_KIND_NONE,
				InstallLayout: pb.InstallLayout_INSTALL_LAYOUT_DATA_ROOT,
			},
		},
	}
	if len(resp.GetGames()) != len(want) {
		t.Fatalf("got %d games, want %d", len(resp.GetGames()), len(want))
	}
	for i := range want {
		mustEqualProto(t, resp.GetGames()[i], want[i])
	}
	for i := range 2 {
		if resp.GetGames()[i].Capabilities != nil {
			t.Errorf("game %d: capabilities = %v, want unset", i, resp.GetGames()[i].Capabilities)
		}
	}
}

// TestConfigureGameArgMapping locks pb.ConfigureGameRequest → controller argument order.
func TestConfigureGameArgMapping(t *testing.T) {
	fake := &fakeController{}
	client := newTestClient(t, fake)
	_, err := client.ConfigureGame(t.Context(), &pb.ConfigureGameRequest{
		GameId: "skyrimse", Name: "Skyrim Special Edition", SteamAppId: 489830,
		InstallPath: "/games/SkyrimSE", DataSubpath: "Data",
	})
	if err != nil {
		t.Fatalf("ConfigureGame: %v", err)
	}
	want := []any{"skyrimse", "Skyrim Special Edition", uint32(489830), "/games/SkyrimSE", "Data"}
	if !reflect.DeepEqual(fake.configureArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.configureArgs, want)
	}
}

// TestGetModFieldMapping locks dto.ModInfoResult → pb.ModInfo including DataPath mirroring BasePath.
func TestGetModFieldMapping(t *testing.T) {
	fake := &fakeController{mod: &dto.ModInfoResult{
		Name: "SkyUI", GameID: "skyrimse", BasePath: "/mods/skyrimse/SkyUI",
		FileCount: 12, TotalSize: 3456, Files: []string{"SkyUI_SE.esp", "textures/t.dds"},
	}}
	client := newTestClient(t, fake)
	resp, err := client.GetMod(t.Context(), &pb.GetModRequest{GameId: "skyrimse", ModName: "SkyUI"})
	if err != nil {
		t.Fatalf("GetMod: %v", err)
	}
	mustEqualProto(t, resp, &pb.ModInfo{
		Name: "SkyUI", GameId: "skyrimse",
		BasePath: "/mods/skyrimse/SkyUI", DataPath: "/mods/skyrimse/SkyUI",
		FileCount: 12, TotalSize: 3456, Files: []string{"SkyUI_SE.esp", "textures/t.dds"},
	})
	if resp.GetDataPath() != resp.GetBasePath() {
		t.Errorf("DataPath %q != BasePath %q; current contract mirrors them", resp.GetDataPath(), resp.GetBasePath())
	}
	if !reflect.DeepEqual(fake.getModArgs, []string{"skyrimse", "SkyUI"}) {
		t.Errorf("controller args = %v", fake.getModArgs)
	}
}

// TestGetModErrorStatusOverWire locks that typed controller errors reach the client with exact code and message.
func TestGetModErrorStatusOverWire(t *testing.T) {
	fake := &fakeController{modErr: &daemon.ModNotFoundError{GameID: "skyrimse", Name: "Missing"}}
	client := newTestClient(t, fake)
	_, err := client.GetMod(t.Context(), &pb.GetModRequest{GameId: "skyrimse", ModName: "Missing"})
	if err == nil {
		t.Fatalf("GetMod: expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st.Code())
	}
	if st.Message() != "mod_not_found:game=skyrimse:name=Missing" {
		t.Errorf("message = %q", st.Message())
	}
}

// TestReinstallModCountMapping locks the three int results → int32 response fields.
func TestReinstallModCountMapping(t *testing.T) {
	fake := &fakeController{reinstallCounts: [3]int{3, 1, 42}}
	client := newTestClient(t, fake)
	resp, err := client.ReinstallMod(t.Context(), &pb.ReinstallModRequest{GameId: "skyrimse", ModName: "SkyUI"})
	if err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	mustEqualProto(t, resp, &pb.ReinstallModResponse{ArchivesReplayed: 3, ArchivesSkipped: 1, FileCount: 42})
	if !reflect.DeepEqual(fake.reinstallArgs, []string{"skyrimse", "SkyUI"}) {
		t.Errorf("controller args = %v", fake.reinstallArgs)
	}
}

// TestGetModListMapping locks dto.ModListEntryResult → pb.ModListEntry conversion.
func TestGetModListMapping(t *testing.T) {
	fake := &fakeController{modList: []dto.ModListEntryResult{
		{ModName: "SkyUI", Enabled: true, Priority: 0},
		{ModName: "USSEP", Enabled: false, Priority: 7},
	}}
	client := newTestClient(t, fake)
	resp, err := client.GetModList(t.Context(), &pb.GetModListRequest{GameId: "skyrimse", ProfileName: "Default"})
	if err != nil {
		t.Fatalf("GetModList: %v", err)
	}
	want := []*pb.ModListEntry{
		{ModName: "SkyUI", Enabled: true, Priority: 0},
		{ModName: "USSEP", Enabled: false, Priority: 7},
	}
	if len(resp.GetEntries()) != len(want) {
		t.Fatalf("got %d entries, want %d", len(resp.GetEntries()), len(want))
	}
	for i := range want {
		mustEqualProto(t, resp.GetEntries()[i], want[i])
	}
	if !reflect.DeepEqual(fake.getModListArgs, []string{"skyrimse", "Default"}) {
		t.Errorf("controller args = %v", fake.getModListArgs)
	}
}

// TestSetModListMapping locks pb.ModListEntry → dto.ModListEntryResult conversion.
func TestSetModListMapping(t *testing.T) {
	fake := &fakeController{}
	client := newTestClient(t, fake)
	_, err := client.SetModList(t.Context(), &pb.SetModListRequest{
		GameId: "skyrimse", ProfileName: "Default",
		Entries: []*pb.ModListEntry{
			{ModName: "SkyUI", Enabled: true, Priority: 0},
			{ModName: "USSEP", Enabled: false, Priority: 7},
		},
	})
	if err != nil {
		t.Fatalf("SetModList: %v", err)
	}
	want := []any{"skyrimse", "Default", []dto.ModListEntryResult{
		{ModName: "SkyUI", Enabled: true, Priority: 0},
		{ModName: "USSEP", Enabled: false, Priority: 7},
	}}
	if !reflect.DeepEqual(fake.setModListArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.setModListArgs, want)
	}
}

// TestMountVFSAutoSwapRouting locks AutoSwap routing plus dto.VFSStatusResult → pb.VFSStatus conversion.
func TestMountVFSAutoSwapRouting(t *testing.T) {
	cases := []struct {
		name       string
		autoSwap   bool
		wantCalled string
	}{
		{"direct", false, "MountVFS"},
		{"swap", true, "MountVFSWithSwap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeController{vfsStatus: &dto.VFSStatusResult{
				Mounted: true, GameID: "skyrimse", ProfileName: "Default",
				MountPoint: "/games/SkyrimSE/Data", EnabledModCount: 5, TotalFileCount: 100,
				Dirty: true, DesiredGen: 7, AppliedGen: 6,
			}}
			client := newTestClient(t, fake)
			resp, err := client.MountVFS(t.Context(), &pb.MountVFSRequest{
				GameId: "skyrimse", ProfileName: "Default", AutoSwap: tc.autoSwap,
			})
			if err != nil {
				t.Fatalf("MountVFS: %v", err)
			}
			if fake.mountCalled != tc.wantCalled {
				t.Errorf("called %s, want %s", fake.mountCalled, tc.wantCalled)
			}
			if !reflect.DeepEqual(fake.mountArgs, []string{"skyrimse", "Default"}) {
				t.Errorf("controller args = %v", fake.mountArgs)
			}
			mustEqualProto(t, resp.GetStatus(), &pb.VFSStatus{
				Mounted: true, GameId: "skyrimse", ProfileName: "Default",
				MountPoint: "/games/SkyrimSE/Data", EnabledModCount: 5, TotalFileCount: 100,
				Dirty: true, DesiredGen: 7, AppliedGen: 6,
			})
		})
	}
}

// TestStartDownloadMapping locks the StartDownload request and response mapping.
func TestStartDownloadMapping(t *testing.T) {
	fake := &fakeController{downloadID: "dl-1", queuedAhead: 3}
	client := newTestClient(t, fake)
	resp, err := client.StartDownload(t.Context(), &pb.StartDownloadRequest{
		NxmUri: "nxm://skyrimspecialedition/mods/12604/files/35407?key=abc",
	})
	if err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	if fake.startDownloadURI != "nxm://skyrimspecialedition/mods/12604/files/35407?key=abc" {
		t.Errorf("controller uri = %q", fake.startDownloadURI)
	}
	mustEqualProto(t, resp, &pb.StartDownloadResponse{DownloadId: "dl-1", QueuedAhead: 3})
}

// TestListArchivesFieldMapping locks dto.ArchiveRowResult → pb.ArchiveRow field-by-field conversion.
func TestListArchivesFieldMapping(t *testing.T) {
	fake := &fakeController{archives: []dto.ArchiveRowResult{{
		ArchiveRelPath: "SkyUI_5_2_SE-12604-5-2SE.7z", ModID: 12604, FileID: 35407,
		ModName: "SkyUI", FileName: "SkyUI 5.2 SE", FileArchiveName: "SkyUI_5_2_SE-12604-5-2SE.7z",
		Version: "5.2SE", Category: "MAIN", SizeBytes: 2048,
		UploadedAt: "2019-01-01T00:00:00Z", DownloadedAt: "2026-07-01T00:00:00Z",
		Hidden: true, GameDomain: "skyrimspecialedition",
		ThumbnailURL: "https://example.com/t.jpg", AdultContent: true,
		Status: dto.DownloadStatusInstalled, InstalledModFolder: "SkyUI",
		DownloadID: "dl-9", BytesDownloaded: 1024, QueuedAhead: 2, Merged: true,
	}}}
	client := newTestClient(t, fake)
	resp, err := client.ListArchives(t.Context(), &pb.ListArchivesRequest{GameId: "skyrimse"})
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	if fake.listArchivesGame != "skyrimse" {
		t.Errorf("controller game = %q", fake.listArchivesGame)
	}
	if len(resp.GetRows()) != 1 {
		t.Fatalf("got %d rows, want 1", len(resp.GetRows()))
	}
	mustEqualProto(t, resp.GetRows()[0], &pb.ArchiveRow{
		ArchiveRelPath: "SkyUI_5_2_SE-12604-5-2SE.7z", ModId: 12604, FileId: 35407,
		ModName: "SkyUI", FileName: "SkyUI 5.2 SE", FileArchiveName: "SkyUI_5_2_SE-12604-5-2SE.7z",
		Version: "5.2SE", Category: "MAIN", SizeBytes: 2048,
		UploadedAt: "2019-01-01T00:00:00Z", DownloadedAt: "2026-07-01T00:00:00Z",
		Hidden: true, GameDomain: "skyrimspecialedition",
		ThumbnailUrl: "https://example.com/t.jpg", AdultContent: true,
		Status: pb.DownloadStatus_DOWNLOAD_STATUS_INSTALLED, InstalledModFolder: "SkyUI",
		DownloadId: "dl-9", BytesDownloaded: 1024, QueuedAhead: 2, Merged: true,
	})
}

// TestSetArchivesHiddenBulkScopeMapping locks proto scope → dto.BulkHideScope conversion and affected count.
func TestSetArchivesHiddenBulkScopeMapping(t *testing.T) {
	cases := []struct {
		name      string
		scope     pb.SetArchivesHiddenBulkRequest_Scope
		wantScope dto.BulkHideScope
	}{
		{"all", pb.SetArchivesHiddenBulkRequest_ALL, dto.BulkHideAll},
		{"installed", pb.SetArchivesHiddenBulkRequest_INSTALLED, dto.BulkHideInstalled},
		{"uninstalled", pb.SetArchivesHiddenBulkRequest_UNINSTALLED, dto.BulkHideUninstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeController{bulkAffected: 7}
			client := newTestClient(t, fake)
			resp, err := client.SetArchivesHiddenBulk(t.Context(), &pb.SetArchivesHiddenBulkRequest{
				GameId: "skyrimse", Hidden: true, Scope: tc.scope,
			})
			if err != nil {
				t.Fatalf("SetArchivesHiddenBulk: %v", err)
			}
			want := []any{"skyrimse", true, tc.wantScope}
			if !reflect.DeepEqual(fake.bulkArgs, want) {
				t.Errorf("controller args = %v, want %v", fake.bulkArgs, want)
			}
			if resp.GetAffected() != 7 {
				t.Errorf("affected = %d, want 7", resp.GetAffected())
			}
		})
	}
}

// TestStartInstallRequestMapping locks pb.StartInstallRequest → dto.StartInstallRequest conversion and the response mapping.
func TestStartInstallRequestMapping(t *testing.T) {
	fake := &fakeController{installFolder: "SkyUI", installCount: 42}
	client := newTestClient(t, fake)
	resp, err := client.StartInstall(t.Context(), &pb.StartInstallRequest{
		GameId:              "skyrimse",
		ArchiveRelPath:      "SkyUI_5_2_SE-12604-5-2SE.7z",
		ExternalArchivePath: "/downloads/external.zip",
		Mode:                pb.InstallMode_INSTALL_MODE_MERGE_INTO,
		TargetMod:           "SkyUI",
		PreviewId:           "pv-1",
		FomodSelectedFiles: []*pb.FomodFile{
			{Source: "core", Destination: "", IsFolder: true, Priority: 0},
			{Source: "opt/extra.esp", Destination: "extra.esp", IsFolder: false, Priority: 2},
		},
	})
	if err != nil {
		t.Fatalf("StartInstall: %v", err)
	}
	want := dto.StartInstallRequest{
		GameID:              "skyrimse",
		ArchiveRelPath:      "SkyUI_5_2_SE-12604-5-2SE.7z",
		ExternalArchivePath: "/downloads/external.zip",
		Mode:                dto.InstallMergeIntoMod,
		TargetMod:           "SkyUI",
		PreviewID:           "pv-1",
		FomodSelectedFiles: []dto.FomodFileResult{
			{Source: "core", Destination: "", IsFolder: true, Priority: 0},
			{Source: "opt/extra.esp", Destination: "extra.esp", IsFolder: false, Priority: 2},
		},
	}
	if !reflect.DeepEqual(fake.installReq, want) {
		t.Errorf("controller req = %+v, want %+v", fake.installReq, want)
	}
	mustEqualProto(t, resp, &pb.StartInstallResponse{ModFolder: "SkyUI", FileCount: 42})
}

// TestSetPluginOrderArgMapping locks the SetPluginOrder request → controller argument mapping.
func TestSetPluginOrderArgMapping(t *testing.T) {
	fake := &fakeController{}
	client := newTestClient(t, fake)
	_, err := client.SetPluginOrder(t.Context(), &pb.SetPluginOrderRequest{
		GameId: "skyrimse", ProfileName: "Default",
		Filenames: []string{"Skyrim.esm", "SkyUI_SE.esp"},
	})
	if err != nil {
		t.Fatalf("SetPluginOrder: %v", err)
	}
	want := []any{"skyrimse", "Default", []string{"Skyrim.esm", "SkyUI_SE.esp"}}
	if !reflect.DeepEqual(fake.pluginOrderArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.pluginOrderArgs, want)
	}
}

func TestSetPluginLoadoutArgMapping(t *testing.T) {
	fake := &fakeController{}
	client := newTestClient(t, fake)
	_, err := client.SetPluginLoadout(t.Context(), &pb.SetPluginLoadoutRequest{
		GameId: "skyrimse", ProfileName: "Default",
		Plugins: []*pb.PluginLoadoutEntry{
			{Filename: "Skyrim.esm", Enabled: true},
			{Filename: "Optional.esp", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("SetPluginLoadout: %v", err)
	}
	want := []any{"skyrimse", "Default", []dto.PluginLoadoutEntryResult{
		{Filename: "Skyrim.esm", Enabled: true},
		{Filename: "Optional.esp", Enabled: false},
	}}
	if !reflect.DeepEqual(fake.pluginLoadoutArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.pluginLoadoutArgs, want)
	}
}

// TestLaunchGameMapping locks the LaunchGame request mapping and pid → int32 response.
func TestLaunchGameMapping(t *testing.T) {
	fake := &fakeController{launchPid: 4242}
	client := newTestClient(t, fake)
	resp, err := client.LaunchGame(t.Context(), &pb.LaunchGameRequest{
		GameId: "skyrimse", UseTool: true, ProfileName: "Default",
	})
	if err != nil {
		t.Fatalf("LaunchGame: %v", err)
	}
	want := []any{"skyrimse", true, "Default"}
	if !reflect.DeepEqual(fake.launchArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.launchArgs, want)
	}
	if resp.GetPid() != 4242 {
		t.Errorf("pid = %d, want 4242", resp.GetPid())
	}
}

// TestHealthMapping locks dto.ReadinessResult → pb.Readiness conversion.
func TestHealthMapping(t *testing.T) {
	fake := &fakeController{readiness: dto.ReadinessResult{
		SocketReady: true, RecoveryDone: true, GamesWarmed: false, LastInitStep: "warming games",
	}}
	client := newTestClient(t, fake)
	resp, err := client.Health(t.Context(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	mustEqualProto(t, resp, &pb.Readiness{
		SocketReady: true, RecoveryDone: true, GamesWarmed: false, LastInitStep: "warming games",
	})
}

// TestWatchStatusStream locks the dto.StatusEventResult → pb.StatusEvent oneof mapping and stream termination.
func TestWatchStatusStream(t *testing.T) {
	fake := &fakeController{statusCh: make(chan dto.StatusEventResult, 5)}
	fake.statusCh <- dto.StatusEventResult{VFSStatus: &dto.VFSStatusResult{
		Mounted: true, GameID: "skyrimse", ProfileName: "Default",
		MountPoint: "/games/SkyrimSE/Data", EnabledModCount: 2, TotalFileCount: 10,
		Dirty: false, DesiredGen: 3, AppliedGen: 3,
	}}
	fake.statusCh <- dto.StatusEventResult{RecoveryPending: &dto.RecoveryPendingResult{
		GameID: "skyrimse", DataPath: "/games/SkyrimSE/Data",
		BackupPath: "/games/SkyrimSE/Data.gorganizer-backup", Reason: "unclean shutdown",
	}}
	fake.statusCh <- dto.StatusEventResult{DependencyWarning: &dto.DependencyWarningResult{
		PluginFilename: "SkyUI_SE.esp", Detail: "missing master", Kind: dto.DepKindMasterAbsent,
	}}
	fake.statusCh <- dto.StatusEventResult{Error: "boom"}
	fake.statusCh <- dto.StatusEventResult{Info: "hello"}
	close(fake.statusCh)
	client := newTestClient(t, fake)
	stream, err := client.WatchStatus(t.Context(), &pb.WatchStatusRequest{})
	if err != nil {
		t.Fatalf("WatchStatus: %v", err)
	}
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 1: %v", err)
	}
	mustEqualProto(t, evt.GetVfsStatus(), &pb.VFSStatus{
		Mounted: true, GameId: "skyrimse", ProfileName: "Default",
		MountPoint: "/games/SkyrimSE/Data", EnabledModCount: 2, TotalFileCount: 10,
		Dirty: false, DesiredGen: 3, AppliedGen: 3,
	})
	evt, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv 2: %v", err)
	}
	mustEqualProto(t, evt.GetRecoveryPending(), &pb.RecoveryPending{
		GameId: "skyrimse", DataPath: "/games/SkyrimSE/Data",
		BackupPath: "/games/SkyrimSE/Data.gorganizer-backup", Reason: "unclean shutdown",
	})
	evt, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv 3: %v", err)
	}
	mustEqualProto(t, evt.GetDependencyWarning(), &pb.DependencyWarning{
		PluginFilename: "SkyUI_SE.esp", Detail: "missing master", Kind: pb.DepKind_DEP_MASTER_ABSENT,
	})
	evt, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv 4: %v", err)
	}
	if evt.GetError() != "boom" {
		t.Errorf("error event = %q, want %q", evt.GetError(), "boom")
	}
	evt, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv 5: %v", err)
	}
	if evt.GetInfo() != "hello" {
		t.Errorf("info event = %q, want %q", evt.GetInfo(), "hello")
	}
	if _, err = stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("final Recv err = %v, want io.EOF", err)
	}
}

// TestStreamPluginStatusEmptySnapshot locks that an empty non-nil snapshot reaches the client as one snapshot with zero plugins.
func TestStreamPluginStatusEmptySnapshot(t *testing.T) {
	fake := &fakeController{pluginEvents: []dto.PluginStatusEventResult{
		{Snapshot: make([]dto.PluginStatusItemResult, 0)},
	}}
	client := newTestClient(t, fake)
	stream, err := client.StreamPluginStatus(t.Context(), &pb.StreamPluginStatusRequest{
		GameId: "stardewvalley", ProfileName: "Default",
	})
	if err != nil {
		t.Fatalf("StreamPluginStatus: %v", err)
	}
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	snapshot := evt.GetSnapshot()
	if snapshot == nil {
		t.Fatalf("event = %v, want a snapshot", evt)
	}
	if len(snapshot.GetPlugins()) != 0 {
		t.Errorf("snapshot plugins = %v, want none", snapshot.GetPlugins())
	}
	if _, err = stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("final Recv err = %v, want io.EOF", err)
	}
}

// TestExportInstanceStream locks the export request mapping plus progress/summary event conversion over the wire.
func TestExportInstanceStream(t *testing.T) {
	fake := &fakeController{
		transferProgress: []dto.TransferProgress{
			{Step: "mods", CurrentItem: "SkyUI", ItemsDone: 1, ItemsTotal: 3, BytesDone: 512, BytesTotal: 2048},
		},
		transferSummary: dto.TransferSummary{
			ModsExported:        3,
			ProfilesTransferred: 1,
			OutputPath:          "/tmp/out.tar.zst",
		},
	}
	client := newTestClient(t, fake)
	stream, err := client.ExportInstance(t.Context(), &pb.ExportInstanceRequest{
		GameId:              "skyrimse",
		OutputPath:          "/tmp/out.tar.zst",
		ModFolders:          []string{"SkyUI", "USSEP"},
		ProfileNames:        []string{"Default"},
		IncludeOverwrite:    true,
		IncludeGameSettings: true,
	})
	if err != nil {
		t.Fatalf("ExportInstance: %v", err)
	}
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv progress: %v", err)
	}
	mustEqualProto(t, evt.GetProgress(), &pb.TransferProgress{
		Step: "mods", CurrentItem: "SkyUI", ItemsDone: 1, ItemsTotal: 3, BytesDone: 512, BytesTotal: 2048,
	})
	evt, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv summary: %v", err)
	}
	mustEqualProto(t, evt.GetSummary(), &pb.TransferSummary{
		ModsExported: 3, ProfilesTransferred: 1, OutputPath: "/tmp/out.tar.zst",
	})
	if _, err = stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("final Recv err = %v, want io.EOF", err)
	}
	want := dto.ExportRequest{
		GameID:              "skyrimse",
		OutputPath:          "/tmp/out.tar.zst",
		ModFolders:          []string{"SkyUI", "USSEP"},
		ProfileNames:        []string{"Default"},
		IncludeOverwrite:    true,
		IncludeGameSettings: true,
	}
	if !reflect.DeepEqual(fake.exportReq, want) {
		t.Errorf("controller req = %+v, want %+v", fake.exportReq, want)
	}
}

// TestPreviewImportMapping locks dto.ImportPreview → pb.PreviewImportResponse conversion.
func TestPreviewImportMapping(t *testing.T) {
	fake := &fakeController{preview: dto.ImportPreview{
		SchemaVersion:     1,
		GorganizerVersion: "1.0.0",
		GameID:            "skyrimse",
		ExportedAt:        "2026-07-11T10:30:00Z",
		Mods: []dto.ImportPreviewMod{
			{Folder: "SkyUI", Name: "SkyUI", FileCount: 12, TotalBytes: 3456, NexusModID: 12604, NexusFileID: 35407, Collision: true},
		},
		Profiles: []dto.ImportPreviewProfile{
			{Name: "Default", Collision: false},
		},
		IncludesOverwrite:    true,
		IncludesGameSettings: true,
	}}
	client := newTestClient(t, fake)
	resp, err := client.PreviewImport(t.Context(), &pb.PreviewImportRequest{
		GameId: "skyrimse", ArchivePath: "/tmp/in.tar.zst",
	})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if !reflect.DeepEqual(fake.previewArgs, []string{"skyrimse", "/tmp/in.tar.zst"}) {
		t.Errorf("controller args = %v", fake.previewArgs)
	}
	mustEqualProto(t, resp, &pb.PreviewImportResponse{
		SchemaVersion:     1,
		GorganizerVersion: "1.0.0",
		GameId:            "skyrimse",
		ExportedAt:        "2026-07-11T10:30:00Z",
		Mods: []*pb.TransferModEntry{
			{Folder: "SkyUI", Name: "SkyUI", FileCount: 12, TotalBytes: 3456, NexusModId: 12604, NexusFileId: 35407, Collision: true},
		},
		Profiles: []*pb.TransferProfileEntry{
			{Name: "Default", Collision: false},
		},
		IncludesOverwrite:    true,
		IncludesGameSettings: true,
	})
}

// TestImportInstanceStream locks the import request mapping (policy + override map) and the summary event.
func TestImportInstanceStream(t *testing.T) {
	fake := &fakeController{
		transferSummary: dto.TransferSummary{
			ModsImported:        2,
			ProfilesTransferred: 1,
			Skipped:             []string{"SkyUI"},
			Renamed:             map[string]string{"USSEP": "USSEP (2)"},
		},
	}
	client := newTestClient(t, fake)
	stream, err := client.ImportInstance(t.Context(), &pb.ImportInstanceRequest{
		GameId:      "skyrimse",
		ArchivePath: "/tmp/in.tar.zst",
		Policy:      pb.TransferCollisionPolicy_TRANSFER_POLICY_RENAME,
		ModPolicyOverrides: map[string]pb.TransferCollisionPolicy{
			"SkyUI": pb.TransferCollisionPolicy_TRANSFER_POLICY_SKIP,
		},
		ModFolders:   []string{"SkyUI", "USSEP"},
		ProfileNames: []string{"Default"},
	})
	if err != nil {
		t.Fatalf("ImportInstance: %v", err)
	}
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv summary: %v", err)
	}
	mustEqualProto(t, evt.GetSummary(), &pb.TransferSummary{
		ModsImported:        2,
		ProfilesTransferred: 1,
		Skipped:             []string{"SkyUI"},
		Renamed:             map[string]string{"USSEP": "USSEP (2)"},
	})
	if _, err = stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("final Recv err = %v, want io.EOF", err)
	}
	want := dto.ImportRequest{
		GameID:      "skyrimse",
		ArchivePath: "/tmp/in.tar.zst",
		Policy:      dto.PolicyRename,
		ModPolicyOverrides: map[string]dto.CollisionPolicy{
			"SkyUI": dto.PolicySkip,
		},
		ModFolders:   []string{"SkyUI", "USSEP"},
		ProfileNames: []string{"Default"},
	}
	if !reflect.DeepEqual(fake.importReq, want) {
		t.Errorf("controller req = %+v, want %+v", fake.importReq, want)
	}
}

// TestImportInstanceErrorStatusOverWire locks that transfer typed errors reach the stream client with exact code and message.
func TestImportInstanceErrorStatusOverWire(t *testing.T) {
	fake := &fakeController{transferErr: &transfer.TransferCollisionError{Name: "SkyUI"}}
	client := newTestClient(t, fake)
	stream, err := client.ImportInstance(t.Context(), &pb.ImportInstanceRequest{
		GameId: "skyrimse", ArchivePath: "/tmp/in.tar.zst",
	})
	if err != nil {
		t.Fatalf("ImportInstance: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatalf("Recv: expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	if st.Code() != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", st.Code())
	}
	if st.Message() != "transfer_collision:name=SkyUI" {
		t.Errorf("message = %q", st.Message())
	}
}

// TestModLoaderRPCMapping locks the request fields each mod-loader RPC forwards and the status field mapping.
func TestModLoaderRPCMapping(t *testing.T) {
	fake := &fakeController{modLoaderStatus: dto.ModLoaderStatusResult{
		GameID: "stardewvalley", Kind: dto.ModLoaderKindSMAPI, State: dto.ModLoaderStateLauncherReverted,
		Managed: true, InstalledVersion: "4.5.2", ActiveVersion: "4.5.2", PreviousVersion: "4.5.1",
		LatestVersion: "4.6.0", UpdateAvailable: true, Busy: true, Detail: "StardewValley no longer starts StardewModdingAPI",
	}}
	client := newTestClient(t, fake)
	want := &pb.ModLoaderStatus{
		GameId: "stardewvalley", Kind: pb.ModLoaderKind_MOD_LOADER_KIND_SMAPI,
		State: pb.ModLoaderState_MOD_LOADER_STATE_LAUNCHER_REVERTED, Managed: true,
		InstalledVersion: "4.5.2", ActiveVersion: "4.5.2", PreviousVersion: "4.5.1",
		LatestVersion: "4.6.0", UpdateAvailable: true, Busy: true,
		Detail: "StardewValley no longer starts StardewModdingAPI",
	}
	for _, tc := range []struct {
		name     string
		call     func() (*pb.ModLoaderStatus, error)
		wantArgs []any
	}{
		{"status", func() (*pb.ModLoaderStatus, error) {
			return client.GetModLoaderStatus(t.Context(), &pb.ModLoaderRequest{GameId: "stardewvalley", CheckLatest: true, RepairOnly: true})
		}, []any{"status", "stardewvalley", true}},
		{"install", func() (*pb.ModLoaderStatus, error) {
			return client.InstallModLoader(t.Context(), &pb.ModLoaderRequest{GameId: "stardewvalley", CheckLatest: true, RepairOnly: true})
		}, []any{"install", "stardewvalley", true}},
		{"uninstall", func() (*pb.ModLoaderStatus, error) {
			return client.UninstallModLoader(t.Context(), &pb.ModLoaderRequest{GameId: "stardewvalley"})
		}, []any{"uninstall", "stardewvalley"}},
		{"rollback", func() (*pb.ModLoaderStatus, error) {
			return client.RollbackModLoader(t.Context(), &pb.ModLoaderRequest{GameId: "stardewvalley"})
		}, []any{"rollback", "stardewvalley"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.call()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(fake.modLoaderArgs, tc.wantArgs) {
				t.Errorf("controller args = %v, want %v", fake.modLoaderArgs, tc.wantArgs)
			}
			mustEqualProto(t, resp, want)
		})
	}
}

// TestModLoaderRPCBusyError locks that a fence refusal reaches the client as a modloader_busy token.
func TestModLoaderRPCBusyError(t *testing.T) {
	fake := &fakeController{modLoaderErr: &dto.OperationBusyError{GameID: "stardewvalley", Operation: dto.BusyOperationMounted}}
	client := newTestClient(t, fake)
	_, err := client.InstallModLoader(t.Context(), &pb.ModLoaderRequest{GameId: "stardewvalley"})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition || st.Message() != "modloader_busy:game=stardewvalley:operation=mounted" {
		t.Fatalf("InstallModLoader error = %v, want FailedPrecondition modloader_busy", err)
	}
}

// TestModDependencyReportMapping locks the request fields GetModDependencyReport forwards and every report field mapping.
func TestModDependencyReportMapping(t *testing.T) {
	fake := &fakeController{depReport: dto.ModDependencyReportResult{
		GameID: "stardewvalley", ProfileName: "Default",
		Components: []dto.ModComponentResult{{
			Folder: "ContentPatcher", ProviderMod: "Content Patcher", UniqueID: "Pathoschild.ContentPatcher",
			Name: "Content Patcher", Version: "2.0.0", Kind: dto.ModComponentCode, Bundled: true, Failed: true,
			Issues: []dto.ModIssueResult{{
				Kind: dto.ModIssueVersionTooLow, TargetID: "SMAPI", RequiredVersion: "4.1.0", FoundVersion: "4.0.0",
				Providers: []string{"Base"}, Detail: "too old",
			}},
			UpdateVersion: "2.1.0", UpdateURL: "https://example.test/cp", NexusID: 1915, UpdateStale: true,
		}},
		Missing: []dto.MissingDependencyResult{{
			UniqueID: "spacechase0.SpaceCore", MinimumVersion: "1.2.0", RequiredBy: []string{"A"}, DisabledProviders: []string{"B"},
			Name: "SpaceCore", NexusID: 1348, URL: "https://www.nexusmods.com/stardewvalley/mods/1348", Resolvable: true, Stale: true,
		}},
		PendingEnables:   []dto.PendingEnableResult{{BatchID: "dep-1", ProfileName: "Default", ModName: "SpaceCore", UniqueID: "spacechase0.SpaceCore"}},
		RootManifestMods: []string{"Broken"},
		RemoteChecked:    true, RemoteError: "offline", LoaderVersion: "4.5.2", GameVersion: "1.6.15",
		RecentFailures: []dto.DependencyRequestIssueResult{{
			UniqueID: "Dep.Core", BatchID: "dep-2", State: "failed", Detail: "archive_mismatch: x",
			UpdatedAt: time.Date(2026, 9, 25, 12, 30, 0, 0, time.FixedZone("EDT", -4*3600)),
		}},
	}}
	client := newTestClient(t, fake)
	resp, err := client.GetModDependencyReport(t.Context(), &pb.ModDependencyReportRequest{GameId: "stardewvalley", ProfileName: "Default", RefreshRemote: true, ForceRemote: true})
	if err != nil {
		t.Fatalf("GetModDependencyReport: %v", err)
	}
	if want := []any{"report", "stardewvalley", "Default", true, true}; !reflect.DeepEqual(fake.depArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.depArgs, want)
	}
	mustEqualProto(t, resp, &pb.ModDependencyReport{
		GameId: "stardewvalley", ProfileName: "Default",
		Components: []*pb.ModComponent{{
			Folder: "ContentPatcher", ProviderMod: "Content Patcher", UniqueId: "Pathoschild.ContentPatcher",
			Name: "Content Patcher", Version: "2.0.0", Kind: pb.ModComponentKind_MOD_COMPONENT_KIND_CODE, Bundled: true, Failed: true,
			Issues: []*pb.ModIssue{{
				Kind: pb.ModIssueKind_MOD_ISSUE_KIND_VERSION_TOO_LOW, TargetId: "SMAPI", RequiredVersion: "4.1.0", FoundVersion: "4.0.0",
				Providers: []string{"Base"}, Detail: "too old",
			}},
			UpdateVersion: "2.1.0", UpdateUrl: "https://example.test/cp", NexusId: 1915, UpdateStale: true,
		}},
		Missing: []*pb.MissingDependency{{
			UniqueId: "spacechase0.SpaceCore", MinimumVersion: "1.2.0", RequiredBy: []string{"A"}, DisabledProviders: []string{"B"},
			Name: "SpaceCore", NexusId: 1348, Url: "https://www.nexusmods.com/stardewvalley/mods/1348", Resolvable: true, Stale: true,
		}},
		PendingEnables:   []*pb.PendingEnable{{BatchId: "dep-1", ProfileName: "Default", ModName: "SpaceCore", UniqueId: "spacechase0.SpaceCore"}},
		RootManifestMods: []string{"Broken"},
		RemoteChecked:    true, RemoteError: "offline", LoaderVersion: "4.5.2", GameVersion: "1.6.15",
		RecentFailures: []*pb.DependencyRequestIssue{{
			UniqueId: "Dep.Core", BatchId: "dep-2", State: "failed", Detail: "archive_mismatch: x", UpdatedAt: "2026-09-25T16:30:00Z",
		}},
	})
}

// TestFetchAndAckDependencyMapping locks the request and response mapping of FetchModDependencies and AckDependencyEnable.
func TestFetchAndAckDependencyMapping(t *testing.T) {
	fake := &fakeController{
		depFetch: []dto.DependencyFetchResult{
			{UniqueID: "a", Outcome: dto.FetchOutcomeQueued, DownloadID: "dl-1", BatchID: "dep-1"},
			{UniqueID: "b", Outcome: dto.FetchOutcomeOpenURL, URL: "https://www.nexusmods.com/stardewvalley/mods/2?tab=files", Reason: "not_premium", BatchID: "dep-1"},
			{UniqueID: "c", Outcome: dto.FetchOutcomeUnresolved, Reason: "no_nexus_page"},
			{UniqueID: "d", Outcome: dto.FetchOutcomeAlreadyPresent, Reason: "provided: D"},
		},
		depAcked: 2,
	}
	client := newTestClient(t, fake)
	resp, err := client.FetchModDependencies(t.Context(), &pb.FetchModDependenciesRequest{GameId: "stardewvalley", ProfileName: "Default", UniqueIds: []string{"a", "b", "c", "d"}})
	if err != nil {
		t.Fatalf("FetchModDependencies: %v", err)
	}
	if want := []any{"fetch", "stardewvalley", "Default", []string{"a", "b", "c", "d"}}; !reflect.DeepEqual(fake.depArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.depArgs, want)
	}
	mustEqualProto(t, resp, &pb.FetchModDependenciesResponse{Results: []*pb.DependencyFetchResult{
		{UniqueId: "a", Outcome: pb.FetchOutcome_FETCH_OUTCOME_QUEUED, DownloadId: "dl-1", BatchId: "dep-1"},
		{UniqueId: "b", Outcome: pb.FetchOutcome_FETCH_OUTCOME_OPEN_URL, Url: "https://www.nexusmods.com/stardewvalley/mods/2?tab=files", Reason: "not_premium", BatchId: "dep-1"},
		{UniqueId: "c", Outcome: pb.FetchOutcome_FETCH_OUTCOME_UNRESOLVED, Reason: "no_nexus_page"},
		{UniqueId: "d", Outcome: pb.FetchOutcome_FETCH_OUTCOME_ALREADY_PRESENT, Reason: "provided: D"},
	}})
	ack, err := client.AckDependencyEnable(t.Context(), &pb.AckDependencyEnableRequest{GameId: "stardewvalley", BatchId: "dep-1", UniqueIds: []string{"a"}})
	if err != nil {
		t.Fatalf("AckDependencyEnable: %v", err)
	}
	if want := []any{"ack", "stardewvalley", "dep-1", []string{"a"}}; !reflect.DeepEqual(fake.depArgs, want) {
		t.Errorf("controller args = %v, want %v", fake.depArgs, want)
	}
	if ack.GetAcknowledged() != 2 {
		t.Errorf("acknowledged = %d, want 2", ack.GetAcknowledged())
	}
}

// TestModDependencyUnsupportedErrorOverWire locks the token a non-SMAPI game receives.
func TestModDependencyUnsupportedErrorOverWire(t *testing.T) {
	fake := &fakeController{depErr: &dto.ModDependenciesUnsupportedError{GameID: "skyrimse"}}
	client := newTestClient(t, fake)
	_, err := client.GetModDependencyReport(t.Context(), &pb.ModDependencyReportRequest{GameId: "skyrimse", ProfileName: "Default"})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument || st.Message() != "mod_dependencies_unsupported:game=skyrimse" {
		t.Fatalf("GetModDependencyReport error = %v, want InvalidArgument mod_dependencies_unsupported", err)
	}
}

// TestStreamInstallEventsMapsProgressAndCompleted locks the install event oneof mapping and that empty events are skipped.
func TestStreamInstallEventsMapsProgressAndCompleted(t *testing.T) {
	fake := &fakeController{installEvents: []dto.InstallEventResult{
		{GameID: "stardewvalley", Progress: &dto.InstallProgressResult{InstallID: "inst-1", ModName: "A", Step: dto.InstallStepComplete, Pct: 100}},
		{GameID: "stardewvalley"},
		{GameID: "stardewvalley", Completed: &dto.InstallCompletedResult{GameID: "stardewvalley", ModName: "A", ArchiveRelPath: "1_A/a.zip", BatchID: "dep-1", BatchIDs: []string{"dep-1", "dep-2"}}},
	}}
	client := newTestClient(t, fake)
	stream, err := client.StreamInstallEvents(t.Context(), &pb.StreamInstallEventsRequest{GameId: "stardewvalley"})
	if err != nil {
		t.Fatalf("StreamInstallEvents: %v", err)
	}
	var got []*pb.InstallEvent
	for {
		evt, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, evt)
	}
	if len(got) != 2 {
		t.Fatalf("received %d events, want 2: %v", len(got), got)
	}
	if got[0].GetInstallProgress().GetInstallId() != "inst-1" || got[0].GetInstallProgress().GetStep() != pb.InstallProgress_STEP_COMPLETE {
		t.Errorf("first event = %v, want the progress event", got[0])
	}
	mustEqualProto(t, got[1], &pb.InstallEvent{Event: &pb.InstallEvent_InstallCompleted{InstallCompleted: &pb.InstallCompleted{
		GameId: "stardewvalley", ModName: "A", ArchiveRelPath: "1_A/a.zip", BatchId: "dep-1", BatchIds: []string{"dep-1", "dep-2"},
	}}})
}
