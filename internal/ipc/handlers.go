package ipc

import (
	"context"
	"errors"
	"os"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/vfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// gorganizerServer implements the generated GorganizerServer interface.
type gorganizerServer struct {
	pb.UnimplementedGorganizerServer
	ctrl DaemonController
}

// grpcError maps errors to gRPC status codes. Typed errors from errors.go
// go through MapError first; everything else falls back to the sentinel
// table below.
func grpcError(err error) error {
	if mapped, ok := MapError(err); ok {
		return mapped
	}
	switch {
	case errors.Is(err, vfs.ErrAlreadyMounted):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, vfs.ErrNotMounted):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, vfs.ErrBackupExists):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, vfs.ErrDataDirMissing):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, config.ErrInvalidGameID):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, config.ErrNoAPIKey):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, os.ErrNotExist):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// --- Game handlers ---

func (s *gorganizerServer) ListGames(_ context.Context, _ *pb.ListGamesRequest) (*pb.ListGamesResponse, error) {
	games, err := s.ctrl.ListConfiguredGames()
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ListGamesResponse{Games: gamesToProto(games)}, nil
}

func (s *gorganizerServer) DetectGames(_ context.Context, _ *pb.DetectGamesRequest) (*pb.DetectGamesResponse, error) {
	games, err := s.ctrl.DetectInstalledGames()
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.DetectGamesResponse{Games: gamesToProto(games)}, nil
}

func (s *gorganizerServer) ConfigureGame(_ context.Context, req *pb.ConfigureGameRequest) (*pb.ConfigureGameResponse, error) {
	err := s.ctrl.ConfigureGame(
		req.GetGameId(), req.GetName(), req.GetSteamAppId(),
		req.GetInstallPath(), req.GetDataSubpath(),
	)
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ConfigureGameResponse{}, nil
}

// --- Mod handlers ---

