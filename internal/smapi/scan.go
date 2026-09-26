package smapi

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

type FolderKind uint8

const (
	FolderMod FolderKind = iota
	FolderIgnored
	FolderEmpty
	FolderXnb
	FolderInvalid
	FolderRootManifest
)

type Folder struct {
	RelPath      string
	Kind         FolderKind
	ManifestPath string
	Manifest     *Manifest
	ParseErr     error
	Reason       string
}

type ScanOptions struct {
	FollowSymlinks bool
}

const (
	manifestFileName   = "manifest.json"
	maxManifestSize    = 1 << 20
	maxViewScanDepth   = 64
	vortexMarkerName   = "__folder_managed_by_vortex"
	vortexConfigName   = "config.json"
	reasonDotFolder    = "ignored folder because its name starts with a dot."
	reasonEmptyFolder  = "it's an empty folder."
	reasonEmptyVortex  = "it's an empty Vortex folder (is the mod disabled in Vortex?)."
	reasonXnbMod       = "it's not a SMAPI mod (see https://smapi.io/xnb for info)."
	reasonInstaller    = "the SMAPI installer isn't a mod (you can delete this folder after running the installer file)."
	reasonNoManifest   = "it contains files, but none of them are manifest.json."
	reasonParsePrefix  = "parsing its manifest failed: "
	reasonNullManifest = "its manifest is invalid."
	reasonRootManifest = "SMAPI never loads a manifest.json placed directly in the scanned folder; it must be inside its own mod folder."
)

var (
	ignoredFileExtensions = map[string]bool{
		".doc": true, ".docx": true, ".md": true, ".rtf": true, ".txt": true,
		".bmp": true, ".gif": true, ".ico": true, ".jpeg": true, ".jpg": true, ".png": true, ".psd": true, ".tif": true, ".xcf": true,
		".rar": true, ".zip": true, ".7z": true, ".tar": true, ".tar.gz": true,
		".backup": true, ".bak": true, ".old": true,
		".url": true, ".lnk": true,
	}
	strictXnbExtensions    = map[string]bool{".xgs": true, ".xnb": true, ".xsb": true, ".xwb": true}
	potentialXnbExtensions = map[string]bool{".json": true, ".yaml": true}
	installerFileNames     = map[string]bool{"install on Linux.sh": true, "install on macOS.command": true, "install on Windows.bat": true}
)

type scanEntry struct {
	name    string
	dir     bool
	regular bool
}

type scanSource interface {
	list(dir string) ([]scanEntry, error)
	push(dir string) error
	pop()
	join(dir, name string) string
	readManifest(manifestPath string) ([]byte, error)
}

type scanner struct {
	scanSource
}

type osSource struct {
	follow    bool
	ancestors []os.FileInfo
}

type fsSource struct {
	fsys  fs.FS
	chain []string
}

type DirIdentifier interface {
	DirIdentity(name string) (string, bool)
}

// Scan ports ModScanner.GetModFolders without following symlinks, for untrusted archives and stores.
func Scan(root string) ([]Folder, error) {
	return ScanWith(root, ScanOptions{})
}

// ScanWith ports ModScanner.GetModFolders with root as a search folder, reporting a root manifest.json as FolderRootManifest.
func ScanWith(root string, opts ScanOptions) ([]Folder, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("scanning SMAPI mods at %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scanning SMAPI mods at %s: not a directory", root)
	}
	return scan(&osSource{follow: opts.FollowSymlinks}, root)
}

// ScanFS ports ModScanner.GetModFolders over an abstract directory view whose symlinks are already resolved, with its root as a search folder, never descending into a folder already on the walk chain or deeper than maxViewScanDepth.
func ScanFS(fsys fs.FS) ([]Folder, error) {
	info, err := fs.Stat(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("scanning SMAPI mods: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scanning SMAPI mods: the view root is not a directory")
	}
	return scan(&fsSource{fsys: fsys}, ".")
}

// scan runs the ModScanner port over src from root, reporting a root manifest.json as FolderRootManifest.
func scan(src scanSource, root string) ([]Folder, error) {
	s := &scanner{scanSource: src}
	result := []Folder{}
	manifestPath, err := s.findManifest(root)
	if err != nil {
		return nil, err
	}
	if manifestPath != "" {
		folder := s.readModFolder("", manifestPath)
		folder.Kind = FolderRootManifest
		folder.Reason = reasonRootManifest
		result = append(result, folder)
	}
	children, err := s.searchChildren("", root)
	if err != nil {
		return nil, err
	}
	return append(result, children...), nil
}

