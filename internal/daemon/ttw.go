package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/game"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/tools"
)

type TTWBackend int

const (
	TTWBackendNative TTWBackend = iota
	TTWBackendWine
)

func (b TTWBackend) String() string {
	switch b {
	case TTWBackendNative:
		return "native"
	case TTWBackendWine:
		return "wine"
	default:
		return "unknown"
	}
}

const (
	pinnedNativeInstallerVersion      = "0.1.8"
	pinnedNativeInstallerSHA256       = "54b44e73c2b4f0c4b72dce8a76d667f7a8a1955e6799697b04f38f2970950ef4"
	pinnedNativeInstallerBinarySHA256 = "2288d47f8e026a029f84c3fe866fbd5cffd862da7ad37f705baef37c37deb04b"
	pinnedNativeInstallerURL          = "https://github.com/SulfurNitride/TTW_Linux_Installer/releases/download/" +
		pinnedNativeInstallerVersion + "/mpi-installer-linux-x86_64.zip"
	pinnedNativeInstallerArchiveEntry = "mpi_installer"
)

const minTTWFreeBytes int64 = 50 * 1024 * 1024 * 1024

type TTWPrereqStatus struct {
	Backend TTWBackend

	GstreamerInstalled  bool
	GstreamerCodecsHint string
	XdeltaInstalled     bool
	DiskSpaceAvailable  int64
	DiskSpaceRequired   int64
	FNVVanilla          bool

	MpiInstallerPath    string
	MpiInstallerVersion string

	PrefixExists          bool
	HasDotnet48           bool
	DotNet48ReleaseRev    uint32
	HasMsxml6             bool
	HasVcrun2022          bool
	HasCorefonts          bool
	MonoNeedsRemoval      bool
	SteamRunning          bool
	ProtontricksAvailable bool
	WinetricksAvailable   bool

	Missing []string
}

type TTWInstallerInfo struct {
	Backend       TTWBackend
	MpiFile       string
	InstallerExe  string
	Version       string
	AlternateMpis []string
}

type TTWInstallResult struct {
	InstallerExitCode int
	OutputTail        string
	ChangedExesInRoot []ExeDelta
	DataModExes       []ExeDelta
	DataModFileCount  int
	DataModBytes      int64
	LayoutFixed       bool
}

type ExeDelta struct {
	RelPath string
	Kind    string
	Size    int64
	MTime   time.Time
	SHA256  string
}

type TTWInstallHandle struct {
	ID       string
	Backend  TTWBackend
	Done     <-chan struct{}
	cancel   func()
	resultMu sync.Mutex
	result   *TTWInstallResult
	err      error
}

// Cancel runs the backend-appropriate teardown. Idempotent.
func (h *TTWInstallHandle) Cancel(ctx context.Context) {
	if h.cancel != nil {
		h.cancel()
	}
}

// Result returns the install result once Done has closed; nil + nil before
func (h *TTWInstallHandle) Result() (*TTWInstallResult, error) {
	h.resultMu.Lock()
	defer h.resultMu.Unlock()
	return h.result, h.err
}

var ttwInstalls = struct {
	sync.Mutex
	m map[string]*TTWInstallHandle
}{m: map[string]*TTWInstallHandle{}}

var ttwIDCounter atomic.Uint64

func mintTTWInstallID(backend TTWBackend) string {
	return fmt.Sprintf("ttw-%s-%d", backend.String(), ttwIDCounter.Add(1))
}

// PrepareTTWInstallerInternal resolves a user-supplied path into a
func (tt *TTWService) PrepareTTWInstallerInternal(userPath string, backend TTWBackend) (TTWInstallerInfo, error) {
	if userPath == "" {
		return TTWInstallerInfo{}, fmt.Errorf("PrepareTTWInstaller: path is required")
	}
	abs, err := filepath.Abs(userPath)
	if err != nil {
		return TTWInstallerInfo{}, fmt.Errorf("absolute path: %w", err)
	}

	tt.s.mu.RLock()
	_, hasFO3 := tt.s.config.Games["fallout3"]
	_, hasFNV := tt.s.config.Games["falloutnv"]
	tt.s.mu.RUnlock()
	if !hasFO3 || !hasFNV {
		return TTWInstallerInfo{}, fmt.Errorf("TTW install requires both Fallout 3 and Fallout: New Vegas to be configured")
	}

	info := TTWInstallerInfo{Backend: backend}
	stat, err := os.Stat(abs)
	if err != nil {
		return info, fmt.Errorf("stat %s: %w", abs, err)
	}

	dir := abs
	if !stat.IsDir() {
		dir = filepath.Dir(abs)
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".mpi":
			info.MpiFile = abs
		case ".exe":
			info.InstallerExe = abs
		default:
			return info, fmt.Errorf("expected .mpi or .exe, got %s", filepath.Ext(abs))
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return info, fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".mpi":
			full := filepath.Join(dir, name)
			if info.MpiFile == "" {
				info.MpiFile = full
			} else if full != info.MpiFile {
				info.AlternateMpis = append(info.AlternateMpis, full)
			}
		case ".exe":
			if backend == TTWBackendWine && info.InstallerExe == "" &&
				strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), "TTW Install") {
				info.InstallerExe = filepath.Join(dir, name)
			}
		}
	}

	if info.MpiFile == "" {
		return info, fmt.Errorf("no .mpi file found in %s", dir)
	}
	if backend == TTWBackendWine && info.InstallerExe == "" {
		return info, fmt.Errorf("Wine backend requires TTW Install.exe alongside the .mpi (not found in %s)", dir)
	}
	info.Version = parseTTWVersionFromMpi(info.MpiFile)
	return info, nil
}

