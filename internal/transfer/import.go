package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
)

type importLimits struct {
	entries       int64
	fileBytes     int64
	payloadBytes  int64
	streamBytes   int64
	manifestBytes int64
}

// defaultImportLimits returns the maximum sizes accepted from an import bundle.
func defaultImportLimits() importLimits {
	return importLimits{
		entries:       500_000,
		fileBytes:     8 << 30,
		payloadBytes:  32 << 30,
		streamBytes:   40 << 30,
		manifestBytes: 64 << 20,
	}
}

type ImportOptions struct {
	limits             *importLimits
	commitOps          *transferCommitOps
	GameID             string
	ArchivePath        string
	Policy             dto.CollisionPolicy
	ModPolicyOverrides map[string]dto.CollisionPolicy
	ModFolders         []string
	ProfileNames       []string
	LockMod            func(name string) func()
	LockProfiles       func() func()
}

// ReadManifest opens an archive and returns its validated manifest without extracting anything.
func ReadManifest(ctx context.Context, gameID, archivePath string) (*Manifest, error) {
	tr, closer, err := openArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer closer()
	m, _, err := readManifestEntry(ctx, tr, gameID, defaultImportLimits().manifestBytes)
	return m, err
}

type manifestContextReader struct {
	ctx context.Context
	r   io.Reader
}

// Read returns the next manifest bytes unless validation was cancelled.
func (r manifestContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// readManifestEntry consumes the first tar entry, requiring a size-bounded valid manifest for gameID.
func readManifestEntry(ctx context.Context, tr *tar.Reader, gameID string, maxBytes int64) (*Manifest, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	hdr, err := tr.Next()
	if err != nil {
		return nil, 0, fmt.Errorf("reading archive: %w", err)
	}
	if err := validateEntryType(hdr); err != nil {
		return nil, 0, err
	}
	if hdr.Name != manifestEntryName || (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) {
		return nil, 0, &TransferPathError{Entry: hdr.Name}
	}
	if hdr.Size < 0 || hdr.Size > maxBytes {
		return nil, 0, &BundleRejectedError{Reason: BundleRejectedLimit, Item: manifestEntryName}
	}
	data, err := io.ReadAll(manifestContextReader{ctx: ctx, r: io.LimitReader(tr, hdr.Size)})
	if err != nil {
		return nil, 0, fmt.Errorf("reading manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m, err := DecodeManifest(data)
	if err != nil {
		return nil, 0, err
	}
	if m.SchemaVersion < 1 || m.SchemaVersion > SchemaVersion {
		return nil, 0, &TransferSchemaError{Version: m.SchemaVersion}
	}
	if m.GameID != gameID {
		return nil, 0, &TransferGameMismatchError{Want: gameID, Got: m.GameID}
	}
	if len(m.Mods) > 100_000 || len(m.Profiles) > 100_000 {
		return nil, 0, &BundleRejectedError{Reason: BundleRejectedLimit, Item: manifestEntryName}
	}
	seenMods := make(map[string]string, len(m.Mods))
	seenFolds := make(map[string]string, len(m.Mods))
	for _, me := range m.Mods {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if err := download.ValidateTargetModName(me.Folder); err != nil {
			return nil, 0, err
		}
		key := strings.ToLower(me.Folder)
		if folder, ok := seenMods[key]; ok && strings.EqualFold(folder, me.Folder) {
			return nil, 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: me.Folder}
		}
		fold := simpleFoldKey(me.Folder)
		if folder, ok := seenFolds[fold]; ok && strings.EqualFold(folder, me.Folder) {
			return nil, 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: me.Folder}
		}
		seenMods[key] = me.Folder
		seenFolds[fold] = me.Folder
	}
	seenProfiles := make(map[string]bool, len(m.Profiles))
	for _, name := range m.Profiles {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if err := validateImportedProfileName(name); err != nil {
			return nil, 0, err
		}
		if seenProfiles[name] {
			return nil, 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: name}
		}
		seenProfiles[name] = true
	}
	return m, hdr.Size, nil
}

// simpleFoldKey maps equivalent Unicode spellings to the same mod identity key.
func simpleFoldKey(name string) string {
	return strings.Map(func(r rune) rune {
		min := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < min {
				min = next
			}
		}
		return min
	}, name)
}

