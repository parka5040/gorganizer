package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
)

type ImportOptions struct {
	GameID             string
	ArchivePath        string
	Policy             dto.CollisionPolicy
	ModPolicyOverrides map[string]dto.CollisionPolicy
	ModFolders         []string
	ProfileNames       []string
	LockMod            func(name string) func()
}

// ReadManifest opens an archive and returns its validated manifest without extracting anything.
func ReadManifest(gameID, archivePath string) (*Manifest, error) {
	tr, closer, err := openArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer closer()
	return readManifestEntry(tr, gameID)
}

// readManifestEntry consumes the first tar entry, requiring a valid manifest for gameID.
func readManifestEntry(tr *tar.Reader, gameID string) (*Manifest, error) {
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}
	if hdr.Name != manifestEntryName || hdr.Typeflag != tar.TypeReg {
		return nil, &TransferPathError{Entry: hdr.Name}
	}
	data, err := io.ReadAll(io.LimitReader(tr, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	m, err := DecodeManifest(data)
	if err != nil {
		return nil, err
	}
	if m.SchemaVersion < 1 || m.SchemaVersion > SchemaVersion {
		return nil, &TransferSchemaError{Version: m.SchemaVersion}
	}
	if m.GameID != gameID {
		return nil, &TransferGameMismatchError{Want: gameID, Got: m.GameID}
	}
	return m, nil
}

// Preview reads an archive's manifest and reports per-item collisions against the target instance.
func Preview(gameID, archivePath string) (dto.ImportPreview, error) {
	m, err := ReadManifest(gameID, archivePath)
	if err != nil {
		return dto.ImportPreview{}, err
	}
	out := dto.ImportPreview{
		SchemaVersion:        int32(m.SchemaVersion),
		GorganizerVersion:    m.GorganizerVersion,
		GameID:               m.GameID,
		ExportedAt:           m.ExportedAt.UTC().Format(time.RFC3339),
		IncludesOverwrite:    m.IncludesOverwrite,
		IncludesGameSettings: m.IncludesGameSettings,
	}
	for _, me := range m.Mods {
		out.Mods = append(out.Mods, dto.ImportPreviewMod{
			Folder:      me.Folder,
			Name:        me.Name,
			FileCount:   int32(me.FileCount),
			TotalBytes:  me.TotalBytes,
			NexusModID:  int32(me.NexusModID),
			NexusFileID: int32(me.NexusFileID),
			Collision:   modFolderExists(gameID, me.Folder),
		})
	}
	for _, name := range m.Profiles {
		out.Profiles = append(out.Profiles, dto.ImportPreviewProfile{
			Name:      name,
			Collision: profileExists(gameID, name),
		})
	}
	return out, nil
}

// Import applies an exported archive to the target instance under the configured collision policies.
func Import(ctx context.Context, opts ImportOptions, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	summary := dto.TransferSummary{Renamed: map[string]string{}}
	if emit == nil {
		emit = func(dto.TransferProgress) {}
	}

	tr, closer, err := openArchiveReader(opts.ArchivePath)
	if err != nil {
		return summary, err
	}
	defer closer()

	manifest, err := readManifestEntry(tr, opts.GameID)
	if err != nil {
		return summary, err
	}

	selMods, err := selectNames(manifestModFolders(manifest), opts.ModFolders, "mod folder")
	if err != nil {
		return summary, err
	}
	selProfiles, err := selectNames(manifest.Profiles, opts.ProfileNames, "profile")
	if err != nil {
		return summary, err
	}

	policyFor := func(folder string) dto.CollisionPolicy {
		if p, ok := opts.ModPolicyOverrides[folder]; ok {
			return p
		}
		return opts.Policy
	}
	for _, me := range manifest.Mods {
		if selMods[me.Folder] && modFolderExists(opts.GameID, me.Folder) && policyFor(me.Folder) == dto.PolicyAbort {
			return summary, &TransferCollisionError{Name: me.Folder}
		}
	}
	for _, name := range manifest.Profiles {
		if selProfiles[name] && profileExists(opts.GameID, name) && opts.Policy == dto.PolicyAbort {
			return summary, &TransferCollisionError{Name: name}
		}
	}

	skipMods := map[string]bool{}
	var bytesTotal int64
	for _, me := range manifest.Mods {
		if !selMods[me.Folder] {
			continue
		}
		if modFolderExists(opts.GameID, me.Folder) && policyFor(me.Folder) == dto.PolicySkip {
			skipMods[me.Folder] = true
			continue
		}
		bytesTotal += me.TotalBytes
	}
	skipProfiles := map[string]bool{}
	for _, name := range manifest.Profiles {
		if selProfiles[name] && profileExists(opts.GameID, name) && opts.Policy == dto.PolicySkip {
			skipProfiles[name] = true
		}
	}

	modsDir := config.ModsDir(opts.GameID)
	profilesDir := config.ProfilesDir(opts.GameID)
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		return summary, err
	}
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		return summary, err
	}
	stageID := mod.ImportStagePrefix + uuid.NewString()
	stageMods := filepath.Join(modsDir, stageID)
	stageProfiles := filepath.Join(profilesDir, stageID)
	defer os.RemoveAll(stageMods)
	defer os.RemoveAll(stageProfiles)

	itemsTotal := int32(len(selMods) + len(selProfiles))
	itemsDone := int32(0)
	var bytesDone int64
	progress := func(step, item string) {
		emit(dto.TransferProgress{
			Step: step, CurrentItem: item,
			ItemsDone: itemsDone, ItemsTotal: itemsTotal,
			BytesDone: bytesDone, BytesTotal: bytesTotal,
		})
	}

	gsBase := filepath.Base(config.GameSettingsPath(opts.GameID))
	manifestMods := map[string]bool{}
	for _, me := range manifest.Mods {
		manifestMods[me.Folder] = true
	}
	manifestProfiles := map[string]bool{}
	for _, name := range manifest.Profiles {
		manifestProfiles[name] = true
	}

	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return summary, fmt.Errorf("reading archive: %w", err)
		}
		prefix, rest, err := splitEntryName(hdr.Name)
		if err != nil {
			return summary, err
		}
		clean := strings.TrimSuffix(hdr.Name, "/")
		switch prefix {
		case "mods":
			folder, _, _ := strings.Cut(rest, "/")
			if folder == "" || !manifestMods[folder] {
				return summary, &TransferPathError{Entry: hdr.Name}
			}
			if !selMods[folder] || skipMods[folder] {
				continue
			}
			n, err := extractEntry(tr, hdr, stageMods, strings.TrimPrefix(clean, "mods/"))
			if err != nil {
				return summary, err
			}
			bytesDone += n
			progress("extract", clean)
		case "profiles":
			name, _, _ := strings.Cut(rest, "/")
			if name == "" || !manifestProfiles[name] {
				return summary, &TransferPathError{Entry: hdr.Name}
			}
			if !selProfiles[name] || skipProfiles[name] {
				continue
			}
			if _, err := extractEntry(tr, hdr, stageProfiles, strings.TrimPrefix(clean, "profiles/")); err != nil {
				return summary, err
			}
			progress("extract", clean)
		case "overwrite":
			if _, err := extractEntry(tr, hdr, filepath.Join(stageMods, "__overwrite__"), rest); err != nil {
				return summary, err
			}
			progress("extract", clean)
		case "gamesettings":
			if rest != gsBase {
				return summary, &TransferPathError{Entry: hdr.Name}
			}
			if _, err := extractEntry(tr, hdr, filepath.Join(stageMods, "__gamesettings__"), rest); err != nil {
				return summary, err
			}
		default:
			return summary, &TransferPathError{Entry: hdr.Name}
		}
	}

	for _, me := range manifest.Mods {
		if !selMods[me.Folder] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if skipMods[me.Folder] {
			summary.Skipped = append(summary.Skipped, me.Folder)
			itemsDone++
			continue
		}
		staged := filepath.Join(stageMods, me.Folder)
		if _, err := os.Stat(staged); err != nil {
			return summary, fmt.Errorf("archive has no data for mod %q: %w", me.Folder, os.ErrNotExist)
		}
		if err := finalizeMod(opts, me.Folder, staged, policyFor(me.Folder), &summary); err != nil {
			return summary, err
		}
		itemsDone++
		progress("finalize", me.Folder)
	}

	for _, name := range manifest.Profiles {
		if !selProfiles[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if skipProfiles[name] {
			summary.Skipped = append(summary.Skipped, name)
			itemsDone++
			continue
		}
		staged := filepath.Join(stageProfiles, name)
		if _, err := os.Stat(staged); err != nil {
			return summary, fmt.Errorf("archive has no data for profile %q: %w", name, os.ErrNotExist)
		}
		if err := finalizeProfile(opts, name, staged, &summary); err != nil {
			return summary, err
		}
		itemsDone++
		progress("finalize", name)
	}

	if err := mergeOverwrite(filepath.Join(stageMods, "__overwrite__"), filepath.Join(modsDir, profile.OverwriteModName)); err != nil {
		return summary, err
	}
	stagedGS := filepath.Join(stageMods, "__gamesettings__", gsBase)
	if _, err := os.Stat(stagedGS); err == nil {
		if err := os.Rename(stagedGS, config.GameSettingsPath(opts.GameID)); err != nil {
			return summary, fmt.Errorf("applying game settings: %w", err)
		}
	}

	progress("done", "")
	return summary, nil
}

