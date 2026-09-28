package daemon

import (
	"context"

	"github.com/parka/gorganizer/internal/download"
)

type downloadState struct {
	key     string
	manager *download.Manager
	gameIDs []string
}

type nexusDownloadClient interface {
	download.URLResolver
	ValidateAPIKey(context.Context) error
}

var newNexusDownloadClient = func(key string) nexusDownloadClient {
	return download.NewNexusClient(key)
}

var newDownloadManager = download.NewManager

// downloadStateSnapshot returns detached download settings and the current manager under a short session read lock.
func (s *session) downloadStateSnapshot() downloadState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloadStateSnapshotLocked()
}

// downloadStateSnapshotLocked returns detached download settings while the caller holds the session read or write lock.
func (s *session) downloadStateSnapshotLocked() downloadState {
	state := downloadState{manager: s.downloadMgr}
	if s.config != nil {
		state.key = s.config.NexusAPIKey
		state.gameIDs = make([]string, 0, len(s.config.Games))
		for gameID := range s.config.Games {
			state.gameIDs = append(state.gameIDs, gameID)
		}
	}
	return state
}
