package tools

// FindSteamRootForTTW exposes findSteamRoot for the daemon's TTW module.
func FindSteamRootForTTW() (string, error) {
	return findSteamRoot()
}

// SteamIsRunningForTTW exposes steamIsRunning for the daemon's TTW pre-flight.
func SteamIsRunningForTTW() bool {
	return steamIsRunning()
}
