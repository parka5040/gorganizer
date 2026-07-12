package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
)

// SetPluginOrder persists a user-set plugin load order for a profile.
func (pl *PluginStatusService) SetPluginOrder(gameID, profileName string, filenames []string) error {
	if _, ok := pl.s.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	return pl.s.profileMgr.SavePluginOrder(gameID, profileName, filenames)
}

// SetPluginLoadout persists a profile's complete ordered activation state.
func (pl *PluginStatusService) SetPluginLoadout(gameID, profileName string, entries []dto.PluginLoadoutEntryResult) error {
	if _, ok := pl.s.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	loadout := make([]profile.PluginLoadoutEntry, 0, len(entries))
	for _, entry := range entries {
		loadout = append(loadout, profile.PluginLoadoutEntry{
			Filename: entry.Filename,
			Enabled:  entry.Enabled,
		})
	}
	return pl.s.profileMgr.SavePluginLoadout(gameID, profileName, loadout)
}

// StreamPluginStatus is the daemon-side implementation of the IPC streaming
func (pl *PluginStatusService) StreamPluginStatus(ctx context.Context, gameID, profileName string) (<-chan dto.PluginStatusEventResult, error) {
	gc, ok := pl.s.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	_, entries, err := pl.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	spec, _ := plugins.SpecFor(gameID)

	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	baseData := filepath.Join(gc.InstallPath, subpath)
	pl.s.mu.RLock()
	mm, mmOk := pl.s.mountMgrs[gameID]
	pl.s.mu.RUnlock()
	mounted := mmOk && mm.IsMounted()

	enabled := make([]plugins.ModEntry, 0, len(entries))
	allFolders := make([]plugins.ModEntry, 0, len(entries))
	modsDir := config.ModsDir(gameID)
	for _, e := range entries {
		path := filepath.Join(modsDir, e.Name)
		if e.Enabled {
			enabled = append(enabled, plugins.ModEntry{Name: e.Name, Path: path})
		}
		allFolders = append(allFolders, plugins.ModEntry{Name: e.Name, Path: path})
	}

	discoveryMods := enabled
	if mounted {
		discoveryMods = nil
	}
	discovered, err := plugins.DiscoverPlugins(baseData, discoveryMods, spec)
	if err != nil {
		return nil, fmt.Errorf("discovering plugins: %w", err)
	}
	plugins.ApplyCanonicalOrder(discovered, spec)
	seedDir := baseData
	if mounted {
		seedDir = mm.BackupPath()
	}
	if err := applyProfilePluginLoadout(pl.s.profileMgr, gameID, profileName, seedDir, spec, discovered); err != nil {
		return nil, err
	}

	cache := pl.pluginHeaderCacheLazy()
	statuses := plugins.AnalyzeHardDeps(ctx, cache, discovered, allFolders, spec, nil)

	out := make(chan dto.PluginStatusEventResult, 8)

	soft := pl.softDepFetcherLazy()
	snapshot := make([]dto.PluginStatusItemResult, 0, len(statuses))
	enabledModIDs := pl.installedModIDs(gameID)
	gameSlug := download.GameSlug(gameID)

	type pendingJob struct {
		req plugins.SoftDepRequest
	}
	var jobs []pendingJob
	for i := range statuses {
		ps := &statuses[i]
		item := pluginStatusToIPC(ps)
		modID, fileID := pl.lookupNexusIDsForPlugin(gameID, ps.Plugin)
		if ps.Plugin.Enabled && soft != nil && gameSlug != "" && modID != 0 && fileID != 0 {
			item.SoftPending = true
			jobs = append(jobs, pendingJob{req: plugins.SoftDepRequest{
				Filename:   ps.Plugin.Filename,
				GameDomain: gameSlug,
				ModID:      modID,
				ModURL:     fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", gameSlug, modID),
				FileID:     fileID,
			}})
		}
		snapshot = append(snapshot, item)
	}

	pl.emitHardDepWarnings(statuses)

	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case out <- dto.PluginStatusEventResult{Snapshot: snapshot}:
		}

		if soft == nil || len(jobs) == 0 {
			return
		}

		reqs := make(chan plugins.SoftDepRequest, len(jobs))
		results := make(chan plugins.SoftDepResult, len(jobs))
		for _, j := range jobs {
			reqs <- j.req
		}
		close(reqs)

		go soft.Run(ctx, reqs, results)

		for r := range results {
			plugins.FilterSatisfiedSoftDeps(&r, enabledModIDs)
			var update *dto.PluginStatusItemResult
			for i := range snapshot {
				if !strings.EqualFold(snapshot[i].Filename, r.Filename) {
					continue
				}
				merged := snapshot[i]
				merged.SoftPending = false
				kept := merged.Issues[:0]
				for _, iss := range merged.Issues {
					if iss.Kind != dto.DepKindSoftMissing {
						kept = append(kept, iss)
					}
				}
				for _, iss := range r.Issues {
					if iss.Kind == plugins.DepSoftMissing {
						softRef := iss.SoftRef
						if softRef == nil {
							softRef = &plugins.SoftDepRef{}
						}
						kept = append(kept, dto.DepIssueResult{
							Kind:        dto.DepKindSoftMissing,
							SoftModName: softRef.ModName,
							SoftModID:   int32(softRef.ModID),
							SoftModURL:  softRef.URL,
						})
					}
				}
				merged.Issues = kept
				snapshot[i] = merged
				update = &merged
				for _, iss := range r.Issues {
					if iss.Kind != plugins.DepSoftMissing {
						continue
					}
					name := ""
					if iss.SoftRef != nil {
						name = iss.SoftRef.ModName
					}
					detail := fmt.Sprintf("Soft dep missing: %s", name)
					pl.s.publishStatus(dto.StatusEventResult{
						DependencyWarning: &dto.DependencyWarningResult{
							PluginFilename: r.Filename,
							Detail:         detail,
							Kind:           dto.DepKindSoftMissing,
						},
					})
				}
				break
			}
			if update == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- dto.PluginStatusEventResult{Update: update}:
			}
		}
	}()

	return out, nil
}

