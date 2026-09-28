package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/tools"
)

// ListProfileIniFiles seeds the profile's ini directory from the game's current Documents INIs on first call.
func (in *IniService) ListProfileIniFiles(gameID, profileName string) (*dto.ProfileIniListResult, error) {
	gc, err := in.s.effectiveGameConfigSnapshot(gameID)
	if err != nil {
		return nil, err
	}
	spec, hasSpec := inipkg.SpecFor(gameID)
	if !hasSpec {
		return &dto.ProfileIniListResult{}, nil
	}
	p, _, err := in.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	compatData, _ := tools.ResolveCompatDataPath(&gc, 0)
	unlockProfiles := in.s.lockProfiles(gameID)
	seedErr := in.s.iniMgr.SeedFromDocumentsAt(gameID, profileName, gc.SteamAppID, compatData)
	unlockProfiles()
	if seedErr != nil {
		slog.Warn("seeding profile INIs failed", "err", seedErr)
	}
	docs, _ := inipkg.DocumentsPath(gc.SteamAppID, spec.MyGamesSubdir)

	result := &dto.ProfileIniListResult{
		MyGamesDir:   docs,
		UseCustomIni: p.UseCustomIni,
	}
	for _, name := range spec.Files {
		content, err := in.s.iniMgr.Read(gameID, profileName, name)
		if err != nil {
			slog.Warn("reading profile INI failed", "file", name, "err", err)
			continue
		}
		result.Files = append(result.Files, dto.ProfileIniFileResult{
			Filename: name,
			Content:  content,
			DiskPath: in.s.iniMgr.IniPath(gameID, profileName, name),
		})
	}
	return result, nil
}

// SaveProfileIniFile writes a profile INI and reports whether it reached the game.
func (in *IniService) SaveProfileIniFile(gameID, profileName, filename, content string) (*dto.ProfileIniSaveResult, error) {
	if !in.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer in.s.lockProfiles(gameID)()
	if err := in.s.iniMgr.Write(gameID, profileName, filename, content); err != nil {
		return nil, err
	}
	result := &dto.ProfileIniSaveResult{Outcome: dto.IniSaveSaved}
	p, _, err := in.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		result.Outcome = dto.IniSaveSavedApplyFailed
		result.ApplyError = fmt.Errorf("loading profile: %w", err).Error()
		return result, nil
	}
	if !p.UseCustomIni {
		return result, nil
	}
	if _, err := in.pushProfileIniFiles(gameID, profileName); err != nil {
		result.Outcome = dto.IniSaveSavedApplyFailed
		result.ApplyError = err.Error()
		return result, nil
	}
	result.Outcome = dto.IniSaveSavedAndApplied
	return result, nil
}

