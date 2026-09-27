package tools

// SteamIsRunningForTTW exposes steamIsRunning for the daemon's TTW pre-flight.
func SteamIsRunningForTTW() bool {
	return steamIsRunning()
}