// applyProfilePluginLoadout overlays a profile's persisted order and activation onto discovery.
func applyProfilePluginLoadout(pm *profile.Manager, gameID, profileName, seedDir string, spec plugins.Spec, list []plugins.Plugin) error {
	loadout, stateExists, err := pm.LoadPluginLoadoutSnapshot(gameID, profileName)
	if err != nil {
		return err
	}
	order := make([]string, 0, len(loadout))
	state := make(map[string]bool, len(loadout))
	for _, entry := range loadout {
		order = append(order, entry.Filename)
		state[strings.ToLower(entry.Filename)] = entry.Enabled
	}

	seeded := false
	if len(order) == 0 && !stateExists && spec.SeedFromData {
		seed, err := plugins.ReadEngineLoadout(spec, seedDir)
		if err != nil {
			return err
		}
		if len(seed) > 0 {
			state = make(map[string]bool, len(seed))
			order = make([]string, 0, len(seed))
			for _, entry := range seed {
				order = append(order, entry.Filename)
				state[strings.ToLower(entry.Filename)] = entry.Enabled
			}
			seeded = true
		} else if len(list) > 0 {
			plugins.ApplyDefaultOrder(list, spec)
			seeded = true
		}
	}

	plugins.ApplyUserOrder(list, spec, order)
	plugins.ApplyActivationState(list, spec, state)
	if !seeded {
		return nil
	}
	seededLoadout := make([]profile.PluginLoadoutEntry, 0, len(list))
	for _, plugin := range list {
		seededLoadout = append(seededLoadout, profile.PluginLoadoutEntry{
			Filename: plugin.Filename,
			Enabled:  plugin.Enabled,
		})
	}
	if err := pm.SavePluginLoadout(gameID, profileName, seededLoadout); err != nil {
		return fmt.Errorf("persisting seeded plugin loadout: %w", err)
	}
	slog.Info("seeded plugin loadout from game data", "game", gameID, "profile", profileName, "source", seedDir)
	return nil
}

