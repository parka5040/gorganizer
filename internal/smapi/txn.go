package smapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/fsutil"
)

const (
	OpInstall       = "install"
	OpUninstall     = "uninstall"
	PhaseBackingUp  = "backing_up"
	PhasePlacing    = "placing"
	PhaseCommitting = "committing"

	intentSchema     = 1
	ownerFileName    = "owner.json"
	maxRestoreBytes  = 64 << 20
	stateFileMode    = 0644
	restoreDirName   = "restore"
	atomicTempPrefix = ".tmp-"
)

var errInjectedFault = errors.New("injected transaction fault")

type Intent struct {
	SchemaVersion  int               `json:"schema_version"`
	OpID           string            `json:"op_id"`
	Op             string            `json:"op"`
	Phase          string            `json:"phase"`
	Version        string            `json:"version,omitempty"`
	WorkDir        string            `json:"work_dir"`
	OwnerPID       int               `json:"owner_pid"`
	OwnerStart     uint64            `json:"owner_start"`
	InstallerPID   int               `json:"installer_pid,omitempty"`
	InstallerStart uint64            `json:"installer_start,omitempty"`
	Place          []string          `json:"place"`
	Remove         []string          `json:"remove"`
	Existing       map[string]FileID `json:"existing"`
	Placed         map[string]FileID `json:"placed"`
	Restored       map[string]string `json:"restored,omitempty"`
	NewDirs        []string          `json:"new_dirs,omitempty"`
	NewOriginals   []OriginalFile    `json:"new_originals,omitempty"`
	DropOriginals  []OriginalFile    `json:"drop_originals,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
}

type RestoreFile struct {
	Data []byte
	Mode os.FileMode
}

type TxnPlan struct {
	OpID              string
	Op                string
	Version           string
	ArtifactSHA256    string
	WorkDir           string
	StageGame         string
	Place             []string
	Remove            []string
	Restore           map[string]RestoreFile
	PreserveOriginals []string
	Originals         []OriginalFile
	DropOriginals     []OriginalFile
	Expect            map[string]string
	Protected         map[string]bool
	Farm              FarmGuard
	InstallerPID      int
	InstallerStart    uint64
	InstalledAt       time.Time
	fault             func(step string) error
	warn              func(message string)
}

type RecoverResult struct {
	RolledBack    bool
	Committed     bool
	SweptWorkDirs int
	SweptBackups  int
	Warnings      []string
}

type ownerInfo struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

type txn struct {
	gameDir     string
	workDir     string
	ownWork     bool
	mount       mountKey
	plan        TxnPlan
	intent      Intent
	placeKeys   []string
	restoreKeys []string
	files       []RecordEntry
	originals   []OriginalFile
	dirty       map[string]bool
	committed   bool
}

type restorer struct {
	gameDir    string
	backupRoot string
	mount      mountKey
	intent     *Intent
	hook       func(string) error
	dirty      map[string]bool
}

// Apply journals and performs a mod-loader transaction on gameDir under the game lock, rolling back in-process on any real error before the commit point.
func Apply(gameDir string, plan TxnPlan) (*Record, error) {
	if err := checkGameDir(gameDir); err != nil {
		return nil, err
	}
	unlock, err := lockGame(gameDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return apply(gameDir, plan)
}

// apply runs a transaction for a caller that already holds the game lock.
func apply(gameDir string, plan TxnPlan) (*Record, error) {
	t, err := preflight(gameDir, plan)
	if err != nil {
		return nil, err
	}
	if err := t.prepare(); err != nil {
		t.discard()
		return nil, err
	}
	if err := t.step("intent"); err != nil {
		if !isFault(err) {
			t.discard()
		}
		return nil, err
	}
	if err := writeIntent(gameDir, &t.intent); err != nil {
		t.discard()
		return nil, err
	}
	record, err := t.execute()
	if err == nil || isFault(err) {
		return record, err
	}
	if t.committed {
		return record, fmt.Errorf("mod-loader operation %s committed but did not finish: %w: %w", t.intent.OpID, err, ErrInterrupted)
	}
	if rollbackErr := t.abort(); rollbackErr != nil {
		if isFault(rollbackErr) {
			return nil, rollbackErr
		}
		return nil, &RollbackFailedError{Cause: err, RollbackErr: rollbackErr}
	}
	return nil, err
}

// execute runs the journaled phases and the commit, marking the transaction committed once its commit point is durable.
func (t *txn) execute() (*Record, error) {
	if err := t.captureOriginals(); err != nil {
		return nil, err
	}
	if err := t.step("backup"); err != nil {
		return nil, err
	}
	if err := t.backupAll(); err != nil {
		return nil, err
	}
	if err := t.step("place"); err != nil {
		return nil, err
	}
	if err := t.setPhase(PhasePlacing); err != nil {
		return nil, err
	}
	if err := t.placeAll(); err != nil {
		return nil, err
	}
	if err := t.step("commit"); err != nil {
		return nil, err
	}
	if err := t.setPhase(PhaseCommitting); err != nil {
		return nil, err
	}
	var record *Record
	if t.plan.Op == OpInstall {
		if err := t.step("record"); err != nil {
			return nil, err
		}
		written, err := t.writeRecord()
		if err != nil {
			return nil, err
		}
		record = written
	}
	t.committed = true
	warnings, err := finishCommitted(t.gameDir, &t.intent, t.plan.fault)
	t.warn(warnings...)
	return record, err
}

// abort rolls an uncommitted transaction back in-process and removes its intent.
func (t *txn) abort() error {
	if err := rollback(t.gameDir, &t.intent, t.plan.fault); err != nil {
		return err
	}
	warnings, err := finishRolledBack(t.gameDir, &t.intent, t.plan.fault)
	t.warn(warnings...)
	return err
}

// step invokes the test fault hook for a named transaction step.
func (t *txn) step(name string) error {
	return runStep(t.plan.fault, name)
}

// warn forwards non-fatal problems to the plan's warning callback.
func (t *txn) warn(messages ...string) {
	if t.plan.warn == nil {
		return
	}
	for _, message := range messages {
		t.plan.warn(message)
	}
}

// setPhase atomically rewrites the intent with a new phase.
func (t *txn) setPhase(phase string) error {
	t.intent.Phase = phase
	return writeIntent(t.gameDir, &t.intent)
}

// preflight validates the plan against gameDir without changing anything and builds the intent.
func preflight(gameDir string, plan TxnPlan) (*txn, error) {
	if err := checkGameDir(gameDir); err != nil {
		return nil, err
	}
	if err := validateOpID(plan.OpID); err != nil {
		return nil, err
	}
	if plan.Op != OpInstall && plan.Op != OpUninstall {
		return nil, fmt.Errorf("unknown mod-loader operation %q", plan.Op)
	}
	workDir := filepath.Join(gameDir, WorkDir, plan.OpID)
	if plan.WorkDir != "" && filepath.Clean(plan.WorkDir) != workDir {
		return nil, fmt.Errorf("work directory %s does not belong to operation %s", plan.WorkDir, plan.OpID)
	}
	mount, err := mountOf(gameDir)
	if err != nil {
		return nil, err
	}
	if info, err := lstatOptional(filepath.Join(gameDir, IntentFile)); err != nil || info != nil {
		return nil, fmt.Errorf("%s exists: %w", IntentFile, ErrInterrupted)
	}
	if err := checkReservedDirs(gameDir, mount); err != nil {
		return nil, err
	}
	if err := checkFarm(gameDir, plan.Farm); err != nil {
		return nil, err
	}
	if info, err := lstatOptional(filepath.Join(gameDir, BackupDir, plan.OpID)); err != nil || info != nil {
		return nil, fmt.Errorf("backup for operation %s already exists", plan.OpID)
	}
	t := &txn{gameDir: gameDir, workDir: workDir, mount: mount, plan: plan, dirty: map[string]bool{}}
	for rel := range plan.Restore {
		t.restoreKeys = append(t.restoreKeys, rel)
	}
	sort.Strings(t.restoreKeys)
	t.placeKeys = sortedUnique(plan.Place)
	place := append(append([]string{}, t.placeKeys...), t.restoreKeys...)
	remove := sortedUnique(plan.Remove)
	all := append(append([]string{}, place...), remove...)
	for i, rel := range all {
		if err := validateRelPath(rel); err != nil {
			return nil, &UnsafeTargetError{Path: rel, Reason: err.Error()}
		}
		if protectedTarget(rel, plan.Protected) {
			return nil, &UnsafeTargetError{Path: rel, Reason: "the path is game-owned"}
		}
		for _, other := range all[:i] {
			if overlaps(rel, other) {
				return nil, fmt.Errorf("mod-loader targets %q and %q overlap", other, rel)
			}
		}
	}
	t.intent = Intent{
		SchemaVersion:  intentSchema,
		OpID:           plan.OpID,
		Op:             plan.Op,
		Phase:          PhaseBackingUp,
		Version:        plan.Version,
		WorkDir:        WorkDir + "/" + plan.OpID,
		InstallerPID:   plan.InstallerPID,
		InstallerStart: plan.InstallerStart,
		Place:          place,
		Remove:         remove,
		Existing:       map[string]FileID{},
		Placed:         map[string]FileID{},
		Restored:       map[string]string{},
		CreatedAt:      time.Now().UTC(),
	}
	if err := t.checkStage(); err != nil {
		return nil, err
	}
	for _, rel := range t.restoreKeys {
		file := plan.Restore[rel]
		if len(file.Data) > maxRestoreBytes || file.Mode&^os.ModePerm != 0 {
			return nil, fmt.Errorf("restore content for %s is too large or has a non-permission mode", rel)
		}
	}
	newDirs := map[string]bool{}
	for _, rel := range all {
		id, missing, err := checkTarget(gameDir, mount, rel)
		if err != nil {
			return nil, err
		}
		if id != nil {
			t.intent.Existing[rel] = *id
		}
		if contains(place, rel) {
			for _, dir := range missing {
				newDirs[dir] = true
			}
		}
	}
	t.intent.NewDirs = sortedSet(newDirs)
	for _, rel := range sortedFileKeys(plan.Expect) {
		if err := validateRelPath(rel); err != nil {
			return nil, fmt.Errorf("expected input: %w", err)
		}
		full, err := safeGamePath(gameDir, rel)
		if err != nil {
			return nil, err
		}
		if err := checkExpected(full, plan.Expect[rel]); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
	}
	if err := t.checkOriginals(); err != nil {
		return nil, err
	}
	return t, nil
}

// checkStage requires every staged entry to be a regular file or directory on the game mount and records its identity.
func (t *txn) checkStage() error {
	if len(t.placeKeys) == 0 {
		return nil
	}
	stage := t.plan.StageGame
	if !filepath.IsAbs(stage) || !fsutil.ContainedBy(t.workDir, stage) || !isPlainDir(stage) {
		return fmt.Errorf("stage %s is not a directory inside the operation work directory", stage)
	}
	if err := guardAncestors(t.gameDir, t.mount, filepath.Join(stage, "x")); err != nil {
		return err
	}
	for _, rel := range t.placeKeys {
		source := filepath.Join(stage, filepath.FromSlash(rel))
		if err := guardAncestors(t.gameDir, t.mount, source); err != nil {
			return err
		}
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("staged entry %s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !(info.IsDir() || info.Mode().IsRegular()) {
			return fmt.Errorf("staged entry %s is not a regular file or directory", rel)
		}
		if err := requireMount(source, t.mount); err != nil {
			return err
		}
		t.intent.Placed[rel] = identify(info)
	}
	return nil
}

// checkOriginals digests every game file to preserve, adopts only a saved copy that matches it byte for byte, and validates carried and dropped originals.
func (t *txn) checkOriginals() error {
	for _, group := range [][]OriginalFile{t.plan.Originals, t.plan.DropOriginals} {
		for _, original := range group {
			if err := validateRelPath(original.Path); err != nil {
				return fmt.Errorf("original: %w", err)
			}
			if err := validateDigest(original.SHA256, original.Size, original.Mode); err != nil {
				return fmt.Errorf("original %s: %w", original.Path, err)
			}
		}
	}
	t.originals = append(t.originals, t.plan.Originals...)
	for _, rel := range sortedUnique(t.plan.PreserveOriginals) {
		if !contains(t.placeKeys, rel) {
			return fmt.Errorf("original %s is not a placed target", rel)
		}
		full := filepath.Join(t.gameDir, filepath.FromSlash(rel))
		if !isRegularFile(full) {
			continue
		}
		sum, info, err := hashRegular(full, false)
		if err != nil {
			return err
		}
		original := OriginalFile{Path: rel, SHA256: sum.SHA256, Size: sum.Size, Mode: uint32(info.Mode().Perm())}
		saved, err := safeGamePath(t.gameDir, OriginalsDir+"/"+rel)
		if err != nil {
			return err
		}
		savedInfo, err := lstatOptional(saved)
		if err != nil {
			return err
		}
		switch {
		case savedInfo == nil:
			t.intent.NewOriginals = append(t.intent.NewOriginals, original)
		case savedInfo.Mode().IsRegular():
			got, _, err := hashRegular(saved, false)
			if err != nil {
				return err
			}
			if got != sum {
				return &UnsafeTargetError{Path: OriginalsDir + "/" + rel, Reason: "a saved original that no install record vouches for differs from the game file"}
			}
		default:
			return &UnsafeTargetError{Path: OriginalsDir + "/" + rel, Reason: "saved original is not a regular file"}
		}
		t.originals = append(t.originals, original)
	}
	sort.Slice(t.originals, func(i, j int) bool { return t.originals[i].Path < t.originals[j].Path })
	t.intent.DropOriginals = append([]OriginalFile{}, t.plan.DropOriginals...)
	return nil
}

// prepare creates the operation work directory, proves no-replace renames there, stages restored files, and hashes the staged payload.
func (t *txn) prepare() error {
	if !isPlainDir(t.workDir) {
		if err := os.MkdirAll(filepath.Dir(t.workDir), 0755); err != nil {
			return fmt.Errorf("creating mod-loader work directory: %w", err)
		}
		if err := os.Mkdir(t.workDir, 0700); err != nil {
			return fmt.Errorf("creating mod-loader work directory: %w", err)
		}
		t.ownWork = true
	}
	if err := guardAncestors(t.gameDir, t.mount, filepath.Join(t.workDir, "x")); err != nil {
		return err
	}
	if err := probeRenameNoReplace(t.workDir); err != nil {
		return err
	}
	for _, rel := range t.restoreKeys {
		file := t.plan.Restore[rel]
		source := t.source(rel)
		if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
			return fmt.Errorf("staging restored %s: %w", rel, err)
		}
		if err := atomicfile.WriteFile(source, file.Data, file.Mode.Perm()); err != nil {
			return fmt.Errorf("staging restored %s: %w", rel, err)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		t.intent.Placed[rel] = identify(info)
		t.intent.Restored[rel] = digestBytes(file.Data).SHA256
	}
	if t.plan.Op == OpInstall && len(t.placeKeys) > 0 {
		files, err := recordEntries(t.plan.StageGame, t.placeKeys)
		if err != nil {
			return err
		}
		t.files = files
	}
	t.intent.OwnerPID = os.Getpid()
	if start, err := ProcessStartTime(t.intent.OwnerPID); err == nil {
		t.intent.OwnerStart = start
	}
	return nil
}

// discard removes what prepare created before any intent existed.
func (t *txn) discard() {
	if t.ownWork {
		_ = removeOwnedTree(t.workDir)
		removeIfEmpty(filepath.Dir(t.workDir))
		return
	}
	_ = removeOwnedTree(filepath.Join(t.workDir, restoreDirName))
}

// source returns the staged path a placed target is renamed from.
func (t *txn) source(rel string) string {
	if _, ok := t.plan.Restore[rel]; ok {
		return filepath.Join(t.workDir, restoreDirName, filepath.FromSlash(rel))
	}
	return filepath.Join(t.plan.StageGame, filepath.FromSlash(rel))
}

// checkGameDir requires gameDir to be an absolute, clean path to a real directory.
func checkGameDir(gameDir string) error {
	if !filepath.IsAbs(gameDir) || filepath.Clean(gameDir) != gameDir {
		return fmt.Errorf("game directory %q is not an absolute clean path", gameDir)
	}
	info, err := os.Lstat(gameDir)
	if err != nil {
		return fmt.Errorf("game directory: %w", err)
	}
	if !info.IsDir() {
		return &UnsafeTargetError{Path: gameDir, Reason: "game directory is not a real directory"}
	}
	return nil
}

// checkReservedDirs requires every existing reserved directory to be a real directory on the game mount.
func checkReservedDirs(gameDir string, mount mountKey) error {
	for _, reserved := range []string{WorkDir, BackupDir, OriginalsDir} {
		full := filepath.Join(gameDir, reserved)
		info, err := lstatOptional(full)
		if err != nil {
			return err
		}
		if info == nil {
			continue
		}
		if !info.IsDir() {
			return &UnsafeTargetError{Path: reserved, Reason: "reserved path is not a real directory"}
		}
		if err := requireMount(full, mount); err != nil {
			return err
		}
	}
	return nil
}

// checkFarm refuses while the configured deploy folder is a mounted farm or a farm transition has left a sibling behind, and refuses a guard that does not name the farm's deploy folder and sentinel.
func checkFarm(gameDir string, guard FarmGuard) error {
	if guard.DeployDir == "" || guard.Sentinel == "" || len(guard.SiblingSuffixes) == 0 {
		return errors.New("the mod-loader transaction has no farm guard naming the deploy folder, sentinel and transition siblings")
	}
	if err := validateRelPath(guard.DeployDir); err != nil {
		return fmt.Errorf("farm guard deploy folder: %w", err)
	}
	deploy := filepath.Join(gameDir, filepath.FromSlash(guard.DeployDir))
	info, err := lstatOptional(filepath.Join(deploy, guard.Sentinel))
	if err != nil {
		return err
	}
	if info != nil {
		return fmt.Errorf("%s contains %s: %w", guard.DeployDir, guard.Sentinel, ErrFarmMounted)
	}
	for _, suffix := range guard.SiblingSuffixes {
		info, err := lstatOptional(deploy + suffix)
		if err != nil {
			return err
		}
		if info != nil {
			return fmt.Errorf("%s exists, so a farm transition is pending: %w", guard.DeployDir+suffix, ErrFarmMounted)
		}
	}
	return nil
}

// checkTarget validates one game-root target and returns its identity when it exists and which of its ancestors are missing.
func checkTarget(gameDir string, mount mountKey, rel string) (*FileID, []string, error) {
	full, err := fsutil.SafeJoin(gameDir, rel, false)
	if err != nil {
		return nil, nil, &UnsafeTargetError{Path: rel, Reason: err.Error()}
	}
	segments := strings.Split(rel, "/")
	var missing []string
	for i := 1; i < len(segments); i++ {
		ancestor := strings.Join(segments[:i], "/")
		if len(missing) > 0 {
			missing = append(missing, ancestor)
			continue
		}
		ancestorPath := filepath.Join(gameDir, filepath.FromSlash(ancestor))
		info, err := lstatOptional(ancestorPath)
		if err != nil {
			return nil, nil, err
		}
		if info == nil {
			missing = append(missing, ancestor)
			continue
		}
		if !info.IsDir() {
			return nil, nil, &UnsafeTargetError{Path: rel, Reason: fmt.Sprintf("ancestor %s is not a real directory", ancestor)}
		}
		if err := requireMount(ancestorPath, mount); err != nil {
			return nil, nil, err
		}
	}
	info, err := lstatOptional(full)
	if err != nil || info == nil {
		return nil, missing, err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return nil, nil, &UnsafeTargetError{Path: rel, Reason: "target is a symlink"}
	case info.IsDir():
	case info.Mode().IsRegular():
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
			return nil, nil, &UnsafeTargetError{Path: rel, Reason: fmt.Sprintf("target has %d hard links", stat.Nlink)}
		}
	default:
		return nil, nil, &UnsafeTargetError{Path: rel, Reason: "target is not a regular file or directory"}
	}
	if err := requireMount(full, mount); err != nil {
		return nil, nil, err
	}
	id := identify(info)
	return &id, nil, nil
}

// checkExpected requires full to have digest sum, or to be absent when sum is empty, failing with ErrGameChanged.
func checkExpected(full, sum string) error {
	if sum == "" {
		info, err := lstatOptional(full)
		if err != nil {
			return err
		}
		if info != nil {
			return fmt.Errorf("appeared while the installer ran: %w", ErrGameChanged)
		}
		return nil
	}
	got, _, err := hashRegular(full, false)
	if err != nil {
		return fmt.Errorf("%v: %w", err, ErrGameChanged)
	}
	if got.SHA256 != sum {
		return fmt.Errorf("changed while the installer ran: %w", ErrGameChanged)
	}
	return nil
}

// checkUnder reports whether rel exists below gameDir after verifying that none of its existing ancestors is a symlink.
func checkUnder(gameDir, rel string) (bool, error) {
	full, err := safeGamePath(gameDir, rel)
	if err != nil {
		return false, err
	}
	info, err := lstatOptional(full)
	return info != nil, err
}

// safeGamePath joins rel to gameDir after verifying that every existing ancestor inside gameDir is a real directory.
func safeGamePath(gameDir, rel string) (string, error) {
	full, err := fsutil.SafeJoin(gameDir, rel, false)
	if err != nil {
		return "", &UnsafeTargetError{Path: rel, Reason: err.Error()}
	}
	segments := strings.Split(rel, "/")
	for i := 1; i < len(segments); i++ {
		ancestor := strings.Join(segments[:i], "/")
		info, err := lstatOptional(filepath.Join(gameDir, filepath.FromSlash(ancestor)))
		if err != nil {
			return "", err
		}
		if info == nil {
			break
		}
		if !info.IsDir() {
			return "", &UnsafeTargetError{Path: rel, Reason: fmt.Sprintf("ancestor %s is not a real directory", ancestor)}
		}
	}
	return full, nil
}

// captureOriginals saves every newly preserved game file into the originals tree before anything is renamed.
func (t *txn) captureOriginals() error {
	for _, original := range t.intent.NewOriginals {
		if err := t.step("original:" + original.Path); err != nil {
			return err
		}
		source := filepath.Join(t.gameDir, filepath.FromSlash(original.Path))
		if err := guardAncestors(t.gameDir, t.mount, source); err != nil {
			return err
		}
		data, err := readSmallRegular(source, maxRestoreBytes)
		if err != nil {
			return fmt.Errorf("reading original %s: %w", original.Path, err)
		}
		if got := digestBytes(data); got.SHA256 != original.SHA256 || got.Size != original.Size {
			return fmt.Errorf("original %s: %w", original.Path, ErrGameChanged)
		}
		target := filepath.Join(t.gameDir, OriginalsDir, filepath.FromSlash(original.Path))
		if err := t.ensureDir(filepath.Dir(target)); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(target, data, os.FileMode(original.Mode)); err != nil {
			return fmt.Errorf("preserving original %s: %w", original.Path, err)
		}
		t.dirty[filepath.Dir(target)] = true
	}
	return t.flush()
}

// backupAll renames every existing target into the operation backup after proving it is the entry seen at preflight.
func (t *txn) backupAll() error {
	backupRoot := filepath.Join(t.gameDir, BackupDir, t.intent.OpID)
	for _, rel := range sortedIDKeys(t.intent.Existing) {
		if err := t.step("backup:" + rel); err != nil {
			return err
		}
		source := filepath.Join(t.gameDir, filepath.FromSlash(rel))
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("backing up %s: %w", rel, err)
		}
		if identify(info) != t.intent.Existing[rel] {
			return fmt.Errorf("%s was replaced: %w", rel, ErrGameChanged)
		}
		target := filepath.Join(backupRoot, filepath.FromSlash(rel))
		if err := t.ensureDir(filepath.Dir(target)); err != nil {
			return err
		}
		if err := t.rename(source, target); err != nil {
			return fmt.Errorf("backing up %s: %w", rel, err)
		}
		if err := t.verifyExpected(rel, target); err != nil {
			return err
		}
	}
	return t.flush()
}

// verifyExpected re-hashes every expected input inside the backed-up target rel at its backup location.
func (t *txn) verifyExpected(rel, backup string) error {
	for _, key := range sortedFileKeys(t.plan.Expect) {
		if !isWithin(key, rel) {
			continue
		}
		full := filepath.Join(backup, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(key, rel), "/")))
		if err := checkExpected(full, t.plan.Expect[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// placeAll renames every staged and restored entry into the game after proving each source is the one recorded at preflight.
func (t *txn) placeAll() error {
	for _, rel := range t.intent.Place {
		if err := t.step("place:" + rel); err != nil {
			return err
		}
		source := t.source(rel)
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("placing %s: %w", rel, err)
		}
		if identify(info) != t.intent.Placed[rel] {
			return fmt.Errorf("staged entry %s was replaced after preflight", rel)
		}
		if sum, ok := t.intent.Restored[rel]; ok {
			got, _, err := hashRegular(source, true)
			if err != nil || got.SHA256 != sum {
				return fmt.Errorf("staged restore of %s does not match its digest", rel)
			}
		}
		target := filepath.Join(t.gameDir, filepath.FromSlash(rel))
		if err := t.ensureDir(filepath.Dir(target)); err != nil {
			return err
		}
		if err := t.rename(source, target); err != nil {
			return fmt.Errorf("placing %s: %w", rel, err)
		}
	}
	return t.flush()
}

// rename moves src to dst without replacing anything after re-checking both ancestries, and marks both parents dirty.
func (t *txn) rename(src, dst string) error {
	for _, candidate := range []string{src, dst} {
		if err := guardAncestors(t.gameDir, t.mount, candidate); err != nil {
			return err
		}
	}
	if err := renameNoReplace(src, dst); err != nil {
		return err
	}
	t.dirty[filepath.Dir(src)] = true
	t.dirty[filepath.Dir(dst)] = true
	return nil
}

// ensureDir creates the missing directories of dir below the game directory one at a time and marks each new directory and its parent dirty.
func (t *txn) ensureDir(dir string) error {
	rel, err := filepath.Rel(t.gameDir, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &UnsafeTargetError{Path: dir, Reason: "directory is not inside the game"}
	}
	if rel == "." {
		return nil
	}
	current := t.gameDir
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		next := filepath.Join(current, segment)
		info, err := lstatOptional(next)
		if err != nil {
			return err
		}
		if info == nil {
			if err := os.Mkdir(next, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			t.dirty[current] = true
			t.dirty[next] = true
			if info, err = os.Lstat(next); err != nil {
				return err
			}
		}
		if !info.IsDir() {
			return &UnsafeTargetError{Path: filepath.ToSlash(rel), Reason: fmt.Sprintf("%s is not a real directory", next)}
		}
		if err := requireMount(next, t.mount); err != nil {
			return err
		}
		current = next
	}
	return nil
}

// flush syncs every directory touched since the last flush.
func (t *txn) flush() error {
	err := syncDirs(t.dirty)
	t.dirty = map[string]bool{}
	return err
}

// writeRecord validates and atomically writes the install record, which is the commit point of an install.
func (t *txn) writeRecord() (*Record, error) {
	installedAt := t.plan.InstalledAt
	if installedAt.IsZero() {
		installedAt = time.Now()
	}
	record := &Record{
		SchemaVersion:  recordSchema,
		Loader:         recordLoader,
		OpID:           t.plan.OpID,
		Version:        t.plan.Version,
		ArtifactSHA256: t.plan.ArtifactSHA256,
		InstalledAt:    installedAt.UTC(),
		Targets:        append([]string{}, t.placeKeys...),
		Files:          t.files,
		Originals:      t.originals,
	}
	if record.Files == nil {
		record.Files = []RecordEntry{}
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	if err := writeRecord(t.gameDir, record); err != nil {
		return nil, err
	}
	return record, nil
}

// finishCommitted completes a committed transaction: it drops superseded state, removes the intent, and cleans up best-effort.
func finishCommitted(gameDir string, intent *Intent, hook func(string) error) ([]string, error) {
	if intent.Op == OpUninstall {
		if err := runStep(hook, "record"); err != nil {
			return nil, err
		}
		if err := removeRecord(gameDir); err != nil {
			return nil, err
		}
	}
	if err := runStep(hook, "committed"); err != nil {
		return nil, err
	}
	warnings := dropOriginals(gameDir, intent.DropOriginals)
	removeIfEmpty(filepath.Join(gameDir, OriginalsDir))
	if err := removeIntent(gameDir); err != nil {
		return warnings, err
	}
	cleanup, err := finishCleanup(gameDir, intent, hook)
	return append(warnings, cleanup...), err
}

// finishRolledBack removes the intent of a rolled-back transaction and cleans up best-effort.
func finishRolledBack(gameDir string, intent *Intent, hook func(string) error) ([]string, error) {
	if err := runStep(hook, "rolled-back"); err != nil {
		return nil, err
	}
	if err := removeIntent(gameDir); err != nil {
		return nil, err
	}
	return finishCleanup(gameDir, intent, hook)
}

// finishCleanup removes the operation's backup and work directories once its intent is gone, never failing on a cleanup error.
func finishCleanup(gameDir string, intent *Intent, hook func(string) error) ([]string, error) {
	if err := runStep(hook, "cleanup"); err != nil {
		if isFault(err) {
			return nil, err
		}
		return []string{"skipped cleanup: " + err.Error()}, nil
	}
	return cleanupOp(gameDir, intent), nil
}

// dropOriginals deletes superseded saved originals that still hold their recorded bytes, flushes their directories, and returns warnings for any it kept.
func dropOriginals(gameDir string, originals []OriginalFile) []string {
	var warnings []string
	dirty := map[string]bool{}
	for _, original := range originals {
		full, err := safeGamePath(gameDir, OriginalsDir+"/"+original.Path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("kept saved original %s: %v", original.Path, err))
			continue
		}
		info, err := lstatOptional(full)
		if err != nil || info == nil {
			continue
		}
		got, _, err := hashRegular(full, false)
		if err != nil || got.SHA256 != original.SHA256 || got.Size != original.Size {
			warnings = append(warnings, fmt.Sprintf("kept saved original %s: it no longer matches the install record", original.Path))
			continue
		}
		if err := os.Remove(full); err != nil {
			warnings = append(warnings, fmt.Sprintf("kept saved original %s: %v", original.Path, err))
			continue
		}
		dirty[filepath.Dir(full)] = true
		for dir := path.Dir(original.Path); dir != "."; dir = path.Dir(dir) {
			removeIfEmpty(filepath.Join(gameDir, OriginalsDir, filepath.FromSlash(dir)))
			dirty[filepath.Dir(filepath.Join(gameDir, OriginalsDir, filepath.FromSlash(dir)))] = true
		}
	}
	if err := syncDirs(dirty); err != nil {
		warnings = append(warnings, fmt.Sprintf("flushing saved originals: %v", err))
	}
	return warnings
}

// cleanupOp removes the operation's backup and work directories best-effort and returns what it could not remove.
func cleanupOp(gameDir string, intent *Intent) []string {
	var warnings []string
	for _, reserved := range []string{BackupDir, WorkDir} {
		root := filepath.Join(gameDir, reserved)
		if !isPlainDir(root) {
			continue
		}
		dir := filepath.Join(root, intent.OpID)
		info, err := lstatOptional(dir)
		if err != nil || info == nil {
			continue
		}
		if !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("left %s/%s: not a directory", reserved, intent.OpID))
			continue
		}
		if err := removeOwnedTree(dir); err != nil {
			warnings = append(warnings, fmt.Sprintf("left %s/%s: %v", reserved, intent.OpID, err))
		}
		removeIfEmpty(root)
	}
	_ = syncDir(gameDir)
	return warnings
}

// Recover finishes or rolls back an interrupted mod-loader transaction and sweeps abandoned work and backup directories.
func Recover(gameDir string, killer func(pid int, start uint64) error) (RecoverResult, error) {
	return recoverGame(gameDir, killer, nil)
}

// recoverGame is Recover with an optional fault hook.
func recoverGame(gameDir string, killer func(pid int, start uint64) error, hook func(string) error) (RecoverResult, error) {
	if killer == nil {
		killer = killRecorded
	}
	if err := checkGameDir(gameDir); err != nil {
		return RecoverResult{}, err
	}
	mount, err := mountOf(gameDir)
	if err != nil {
		return RecoverResult{}, err
	}
	if err := checkReservedDirs(gameDir, mount); err != nil {
		return RecoverResult{}, err
	}
	unlock, err := lockGame(gameDir)
	if err != nil {
		return RecoverResult{}, err
	}
	defer unlock()
	var result RecoverResult
	intent, err := readIntent(gameDir)
	if err != nil {
		return result, err
	}
	if intent != nil {
		result, err = recoverIntent(gameDir, intent, killer, hook)
		if err != nil {
			return result, err
		}
	}
	result.Warnings = append(result.Warnings, sweepAtomicTemps(gameDir)...)
	swept, warnings := sweepBackups(gameDir)
	result.SweptBackups = swept
	result.Warnings = append(result.Warnings, warnings...)
	swept, warnings, err = sweepWorkDirs(gameDir, killer)
	result.SweptWorkDirs = swept
	result.Warnings = append(result.Warnings, warnings...)
	return result, err
}

// recoverIntent resolves one journaled transaction as committed or rolled back.
func recoverIntent(gameDir string, intent *Intent, killer func(pid int, start uint64) error, hook func(string) error) (RecoverResult, error) {
	if intent.OwnerPID > 0 && intent.OwnerPID != os.Getpid() && processAlive(intent.OwnerPID, intent.OwnerStart) {
		return RecoverResult{}, fmt.Errorf("mod-loader operation %s belongs to live process %d: %w", intent.OpID, intent.OwnerPID, ErrTransactionActive)
	}
	if intent.InstallerPID > 0 {
		if err := killer(intent.InstallerPID, intent.InstallerStart); err != nil {
			return RecoverResult{}, fmt.Errorf("stopping recorded installer process %d: %w", intent.InstallerPID, err)
		}
	}
	for _, dir := range []string{BackupDir + "/" + intent.OpID, intent.WorkDir} {
		info, err := lstatOptional(filepath.Join(gameDir, filepath.FromSlash(dir)))
		if err != nil {
			return RecoverResult{}, err
		}
		if info != nil && !info.IsDir() {
			return RecoverResult{}, &UnsafeTargetError{Path: dir, Reason: "reserved path is not a real directory"}
		}
	}
	committed, err := intentCommitted(gameDir, intent)
	if err != nil {
		return RecoverResult{}, err
	}
	if committed {
		warnings, err := finishCommitted(gameDir, intent, hook)
		return RecoverResult{Committed: true, Warnings: warnings}, err
	}
	if err := rollback(gameDir, intent, hook); err != nil {
		return RecoverResult{}, fmt.Errorf("rolling back mod-loader operation %s: %w", intent.OpID, err)
	}
	warnings, err := finishRolledBack(gameDir, intent, hook)
	return RecoverResult{RolledBack: true, Warnings: warnings}, err
}

// intentCommitted reports whether the journaled transaction reached its commit point.
func intentCommitted(gameDir string, intent *Intent) (bool, error) {
	if intent.Phase != PhaseCommitting {
		return false, nil
	}
	if intent.Op == OpUninstall {
		return true, nil
	}
	record, err := ReadRecord(gameDir)
	if err != nil {
		return false, err
	}
	return record != nil && record.OpID == intent.OpID, nil
}

// rollback restores every preimage of an uncommitted transaction and removes only entries whose identity proves the transaction placed them.
func rollback(gameDir string, intent *Intent, hook func(string) error) error {
	mount, err := mountOf(gameDir)
	if err != nil {
		return err
	}
	r := &restorer{
		gameDir:    gameDir,
		backupRoot: filepath.Join(gameDir, BackupDir, intent.OpID),
		mount:      mount,
		intent:     intent,
		hook:       hook,
		dirty:      map[string]bool{gameDir: true},
	}
	var errs []error
	for _, rel := range sortedUnique(append(append([]string{}, intent.Place...), intent.Remove...)) {
		if err := r.restore(rel); err != nil {
			if isFault(err) {
				return err
			}
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
		}
	}
	if len(errs) == 0 {
		if err := r.dropNewOriginals(); err != nil {
			if isFault(err) {
				return err
			}
			errs = append(errs, err)
		}
		r.dropNewDirs()
	}
	if err := syncDirs(r.dirty); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// restore returns rel to its preimage, removing an entry the transaction placed first, and treats an entry holding the preimage identity as restored.
func (r *restorer) restore(rel string) error {
	target := filepath.Join(r.gameDir, filepath.FromSlash(rel))
	if err := guardAncestors(r.gameDir, r.mount, target); err != nil {
		return err
	}
	current, err := lstatOptional(target)
	if err != nil {
		return err
	}
	preimage, existed := r.intent.Existing[rel]
	if existed && current != nil && identify(current) == preimage {
		return nil
	}
	saved := filepath.Join(r.backupRoot, filepath.FromSlash(rel))
	if existed {
		if err := guardAncestors(r.gameDir, r.mount, saved); err != nil {
			return err
		}
		backup, err := lstatOptional(saved)
		if err != nil {
			return err
		}
		if backup == nil || identify(backup) != preimage {
			return errors.New("its preimage is missing from the backup")
		}
	}
	if current != nil {
		placed, ok := r.intent.Placed[rel]
		if !ok || identify(current) != placed {
			return errors.New("an entry the transaction did not place is in the way")
		}
		if err := runStep(r.hook, "rollback-remove:"+rel); err != nil {
			return err
		}
		if err := removeOwnedTree(target); err != nil {
			return err
		}
		r.dirty[filepath.Dir(target)] = true
	}
	if !existed {
		return nil
	}
	if err := runStep(r.hook, "rollback:"+rel); err != nil {
		return err
	}
	for _, candidate := range []string{saved, target} {
		if err := guardAncestors(r.gameDir, r.mount, candidate); err != nil {
			return err
		}
	}
	if err := renameNoReplace(saved, target); err != nil {
		return err
	}
	r.dirty[filepath.Dir(target)] = true
	r.dirty[filepath.Dir(saved)] = true
	return nil
}

// dropNewOriginals deletes the originals this transaction captured while they still hold the captured bytes.
func (r *restorer) dropNewOriginals() error {
	for _, original := range r.intent.NewOriginals {
		full := filepath.Join(r.gameDir, OriginalsDir, filepath.FromSlash(original.Path))
		if err := guardAncestors(r.gameDir, r.mount, full); err != nil {
			return err
		}
		info, err := lstatOptional(full)
		if err != nil {
			return err
		}
		if info == nil {
			continue
		}
		got, _, err := hashRegular(full, false)
		if err != nil || got.SHA256 != original.SHA256 || got.Size != original.Size {
			return fmt.Errorf("saved original %s was modified", original.Path)
		}
		if err := runStep(r.hook, "rollback-original:"+original.Path); err != nil {
			return err
		}
		if err := os.Remove(full); err != nil {
			return err
		}
		r.dirty[filepath.Dir(full)] = true
		for dir := path.Dir(original.Path); dir != "."; dir = path.Dir(dir) {
			removeIfEmpty(filepath.Join(r.gameDir, OriginalsDir, filepath.FromSlash(dir)))
		}
	}
	if len(r.intent.NewOriginals) > 0 {
		removeIfEmpty(filepath.Join(r.gameDir, OriginalsDir))
	}
	return nil
}

// dropNewDirs removes the directories the transaction created for placed entries, deepest first, when they are empty.
func (r *restorer) dropNewDirs() {
	dirs := append([]string{}, r.intent.NewDirs...)
	sort.Slice(dirs, func(i, j int) bool { return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/") })
	for _, dir := range dirs {
		full := filepath.Join(r.gameDir, filepath.FromSlash(dir))
		if guardAncestors(r.gameDir, r.mount, full) != nil {
			continue
		}
		removeIfEmpty(full)
		r.dirty[filepath.Dir(full)] = true
	}
}

// sweepAtomicTemps removes atomicfile temporaries of the loader state files in the game root and inside the originals tree.
func sweepAtomicTemps(gameDir string) []string {
	var warnings []string
	remove := func(full string) {
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("left %s: %v", full, err))
		}
	}
	if entries, err := os.ReadDir(gameDir); err == nil {
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			for _, base := range []string{RecordFile, IntentFile} {
				if isAtomicTemp(entry.Name(), base) {
					remove(filepath.Join(gameDir, entry.Name()))
				}
			}
		}
	}
	originals := filepath.Join(gameDir, OriginalsDir)
	if isPlainDir(originals) {
		_ = filepath.WalkDir(originals, func(full string, entry fs.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() && isAtomicTemp(entry.Name(), "") {
				remove(full)
			}
			return nil
		})
	}
	return warnings
}

// isAtomicTemp reports whether name is an atomicfile temporary, for base when base is not empty.
func isAtomicTemp(name, base string) bool {
	if !strings.HasPrefix(name, atomicTempPrefix+base) {
		return false
	}
	dash := strings.LastIndexByte(name, '-')
	suffix := name[dash+1:]
	if dash < len(atomicTempPrefix) || suffix == "" {
		return false
	}
	if base != "" && name[:dash] != atomicTempPrefix+base {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sweepBackups removes backup directories no intent references; without an intent they belong to committed or rolled-back operations.
func sweepBackups(gameDir string) (int, []string) {
	root := filepath.Join(gameDir, BackupDir)
	if !isPlainDir(root) {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, []string{fmt.Sprintf("reading %s: %v", BackupDir, err)}
	}
	swept := 0
	var warnings []string
	for _, entry := range entries {
		if validateOpID(entry.Name()) != nil {
			warnings = append(warnings, fmt.Sprintf("left unknown entry %s/%s", BackupDir, entry.Name()))
			continue
		}
		if err := removeOwnedTree(filepath.Join(root, entry.Name())); err != nil {
			warnings = append(warnings, fmt.Sprintf("left %s/%s: %v", BackupDir, entry.Name(), err))
			continue
		}
		swept++
	}
	removeIfEmpty(root)
	return swept, warnings
}

// sweepWorkDirs stops any recorded live installer and removes every abandoned work directory, failing only when a live installer cannot be stopped.
func sweepWorkDirs(gameDir string, killer func(pid int, start uint64) error) (int, []string, error) {
	root := filepath.Join(gameDir, WorkDir)
	if !isPlainDir(root) {
		return 0, nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, nil, err
	}
	swept := 0
	var warnings []string
	var errs []error
	for _, entry := range entries {
		full := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			if owner, err := readOwner(full); err == nil && owner.PID > 0 {
				if err := killer(owner.PID, owner.Start); err != nil {
					errs = append(errs, fmt.Errorf("stopping installer process %d: %w", owner.PID, err))
					continue
				}
			}
		}
		if err := removeOwnedTree(full); err != nil {
			warnings = append(warnings, fmt.Sprintf("left %s/%s: %v", WorkDir, entry.Name(), err))
			continue
		}
		swept++
	}
	removeIfEmpty(root)
	return swept, warnings, errors.Join(errs...)
}

// readOwner loads the recorded installer process of a work directory.
func readOwner(workDir string) (ownerInfo, error) {
	data, err := readSmallRegular(filepath.Join(workDir, ownerFileName), maxStateBytes)
	if err != nil {
		return ownerInfo{}, err
	}
	var owner ownerInfo
	if err := json.Unmarshal(data, &owner); err != nil {
		return ownerInfo{}, err
	}
	return owner, nil
}

// writeOwner atomically records the installer process of a work directory.
func writeOwner(workDir string, owner ownerInfo) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(workDir, ownerFileName), data, stateFileMode)
}

// writeIntent atomically writes the transaction journal.
func writeIntent(gameDir string, intent *Intent) error {
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(gameDir, IntentFile), data, stateFileMode); err != nil {
		return fmt.Errorf("writing mod-loader intent: %w", err)
	}
	return nil
}

// readIntent loads and validates the transaction journal, returning nil when there is none.
func readIntent(gameDir string) (*Intent, error) {
	full := filepath.Join(gameDir, IntentFile)
	info, err := lstatOptional(full)
	if err != nil || info == nil {
		return nil, err
	}
	data, err := readSmallRegular(full, maxStateBytes)
	if err != nil {
		return nil, fmt.Errorf("reading mod-loader intent: %w", err)
	}
	var intent Intent
	if err := json.Unmarshal(data, &intent); err != nil {
		return nil, fmt.Errorf("decoding mod-loader intent: %w", err)
	}
	if err := intent.validate(); err != nil {
		return nil, err
	}
	return &intent, nil
}

// validate checks the journal schema, identifiers, and every recorded path.
func (i *Intent) validate() error {
	if i.SchemaVersion != intentSchema {
		return fmt.Errorf("mod-loader intent has unsupported schema %d", i.SchemaVersion)
	}
	if err := validateOpID(i.OpID); err != nil {
		return fmt.Errorf("mod-loader intent: %w", err)
	}
	if i.Op != OpInstall && i.Op != OpUninstall {
		return fmt.Errorf("mod-loader intent has unknown operation %q", i.Op)
	}
	if i.Phase != PhaseBackingUp && i.Phase != PhasePlacing && i.Phase != PhaseCommitting {
		return fmt.Errorf("mod-loader intent has unknown phase %q", i.Phase)
	}
	if i.WorkDir != WorkDir+"/"+i.OpID {
		return fmt.Errorf("mod-loader intent names foreign work directory %q", i.WorkDir)
	}
	if i.OwnerPID < 0 || i.InstallerPID < 0 {
		return errors.New("mod-loader intent has a negative process id")
	}
	all := append(append([]string{}, i.Place...), i.Remove...)
	for n, rel := range all {
		if err := validateRelPath(rel); err != nil {
			return fmt.Errorf("mod-loader intent target: %w", err)
		}
		for _, other := range all[:n] {
			if overlaps(rel, other) {
				return fmt.Errorf("mod-loader intent targets %q and %q overlap", other, rel)
			}
		}
	}
	for rel := range i.Existing {
		if !contains(all, rel) {
			return fmt.Errorf("mod-loader intent lists unknown existing target %q", rel)
		}
	}
	for rel := range i.Placed {
		if !contains(i.Place, rel) {
			return fmt.Errorf("mod-loader intent lists unknown placed target %q", rel)
		}
	}
	for rel := range i.Restored {
		if _, ok := i.Placed[rel]; !ok {
			return fmt.Errorf("mod-loader intent lists unknown restored target %q", rel)
		}
	}
	for _, rel := range i.NewDirs {
		if err := validateRelPath(rel); err != nil {
			return fmt.Errorf("mod-loader intent directory: %w", err)
		}
	}
	for _, group := range [][]OriginalFile{i.NewOriginals, i.DropOriginals} {
		for _, original := range group {
			if err := validateRelPath(original.Path); err != nil {
				return fmt.Errorf("mod-loader intent original: %w", err)
			}
			if err := validateDigest(original.SHA256, original.Size, original.Mode); err != nil {
				return fmt.Errorf("mod-loader intent original %s: %w", original.Path, err)
			}
		}
	}
	return nil
}

// removeIntent deletes the transaction journal and flushes the game directory.
func removeIntent(gameDir string) error {
	if err := os.Remove(filepath.Join(gameDir, IntentFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing mod-loader intent: %w", err)
	}
	return syncDir(gameDir)
}

// runStep invokes an optional fault hook for a named step.
func runStep(hook func(string) error, name string) error {
	if hook == nil {
		return nil
	}
	return hook(name)
}

// isFault reports whether err is a simulated crash rather than a real failure.
func isFault(err error) bool {
	return errors.Is(err, errInjectedFault)
}

// validateOpID requires id to be a canonical UUID.
func validateOpID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("invalid operation id %q", id)
	}
	return nil
}

// sortedUnique returns the distinct values in ascending order.
func sortedUnique(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	return sortedSet(set)
}

// sortedIDKeys returns the keys of an identity map in ascending order.
func sortedIDKeys(values map[string]FileID) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// contains reports whether values holds value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