// parseTTWVersionFromMpi extracts a "3.4" / "3.4.0" version string from
func parseTTWVersionFromMpi(mpiPath string) string {
	base := filepath.Base(mpiPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	for _, sep := range []string{"_", "-", " "} {
		parts := strings.Split(base, sep)
		for i := len(parts) - 1; i >= 0; i-- {
			p := parts[i]
			if p == "" {
				continue
			}
			if p[0] >= '0' && p[0] <= '9' {
				return p
			}
		}
	}
	return ""
}

// CheckTTWPrereqsInternal is the daemon-private form of CheckTTWPrereqs
func (tt *TTWService) CheckTTWPrereqsInternal(backend TTWBackend) (TTWPrereqStatus, error) {
	st := TTWPrereqStatus{Backend: backend}
	st.GstreamerInstalled = detectGstreamer()
	st.GstreamerCodecsHint = "gstreamer1.0-libav, gstreamer1.0-plugins-good (apt) — gst-libav, gst-plugins-good (pacman)"
	st.XdeltaInstalled = onPath("xdelta3")

	free, err := tt.checkTTWDiskBytes()
	if err == nil {
		st.DiskSpaceAvailable = free
		st.DiskSpaceRequired = minTTWFreeBytes
	}

	if mm, ok := tt.s.mountMgrs["falloutnv"]; ok {
		st.FNVVanilla = !mm.IsMounted()
	} else {
		st.FNVVanilla = true
	}

	switch backend {
	case TTWBackendNative:
		path, version, ok := tt.locateMpiInstaller()
		if ok {
			st.MpiInstallerPath = path
			st.MpiInstallerVersion = version
		}
	case TTWBackendWine:
		tt.populateWinePrereqs(&st)
	}

	st.Missing = collectMissing(&st)
	return st, nil
}

// collectMissing builds a friendly "what's blocking the user" list from a
func collectMissing(st *TTWPrereqStatus) []string {
	var missing []string
	if !st.FNVVanilla {
		missing = append(missing, "FNV's VFS is currently mounted (TTW needs vanilla Data/)")
	}
	if !st.GstreamerInstalled {
		missing = append(missing, "GStreamer codecs (in-game music)")
	}
	if !st.XdeltaInstalled {
		missing = append(missing, "xdelta3")
	}
	if st.DiskSpaceRequired > 0 && st.DiskSpaceAvailable < st.DiskSpaceRequired {
		missing = append(missing, "disk space (≥50 GB free)")
	}
	switch st.Backend {
	case TTWBackendNative:
		if st.MpiInstallerPath == "" {
			missing = append(missing, "mpi_installer (Backend B)")
		}
	case TTWBackendWine:
		if !st.PrefixExists {
			missing = append(missing, "FNV Proton prefix (run BootstrapFNVPrefix)")
		}
		if st.MonoNeedsRemoval {
			missing = append(missing, "Wine Mono (must uninstall before .NET 4.8)")
		}
		if !st.HasDotnet48 {
			missing = append(missing, ".NET Framework 4.8")
		}
		if !st.HasVcrun2022 {
			missing = append(missing, "VC++ 2015–2022 redistributables")
		}
		if !st.SteamRunning {
			missing = append(missing, "Steam (must be running)")
		}
		if !st.ProtontricksAvailable {
			missing = append(missing, "protontricks (Flatpak / pipx)")
		}
		if !st.WinetricksAvailable {
			missing = append(missing, "winetricks")
		}
	}
	return missing
}

func (tt *TTWService) locateMpiInstaller() (string, string, bool) {
	candidates := []string{}
	if p, err := exec.LookPath("mpi_installer"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		filepath.Join(config.DataDir(), "bin", "mpi_installer"),
	)
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.Mode()&0o111 != 0 {
			version, _ := readMpiInstallerVersion(c)
			return c, version, true
		}
	}
	return "", "", false
}

