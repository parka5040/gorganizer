package daemon

import (
	"github.com/parka/gorganizer/internal/config"
)

// cloneGameConfig returns a copy of gc that shares no memory with it, including every executable's Args, Environment and ExtraRWPaths; it takes no lock, so the caller must own gc or hold s.mu.
func cloneGameConfig(gc config.GameConfig) config.GameConfig {
	out := gc
	if gc.Executables == nil {
		return out
	}
	out.Executables = make([]config.Executable, len(gc.Executables))
	for i, exe := range gc.Executables {
		out.Executables[i] = cloneExecutable(exe)
	}
	return out
}

// cloneExecutable returns a copy of exe whose slices and map share no memory with it, keeping nil collections nil.
func cloneExecutable(exe config.Executable) config.Executable {
	out := exe
	if exe.Args != nil {
		out.Args = append(make([]string, 0, len(exe.Args)), exe.Args...)
	}
	if exe.ExtraRWPaths != nil {
		out.ExtraRWPaths = append(make([]string, 0, len(exe.ExtraRWPaths)), exe.ExtraRWPaths...)
	}
	if exe.Environment != nil {
		out.Environment = make(map[string]string, len(exe.Environment))
		for k, v := range exe.Environment {
			out.Environment[k] = v
		}
	}
	return out
}

// gameConfigSnapshot returns a detached copy of gameID's own configuration read under s.mu; callers must not hold s.mu.
func (s *session) gameConfigSnapshot(gameID string) (config.GameConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gc, ok := s.config.Games[gameID]
	if !ok {
		return config.GameConfig{}, false
	}
	return cloneGameConfig(gc), true
}

// effectiveGameConfigSnapshot resolves gameID's effective configuration, parent included, in one s.mu hold and returns a detached copy; callers must not hold s.mu.
func (s *session) effectiveGameConfigSnapshot(gameID string) (config.GameConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gc, err := s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return config.GameConfig{}, err
	}
	return cloneGameConfig(gc), nil
}

// preferredProton returns the global Proton preference read under s.mu; callers must not hold s.mu.
func (s *session) preferredProton() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.PreferredProton
}

// nexusAPIKey returns the stored Nexus API key read under s.mu; callers must not hold s.mu.
func (s *session) nexusAPIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.NexusAPIKey
}
