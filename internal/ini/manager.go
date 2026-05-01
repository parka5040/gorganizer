package ini

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// PushedIniReport is the per-file outcome of PushToDocuments, used to verify
// the on-disk state matches intent before launch.
type PushedIniReport struct {
	Filename   string
	SourcePath string
	TargetPath string
	Bytes      int64
	SHA256     string
	ModTime    time.Time
	Verified   bool
	Skipped    bool
	Note       string
}

// Manager handles per-profile INI storage at {profileDir}/ini/{filename}.
type Manager struct {
	profileDir func(gameID, profileName string) string
}

func NewManager(profileDir func(gameID, profileName string) string) *Manager {
	return &Manager{profileDir: profileDir}
}

func (m *Manager) IniDir(gameID, profileName string) string {
	return filepath.Join(m.profileDir(gameID, profileName), "ini")
}

func (m *Manager) IniPath(gameID, profileName, filename string) string {
	return filepath.Join(m.IniDir(gameID, profileName), filename)
}

// Read returns the profile INI content; missing files return an empty string.
func (m *Manager) Read(gameID, profileName, filename string) (string, error) {
	if !IsINIFile(gameID, filename) {
		return "", fmt.Errorf("%q is not a managed INI for %s", filename, gameID)
	}
	path := m.IniPath(gameID, profileName, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(data), nil
}

// Write persists INI content to the profile, creating dirs as needed.
func (m *Manager) Write(gameID, profileName, filename, content string) error {
	if !IsINIFile(gameID, filename) {
		return fmt.Errorf("%q is not a managed INI for %s", filename, gameID)
	}
	dir := m.IniDir(gameID, profileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating ini dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	return writeFileOverwritable(path, []byte(content))
}

// SeedFromDocuments seeds the profile's ini dir from the game's My Games dir
// when profile copies are missing, matching filenames case-insensitively.
func (m *Manager) SeedFromDocuments(gameID, profileName string, steamAppID int) error {
	spec, ok := SpecFor(gameID)
	if !ok {
		return fmt.Errorf("no INI spec for game %q", gameID)
	}
	docsDir, err := DocumentsPath(steamAppID, spec.MyGamesSubdir)
	if err != nil {
		return err
	}
	iniDir := m.IniDir(gameID, profileName)
	if err := os.MkdirAll(iniDir, 0755); err != nil {
		return err
	}
	for _, name := range spec.Files {
		dst := filepath.Join(iniDir, name)
		if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
			continue
		}
		src := resolveDocsFile(docsDir, name)
		if src == "" {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("seeding %s: %w", name, err)
		}
	}
	return nil
}

// resolveDocsFile returns the absolute path of name inside dir matched case-insensitively.
func resolveDocsFile(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	wantLower := toLowerASCII(name)
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if toLowerASCII(ent.Name()) == wantLower {
			return filepath.Join(dir, ent.Name())
		}
	}
	return ""
}

