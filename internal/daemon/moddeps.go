package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/transfer"
	"github.com/parka/gorganizer/internal/vfs"
)

const (
	gameVersionFileMaxBytes = 8 << 20

	fetchReasonProvided        = "provided"
	fetchReasonDisabled        = "disabled"
	fetchReasonPendingEnable   = "pending_enable"
	fetchReasonNotMissing      = "not_missing"
	fetchReasonNoNexusPage     = "no_nexus_page"
	fetchReasonLookupFailed    = "lookup_failed"
	fetchReasonBundled         = "bundled"
	fetchReasonNoAPIKey        = "no_api_key"
	fetchReasonNoDownloads     = "downloads_unavailable"
	fetchReasonPremiumCheck    = "premium_check_failed"
	fetchReasonNotPremium      = "not_premium"
	fetchReasonFileList        = "file_list_failed"
	fetchReasonAmbiguousMain   = "ambiguous_main_file"
	fetchReasonNoMainFile      = "no_main_file"
	fetchReasonPremiumMismatch = "premium_file_mismatch"
	fetchReasonQueueFailed     = "queue_failed"
	fetchReasonInFlight        = "in_flight"
	fetchReasonInstalling      = "installing"
)

type nexusFileLister interface {
	ListModFilesContext(ctx context.Context, gameSlug string, modID int) (*download.NexusFileList, error)
}

type dependencyDownloader interface {
	StartDownloadForGame(uri, overrideGameID string) (string, int, error)
	GetProgress(downloadID string) (*download.DownloadSnapshot, error)
}

type ModDependencyService struct {
	s *session

	clientMu  sync.Mutex
	client    *smapi.Client
	newClient func(def *gamedef.ModLoaderSpec) *smapi.Client

	nexusFiles func(key string) nexusFileLister
	downloads  func() dependencyDownloader

	requestLocks depRequestLocks
	loadRequests func(gameID string) (*depRequestsDoc, error)
	outcomes     downloadOutcomes
}

type projectionMeta struct {
	farmRoot         string
	loader           *gamedef.ModLoaderSpec
	rootManifestMods []string
	loaderVersion    string
	gameVersion      string
}

type projectionLayer struct {
	root     string
	provider string
	mod      bool
}

type intendedMod struct {
	layer    int
	folder   string
	manifest *smapi.Manifest
}

type fetchPlan struct {
	nexusID        int
	indices        []int
	fileID         int
	reason         string
	downloadID     string
	attached       bool
	install        string
	installFileID  int
	installArchive string
	pendingEnable  map[int]string
	stale          map[string]bool
	queued         bool
	queueFailed    bool
}

var issueKindResults = map[smapi.IssueKind]dto.ModIssueKind{
	smapi.IssueMissing:          dto.ModIssueMissing,
	smapi.IssueDisabled:         dto.ModIssueDisabled,
	smapi.IssueVersionTooLow:    dto.ModIssueVersionTooLow,
	smapi.IssueDuplicateID:      dto.ModIssueDuplicateID,
	smapi.IssueInvalidManifest:  dto.ModIssueInvalidManifest,
	smapi.IssueNeedsNewerLoader: dto.ModIssueNeedsNewerLoader,
	smapi.IssueNeedsNewerGame:   dto.ModIssueNeedsNewerGame,
	smapi.IssueCircular:         dto.ModIssueCircular,
	smapi.IssueFolderCollision:  dto.ModIssueFolderCollision,
	smapi.IssueDependencyFailed: dto.ModIssueDependencyFailed,
}

var componentKindResults = map[smapi.ModKind]dto.ModComponentKind{
	smapi.KindCode:        dto.ModComponentCode,
	smapi.KindContentPack: dto.ModComponentContentPack,
	smapi.KindInvalid:     dto.ModComponentInvalid,
}

// newModDependencyService builds the dependency service with the live smapi.io, Nexus and download seams.
func newModDependencyService(s *session) *ModDependencyService {
	return &ModDependencyService{
		s:          s,
		newClient:  newSMAPIWebClient,
		nexusFiles: func(key string) nexusFileLister { return download.NewNexusClient(key) },
	}
}

// newSMAPIWebClient builds the daemon's smapi.io client for a loader row with the per-user metadata cache.
func newSMAPIWebClient(def *gamedef.ModLoaderSpec) *smapi.Client {
	client := &smapi.Client{
		BaseURL:      def.WebAPIBaseURL,
		RouteVersion: def.WebAPIVersion,
		UserAgent:    "gorganizer/" + transfer.GorganizerVersion,
	}
	if dir, err := smapi.DefaultCacheDir(); err == nil {
		client.Cache = smapi.OpenCache(dir)
	}
	return client
}

// webClient returns the daemon's shared smapi.io client, building it on first use.
func (md *ModDependencyService) webClient(def *gamedef.ModLoaderSpec) *smapi.Client {
	md.clientMu.Lock()
	defer md.clientMu.Unlock()
	if md.client == nil {
		md.client = md.newClient(def)
	}
	return md.client
}

// nexusAccess reads the Nexus API key and the download manager under the session lock and releases it.
func (md *ModDependencyService) nexusAccess() (string, dependencyDownloader) {
	state := md.s.downloadStateSnapshot()
	if md.downloads != nil {
		return state.key, md.downloads()
	}
	if state.manager == nil {
		return state.key, nil
	}
	return state.key, state.manager
}