// searchChildren scans every subfolder of a search folder in name order.
func (s *scanner) searchChildren(rel, abs string) ([]Folder, error) {
	entries, err := s.list(abs)
	if err != nil {
		return nil, err
	}
	if err := s.push(abs); err != nil {
		return nil, err
	}
	defer s.pop()
	result := []Folder{}
	for _, entry := range entries {
		if !entry.dir {
			continue
		}
		children, err := s.scanFolder(joinRel(rel, entry.name), s.join(abs, entry.name), entry.name)
		if err != nil {
			return nil, err
		}
		result = append(result, children...)
	}
	return result, nil
}

// scanFolder ports the non-root branch of ModScanner.GetModFolders for one folder.
func (s *scanner) scanFolder(rel, abs, name string) ([]Folder, error) {
	if strings.HasPrefix(name, ".") {
		return []Folder{{RelPath: rel, Kind: FolderIgnored, Reason: reasonDotFolder}}, nil
	}
	if !isRelevant(name, false) {
		return nil, nil
	}
	search, err := s.isModSearchFolder(abs)
	if err != nil {
		return nil, err
	}
	if !search {
		folder, err := s.readFolder(rel, abs)
		if err != nil {
			return nil, err
		}
		return []Folder{folder}, nil
	}
	children, err := s.searchChildren(rel, abs)
	if err != nil {
		return nil, err
	}
	return tryConsolidate(rel, children), nil
}

// tryConsolidate ports ModScanner.TryConsolidate, merging empty or XNB-only subfolders into their parent.
func tryConsolidate(rel string, children []Folder) []Folder {
	if len(children) <= 1 {
		return children
	}
	allEmpty, allXnb := true, true
	for _, child := range children {
		if child.Kind != FolderEmpty {
			allEmpty = false
		}
		if child.Kind != FolderXnb && child.Kind != FolderEmpty {
			allXnb = false
		}
	}
	switch {
	case allEmpty:
		return []Folder{{RelPath: rel, Kind: FolderEmpty, Reason: children[0].Reason}}
	case allXnb:
		return []Folder{{RelPath: rel, Kind: FolderXnb, Reason: children[0].Reason}}
	}
	return children
}

// isModSearchFolder reports whether a non-root folder holds relevant subfolders and no relevant files.
func (s *scanner) isModSearchFolder(abs string) (bool, error) {
	entries, err := s.list(abs)
	if err != nil {
		return false, err
	}
	hasDir, hasFile := false, false
	for _, entry := range entries {
		if entry.dir {
			hasDir = hasDir || isRelevant(entry.name, false)
		} else {
			hasFile = hasFile || isRelevant(entry.name, true)
		}
	}
	return hasDir && !hasFile, nil
}

// readFolder ports ModScanner.ReadFolder for a folder that is not a search folder.
func (s *scanner) readFolder(rel, abs string) (Folder, error) {
	manifestPath, err := s.findManifest(abs)
	if err != nil {
		return Folder{}, err
	}
	if manifestPath != "" {
		return s.readModFolder(rel, manifestPath), nil
	}
	files, err := s.recursiveFiles(abs)
	if err != nil {
		return Folder{}, err
	}
	var relevant []string
	for _, name := range files {
		if isRelevant(name, true) {
			relevant = append(relevant, name)
		}
	}
	switch {
	case isEmptyVortexFolder(files):
		return Folder{RelPath: rel, Kind: FolderInvalid, Reason: reasonEmptyVortex}, nil
	case len(relevant) == 0:
		return Folder{RelPath: rel, Kind: FolderEmpty, Reason: reasonEmptyFolder}, nil
	case isXnbMod(relevant):
		return Folder{RelPath: rel, Kind: FolderXnb, Reason: reasonXnbMod}, nil
	}
	for _, name := range relevant {
		if installerFileNames[name] {
			return Folder{RelPath: rel, Kind: FolderInvalid, Reason: reasonInstaller}, nil
		}
	}
	return Folder{RelPath: rel, Kind: FolderInvalid, Reason: reasonNoManifest}, nil
}

