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

	"github.com/parka/gorganizer/internal/download"
)

const seManifestFilename = ".gorganizer-script-extender.manifest"

// seManifestEntry records one file from the installed extender tree.
type seManifestEntry struct {
	RelPath string
	Size    int64
	SHA256  string
}

// seInstallManifest is the on-disk record of what an extender install
type seInstallManifest struct {
	GameID          string
	ExtenderName    string
	InstalledAtUTC  time.Time
	SteamLastUpdate int64
	Entries         []seManifestEntry
}

// ScriptExtenderDef describes where to pull a script extender from plus
// the post-extraction layout. Two source flavors are supported: a public
type ScriptExtenderDef struct {
	Name        string
	LoaderExe   string
	DataSubdirs []string

	GitHubRepo  string
	AssetSuffix string

	GameSlug string
	ModID    int
}

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
	"ttw": {
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
type gitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type gitHubRelease struct {
	TagName string               `json:"tag_name"`
	Name    string               `json:"name"`
	Assets  []gitHubReleaseAsset `json:"assets"`
}

// fetchLatestGitHubRelease grabs a repo's latest (non-draft, non-
// prerelease) release from the GitHub REST API and downloads the first
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
func (d *Daemon) InstallScriptExtender(gameID string) (string, error) {
	gc, err := d.config.EffectiveGameConfig(gameID)
	if err != nil {
		return "", err
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

	srcRoot, err := findExtenderRoot(extractDir, def.LoaderExe)
	if err != nil {
		return "", err
	}
	if err := copyTree(srcRoot, gc.InstallPath); err != nil {
		return "", fmt.Errorf("copying to install dir: %w", err)
	}

	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	for _, sub := range def.DataSubdirs {
		_ = os.MkdirAll(filepath.Join(gc.InstallPath, subpath, sub), 0755)
	}

	saved := d.config.Games[gameID]
	saved.Tool = strings.ToLower(def.Name)
	saved.ToolExe = def.LoaderExe
	d.config.Games[gameID] = saved
	if err := d.config.Save(); err != nil {
		slog.Warn("saving script extender tool config failed", "err", err)
	}

	if err := writeScriptExtenderManifest(gameID, def.Name, srcRoot, gc.InstallPath); err != nil {
		slog.Warn("writing script-extender manifest failed — Steam-update drift detection will be disabled",
			"game", gameID, "err", err)
	}

	slog.Info("script extender installed",
		"game", gameID, "name", def.Name,
		"version", versionLabel,
		"destination", gc.InstallPath)

	go d.ensurePrefixRuntime(gameID, gc)

	return def.Name, nil
}

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
			return nil
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
