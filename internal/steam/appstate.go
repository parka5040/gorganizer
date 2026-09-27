package steam

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const maxAppManifestSize = 1 << 20

type AppState struct {
	AppID            int
	InstallDir       string
	BuildID          string
	StateFlags       uint64
	UpdateResult     string
	LastUpdated      int64
	DepotFingerprint string
}

// Idle reports whether Steam marks the installation as fully installed and inactive.
func (a AppState) Idle() bool { return a.StateFlags == 4 }

// StorefrontValues returns the identity and version fields used to compare an installation.
func (a AppState) StorefrontValues() (int, string, int64, string) {
	return a.AppID, a.BuildID, a.LastUpdated, a.DepotFingerprint
}

// ReadAppState reads the manifest beside a Steam library's common installation directory.
func ReadAppState(installPath string, appID int) (AppState, error) {
	clean := filepath.Clean(installPath)
	common := filepath.Dir(clean)
	steamapps := filepath.Dir(common)
	if appID <= 0 || !filepath.IsAbs(clean) || filepath.Base(common) != "common" || filepath.Base(steamapps) != "steamapps" ||
		clean == common || filepath.Base(clean) == "." {
		return AppState{}, fmt.Errorf("%w: %s is not a Steam library installation", ErrAppManifestNotFound, installPath)
	}
	manifest := filepath.Join(steamapps, fmt.Sprintf("appmanifest_%d.acf", appID))
	fd, err := unix.Open(manifest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AppState{}, fmt.Errorf("%w: %s: %w", ErrAppManifestNotFound, manifest, err)
		}
		return AppState{}, fmt.Errorf("opening Steam app manifest %s: %w", manifest, err)
	}
	f := os.NewFile(uintptr(fd), manifest)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return AppState{}, fmt.Errorf("stating Steam app manifest %s: %w", manifest, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxAppManifestSize {
		return AppState{}, fmt.Errorf("Steam app manifest %s must be a regular file no larger than 1 MiB", manifest)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxAppManifestSize+1))
	if err != nil {
		return AppState{}, fmt.Errorf("reading Steam app manifest %s: %w", manifest, err)
	}
	if len(body) > maxAppManifestSize {
		return AppState{}, fmt.Errorf("Steam app manifest %s exceeds 1 MiB", manifest)
	}
	parsed, err := ParseVDF(strings.NewReader(string(body)))
	if err != nil {
		return AppState{}, fmt.Errorf("parsing Steam app manifest %s: %w", manifest, err)
	}
	fields, ok := parsed["AppState"].(map[string]interface{})
	if !ok {
		return AppState{}, fmt.Errorf("Steam app manifest %s has no AppState", manifest)
	}
	appid, err := strconv.Atoi(vdfFieldString(fields, "appid"))
	if err != nil {
		return AppState{}, fmt.Errorf("Steam app manifest %s has an invalid appid: %w", manifest, err)
	}
	if appid != appID {
		return AppState{}, fmt.Errorf("Steam app manifest %s has an unexpected appid", manifest)
	}
	installDir := vdfFieldString(fields, "installdir")
	if installDir != filepath.Base(clean) {
		return AppState{}, fmt.Errorf("Steam app manifest %s has an unexpected installdir", manifest)
	}
	stateFlags, err := strconv.ParseUint(vdfFieldString(fields, "StateFlags"), 10, 64)
	if err != nil {
		return AppState{}, fmt.Errorf("Steam app manifest %s has invalid StateFlags: %w", manifest, err)
	}
	lastUpdated := int64(0)
	if value := vdfFieldString(fields, "LastUpdated"); value != "" {
		lastUpdated, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return AppState{}, fmt.Errorf("Steam app manifest %s has invalid LastUpdated: %w", manifest, err)
		}
	}
	fingerprint, err := depotFingerprint(fields["InstalledDepots"])
	if err != nil {
		return AppState{}, fmt.Errorf("Steam app manifest %s: %w", manifest, err)
	}
	return AppState{
		AppID: appid, InstallDir: installDir, BuildID: vdfFieldString(fields, "buildid"),
		StateFlags: stateFlags, UpdateResult: vdfFieldString(fields, "UpdateResult"),
		LastUpdated: lastUpdated, DepotFingerprint: fingerprint,
	}, nil
}

func vdfFieldString(fields map[string]interface{}, key string) string {
	value, _ := fields[key].(string)
	return value
}

func depotFingerprint(raw interface{}) (string, error) {
	if raw == nil {
		return "", nil
	}
	depots, ok := raw.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid InstalledDepots")
	}
	if len(depots) == 0 {
		return "", nil
	}
	pairs := make([]string, 0, len(depots))
	for id, rawDepot := range depots {
		depot, ok := rawDepot.(map[string]interface{})
		if !ok || vdfFieldString(depot, "manifest") == "" {
			return "", fmt.Errorf("invalid InstalledDepots entry %q", id)
		}
		pairs = append(pairs, id+":"+vdfFieldString(depot, "manifest"))
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))
	return hex.EncodeToString(sum[:]), nil
}