// readModFolder reads and parses the manifest of a mod folder, keeping parse failures on the result.
func (s *scanner) readModFolder(rel, manifestPath string) Folder {
	folder := Folder{RelPath: rel, Kind: FolderMod, ManifestPath: manifestPath}
	data, err := s.readManifest(manifestPath)
	if err != nil {
		folder.ParseErr = err
		folder.Reason = reasonParsePrefix + err.Error()
		return folder
	}
	folder.Manifest, folder.ParseErr = ParseManifest(data)
	switch {
	case errors.Is(folder.ParseErr, errNullManifest):
		folder.Reason = reasonNullManifest
	case folder.ParseErr != nil:
		folder.Reason = reasonParsePrefix + folder.ParseErr.Error()
	}
	return folder
}

// readManifestFile reads a manifest file up to the size cap, refusing to open a symlink unless follow is set.
func readManifestFile(manifestPath string, follow bool) ([]byte, error) {
	flags := os.O_RDONLY
	if !follow {
		flags |= syscall.O_NOFOLLOW
	}
	file, err := os.OpenFile(manifestPath, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("%w: manifest %s exceeds %d bytes", ErrInvalidManifest, manifestPath, maxManifestSize)
	}
	return data, nil
}

// findManifest returns the path of a regular manifest.json in folder, preferring the exact name over case variants.
func (s *scanner) findManifest(folder string) (string, error) {
	entries, err := s.list(folder)
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range entries {
		if !entry.regular || !strings.EqualFold(entry.name, manifestFileName) {
			continue
		}
		if entry.name == manifestFileName {
			return s.join(folder, entry.name), nil
		}
		if found == "" {
			found = s.join(folder, entry.name)
		}
	}
	return found, nil
}

// recursiveFiles ports ModScanner.RecursivelyGetFiles, returning file names and skipping irrelevant folders.
func (s *scanner) recursiveFiles(abs string) ([]string, error) {
	entries, err := s.list(abs)
	if err != nil {
		return nil, err
	}
	if err := s.push(abs); err != nil {
		return nil, err
	}
	defer s.pop()
	var result []string
	for _, entry := range entries {
		if !entry.dir {
			result = append(result, entry.name)
			continue
		}
		if !isRelevant(entry.name, false) {
			continue
		}
		nested, err := s.recursiveFiles(s.join(abs, entry.name))
		if err != nil {
			return nil, err
		}
		result = append(result, nested...)
	}
	return result, nil
}

// readManifest reads a manifest file, refusing a symlink unless following symlinks.
func (s *osSource) readManifest(manifestPath string) ([]byte, error) {
	return readManifestFile(manifestPath, s.follow)
}

// join joins a real folder path and a child name.
func (s *osSource) join(dir, name string) string {
	return filepath.Join(dir, name)
}

// list reads a folder in name order, dropping symlinks in strict mode; when following, a broken link counts as a non-manifest file and a link back to a folder being scanned is dropped.
func (s *osSource) list(abs string) ([]scanEntry, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("reading SMAPI folder %s: %w", abs, err)
	}
	var self os.FileInfo
	if s.follow {
		if self, err = os.Stat(abs); err != nil {
			return nil, fmt.Errorf("reading SMAPI folder %s: %w", abs, err)
		}
	}
	result := make([]scanEntry, 0, len(entries))
	for _, entry := range entries {
		item := scanEntry{name: entry.Name()}
		switch {
		case isSymlink(entry):
			if !s.follow {
				continue
			}
			target, err := os.Stat(filepath.Join(abs, entry.Name()))
			if err == nil && target.IsDir() && s.isLoop(target, self) {
				continue
			}
			item.dir = err == nil && target.IsDir()
			item.regular = err == nil && target.Mode().IsRegular()
		case entry.IsDir():
			item.dir = true
		default:
			item.regular = entry.Type().IsRegular()
		}
		result = append(result, item)
	}
	return result, nil
}

// isLoop reports whether a symlinked directory resolves to the listed folder or one of the folders being descended.
func (s *osSource) isLoop(target, self os.FileInfo) bool {
	if os.SameFile(target, self) {
		return true
	}
	for _, ancestor := range s.ancestors {
		if os.SameFile(target, ancestor) {
			return true
		}
	}
	return false
}

// push records a folder being descended so symlinks back into it are treated as loops when following symlinks.
func (s *osSource) push(abs string) error {
	if !s.follow {
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("reading SMAPI folder %s: %w", abs, err)
	}
	s.ancestors = append(s.ancestors, info)
	return nil
}