// ApplyProfileIniFiles copies the profile's INIs into the game without changing its settings.
func (in *IniService) ApplyProfileIniFiles(gameID, profileName string) (int, error) {
	if !in.s.gameConfigured(gameID) {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer in.s.lockProfiles(gameID)()
	if _, _, err := in.s.profileMgr.Load(gameID, profileName); err != nil {
		return 0, fmt.Errorf("loading profile: %w", err)
	}
	return in.pushProfileIniFiles(gameID, profileName)
}

// pushProfileIniFiles copies and verifies the profile's INIs in the game's Documents folder.
func (in *IniService) pushProfileIniFiles(gameID, profileName string) (int, error) {
	gc, err := in.s.effectiveGameConfigSnapshot(gameID)
	if err != nil {
		return 0, err
	}
	compatData, _ := tools.ResolveCompatDataPath(&gc, 0)
	spec, ok := inipkg.SpecFor(gameID)
	if !ok {
		return 0, fmt.Errorf("no INI spec for game %q", gameID)
	}
	var docs string
	if compatData != "" {
		docs, err = inipkg.DocumentsPathAt(compatData, spec.MyGamesSubdir)
	} else {
		docs, err = inipkg.DocumentsPath(gc.SteamAppID, spec.MyGamesSubdir)
	}
	if err != nil {
		return 0, fmt.Errorf("finding game Documents folder: %w", err)
	}
	documentsDir := filepath.Dir(filepath.Dir(docs))
	info, err := os.Stat(documentsDir)
	if err != nil {
		return 0, fmt.Errorf("game Documents folder %s is unavailable: %w", documentsDir, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("game Documents folder %s is not a directory", documentsDir)
	}
	reports, err := in.s.iniMgr.PushToDocumentsAt(gameID, profileName, gc.SteamAppID, compatData)
	if err != nil {
		return 0, fmt.Errorf("applying profile INIs: %w", err)
	}
	count := 0
	for _, report := range reports {
		if report.Skipped {
			continue
		}
		if !report.Verified {
			return count, fmt.Errorf("could not verify %s at %s: %s", report.Filename, report.TargetPath, report.Note)
		}
		count++
	}
	return count, nil
}

func (in *IniService) SetProfileIniEnabled(gameID, profileName string, enabled bool) (*dto.ProfileIniStatusResult, error) {
	if !in.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := in.saveCustomIniFlag(gameID, profileName, enabled); err != nil {
		return nil, err
	}
	return in.GetProfileIniStatus(gameID, profileName)
}

// saveCustomIniFlag rewrites a profile's custom-INI flag under the game's profile lock.
func (in *IniService) saveCustomIniFlag(gameID, profileName string, enabled bool) error {
	defer in.s.lockProfiles(gameID)()
	p, entries, err := in.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}
	p.UseCustomIni = enabled
	if err := in.s.profileMgr.Save(p, entries); err != nil {
		return fmt.Errorf("saving profile: %w", err)
	}
	return nil
}

// ListIniTweaks returns the named INI presets for the game paired with their applied state.
func (in *IniService) ListIniTweaks(gameID, profileName string) ([]dto.IniTweakStateResult, error) {
	if !in.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	states, err := in.s.iniMgr.ListTweaks(gameID, profileName)
	if err != nil {
		return nil, err
	}
	out := make([]dto.IniTweakStateResult, 0, len(states))
	for _, s := range states {
		out = append(out, dto.IniTweakStateResult{
			ID:          s.ID,
			Name:        s.Name,
			Description: s.Description,
			TargetFile:  s.TargetFile,
			Enabled:     s.Enabled,
		})
	}
	return out, nil
}

// SetIniTweak toggles an INI preset on or off in the profile's Custom.ini.
func (in *IniService) SetIniTweak(gameID, profileName, tweakID string, enabled bool) (*dto.IniTweakStateResult, error) {
	if !in.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer in.s.lockProfiles(gameID)()
	state, err := in.s.iniMgr.SetTweak(gameID, profileName, tweakID, enabled)
	if err != nil {
		return nil, err
	}
	p, _, perr := in.s.profileMgr.Load(gameID, profileName)
	if perr != nil {
		slog.Warn("loading profile after tweak toggle failed", "err", perr)
	} else if p.UseCustomIni {
		if _, err := in.pushProfileIniFiles(gameID, profileName); err != nil {
			slog.Warn("pushing INI after tweak toggle failed", "err", err)
		}
	}
	return &dto.IniTweakStateResult{
		ID:          state.ID,
		Name:        state.Name,
		Description: state.Description,
		TargetFile:  state.TargetFile,
		Enabled:     state.Enabled,
	}, nil
}

func (in *IniService) GetProfileIniStatus(gameID, profileName string) (*dto.ProfileIniStatusResult, error) {
	gc, err := in.s.effectiveGameConfigSnapshot(gameID)
	if err != nil {
		return nil, err
	}
	spec, hasSpec := inipkg.SpecFor(gameID)
	result := &dto.ProfileIniStatusResult{
		GameID:          gameID,
		ProfileName:     profileName,
		GameSupportsIni: hasSpec,
	}
	if hasSpec {
		docs, _ := inipkg.DocumentsPath(gc.SteamAppID, spec.MyGamesSubdir)
		result.MyGamesDir = docs
	}
	p, _, err := in.s.profileMgr.Load(gameID, profileName)
	if err == nil {
		result.UseCustomIni = p.UseCustomIni
	}
	return result, nil
}