// finalizeMod moves one staged mod into ModsDir, applying the collision policy under the install lock.
func finalizeMod(opts ImportOptions, folder, staged string, policy dto.CollisionPolicy, summary *dto.TransferSummary) error {
	unlock := func() {}
	if opts.LockMod != nil {
		unlock = opts.LockMod(folder)
	}
	defer unlock()

	target := filepath.Join(config.ModsDir(opts.GameID), folder)
	if _, err := os.Stat(target); err == nil {
		switch policy {
		case dto.PolicySkip:
			summary.Skipped = append(summary.Skipped, folder)
			return nil
		case dto.PolicyRename:
			newName := renameCandidate(folder, func(c string) bool {
				return modFolderExists(opts.GameID, c)
			})
			relabelModMetadata(staged, folder, newName)
			if err := os.Rename(staged, filepath.Join(config.ModsDir(opts.GameID), newName)); err != nil {
				return fmt.Errorf("importing mod %q as %q: %w", folder, newName, err)
			}
			summary.Renamed[folder] = newName
			summary.ModsImported++
			return nil
		case dto.PolicyOverwrite:
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("replacing mod %q: %w", folder, err)
			}
		default:
			return &TransferCollisionError{Name: folder}
		}
	}
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("importing mod %q: %w", folder, err)
	}
	summary.ModsImported++
	return nil
}