func readMpiInstallerVersion(path string) (string, error) {
	cmd := exec.Command(path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// populateWinePrereqs fills the Backend A fields by inspecting FNV's
func (tt *TTWService) populateWinePrereqs(st *TTWPrereqStatus) {
	st.SteamRunning = tools.SteamIsRunningForTTW()
	st.ProtontricksAvailable = onPath("protontricks") || onPath("flatpak")
	st.WinetricksAvailable = onPath("winetricks")

	tt.s.mu.RLock()
	fnv, ok := tt.s.config.Games["falloutnv"]
	tt.s.mu.RUnlock()
	if !ok {
		return
	}
	prefixPath, ok := tt.fnvPrefixPath(fnv)
	if !ok {
		return
	}
	if _, err := os.Stat(prefixPath); err == nil {
		st.PrefixExists = true
	}
	st.DotNet48ReleaseRev = readDotNet48ReleaseRev(prefixPath)
	st.HasDotnet48 = isDotNet48ReleaseRevValid(st.DotNet48ReleaseRev)
	st.MonoNeedsRemoval = prefixHasWineMono(prefixPath)
	st.HasMsxml6 = prefixHasNativeOverride(prefixPath, "msxml6")
	st.HasVcrun2022 = prefixHasVcrun2022(prefixPath)
	st.HasCorefonts = prefixHasCorefonts(prefixPath)
}

// fnvPrefixPath resolves the FNV Proton prefix (compatdata/22380/pfx).
func (tt *TTWService) fnvPrefixPath(fnv config.GameConfig) (string, bool) {
	if tt.s.toolMgr == nil {
		return "", false
	}
	steamRoot, err := tools.FindSteamRootForTTW()
	if err != nil {
		return "", false
	}
	return filepath.Join(steamRoot, "steamapps", "compatdata",
		fmt.Sprintf("%d", fnv.SteamAppID), "pfx"), true
}

func readDotNet48ReleaseRev(prefixPath string) uint32 {
	regPath := filepath.Join(prefixPath, "system.reg")
	data, err := os.ReadFile(regPath)
	if err != nil {
		return 0
	}
	const wantKey = `[Software\\Microsoft\\NET Framework Setup\\NDP\\v4\\Full]`
	idx := strings.Index(string(data), wantKey)
	if idx < 0 {
		return 0
	}
	tail := string(data)[idx:]
	end := strings.Index(tail, "\n[")
	if end > 0 {
		tail = tail[:end]
	}
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"Release"=dword:`) {
			continue
		}
		hex := strings.TrimPrefix(line, `"Release"=dword:`)
		hex = strings.TrimSpace(hex)
		var n uint32
		_, err := fmt.Sscanf(hex, "%x", &n)
		if err != nil {
			continue
		}
		return n
	}
	return 0
}

// isDotNet48ReleaseRevValid checks against the known set of .NET 4.8
func isDotNet48ReleaseRevValid(rev uint32) bool {
	switch rev {
	case 528040, 528049, 528209, 528372, 528449, 528562,
		533320, 533325, 533326, 533391, 533392:
		return true
	}
	return rev >= 528040
}

// prefixHasWineMono detects a Wine Mono install in the prefix that must
func prefixHasWineMono(prefixPath string) bool {
	monoDir := filepath.Join(prefixPath, "drive_c", "windows", "mono")
	if info, err := os.Stat(monoDir); err == nil && info.IsDir() {
		return true
	}
	return false
}

// prefixHasNativeOverride checks whether a DLL override has been registered
func prefixHasNativeOverride(prefixPath, dllName string) bool {
	regPath := filepath.Join(prefixPath, "user.reg")
	data, err := os.ReadFile(regPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"`+dllName+`"=`)
}

// prefixHasVcrun2022 checks for the 2015–2022 redistributable's filesystem
func prefixHasVcrun2022(prefixPath string) bool {
	for _, dll := range []string{"vcruntime140.dll", "msvcp140.dll", "vcruntime140_1.dll"} {
		path := filepath.Join(prefixPath, "drive_c", "windows", "syswow64", dll)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// prefixHasCorefonts looks for Arial.TTF as a stand-in for the corefonts
func prefixHasCorefonts(prefixPath string) bool {
	for _, p := range []string{"arial.ttf", "Arial.ttf", "ARIAL.TTF"} {
		full := filepath.Join(prefixPath, "drive_c", "windows", "Fonts", p)
		if _, err := os.Stat(full); err == nil {
			return true
		}
	}
	return false
}

// detectGstreamer is a host-level check (gstreamer codecs cannot be
func detectGstreamer() bool {
	return onPath("gst-launch-1.0") || onPath("gst-inspect-1.0")
}

// onPath returns true if the named binary is on $PATH.
func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// CheckTTWDiskSpace exposes the disk-space pre-flight to the IPC layer.
func (tt *TTWService) CheckTTWDiskSpace() (free int64, required int64, err error) {
	free, err = tt.checkTTWDiskBytes()
	return free, minTTWFreeBytes, err
}

// checkTTWDiskBytes returns the bytes available on the filesystem holding
func (tt *TTWService) checkTTWDiskBytes() (int64, error) {
	target := os.Getenv("GORGANIZER_ROOT")
	if target == "" {
		target = config.DataDir()
	}
	if _, err := os.Stat(target); err != nil {
		for target != "/" && target != "." {
			target = filepath.Dir(target)
			if _, err := os.Stat(target); err == nil {
				break
			}
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(target, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", target, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// CheckFNVNotMounted refuses TTW installer launches while FNV's VFS is
func (tt *TTWService) CheckFNVNotMounted() error {
	mm, ok := tt.s.mountMgrs["falloutnv"]
	if !ok {
		return nil
	}
	if mm.IsMounted() {
		return &ErrTTWRequiresVanillaFNV{}
	}
	return nil
}

func (tt *TTWService) ensureNativeMpiInstaller() (string, error) {
	if path, _, ok := tt.locateMpiInstaller(); ok {
		return path, nil
	}
	if pinnedNativeInstallerSHA256 == "" {
		return "", fmt.Errorf("mpi_installer not on PATH and auto-download is disabled (no pinned sha256). " +
			"Install via your distro's package manager or place the binary at ~/.local/share/gorganizer/bin/mpi_installer")
	}

	binDir := filepath.Join(config.DataDir(), "bin")
	if _, err := config.EnsureDir(binDir); err != nil {
		return "", fmt.Errorf("creating %s: %w", binDir, err)
	}
	dest := filepath.Join(binDir, "mpi_installer")

	zipBytes, err := downloadAndVerifyZip(pinnedNativeInstallerURL, pinnedNativeInstallerSHA256)
	if err != nil {
		return "", err
	}

	binBytes, err := extractFromZip(zipBytes, pinnedNativeInstallerArchiveEntry)
	if err != nil {
		return "", fmt.Errorf("extracting %s from upstream zip: %w",
			pinnedNativeInstallerArchiveEntry, err)
	}

	if pinnedNativeInstallerBinarySHA256 != "" {
		got := sha256.Sum256(binBytes)
		gotHex := hex.EncodeToString(got[:])
		if !strings.EqualFold(gotHex, pinnedNativeInstallerBinarySHA256) {
			return "", fmt.Errorf("mpi_installer (extracted) sha256 mismatch: got %s, want %s",
				gotHex, pinnedNativeInstallerBinarySHA256)
		}
	}

	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, binBytes, 0o755); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	slog.Info("downloaded mpi_installer", "version", pinnedNativeInstallerVersion, "path", dest)
	return dest, nil
}

const maxNativeInstallerZipBytes = 64 * 1024 * 1024

// downloadAndVerifyZip fetches `url`, verifies its sha256 matches
func downloadAndVerifyZip(url, wantSha256 string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %s", url, resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxNativeInstallerZipBytes+1)
	buf := &bytes.Buffer{}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(buf, hasher), limited); err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if buf.Len() > maxNativeInstallerZipBytes {
		return nil, fmt.Errorf("downloaded zip exceeds %d-byte cap; refusing to extract",
			maxNativeInstallerZipBytes)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, wantSha256) {
		return nil, fmt.Errorf("zip sha256 mismatch for %s: got %s, want %s",
			url, got, wantSha256)
	}
	return buf.Bytes(), nil
}

// extractFromZip returns the bytes of the named file from the in-memory
func extractFromZip(zipBytes []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s in zip: %w", f.Name, err)
		}
		defer rc.Close()
		buf := &bytes.Buffer{}
		if _, err := io.Copy(buf, io.LimitReader(rc, maxNativeInstallerZipBytes+1)); err != nil {
			return nil, fmt.Errorf("reading %s from zip: %w", f.Name, err)
		}
		if buf.Len() > maxNativeInstallerZipBytes {
			return nil, fmt.Errorf("zip entry %s exceeds %d-byte cap; refusing to extract",
				f.Name, maxNativeInstallerZipBytes)
		}
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("entry %q not found in zip", name)
}

// CreateBlankTTWMod creates an empty TTW_Mods/<modName>/ folder ready to
func (tt *TTWService) CreateBlankTTWMod(modName string) (string, error) {
	if modName == "" {
		return "", fmt.Errorf("mod name is required")
	}
	dest := filepath.Join(config.ModsDir("ttw"), modName)
	if _, err := os.Stat(dest); err == nil {
		return "", &ModCollisionError{
			Name:         modName,
			ExistingMods: []string{modName},
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dest, err)
	}
	return dest, nil
}

// LaunchTTWInstallerInternal dispatches to the backend-specific runner.
func (tt *TTWService) LaunchTTWInstallerInternal(info TTWInstallerInfo, dataModName string) (*TTWInstallHandle, error) {
	if err := tt.CheckFNVNotMounted(); err != nil {
		return nil, err
	}
	tt.s.mu.RLock()
	fo3, _ := tt.s.config.Games["fallout3"]
	fnv, _ := tt.s.config.Games["falloutnv"]
	tt.s.mu.RUnlock()
	if fo3.InstallPath == "" || fnv.InstallPath == "" {
		return nil, fmt.Errorf("TTW install requires both Fallout 3 and Fallout: New Vegas configured")
	}

	dest, err := tt.ensureTTWModDir(dataModName)
	if err != nil {
		return nil, err
	}

	pruneFinishedTTWInstalls()

	id := mintTTWInstallID(info.Backend)
	switch info.Backend {
	case TTWBackendNative:
		return tt.launchNativeTTW(id, info, dest, fo3, fnv, dataModName)
	case TTWBackendWine:
		return tt.launchWineTTW(id, info, dest, fo3, fnv, dataModName)
	default:
		return nil, fmt.Errorf("unknown TTW backend: %d", info.Backend)
	}
}

// pruneFinishedTTWInstalls drops handles whose Done channel has already
func pruneFinishedTTWInstalls() {
	ttwInstalls.Lock()
	defer ttwInstalls.Unlock()
	for id, h := range ttwInstalls.m {
		select {
		case <-h.Done:
			delete(ttwInstalls.m, id)
		default:
		}
	}
}

// getTTWInstallResultInternal returns the typed result for an in-flight
func (tt *TTWService) getTTWInstallResultInternal(id string, block bool) (*TTWInstallResult, error) {
	ttwInstalls.Lock()
	h, ok := ttwInstalls.m[id]
	ttwInstalls.Unlock()
	if !ok {
		return nil, fmt.Errorf("no TTW install with id %q", id)
	}
	if block {
		select {
		case <-h.Done:
		case <-time.After(time.Hour):
			return nil, fmt.Errorf("timeout waiting for install %q to finish", id)
		}
	}
	res, err := h.Result()
	if err != nil {
		return res, err
	}
	if res == nil {
		return nil, fmt.Errorf("install %q has not finished yet", id)
	}
	return res, nil
}

// ensureTTWModDir resolves and (if absent) materializes the destination
func (tt *TTWService) ensureTTWModDir(modName string) (string, error) {
	if modName == "" {
		return "", fmt.Errorf("mod name is required")
	}
	dest := filepath.Join(config.ModsDir("ttw"), modName)
	info, err := os.Stat(dest)
	switch {
	case err == nil && info.IsDir():
		entries, readErr := os.ReadDir(dest)
		if readErr != nil {
			return "", fmt.Errorf("reading %s: %w", dest, readErr)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".gorganizer-") {
				continue
			}
			return "", &ModCollisionError{
				Name:         modName,
				ExistingMods: []string{modName},
			}
		}
		return dest, nil
	case err != nil && os.IsNotExist(err):
		if mkErr := os.MkdirAll(dest, 0o755); mkErr != nil {
			return "", fmt.Errorf("creating %s: %w", dest, mkErr)
		}
		return dest, nil
	case err != nil:
		return "", fmt.Errorf("stat %s: %w", dest, err)
	default:
		return "", fmt.Errorf("%s exists and is not a directory", dest)
	}
}

// launchNativeTTW spawns mpi_installer with the resolved paths. Captures
func (tt *TTWService) launchNativeTTW(
	id string, info TTWInstallerInfo, dest string,
	fo3, fnv config.GameConfig, dataModName string,
) (*TTWInstallHandle, error) {
	bin, err := tt.ensureNativeMpiInstaller()
	if err != nil {
		return nil, err
	}
	preSnap := snapshotExeFiles(fnv.InstallPath)

	cmd := exec.Command(bin,
		"install",
		"--mpi", info.MpiFile,
		"--fo3", fo3.InstallPath,
		"--fnv", fnv.InstallPath,
		"--dest", dest,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mpi_installer: %w", err)
	}
	startedAt := time.Now()
	tt.emitTTWInfo(id, "start",
		fmt.Sprintf("backend=native pid=%d mpi=%s dest=%s",
			cmd.Process.Pid, filepath.Base(info.MpiFile), filepath.Base(dest)))

	done := make(chan struct{})
	handle := &TTWInstallHandle{
		ID:      id,
		Backend: TTWBackendNative,
		Done:    done,
		cancel: func() {
			if cmd.Process == nil {
				return
			}
			pgid, _ := syscall.Getpgid(cmd.Process.Pid)
			if pgid > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			} else {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		},
	}
	ttwInstalls.Lock()
	ttwInstalls.m[id] = handle
	ttwInstalls.Unlock()

	go tt.streamTTWPipes(id, stdoutPipe, stderrPipe)
	go tt.runTTWHeartbeat(id, done, startedAt)
	go func() {
		defer close(done)
		err := cmd.Wait()
		exit := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exit = exitErr.ExitCode()
			} else {
				exit = -1
			}
		}
		elapsed := int(time.Since(startedAt).Seconds())
		res, postErr := tt.finalizeTTWInstall(dest, fnv, dataModName, info, exit, preSnap)
		handle.resultMu.Lock()
		handle.result = res
		if postErr != nil {
			handle.err = postErr
		} else if exit != 0 {
			handle.err = fmt.Errorf("mpi_installer exited with code %d", exit)
		}
		handle.resultMu.Unlock()
		tt.emitTTWInfo(id, "exit", fmt.Sprintf("code=%d elapsed=%ds", exit, elapsed))
	}()
	return handle, nil
}

// launchWineTTW runs TTW Install.exe under sanitized Proton in FNV's
func (tt *TTWService) launchWineTTW(
	id string, info TTWInstallerInfo, dest string,
	fo3, fnv config.GameConfig, dataModName string,
) (*TTWInstallHandle, error) {
	if tt.s.toolMgr == nil {
		return nil, fmt.Errorf("Wine backend requires the tool manager")
	}
	preSnap := snapshotExeFiles(fnv.InstallPath)

	rwPaths := []string{dest, fnv.InstallPath, fo3.InstallPath}
	ext, err := tt.s.toolMgr.LaunchExternal(
		"falloutnv", &fnv, info.InstallerExe,
		nil, nil, tt.s.config.PreferredProton, true, rwPaths,
	)
	if err != nil {
		return nil, err
	}
	startedAt := time.Now()
	tt.emitTTWInfo(id, "start",
		fmt.Sprintf("backend=wine pid=%d exe=%s dest=%s",
			ext.PID, filepath.Base(info.InstallerExe), filepath.Base(dest)))

	done := make(chan struct{})
	handle := &TTWInstallHandle{
		ID:      id,
		Backend: TTWBackendWine,
		Done:    done,
		cancel: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ext.Cancel(ctx)
		},
	}
	ttwInstalls.Lock()
	ttwInstalls.m[id] = handle
	ttwInstalls.Unlock()

	go tt.runTTWHeartbeat(id, done, startedAt)
	go func() {
		defer close(done)
		<-ext.Done
		exit := -1
		select {
		case exit = <-ext.ExitCode:
		default:
		}
		elapsed := int(time.Since(startedAt).Seconds())
		res, postErr := tt.finalizeTTWInstall(dest, fnv, dataModName, info, exit, preSnap)
		handle.resultMu.Lock()
		handle.result = res
		if postErr != nil {
			handle.err = postErr
		} else if exit != 0 {
			handle.err = fmt.Errorf("TTW Install.exe exited with code %d", exit)
		}
		handle.resultMu.Unlock()
		tt.emitTTWInfo(id, "exit", fmt.Sprintf("code=%d elapsed=%ds", exit, elapsed))
	}()
	return handle, nil
}