func toLowerASCII(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// PushToDocuments copies profile INIs into the game's My Games dir,
// merging Custom.ini into the primary INI for engines without native support.
func (m *Manager) PushToDocuments(gameID, profileName string, steamAppID int) ([]PushedIniReport, error) {
	spec, ok := SpecFor(gameID)
	if !ok {
		return nil, fmt.Errorf("no INI spec for game %q", gameID)
	}
	docsDir, err := DocumentsPath(steamAppID, spec.MyGamesSubdir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating documents dir: %w", err)
	}
	iniDir := m.IniDir(gameID, profileName)

	slog.Info("pushing profile INIs",
		"game", gameID, "profile", profileName,
		"from", iniDir, "to", docsDir)

	var customOverlay *Document
	mergeCustomIntoPrimary := spec.CustomIni != "" && !spec.NativeCustomIni
	if mergeCustomIntoPrimary {
		customPath := filepath.Join(iniDir, spec.CustomIni)
		customContent, readErr := os.ReadFile(customPath)
		switch {
		case readErr != nil && !os.IsNotExist(readErr):
			slog.Warn("reading profile Custom.ini failed", "path", customPath, "err", readErr)
		case len(customContent) == 0:
			slog.Info("profile has no Custom.ini overlay — tweaks set via the editor will NOT be merged",
				"path", customPath, "hint", "enable at least one tweak (e.g. Disable Intro Videos) to create it")
		default:
			customOverlay = ParseDocument(string(customContent))
			slog.Info("loaded Custom.ini overlay",
				"path", customPath, "bytes", len(customContent),
				"keys", overlayKeySummary(customOverlay))
		}
	}

	reports := make([]PushedIniReport, 0, len(spec.Files))
	for _, name := range spec.Files {
		src := filepath.Join(iniDir, name)
		dst := filepath.Join(docsDir, name)
		if existing := resolveDocsFile(docsDir, name); existing != "" {
			dst = existing
			specCased := filepath.Join(docsDir, name)
			if specCased != existing {
				if _, err := os.Stat(specCased); err == nil {
					if rmErr := os.Remove(specCased); rmErr != nil {
						slog.Warn("could not remove case-duplicate INI",
							"path", specCased, "kept", existing, "err", rmErr)
					} else {
						slog.Info("removed case-duplicate INI",
							"path", specCased, "kept", existing)
					}
				}
			}
		}

		if mergeCustomIntoPrimary && name == spec.CustomIni {
			reports = append(reports, PushedIniReport{
				Filename: name, SourcePath: src, TargetPath: dst,
				Skipped: true, Note: "merged into primary",
			})
			continue
		}

		if mergeCustomIntoPrimary && name == spec.PrimaryIni {
			primaryContent, primaryErr := os.ReadFile(src)
			primarySource := src
			if primaryErr != nil {
				primaryContent, _ = os.ReadFile(dst)
				primarySource = dst + " (fallback; profile had no copy)"
			}
			merged := ParseDocument(string(primaryContent))
			if customOverlay != nil {
				merged.Merge(customOverlay)
			}
			serialized := merged.Serialize()
			if err := writeFileOverwritable(dst, []byte(serialized)); err != nil {
				return reports, fmt.Errorf("writing merged %s: %w", name, err)
			}
			note := fmt.Sprintf("merged from %s, sIntroSequence=%s",
				primarySource, mergedKey(merged, "General", "SIntroSequence"))
			reports = append(reports, verifyPushed(name, src, dst, []byte(serialized), note))
			slog.Info("wrote merged primary INI",
				"dst", dst, "source", primarySource, "bytes", len(serialized),
				"sIntroSequence", mergedKey(merged, "General", "SIntroSequence"))
			continue
		}

		if _, err := os.Stat(src); err != nil {
			slog.Debug("profile INI absent — skipping", "name", name, "path", src)
			reports = append(reports, PushedIniReport{
				Filename: name, SourcePath: src, TargetPath: dst,
				Skipped: true, Note: "profile has no override for this file",
			})
			continue
		}
		content, readErr := os.ReadFile(src)
		if readErr != nil {
			return reports, fmt.Errorf("reading profile %s: %w", name, readErr)
		}
		if err := writeFileOverwritable(dst, content); err != nil {
			return reports, fmt.Errorf("copying %s: %w", name, err)
		}
		reports = append(reports, verifyPushed(name, src, dst, content, "copied"))
		slog.Info("copied profile INI through", "name", name, "dst", dst)
	}
	return reports, nil
}

// verifyPushed re-stats the target and compares against the intended bytes.
func verifyPushed(name, src, dst string, wrote []byte, note string) PushedIniReport {
	r := PushedIniReport{
		Filename: name, SourcePath: src, TargetPath: dst,
		Bytes: int64(len(wrote)), Note: note,
	}
	h := sha256.Sum256(wrote)
	r.SHA256 = hex.EncodeToString(h[:])

	info, err := os.Stat(dst)
	if err != nil {
		r.Note = note + "; post-write stat FAILED: " + err.Error()
		return r
	}
	r.ModTime = info.ModTime()
	if info.Size() != int64(len(wrote)) {
		r.Note = fmt.Sprintf("%s; size mismatch after write (wrote=%d on-disk=%d)",
			note, len(wrote), info.Size())
		return r
	}
	r.Verified = true
	return r
}

// overlayKeySummary renders "[Section] Key=Value; ..." for an overlay document.
func overlayKeySummary(doc *Document) string {
	if doc == nil {
		return "(empty)"
	}
	var out []byte
	for _, ln := range doc.lines {
		if ln.kind != lineKey {
			continue
		}
		if len(out) > 0 {
			out = append(out, ';', ' ')
		}
		out = append(out, '[')
		out = append(out, ln.section...)
		out = append(out, ']', ' ')
		out = append(out, ln.key...)
		out = append(out, '=')
		out = append(out, ln.value...)
	}
	if len(out) == 0 {
		return "(no keys)"
	}
	return string(out)
}

// mergedKey fetches one (section, key) for logging; "<unset>" when absent.
func mergedKey(doc *Document, section, key string) string {
	if doc == nil {
		return "<unset>"
	}
	if v, ok := doc.Get(section, key); ok {
		if v == "" {
			return "(empty — intro skip ACTIVE)"
		}
		return v
	}
	return "<unset>"
}

// writeFileOverwritable writes data to path, restoring u+w first if Wine's DOS
// read-only attribute has stripped it.
func writeFileOverwritable(path string, data []byte) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&0200 == 0 {
			if chErr := os.Chmod(path, 0644); chErr != nil {
				return fmt.Errorf("clearing read-only on %s: %w", path, chErr)
			}
		}
	}
	return os.WriteFile(path, data, 0644)
}

