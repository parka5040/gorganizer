package smapi

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type PlannedFolder struct {
	SourceRel string
	DestName  string
	UniqueID  string
	Name      string
	Version   string
}

type Plan struct {
	Folders []PlannedFolder
}

const (
	maxDestNameBytes      = 255
	maxSanitizedNameBytes = 128
	fallbackDestName      = "Mod"
)

var installerPayloadSuffixes = []string{"internal/linux/install.dat", "internal/windows/install.dat", "internal/macos/install.dat"}

// PlanArchive plans how each SMAPI manifest folder in an extracted archive becomes a top-level mod folder.
func PlanArchive(extractRoot string) (Plan, error) {
	installer, err := hasInstallerBundle(extractRoot)
	if err != nil {
		return Plan{}, err
	}
	if installer {
		return Plan{}, NotAModError{Reason: ReasonLoaderInstaller}
	}
	scanned, err := Scan(extractRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("planning SMAPI archive layout: %w", err)
	}
	if err := refuseNestedMods(scanned); err != nil {
		return Plan{}, err
	}
	var plan Plan
	seen := map[string]string{}
	for _, folder := range scanned {
		if folder.Kind == FolderIgnored && isReservedName(path.Base(folder.RelPath)) {
			return Plan{}, NotAModError{Reason: ReasonUnsafeDestination, Detail: path.Base(folder.RelPath)}
		}
		if folder.Kind != FolderMod && folder.Kind != FolderRootManifest {
			continue
		}
		if err := validateSourceRel(folder.RelPath); err != nil {
			return Plan{}, err
		}
		planned := PlannedFolder{SourceRel: folder.RelPath, DestName: path.Base(folder.RelPath)}
		if folder.Manifest != nil {
			planned.UniqueID = folder.Manifest.UniqueID
			planned.Name = folder.Manifest.Name
			if !folder.Manifest.Version.IsZero() {
				planned.Version = folder.Manifest.Version.String()
			}
		}
		if folder.Kind == FolderRootManifest {
			planned.DestName = rootDestName(folder.Manifest)
		}
		if err := ValidateDestName(planned.DestName); err != nil {
			return Plan{}, err
		}
		key := strings.ToLower(planned.DestName)
		if previous, found := seen[key]; found {
			return Plan{}, NotAModError{Reason: ReasonFolderCollision, Detail: previous + "," + planned.DestName}
		}
		seen[key] = planned.DestName
		plan.Folders = append(plan.Folders, planned)
	}
	if len(plan.Folders) == 0 {
		return Plan{}, NotAModError{Reason: ReasonNoManifest}
	}
	if duplicated := duplicateUniqueIDs(scanned); len(duplicated) > 0 {
		return Plan{}, NotAModError{Reason: ReasonDuplicateIDs, Detail: strings.Join(duplicated, ",")}
	}
	sort.Slice(plan.Folders, func(i, j int) bool {
		return compareFold(plan.Folders[i].DestName, plan.Folders[j].DestName) < 0
	})
	return plan, nil
}

// refuseNestedMods refuses an archive whose root manifest sits above other mod folders, since its intended layout is ambiguous.
func refuseNestedMods(scanned []Folder) error {
	hasRoot := false
	var nested []string
	for _, folder := range scanned {
		switch folder.Kind {
		case FolderRootManifest:
			hasRoot = true
		case FolderMod:
			nested = append(nested, folder.RelPath)
		}
	}
	if !hasRoot || len(nested) == 0 {
		return nil
	}
	return NotAModError{Reason: ReasonNestedMods, Detail: strings.Join(nested, ",")}
}

