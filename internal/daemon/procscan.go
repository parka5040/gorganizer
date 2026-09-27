package daemon

import "github.com/parka/gorganizer/internal/procscan"

var processTableRoot = "/proc"

// processRunningIn reports whether a process runs from dir or is Steam's launch wrapper for one of appIDs, using the injected scanner when one is set.
func (s *session) processRunningIn(dir string, appIDs []int) (bool, error) {
	if s.procScan != nil {
		return s.procScan(dir)
	}
	running, err := procscan.RunningIn(processTableRoot, dir, appIDs)
	if err != nil {
		return true, nil
	}
	return running, nil
}
