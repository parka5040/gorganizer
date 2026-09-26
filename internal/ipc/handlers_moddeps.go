package ipc

import (
	"context"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
)

// GetModDependencyReport analyzes a profile's SMAPI mods and missing dependencies.
func (s *gorganizerServer) GetModDependencyReport(ctx context.Context, req *pb.ModDependencyReportRequest) (*pb.ModDependencyReport, error) {
	report, err := s.ctrl.GetModDependencyReport(ctx, req.GetGameId(), req.GetProfileName(), req.GetRefreshRemote(), req.GetForceRemote())
	if err != nil {
		return nil, grpcError(err)
	}
	return modDependencyReportToProto(report), nil
}

// FetchModDependencies registers durable dependency requests and queues or links their downloads.
func (s *gorganizerServer) FetchModDependencies(ctx context.Context, req *pb.FetchModDependenciesRequest) (*pb.FetchModDependenciesResponse, error) {
	results, err := s.ctrl.FetchModDependencies(ctx, req.GetGameId(), req.GetProfileName(), req.GetUniqueIds())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &pb.FetchModDependenciesResponse{Results: make([]*pb.DependencyFetchResult, 0, len(results))}
	for _, result := range results {
		out.Results = append(out.Results, &pb.DependencyFetchResult{
			UniqueId:   result.UniqueID,
			Outcome:    fetchOutcomeToProto(result.Outcome),
			DownloadId: result.DownloadID,
			Url:        result.URL,
			Reason:     result.Reason,
			BatchId:    result.BatchID,
		})
	}
	return out, nil
}

// AckDependencyEnable marks a batch's pending dependency enables done after the GUI enabled them.
func (s *gorganizerServer) AckDependencyEnable(ctx context.Context, req *pb.AckDependencyEnableRequest) (*pb.AckDependencyEnableResponse, error) {
	acknowledged, err := s.ctrl.AckDependencyEnable(ctx, req.GetGameId(), req.GetBatchId(), req.GetUniqueIds())
	if err != nil {
		return nil, grpcError(err)
	}
	return &pb.AckDependencyEnableResponse{Acknowledged: int32(acknowledged)}, nil
}

