package dto

type CollisionPolicy int

const (
	PolicyAbort     CollisionPolicy = 0
	PolicySkip      CollisionPolicy = 1
	PolicyRename    CollisionPolicy = 2
	PolicyOverwrite CollisionPolicy = 3
)

type ExportRequest struct {
	GameID              string
	OutputPath          string
	ModFolders          []string
	ProfileNames        []string
	IncludeOverwrite    bool
	IncludeGameSettings bool
}

type ImportRequest struct {
	GameID             string
	ArchivePath        string
	Policy             CollisionPolicy
	ModPolicyOverrides map[string]CollisionPolicy
	ModFolders         []string
	ProfileNames       []string
}

type ImportPreviewMod struct {
	Folder      string
	Name        string
	FileCount   int32
	TotalBytes  int64
	NexusModID  int32
	NexusFileID int32
	Collision   bool
}

type ImportPreviewProfile struct {
	Name      string
	Collision bool
}

type ImportPreview struct {
	SchemaVersion        int32
	GorganizerVersion    string
	GameID               string
	ExportedAt           string
	Mods                 []ImportPreviewMod
	Profiles             []ImportPreviewProfile
	IncludesOverwrite    bool
	IncludesGameSettings bool
}

type TransferProgress struct {
	Step        string
	CurrentItem string
	ItemsDone   int32
	ItemsTotal  int32
	BytesDone   int64
	BytesTotal  int64
}

type TransferSummary struct {
	ModsExported        int32
	ModsImported        int32
	ProfilesTransferred int32
	Skipped             []string
	Renamed             map[string]string
	OutputPath          string
}