// projectSMAPI waits for startup recovery within ctx, then builds the analyzer input of profileName's desired deployment: the vanilla deploy folder, the enabled mods in modlist order, then Overwrite.
func (s *session) projectSMAPI(ctx context.Context, gameID, profileName string) (smapi.AnalyzeInput, projectionMeta, error) {
	spec, def, err := dependencySpecFor(gameID)
	if err != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, err
	}
	if err := validateProfileName(profileName); err != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, err
	}
	if err := s.awaitRecoveryCtx(ctx); err != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, err
	}
	s.mu.RLock()
	gc, cfgErr := s.config.EffectiveGameConfig(gameID)
	gameDir, dirErr := s.loaderGameDirLocked(gameID)
	base, backup, farmRoot, dataPending := "", "", "", false
	if mm, ok := s.mountMgrs[gameID]; ok {
		base = mm.DataPath()
		farmRoot = mm.DataPath()
		if mm.IsMounted() {
			base = mm.BackupPath()
		} else {
			backup = mm.BackupPath()
			dataPending = s.dataRecoveryPendingLocked(mm)
		}
	}
	s.mu.RUnlock()
	if cfgErr != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, cfgErr
	}
	if dirErr != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, dirErr
	}
	if base == "" {
		base = filepath.Join(gameDir, filepath.FromSlash(deploySubpath(gameID, gc)))
		farmRoot = base
	} else if backup != "" {
		if base, err = unmountedDeployBase(gameID, base, backup, dataPending); err != nil {
			return smapi.AnalyzeInput{}, projectionMeta{}, err
		}
	}
	_, entries, err := s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return smapi.AnalyzeInput{}, projectionMeta{}, fmt.Errorf("loading profile %q: %w", profileName, err)
	}

	meta := projectionMeta{loader: def, farmRoot: farmRoot}
	modsDir := config.ModsDir(gameID)
	guard := deployRootGuardFor(gameID)
	layers := []projectionLayer{{root: base}}
	var disabled []projectionLayer
	for _, e := range entries {
		if e.Name == profile.OverwriteModName || download.ValidateTargetModName(e.Name) != nil {
			continue
		}
		modDir := filepath.Join(modsDir, e.Name)
		if info, statErr := os.Stat(modDir); statErr != nil || !info.IsDir() {
			continue
		}
		if guard != nil && guard(modDir) != "" {
			meta.rootManifestMods = append(meta.rootManifestMods, e.Name)
			continue
		}
		layer := projectionLayer{root: modDir, provider: e.Name, mod: true}
		if e.Enabled {
			layers = append(layers, layer)
		} else {
			disabled = append(disabled, layer)
		}
	}
	layers = append(layers, projectionLayer{root: filepath.Join(modsDir, profile.OverwriteModName), provider: profile.OverwriteModName})

	in := smapi.AnalyzeInput{BundledIDs: append([]string(nil), spec.BundledModIDs...)}
	in.Active = projectActive(gameID, layers, &meta)
	for _, layer := range disabled {
		in.Disabled = append(in.Disabled, scanLayerProviders(gameID, layer, &meta)...)
	}
	in.LoaderVersion = s.projectedLoaderVersion(gameDir, spec)
	in.GameVersion = projectedGameVersion(gameDir, spec)
	meta.loaderVersion = versionText(in.LoaderVersion)
	meta.gameVersion = versionText(in.GameVersion)
	meta.rootManifestMods = uniqueSorted(meta.rootManifestMods)
	return in, meta, nil
}

// dataRecoveryPendingLocked reports whether a Data-farm recovery of mm's deploy folder awaits the user's confirmation; the caller holds s.mu.
func (s *session) dataRecoveryPendingLocked(mm *vfs.MountManager) bool {
	resolved, err := filepath.Abs(mm.DataPath())
	if err != nil {
		resolved = mm.DataPath()
	}
	s.pendingRecoveriesMu.Lock()
	defer s.pendingRecoveriesMu.Unlock()
	return s.pendingRecoveries[resolved] != nil
}

// unmountedDeployBase returns the vanilla base of an unmounted game: its deploy folder, or the deploy folder's backup while a crashed farm or a pending Data recovery leaves the deploy folder ambiguous, refusing a leftover farm without a backup with the step that resolves it.
func unmountedDeployBase(gameID, dataPath, backupPath string, dataPending bool) (string, error) {
	_, sentinelErr := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename))
	farmLeft := sentinelErr == nil
	if !farmLeft && !dataPending {
		return dataPath, nil
	}
	if info, err := os.Stat(backupPath); err == nil && info.IsDir() {
		return backupPath, nil
	}
	switch {
	case farmLeft && dataPending:
		return "", fmt.Errorf("recovery pending for %s: %s still holds a gorganizer farm without its vanilla backup — confirm the recovery via the GUI prompt or `gorganizerctl recover-confirm` first", gameID, dataPath)
	case farmLeft:
		return "", fmt.Errorf("%s of %s still holds a gorganizer farm without its vanilla backup and startup recovery could not restore it; restart gorganizer, or run `gorganizerctl recover --game %s` with the daemon stopped", dataPath, gameID, gameID)
	}
	return dataPath, nil
}

// deploySubpath returns the configured deploy folder of gameID, defaulting to the registry's.
func deploySubpath(gameID string, gc config.GameConfig) string {
	if gc.DataSubpath != "" {
		return gc.DataSubpath
	}
	if def, ok := gamedef.ByID(gameID); ok && def.DataSubpath != "" {
		return def.DataSubpath
	}
	return "Data"
}

