package transfer

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
)

type ExportOptions struct {
	GameID              string
	OutputPath          string
	ModFolders          []string
	ProfileNames        []string
	IncludeOverwrite    bool
	IncludeGameSettings bool
	LockMod             func(name string) func()
}

// Export streams the selected slice of a game instance into a tar+zstd archive at OutputPath.
func Export(ctx context.Context, opts ExportOptions, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	summary := dto.TransferSummary{OutputPath: opts.OutputPath}
	if emit == nil {
		emit = func(dto.TransferProgress) {}
	}

	folders, err := resolveExportMods(opts.GameID, opts.ModFolders)
	if err != nil {
		return summary, err
	}
	profiles, err := resolveExportProfiles(opts.GameID, opts.ProfileNames)
	if err != nil {
		return summary, err
	}

	manifest := &Manifest{
		SchemaVersion:        SchemaVersion,
		GorganizerVersion:    GorganizerVersion,
		GameID:               opts.GameID,
		ExportedAt:           time.Now().UTC(),
		Profiles:             profiles,
		IncludesOverwrite:    opts.IncludeOverwrite,
		IncludesGameSettings: opts.IncludeGameSettings,
	}
	var bytesTotal int64
	for _, folder := range folders {
		emit(dto.TransferProgress{Step: "scan", CurrentItem: folder, ItemsTotal: int32(len(folders) + len(profiles))})
		entry, err := buildModEntry(opts.GameID, folder)
		if err != nil {
			return summary, err
		}
		manifest.Mods = append(manifest.Mods, entry)
		bytesTotal += entry.TotalBytes
	}
	manifestBytes, err := EncodeManifest(manifest)
	if err != nil {
		return summary, err
	}

	if dir := filepath.Dir(opts.OutputPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return summary, fmt.Errorf("creating output directory: %w", err)
		}
	}
	out, err := os.Create(opts.OutputPath)
	if err != nil {
		return summary, fmt.Errorf("creating archive %s: %w", opts.OutputPath, err)
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(opts.OutputPath)
		}
	}()
	zw, err := zstd.NewWriter(out)
	if err != nil {
		out.Close()
		return summary, fmt.Errorf("creating zstd writer: %w", err)
	}
	tw := tar.NewWriter(zw)

	itemsTotal := int32(len(folders) + len(profiles))
	itemsDone := int32(0)
	var bytesDone int64
	progress := func(step, item string) {
		emit(dto.TransferProgress{
			Step: step, CurrentItem: item,
			ItemsDone: itemsDone, ItemsTotal: itemsTotal,
			BytesDone: bytesDone, BytesTotal: bytesTotal,
		})
	}

	writeAll := func() error {
		if err := writeTarBytes(tw, manifestEntryName, manifestBytes, manifest.ExportedAt); err != nil {
			return err
		}
		modsDir := config.ModsDir(opts.GameID)
		for _, folder := range folders {
			if err := ctx.Err(); err != nil {
				return err
			}
			progress("mods", folder)
			unlock := func() {}
			if opts.LockMod != nil {
				unlock = opts.LockMod(folder)
			}
			err := writeTarTree(tw, filepath.Join(modsDir, folder), "mods/"+folder, func(rel string, size int64) {
				bytesDone += size
				progress("mods", rel)
			})
			unlock()
			if err != nil {
				return fmt.Errorf("exporting mod %q: %w", folder, err)
			}
			itemsDone++
			summary.ModsExported++
		}
		if opts.IncludeOverwrite {
			owDir := filepath.Join(modsDir, profile.OverwriteModName)
			if _, err := os.Stat(owDir); err == nil {
				progress("overwrite", profile.OverwriteModName)
				if err := writeTarTree(tw, owDir, "overwrite", nil); err != nil {
					return fmt.Errorf("exporting overwrite: %w", err)
				}
			}
		}
		for _, name := range profiles {
			if err := ctx.Err(); err != nil {
				return err
			}
			progress("profiles", name)
			dir := filepath.Join(config.ProfilesDir(opts.GameID), name)
			if err := writeTarTree(tw, dir, "profiles/"+name, nil); err != nil {
				return fmt.Errorf("exporting profile %q: %w", name, err)
			}
			itemsDone++
			summary.ProfilesTransferred++
		}
		if opts.IncludeGameSettings {
			gsPath := config.GameSettingsPath(opts.GameID)
			if data, err := os.ReadFile(gsPath); err == nil {
				progress("gamesettings", filepath.Base(gsPath))
				if err := writeTarBytes(tw, "gamesettings/"+filepath.Base(gsPath), data, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
		return nil
	}

	werr := writeAll()
	if cerr := tw.Close(); werr == nil {
		werr = cerr
	}
	if cerr := zw.Close(); werr == nil {
		werr = cerr
	}
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return summary, werr
	}
	keep = true
	progress("done", "")
	return summary, nil
}

// resolveExportMods expands an empty selection to every installed mod and validates explicit names.
func resolveExportMods(gameID string, requested []string) ([]string, error) {
	if len(requested) == 0 {
		mods, err := mod.ListMods(config.ModsDir(gameID), gameID)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, m := range mods {
			if m.Name == profile.OverwriteModName {
				continue
			}
			out = append(out, m.Name)
		}
		sort.Strings(out)
		return out, nil
	}
	var out []string
	for _, folder := range requested {
		if folder == "" || folder == profile.OverwriteModName || filepath.Base(folder) != folder {
			return nil, fmt.Errorf("invalid mod folder %q", folder)
		}
		if !modFolderExists(gameID, folder) {
			return nil, fmt.Errorf("mod folder %q: %w", folder, os.ErrNotExist)
		}
		out = append(out, folder)
	}
	return out, nil
}

// resolveExportProfiles expands an empty selection to every profile and validates explicit names.
func resolveExportProfiles(gameID string, requested []string) ([]string, error) {
	if len(requested) == 0 {
		entries, err := os.ReadDir(config.ProfilesDir(gameID))
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		var out []string
		for _, e := range entries {
			if !e.IsDir() || e.Name() == "" || e.Name()[0] == '.' {
				continue
			}
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out, nil
	}
	var out []string
	for _, name := range requested {
		if name == "" || filepath.Base(name) != name {
			return nil, fmt.Errorf("invalid profile name %q", name)
		}
		if !profileExists(gameID, name) {
			return nil, fmt.Errorf("profile %q: %w", name, os.ErrNotExist)
		}
		out = append(out, name)
	}
	return out, nil
}
