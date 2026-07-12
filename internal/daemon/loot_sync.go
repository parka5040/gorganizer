package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/gamedef"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/tools"
)

func (es *ExecutableService) importLOOTLoadout(
	gameID, profileName string,
	gameConfig config.GameConfig,
	workspace *tools.LOOTWorkspace,
) error {
	spec, ok := plugins.SpecFor(gameID)
	if !ok {
		return fmt.Errorf("game %s has no plugin-state adapter", gameID)
	}
	stateDir := workspace.DataPath
	if spec.StateLocation == gamedef.PluginStateGameRootIni {
		stateDir = workspace.GameRoot
	} else if spec.StateLocation != gamedef.PluginStateDataDir {
		var err error
		compatData, resolveErr := tools.ResolveCompatDataPath(&gameConfig, 0)
		if resolveErr == nil {
			stateDir, err = inipkg.AppDataLocalPathAt(compatData, spec.AppDataSubdir)
		} else {
			stateDir, err = inipkg.AppDataLocalPath(gameConfig.SteamAppID, spec.AppDataSubdir)
		}
		if err != nil {
			return fmt.Errorf("resolving LOOT plugin-state directory: %w", err)
		}
	}
	engineState, err := plugins.ReadEngineLoadout(spec, stateDir)
	if err != nil {
		return err
	}
	enabled := make(map[string]bool, len(engineState))
	canonical := make(map[string]string, len(engineState))
	for _, entry := range engineState {
		key := strings.ToLower(entry.Filename)
		enabled[key] = entry.Enabled
		canonical[key] = entry.Filename
	}
	for _, master := range spec.ImplicitMasters {
		key := strings.ToLower(master)
		enabled[key] = true
		canonical[key] = master
	}

	order, err := readLOOTOrder(spec, stateDir, workspace.DataPath)
	if err != nil {
		return err
	}
	if len(order) == 0 {
		return errors.New("LOOT produced an empty plugin order")
	}
	loadout := make([]profile.PluginLoadoutEntry, 0, len(order))
	for _, filename := range order {
		key := strings.ToLower(filename)
		if name := canonical[key]; name != "" {
			filename = name
		}
		active, mentioned := enabled[key]
		if !mentioned {
			active = false
		}
		loadout = append(loadout, profile.PluginLoadoutEntry{Filename: filename, Enabled: active})
	}
	if err := es.s.profileMgr.SavePluginLoadout(gameID, profileName, loadout); err != nil {
		return fmt.Errorf("saving LOOT profile loadout: %w", err)
	}
	return nil
}

func readLOOTOrder(spec plugins.Spec, stateDir, dataDir string) ([]string, error) {
	if spec.OrderFromPlugins && spec.PluginsFileName != "" {
		path := filepath.Join(stateDir, spec.PluginsFileName)
		order, err := readPluginLines(path)
		if err != nil {
			return nil, err
		}
		filtered := order[:0]
		for _, filename := range order {
			if supportedPluginExtension(spec, filename) {
				filtered = append(filtered, filename)
			}
		}
		return filtered, nil
	}
	if spec.LoadOrderFileName != "" {
		path := filepath.Join(stateDir, spec.LoadOrderFileName)
		if order, err := readPluginLines(path); err == nil && len(order) > 0 {
			filtered := order[:0]
			for _, filename := range order {
				if supportedPluginExtension(spec, filename) {
					filtered = append(filtered, filename)
				}
			}
			if len(filtered) > 0 {
				return filtered, nil
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	type timestampedPlugin struct {
		name    string
		modTime int64
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading LOOT workspace Data: %w", err)
	}
	pluginsByTime := make([]timestampedPlugin, 0)
	for _, entry := range entries {
		if entry.IsDir() || !supportedPluginExtension(spec, entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		pluginsByTime = append(pluginsByTime, timestampedPlugin{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	sort.SliceStable(pluginsByTime, func(i, j int) bool {
		if pluginsByTime[i].modTime != pluginsByTime[j].modTime {
			return pluginsByTime[i].modTime < pluginsByTime[j].modTime
		}
		return strings.ToLower(pluginsByTime[i].name) < strings.ToLower(pluginsByTime[j].name)
	})
	order := make([]string, 0, len(pluginsByTime))
	for _, plugin := range pluginsByTime {
		order = append(order, plugin.name)
	}
	return order, nil
}

func readPluginLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	order := make([]string, 0)
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "*"), "#"))
		if line == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(line))
		if ext != ".esm" && ext != ".esp" && ext != ".esl" {
			continue
		}
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		order = append(order, line)
	}
	return order, scanner.Err()
}

func supportedPluginExtension(spec plugins.Spec, filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".esm" && ext != ".esp" && ext != ".esl" {
		return false
	}
	if len(spec.SupportedExts) == 0 {
		return true
	}
	for _, supported := range spec.SupportedExts {
		if strings.EqualFold(ext, supported) {
			return true
		}
	}
	return false
}