// projectActive scans the merged view of the deployed layers the way SMAPI sees the farm, attributing each mod folder to the layer supplying its manifest and listing other layers' manifests it shadows.
func projectActive(gameID string, layers []projectionLayer, meta *projectionMeta) []smapi.ProjectedFolder {
	var intended []intendedMod
	for i, layer := range layers {
		for _, provider := range scanLayerProviders(gameID, layer, meta) {
			if provider.Manifest != nil {
				intended = append(intended, intendedMod{layer: i, folder: provider.Folder, manifest: provider.Manifest})
			}
		}
	}
	view := newFarmView(gameID, layers, meta.farmRoot)
	folders, err := smapi.ScanFS(view)
	if err != nil {
		slog.Warn("scanning the projected SMAPI Mods folder failed", "game", gameID, "err", err)
		return nil
	}
	var result []smapi.ProjectedFolder
	for _, folder := range folders {
		if folder.Kind != smapi.FolderMod || folder.RelPath == "" {
			continue
		}
		winner := view.layerOf(folder.ManifestPath)
		projected := smapi.ProjectedFolder{Provider: viewProvider(view, folder, winner)}
		projected.Competing = competingProviders(intended, layers, folder.RelPath, winner, folder.Manifest)
		result = append(result, projected)
	}
	return result
}

// scanLayerProviders returns one provider per SMAPI mod folder of a single layer seen through the farm's rules, recording a mod-root manifest in meta.
func scanLayerProviders(gameID string, layer projectionLayer, meta *projectionMeta) []smapi.Provider {
	view := newFarmView(gameID, []projectionLayer{layer}, meta.farmRoot)
	folders, err := smapi.ScanFS(view)
	if err != nil {
		slog.Warn("skipping an unreadable layer in the SMAPI dependency report", "game", gameID, "root", layer.root, "err", err)
		return nil
	}
	var providers []smapi.Provider
	for _, folder := range folders {
		switch {
		case folder.Kind == smapi.FolderRootManifest:
			if layer.mod {
				meta.rootManifestMods = append(meta.rootManifestMods, layer.provider)
			}
		case folder.Kind == smapi.FolderMod && folder.RelPath != "":
			providers = append(providers, viewProvider(view, folder, view.layerOf(folder.ManifestPath)))
		}
	}
	return providers
}

// viewProvider converts a scanned view folder into an analyzer provider supplied by layer, checking its entry DLL in the same view.
func viewProvider(view *farmView, folder smapi.Folder, layer int) smapi.Provider {
	provider := smapi.Provider{Folder: folder.RelPath, Manifest: folder.Manifest, ParseErr: folder.ParseErr}
	if layer >= 0 && layer < len(view.layers) {
		provider.Provider = view.layers[layer].provider
	}
	if folder.Manifest != nil && strings.TrimSpace(folder.Manifest.EntryDll) != "" {
		present := entryDllInView(view, folder.RelPath, folder.Manifest.EntryDll)
		provider.EntryDllPresent = &present
	}
	return provider
}

// entryDllInView reports whether a view folder holds entryDll as a regular file after following symlinks, preferring the exact name over a case-insensitive match.
func entryDllInView(fsys fs.FS, dir, entryDll string) bool {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return false
	}
	var match fs.DirEntry
	for _, entry := range entries {
		if entry.Name() == entryDll {
			match = entry
			break
		}
		if match == nil && strings.EqualFold(entry.Name(), entryDll) {
			match = entry
		}
	}
	return match != nil && match.Type().IsRegular()
}

// competingProviders lists the manifests other layers intended at or below a projected mod folder whose UniqueID differs from the winning manifest's.
func competingProviders(intended []intendedMod, layers []projectionLayer, folder string, winner int, manifest *smapi.Manifest) []smapi.Provider {
	winnerID := ""
	if manifest != nil {
		winnerID = manifest.UniqueID
	}
	key := vfs.NormalizePath(folder)
	var competing []smapi.Provider
	for _, mod := range intended {
		if mod.layer == winner {
			continue
		}
		modKey := vfs.NormalizePath(mod.folder)
		if modKey != key && !strings.HasPrefix(modKey, key+"/") {
			continue
		}
		if smapi.SameID(mod.manifest.UniqueID, winnerID) {
			continue
		}
		competing = append(competing, smapi.Provider{Folder: mod.folder, Provider: layers[mod.layer].provider, Manifest: mod.manifest})
	}
	return competing
}

// projectedLoaderVersion returns the loader version recorded by a managed install in gameDir, or nil when unknown.
func (s *session) projectedLoaderVersion(gameDir string, spec smapi.LoaderSpec) *smapi.Version {
	var status smapi.Status
	var err error
	if s.svc.modLoader != nil {
		status, err = s.svc.modLoader.engineFor(spec).Inspect(gameDir)
	} else {
		status, err = smapi.Inspect(gameDir, spec)
	}
	if err != nil || !status.Recorded || status.RecordedVersion == "" {
		return nil
	}
	version, err := smapi.ParseVersion(status.RecordedVersion, true)
	if err != nil {
		return nil
	}
	return &version
}

