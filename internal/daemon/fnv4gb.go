package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
)

const (
	fnv4gbModID    = 62552
	fnv4gbFileID   = 1000079136
	fnv4gbGameSlug = "newvegas"

	fnv4gbMarkerFilename = ".gorganizer-fnv4gb.applied"
)

var (
	fnv4gbErrXNVSEMissing  = errors.New("xNVSE is required to install the 4GB patcher; install xNVSE first via the Run combo")
	fnv4gbErrAPIKeyMissing = errors.New("Nexus API key is required to download the 4GB patcher; paste one in Tools → Settings")
)

func (fv *FNV4GBService) Install4GBPatcher(gameID string) (dto.FNV4GBInstallResult, error) {
	var zero dto.FNV4GBInstallResult
	g, known := gamedef.ByID(gameID)
	if !known || !g.Supports4GBPatch {
		return zero, fmt.Errorf("the 4GB patcher only applies to Fallout: New Vegas (got %q)", gameID)
	}
	gc, err := fv.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return zero, err
	}
	if fv.s.config.NexusAPIKey == "" {
		return zero, fnv4gbErrAPIKeyMissing
	}
	if g.ScriptExtenderSource == nil || g.ScriptExtenderSource.LoaderExe == "" {
		return zero, fmt.Errorf("internal: no script extender definition for %s", gameID)
	}
	loaderPath := filepath.Join(gc.InstallPath, g.ScriptExtenderSource.LoaderExe)
	if _, err := os.Stat(loaderPath); err != nil {
		return zero, fnv4gbErrXNVSEMissing
	}

	tmpDir, err := os.MkdirTemp("", "gorganizer-fnv4gb-*")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(tmpDir)

	nx := download.NewNexusClient(fv.s.config.NexusAPIKey)

	details, err := nx.GetFileDetails(fnv4gbGameSlug, fnv4gbModID, fnv4gbFileID)
	if err != nil {
		return zero, fmt.Errorf("looking up FNV4GB file metadata: %w", err)
	}

	cdnURL, err := nx.ResolveDownloadURLByID(fnv4gbGameSlug, fnv4gbModID, fnv4gbFileID)
	if err != nil {
		return zero, fmt.Errorf("resolving FNV4GB CDN url: %w "+
			"(if non-premium, open https://www.nexusmods.com/newvegas/mods/%d "+
			"and click Download with Manager to trigger an NXM download)", err, fnv4gbModID)
	}

	archiveName := details.FileName
	if archiveName == "" {
		archiveName = fmt.Sprintf("fnv4gb-linux-%d.archive", fnv4gbFileID)
	}
	archivePath := filepath.Join(tmpDir, archiveName)
	if err := streamTo(cdnURL, archivePath); err != nil {
		return zero, fmt.Errorf("downloading FNV4GB archive: %w", err)
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return zero, err
	}
	extractor, err := download.DetectExtractor(archivePath)
	if err != nil {
		return zero, fmt.Errorf("detecting archive: %w", err)
	}
	if err := extractor.Extract(archivePath, extractDir); err != nil {
		return zero, fmt.Errorf("extracting FNV4GB archive: %w", err)
	}

	if err := copyTree(extractDir, gc.InstallPath); err != nil {
		return zero, fmt.Errorf("copying patcher into game dir: %w", err)
	}

	patcherPath, err := locate4GBPatcherExe(extractDir, gc.InstallPath)
	if err != nil {
		return zero, fmt.Errorf("locating patcher executable: %w", err)
	}
	if err := os.Chmod(patcherPath, 0755); err != nil {
		return zero, fmt.Errorf("making patcher executable: %w", err)
	}

	slog.Info("FNV4GB patcher installed",
		"game", gameID,
		"version", details.Version,
		"path", patcherPath)

	return dto.FNV4GBInstallResult{
		PatcherExePath: patcherPath,
		Version:        details.Version,
	}, nil
}

// Get4GBPatchStatus reports whether FalloutNV.exe in the active game's
func (fv *FNV4GBService) Get4GBPatchStatus(gameID string) (bool, error) {
	gc, err := fv.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return false, nil
	}
	return IsFNV4GBApplied(gc.InstallPath), nil
}

// Apply4GBPatch executes the previously-installed patcher and, on success,
func (fv *FNV4GBService) Apply4GBPatch(gameID, patcherExePath string) (string, error) {
	if g, known := gamedef.ByID(gameID); !known || !g.Supports4GBPatch {
		return "", fmt.Errorf("the 4GB patcher only applies to Fallout: New Vegas (got %q)", gameID)
	}
	gc, err := fv.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return "", err
	}
	if patcherExePath == "" {
		return "", errors.New("empty patcher exe path")
	}
	if _, err := os.Stat(patcherExePath); err != nil {
		return "", fmt.Errorf("patcher executable missing — re-run install: %w", err)
	}

	cmd := exec.Command(patcherExePath)
	cmd.Dir = gc.InstallPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("running patcher: %w", err)
	}

	marker := filepath.Join(gc.InstallPath, fnv4gbMarkerFilename)
	contents := fmt.Sprintf("# applied_at: %s\n# patcher: %s\n",
		time.Now().UTC().Format(time.RFC3339), patcherExePath)
	if writeErr := os.WriteFile(marker, []byte(contents), 0644); writeErr != nil {
		slog.Warn("FNV4GB applied but marker file could not be written — UI may not reflect patched state",
			"err", writeErr, "marker", marker)
	}

	slog.Info("FNV4GB patch applied", "game", gameID, "install", gc.InstallPath)
	return string(out), nil
}

// IsFNV4GBApplied reports whether the marker file is present in the game
func IsFNV4GBApplied(installPath string) bool {
	if installPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(installPath, fnv4gbMarkerFilename))
	return err == nil
}

// locate4GBPatcherExe walks the extracted tree (which mirrors what was
func locate4GBPatcherExe(extractRoot, installPath string) (string, error) {
	knownNames := map[string]bool{
		"falloutnvpatcher": true,
		"fnv4gb":           true,
		"fnv4gb_linux":     true,
	}
	skipExt := map[string]bool{
		".txt": true, ".md": true, ".html": true, ".pdf": true,
		".dll": true, ".exe": true,
	}

	var fallback string
	walkErr := filepath.Walk(extractRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(extractRoot, path)
		if relErr != nil {
			return relErr
		}
		base := strings.ToLower(info.Name())
		ext := strings.ToLower(filepath.Ext(base))
		stem := strings.TrimSuffix(base, ext)

		dest := filepath.Join(installPath, rel)
		if knownNames[stem] {
			fallback = dest
			return io.EOF
		}
		if !skipExt[ext] && fallback == "" {
			fallback = dest
		}
		return nil
	})
	if walkErr != nil && walkErr != io.EOF {
		return "", walkErr
	}
	if fallback == "" {
		return "", errors.New("no executable-looking file found in patcher archive")
	}
	return fallback, nil
}
