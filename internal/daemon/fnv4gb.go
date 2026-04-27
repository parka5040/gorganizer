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

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/ipc"
)

// FNV4GB Patcher: a community tool by RoyBatty that flips the LARGE_ADDRESS_AWARE
// bit on FalloutNV.exe so the game can address >2 GiB of memory — required for
// any heavy mod load order. The author publishes three variants on Nexus mod
// page 62552:
//
//   • Windows native    — runs on real Windows; not useful here
//   • "for Proton"      — Linux ELF that targets a Proton prefix
//   • "for Linux"       — Linux ELF that runs against the on-disk FalloutNV.exe
//
// We hardcode the "for Linux" file id because that's the only flavor that
// works against a Steam-installed FNV under our Linux daemon. Hardcoding is
// safe per the user spec — these IDs never change.
const (
	fnv4gbModID    = 62552
	fnv4gbFileID   = 1000079136 // "FNV4GB for Linux"
	fnv4gbGameSlug = "newvegas"

	// Marker dropped into the game install dir on a successful patch run.
	// Both daemon and frontend stat this to decide whether the
	// "Run xNVSE" launch target is greyed out — the patched FalloutNV.exe
	// must boot through the patcher's own launcher path, not nvse_loader.
	fnv4gbMarkerFilename = ".gorganizer-fnv4gb.applied"
)

// fnv4gbErrXNVSEMissing / fnv4gbErrAPIKeyMissing are sentinel errors so the
// IPC layer can map them to grpc.FailedPrecondition with a stable detail
// string the frontend can pattern-match for the right popup.
var (
	fnv4gbErrXNVSEMissing  = errors.New("xNVSE is required to install the 4GB patcher; install xNVSE first via the Run combo")
	fnv4gbErrAPIKeyMissing = errors.New("Nexus API key is required to download the 4GB patcher; paste one in Tools → Settings")
)

// Install4GBPatcher downloads the FNV4GB Patcher (Linux variant, file id
// hardcoded above) into the game's install directory next to FalloutNV.exe,
// chmod +x's the executable, and returns its path. The patcher is NOT run
// here — the GUI prompts the user with "Apply patch?" and calls
// Apply4GBPatch on accept.
//
// Pre-flight validation:
//   - gameID must be falloutnv (the patch is FNV-specific)
//   - xNVSE must already be installed (its loader exe present in InstallPath)
//   - A Nexus API key must be configured
//
// Errors from this method are designed to be surfaced verbatim in a GUI
// dialog — the message tells the user exactly what to fix.
func (d *Daemon) Install4GBPatcher(gameID string) (ipc.FNV4GBInstallResult, error) {
	var zero ipc.FNV4GBInstallResult
	if gameID != "falloutnv" {
		return zero, fmt.Errorf("the 4GB patcher only applies to Fallout: New Vegas (got %q)", gameID)
	}
	gc, ok := d.config.Games[gameID]
	if !ok {
		return zero, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if d.config.NexusAPIKey == "" {
		return zero, fnv4gbErrAPIKeyMissing
	}
	// xNVSE check: the loader exe lives directly under the game install
	// dir after a script-extender install. We look at the actual file on
	// disk rather than gc.Tool because the user may have installed xNVSE
	// outside gorganizer (manual drop-in).
	def, ok := KnownScriptExtenders[gameID]
	if !ok || def.LoaderExe == "" {
		return zero, fmt.Errorf("internal: no script extender definition for %s", gameID)
	}
	loaderPath := filepath.Join(gc.InstallPath, def.LoaderExe)
	if _, err := os.Stat(loaderPath); err != nil {
		return zero, fnv4gbErrXNVSEMissing
	}

	tmpDir, err := os.MkdirTemp("", "gorganizer-fnv4gb-*")
	if err != nil {
		return zero, err
	}
	// We deliberately do NOT defer RemoveAll — the extracted patcher
	// executable lives outside tmpDir (we copy it into InstallPath), so
	// cleaning the temp dir is fine after copyTree returns. Any later
	// failure during the chmod / executable-detection steps will leak
	// the tmp dir; acceptable.
	defer os.RemoveAll(tmpDir)

	nx := download.NewNexusClient(d.config.NexusAPIKey)

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

	// Copy every extracted file into the game install dir. The Linux
	// archive ships as a single ELF (FalloutNVPatcher) at root, but we
	// walk to be future-proof in case the author re-packages.
	if err := copyTree(extractDir, gc.InstallPath); err != nil {
		return zero, fmt.Errorf("copying patcher into game dir: %w", err)
	}

	patcherPath, err := locate4GBPatcherExe(extractDir, gc.InstallPath)
	if err != nil {
		return zero, fmt.Errorf("locating patcher executable: %w", err)
	}
	// chmod +x — the archive may have stripped executable bits depending
	// on how the user's extractor handles ELF perms.
	if err := os.Chmod(patcherPath, 0755); err != nil {
		return zero, fmt.Errorf("making patcher executable: %w", err)
	}

	slog.Info("FNV4GB patcher installed",
		"game", gameID,
		"version", details.Version,
		"path", patcherPath)

	return ipc.FNV4GBInstallResult{
		PatcherExePath: patcherPath,
		Version:        details.Version,
	}, nil
}

// Get4GBPatchStatus reports whether FalloutNV.exe in the active game's
// install directory has been patched by us. Apply4GBPatch is the only
// thing that writes the marker, so this never reports a false positive
// caused by an out-of-band patcher run.
func (d *Daemon) Get4GBPatchStatus(gameID string) (bool, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return false, nil
	}
	return IsFNV4GBApplied(gc.InstallPath), nil
}