func (pl *PluginStatusService) emitHardDepWarnings(statuses []plugins.PluginStatus) {
	for _, ps := range statuses {
		for _, iss := range ps.HardIssues {
			detail := ""
			switch iss.Kind {
			case plugins.DepMasterAbsent:
				detail = fmt.Sprintf("Missing master: %s", iss.Master)
			case plugins.DepMasterDisabled:
				detail = fmt.Sprintf("Master disabled: %s", iss.Master)
			case plugins.DepMasterOutOfOrder:
				detail = fmt.Sprintf("Master loads after dependent: %s", iss.Master)
			default:
				continue
			}
			pl.s.publishStatus(dto.StatusEventResult{
				DependencyWarning: &dto.DependencyWarningResult{
					PluginFilename: ps.Plugin.Filename,
					Detail:         detail,
					Kind:           dto.DepKindResult(iss.Kind),
				},
			})
		}
	}
}

func (pl *PluginStatusService) pluginHeaderCacheLazy() *plugins.HeaderCache {
	pl.s.pluginHeaderCacheOnce.Do(func() {
		pl.s.pluginHeaderCache = plugins.NewHeaderCache(0)
	})
	return pl.s.pluginHeaderCache
}

func (pl *PluginStatusService) softDepFetcherLazy() *plugins.SoftDepFetcher {
	pl.s.softDepFetcherMu.Lock()
	defer pl.s.softDepFetcherMu.Unlock()
	if pl.s.softDepFetcher != nil {
		return pl.s.softDepFetcher
	}
	if pl.s.config.NexusAPIKey == "" {
		return nil
	}
	client := download.NewNexusClient(pl.s.config.NexusAPIKey)
	adapter := &nexusV3Adapter{client: client}
	cacheDir := filepath.Join(config.CacheDir(), "nexus")
	pl.s.softDepFetcher = plugins.NewSoftDepFetcher(adapter, cacheDir)
	return pl.s.softDepFetcher
}

// installedModIDs returns the set of Nexus mod-ids installed under the
func (pl *PluginStatusService) installedModIDs(gameID string) map[int]bool {
	out := map[int]bool{}
	modsDir := config.ModsDir(gameID)
	entries, err := readSubdirs(modsDir)
	if err != nil {
		return out
	}
	for _, name := range entries {
		meta, err := download.LoadModMetadata(filepath.Join(modsDir, name))
		if err != nil || meta == nil {
			continue
		}
		for _, sa := range meta.SourceArchives {
			if sa.ModID > 0 {
				out[sa.ModID] = true
			}
		}
	}
	return out
}

func (pl *PluginStatusService) lookupNexusIDsForPlugin(gameID string, p plugins.Plugin) (modID, fileID int) {
	if p.FromMod == "" {
		return 0, 0
	}
	modsDir := config.ModsDir(gameID)
	meta, err := download.LoadModMetadata(filepath.Join(modsDir, p.FromMod))
	if err != nil || meta == nil || len(meta.SourceArchives) == 0 {
		return 0, 0
	}
	last := meta.SourceArchives[len(meta.SourceArchives)-1]
	return last.ModID, last.FileID
}

// pluginStatusToIPC converts a plugins.PluginStatus into the ipc result type.
func pluginStatusToIPC(ps *plugins.PluginStatus) dto.PluginStatusItemResult {
	out := dto.PluginStatusItemResult{
		Filename: ps.Plugin.Filename,
		Ext:      ps.Plugin.Ext,
		IsLight:  ps.IsLight,
		Enabled:  ps.Plugin.Enabled,
		FromMod:  ps.Plugin.FromMod,
	}
	for _, iss := range ps.HardIssues {
		out.Issues = append(out.Issues, dto.DepIssueResult{
			Kind:   dto.DepKindResult(iss.Kind),
			Master: iss.Master,
		})
	}
	return out
}