// streamTTWPipes drains the installer's stdout/stderr into the daemon's
func (tt *TTWService) streamTTWPipes(id string, stdout, stderr io.Reader) {
	consume := func(r io.Reader, stream string) {
		buf := make([]byte, 4096)
		var partial string
		var lastLine string
		var lastEmit time.Time
		emit := func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			now := time.Now()
			if line == lastLine && now.Sub(lastEmit) < 100*time.Millisecond {
				return
			}
			lastLine = line
			lastEmit = now
			select {
			case tt.s.statusCh <- dto.StatusEventResult{Info: fmt.Sprintf("[%s:%s] %s", id, stream, line)}:
			default:
			}
		}
		for {
			n, err := r.Read(buf)
			if n > 0 {
				partial += string(buf[:n])
				for {
					idx := strings.IndexAny(partial, "\r\n")
					if idx < 0 {
						break
					}
					emit(partial[:idx])
					partial = partial[idx+1:]
				}
			}
			if err != nil {
				if strings.TrimSpace(partial) != "" {
					emit(partial)
				}
				return
			}
		}
	}
	go consume(stdout, "stdout")
	go consume(stderr, "stderr")
}

func (tt *TTWService) emitTTWInfo(id, kind, msg string) {
	line := fmt.Sprintf("[%s:%s] %s", id, kind, msg)
	select {
	case tt.s.statusCh <- dto.StatusEventResult{Info: line}:
	default:
	}
}