// validateImportedProfileName rejects unsafe or reserved profile names from a bundle.
func validateImportedProfileName(name string) error {
	if fsutil.ValidateName(name) != nil || strings.HasPrefix(name, ".") || strings.EqualFold(name, profile.OverwriteModName) || strings.EqualFold(name, "Downloads") {
		return &BundleRejectedError{Reason: BundleRejectedProfileName, Item: name}
	}
	return nil
}

// validateCollisionPolicy rejects unknown import collision policies.
func validateCollisionPolicy(policy dto.CollisionPolicy) error {
	switch policy {
	case dto.PolicyAbort, dto.PolicySkip, dto.PolicyRename, dto.PolicyOverwrite:
		return nil
	default:
		return &BundleRejectedError{Reason: BundleRejectedManifest, Item: fmt.Sprint(policy)}
	}
}

// Preview reads an archive's manifest and reports per-item collisions against the target instance.
func Preview(ctx context.Context, gameID, archivePath string) (dto.ImportPreview, error) {
	m, err := ReadManifest(ctx, gameID, archivePath)
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
		if err := ctx.Err(); err != nil {
			return dto.ImportPreview{}, err
		}
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
		if err := ctx.Err(); err != nil {
			return dto.ImportPreview{}, err
		}
		out.Profiles = append(out.Profiles, dto.ImportPreviewProfile{
			Name:      name,
			Collision: profileExists(gameID, name),
		})
	}
	return out, nil
}

