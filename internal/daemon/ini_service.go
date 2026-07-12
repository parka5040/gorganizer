package daemon

import (
	"fmt"
	"log/slog"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/tools"
)

// ListProfileIniFiles seeds the profile's ini directory from the game's current Documents INIs on first call.
func (in *IniService) ListProfileIniFiles(gameID, profileName string) (*dto.ProfileIniListResult, error) {
	gc, err := in.s.config.EffectiveGameConfig(gameID)
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
	if err := in.s.iniMgr.SeedFromDocumentsAt(gameID, profileName, gc.SteamAppID, compatData); err != nil {
		slog.Warn("seeding profile INIs failed", "err", err)
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

func (in *IniService) SaveProfileIniFile(gameID, profileName, filename, content string) error {
	if _, ok := in.s.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := in.s.iniMgr.Write(gameID, profileName, filename, content); err != nil {
		return err
	}
	p, _, err := in.s.profileMgr.Load(gameID, profileName)
	if err == nil && p.UseCustomIni {
		gc, gcErr := in.s.config.EffectiveGameConfig(gameID)
		if gcErr == nil {
			if _, pushErr := in.s.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); pushErr != nil {
				slog.Warn("pushing INI after save failed", "err", pushErr)
			}
		}
	}
	return nil
}

func (in *IniService) SetProfileIniEnabled(gameID, profileName string, enabled bool) (*dto.ProfileIniStatusResult, error) {
	if _, ok := in.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	p, entries, err := in.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	p.UseCustomIni = enabled
	if err := in.s.profileMgr.Save(p, entries); err != nil {
		return nil, fmt.Errorf("saving profile: %w", err)
	}
	return in.GetProfileIniStatus(gameID, profileName)
}

// ListIniTweaks returns the named INI presets for the game paired with their applied state.
func (in *IniService) ListIniTweaks(gameID, profileName string) ([]dto.IniTweakStateResult, error) {
	if _, ok := in.s.config.Games[gameID]; !ok {
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
	if _, ok := in.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	state, err := in.s.iniMgr.SetTweak(gameID, profileName, tweakID, enabled)
	if err != nil {
		return nil, err
	}
	p, _, perr := in.s.profileMgr.Load(gameID, profileName)
	if perr == nil && p.UseCustomIni {
		gc, gcErr := in.s.config.EffectiveGameConfig(gameID)
		if gcErr == nil {
			if _, err := in.s.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); err != nil {
				slog.Warn("pushing INI after tweak toggle failed", "err", err)
			}
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
	gc, err := in.s.config.EffectiveGameConfig(gameID)
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