// runTTWHeartbeat emits an `[<id>:tick] elapsed=Ns` event every 5
func (tt *TTWService) runTTWHeartbeat(id string, done <-chan struct{}, started time.Time) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			tt.emitTTWInfo(id, "tick", fmt.Sprintf("elapsed=%ds", int(now.Sub(started).Seconds())))
		}
	}
}

// finalizeTTWInstall runs the post-install steps: layout fix (lift any
func (tt *TTWService) finalizeTTWInstall(
	dest string, fnv config.GameConfig, dataModName string, info TTWInstallerInfo, exit int,
	preFnvExes map[string]exeFingerprint,
) (*TTWInstallResult, error) {
	res := &TTWInstallResult{InstallerExitCode: exit}
	if exit != 0 {
		return res, nil
	}

	nestedData := filepath.Join(dest, "Data")
	if info, err := os.Stat(nestedData); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(nestedData)
		for _, e := range entries {
			from := filepath.Join(nestedData, e.Name())
			to := filepath.Join(dest, e.Name())
			if err := os.Rename(from, to); err != nil {
				slog.Warn("layout-fix rename failed", "from", from, "to", to, "err", err)
			}
		}
		_ = os.Remove(nestedData)
		res.LayoutFixed = true
	}

	var count int
	var bytes int64
	_ = filepath.Walk(dest, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		count++
		bytes += info.Size()
		return nil
	})
	res.DataModFileCount = count
	res.DataModBytes = bytes

	postFnvExes := snapshotExeFiles(fnv.InstallPath)
	for rel, post := range postFnvExes {
		pre, existed := preFnvExes[rel]
		if !existed {
			res.ChangedExesInRoot = append(res.ChangedExesInRoot, ExeDelta{
				RelPath: rel, Kind: "added",
				Size: post.size, MTime: post.mtime,
			})
			continue
		}
		if pre.size != post.size || !pre.mtime.Equal(post.mtime) {
			res.ChangedExesInRoot = append(res.ChangedExesInRoot, ExeDelta{
				RelPath: rel, Kind: "modified",
				Size: post.size, MTime: post.mtime,
			})
		}
	}

	for rel, fp := range snapshotExeFiles(dest) {
		res.DataModExes = append(res.DataModExes, ExeDelta{
			RelPath: rel, Kind: "in-dest",
			Size: fp.size, MTime: fp.mtime,
		})
	}

	masterPath := filepath.Join(dest, "TaleOfTwoWastelands.esm")
	masterHash := partialFileSHA256(masterPath)
	marker := struct {
		Version     string `json:"ttw_version"`
		MasterHash  string `json:"ttw_master_partial_sha256"`
		ModName     string `json:"data_mod_name"`
		FileCount   int    `json:"data_mod_file_count"`
		Bytes       int64  `json:"data_mod_bytes"`
		InstalledAt string `json:"installed_at"`
	}{
		Version:     info.Version,
		MasterHash:  masterHash,
		ModName:     dataModName,
		FileCount:   count,
		Bytes:       bytes,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	markerPath := filepath.Join(fnv.InstallPath, game.TTWMarkerFilename)
	if data, err := json.MarshalIndent(marker, "", "  "); err == nil {
		if werr := os.WriteFile(markerPath, data, 0o644); werr != nil {
			slog.Warn("could not write TTW marker file", "path", markerPath, "err", werr)
		}
	}

	tt.ensureTTWModEnabled(dataModName)

	return res, nil
}

