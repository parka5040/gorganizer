package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/tools"
)

type prefixRuntimeManifest struct {
	SchemaVersion int      `json:"schema_version"`
	Packages      []string `json:"packages"`
}

func (es *ExecutableService) ensureExecutableRuntime(gameID string, gameConfig config.GameConfig, executable config.Executable) error {
	definition, trusted := tools.ValidateCatalogMatch(executable.ToolID, gameID, executable.ExePath)
	if !trusted || len(definition.RuntimePackages) == 0 {
		return nil
	}
	appID := gameConfig.SteamAppID
	if executable.PrefixAppID > 0 {
		appID = executable.PrefixAppID
	}
	compatData, err := tools.ResolveCompatDataPath(&gameConfig, executable.PrefixAppID)
	if err != nil {
		return err
	}
	prefixPath := filepath.Join(compatData, "pfx")
	if info, err := os.Stat(prefixPath); err != nil || !info.IsDir() {
		return fmt.Errorf("Proton prefix for app %d does not exist; launch it through Steam once first", appID)
	}
	prefixRuntimeInstalledMu.Lock()
	defer prefixRuntimeInstalledMu.Unlock()
	manifestPath := filepath.Join(prefixPath, ".gorganizer-prefix-runtime.json")
	installed := make(map[string]bool)
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest prefixRuntimeManifest
		if json.Unmarshal(data, &manifest) == nil && manifest.SchemaVersion == 1 {
			for _, pkg := range manifest.Packages {
				installed[pkg] = true
			}
		}
	}
	missing := make([]string, 0)
	for _, pkg := range definition.RuntimePackages {
		if !installed[pkg] {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	protontricks, err := exec.LookPath("protontricks")
	if err != nil {
		return fmt.Errorf("%s requires %v in its Proton prefix; install protontricks and retry", definition.Title, missing)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	args := []string{"--no-bwrap", strconv.Itoa(appID), "-q"}
	args = append(args, missing...)
	cmd := exec.CommandContext(ctx, protontricks, args...)
	cmd.Env = append(os.Environ(), "STEAM_COMPAT_DATA_PATH="+compatData)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installing %s Proton prerequisites: %w; stdout=%s stderr=%s",
			definition.Title, err, trimForLog(stdout.String()), trimForLog(stderr.String()))
	}
	for _, pkg := range missing {
		installed[pkg] = true
	}
	packages := make([]string, 0, len(installed))
	for pkg := range installed {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	data, err := json.MarshalIndent(prefixRuntimeManifest{SchemaVersion: 1, Packages: packages}, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("recording Proton prerequisites: %w", err)
	}
	return nil
}
