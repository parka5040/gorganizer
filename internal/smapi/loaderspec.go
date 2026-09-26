package smapi

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/parka/gorganizer/internal/fsutil"
)

const (
	RecordFile   = ".gorganizer-modloader.json"
	IntentFile   = ".gorganizer-modloader-intent.json"
	BackupDir    = ".gorganizer-modloader-backup"
	WorkDir      = ".gorganizer-modloader-work"
	OriginalsDir = ".gorganizer-modloader-originals"

	modsDirName       = "Mods"
	loaderAssembly    = "StardewModdingAPI.dll"
	loaderInternalDir = "smapi-internal"
	userConfigRel     = "smapi-internal/config.user.json"
	userLogsRel       = "smapi-internal/logs"
	reservedPrefix    = ".gorganizer"
	gameContentDir    = "Content"
)

type FarmGuard struct {
	DeployDir       string
	Sentinel        string
	SiblingSuffixes []string
}

type LoaderSpec struct {
	InstallerRelPath    string
	PayloadRelPath      string
	LauncherName        string
	LauncherBackupName  string
	LauncherPayloadName string
	LoaderExecutable    string
	GameAssembly        string
	GameVersionFile     string
	GameVersionPackage  string
	LoaderDepsFile      string
	NativeMarkers       []string
	ForeignMarkers      []string
	BundledModIDs       []string
	UninstallPaths      []string
	Farm                FarmGuard
}

// Validate checks that every name in the spec is a safe relative path and that no uninstall path is game-owned.
func (s LoaderSpec) Validate() error {
	names := []struct{ field, value string }{
		{"LauncherName", s.LauncherName},
		{"LauncherBackupName", s.LauncherBackupName},
		{"LauncherPayloadName", s.LauncherPayloadName},
		{"LoaderExecutable", s.LoaderExecutable},
		{"GameAssembly", s.GameAssembly},
		{"GameVersionFile", s.GameVersionFile},
		{"LoaderDepsFile", s.LoaderDepsFile},
	}
	seen := map[string]string{}
	for _, n := range names {
		if err := validateSingleName(n.value); err != nil {
			return fmt.Errorf("mod-loader spec %s: %w", n.field, err)
		}
		if other, ok := seen[n.value]; ok {
			return fmt.Errorf("mod-loader spec %s and %s are both %q", other, n.field, n.value)
		}
		seen[n.value] = n.field
	}
	for _, p := range []struct{ field, value string }{
		{"InstallerRelPath", s.InstallerRelPath},
		{"PayloadRelPath", s.PayloadRelPath},
	} {
		if err := validateRelPath(p.value); err != nil {
			return fmt.Errorf("mod-loader spec %s: %w", p.field, err)
		}
	}
	if s.InstallerRelPath == s.PayloadRelPath {
		return errors.New("mod-loader spec installer and payload paths are identical")
	}
	if strings.TrimSpace(s.GameVersionPackage) == "" {
		return errors.New("mod-loader spec GameVersionPackage is empty")
	}
	if len(s.NativeMarkers) == 0 {
		return errors.New("mod-loader spec has no native markers")
	}
	for _, group := range []struct {
		field  string
		values []string
	}{
		{"NativeMarkers", s.NativeMarkers},
		{"ForeignMarkers", s.ForeignMarkers},
		{"UninstallPaths", s.UninstallPaths},
	} {
		for _, v := range group.values {
			if err := validateRelPath(v); err != nil {
				return fmt.Errorf("mod-loader spec %s entry %q: %w", group.field, v, err)
			}
		}
	}
	for _, id := range s.BundledModIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("mod-loader spec has an empty bundled mod ID")
		}
	}
	gameOwned := map[string]bool{s.LauncherName: true, s.GameAssembly: true, s.GameVersionFile: true, modsDirName: true}
	for _, marker := range s.NativeMarkers {
		gameOwned[marker] = true
	}
	for _, u := range s.UninstallPaths {
		if gameOwned[u] {
			return fmt.Errorf("mod-loader spec uninstall path %q is game-owned", u)
		}
	}
	return nil
}

// validateSingleName requires name to be one safe, non-reserved path segment.
func validateSingleName(name string) error {
	if err := fsutil.ValidateName(name); err != nil {
		return err
	}
	if name != strings.TrimSpace(name) {
		return errors.New("name has surrounding whitespace")
	}
	if strings.HasPrefix(name, reservedPrefix) {
		return fmt.Errorf("name %q is reserved", name)
	}
	return nil
}

// validateRelPath requires rel to be a clean slash-separated relative path of safe, non-reserved segments.
func validateRelPath(rel string) error {
	if rel == "" {
		return errors.New("path is empty")
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return fmt.Errorf("path %q is not a slash-relative path", rel)
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("path %q is not clean", rel)
	}
	for _, segment := range strings.Split(rel, "/") {
		if err := validateSingleName(segment); err != nil {
			return fmt.Errorf("path %q: %w", rel, err)
		}
	}
	return nil
}

// isWithin reports whether rel equals base or lies beneath it.
func isWithin(rel, base string) bool {
	return rel == base || strings.HasPrefix(rel, base+"/")
}

// overlaps reports whether either path equals or contains the other.
func overlaps(a, b string) bool {
	return isWithin(a, b) || isWithin(b, a)
}

// protectedNames returns the game-owned top-level names that no loader payload, plan, or record may claim.
func (s LoaderSpec) protectedNames() map[string]bool {
	names := map[string]bool{s.GameAssembly: true, s.GameVersionFile: true, gameContentDir: true, modsDirName: true}
	for _, group := range [][]string{s.NativeMarkers, s.ForeignMarkers} {
		for _, marker := range group {
			names[strings.SplitN(marker, "/", 2)[0]] = true
		}
	}
	return names
}

// protectedTarget reports whether rel falls on a game-owned name, allowing only folders below Mods.
func protectedTarget(rel string, protected map[string]bool) bool {
	segments := strings.SplitN(rel, "/", 3)
	if segments[0] == modsDirName {
		return len(segments) < 2
	}
	return protected[segments[0]]
}