// ensureTTWModEnabled inserts the TTW data-mod entry into every TTW
func (tt *TTWService) ensureTTWModEnabled(modName string) {
	tt.s.invalidateInstalledArchiveCache("ttw")
	profiles, err := tt.s.profileMgr.List("ttw")
	if err != nil || len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: "ttw"}}
	}
	for _, p := range profiles {
		_, entries, err := tt.s.profileMgr.Load("ttw", p.Name)
		if err != nil {
			entries = nil
		}
		updated := entries[:0]
		seen := false
		for _, e := range entries {
			if e.Name == modName {
				seen = true
				e.Enabled = true
			}
			updated = append(updated, e)
		}
		if !seen {
			updated = append(updated, mod.ModListEntry{Name: modName, Enabled: true})
		}
		if err := tt.s.profileMgr.Save(p, updated); err != nil {
			slog.Warn("could not enable TTW mod in modlist.txt",
				"profile", p.Name, "mod", modName, "err", err)
		}
	}
}

type exeFingerprint struct {
	size  int64
	mtime time.Time
}

func snapshotExeFiles(root string) map[string]exeFingerprint {
	out := map[string]exeFingerprint{}
	if root == "" {
		return out
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".exe") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = exeFingerprint{size: info.Size(), mtime: info.ModTime()}
	}
	return out
}