// pop forgets the most recently descended folder.
func (s *osSource) pop() {
	if s.follow {
		s.ancestors = s.ancestors[:len(s.ancestors)-1]
	}
}

// list reads a view folder in name order, dropping subfolders that resolve to a folder on the walk chain or lie deeper than maxViewScanDepth; entries that are neither directories nor regular files count as non-manifest files.
func (s *fsSource) list(dir string) ([]scanEntry, error) {
	entries, err := fs.ReadDir(s.fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("reading SMAPI folder %s: %w", dir, err)
	}
	self := s.identity(dir)
	result := make([]scanEntry, 0, len(entries))
	for _, entry := range entries {
		item := scanEntry{name: entry.Name(), dir: entry.IsDir(), regular: entry.Type().IsRegular()}
		if item.dir {
			if len(s.chain) >= maxViewScanDepth {
				continue
			}
			if id := s.identity(s.join(dir, entry.Name())); id == self || s.onChain(id) {
				continue
			}
		}
		result = append(result, item)
	}
	return result, nil
}

// push records the identity of a view folder being descended so a folder resolving back to it is never descended again.
func (s *fsSource) push(dir string) error {
	s.chain = append(s.chain, s.identity(dir))
	return nil
}

// pop forgets the most recently descended view folder.
func (s *fsSource) pop() {
	s.chain = s.chain[:len(s.chain)-1]
}

// onChain reports whether id names a view folder being descended.
func (s *fsSource) onChain(id string) bool {
	for _, ancestor := range s.chain {
		if ancestor == id {
			return true
		}
	}
	return false
}

// identity returns the folder a view path resolves to: the view's own identity when it provides one, else the device and inode it reports, else the cleaned path.
func (s *fsSource) identity(name string) string {
	if identifier, ok := s.fsys.(DirIdentifier); ok {
		if id, ok := identifier.DirIdentity(name); ok {
			return id
		}
	}
	if info, err := fs.Stat(s.fsys, name); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			return fmt.Sprintf("inode:%d:%d", st.Dev, st.Ino)
		}
	}
	return "path:" + path.Clean(name)
}

// join joins a slash-separated view folder and a child name.
func (*fsSource) join(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// readManifest reads a view manifest file up to the size cap.
func (s *fsSource) readManifest(manifestPath string) ([]byte, error) {
	file, err := s.fsys.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("%w: manifest %s exceeds %d bytes", ErrInvalidManifest, manifestPath, maxManifestSize)
	}
	return data, nil
}

// isRelevant ports ModScanner.IsRelevant for a file or folder name.
func isRelevant(name string, isFile bool) bool {
	if isFile && (ignoredFileExtensions[strings.ToLower(dotNetExtension(name))] || strings.HasPrefix(name, ".")) {
		return false
	}
	for _, ignored := range []string{vortexMarkerName, ".DS_Store", "__MACOSX", "mcs", "desktop.ini", "Thumbs.db"} {
		if strings.EqualFold(name, ignored) {
			return false
		}
	}
	return !strings.HasPrefix(name, "._")
}

// isXnbMod ports ModScanner.IsXnbMod over relevant file names.
func isXnbMod(names []string) bool {
	hasXnb := false
	for _, name := range names {
		ext := strings.ToLower(dotNetExtension(name))
		if strictXnbExtensions[ext] {
			hasXnb = true
			continue
		}
		if !potentialXnbExtensions[ext] {
			return false
		}
	}
	return hasXnb
}

// isEmptyVortexFolder ports ModScanner.IsEmptyVortexFolder over all file names.
func isEmptyVortexFolder(names []string) bool {
	hasMarker := false
	for _, name := range names {
		if name == vortexMarkerName {
			hasMarker = true
			continue
		}
		if isRelevant(name, true) && name != vortexConfigName {
			return false
		}
	}
	return hasMarker
}

// dotNetExtension returns the extension like .NET's Path.GetExtension, which is empty for a trailing dot.
func dotNetExtension(name string) string {
	ext := path.Ext(name)
	if ext == "." {
		return ""
	}
	return ext
}

// isSymlink reports whether a directory entry is a symbolic link.
func isSymlink(entry os.DirEntry) bool {
	return entry.Type()&os.ModeSymlink != 0
}

// joinRel joins a slash-separated relative path with a child name.
func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}