// projectedGameVersion reads the game build from its deps file, or nil when it cannot be determined.
func projectedGameVersion(gameDir string, spec smapi.LoaderSpec) *smapi.Version {
	if spec.GameVersionFile == "" || spec.GameVersionPackage == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(gameDir, filepath.FromSlash(spec.GameVersionFile)))
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, gameVersionFileMaxBytes+1))
	if err != nil || len(data) > gameVersionFileMaxBytes {
		return nil
	}
	version, err := smapi.GameVersionFromDeps(data, spec.GameVersionPackage)
	if err != nil {
		return nil
	}
	return &version
}

// versionText formats an optional version, empty when unknown.
func versionText(version *smapi.Version) string {
	if version == nil {
		return ""
	}
	return version.String()
}

// apiVersion returns the installed loader version sent to smapi.io, empty when it is unknown.
func (m projectionMeta) apiVersion() string {
	return m.loaderVersion
}

// GetModDependencyReport analyzes profileName's desired SMAPI deployment and merges smapi.io metadata and pending dependency enables.
func (md *ModDependencyService) GetModDependencyReport(ctx context.Context, gameID, profileName string, refreshRemote, forceRemote bool) (dto.ModDependencyReportResult, error) {
	in, meta, err := md.s.projectSMAPI(ctx, gameID, profileName)
	if err != nil {
		return dto.ModDependencyReportResult{}, err
	}
	analysis := smapi.Analyze(in)
	result := dto.ModDependencyReportResult{
		GameID:           gameID,
		ProfileName:      profileName,
		RootManifestMods: meta.rootManifestMods,
		LoaderVersion:    meta.loaderVersion,
		GameVersion:      meta.gameVersion,
	}
	manifests := manifestsByFolder(in.Active)
	request := lookupRequestFor(meta, analysis, manifests)
	infos := map[string]smapi.ModInfo{}
	switch {
	case len(request.Mods) == 0:
		result.RemoteChecked = refreshRemote
	case refreshRemote:
		request.Force = forceRemote
		fetched, lookupErr := md.webClient(meta.loader).Lookup(ctx, request)
		if fetched != nil {
			infos = fetched
		}
		if lookupErr != nil {
			result.RemoteError = lookupErr.Error()
		} else {
			result.RemoteChecked = true
		}
	default:
		infos = md.webClient(meta.loader).Cached(request)
	}
	slug := nexusSlug(gameID)
	for _, component := range analysis.Components {
		result.Components = append(result.Components, componentResult(component, manifests[vfs.NormalizePath(component.Folder)], infos))
	}
	for _, missing := range analysis.Missing {
		result.Missing = append(result.Missing, missingResult(missing, infos, slug))
	}
	result.PendingEnables, result.RecentFailures = md.requestSummary(gameID, profileName)
	return result, nil
}

// manifestsByFolder indexes the winning manifest of each projected folder by its case-folded path.
func manifestsByFolder(active []smapi.ProjectedFolder) map[string]*smapi.Manifest {
	result := make(map[string]*smapi.Manifest, len(active))
	for _, folder := range active {
		result[vfs.NormalizePath(folder.Folder)] = folder.Manifest
	}
	return result
}

// lookupRequestFor builds one smapi.io query for every non-bundled active component and every missing dependency.
func lookupRequestFor(meta projectionMeta, analysis smapi.Report, manifests map[string]*smapi.Manifest) smapi.LookupRequest {
	request := smapi.LookupRequest{APIVersion: meta.apiVersion(), GameVersion: meta.gameVersion}
	for _, component := range analysis.Components {
		if component.Bundled || idKey(component.UniqueID) == "" {
			continue
		}
		query := smapi.ModQuery{ID: component.UniqueID, InstalledVersion: component.Version}
		if manifest := manifests[vfs.NormalizePath(component.Folder)]; manifest != nil {
			query.UpdateKeys = append([]string(nil), manifest.UpdateKeys...)
		}
		request.Mods = append(request.Mods, query)
	}
	for _, missing := range analysis.Missing {
		request.Mods = append(request.Mods, smapi.ModQuery{ID: missing.UniqueID})
	}
	return request
}

// componentResult converts an analyzed component and merges its smapi.io metadata.
func componentResult(component smapi.ComponentResult, manifest *smapi.Manifest, infos map[string]smapi.ModInfo) dto.ModComponentResult {
	result := dto.ModComponentResult{
		Folder:      component.Folder,
		ProviderMod: component.Provider,
		UniqueID:    component.UniqueID,
		Name:        component.Name,
		Version:     component.Version,
		Kind:        componentKindResult(component.Kind),
		Bundled:     component.Bundled,
		Failed:      component.Failed,
	}
	for _, issue := range component.Issues {
		result.Issues = append(result.Issues, dto.ModIssueResult{
			Kind:            issueKindResult(issue.Kind),
			TargetID:        issue.TargetID,
			RequiredVersion: issue.RequiredVersion,
			FoundVersion:    issue.FoundVersion,
			Providers:       append([]string(nil), issue.Providers...),
			Detail:          issue.Detail,
		})
	}
	if key := idKey(component.UniqueID); key != "" {
		if info, ok := infos[key]; ok {
			result.NexusID = info.NexusID
			result.UpdateStale = info.Stale
			if info.SuggestedUpdateVersion != "" && updateIsNewer(info.SuggestedUpdateVersion, component.Version) {
				result.UpdateVersion = info.SuggestedUpdateVersion
				result.UpdateURL = httpsURL(info.SuggestedUpdateURL)
			}
		}
	}
	if result.NexusID <= 0 && manifest != nil {
		if ids := smapi.NexusIDs(manifest.UpdateKeys); len(ids) > 0 {
			result.NexusID = ids[0]
		}
	}
	return result
}

