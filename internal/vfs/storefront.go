package vfs

type StorefrontChange string

const (
	StorefrontUnknown   StorefrontChange = "unknown"
	StorefrontBusy      StorefrontChange = "busy"
	StorefrontChanged   StorefrontChange = "changed"
	StorefrontUnchanged StorefrontChange = "unchanged"
)

type storefrontState interface {
	Idle() bool
	StorefrontValues() (appID int, buildID string, lastUpdated int64, depotFingerprint string)
}

// CompareStorefront classifies the current installation against a recorded baseline.
func CompareStorefront(baseline *StorefrontSnapshot, current storefrontState, currentErr error) StorefrontChange {
	if baseline == nil || currentErr != nil {
		return StorefrontUnknown
	}
	appID, buildID, lastUpdated, depotFingerprint := current.StorefrontValues()
	if baseline.Store != "steam" || baseline.AppID != appID {
		return StorefrontUnknown
	}
	if !current.Idle() {
		return StorefrontBusy
	}
	if baseline.BuildID != buildID || baseline.LastUpdated != lastUpdated || baseline.DepotFingerprint != depotFingerprint {
		return StorefrontChanged
	}
	return StorefrontUnchanged
}
