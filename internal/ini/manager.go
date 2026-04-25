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

// PushedIniReport is the per-file outcome of PushToDocuments. The caller
// (daemon.LaunchGame) uses it to verify the on-disk state matches our
// intent — catches failures where the push succeeded from the OS's point
// of view but the Proton prefix was laid out such that the game reads a
// different file. When Verified=false after the push, the launch must
// abort — this is a surfaceable condition, not a warning.
type PushedIniReport struct {
	Filename   string
	SourcePath string // profile INI that fed this write
	TargetPath string // absolute path inside the Proton prefix
	Bytes      int64
	SHA256     string
	ModTime    time.Time
	Verified   bool   // stat succeeded AND size matches; false means follow-up check failed
	Skipped    bool   // profile had no source for this filename
	Note       string // human diagnostic (merge summary, skip reason, etc.)
}

// Manager handles per-profile INI storage. Files live under the profile's
// directory at `{profileDir}/ini/{filename}`. On-disk format is the raw INI
// content — Gorganizer doesn't re-parse or reorder keys, so mod-specific
// tweaks (e.g. SweetFX-style blocks with duplicate sections) survive round
// trips intact.
type Manager struct {
	profileDir func(gameID, profileName string) string
}

// NewManager wires a Manager to the profile-directory resolver. Passed in
// as a function to avoid a package-level dependency on internal/profile.
func NewManager(profileDir func(gameID, profileName string) string) *Manager {
	return &Manager{profileDir: profileDir}
}

// IniDir returns the per-profile INI directory.
func (m *Manager) IniDir(gameID, profileName string) string {
	return filepath.Join(m.profileDir(gameID, profileName), "ini")
}

// IniPath returns the full path to a specific INI file in the profile.
func (m *Manager) IniPath(gameID, profileName, filename string) string {
	return filepath.Join(m.IniDir(gameID, profileName), filename)
}

// Read returns the content of a profile INI file. Returns an empty string
// (no error) when the file does not yet exist — that's expected for a
// freshly-enabled profile.
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

// Write persists INI content to the profile. Creates the directory tree as
// needed. Accepts empty content (writes an empty file).
func (m *Manager) Write(gameID, profileName, filename, content string) error {
	if !IsINIFile(gameID, filename) {
		return fmt.Errorf("%q is not a managed INI for %s", filename, gameID)
	}
	dir := m.IniDir(gameID, profileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating ini dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	// Profile INIs live outside the Wine prefix so they generally don't
	// have the read-only attribute, but use the same safe writer so the
	// rare profile-restore-from-backup case can't trip on perms either.
	return writeFileOverwritable(path, []byte(content))
}

// SeedFromDocuments copies each managed INI from the game's
// Documents/My Games/{subdir}/ into the profile's ini dir when the profile
// copy is missing. Called on the first editor open so users see their
// current game INIs instead of blank tabs. Missing source files are skipped
// silently (game hasn't been launched yet, etc.).
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
		if _, err := os.Stat(dst); err == nil {
			continue // already have a profile copy
		}
		src := filepath.Join(docsDir, name)
		if err := copyFileIfExists(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// PushToDocuments copies every profile INI into the game's
// Documents/My Games/{subdir}/ directory, overwriting the existing files.
// Called by the daemon at launch time for profiles with UseCustomIni=true.
//
// For engines whose runtime reads {Game}Custom.ini natively (Skyrim SE,
// Fallout 4, Starfield), the Custom.ini is copied through as-is alongside
// the primary INI. For the older engines (Oblivion, Skyrim LE, Fallout 3,
// Fallout NV) — which ignore Custom.ini by themselves — the profile's
// Custom.ini is merged into the primary INI in memory and only the merged
// result is written to disk. This mirrors MO2's virtualized behavior so
// the user's tweaks take effect regardless of engine generation.
//
// Returns one PushedIniReport per managed INI (including skipped ones, so
// the caller can verify every file). Verified=false on any row means the
// post-write stat didn't match the write — the caller should refuse to
// launch in that case, since silently pressing on is what produced the
// "custom INIs ignored" symptom.
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

	// Precompute the Custom.ini merge for non-native engines. When the
	// profile has no Custom.ini or it's empty, this is a no-op.
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

		// Non-native Custom.ini: folded into primary, not written separately.
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
				// Fall back to the game's existing file so we don't wipe it.
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

// verifyPushed re-stats the just-written target and compares against the
// bytes we intended to write. A mismatch here means the Proton prefix
// redirected the write elsewhere (layout mismatch, permissions, symlink
// chain) — exactly the failure mode behind "custom INIs ignored".
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

// overlayKeySummary renders "[Section] Key=Value; ..." for every entry in an
// overlay document. Used at launch-log time so the daemon output makes it
// trivially obvious which tweaks the profile is about to apply.
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

// mergedKey fetches a single (section, key) out of a document for logging.
// Returns "<unset>" when the key isn't present, so the log line reads
// unambiguously without caller-side branching.
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

// writeFileOverwritable writes data to path, transparently handling the
// case where path exists but has been marked read-only — which is what
// happens inside a Wine prefix whenever Windows has flipped the DOS
// read-only attribute on an INI file (Bethesda launchers love doing this
// to "protect" their configs). A plain os.WriteFile returns EACCES in
// that scenario even though the user owns the file. We stat, chmod the
// user-write bit back on if needed, and only then write.
func writeFileOverwritable(path string, data []byte) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&0200 == 0 {
			// u+w stripped (e.g. 0444 from Wine's DOS read-only mapping) —
			// restore to 0644 so the subsequent open-for-write succeeds.
			if chErr := os.Chmod(path, 0644); chErr != nil {
				return fmt.Errorf("clearing read-only on %s: %w", path, chErr)
			}
		}
	}
	return os.WriteFile(path, data, 0644)
}

// ReadDocument loads an INI file out of a profile and returns it as a
// parsed Document. Missing files yield an empty Document.
func (m *Manager) ReadDocument(gameID, profileName, filename string) (*Document, error) {
	content, err := m.Read(gameID, profileName, filename)
	if err != nil {
		return nil, err
	}
	return ParseDocument(content), nil
}

// WriteDocument serializes and writes an INI document back to a profile.
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

// ListTweaks returns every preset available for the game paired with its
// current applied-ness against the profile's Custom.ini.
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

// SetTweak applies or removes a tweak preset in the profile's Custom.ini.
// Returns the updated state.
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
	// Same read-only dance as writeFileOverwritable: if dst exists with
	// u-w stripped (Wine DOS read-only attribute), restore it first so
	// os.Create doesn't fail with EACCES on files the user owns.
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
