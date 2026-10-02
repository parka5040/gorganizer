package dto

type UpdateCheckOutcome int

const (
	UpdateCheckUnspecified     UpdateCheckOutcome = 0
	UpdateCheckUpToDate        UpdateCheckOutcome = 1
	UpdateCheckUpdateAvailable UpdateCheckOutcome = 2
	UpdateCheckOffline         UpdateCheckOutcome = 3
	UpdateCheckUnavailable     UpdateCheckOutcome = 4
	UpdateCheckNotSupported    UpdateCheckOutcome = 5
)

type UpdateCheckResult struct {
	Outcome       UpdateCheckOutcome
	LatestVersion string
	NotesURL      string
	Detail        string
}