// missingResult converts an aggregated missing dependency and merges its smapi.io metadata.
func missingResult(missing smapi.MissingDependency, infos map[string]smapi.ModInfo, slug string) dto.MissingDependencyResult {
	result := dto.MissingDependencyResult{
		UniqueID:          missing.UniqueID,
		MinimumVersion:    missing.MinimumVersion,
		RequiredBy:        append([]string(nil), missing.RequiredBy...),
		DisabledProviders: append([]string(nil), missing.DisabledProviders...),
	}
	info, ok := infos[idKey(missing.UniqueID)]
	if !ok {
		return result
	}
	result.Name = info.Name
	result.NexusID = info.NexusID
	result.Stale = info.Stale
	if info.NexusID > 0 {
		result.URL = nexusModURL(slug, info.NexusID)
		result.Resolvable = true
	} else {
		result.URL = httpsURL(info.MainURL)
	}
	return result
}

// issueKindResult maps an analyzer issue kind to its wire value, unknown kinds to UNSPECIFIED.
func issueKindResult(kind smapi.IssueKind) dto.ModIssueKind {
	if result, ok := issueKindResults[kind]; ok {
		return result
	}
	return dto.ModIssueUnspecified
}

// componentKindResult maps a manifest kind to its wire value, unknown kinds to UNSPECIFIED.
func componentKindResult(kind smapi.ModKind) dto.ModComponentKind {
	if result, ok := componentKindResults[kind]; ok {
		return result
	}
	return dto.ModComponentUnspecified
}

// updateIsNewer reports whether a suggested update is newer than the installed version, trusting smapi.io when either does not parse.
func updateIsNewer(suggested, installed string) bool {
	next, err := smapi.ParseVersion(suggested, true)
	if err != nil {
		return true
	}
	current, err := smapi.ParseVersion(installed, true)
	if err != nil {
		return true
	}
	return next.IsNewerThan(current)
}

// httpsURL returns link when it is an https URL and "" otherwise.
func httpsURL(link string) string {
	if strings.HasPrefix(strings.ToLower(link), "https://") {
		return link
	}
	return ""
}

// nexusSlug returns the Nexus game domain of gameID, falling back to the game ID.
func nexusSlug(gameID string) string {
	if slug := download.GameSlug(gameID); slug != "" {
		return slug
	}
	return gameID
}

// nexusModURL returns the Nexus page of a mod.
func nexusModURL(slug string, modID int) string {
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", slug, modID)
}

// nexusFilesURL returns the files tab of a mod's Nexus page.
func nexusFilesURL(slug string, modID int) string {
	return nexusModURL(slug, modID) + "?tab=files"
}

