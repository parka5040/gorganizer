package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
)

// seManifestFilename is the name of the per-game manifest dropped into the
// game's install directory at extender install time. Lists every file the
// extender installer copied, with its sha256 so we can detect a Steam game
// update that wiped or mutated extender files.
const seManifestFilename = ".gorganizer-script-extender.manifest"

// seManifestEntry records one file from the installed extender tree.
type seManifestEntry struct {
	RelPath string // relative to game InstallPath
	Size    int64
	SHA256  string
}

// seInstallManifest is the on-disk record of what an extender install
// dropped into the game dir. On launch we re-hash each listed file and
// compare — any drift means a Steam update (or manual tamper) has
// potentially broken the extender, and the user should reinstall rather
// than launch into the vanilla game.
type seInstallManifest struct {
	GameID          string
	ExtenderName    string
	InstalledAtUTC  time.Time
	SteamLastUpdate int64 // from appmanifest_<appid>.acf at install time
	Entries         []seManifestEntry
}

// ScriptExtenderDef describes where to pull a script extender from plus
// the post-extraction layout. Two source flavors are supported: a public
// GitHub releases page (preferred — no account, no API key, works for
// every user out of the box) and a Nexus mod page (fallback for
// extenders whose upstream doesn't publish GitHub releases).
//
// DataSubdirs are directory names the installer creates under the game's
// Data/ folder so plugin DLLs have a home even before the user drops
// any in.
type ScriptExtenderDef struct {
	Name        string // user-facing label, e.g. "xNVSE"
	LoaderExe   string // e.g. "nvse_loader.exe" — what tools.DetectTool looks for
	DataSubdirs []string

	// GitHub release source. When set, the daemon uses the public
	// /releases/latest REST endpoint — no authentication, no git client,
	// no Nexus account required. AssetSuffix filters which release asset
	// to download (xNVSE ships its loader as a .7z archive).
	GitHubRepo  string // "owner/repo", e.g. "xNVSE/NVSE"
	AssetSuffix string // e.g. ".7z" — matches case-insensitively

	// Nexus fallback fields (used only when GitHubRepo is empty).
	GameSlug string // Nexus domain, e.g. "newvegas"
	ModID    int
}

// KnownScriptExtenders covers the games the user listed as Linux-compat on
// Bethesda titles. xNVSE is resolved from its public GitHub release page
// (xNVSE/NVSE) so non-premium users don't need a Nexus account for the
// one extender whose upstream publishes GitHub builds. The others still
// resolve via Nexus because that's where their canonical builds live.
var KnownScriptExtenders = map[string]ScriptExtenderDef{
	"fallout3": {
		Name: "FOSE", GameSlug: "fallout3", ModID: 8606,
		LoaderExe: "fose_loader.exe", DataSubdirs: []string{"FOSE"},
	},
	"falloutnv": {
		Name:        "xNVSE",
		GitHubRepo:  "xNVSE/NVSE",
		AssetSuffix: ".7z",
		LoaderExe:   "nvse_loader.exe",
		DataSubdirs: []string{"NVSE"},
	},
	"skyrimse": {
		Name: "SKSE64", GameSlug: "skyrimspecialedition", ModID: 30379,
		LoaderExe: "skse64_loader.exe", DataSubdirs: []string{"SKSE"},
	},
	"fallout4": {
		Name: "F4SE", GameSlug: "fallout4", ModID: 42147,
		LoaderExe: "f4se_loader.exe", DataSubdirs: []string{"F4SE"},
	},
}

// gitHubReleaseAsset is the subset of the GitHub Releases REST payload
// we need — asset filename and its direct download URL.
type gitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// gitHubRelease mirrors the top-level shape returned by
// /repos/{owner}/{repo}/releases/latest. TagName gets logged so the
// installed version is traceable.
type gitHubRelease struct {
	TagName string               `json:"tag_name"`
	Name    string               `json:"name"`
	Assets  []gitHubReleaseAsset `json:"assets"`
}

