package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/plugins"
)

// StreamPluginStatus is the daemon-side implementation of the IPC streaming
// RPC. The first message on the returned channel is a snapshot with hard-
// dependency status fully computed; subsequent messages are deltas as
// background soft-dep checks complete.
//
// The channel closes when:
//   - all soft-dep work has finished (or there was none to do), OR
//   - ctx is cancelled (the gRPC stream's context — closes when client
//     disconnects)
func (d *Daemon) StreamPluginStatus(ctx context.Context, gameID, profileName string) (<-chan ipc.PluginStatusEventResult, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	_, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	spec, _ := plugins.SpecFor(gameID)

	// Discovered plugins (load order) — base Data/ + every enabled mod.
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	baseData := filepath.Join(gc.InstallPath, subpath)

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

	discovered, err := plugins.DiscoverPlugins(baseData, enabled)
	if err != nil {
		return nil, fmt.Errorf("discovering plugins: %w", err)
	}

	cache := d.pluginHeaderCacheLazy()
	statuses := plugins.AnalyzeHardDeps(ctx, cache, discovered, allFolders, spec, nil)

	out := make(chan ipc.PluginStatusEventResult, 8)

	// Build snapshot. Soft-dep checks are still pending for every plugin
	// that has a Nexus mod-id we can look up.
	soft := d.softDepFetcherLazy()
	snapshot := make([]ipc.PluginStatusItemResult, 0, len(statuses))
	enabledModIDs := d.installedModIDs(gameID)
	gameSlug := download.GameSlug(gameID)

	type pendingJob struct {
		req plugins.SoftDepRequest
	}
	var jobs []pendingJob
	for i := range statuses {
		ps := &statuses[i]
		item := pluginStatusToIPC(ps)
		// Decide whether a soft-dep lookup is worth scheduling for this plugin.
		modID, fileID := d.lookupNexusIDsForPlugin(gameID, ps.Plugin)
		if soft != nil && gameSlug != "" && modID != 0 && fileID != 0 {
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

	// Emit a DependencyWarning for each hard issue so the Activity Log
	// surfaces them on profile activation. We do this here (in the same
	// path the GUI subscribes to) instead of inside MountVFS so cold-launch
	// (no plugin panel open) doesn't burn the analyzer pass twice.
	d.emitHardDepWarnings(statuses)

	// Send the snapshot synchronously before kicking off the worker pool —
	// the receiver's first read must always be the snapshot. The send is
	// guarded by ctx.Done in case the client already disconnected.
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case out <- ipc.PluginStatusEventResult{Snapshot: snapshot}:
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
			// Build the delta. Find the matching snapshot row to merge with.
			var update *ipc.PluginStatusItemResult
			for i := range snapshot {
				if !strings.EqualFold(snapshot[i].Filename, r.Filename) {
					continue
				}
				merged := snapshot[i]
				merged.SoftPending = false
				// Strip any existing soft issues; replace with the result's.
				kept := merged.Issues[:0]
				for _, iss := range merged.Issues {
					if iss.Kind != ipc.DepKindSoftMissing {
						kept = append(kept, iss)
					}
				}
				for _, iss := range r.Issues {
					if iss.Kind == plugins.DepSoftMissing {
						softRef := iss.SoftRef
						if softRef == nil {
							softRef = &plugins.SoftDepRef{}
						}
						kept = append(kept, ipc.DepIssueResult{
							Kind:        ipc.DepKindSoftMissing,
							SoftModName: softRef.ModName,
							SoftModID:   int32(softRef.ModID),
							SoftModURL:  softRef.URL,
						})
					}
				}
				merged.Issues = kept
				snapshot[i] = merged
				update = &merged
				// Emit Activity Log warnings for the soft issues.
				for _, iss := range r.Issues {
					if iss.Kind != plugins.DepSoftMissing {
						continue
					}
					name := ""
					if iss.SoftRef != nil {
						name = iss.SoftRef.ModName
					}
					detail := fmt.Sprintf("Soft dep missing: %s", name)
					d.publishStatus(ipc.StatusEventResult{
						DependencyWarning: &ipc.DependencyWarningResult{
							PluginFilename: r.Filename,
							Detail:         detail,
							Kind:           ipc.DepKindSoftMissing,
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
			case out <- ipc.PluginStatusEventResult{Update: update}:
			}
		}
	}()

	return out, nil
}

// emitHardDepWarnings pushes one DependencyWarning per hard issue onto the
// daemon's status stream. Severity is implied by Kind on the wire — the C++
// frontend maps absent / out-of-order to Error and disabled to Warning.
func (d *Daemon) emitHardDepWarnings(statuses []plugins.PluginStatus) {
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
			d.publishStatus(ipc.StatusEventResult{
				DependencyWarning: &ipc.DependencyWarningResult{
					PluginFilename: ps.Plugin.Filename,
					Detail:         detail,
					Kind:           ipc.DepKindResult(iss.Kind),
				},
			})
		}
	}
}

// publishStatus is a non-blocking send to the daemon's status channel.
// Drops the event when the channel is full — same backpressure policy as
// other daemon-internal status producers.
func (d *Daemon) publishStatus(evt ipc.StatusEventResult) {
	select {
	case d.statusCh <- evt:
	default:
		slog.Debug("status channel full, dropping plugin-status event")
	}
}

func (d *Daemon) pluginHeaderCacheLazy() *plugins.HeaderCache {
	d.pluginHeaderCacheOnce.Do(func() {
		d.pluginHeaderCache = plugins.NewHeaderCache(0)
	})
	return d.pluginHeaderCache
}

func (d *Daemon) softDepFetcherLazy() *plugins.SoftDepFetcher {
	d.softDepFetcherMu.Lock()
	defer d.softDepFetcherMu.Unlock()
	if d.softDepFetcher != nil {
		return d.softDepFetcher
	}
	if d.config.NexusAPIKey == "" {
		return nil
	}
	client := download.NewNexusClient(d.config.NexusAPIKey)
	adapter := &nexusV3Adapter{client: client}
	cacheDir := filepath.Join(config.CacheDir(), "nexus")
	d.softDepFetcher = plugins.NewSoftDepFetcher(adapter, cacheDir)
	return d.softDepFetcher
}

// installedModIDs returns the set of Nexus mod-ids installed under the
// active mod set for a game. Used to filter satisfied soft-deps.
func (d *Daemon) installedModIDs(gameID string) map[int]bool {
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

// lookupNexusIDsForPlugin returns (modID, fileID) recorded in the mod
// folder's metadata.yaml for the mod that contributes this plugin. Returns
// (0, 0) when the plugin came from the base Data dir or the mod has no
// Nexus archive recorded.
func (d *Daemon) lookupNexusIDsForPlugin(gameID string, p plugins.Plugin) (modID, fileID int) {
	if p.FromMod == "" {
		return 0, 0
	}
	modsDir := config.ModsDir(gameID)
	meta, err := download.LoadModMetadata(filepath.Join(modsDir, p.FromMod))
	if err != nil || meta == nil || len(meta.SourceArchives) == 0 {
		return 0, 0
	}
	// Pick the most recent archive — it carries the freshest mod/file ids
	// (a mod that's been re-uploaded rolls forward through SourceArchives).
	last := meta.SourceArchives[len(meta.SourceArchives)-1]
	return last.ModID, last.FileID
}

// pluginStatusToIPC converts a plugins.PluginStatus into the ipc result type.
// Soft issues are not copied — those arrive via the soft-dep channel later.
func pluginStatusToIPC(ps *plugins.PluginStatus) ipc.PluginStatusItemResult {
	out := ipc.PluginStatusItemResult{
		Filename: ps.Plugin.Filename,
		Ext:      ps.Plugin.Ext,
		IsLight:  ps.IsLight,
		Enabled:  ps.Plugin.Enabled,
		FromMod:  ps.Plugin.FromMod,
	}
	for _, iss := range ps.HardIssues {
		out.Issues = append(out.Issues, ipc.DepIssueResult{
			Kind:   ipc.DepKindResult(iss.Kind),
			Master: iss.Master,
		})
	}
	return out
}