// uniqueSorted returns values sorted without duplicates.
func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	result := sorted[:0]
	for _, value := range sorted {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// FetchModDependencies registers durable requests for missing dependencies, then queues Premium downloads or returns the Nexus pages to open.
func (md *ModDependencyService) FetchModDependencies(ctx context.Context, gameID, profileName string, uniqueIDs []string) ([]dto.DependencyFetchResult, error) {
	in, meta, err := md.s.projectSMAPI(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	results, pending := classifyFetchRequests(smapi.Analyze(in), in.BundledIDs, uniqueIDs)
	if len(pending) == 0 {
		return results, nil
	}
	infos, lookupErr := md.dependencyInfo(ctx, meta, results, pending)
	plans := groupFetchPlans(results, pending, infos, lookupErr != nil)
	if len(plans) == 0 {
		return results, nil
	}
	slug := nexusSlug(gameID)
	key, downloader := md.nexusAccess()
	if reason := unregisteredReason(key, downloader); reason != "" {
		for _, plan := range plans {
			for _, i := range plan.indices {
				results[i].Outcome = dto.FetchOutcomeOpenURL
				results[i].URL = nexusFilesURL(slug, plan.nexusID)
				results[i].Reason = reason
			}
		}
		return results, nil
	}
	md.chooseMainFiles(ctx, key, slug, plans)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	md.listPendingEnableMods(gameID, profileName, results, plans)
	batchID, err := md.registerBatch(gameID, profileName, results, plans, downloader)
	if err != nil {
		return nil, err
	}
	md.enqueuePlans(gameID, batchID, slug, downloader, plans)
	for _, plan := range plans {
		for _, i := range plan.indices {
			results[i].BatchID = batchID
			switch {
			case plan.pendingEnable[i] != "":
				results[i].Outcome = dto.FetchOutcomeAlreadyPresent
				results[i].Reason = fetchReasonPendingEnable + ": " + plan.pendingEnable[i]
			case plan.install != "":
				results[i].Outcome = dto.FetchOutcomeQueued
				results[i].DownloadID = plan.downloadID
				results[i].Reason = fetchReasonInstalling
			case plan.premium():
				results[i].Outcome = dto.FetchOutcomeQueued
				results[i].DownloadID = plan.downloadID
				if plan.attached {
					results[i].Reason = fetchReasonInFlight
				}
			default:
				results[i].Outcome = dto.FetchOutcomeOpenURL
				results[i].URL = nexusFilesURL(slug, plan.nexusID)
				results[i].Reason = plan.reason
			}
		}
	}
	return results, nil
}

// unregisteredReason explains why no request can complete without a Nexus API key and a download manager, or returns "".
func unregisteredReason(key string, downloader dependencyDownloader) string {
	switch {
	case key == "":
		return fetchReasonNoAPIKey
	case downloader == nil:
		return fetchReasonNoDownloads
	}
	return ""
}

// classifyFetchRequests dedupes the requested IDs and settles those that are not fetchable missing dependencies, returning the fetchable indices.
func classifyFetchRequests(analysis smapi.Report, bundledIDs, uniqueIDs []string) ([]dto.DependencyFetchResult, []int) {
	missing := make(map[string]smapi.MissingDependency, len(analysis.Missing))
	for _, dep := range analysis.Missing {
		missing[idKey(dep.UniqueID)] = dep
	}
	bundled := make(map[string]bool, len(bundledIDs))
	for _, id := range bundledIDs {
		bundled[idKey(id)] = true
	}
	present := map[string]string{}
	for _, component := range analysis.Components {
		key := idKey(component.UniqueID)
		if _, seen := present[key]; key == "" || seen {
			continue
		}
		present[key] = component.Provider
		if component.Provider == "" {
			present[key] = component.Folder
		}
	}
	seen := map[string]bool{}
	var results []dto.DependencyFetchResult
	var pending []int
	for _, raw := range uniqueIDs {
		id := strings.TrimSpace(raw)
		key := idKey(id)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result := dto.DependencyFetchResult{UniqueID: id}
		dep, isMissing := missing[key]
		provider, isPresent := present[key]
		switch {
		case isMissing && len(dep.DisabledProviders) > 0:
			result.Outcome = dto.FetchOutcomeAlreadyPresent
			result.Reason = fetchReasonDisabled + ": " + strings.Join(dep.DisabledProviders, ", ")
		case isMissing && bundled[key]:
			result.Outcome = dto.FetchOutcomeUnresolved
			result.Reason = fetchReasonBundled + ": reinstall or repair the mod loader to restore it"
		case isMissing:
			pending = append(pending, len(results))
		case isPresent:
			result.Outcome = dto.FetchOutcomeAlreadyPresent
			result.Reason = fetchReasonProvided + ": " + provider
		default:
			result.Outcome = dto.FetchOutcomeUnresolved
			result.Reason = fetchReasonNotMissing
		}
		results = append(results, result)
	}
	return results, pending
}

// dependencyInfo returns smapi.io metadata for the fetchable IDs, looking up every ID whose cached answer is absent, stale, unknown or without a Nexus page.
func (md *ModDependencyService) dependencyInfo(ctx context.Context, meta projectionMeta, results []dto.DependencyFetchResult, pending []int) (map[string]smapi.ModInfo, error) {
	request := smapi.LookupRequest{APIVersion: meta.apiVersion(), GameVersion: meta.gameVersion}
	for _, i := range pending {
		request.Mods = append(request.Mods, smapi.ModQuery{ID: results[i].UniqueID})
	}
	client := md.webClient(meta.loader)
	infos := client.Cached(request)
	absent := smapi.LookupRequest{APIVersion: request.APIVersion, GameVersion: request.GameVersion}
	for _, query := range request.Mods {
		if info, ok := infos[idKey(query.ID)]; !ok || info.Stale || !info.Known || info.NexusID <= 0 {
			absent.Mods = append(absent.Mods, query)
		}
	}
	if len(absent.Mods) == 0 {
		return infos, nil
	}
	fetched, err := client.Lookup(ctx, absent)
	if err != nil {
		slog.Warn("looking up missing SMAPI dependencies on smapi.io failed", "err", err)
	}
	for id, info := range fetched {
		infos[id] = info
	}
	return infos, err
}

// groupFetchPlans groups the fetchable IDs by Nexus mod, settling IDs without a known Nexus page as unresolved.
func groupFetchPlans(results []dto.DependencyFetchResult, pending []int, infos map[string]smapi.ModInfo, lookupFailed bool) []*fetchPlan {
	byNexus := map[int]*fetchPlan{}
	var plans []*fetchPlan
	for _, i := range pending {
		info := infos[idKey(results[i].UniqueID)]
		if info.NexusID <= 0 {
			results[i].Outcome = dto.FetchOutcomeUnresolved
			results[i].Reason = fetchReasonNoNexusPage
			if lookupFailed {
				results[i].Reason = fetchReasonLookupFailed
			}
			results[i].URL = httpsURL(info.MainURL)
			continue
		}
		plan := byNexus[info.NexusID]
		if plan == nil {
			plan = &fetchPlan{nexusID: info.NexusID}
			byNexus[info.NexusID] = plan
			plans = append(plans, plan)
		}
		plan.indices = append(plan.indices, i)
	}
	sort.Slice(plans, func(a, b int) bool { return plans[a].nexusID < plans[b].nexusID })
	return plans
}

// chooseMainFiles picks the unambiguous MAIN file of each planned Nexus mod for Premium accounts and records why others need the browser.
func (md *ModDependencyService) chooseMainFiles(ctx context.Context, key, slug string, plans []*fetchPlan) {
	reason := ""
	premium, err := md.s.nexusPremium(ctx, key)
	switch {
	case err != nil:
		slog.Warn("checking Nexus Premium for a dependency fetch failed", "err", err)
		reason = fetchReasonPremiumCheck
	case !premium:
		reason = fetchReasonNotPremium
	}
	for _, plan := range plans {
		if reason != "" {
			plan.reason = reason
			continue
		}
		list, err := md.nexusFiles(key).ListModFilesContext(ctx, slug, plan.nexusID)
		if err != nil || list == nil {
			slog.Warn("listing Nexus files for a dependency failed", "mod", plan.nexusID, "err", err)
			plan.reason = fetchReasonFileList
			continue
		}
		chosen, err := download.SelectMainFile(list.Files, download.MainFileOptions{FailOnAmbiguous: true})
		switch {
		case errors.Is(err, download.ErrAmbiguousMainFile):
			plan.reason = fetchReasonAmbiguousMain
		case err != nil:
			plan.reason = fetchReasonNoMainFile
		default:
			plan.fileID = chosen.FileID
		}
	}
}

// premium reports whether the plan's entries wait on a queued or in-flight download rather than the browser.
func (p *fetchPlan) premium() bool {
	return p.fileID > 0 && !p.queueFailed
}

// needsDownload reports whether the plan must queue a fresh download for at least one of its IDs.
func (p *fetchPlan) needsDownload() bool {
	return p.fileID > 0 && !p.attached && p.install == "" && len(p.pendingEnable) < len(p.indices)
}

// listPendingEnableMods appends, disabled, to profileName's modlist every installed mod whose pending enable the planned requests will attach to, before they are registered, so the profile can enable it and its enable never counts as outside the modlist.
func (md *ModDependencyService) listPendingEnableMods(gameID, profileName string, results []dto.DependencyFetchResult, plans []*fetchPlan) {
	var mods []string
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, _ time.Time) bool {
		for _, plan := range plans {
			for _, i := range plan.indices {
				if mod := doc.pendingEnableMod(idKey(results[i].UniqueID)); mod != "" && modFolderPresent(gameID, mod) {
					mods = append(mods, mod)
				}
			}
		}
		return false
	})
	if err != nil {
		slog.Warn("reading pending dependency enables before a fetch failed", "game", gameID, "err", err)
		return
	}
	for _, mod := range uniqueSorted(mods) {
		if err := md.s.svc.mods.appendToProfileModList(gameID, profileName, mod); err != nil {
			slog.Warn("adding an installed dependency to the profile's mod list failed", "game", gameID, "profile", profileName, "mod", mod, "err", err)
		}
	}
}