// Import applies an exported archive to the target instance under the configured collision policies.
func Import(ctx context.Context, opts ImportOptions, emit func(dto.TransferProgress)) (summary dto.TransferSummary, importErr error) {
	summary = dto.TransferSummary{Renamed: map[string]string{}}
	mergedFiles := 0
	defer func() {
		if importErr == nil {
			return
		}
		var commitErr *transferCommitError
		committed := int(summary.ModsImported+summary.ProfilesTransferred) + mergedFiles
		if committed > 0 {
			recovery := "none"
			if errors.As(importErr, &commitErr) && commitErr.pending {
				recovery = "pending"
			}
			importErr = &BundleIncompleteError{Items: committed, Recovery: recovery, Err: importErr}
		}
	}()
	if emit == nil {
		emit = func(dto.TransferProgress) {}
	}

	limits := defaultImportLimits()
	if opts.limits != nil {
		limits = *opts.limits
	}
	tr, stream, closer, err := openArchiveReaderWithLimit(opts.ArchivePath, limits.streamBytes)
	if err != nil {
		return summary, err
	}
	defer closer()

	manifest, manifestSize, err := readManifestEntry(ctx, tr, opts.GameID, limits.manifestBytes)
	if err != nil {
		return summary, err
	}
	if limits.entries < 1 || manifestSize > limits.payloadBytes {
		return summary, &BundleRejectedError{Reason: BundleRejectedLimit, Item: manifestEntryName}
	}
	entryCount := int64(1)
	payloadBytes := manifestSize
	if err := validateCollisionPolicy(opts.Policy); err != nil {
		return summary, err
	}
	for _, policy := range opts.ModPolicyOverrides {
		if err := validateCollisionPolicy(policy); err != nil {
			return summary, err
		}
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
		if me.TotalBytes > limits.payloadBytes-bytesTotal {
			bytesTotal = limits.payloadBytes
		} else if me.TotalBytes > 0 {
			bytesTotal += me.TotalBytes
		}
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
	stageMods, err := os.MkdirTemp(modsDir, mod.ImportStagePrefix)
	if err != nil {
		return summary, fmt.Errorf("creating import staging directory: %w", err)
	}
	defer os.RemoveAll(stageMods)
	stageProfiles := filepath.Join(profilesDir, filepath.Base(stageMods))
	if err := os.Mkdir(stageProfiles, 0700); err != nil {
		return summary, fmt.Errorf("creating profile staging directory: %w", err)
	}
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
	seenEntries := map[string]byte{}
	copyBuffer := make([]byte, 1<<20)

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
		stream.item = hdr.Name
		if entryCount >= limits.entries {
			return summary, &BundleRejectedError{Reason: BundleRejectedLimit, Item: hdr.Name}
		}
		entryCount++
		prefix, rest, err := splitEntryName(hdr.Name)
		if err != nil {
			return summary, err
		}
		if err := validateEntryType(hdr); err != nil {
			return summary, err
		}
		remainingPayload := limits.payloadBytes - payloadBytes
		if hdr.Size < 0 || hdr.Size > remainingPayload || (hdr.Typeflag != tar.TypeDir && hdr.Size > limits.fileBytes) {
			return summary, &BundleRejectedError{Reason: BundleRejectedLimit, Item: hdr.Name}
		}
		payloadBytes += hdr.Size
		if (prefix == "overwrite" && !manifest.IncludesOverwrite) || (prefix == "gamesettings" && !manifest.IncludesGameSettings) {
			return summary, &BundleRejectedError{Reason: BundleRejectedManifest, Item: hdr.Name}
		}
		if prefix != manifestEntryName {
			if err := recordEntry(seenEntries, hdr); err != nil {
				return summary, err
			}
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
			n, err := extractEntry(ctx, tr, hdr, stageMods, strings.TrimPrefix(clean, "mods/"), limits.fileBytes, remainingPayload, copyBuffer)
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
			if rest == name+"/profile.json" && hdr.Size > 1<<20 {
				return summary, &BundleRejectedError{Reason: BundleRejectedLimit, Item: name + "/profile.json"}
			}
			if _, err := extractEntry(ctx, tr, hdr, stageProfiles, strings.TrimPrefix(clean, "profiles/"), limits.fileBytes, remainingPayload, copyBuffer); err != nil {
				return summary, err
			}
			progress("extract", clean)
		case "overwrite":
			if _, err := extractEntry(ctx, tr, hdr, filepath.Join(stageMods, "__overwrite__"), rest, limits.fileBytes, remainingPayload, copyBuffer); err != nil {
				return summary, err
			}
			progress("extract", clean)
		case "gamesettings":
			if rest != gsBase {
				return summary, &TransferPathError{Entry: hdr.Name}
			}
			if _, err := extractEntry(ctx, tr, hdr, filepath.Join(stageMods, "__gamesettings__"), rest, limits.fileBytes, remainingPayload, copyBuffer); err != nil {
				return summary, err
			}
		default:
			return summary, &TransferPathError{Entry: hdr.Name}
		}
	}

	for _, me := range manifest.Mods {
		if !selMods[me.Folder] || skipMods[me.Folder] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if err := checkStagedRoot(filepath.Join(stageMods, me.Folder), "mods/"+me.Folder); err != nil {
			return summary, err
		}
	}
	for _, name := range manifest.Profiles {
		if !selProfiles[name] || skipProfiles[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if err := checkStagedRoot(filepath.Join(stageProfiles, name), "profiles/"+name); err != nil {
			return summary, err
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
		if err := finalizeProfile(opts, name, staged, &summary); err != nil {
			return summary, err
		}
		itemsDone++
		progress("finalize", name)
	}

	merged, err := mergeOverwriteCount(filepath.Join(stageMods, "__overwrite__"), filepath.Join(modsDir, profile.OverwriteModName))
	mergedFiles += merged
	if err != nil {
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

// checkStagedRoot requires a selected bundle root to be a real directory.
func checkStagedRoot(path, item string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return &BundleRejectedError{Reason: BundleRejectedManifest, Item: item}
	}
	if err != nil {
		return fmt.Errorf("checking staged bundle root %s: %w", item, err)
	}
	return nil
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
			if err := relabelModMetadata(staged, folder, newName); err != nil {
				return fmt.Errorf("preparing mod %q: %w", folder, err)
			}
			if err := os.Rename(staged, filepath.Join(config.ModsDir(opts.GameID), newName)); err != nil {
				return fmt.Errorf("importing mod %q as %q: %w", folder, newName, err)
			}
			summary.Renamed[folder] = newName
			summary.ModsImported++
			return nil
		case dto.PolicyOverwrite:
			var replace error
			if opts.commitOps != nil {
				replace = replaceDirWithOps(config.ModsDir(opts.GameID), folder, staged, "mod", *opts.commitOps)
			} else {
				replace = replaceDir(config.ModsDir(opts.GameID), folder, staged)
			}
			if err := replace; err != nil {
				var commitErr *transferCommitError
				if errors.As(err, &commitErr) && commitErr.committed {
					summary.ModsImported++
				}
				return fmt.Errorf("replacing mod %q: %w", folder, err)
			}
			summary.ModsImported++
			return nil
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
	if err := validateImportedProfileName(name); err != nil {
		return err
	}
	if err := rewriteModlist(filepath.Join(staged, "modlist.txt"), summary.Renamed); err != nil {
		return err
	}
	unlock := func() {}
	if opts.LockProfiles != nil {
		unlock = opts.LockProfiles()
	}
	defer unlock()
	target := filepath.Join(config.ProfilesDir(opts.GameID), name)
	finalName := name
	collision := profileExists(opts.GameID, name)
	if collision {
		switch opts.Policy {
		case dto.PolicySkip:
			summary.Skipped = append(summary.Skipped, name)
			return nil
		case dto.PolicyRename:
			finalName = renameCandidate(name, func(c string) bool {
				return profileExists(opts.GameID, c)
			})
			target = filepath.Join(config.ProfilesDir(opts.GameID), finalName)
		case dto.PolicyOverwrite:
		default:
			return &TransferCollisionError{Name: name}
		}
	}
	if err := canonicalizeProfileJSON(staged, opts.GameID, finalName); err != nil {
		return fmt.Errorf("preparing profile %q: %w", name, err)
	}
	if collision && opts.Policy == dto.PolicyOverwrite {
		var replace error
		if opts.commitOps != nil {
			replace = replaceDirWithOps(config.ProfilesDir(opts.GameID), name, staged, "profile", *opts.commitOps)
		} else {
			replace = replaceDir(config.ProfilesDir(opts.GameID), name, staged)
		}
		if err := replace; err != nil {
			var commitErr *transferCommitError
			if errors.As(err, &commitErr) && commitErr.committed {
				summary.ProfilesTransferred++
			}
			return fmt.Errorf("replacing profile %q: %w", name, err)
		}
		summary.ProfilesTransferred++
		return nil
	}
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("importing profile %q: %w", name, err)
	}
	if finalName != name {
		summary.Renamed[name] = finalName
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
	return atomicfile.WriteFile(path, buf.Bytes(), 0644)
}

// mergeOverwrite moves every staged Overwrite file into the live Overwrite layer, replacing on conflict.
func mergeOverwrite(stagedRoot, owDir string) error {
	_, err := mergeOverwriteCount(stagedRoot, owDir)
	return err
}

// mergeOverwriteCount moves staged Overwrite files and counts successful file replacements.
func mergeOverwriteCount(stagedRoot, owDir string) (int, error) {
	if _, err := os.Stat(stagedRoot); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	err := filepath.WalkDir(stagedRoot, func(p string, d fs.DirEntry, err error) error {
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
		if err := fsutil.CheckExistingPath(owDir, rel); err != nil {
			if errors.Is(err, fsutil.ErrExistingLink) {
				return &BundleRejectedError{Reason: BundleRejectedLink, Item: filepath.ToSlash(rel)}
			}
			if errors.Is(err, fsutil.ErrExistingNonDirectory) {
				return &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: filepath.ToSlash(rel)}
			}
			return err
		}
		dest := filepath.Join(owDir, rel)
		info, err := os.Lstat(dest)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.IsDir() != d.IsDir() || !info.IsDir() && !info.Mode().IsRegular()) {
			return &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: filepath.ToSlash(rel)}
		}
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.Rename(p, dest); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

// relabelModMetadata rewrites a staged mod's metadata.yaml folder and name after a rename.
func relabelModMetadata(staged, oldName, newName string) error {
	meta, err := download.LoadModMetadata(staged)
	if err != nil {
		return fmt.Errorf("reading staged metadata: %w", err)
	}
	if meta == nil || meta.Folder == "" && meta.Name == "" {
		return nil
	}
	meta.Folder = newName
	if meta.Name == oldName {
		meta.Name = newName
	}
	if err := download.SaveModMetadata(staged, meta); err != nil {
		return fmt.Errorf("writing staged metadata: %w", err)
	}
	return nil
}

// canonicalizeProfileJSON writes the final directory identity into staged profile.json.
func canonicalizeProfileJSON(staged, gameID, name string) error {
	path := filepath.Join(staged, "profile.json")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	if stat.Size() > 1<<20 {
		return &BundleRejectedError{Reason: BundleRejectedLimit, Item: filepath.Base(staged) + "/profile.json"}
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) > 1<<20 {
		return &BundleRejectedError{Reason: BundleRejectedLimit, Item: filepath.Base(staged) + "/profile.json"}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if raw == nil {
		return fmt.Errorf("parsing %s: expected a JSON object", path)
	}
	nameJSON, err := json.Marshal(name)
	if err != nil {
		return fmt.Errorf("encoding profile name: %w", err)
	}
	gameJSON, err := json.Marshal(gameID)
	if err != nil {
		return fmt.Errorf("encoding profile game ID: %w", err)
	}
	raw["name"] = nameJSON
	raw["game_id"] = gameJSON
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	if err := atomicfile.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
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