// finalizeProfile rewrites the staged modlist through the rename map and moves the profile into place.
func finalizeProfile(opts ImportOptions, name, staged string, summary *dto.TransferSummary) error {
	if err := rewriteModlist(filepath.Join(staged, "modlist.txt"), summary.Renamed); err != nil {
		return err
	}
	target := filepath.Join(config.ProfilesDir(opts.GameID), name)
	if _, err := os.Stat(target); err == nil {
		switch opts.Policy {
		case dto.PolicySkip:
			summary.Skipped = append(summary.Skipped, name)
			return nil
		case dto.PolicyRename:
			newName := renameCandidate(name, func(c string) bool {
				return profileExists(opts.GameID, c)
			})
			relabelProfileJSON(staged, newName)
			if err := os.Rename(staged, filepath.Join(config.ProfilesDir(opts.GameID), newName)); err != nil {
				return fmt.Errorf("importing profile %q as %q: %w", name, newName, err)
			}
			summary.Renamed[name] = newName
			summary.ProfilesTransferred++
			return nil
		case dto.PolicyOverwrite:
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("replacing profile %q: %w", name, err)
			}
		default:
			return &TransferCollisionError{Name: name}
		}
	}
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("importing profile %q: %w", name, err)
	}
	summary.ProfilesTransferred++
	return nil
}

// rewriteModlist maps renamed mod folders through a staged profile's modlist.txt.
func rewriteModlist(path string, renamed map[string]string) error {
	if len(renamed) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries, err := mod.ParseModList(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("rewriting %s: %w", path, err)
	}
	changed := false
	for i := range entries {
		if newName, ok := renamed[entries[i].Name]; ok {
			entries[i].Name = newName
			changed = true
		}
	}
	if !changed {
		return nil
	}
	var buf bytes.Buffer
	if err := mod.WriteModList(&buf, entries); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// mergeOverwrite moves every staged Overwrite file into the live Overwrite layer, replacing on conflict.
func mergeOverwrite(stagedRoot, owDir string) error {
	if _, err := os.Stat(stagedRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(stagedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stagedRoot, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(owDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		return os.Rename(p, dest)
	})
}

// relabelModMetadata rewrites a staged mod's metadata.yaml folder/name after a RENAME, best-effort.
func relabelModMetadata(staged, oldName, newName string) {
	meta, err := download.LoadModMetadata(staged)
	if err != nil || meta == nil {
		return
	}
	if meta.Folder == "" && meta.Name == "" {
		return
	}
	meta.Folder = newName
	if meta.Name == oldName {
		meta.Name = newName
	}
	_ = download.SaveModMetadata(staged, meta)
}

// relabelProfileJSON rewrites a staged profile.json name after a RENAME, best-effort.
func relabelProfileJSON(staged, newName string) {
	path := filepath.Join(staged, "profile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	raw["name"] = newName
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, out, 0644)
}

// renameCandidate returns "<base> (2)", "<base> (3)", ... skipping taken names.
func renameCandidate(base string, taken func(string) bool) string {
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", base, i)
		if !taken(candidate) {
			return candidate
		}
	}
}

// selectNames intersects a requested subset with the archive's items; empty request selects all.
func selectNames(available, requested []string, kind string) (map[string]bool, error) {
	availSet := map[string]bool{}
	for _, name := range available {
		availSet[name] = true
	}
	if len(requested) == 0 {
		return availSet, nil
	}
	out := map[string]bool{}
	for _, name := range requested {
		if !availSet[name] {
			return nil, fmt.Errorf("%s %q not in archive: %w", kind, name, os.ErrNotExist)
		}
		out[name] = true
	}
	return out, nil
}

// manifestModFolders lists the manifest's mod folder names.
func manifestModFolders(m *Manifest) []string {
	out := make([]string, 0, len(m.Mods))
	for _, me := range m.Mods {
		out = append(out, me.Folder)
	}
	return out
}