// registerBatch durably records one request batch for the planned IDs before any download is queued, attaching them to in-flight work where possible.
func (md *ModDependencyService) registerBatch(gameID, profileName string, results []dto.DependencyFetchResult, plans []*fetchPlan, downloader dependencyDownloader) (string, error) {
	batchID := "dep-" + uuid.NewString()
	ledger := &ledgerSnapshot{gameID: gameID}
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		batch := depBatch{BatchID: batchID, Profile: profileName, CreatedAt: now.UTC()}
		for _, plan := range plans {
			md.attachPlan(gameID, doc, plan, results, downloader, ledger, now)
			for _, i := range plan.indices {
				entry := depEntry{UniqueID: results[i].UniqueID, NexusModID: plan.nexusID}
				switch {
				case plan.pendingEnable[i] != "":
					entry.ModName = plan.pendingEnable[i]
					entry.set(depStateEnablePending, "", now)
				case plan.install != "":
					entry.Install = plan.install
					entry.DownloadID = plan.downloadID
					entry.FileID = plan.installFileID
					entry.Archive = plan.installArchive
					entry.set(depStateInstalling, "", now)
				case plan.fileID > 0:
					entry.FileID = plan.fileID
					entry.DownloadID = plan.downloadID
					entry.set(depStateDownloading, "", now)
				default:
					awaitBrowser(&entry, plan.reason, now)
				}
				batch.Entries = append(batch.Entries, entry)
			}
		}
		doc.Batches = append(doc.Batches, batch)
		return true
	})
	if err != nil {
		return "", fmt.Errorf("registering dependency requests: %w", err)
	}
	return batchID, nil
}

// attachPlan points a plan at an existing pending enable, install or live download of the same dependency, falls back to the browser after a mismatched Premium file, and marks dead downloads it supersedes.
func (md *ModDependencyService) attachPlan(gameID string, doc *depRequestsDoc, plan *fetchPlan, results []dto.DependencyFetchResult, downloader dependencyDownloader, ledger *ledgerSnapshot, now time.Time) {
	wanted := map[string]bool{}
	for _, i := range plan.indices {
		key := idKey(results[i].UniqueID)
		if mod := doc.pendingEnableMod(key); mod != "" && modFolderPresent(gameID, mod) {
			if plan.pendingEnable == nil {
				plan.pendingEnable = map[int]string{}
			}
			plan.pendingEnable[i] = mod
			continue
		}
		wanted[key] = true
	}
	if len(wanted) == 0 {
		return
	}
	var liveID string
	pendingID := false
	doc.each(func(batch *depBatch, entry *depEntry) {
		if entry.NexusModID != plan.nexusID {
			return
		}
		switch entry.State {
		case depStateInstalling:
			if plan.install == "" && entry.Install != "" {
				plan.install = entry.Install
				plan.installFileID = entry.FileID
				plan.installArchive = entry.Archive
				plan.downloadID = entry.DownloadID
			}
		case depStateDownloading:
			if plan.fileID <= 0 || entry.FileID != plan.fileID {
				return
			}
			switch {
			case entry.DownloadID == "" && now.Sub(entry.changedAt(*batch)) < pendingDownloadGrace:
				pendingID = true
			case entry.DownloadID != "" && liveID == "" && md.downloadLive(gameID, entry.DownloadID, downloader, ledger):
				liveID = entry.DownloadID
			case entry.DownloadID != "" && entry.DownloadID != liveID:
				if plan.stale == nil {
					plan.stale = map[string]bool{}
				}
				plan.stale[entry.DownloadID] = true
			}
		case depStateFailed:
			if plan.fileID > 0 && entry.FileID == plan.fileID && wanted[idKey(entry.UniqueID)] && strings.HasPrefix(entry.Detail, detailArchiveMismatch) {
				plan.fileID = 0
				plan.reason = fetchReasonPremiumMismatch
			}
		}
	})
	switch {
	case plan.install != "":
	case plan.fileID <= 0:
	case liveID != "":
		plan.downloadID = liveID
		plan.attached = true
	case pendingID:
		plan.attached = true
	}
	delete(plan.stale, liveID)
}