// Apply4GBPatch executes the previously-installed patcher and, on success,
// drops the marker file the GUI uses to decide whether to disable the xNVSE
// launch target. The patcher mutates FalloutNV.exe in place; running it
// twice is harmless (it detects an already-patched binary and exits clean)
// but the GUI guards against that by hiding "Patch Fallout to 4GB" once
// the marker exists.
//
// The returned string carries combined stdout+stderr from the patcher run;
// the GUI surfaces it in a result dialog so the user can see what the
// patcher reported.
func (d *Daemon) Apply4GBPatch(gameID, patcherExePath string) (string, error) {
	if gameID != "falloutnv" {
		return "", fmt.Errorf("the 4GB patcher only applies to Fallout: New Vegas (got %q)", gameID)
	}
	gc, ok := d.config.Games[gameID]
	if !ok {
		return "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if patcherExePath == "" {
		return "", errors.New("empty patcher exe path")
	}
	if _, err := os.Stat(patcherExePath); err != nil {
		return "", fmt.Errorf("patcher executable missing — re-run install: %w", err)
	}

	// Run with the game install dir as cwd so the patcher finds
	// FalloutNV.exe in its working directory. The patcher's source is
	// public (RoyBatty/FNV4GBPatcher) — it `fopen("FalloutNV.exe", "rb+")`
	// off the cwd, no path argument.
	cmd := exec.Command(patcherExePath)
	cmd.Dir = gc.InstallPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("running patcher: %w", err)
	}

	// Marker file: timestamp + version label so a subsequent re-install
	// of the patcher can compare. Format is one-line-per-field, no YAML
	// dep — same convention the script-extender manifest uses.
	marker := filepath.Join(gc.InstallPath, fnv4gbMarkerFilename)
	contents := fmt.Sprintf("# applied_at: %s\n# patcher: %s\n",
		time.Now().UTC().Format(time.RFC3339), patcherExePath)
	if writeErr := os.WriteFile(marker, []byte(contents), 0644); writeErr != nil {
		// Don't fail the operation — the binary IS patched on disk; the
		// marker is just a UI hint. Log loudly so the disabled
		// "Run xNVSE" affordance flag isn't silently lost.
		slog.Warn("FNV4GB applied but marker file could not be written — UI may not reflect patched state",
			"err", writeErr, "marker", marker)
	}

	slog.Info("FNV4GB patch applied", "game", gameID, "install", gc.InstallPath)
	return string(out), nil
}

// IsFNV4GBApplied reports whether the marker file is present in the game
// install dir. Frontend reads this through a dedicated query RPC so the
// Run combo can grey out the xNVSE entry post-patch.
func IsFNV4GBApplied(installPath string) bool {
	if installPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(installPath, fnv4gbMarkerFilename))
	return err == nil
}

// locate4GBPatcherExe walks the extracted tree (which mirrors what was
// copied into installPath) and returns the absolute path to the patcher
// executable inside installPath. Strategy: prefer files matching the
// known Linux-variant binary name; fall back to the first regular file
// without an obvious data-only extension.
func locate4GBPatcherExe(extractRoot, installPath string) (string, error) {
	knownNames := map[string]bool{
		"falloutnvpatcher": true, // ships in both "for Proton" and "for Linux" variants
		"fnv4gb":           true,
		"fnv4gb_linux":     true,
	}
	skipExt := map[string]bool{
		".txt": true, ".md": true, ".html": true, ".pdf": true,
		".dll": true, ".exe": true, // the Windows-variant exe — never the Linux entry point
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
			return io.EOF // sentinel to stop the walk
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