// fetchLatestGitHubRelease grabs a repo's latest (non-draft, non-
// prerelease) release from the GitHub REST API and downloads the first
// asset whose filename ends in suffix (case-insensitive). The archive
// is written to destDir and its local path returned alongside the
// release tag.
//
// No authentication is used — the 60 req/hr/IP unauthenticated quota is
// plenty for the "install once per game" workflow, and requiring auth
// would put a git/GitHub prerequisite in the way of first-time users,
// which is exactly what we're trying to avoid.
func fetchLatestGitHubRelease(repo, suffix, destDir string) (archivePath, version string, err error) {
	if repo == "" {
		return "", "", errors.New("empty repo")
	}
	if suffix == "" {
		suffix = ".7z"
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	// GitHub requires a User-Agent on every REST call; omitting it gets
	// a 403 with no useful body. The Accept header pins the v3 schema.
	req.Header.Set("User-Agent", "gorganizer")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GET %s: %w", apiURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("GitHub API %s: HTTP %d: %s",
			apiURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel gitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("decoding release payload: %w", err)
	}

	suffixLower := strings.ToLower(suffix)
	var chosen *gitHubReleaseAsset
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if strings.HasSuffix(strings.ToLower(a.Name), suffixLower) {
			chosen = a
			break
		}
	}
	if chosen == nil {
		return "", "", fmt.Errorf("no %q asset in release %s", suffix, rel.TagName)
	}

	archivePath = filepath.Join(destDir, chosen.Name)
	if err := streamTo(chosen.BrowserDownloadURL, archivePath); err != nil {
		return "", "", fmt.Errorf("downloading %s: %w", chosen.Name, err)
	}
	tag := rel.TagName
	if tag == "" {
		tag = rel.Name
	}
	return archivePath, tag, nil
}

// InstallScriptExtender resolves the latest build of the game's script
// extender, downloads it, and extracts the archive into the game's
// install directory (next to the exe — NOT Data/). The extender's own
// plugin subfolder (SKSE/NVSE/FOSE/F4SE) inside Data/ is created so
// user-installed plugins have a home.
//
// xNVSE ships public GitHub releases, so its build is pulled directly
// from api.github.com — no Nexus account, no API key, no git client
// required. The other extenders still resolve via Nexus until their
// upstreams publish comparable release feeds.
func (d *Daemon) InstallScriptExtender(gameID string) (string, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	def, ok := KnownScriptExtenders[gameID]
	if !ok {
		return "", fmt.Errorf("no known script extender for %q", gameID)
	}

	tmpDir, err := os.MkdirTemp("", "gorganizer-se-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	var archivePath, versionLabel string
	if def.GitHubRepo != "" {
		archivePath, versionLabel, err = fetchLatestGitHubRelease(def.GitHubRepo, def.AssetSuffix, tmpDir)
		if err != nil {
			return "", fmt.Errorf("fetching %s from GitHub: %w", def.Name, err)
		}
	} else {
		archivePath, versionLabel, err = fetchLatestFromNexus(d.config.NexusAPIKey, def, tmpDir)
		if err != nil {
			return "", err
		}
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return "", err
	}
	extractor, err := download.DetectExtractor(archivePath)
	if err != nil {
		return "", fmt.Errorf("detecting archive: %w", err)
	}
	if err := extractor.Extract(archivePath, extractDir); err != nil {
		return "", fmt.Errorf("extracting: %w", err)
	}

	// Find the directory that holds the loader exe — the archive usually
	// wraps everything in a versioned folder (e.g. xnvse_6_4_7/…).
	srcRoot, err := findExtenderRoot(extractDir, def.LoaderExe)
	if err != nil {
		return "", err
	}
	if err := copyTree(srcRoot, gc.InstallPath); err != nil {
		return "", fmt.Errorf("copying to install dir: %w", err)
	}

	// Ensure Data/{SKSE,NVSE,FOSE,F4SE} exists so plugin DLLs land
	// somewhere sensible even before the user downloads any.
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	for _, sub := range def.DataSubdirs {
		_ = os.MkdirAll(filepath.Join(gc.InstallPath, subpath, sub), 0755)
	}

	// Record the tool on the game config so the Run button can find it
	// without another filesystem probe.
	gc.Tool = strings.ToLower(def.Name)
	gc.ToolExe = def.LoaderExe
	d.config.Games[gameID] = gc
	if err := d.config.Save(); err != nil {
		slog.Warn("saving script extender tool config failed", "err", err)
	}

	// Write the install manifest. If this fails we still consider the
	// install successful — the manifest is diagnostic, not essential —
	// but log so users can spot the drift-detection blind spot.
	if err := writeScriptExtenderManifest(gameID, def.Name, srcRoot, gc.InstallPath); err != nil {
		slog.Warn("writing script-extender manifest failed — Steam-update drift detection will be disabled",
			"game", gameID, "err", err)
	}

	slog.Info("script extender installed",
		"game", gameID, "name", def.Name,
		"version", versionLabel,
		"destination", gc.InstallPath)

	// Best-effort: make sure the Proton prefix has the DX9 and VC++
	// runtimes the Bethesda DX9 engines rely on (heavy mod loadouts
	// crash without them). Runs asynchronously against the shared
	// prefix; a missing protontricks is surfaced as a warning rather
	// than a hard failure so install still succeeds.
	go d.ensurePrefixRuntime(gameID, gc)

	return def.Name, nil
}

// fetchLatestFromNexus pulls the newest MAIN-category file for a Nexus-
// hosted script extender into destDir. Kept as a dedicated path (not the
// default anymore) because SKSE64, F4SE, and FOSE don't publish to GitHub
// and Nexus is still the canonical source for those builds.
func fetchLatestFromNexus(apiKey string, def ScriptExtenderDef, destDir string) (archivePath, version string, err error) {
	if apiKey == "" {
		return "", "", fmt.Errorf("nexus API key required for %s — paste one in Tools → Settings", def.Name)
	}
	nx := download.NewNexusClient(apiKey)

	files, err := nx.ListModFiles(def.GameSlug, def.ModID)
	if err != nil {
		return "", "", fmt.Errorf("listing %s files: %w", def.Name, err)
	}
	var chosen *download.NexusFileDetails
	for i := range files.Files {
		f := &files.Files[i]
		if !strings.EqualFold(f.CategoryName, "MAIN") {
			continue
		}
		if chosen == nil || f.FileID > chosen.FileID {
			chosen = f
		}
	}
	if chosen == nil {
		return "", "", fmt.Errorf("no MAIN-category file found for %s", def.Name)
	}
	cdnURL, err := nx.ResolveDownloadURLByID(def.GameSlug, def.ModID, chosen.FileID)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w (if non-premium, click Download with Manager on the Nexus page)",
			def.Name, err)
	}

	archivePath = filepath.Join(destDir, chosen.FileName)
	if archivePath == filepath.Join(destDir, "") {
		archivePath = filepath.Join(destDir, fmt.Sprintf("%s-%d.archive", def.Name, chosen.FileID))
	}
	if err := streamTo(cdnURL, archivePath); err != nil {
		return "", "", fmt.Errorf("downloading %s: %w", def.Name, err)
	}
	return archivePath, chosen.Version, nil
}