// pendingEnableMod returns the installed mod of an unacknowledged request for the UniqueID key, or "".
func (doc *depRequestsDoc) pendingEnableMod(key string) string {
	mod := ""
	doc.each(func(_ *depBatch, entry *depEntry) {
		if mod == "" && entry.State == depStateEnablePending && entry.ModName != "" && idKey(entry.UniqueID) == key {
			mod = entry.ModName
		}
	})
	return mod
}

// awaitBrowser turns entry into a browser request that expires after browserRequestTTL.
func awaitBrowser(entry *depEntry, reason string, now time.Time) {
	expires := now.Add(browserRequestTTL).UTC()
	entry.ExpiresAt = &expires
	entry.set(depStateAwaitingDownload, reason, now)
}

// enqueuePlans queues the Premium downloads of registered plans and records their IDs on every entry waiting on that file, falling back to the browser when queueing fails.
func (md *ModDependencyService) enqueuePlans(gameID, batchID, slug string, downloader dependencyDownloader, plans []*fetchPlan) {
	started := false
	for _, plan := range plans {
		if !plan.needsDownload() {
			continue
		}
		uri := fmt.Sprintf("nxm://%s/mods/%d/files/%d", slug, plan.nexusID, plan.fileID)
		id, _, err := downloader.StartDownloadForGame(uri, gameID)
		if err != nil {
			slog.Warn("queueing a dependency download failed", "game", gameID, "mod", plan.nexusID, "err", err)
			plan.queueFailed = true
			plan.reason = fetchReasonQueueFailed
		} else {
			plan.downloadID = id
		}
		plan.queued = true
		started = true
	}
	if !started {
		return
	}
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		for _, plan := range plans {
			if plan.queued {
				md.recordQueuedPlan(gameID, doc, batchID, plan, now)
			}
		}
		return true
	})
	if err != nil {
		slog.Warn("recording dependency download IDs failed", "game", gameID, "err", err)
	}
}

// recordQueuedPlan records a queued plan's download ID on its entries and on entries of the same file whose download is pending or dead, then applies a failure that raced ahead.
func (md *ModDependencyService) recordQueuedPlan(gameID string, doc *depRequestsDoc, batchID string, plan *fetchPlan, now time.Time) {
	doc.each(func(batch *depBatch, entry *depEntry) {
		if entry.State != depStateDownloading || entry.NexusModID != plan.nexusID || entry.FileID != plan.fileID {
			return
		}
		own := batch.BatchID == batchID
		if !own && entry.DownloadID != "" && !plan.stale[entry.DownloadID] {
			return
		}
		if plan.queueFailed {
			if own || entry.DownloadID == "" {
				awaitBrowser(entry, plan.reason, now)
			}
			return
		}
		entry.DownloadID = plan.downloadID
		stamp := now.UTC()
		entry.UpdatedAt = &stamp
	})
	if plan.queueFailed {
		return
	}
	detail, failed := doc.failedDownload(plan.downloadID)
	if outcome, known := md.outcomes.lookup(gameID, plan.downloadID); known && !outcome.landed {
		detail, failed = outcome.detail, true
	}
	if failed {
		doc.failDownloadEntries(plan.downloadID, detail, now)
	}
}

// AckDependencyEnable marks the batch's pending enables for uniqueIDs (all when empty) done and returns how many changed.
func (md *ModDependencyService) AckDependencyEnable(_ context.Context, gameID, batchID string, uniqueIDs []string) (int, error) {
	if _, _, err := dependencySpecFor(gameID); err != nil {
		return 0, err
	}
	md.s.mu.RLock()
	_, configured := md.s.config.Games[gameID]
	md.s.mu.RUnlock()
	if !configured {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	wanted := map[string]bool{}
	for _, id := range uniqueIDs {
		if key := idKey(id); key != "" {
			wanted[key] = true
		}
	}
	acknowledged := 0
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, _ time.Time) bool {
		for bi := range doc.Batches {
			if batchID == "" || doc.Batches[bi].BatchID != batchID {
				continue
			}
			for ei := range doc.Batches[bi].Entries {
				entry := &doc.Batches[bi].Entries[ei]
				if entry.State != depStateEnablePending || len(wanted) > 0 && !wanted[idKey(entry.UniqueID)] {
					continue
				}
				entry.State = depStateDone
				acknowledged++
			}
		}
		return acknowledged > 0
	})
	if err != nil {
		return 0, err
	}
	return acknowledged, nil
}
