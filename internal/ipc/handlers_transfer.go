package ipc

import (
	"context"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

func (s *gorganizerServer) ExportInstance(req *pb.ExportInstanceRequest, stream pb.Gorganizer_ExportInstanceServer) error {
	emit := func(p dto.TransferProgress) {
		_ = stream.Send(&pb.TransferEvent{
			Event: &pb.TransferEvent_Progress{Progress: transferProgressToProto(p)},
		})
	}
	summary, err := s.ctrl.ExportInstance(stream.Context(), dto.ExportRequest{
		GameID:              req.GetGameId(),
		OutputPath:          req.GetOutputPath(),
		ModFolders:          req.GetModFolders(),
		ProfileNames:        req.GetProfileNames(),
		IncludeOverwrite:    req.GetIncludeOverwrite(),
		IncludeGameSettings: req.GetIncludeGameSettings(),
	}, emit)
	if err != nil {
		return grpcError(err)
	}
	return stream.Send(&pb.TransferEvent{
		Event: &pb.TransferEvent_Summary{Summary: transferSummaryToProto(summary)},
	})
}

func (s *gorganizerServer) PreviewImport(_ context.Context, req *pb.PreviewImportRequest) (*pb.PreviewImportResponse, error) {
	preview, err := s.ctrl.PreviewImport(req.GetGameId(), req.GetArchivePath())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.PreviewImportResponse{
		SchemaVersion:        preview.SchemaVersion,
		GorganizerVersion:    preview.GorganizerVersion,
		GameId:               preview.GameID,
		ExportedAt:           preview.ExportedAt,
		IncludesOverwrite:    preview.IncludesOverwrite,
		IncludesGameSettings: preview.IncludesGameSettings,
	}
	for _, m := range preview.Mods {
		out.Mods = append(out.Mods, &pb.TransferModEntry{
			Folder:      m.Folder,
			Name:        m.Name,
			FileCount:   m.FileCount,
			TotalBytes:  m.TotalBytes,
			NexusModId:  m.NexusModID,
			NexusFileId: m.NexusFileID,
			Collision:   m.Collision,
		})
	}
	for _, p := range preview.Profiles {
		out.Profiles = append(out.Profiles, &pb.TransferProfileEntry{
			Name:      p.Name,
			Collision: p.Collision,
		})
	}
	return out, nil
}

func (s *gorganizerServer) ImportInstance(req *pb.ImportInstanceRequest, stream pb.Gorganizer_ImportInstanceServer) error {
	emit := func(p dto.TransferProgress) {
		_ = stream.Send(&pb.TransferEvent{
			Event: &pb.TransferEvent_Progress{Progress: transferProgressToProto(p)},
		})
	}
	var overrides map[string]dto.CollisionPolicy
	if len(req.GetModPolicyOverrides()) > 0 {
		overrides = make(map[string]dto.CollisionPolicy, len(req.GetModPolicyOverrides()))
		for folder, policy := range req.GetModPolicyOverrides() {
			overrides[folder] = dto.CollisionPolicy(policy)
		}
	}
	summary, err := s.ctrl.ImportInstance(stream.Context(), dto.ImportRequest{
		GameID:             req.GetGameId(),
		ArchivePath:        req.GetArchivePath(),
		Policy:             dto.CollisionPolicy(req.GetPolicy()),
		ModPolicyOverrides: overrides,
		ModFolders:         req.GetModFolders(),
		ProfileNames:       req.GetProfileNames(),
	}, emit)
	if err != nil {
		return grpcError(err)
	}
	return stream.Send(&pb.TransferEvent{
		Event: &pb.TransferEvent_Summary{Summary: transferSummaryToProto(summary)},
	})
}

func transferProgressToProto(p dto.TransferProgress) *pb.TransferProgress {
	return &pb.TransferProgress{
		Step:        p.Step,
		CurrentItem: p.CurrentItem,
		ItemsDone:   p.ItemsDone,
		ItemsTotal:  p.ItemsTotal,
		BytesDone:   p.BytesDone,
		BytesTotal:  p.BytesTotal,
	}
}

func transferSummaryToProto(s dto.TransferSummary) *pb.TransferSummary {
	return &pb.TransferSummary{
		ModsExported:        s.ModsExported,
		ModsImported:        s.ModsImported,
		ProfilesTransferred: s.ProfilesTransferred,
		Skipped:             s.Skipped,
		Renamed:             s.Renamed,
		OutputPath:          s.OutputPath,
	}
}