// writeScriptExtenderManifest records the sha256 of every file the extender
// installer placed under installPath. Walks `srcRoot` (the extractor's
// output root) to know exactly which files to track — walking installPath
// would also pick up game files we don't own.
func writeScriptExtenderManifest(gameID, extenderName, srcRoot, installPath string) error {
	manifest := seInstallManifest{
		GameID:         gameID,
		ExtenderName:   extenderName,
		InstalledAtUTC: time.Now().UTC(),
	}

	var entries []seManifestEntry
	walkErr := filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(installPath, rel)
		sum, size, hashErr := hashFile(target)
		if hashErr != nil {
			slog.Warn("manifest: could not hash installed file", "path", target, "err", hashErr)
			return nil // best-effort
		}
		entries = append(entries, seManifestEntry{
			RelPath: filepath.ToSlash(rel),
			Size:    size,
			SHA256:  sum,
		})
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walking extender source: %w", walkErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	manifest.Entries = entries

	return saveScriptExtenderManifest(installPath, manifest)
}

// hashFile returns (sha256-hex, size, error) for a single file. Errors are
// returned verbatim so the caller can decide fatal-vs-warn.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// saveScriptExtenderManifest writes a manifest as a minimal one-line-per-
// entry text file so it's both human-readable and trivial to parse back
// without pulling in a YAML dep. Format:
//
//	# game: falloutnv
//	# extender: xNVSE
//	# installed_at: 2026-04-23T...Z
//	<sha256>  <size>  <relpath>
//	...
func saveScriptExtenderManifest(installPath string, m seInstallManifest) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# game: %s\n", m.GameID)
	fmt.Fprintf(&b, "# extender: %s\n", m.ExtenderName)
	fmt.Fprintf(&b, "# installed_at: %s\n", m.InstalledAtUTC.Format(time.RFC3339Nano))
	for _, e := range m.Entries {
		fmt.Fprintf(&b, "%s\t%d\t%s\n", e.SHA256, e.Size, e.RelPath)
	}
	target := filepath.Join(installPath, seManifestFilename)
	return os.WriteFile(target, []byte(b.String()), 0644)
}