// partialFileSHA256 hashes the first 4 KB + last 4 KB of a file. Cheap
func partialFileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	hasher := sha256.New()
	head := make([]byte, 4096)
	n, _ := f.Read(head)
	hasher.Write(head[:n])
	if info.Size() > int64(len(head)) {
		_, err := f.Seek(-4096, io.SeekEnd)
		if err == nil {
			tail := make([]byte, 4096)
			tn, _ := f.Read(tail)
			hasher.Write(tail[:tn])
		}
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// CancelTTWInstaller looks up the install handle by id and runs its
func (tt *TTWService) CancelTTWInstaller(id string) error {
	ttwInstalls.Lock()
	h, ok := ttwInstalls.m[id]
	ttwInstalls.Unlock()
	if !ok {
		return fmt.Errorf("no TTW install in flight with id %q", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h.Cancel(ctx)
	return nil
}

// SetTTWLauncherExe stores the relative path to the launcher exe (typically
func (tt *TTWService) SetTTWLauncherExe(relPath string) error {
	tt.s.mu.Lock()
	defer tt.s.mu.Unlock()
	gc, ok := tt.s.config.Games["ttw"]
	if !ok {
		return fmt.Errorf("ttw not configured")
	}
	gc.ToolExe = relPath
	tt.s.config.Games["ttw"] = gc
	if err := tt.s.config.Save(); err != nil {
		return err
	}
	slog.Info("TTW launcher exe configured", "rel_path", relPath)
	return nil
}

// VerifyTTWIntegrity runs at TTW launch time. Reports drift via typed
func (tt *TTWService) VerifyTTWIntegrity() error {
	tt.s.mu.RLock()
	fnv, ok := tt.s.config.Games["falloutnv"]
	ttw, _ := tt.s.config.Games["ttw"]
	tt.s.mu.RUnlock()
	if !ok {
		return &TTWDriftError{Reason: "marker-missing"}
	}
	markerPath := filepath.Join(fnv.InstallPath, game.TTWMarkerFilename)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return &TTWDriftError{InstallPath: fnv.InstallPath, Reason: "marker-missing"}
	}
	var marker struct {
		MasterHash string `json:"ttw_master_partial_sha256"`
		ModName    string `json:"data_mod_name"`
	}
	if jerr := json.Unmarshal(data, &marker); jerr != nil {
		return &TTWDriftError{InstallPath: fnv.InstallPath, Reason: "marker-missing"}
	}
	if marker.ModName == "" {
		return &TTWDriftError{InstallPath: fnv.InstallPath, Reason: "marker-missing"}
	}
	masterPath := filepath.Join(config.ModsDir("ttw"), marker.ModName, "TaleOfTwoWastelands.esm")
	if _, err := os.Stat(masterPath); err != nil {
		return &TTWDriftError{InstallPath: fnv.InstallPath, Reason: "ttw-master-missing"}
	}
	got := partialFileSHA256(masterPath)
	if marker.MasterHash != "" && got != marker.MasterHash {
		return &TTWDriftError{InstallPath: fnv.InstallPath, Reason: "hash-mismatch"}
	}
	if !hasXNVSEDlls(fnv.InstallPath) {
		return &ErrXNVSEMissingForTTW{InstallPath: fnv.InstallPath}
	}
	if !IsFNV4GBApplied(fnv.InstallPath) {
		return &ErrFNV4GBNotAppliedForTTW{InstallPath: fnv.InstallPath}
	}
	_ = ttw
	return nil
}

// hasXNVSEDlls reports whether at least one nvse_*.dll lives in the FNV
func hasXNVSEDlls(installPath string) bool {
	entries, err := os.ReadDir(installPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if strings.HasPrefix(low, "nvse_") && strings.HasSuffix(low, ".dll") {
			return true
		}
	}
	return false
}

func (tt *TTWService) BootstrapFNVPrefix() error {
	tt.s.mu.RLock()
	fnv, ok := tt.s.config.Games["falloutnv"]
	tt.s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("FNV not configured")
	}
	if tt.s.toolMgr == nil {
		return fmt.Errorf("tool manager not initialized")
	}
	prefixPath, ok := tt.fnvPrefixPath(fnv)
	if !ok {
		return fmt.Errorf("could not resolve FNV prefix path")
	}
	if _, err := os.Stat(prefixPath); err == nil {
		return nil
	}
	cmd := filepath.Join(prefixPath, "drive_c", "windows", "system32", "cmd.exe")
	ext, err := tt.s.toolMgr.LaunchExternal(
		"falloutnv", &fnv, cmd, []string{"/c", "exit"},
		nil, tt.s.config.PreferredProton, true, []string{prefixPath},
	)
	if err != nil {
		var prefixMissing *tools.ErrPrefixMissing
		if errors.As(err, &prefixMissing) {
			return manualWineboot(prefixPath)
		}
		return err
	}
	<-ext.Done
	return nil
}

// manualWineboot is the fallback bootstrap path: wine wineboot --init
func manualWineboot(prefixPath string) error {
	wine, err := exec.LookPath("wine")
	if err != nil {
		return fmt.Errorf("wine not on PATH for fallback bootstrap: %w", err)
	}
	if err := os.MkdirAll(prefixPath, 0o755); err != nil {
		return fmt.Errorf("creating prefix dir: %w", err)
	}
	cmd := exec.Command(wine, "wineboot", "--init")
	cmd.Env = append(os.Environ(), "WINEPREFIX="+prefixPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wineboot --init: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallTTWPrereqs runs protontricks against FNV's prefix to install
func (tt *TTWService) InstallTTWPrereqs() (string, error) {
	tt.s.mu.RLock()
	fnv, ok := tt.s.config.Games["falloutnv"]
	tt.s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("FNV not configured")
	}
	pt, err := exec.LookPath("protontricks")
	if err != nil {
		if _, ferr := exec.LookPath("flatpak"); ferr == nil {
			pt = "flatpak"
		} else {
			return "", fmt.Errorf("protontricks not on PATH (install via Flatpak or pipx)")
		}
	}

	id := mintTTWInstallID(TTWBackendWine) + "-prereqs"

	args := []string{
		fmt.Sprintf("%d", fnv.SteamAppID),
		"-q", "vcrun2022", "msxml6", "corefonts", "dotnet48",
	}
	var cmd *exec.Cmd
	if pt == "flatpak" {
		cmd = exec.Command("flatpak", append([]string{"run", "com.github.Matoking.protontricks"}, args...)...)
	} else {
		cmd = exec.Command(pt, args...)
	}
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting protontricks: %w", err)
	}
	go tt.streamTTWPipes(id, stdout, stderr)
	go func() {
		_ = cmd.Wait()
	}()
	return id, nil
}