func (s *gorganizerServer) ListMods(_ context.Context, req *pb.ListModsRequest) (*pb.ListModsResponse, error) {
	mods, err := s.ctrl.ListMods(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ListModsResponse{Mods: modsToProto(mods)}, nil
}

func (s *gorganizerServer) GetMod(_ context.Context, req *pb.GetModRequest) (*pb.ModInfo, error) {
	info, err := s.ctrl.GetMod(req.GetGameId(), req.GetModName())
	if err != nil {
		return nil, grpcError(err)
	}
	return modToProto(info), nil
}

func (s *gorganizerServer) RescanMod(_ context.Context, req *pb.RescanModRequest) (*pb.ModInfo, error) {
	info, err := s.ctrl.RescanMod(req.GetGameId(), req.GetModName())
	if err != nil {
		return nil, grpcError(err)
	}
	return modToProto(info), nil
}

func (s *gorganizerServer) RenameMod(_ context.Context, req *pb.RenameModRequest) (*pb.RenameModResponse, error) {
	if err := s.ctrl.RenameMod(req.GetGameId(), req.GetOldName(), req.GetNewName()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.RenameModResponse{ModName: req.GetNewName()}, nil
}

func (s *gorganizerServer) UninstallMod(_ context.Context, req *pb.UninstallModRequest) (*pb.UninstallModResponse, error) {
	flagged, err := s.ctrl.UninstallMod(req.GetGameId(), req.GetModName(), req.GetForce())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.UninstallModResponse{ArchivesFlaggedUninstalled: flagged}, nil
}

func (s *gorganizerServer) ReinstallMod(_ context.Context, req *pb.ReinstallModRequest) (*pb.ReinstallModResponse, error) {
	replayed, skipped, fileCount, err := s.ctrl.ReinstallMod(req.GetGameId(), req.GetModName())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ReinstallModResponse{
		ArchivesReplayed: int32(replayed),
		ArchivesSkipped:  int32(skipped),
		FileCount:        int32(fileCount),
	}, nil
}

func (s *gorganizerServer) RegisterManualInstall(_ context.Context, req *pb.RegisterManualInstallRequest) (*pb.RegisterManualInstallResponse, error) {
	updated, err := s.ctrl.RegisterManualInstall(req.GetGameId(), req.GetModName(), req.GetArchiveRelPath())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.RegisterManualInstallResponse{ProfilesUpdated: int32(updated)}, nil
}

func (s *gorganizerServer) ListOverwriteFiles(_ context.Context, req *pb.ListOverwriteFilesRequest) (*pb.ListOverwriteFilesResponse, error) {
	entries, dir, err := s.ctrl.ListOverwriteFiles(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.ListOverwriteFilesResponse{OverwriteDir: dir}
	for _, e := range entries {
		out.Files = append(out.Files, &pb.OverwriteEntry{
			RelPath:    e.RelPath,
			SizeBytes:  e.SizeBytes,
			ModifiedAt: e.ModifiedAt,
			IsDir:      e.IsDir,
		})
	}
	return out, nil
}

func (s *gorganizerServer) ExtractOverwriteToMod(_ context.Context, req *pb.ExtractOverwriteToModRequest) (*pb.ExtractOverwriteToModResponse, error) {
	count, err := s.ctrl.ExtractOverwriteToMod(req.GetGameId(), req.GetModName(), req.GetFiles(), req.GetKeepInOverwrite())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ExtractOverwriteToModResponse{FileCount: int32(count)}, nil
}

// --- Profile handlers ---

func (s *gorganizerServer) ListProfiles(_ context.Context, req *pb.ListProfilesRequest) (*pb.ListProfilesResponse, error) {
	profiles, err := s.ctrl.ListProfiles(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ListProfilesResponse{Profiles: profilesToProto(profiles)}, nil
}

func (s *gorganizerServer) CreateProfile(_ context.Context, req *pb.CreateProfileRequest) (*pb.Profile, error) {
	p, err := s.ctrl.CreateProfile(req.GetGameId(), req.GetName())
	if err != nil {
		return nil, grpcError(err)
	}
	return profileToProto(p), nil
}

func (s *gorganizerServer) DeleteProfile(_ context.Context, req *pb.DeleteProfileRequest) (*pb.DeleteProfileResponse, error) {
	if err := s.ctrl.DeleteProfile(req.GetGameId(), req.GetName()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.DeleteProfileResponse{}, nil
}

func (s *gorganizerServer) GetModList(_ context.Context, req *pb.GetModListRequest) (*pb.ModListResponse, error) {
	entries, err := s.ctrl.GetModList(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.ModListResponse{Entries: modListToProto(entries)}, nil
}

func (s *gorganizerServer) SetModList(_ context.Context, req *pb.SetModListRequest) (*pb.SetModListResponse, error) {
	entries := modListFromProto(req.GetEntries())
	if err := s.ctrl.SetModList(req.GetGameId(), req.GetProfileName(), entries); err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetModListResponse{}, nil
}

func (s *gorganizerServer) ListSeparators(_ context.Context, req *pb.ListSeparatorsRequest) (*pb.ListSeparatorsResponse, error) {
	seps, err := s.ctrl.ListSeparators(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*pb.Separator, len(seps))
	for i, s := range seps {
		out[i] = &pb.Separator{Name: s.Name, VisualIndex: s.VisualIndex, Collapsed: s.Collapsed}
	}
	return &pb.ListSeparatorsResponse{Separators: out}, nil
}

func (s *gorganizerServer) SetSeparators(_ context.Context, req *pb.SetSeparatorsRequest) (*pb.SetSeparatorsResponse, error) {
	in := req.GetSeparators()
	seps := make([]SeparatorResult, len(in))
	for i, sp := range in {
		seps[i] = SeparatorResult{Name: sp.GetName(), VisualIndex: sp.GetVisualIndex(), Collapsed: sp.GetCollapsed()}
	}
	if err := s.ctrl.SetSeparators(req.GetGameId(), req.GetProfileName(), seps); err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetSeparatorsResponse{}, nil
}

// --- VFS handlers ---

func (s *gorganizerServer) MountVFS(_ context.Context, req *pb.MountVFSRequest) (*pb.MountVFSResponse, error) {
	st, err := s.ctrl.MountVFS(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.MountVFSResponse{Status: vfsStatusToProto(st)}, nil
}

func (s *gorganizerServer) UnmountVFS(_ context.Context, req *pb.UnmountVFSRequest) (*pb.UnmountVFSResponse, error) {
	if err := s.ctrl.UnmountVFS(req.GetGameId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.UnmountVFSResponse{}, nil
}

func (s *gorganizerServer) GetVFSStatus(_ context.Context, req *pb.GetVFSStatusRequest) (*pb.VFSStatus, error) {
	st, err := s.ctrl.GetVFSStatus(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return vfsStatusToProto(st), nil
}

func (s *gorganizerServer) RebuildVFS(_ context.Context, req *pb.RebuildVFSRequest) (*pb.RebuildVFSResponse, error) {
	if err := s.ctrl.RebuildVFS(req.GetGameId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.RebuildVFSResponse{}, nil
}

func (s *gorganizerServer) RestoreFromBackup(_ context.Context, req *pb.RestoreFromBackupRequest) (*pb.RestoreFromBackupResponse, error) {
	if err := s.ctrl.RestoreFromBackup(req.GetGameId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.RestoreFromBackupResponse{}, nil
}

// --- Conflict handler ---

func (s *gorganizerServer) GetConflicts(_ context.Context, req *pb.GetConflictsRequest) (*pb.ConflictsResponse, error) {
	conflicts, err := s.ctrl.GetConflicts(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	pbConflicts := make([]*pb.FileConflict, len(conflicts))
	for i, c := range conflicts {
		pbConflicts[i] = &pb.FileConflict{
			VirtualPath: c.VirtualPath,
			WinningMod:  c.WinningMod,
			LosingMods:  c.LosingMods,
		}
	}
	return &pb.ConflictsResponse{Conflicts: pbConflicts}, nil
}

// --- Archive handlers (the Downloads tab lifecycle) ---

func (s *gorganizerServer) StartDownload(_ context.Context, req *pb.StartDownloadRequest) (*pb.StartDownloadResponse, error) {
	id, ahead, err := s.ctrl.StartDownload(req.GetNxmUri())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.StartDownloadResponse{DownloadId: id, QueuedAhead: int32(ahead)}, nil
}

func (s *gorganizerServer) CancelDownload(_ context.Context, req *pb.CancelDownloadRequest) (*pb.CancelDownloadResponse, error) {
	if err := s.ctrl.CancelDownload(req.GetDownloadId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.CancelDownloadResponse{}, nil
}

func (s *gorganizerServer) RetryDownload(_ context.Context, req *pb.RetryDownloadRequest) (*pb.RetryDownloadResponse, error) {
	ahead, err := s.ctrl.RetryDownload(req.GetDownloadId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.RetryDownloadResponse{QueuedAhead: int32(ahead)}, nil
}

func (s *gorganizerServer) ListArchives(_ context.Context, req *pb.ListArchivesRequest) (*pb.ListArchivesResponse, error) {
	rows, err := s.ctrl.ListArchives(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*pb.ArchiveRow, len(rows))
	for i, r := range rows {
		out[i] = archiveRowToProto(r)
	}
	return &pb.ListArchivesResponse{Rows: out}, nil
}

func (s *gorganizerServer) RemoveArchive(_ context.Context, req *pb.RemoveArchiveRequest) (*pb.RemoveArchiveResponse, error) {
	if err := s.ctrl.RemoveArchive(req.GetGameId(), req.GetArchiveRelPath()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.RemoveArchiveResponse{}, nil
}

func (s *gorganizerServer) SetArchiveHidden(_ context.Context, req *pb.SetArchiveHiddenRequest) (*pb.SetArchiveHiddenResponse, error) {
	if err := s.ctrl.SetArchiveHidden(req.GetGameId(), req.GetArchiveRelPath(), req.GetHidden()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetArchiveHiddenResponse{}, nil
}

func (s *gorganizerServer) SetArchivesHiddenBulk(_ context.Context, req *pb.SetArchivesHiddenBulkRequest) (*pb.SetArchivesHiddenBulkResponse, error) {
	scope := BulkHideScope(req.GetScope())
	affected, err := s.ctrl.SetArchivesHiddenBulk(req.GetGameId(), req.GetHidden(), scope)
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetArchivesHiddenBulkResponse{Affected: int32(affected)}, nil
}

func (s *gorganizerServer) RefreshArchiveMetadata(_ context.Context, req *pb.RefreshArchiveMetadataRequest) (*pb.RefreshArchiveMetadataResponse, error) {
	row, err := s.ctrl.RefreshArchiveMetadata(req.GetGameId(), req.GetArchiveRelPath())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.RefreshArchiveMetadataResponse{Row: archiveRowToProto(*row)}, nil
}

func (s *gorganizerServer) StreamArchiveEvents(req *pb.StreamArchiveEventsRequest, stream pb.Gorganizer_StreamArchiveEventsServer) error {
	ch, err := s.ctrl.StreamArchiveEvents(stream.Context(), req.GetGameId())
	if err != nil {
		return grpcError(err)
	}
	for evt := range ch {
		out := &pb.ArchiveEvent{}
		switch {
		case evt.Progress != nil:
			out.Event = &pb.ArchiveEvent_DownloadProgress{
				DownloadProgress: downloadProgressToProto(evt.Progress),
			}
		case evt.RowChanged != nil:
			out.Event = &pb.ArchiveEvent_RowChanged{
				RowChanged: archiveRowToProto(*evt.RowChanged),
			}
		case evt.ArchiveRemoved != "":
			out.Event = &pb.ArchiveEvent_ArchiveRemoved{ArchiveRemoved: evt.ArchiveRemoved}
		default:
			continue
		}
		if err := stream.Send(out); err != nil {
			return err
		}
	}
	return nil
}

// --- Install handlers ---

func (s *gorganizerServer) PreviewInstall(_ context.Context, req *pb.PreviewInstallRequest) (*pb.PreviewInstallResponse, error) {
	res, err := s.ctrl.PreviewInstall(req.GetGameId(), req.GetArchiveRelPath())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.PreviewInstallResponse{
		PreviewId:    res.PreviewID,
		HasFomod:     res.HasFomod,
		FlatFileList: res.FlatFileList,
	}
	if res.Plan != nil {
		out.Plan = fomodPlanToProto(res.Plan)
	}
	return out, nil
}

func (s *gorganizerServer) StartInstall(_ context.Context, req *pb.StartInstallRequest) (*pb.StartInstallResponse, error) {
	files := make([]FomodFileResult, len(req.GetFomodSelectedFiles()))
	for i, f := range req.GetFomodSelectedFiles() {
		files[i] = FomodFileResult{
			Source: f.GetSource(), Destination: f.GetDestination(),
			IsFolder: f.GetIsFolder(), Priority: f.GetPriority(),
		}
	}
	folder, count, err := s.ctrl.StartInstall(StartInstallRequest{
		GameID:              req.GetGameId(),
		ArchiveRelPath:      req.GetArchiveRelPath(),
		ExternalArchivePath: req.GetExternalArchivePath(),
		Mode:                InstallMode(req.GetMode()),
		TargetMod:           req.GetTargetMod(),
		PreviewID:           req.GetPreviewId(),
		FomodSelectedFiles:  files,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.StartInstallResponse{ModFolder: folder, FileCount: int32(count)}, nil
}

func (s *gorganizerServer) DiscardPreview(_ context.Context, req *pb.DiscardPreviewRequest) (*pb.DiscardPreviewResponse, error) {
	if err := s.ctrl.DiscardPreview(req.GetPreviewId()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.DiscardPreviewResponse{}, nil
}

func (s *gorganizerServer) StreamInstallEvents(req *pb.StreamInstallEventsRequest, stream pb.Gorganizer_StreamInstallEventsServer) error {
	ch, err := s.ctrl.StreamInstallEvents(stream.Context(), req.GetGameId())
	if err != nil {
		return grpcError(err)
	}
	for evt := range ch {
		if evt.Progress == nil {
			continue
		}
		out := &pb.InstallEvent{
			Event: &pb.InstallEvent_InstallProgress{
				InstallProgress: installProgressToProto(evt.Progress),
			},
		}
		if err := stream.Send(out); err != nil {
			return err
		}
	}
	return nil
}

// --- Game settings ---

func (s *gorganizerServer) GetGameSettings(_ context.Context, req *pb.GetGameSettingsRequest) (*pb.GameSettings, error) {
	gs, err := s.ctrl.GetGameSettings(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return gameSettingsToProto(gs), nil
}

func (s *gorganizerServer) SetGameSettings(_ context.Context, req *pb.SetGameSettingsRequest) (*pb.GameSettings, error) {
	gs, err := s.ctrl.SetGameSettings(req.GetGameId(), req.GetAutoInstall())
	if err != nil {
		return nil, grpcError(err)
	}
	return gameSettingsToProto(gs), nil
}

// --- INI handlers ---

func (s *gorganizerServer) ListProfileIniFiles(_ context.Context, req *pb.ListProfileIniFilesRequest) (*pb.ListProfileIniFilesResponse, error) {
	res, err := s.ctrl.ListProfileIniFiles(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.ListProfileIniFilesResponse{
		MyGamesDir:   res.MyGamesDir,
		UseCustomIni: res.UseCustomIni,
	}
	for _, f := range res.Files {
		out.Files = append(out.Files, &pb.ProfileIniFile{
			Filename: f.Filename, Content: f.Content, DiskPath: f.DiskPath,
		})
	}
	return out, nil
}

func (s *gorganizerServer) SaveProfileIniFile(_ context.Context, req *pb.SaveProfileIniFileRequest) (*pb.SaveProfileIniFileResponse, error) {
	if err := s.ctrl.SaveProfileIniFile(req.GetGameId(), req.GetProfileName(), req.GetFilename(), req.GetContent()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.SaveProfileIniFileResponse{}, nil
}

func (s *gorganizerServer) SetProfileIniEnabled(_ context.Context, req *pb.SetProfileIniEnabledRequest) (*pb.ProfileIniStatus, error) {
	st, err := s.ctrl.SetProfileIniEnabled(req.GetGameId(), req.GetProfileName(), req.GetEnabled())
	if err != nil {
		return nil, grpcError(err)
	}
	return profileIniStatusToProto(st), nil
}

func (s *gorganizerServer) GetProfileIniStatus(_ context.Context, req *pb.GetProfileIniStatusRequest) (*pb.ProfileIniStatus, error) {
	st, err := s.ctrl.GetProfileIniStatus(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	return profileIniStatusToProto(st), nil
}

func profileIniStatusToProto(st *ProfileIniStatusResult) *pb.ProfileIniStatus {
	return &pb.ProfileIniStatus{
		GameId:          st.GameID,
		ProfileName:     st.ProfileName,
		UseCustomIni:    st.UseCustomIni,
		MyGamesDir:      st.MyGamesDir,
		GameSupportsIni: st.GameSupportsIni,
	}
}

func (s *gorganizerServer) ListIniTweaks(_ context.Context, req *pb.ListIniTweaksRequest) (*pb.ListIniTweaksResponse, error) {
	tweaks, err := s.ctrl.ListIniTweaks(req.GetGameId(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.ListIniTweaksResponse{}
	for _, t := range tweaks {
		out.Tweaks = append(out.Tweaks, iniTweakToProto(t))
	}
	return out, nil
}

func (s *gorganizerServer) SetIniTweak(_ context.Context, req *pb.SetIniTweakRequest) (*pb.IniTweakState, error) {
	st, err := s.ctrl.SetIniTweak(req.GetGameId(), req.GetProfileName(), req.GetTweakId(), req.GetEnabled())
	if err != nil {
		return nil, grpcError(err)
	}
	return iniTweakToProto(*st), nil
}

func iniTweakToProto(t IniTweakStateResult) *pb.IniTweakState {
	return &pb.IniTweakState{
		Id: t.ID, Name: t.Name, Description: t.Description,
		TargetFile: t.TargetFile, Enabled: t.Enabled,
	}
}

// --- Launch handlers ---

func (s *gorganizerServer) LaunchGame(_ context.Context, req *pb.LaunchGameRequest) (*pb.LaunchGameResponse, error) {
	pid, err := s.ctrl.LaunchGame(req.GetGameId(), req.GetUseTool(), req.GetProfileName())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.LaunchGameResponse{Pid: int32(pid)}, nil
}

func (s *gorganizerServer) DetectProton(_ context.Context, _ *pb.DetectProtonRequest) (*pb.DetectProtonResponse, error) {
	versions, err := s.ctrl.DetectProton()
	if err != nil {
		return nil, grpcError(err)
	}
	pbVersions := make([]*pb.ProtonVersion, len(versions))
	for i, v := range versions {
		pbVersions[i] = &pb.ProtonVersion{Name: v.Name, Path: v.Path}
	}
	return &pb.DetectProtonResponse{Versions: pbVersions}, nil
}

func (s *gorganizerServer) InstallScriptExtender(_ context.Context, req *pb.InstallScriptExtenderRequest) (*pb.InstallScriptExtenderResponse, error) {
	name, err := s.ctrl.InstallScriptExtender(req.GetGameId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.InstallScriptExtenderResponse{Name: name}, nil
}

func (s *gorganizerServer) GetPreferredProton(_ context.Context, _ *pb.GetPreferredProtonRequest) (*pb.GetPreferredProtonResponse, error) {
	path, err := s.ctrl.GetPreferredProton()
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.GetPreferredProtonResponse{Path: path}, nil
}

func (s *gorganizerServer) SetPreferredProton(_ context.Context, req *pb.SetPreferredProtonRequest) (*pb.SetPreferredProtonResponse, error) {
	if err := s.ctrl.SetPreferredProton(req.GetPath()); err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetPreferredProtonResponse{}, nil
}

// --- Settings handlers ---

func (s *gorganizerServer) SetNexusAPIKey(ctx context.Context, req *pb.SetNexusAPIKeyRequest) (*pb.SetNexusAPIKeyResponse, error) {
	result, err := s.ctrl.SetNexusAPIKey(ctx, req.GetApiKey())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.SetNexusAPIKeyResponse{
		Valid:        result.Valid,
		ErrorMessage: result.ErrorMessage,
	}, nil
}

// --- Lifecycle handlers ---

func (s *gorganizerServer) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.ctrl.Shutdown()
	return &pb.ShutdownResponse{}, nil
}

func (s *gorganizerServer) Health(_ context.Context, _ *pb.HealthRequest) (*pb.Readiness, error) {
	r := s.ctrl.Health()
	return &pb.Readiness{
		SocketReady:  r.SocketReady,
		RecoveryDone: r.RecoveryDone,
		GamesWarmed:  r.GamesWarmed,
		LastInitStep: r.LastInitStep,
	}, nil
}

func (s *gorganizerServer) WatchStatus(_ *pb.WatchStatusRequest, stream pb.Gorganizer_WatchStatusServer) error {
	ch := s.ctrl.WatchStatus()
	for evt := range ch {
		pbEvt := &pb.StatusEvent{}
		switch {
		case evt.VFSStatus != nil:
			pbEvt.Event = &pb.StatusEvent_VfsStatus{VfsStatus: vfsStatusToProto(evt.VFSStatus)}
		case evt.RecoveryPending != nil:
			pbEvt.Event = &pb.StatusEvent_RecoveryPending{
				RecoveryPending: &pb.RecoveryPending{
					GameId:     evt.RecoveryPending.GameID,
					DataPath:   evt.RecoveryPending.DataPath,
					BackupPath: evt.RecoveryPending.BackupPath,
					Reason:     evt.RecoveryPending.Reason,
				},
			}
		case evt.Error != "":
			pbEvt.Event = &pb.StatusEvent_Error{Error: evt.Error}
		case evt.Info != "":
			pbEvt.Event = &pb.StatusEvent_Info{Info: evt.Info}
		default:
			continue
		}
		if err := stream.Send(pbEvt); err != nil {
			return err
		}
	}
	return nil
}

// --- Conversion helpers ---

func gamesToProto(games []GameInfo) []*pb.Game {
	result := make([]*pb.Game, len(games))
	for i, g := range games {
		result[i] = &pb.Game{
			GameId: g.GameID, Name: g.Name, SteamAppId: g.SteamAppID,
			InstallPath: g.InstallPath, DataPath: g.DataPath,
		}
	}
	return result
}

func modToProto(m *ModInfoResult) *pb.ModInfo {
	return &pb.ModInfo{
		Name: m.Name, GameId: m.GameID,
		BasePath:  m.BasePath,
		DataPath:  m.BasePath, // mod folder IS the data content
		FileCount: int32(m.FileCount), TotalSize: m.TotalSize,
		Files: m.Files,
	}
}

func modsToProto(mods []ModInfoResult) []*pb.ModInfo {
	result := make([]*pb.ModInfo, len(mods))
	for i := range mods {
		result[i] = modToProto(&mods[i])
	}
	return result
}

func profileToProto(p *ProfileResult) *pb.Profile {
	return &pb.Profile{Name: p.Name, GameId: p.GameID, CreatedAt: p.CreatedAt}
}

func profilesToProto(profiles []ProfileResult) []*pb.Profile {
	result := make([]*pb.Profile, len(profiles))
	for i := range profiles {
		result[i] = profileToProto(&profiles[i])
	}
	return result
}

func modListToProto(entries []ModListEntryResult) []*pb.ModListEntry {
	result := make([]*pb.ModListEntry, len(entries))
	for i, e := range entries {
		result[i] = &pb.ModListEntry{
			ModName: e.ModName, Enabled: e.Enabled, Priority: int32(e.Priority),
		}
	}
	return result
}

func modListFromProto(entries []*pb.ModListEntry) []ModListEntryResult {
	result := make([]ModListEntryResult, len(entries))
	for i, e := range entries {
		result[i] = ModListEntryResult{
			ModName: e.GetModName(), Enabled: e.GetEnabled(), Priority: int(e.GetPriority()),
		}
	}
	return result
}

func vfsStatusToProto(st *VFSStatusResult) *pb.VFSStatus {
	return &pb.VFSStatus{
		Mounted:         st.Mounted,
		GameId:          st.GameID,
		ProfileName:     st.ProfileName,
		MountPoint:      st.MountPoint,
		EnabledModCount: int32(st.EnabledModCount),
		TotalFileCount:  int32(st.TotalFileCount),
	}
}

func downloadProgressToProto(p *DownloadProgressResult) *pb.DownloadProgress {
	return &pb.DownloadProgress{
		DownloadId:      p.DownloadID,
		ModName:         p.ModName,
		BytesDownloaded: p.BytesDownloaded,
		BytesTotal:      p.BytesTotal,
		Status:          pb.DownloadStatus(p.Status),
		Error:           p.Error,
		QueuedAhead:     p.QueuedAhead,
	}
}

func installProgressToProto(p *InstallProgressResult) *pb.InstallProgress {
	return &pb.InstallProgress{
		InstallId:      p.InstallID,
		ArchiveRelPath: p.ArchiveRelPath,
		ModName:        p.ModName,
		Step:           pb.InstallProgress_Step(p.Step),
		Pct:            p.Pct,
		CurrentFile:    p.CurrentFile,
		FilesDone:      p.FilesDone,
		FilesTotal:     p.FilesTotal,
		Error:          p.Error,
	}
}

func archiveRowToProto(r ArchiveRowResult) *pb.ArchiveRow {
	return &pb.ArchiveRow{
		ArchiveRelPath:     r.ArchiveRelPath,
		ModId:              int32(r.ModID),
		FileId:             int32(r.FileID),
		ModName:            r.ModName,
		FileName:           r.FileName,
		FileArchiveName:    r.FileArchiveName,
		Version:            r.Version,
		Category:           r.Category,
		SizeBytes:          r.SizeBytes,
		UploadedAt:         r.UploadedAt,
		DownloadedAt:       r.DownloadedAt,
		Hidden:             r.Hidden,
		GameDomain:         r.GameDomain,
		ThumbnailUrl:       r.ThumbnailURL,
		AdultContent:       r.AdultContent,
		Status:             pb.DownloadStatus(r.Status),
		InstalledModFolder: r.InstalledModFolder,
		DownloadId:         r.DownloadID,
		BytesDownloaded:    r.BytesDownloaded,
		QueuedAhead:        r.QueuedAhead,
		Merged:             r.Merged,
	}
}

func gameSettingsToProto(gs *GameSettingsResult) *pb.GameSettings {
	return &pb.GameSettings{
		GameId:      gs.GameID,
		AutoInstall: gs.AutoInstall,
	}
}

func fomodPlanToProto(p *FomodPlanResult) *pb.FomodPlan {
	out := &pb.FomodPlan{
		ModuleName:     p.ModuleName,
		ModulePath:     p.ModulePath,
		LegacyInfoOnly: p.LegacyInfoOnly,
		Description:    p.Description,
		ScreenshotPath: p.ScreenshotPath,
		Version:        p.Version,
		Author:         p.Author,
	}
	for _, f := range p.RequiredFiles {
		out.RequiredFiles = append(out.RequiredFiles, &pb.FomodFile{
			Source: f.Source, Destination: f.Destination,
			IsFolder: f.IsFolder, Priority: f.Priority,
		})
	}
	for _, step := range p.Steps {
		ps := &pb.FomodStep{Name: step.Name}
		for _, g := range step.Groups {
			pg := &pb.FomodGroup{Name: g.Name, Type: pb.FomodGroupType(g.Type)}
			for _, pl := range g.Plugins {
				ppl := &pb.FomodPlugin{
					Name: pl.Name, Description: pl.Description,
					ImagePath:    pl.ImagePath,
					DefaultState: pb.FomodPluginState(pl.DefaultState),
				}
				for _, f := range pl.Files {
					ppl.Files = append(ppl.Files, &pb.FomodFile{
						Source: f.Source, Destination: f.Destination,
						IsFolder: f.IsFolder, Priority: f.Priority,
					})
				}
				pg.Plugins = append(pg.Plugins, ppl)
			}
			ps.Groups = append(ps.Groups, pg)
		}
		out.Steps = append(out.Steps, ps)
	}
	return out
}