// loadScriptExtenderManifest reads the manifest from a game dir. Returns an
// empty manifest (no error) when the file doesn't exist — a game that was
// modded by an older gorganizer build has no manifest yet, and we degrade
// gracefully instead of refusing to launch.
func loadScriptExtenderManifest(installPath string) (*seInstallManifest, error) {
	path := filepath.Join(installPath, seManifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &seInstallManifest{}, nil
		}
		return nil, err
	}
	m := &seInstallManifest{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# game:") {
			m.GameID = strings.TrimSpace(strings.TrimPrefix(line, "# game:"))
			continue
		}
		if strings.HasPrefix(line, "# extender:") {
			m.ExtenderName = strings.TrimSpace(strings.TrimPrefix(line, "# extender:"))
			continue
		}
		if strings.HasPrefix(line, "# installed_at:") {
			ts := strings.TrimSpace(strings.TrimPrefix(line, "# installed_at:"))
			if t, parseErr := time.Parse(time.RFC3339Nano, ts); parseErr == nil {
				m.InstalledAtUTC = t
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		var size int64
		fmt.Sscanf(parts[1], "%d", &size)
		m.Entries = append(m.Entries, seManifestEntry{
			SHA256:  parts[0],
			Size:    size,
			RelPath: parts[2],
		})
	}
	return m, nil
}

// VerifyScriptExtenderManifest re-hashes every file in the manifest and
// returns the list of entries whose sha256 no longer matches (missing
// counts as a mismatch). An empty slice means the install is intact.
// Missing manifest (pre-existing install) is also returned as OK so we
// don't block users who installed before this check existed.
//
// Path resolution is case-insensitive component-wise: the manifest may
// record `Data/NVSE/nvse_config.ini` but the active VFS view (assembled
// from mods that pack `nvse/` lowercase) presents `Data/nvse/nvse_config.ini`.
// On Linux that's a different file by `os.Stat` rules, but at runtime Wine
// treats them as the same. Resolving case-insensitively keeps the manifest
// check honest about content drift without flagging case-only differences
// as "modified".
func VerifyScriptExtenderManifest(installPath string) ([]string, error) {
	m, err := loadScriptExtenderManifest(installPath)
	if err != nil {
		return nil, err
	}
	if len(m.Entries) == 0 {
		return nil, nil
	}
	var drifted []string
	for _, e := range m.Entries {
		resolved := resolvePathCaseInsensitive(installPath, filepath.FromSlash(e.RelPath))
		sum, size, hashErr := hashFile(resolved)
		if hashErr != nil || sum != e.SHA256 || size != e.Size {
			drifted = append(drifted, e.RelPath)
		}
	}
	return drifted, nil
}

// resolvePathCaseInsensitive walks `relPath` against `base` one component
// at a time, matching each segment case-insensitively against the actual
// directory entries. Returns the path with on-disk casing if every segment
// matched; otherwise returns the unmodified `base/relPath` (so the caller's
// hashFile produces a normal "missing" error rather than a different one).
func resolvePathCaseInsensitive(base, relPath string) string {
	parts := strings.Split(relPath, string(filepath.Separator))
	cur := base
	for _, want := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return filepath.Join(base, relPath)
		}
		matched := ""
		for _, ent := range entries {
			if strings.EqualFold(ent.Name(), want) {
				matched = ent.Name()
				break
			}
		}
		if matched == "" {
			return filepath.Join(base, relPath)
		}
		cur = filepath.Join(cur, matched)
	}
	return cur
}

// streamTo does a plain HTTP GET into `path`, following redirects, with a
// generous timeout so slower CDNs don't fail mid-archive.
func streamTo(url, path string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// findExtenderRoot locates the directory containing the loader exe inside
// the extracted archive. Walks at most two levels deep — every shipped
// script extender archive either puts the loader at root or inside a
// single version-named wrapper.
func findExtenderRoot(extractDir, loaderExe string) (string, error) {
	if _, err := os.Stat(filepath.Join(extractDir, loaderExe)); err == nil {
		return extractDir, nil
	}
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner := filepath.Join(extractDir, e.Name())
		if _, err := os.Stat(filepath.Join(inner, loaderExe)); err == nil {
			return inner, nil
		}
	}
	return "", errors.New("loader exe not found in extracted archive")
}

// copyTree walks src and copies every file into dst, preserving relative
// paths. Overwrites existing files — script extender updates are a full
// replacement of prior versions.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode())
	})
}
