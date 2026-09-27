package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/gamedef"
)

const JournalFilename = ".gorganizer-migration.json"

type ExecuteOptions struct {
	ForceCopy     bool
	AfterBoundary func(string) error
	CopyProgress  func(int64, int64)
}

type journal struct {
	SchemaVersion int           `json:"schema_version"`
	OperationID   string        `json:"operation_id"`
	FromRoot      string        `json:"from_root"`
	Items         []journalItem `json:"items"`
	ConfigExists  bool          `json:"config_exists"`
	ConfigBefore  []byte        `json:"config_patch_preimage,omitempty"`
	Phase         string        `json:"phase"`
}

type journalItem struct {
	Item
	Phase string `json:"phase"`
}

// JournalPath returns the location of the offline migration journal.
func JournalPath() string { return journalPath() }

// journalPath returns the location of the offline migration journal.
func journalPath() string { return filepath.Join(config.DataDir(), JournalFilename) }

// JournalExists reports whether a migration journal is present and refuses uncertain access.
func JournalExists() (bool, error) {
	_, err := os.Lstat(journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking migration journal: %w", err)
	}
	return true, nil
}

// Execute starts a journaled move of every unblocked item in the plan.
func Execute(plan *MigrationPlan, opts ExecuteOptions) error {
	if plan == nil {
		return fmt.Errorf("no move was planned")
	}
	if exists, err := JournalExists(); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("a move is unfinished; run gorganizerctl migrate-data --resume")
	}
	if plan.HasBlockers() {
		return fmt.Errorf("resolve the listed problems before moving any folders")
	}
	fresh, err := Plan(plan.FromRoot)
	if err != nil {
		return err
	}
	if fresh.HasBlockers() || len(fresh.Items) != len(plan.Items) {
		return fmt.Errorf("the folders have changed; review the move again")
	}
	for i := range fresh.Items {
		if (fresh.Items[i].Mode == "copy" || opts.ForceCopy) && fresh.Items[i].FreeBytes < uint64(fresh.Items[i].Bytes)+uint64(fresh.Items[i].Bytes)/20+1 {
			return fmt.Errorf("not enough free space to copy %s", fresh.Items[i].Name)
		}
		fresh.Items[i].FreeBytes = plan.Items[i].FreeBytes
	}
	original, err := json.Marshal(plan.Items)
	if err != nil {
		return err
	}
	current, err := json.Marshal(fresh.Items)
	if err != nil {
		return err
	}
	if !bytes.Equal(original, current) {
		return fmt.Errorf("the folders have changed; review the move again")
	}
	if len(plan.Items) == 0 {
		return nil
	}
	preimage, err := os.ReadFile(configFilePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading game settings: %w", err)
	}
	j := &journal{SchemaVersion: 1, OperationID: uuid.NewString(), FromRoot: plan.FromRoot, Phase: "moving", ConfigExists: err == nil, ConfigBefore: preimage}
	for _, item := range plan.Items {
		if opts.ForceCopy {
			item.Mode = "copy"
		}
		j.Items = append(j.Items, journalItem{Item: item, Phase: "planned"})
	}
	if err := ensureDirDurable(config.DataDir()); err != nil {
		return fmt.Errorf("creating personal data folder: %w", err)
	}
	if err := saveJournal(j); err != nil {
		return err
	}
	if opts.AfterBoundary != nil {
		if err := opts.AfterBoundary("planned"); err != nil {
			return err
		}
	}
	return run(j, opts)
}

// Resume completes an interrupted move using only the durable journal.
func Resume() error { return ResumeWithOptions(ExecuteOptions{}) }

// ResumeWithOptions completes an interrupted move and reports copy progress.
func ResumeWithOptions(opts ExecuteOptions) error {
	exists, err := JournalExists()
	if err != nil || !exists {
		return err
	}
	j, err := readJournal()
	if err != nil {
		return err
	}
	return run(j, opts)
}