// ReadDocument loads a profile INI as a parsed Document; missing files yield an empty doc.
func (m *Manager) ReadDocument(gameID, profileName, filename string) (*Document, error) {
	content, err := m.Read(gameID, profileName, filename)
	if err != nil {
		return nil, err
	}
	return ParseDocument(content), nil
}

func (m *Manager) WriteDocument(gameID, profileName, filename string, doc *Document) error {
	return m.Write(gameID, profileName, filename, doc.Serialize())
}

// TweakState mirrors what ListTweaks returns over IPC.
type TweakState struct {
	ID          string
	Name        string
	Description string
	TargetFile  string
	Enabled     bool
}

// ListTweaks returns every preset paired with its current applied state.
func (m *Manager) ListTweaks(gameID, profileName string) ([]TweakState, error) {
	spec, ok := SpecFor(gameID)
	if !ok {
		return nil, nil
	}
	presets := AvailableTweaks(gameID)
	if len(presets) == 0 {
		return nil, nil
	}
	customFile := spec.CustomIni
	if customFile == "" {
		return nil, nil
	}
	doc, err := m.ReadDocument(gameID, profileName, customFile)
	if err != nil {
		return nil, err
	}
	out := make([]TweakState, 0, len(presets))
	for _, t := range presets {
		out = append(out, TweakState{
			ID:          t.ID,
			Name:        t.Name,
			Description: t.Description,
			TargetFile:  customFile,
			Enabled:     t.IsApplied(doc),
		})
	}
	return out, nil
}

// SetTweak applies or removes a tweak in the profile's Custom.ini.
func (m *Manager) SetTweak(gameID, profileName, tweakID string, enabled bool) (*TweakState, error) {
	spec, ok := SpecFor(gameID)
	if !ok || spec.CustomIni == "" {
		return nil, fmt.Errorf("game %q has no Custom.ini target", gameID)
	}
	tweak, ok := FindTweak(gameID, tweakID)
	if !ok {
		return nil, fmt.Errorf("unknown tweak %q for game %q", tweakID, gameID)
	}
	doc, err := m.ReadDocument(gameID, profileName, spec.CustomIni)
	if err != nil {
		return nil, err
	}
	if enabled {
		tweak.Apply(doc)
	} else {
		tweak.Unapply(doc)
	}
	if err := m.WriteDocument(gameID, profileName, spec.CustomIni, doc); err != nil {
		return nil, err
	}
	return &TweakState{
		ID:          tweak.ID,
		Name:        tweak.Name,
		Description: tweak.Description,
		TargetFile:  spec.CustomIni,
		Enabled:     tweak.IsApplied(doc),
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, statErr := os.Stat(dst); statErr == nil && info.Mode()&0200 == 0 {
		if chErr := os.Chmod(dst, 0644); chErr != nil {
			return fmt.Errorf("clearing read-only on %s: %w", dst, chErr)
		}
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyFileIfExists(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return copyFile(src, dst)
}