// modDependencyReportToProto maps a daemon dependency report to the wire message.
func modDependencyReportToProto(report dto.ModDependencyReportResult) *pb.ModDependencyReport {
	out := &pb.ModDependencyReport{
		GameId:           report.GameID,
		ProfileName:      report.ProfileName,
		RootManifestMods: append([]string(nil), report.RootManifestMods...),
		RemoteChecked:    report.RemoteChecked,
		RemoteError:      report.RemoteError,
		LoaderVersion:    report.LoaderVersion,
		GameVersion:      report.GameVersion,
	}
	for _, component := range report.Components {
		out.Components = append(out.Components, modComponentToProto(component))
	}
	for _, missing := range report.Missing {
		out.Missing = append(out.Missing, &pb.MissingDependency{
			UniqueId:          missing.UniqueID,
			MinimumVersion:    missing.MinimumVersion,
			RequiredBy:        append([]string(nil), missing.RequiredBy...),
			DisabledProviders: append([]string(nil), missing.DisabledProviders...),
			Name:              missing.Name,
			NexusId:           int32(missing.NexusID),
			Url:               missing.URL,
			Resolvable:        missing.Resolvable,
			Stale:             missing.Stale,
		})
	}
	for _, pending := range report.PendingEnables {
		out.PendingEnables = append(out.PendingEnables, &pb.PendingEnable{
			BatchId:     pending.BatchID,
			ProfileName: pending.ProfileName,
			ModName:     pending.ModName,
			UniqueId:    pending.UniqueID,
		})
	}
	for _, failure := range report.RecentFailures {
		out.RecentFailures = append(out.RecentFailures, &pb.DependencyRequestIssue{
			UniqueId:  failure.UniqueID,
			BatchId:   failure.BatchID,
			State:     failure.State,
			Detail:    failure.Detail,
			UpdatedAt: failure.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// modComponentToProto maps one analyzed mod folder to the wire message.
func modComponentToProto(component dto.ModComponentResult) *pb.ModComponent {
	out := &pb.ModComponent{
		Folder:        component.Folder,
		ProviderMod:   component.ProviderMod,
		UniqueId:      component.UniqueID,
		Name:          component.Name,
		Version:       component.Version,
		Kind:          modComponentKindToProto(component.Kind),
		Bundled:       component.Bundled,
		Failed:        component.Failed,
		UpdateVersion: component.UpdateVersion,
		UpdateUrl:     component.UpdateURL,
		NexusId:       int32(component.NexusID),
		UpdateStale:   component.UpdateStale,
	}
	for _, issue := range component.Issues {
		out.Issues = append(out.Issues, &pb.ModIssue{
			Kind:            modIssueKindToProto(issue.Kind),
			TargetId:        issue.TargetID,
			RequiredVersion: issue.RequiredVersion,
			FoundVersion:    issue.FoundVersion,
			Providers:       append([]string(nil), issue.Providers...),
			Detail:          issue.Detail,
		})
	}
	return out
}

// modIssueKindToProto maps a DTO issue kind to its proto enum value, unknown values to UNSPECIFIED.
func modIssueKindToProto(kind dto.ModIssueKind) pb.ModIssueKind {
	switch kind {
	case dto.ModIssueMissing:
		return pb.ModIssueKind_MOD_ISSUE_KIND_MISSING
	case dto.ModIssueDisabled:
		return pb.ModIssueKind_MOD_ISSUE_KIND_DISABLED
	case dto.ModIssueVersionTooLow:
		return pb.ModIssueKind_MOD_ISSUE_KIND_VERSION_TOO_LOW
	case dto.ModIssueDuplicateID:
		return pb.ModIssueKind_MOD_ISSUE_KIND_DUPLICATE_ID
	case dto.ModIssueInvalidManifest:
		return pb.ModIssueKind_MOD_ISSUE_KIND_INVALID_MANIFEST
	case dto.ModIssueNeedsNewerLoader:
		return pb.ModIssueKind_MOD_ISSUE_KIND_NEEDS_NEWER_LOADER
	case dto.ModIssueNeedsNewerGame:
		return pb.ModIssueKind_MOD_ISSUE_KIND_NEEDS_NEWER_GAME
	case dto.ModIssueCircular:
		return pb.ModIssueKind_MOD_ISSUE_KIND_CIRCULAR
	case dto.ModIssueFolderCollision:
		return pb.ModIssueKind_MOD_ISSUE_KIND_FOLDER_COLLISION
	case dto.ModIssueDependencyFailed:
		return pb.ModIssueKind_MOD_ISSUE_KIND_DEPENDENCY_FAILED
	default:
		return pb.ModIssueKind_MOD_ISSUE_KIND_UNSPECIFIED
	}
}

// modComponentKindToProto maps a DTO component kind to its proto enum value, unknown values to UNSPECIFIED.
func modComponentKindToProto(kind dto.ModComponentKind) pb.ModComponentKind {
	switch kind {
	case dto.ModComponentCode:
		return pb.ModComponentKind_MOD_COMPONENT_KIND_CODE
	case dto.ModComponentContentPack:
		return pb.ModComponentKind_MOD_COMPONENT_KIND_CONTENT_PACK
	case dto.ModComponentInvalid:
		return pb.ModComponentKind_MOD_COMPONENT_KIND_INVALID
	default:
		return pb.ModComponentKind_MOD_COMPONENT_KIND_UNSPECIFIED
	}
}

// fetchOutcomeToProto maps a DTO fetch outcome to its proto enum value, unknown values to UNSPECIFIED.
func fetchOutcomeToProto(outcome dto.FetchOutcome) pb.FetchOutcome {
	switch outcome {
	case dto.FetchOutcomeQueued:
		return pb.FetchOutcome_FETCH_OUTCOME_QUEUED
	case dto.FetchOutcomeOpenURL:
		return pb.FetchOutcome_FETCH_OUTCOME_OPEN_URL
	case dto.FetchOutcomeUnresolved:
		return pb.FetchOutcome_FETCH_OUTCOME_UNRESOLVED
	case dto.FetchOutcomeAlreadyPresent:
		return pb.FetchOutcome_FETCH_OUTCOME_ALREADY_PRESENT
	default:
		return pb.FetchOutcome_FETCH_OUTCOME_UNSPECIFIED
	}
}