// duplicateUniqueIDs returns each UniqueID that more than one parsed manifest folder declares, compared case-insensitively.
func duplicateUniqueIDs(scanned []Folder) []string {
	first := map[string]string{}
	reported := map[string]bool{}
	var duplicated []string
	for _, folder := range scanned {
		if folder.Kind != FolderMod && folder.Kind != FolderRootManifest {
			continue
		}
		if folder.Manifest == nil || folder.ParseErr != nil || strings.TrimSpace(folder.Manifest.UniqueID) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(folder.Manifest.UniqueID))
		spelling, seen := first[key]
		if !seen {
			first[key] = folder.Manifest.UniqueID
			continue
		}
		if !reported[key] {
			reported[key] = true
			duplicated = append(duplicated, spelling)
		}
	}
	return duplicated
}

// rootDestName picks the folder name that wraps a root manifest, skipping names SMAPI's scanner would ignore.
func rootDestName(manifest *Manifest) string {
	if manifest == nil {
		return fallbackDestName
	}
	for _, candidate := range []string{manifest.Name, manifest.UniqueID} {
		name := SanitizeFolderName(candidate)
		if name != "" && isRelevant(name, true) && ValidateDestName(name) == nil {
			return name
		}
	}
	return fallbackDestName
}

// validateSourceRel rejects a scanned source path that is absolute or contains empty, dot or dot-dot components.
func validateSourceRel(rel string) error {
	if rel == "" {
		return nil
	}
	if strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return NotAModError{Reason: ReasonUnsafeDestination, Detail: rel}
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return NotAModError{Reason: ReasonUnsafeDestination, Detail: rel}
		}
	}
	return nil
}

// ValidateDestName rejects a destination folder name that is empty, hidden, reserved, or not a single safe path component.
func ValidateDestName(name string) error {
	unsafe := NotAModError{Reason: ReasonUnsafeDestination, Detail: name}
	if name == "" || len(name) > maxDestNameBytes || name == "." || name == ".." || !utf8.ValidString(name) {
		return unsafe
	}
	if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || strings.ContainsFunc(name, unicode.IsControl) {
		return unsafe
	}
	if isReservedName(name) {
		return unsafe
	}
	return nil
}

// isReservedName reports whether name collides case-insensitively with a gorganizer-owned mod entry.
func isReservedName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "metadata.yaml" || lower == "overwrite" || strings.HasPrefix(lower, ".gorganizer")
}

// SanitizeFolderName turns a manifest name into a safe folder name, possibly returning an empty string.
func SanitizeFolderName(name string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range name {
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	result := trimFolderName(b.String())
	if len(result) > maxSanitizedNameBytes {
		cut := maxSanitizedNameBytes
		for cut > 0 && !utf8.RuneStart(result[cut]) {
			cut--
		}
		result = trimFolderName(result[:cut])
	}
	return result
}

// trimFolderName trims spaces and dots from both ends of a folder name.
func trimFolderName(name string) string {
	return strings.Trim(name, " .")
}

// hasInstallerBundle reports whether an extracted tree contains SMAPI installer launchers or payloads.
func hasInstallerBundle(root string) (bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		return false, fmt.Errorf("checking SMAPI archive layout at %s: %w", root, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("checking SMAPI archive layout at %s: not a directory", root)
	}
	return walkForInstaller(root, "")
}

// walkForInstaller recursively searches a folder for installer launcher files or install.dat payloads.
func walkForInstaller(abs, rel string) (bool, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return false, fmt.Errorf("checking SMAPI archive layout at %s: %w", abs, err)
	}
	for _, entry := range entries {
		childRel := joinRel(rel, entry.Name())
		if isSymlink(entry) {
			continue
		}
		if entry.IsDir() {
			found, err := walkForInstaller(filepath.Join(abs, entry.Name()), childRel)
			if err != nil || found {
				return found, err
			}
			continue
		}
		if isInstallerPath(childRel) {
			return true, nil
		}
	}
	return false, nil
}

// isInstallerPath reports whether a slash-separated file path is a SMAPI installer launcher or payload.
func isInstallerPath(rel string) bool {
	base := path.Base(rel)
	for name := range installerFileNames {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	lower := strings.ToLower(rel)
	for _, suffix := range installerPayloadSuffixes {
		if lower == suffix || strings.HasSuffix(lower, "/"+suffix) {
			return true
		}
	}
	return false
}