// readJournal loads and validates an existing migration journal before any changes.
func readJournal() (*journal, error) {
	info, err := os.Lstat(journalPath())
	if err != nil {
		return nil, fmt.Errorf("checking migration journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("migration journal is not a regular file; nothing was changed")
	}
	body, err := os.ReadFile(journalPath())
	if err != nil {
		return nil, fmt.Errorf("reading migration journal: %w", err)
	}
	var j journal
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("invalid migration journal: %w", err)
	}
	if j.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported migration journal version %d; nothing was changed", j.SchemaVersion)
	}
	if uuid.Validate(j.OperationID) != nil || !filepath.IsAbs(j.FromRoot) || filepath.Clean(j.FromRoot) != j.FromRoot || len(j.Items) == 0 {
		return nil, fmt.Errorf("invalid migration journal; nothing was changed")
	}
	if j.Phase != "moving" && j.Phase != "config-patched" && j.Phase != "committed" {
		return nil, fmt.Errorf("unknown migration journal phase; nothing was changed")
	}
	seen := map[string]bool{}
	for _, item := range j.Items {
		definition, ok := gamedef.ByID(item.GameID)
		if !ok || definition.ModsDirName == "" || item.Source != filepath.Join(j.FromRoot, definition.ModsDirName) || item.Destination != config.XDGModsDir(item.GameID) || seen[item.GameID] {
			return nil, fmt.Errorf("invalid migration folder in journal; nothing was changed")
		}
		seen[item.GameID] = true
		if item.Mode != "rename" && item.Mode != "copy" {
			return nil, fmt.Errorf("unknown migration method; nothing was changed")
		}
		switch item.Phase {
		case "planned", "moved", "copied", "verified", "published", "source-removed":
		default:
			return nil, fmt.Errorf("unknown migration item phase; nothing was changed")
		}
		if len(item.Entries) == 0 || item.Entries[0].Path != "." || item.Entries[0].Kind != "dir" {
			return nil, fmt.Errorf("missing migration inventory; nothing was changed")
		}
		for _, e := range item.Entries {
			if filepath.IsAbs(e.Path) || e.Path != filepath.Clean(e.Path) || e.Path == ".." || strings.HasPrefix(e.Path, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("invalid migration inventory; nothing was changed")
			}
			if item.Mode == "copy" && item.Phase != "planned" && e.Kind == "file" && len(e.SHA256) != 64 {
				return nil, fmt.Errorf("missing copied file hash; nothing was changed")
			}
		}
	}
	return &j, nil
}

// saveJournal durably records the current transition state.
func saveJournal(j *journal) error {
	body, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding migration journal: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(journalPath(), body, 0o600); err != nil {
		return fmt.Errorf("saving migration journal: %w", err)
	}
	return nil
}

// run advances every item then patches settings and removes the committed journal.
func run(j *journal, opts ExecuteOptions) error {
	checkpoint := func(name string) error {
		if opts.AfterBoundary != nil {
			return opts.AfterBoundary(name)
		}
		return nil
	}
	for i := range j.Items {
		var copied int64
		progress := func(n int64) {
			copied += n
			if opts.CopyProgress != nil {
				opts.CopyProgress(copied, j.Items[i].Bytes)
			}
		}
		if err := advanceItem(j, &j.Items[i], checkpoint, progress); err != nil {
			return fmt.Errorf("moving %s: %w", j.Items[i].Name, err)
		}
	}
	if j.Phase == "moving" {
		if err := patchConfig(j); err != nil {
			return err
		}
		if err := checkpoint("config-saved"); err != nil {
			return err
		}
		j.Phase = "config-patched"
		if err := saveJournal(j); err != nil {
			return err
		}
		if err := checkpoint("config-patched"); err != nil {
			return err
		}
	}
	if j.Phase == "config-patched" {
		if err := verifyPatchedConfig(j); err != nil {
			return err
		}
		j.Phase = "committed"
		if err := saveJournal(j); err != nil {
			return err
		}
		if err := checkpoint("committed"); err != nil {
			return err
		}
	}
	if err := verifyPatchedConfig(j); err != nil {
		return err
	}
	if err := atomicfile.RemoveDurable(journalPath()); err != nil {
		return fmt.Errorf("removing finished migration journal: %w", err)
	}
	return nil
}

