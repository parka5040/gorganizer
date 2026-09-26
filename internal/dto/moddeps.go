package dto

import (
	"fmt"
	"time"
)

type ModIssueKind int32

const (
	ModIssueUnspecified      ModIssueKind = 0
	ModIssueMissing          ModIssueKind = 1
	ModIssueDisabled         ModIssueKind = 2
	ModIssueVersionTooLow    ModIssueKind = 3
	ModIssueDuplicateID      ModIssueKind = 4
	ModIssueInvalidManifest  ModIssueKind = 5
	ModIssueNeedsNewerLoader ModIssueKind = 6
	ModIssueNeedsNewerGame   ModIssueKind = 7
	ModIssueCircular         ModIssueKind = 8
	ModIssueFolderCollision  ModIssueKind = 9
	ModIssueDependencyFailed ModIssueKind = 10
)

type ModComponentKind int32

const (
	ModComponentUnspecified ModComponentKind = 0
	ModComponentCode        ModComponentKind = 1
	ModComponentContentPack ModComponentKind = 2
	ModComponentInvalid     ModComponentKind = 3
)

type FetchOutcome int32

const (
	FetchOutcomeUnspecified    FetchOutcome = 0
	FetchOutcomeQueued         FetchOutcome = 1
	FetchOutcomeOpenURL        FetchOutcome = 2
	FetchOutcomeUnresolved     FetchOutcome = 3
	FetchOutcomeAlreadyPresent FetchOutcome = 4
)

type ModIssueResult struct {
	Kind            ModIssueKind
	TargetID        string
	RequiredVersion string
	FoundVersion    string
	Providers       []string
	Detail          string
}

type ModComponentResult struct {
	Folder        string
	ProviderMod   string
	UniqueID      string
	Name          string
	Version       string
	Kind          ModComponentKind
	Bundled       bool
	Failed        bool
	Issues        []ModIssueResult
	UpdateVersion string
	UpdateURL     string
	NexusID       int
	UpdateStale   bool
}

type MissingDependencyResult struct {
	UniqueID          string
	MinimumVersion    string
	RequiredBy        []string
	DisabledProviders []string
	Name              string
	NexusID           int
	URL               string
	Resolvable        bool
	Stale             bool
}

type PendingEnableResult struct {
	BatchID     string
	ProfileName string
	ModName     string
	UniqueID    string
}

type DependencyRequestIssueResult struct {
	UniqueID  string
	BatchID   string
	State     string
	Detail    string
	UpdatedAt time.Time
}

type ModDependencyReportResult struct {
	GameID           string
	ProfileName      string
	Components       []ModComponentResult
	Missing          []MissingDependencyResult
	PendingEnables   []PendingEnableResult
	RootManifestMods []string
	RemoteChecked    bool
	RemoteError      string
	LoaderVersion    string
	GameVersion      string
	RecentFailures   []DependencyRequestIssueResult
}

type DependencyFetchResult struct {
	UniqueID   string
	Outcome    FetchOutcome
	DownloadID string
	URL        string
	Reason     string
	BatchID    string
}

type InstallCompletedResult struct {
	GameID         string
	ModName        string
	ArchiveRelPath string
	BatchID        string
	BatchIDs       []string
}

type ModDependenciesUnsupportedError struct {
	GameID string
}

// Error reports that the game has no SMAPI-style mod dependency support.
func (e *ModDependenciesUnsupportedError) Error() string {
	return fmt.Sprintf("%s has no mod dependency support", e.GameID)
}