// advanceItem replays an item from the last durably recorded phase.
func advanceItem(j *journal, item *journalItem, checkpoint func(string) error, progress func(int64)) error {
	if item.Phase == "source-removed" {
		return verifyTree(item.Destination, item.Entries, false)
	}
	stage := item.Destination + ".gorganizer-migrating"
	quarantine := item.Source + ".gorganizer-migrating-source-" + j.OperationID
	setPhase := func(phase string) error {
		item.Phase = phase
		if err := saveJournal(j); err != nil {
			return err
		}
		return checkpoint(phase)
	}
	if item.Phase == "planned" {
		if item.Mode == "rename" {
			if !pathExists(item.Source) && pathExists(item.Destination) {
				if err := verifyTree(item.Destination, item.Entries, true); err != nil {
					return err
				}
			} else {
				if err := verifyTree(item.Source, item.Entries, true); err != nil {
					return fmt.Errorf("old folder changed since planning: %w", err)
				}
				if err := prepareDestination(item.Destination); err != nil {
					return err
				}
				if err := os.Rename(item.Source, item.Destination); err != nil {
					return fmt.Errorf("renaming folder: %w", err)
				}
				if err := syncRename(item.Source, item.Destination); err != nil {
					return err
				}
				if err := checkpoint("moved-tree"); err != nil {
					return err
				}
			}
			if err := setPhase("moved"); err != nil {
				return err
			}
		} else {
			if exists, nonempty, err := directoryState(item.Destination); err != nil {
				return err
			} else if exists && nonempty {
				for _, e := range item.Entries {
					if e.Kind == "file" && e.SHA256 == "" {
						return fmt.Errorf("new folder already exists before its files were copied")
					}
				}
				if err := verifyTree(item.Destination, item.Entries, false); err != nil {
					return fmt.Errorf("new folder already exists: %w", err)
				}
				if err := setPhase("published"); err != nil {
					return err
				}
			} else {
				if err := buildStage(j, item, stage, checkpoint, progress); err != nil {
					return err
				}
				if err := checkpoint("copied-tree"); err != nil {
					return err
				}
				if err := setPhase("copied"); err != nil {
					return err
				}
			}
		}
	}
	if item.Phase == "moved" || item.Phase == "copied" {
		if item.Mode == "rename" {
			if err := verifyTree(item.Destination, item.Entries, true); err != nil {
				return err
			}
		} else if exists, nonempty, err := directoryState(item.Destination); err != nil {
			return err
		} else if !exists || !nonempty {
			if err := verifyTree(stage, item.Entries, false); err != nil {
				if err := buildStage(j, item, stage, checkpoint, progress); err != nil {
					return err
				}
			}
		} else if err := verifyTree(item.Destination, item.Entries, false); err != nil {
			return err
		}
		if err := setPhase("verified"); err != nil {
			return err
		}
	}
	if item.Phase == "verified" {
		if exists, nonempty, err := directoryState(item.Destination); err != nil {
			return err
		} else if item.Mode == "copy" && (!exists || !nonempty) {
			if err := verifyTree(stage, item.Entries, false); err != nil {
				return err
			}
			if err := prepareDestination(item.Destination); err != nil {
				return err
			}
			if err := os.Rename(stage, item.Destination); err != nil {
				return fmt.Errorf("publishing copied folder: %w", err)
			}
			if err := atomicfile.SyncDir(filepath.Dir(item.Destination)); err != nil {
				return err
			}
			if err := checkpoint("published-tree"); err != nil {
				return err
			}
		}
		if err := verifyTree(item.Destination, item.Entries, item.Mode == "rename"); err != nil {
			return err
		}
		if err := setPhase("published"); err != nil {
			return err
		}
	}
	if item.Phase == "published" {
		if err := verifyTree(item.Destination, item.Entries, item.Mode == "rename"); err != nil {
			return err
		}
		if item.Mode == "copy" {
			if pathExists(item.Source) {
				if pathExists(quarantine) {
					return fmt.Errorf("both old and parked folders exist; neither was deleted")
				}
				if err := verifyTree(item.Source, item.Entries, true); err != nil {
					return fmt.Errorf("old folder changed since planning; both copies were kept: %w", err)
				}
				if err := os.Rename(item.Source, quarantine); err != nil {
					return fmt.Errorf("parking old folder: %w", err)
				}
				if err := atomicfile.SyncDir(filepath.Dir(item.Source)); err != nil {
					return err
				}
				if err := checkpoint("source-parked"); err != nil {
					return err
				}
			}
			if pathExists(quarantine) {
				if err := verifyRemaining(quarantine, item.Entries); err != nil {
					return fmt.Errorf("old folder changed during removal; both copies were kept: %w", err)
				}
				if err := os.RemoveAll(quarantine); err != nil {
					return fmt.Errorf("removing old folder: %w", err)
				}
				if err := atomicfile.SyncDir(filepath.Dir(item.Source)); err != nil {
					return err
				}
				if err := checkpoint("source-deleted"); err != nil {
					return err
				}
			}
		} else if pathExists(item.Source) {
			return fmt.Errorf("old folder reappeared; it was not removed")
		}
		if err := setPhase("source-removed"); err != nil {
			return err
		}
	}
	return nil
}

// prepareDestination creates the parent and removes only an empty destination folder.
func prepareDestination(dest string) error {
	if err := ensureDirDurable(filepath.Dir(dest)); err != nil {
		return err
	}
	if exists, nonempty, err := directoryState(dest); err != nil {
		return err
	} else if exists {
		if nonempty {
			return fmt.Errorf("new folder already contains files; nothing was replaced")
		}
		if err := atomicfile.RemoveDurable(dest); err != nil {
			return err
		}
	}
	return nil
}

// syncRename flushes both parents after moving an item between them.
func syncRename(from, to string) error {
	if err := atomicfile.SyncDir(filepath.Dir(to)); err != nil {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(from))
}

// pathExists checks a path without following symlinks and treats access errors as present.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
