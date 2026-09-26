package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/vfs"
)

const (
	farmBaseLayerName  = "__base__"
	maxViewSymlinkHops = 40
)

var errViewLoop = errors.New("symlink loops back into the view")

type farmVisit struct {
	vpath string
	real  string
	dir   bool
}

type farmFile struct {
	real  string
	layer int
}

type farmView struct {
	layers     []projectionLayer
	farmRoot   string
	farmDev    uint64
	farmDevOK  bool
	farmDevSet bool
	dirs       map[string]map[string]vfs.ChildEntry
	dirReals   map[string][]string
	files      map[string]farmFile
	stats      map[string]statResult
	unreadable map[string]bool
}

type statResult struct {
	info os.FileInfo
	err  error
}

type viewNode struct {
	name   string
	dir    bool
	merged bool
	vpath  string
	real   string
	layer  int
	mode   fs.FileMode
	chain  []string
}

type viewDirFile struct {
	info    viewInfo
	entries []fs.DirEntry
	offset  int
}

type viewInfo struct {
	name string
	mode fs.FileMode
	size int64
}

type viewEntry struct {
	info viewInfo
}

// newFarmView merges layers with the hardlink farm's rules: casefold names, the first-seen entry type and spelling, and the last layer's file, resolving farm-placed relative symlinks as if deployed at farmRoot.
func newFarmView(gameID string, layers []projectionLayer, farmRoot string) *farmView {
	v := &farmView{
		layers:     layers,
		farmRoot:   farmRoot,
		dirs:       map[string]map[string]vfs.ChildEntry{"": {}},
		dirReals:   map[string][]string{},
		files:      map[string]farmFile{},
		stats:      map[string]statResult{},
		unreadable: map[string]bool{},
	}
	for i, layer := range layers {
		info, err := os.Stat(layer.root)
		if err != nil || !info.IsDir() {
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("skipping an unreadable layer in the SMAPI dependency report", "game", gameID, "root", layer.root, "err", err)
			}
			continue
		}
		var visits []farmVisit
		walk := vfs.Layer{Name: layer.walkName(), RootPath: layer.root, Enabled: true}
		err = vfs.WalkLayers([]vfs.Layer{walk}, func(vpath, realPath string, _ int, _ vfs.Layer, isDir bool) error {
			visits = append(visits, farmVisit{vpath: vpath, real: realPath, dir: isDir})
			return nil
		})
		if err != nil {
			slog.Warn("skipping an unreadable layer in the SMAPI dependency report", "game", gameID, "root", layer.root, "err", err)
			continue
		}
		v.dirReals[""] = append(v.dirReals[""], layer.root)
		for _, visit := range visits {
			v.apply(i, visit)
		}
	}
	return v
}

// walkName returns the layer name the farm walker uses, which decides whether mod-root internal names are skipped.
func (l projectionLayer) walkName() string {
	if !l.mod && l.provider == "" {
		return farmBaseLayerName
	}
	return l.provider
}

// apply records one walked entry of layer the way vfs.MergedTree.Build does.
func (v *farmView) apply(layer int, visit farmVisit) {
	parent, child := splitViewPath(visit.vpath)
	key := vfs.NormalizeName(child)
	if v.dirs[parent] == nil {
		v.dirs[parent] = map[string]vfs.ChildEntry{}
	}
	if _, exists := v.dirs[parent][key]; !exists {
		v.dirs[parent][key] = vfs.ChildEntry{Name: filepath.Base(visit.real), IsDir: visit.dir}
	}
	if visit.dir {
		if v.dirs[visit.vpath] == nil {
			v.dirs[visit.vpath] = map[string]vfs.ChildEntry{}
		}
		v.dirReals[visit.vpath] = append(v.dirReals[visit.vpath], visit.real)
		return
	}
	v.files[visit.vpath] = farmFile{real: visit.real, layer: layer}
}

// splitViewPath splits a normalized virtual path into its parent and last component.
func splitViewPath(vpath string) (string, string) {
	if i := strings.LastIndexByte(vpath, '/'); i >= 0 {
		return vpath[:i], vpath[i+1:]
	}
	return "", vpath
}

// stat follows symlinks for path, caching the answer for the view's lifetime.
func (v *farmView) stat(path string) (os.FileInfo, error) {
	if cached, ok := v.stats[path]; ok {
		return cached.info, cached.err
	}
	info, err := os.Stat(path)
	v.stats[path] = statResult{info: info, err: err}
	return info, err
}

// loops reports whether a directory resolves to one of the real directories on the chain.
func (v *farmView) loops(target os.FileInfo, chain []string) bool {
	for _, ancestor := range chain {
		if info, err := v.stat(ancestor); err == nil && os.SameFile(target, info) {
			return true
		}
	}
	return false
}

// leaf resolves a real entry reached from chain, following symlinks, dropping loops and keeping broken links as irregular files.
func (v *farmView) leaf(name, real string, layer int, chain []string) (viewNode, error) {
	node := viewNode{name: name, real: real, layer: layer}
	info, err := v.stat(real)
	switch {
	case err != nil:
		node.mode = fs.ModeIrregular
	case info.IsDir():
		if v.loops(info, chain) {
			return viewNode{}, errViewLoop
		}
		node.dir = true
		node.mode = fs.ModeDir
		node.chain = appendChain(chain, real)
	default:
		node.mode = info.Mode().Type()
	}
	return node, nil
}

// appendChain returns chain extended by more without aliasing the caller's backing array.
func appendChain(chain []string, more ...string) []string {
	out := make([]string, 0, len(chain)+len(more))
	return append(append(out, chain...), more...)
}

// root returns the view's merged root directory.
func (v *farmView) root() viewNode {
	return viewNode{name: ".", dir: true, merged: true, layer: -1, mode: fs.ModeDir, chain: v.dirReals[""]}
}

// child resolves one component below a directory node, case-insensitively inside the merged tree and exactly below a followed symlink, having followed depth symlinks so far.
func (v *farmView) child(parent viewNode, part string, depth int) (viewNode, error) {
	if !parent.merged {
		return v.leaf(part, filepath.Join(parent.real, part), parent.layer, parent.chain)
	}
	key := vfs.NormalizeName(part)
	entry, ok := v.dirs[parent.vpath][key]
	if !ok {
		return viewNode{}, fs.ErrNotExist
	}
	vpath := key
	if parent.vpath != "" {
		vpath = parent.vpath + "/" + key
	}
	if entry.IsDir {
		return viewNode{name: entry.Name, dir: true, merged: true, vpath: vpath, layer: -1, mode: fs.ModeDir, chain: appendChain(parent.chain, v.dirReals[vpath]...)}, nil
	}
	file, ok := v.files[vpath]
	if !ok {
		return viewNode{}, fs.ErrNotExist
	}
	return v.farmLeaf(parent, entry.Name, file, depth)
}

// farmLeaf resolves a layer file placed in a merged directory the way the deployed farm does: a relative symlink the materializer hardlinks into the farm resolves from its farm-side parent through the merged view, also after leaving and re-entering farmRoot, or otherwise below farmRoot's parent; any other file resolves from its real path.
func (v *farmView) farmLeaf(parent viewNode, name string, file farmFile, depth int) (viewNode, error) {
	target, relative := v.farmRelativeLink(file.real)
	if !relative {
		return v.leaf(name, file.real, file.layer, parent.chain)
	}
	broken := viewNode{name: name, real: file.real, layer: file.layer, mode: fs.ModeIrregular}
	joined := path.Join(parent.vpath, target)
	if escapesView(joined) {
		if v.farmRoot == "" {
			return broken, nil
		}
		outside := filepath.Join(v.farmRoot, filepath.FromSlash(joined))
		rel, err := filepath.Rel(v.farmRoot, outside)
		if err != nil || escapesView(filepath.ToSlash(rel)) {
			return v.leaf(name, outside, file.layer, parent.chain)
		}
		joined = filepath.ToSlash(rel)
	}
	if depth >= maxViewSymlinkHops {
		return broken, nil
	}
	resolved, err := v.resolveDepth(joined, depth+1)
	if err != nil {
		return broken, nil
	}
	if resolved.dir && resolved.merged && viewAncestor(resolved.vpath, parent.vpath) {
		return viewNode{}, errViewLoop
	}
	resolved.name = name
	if !resolved.dir {
		resolved.layer = file.layer
	}
	return resolved, nil
}

// farmRelativeLink returns the relative target of a symlink the materializer hardlinks into the farm, reporting false for other files, absolute links, and links it replaces by an absolute link to their store path because they or their target live on another device than the farm.
func (v *farmView) farmRelativeLink(real string) (string, bool) {
	info, err := os.Lstat(real)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(real)
	if err != nil || target == "" || filepath.IsAbs(target) {
		return "", false
	}
	if dev, ok := v.farmDevice(); ok {
		if fileDevice(info) != dev {
			return "", false
		}
		if targetInfo, err := v.stat(real); err == nil && fileDevice(targetInfo) != dev {
			return "", false
		}
	}
	return filepath.ToSlash(target), true
}

// farmDevice returns the device the farm is materialized on, from farmRoot or its parent, and false when it is unknown.
func (v *farmView) farmDevice() (uint64, bool) {
	if v.farmDevSet {
		return v.farmDev, v.farmDevOK
	}
	v.farmDevSet = true
	if v.farmRoot == "" {
		return 0, false
	}
	for _, candidate := range []string{v.farmRoot, filepath.Dir(v.farmRoot)} {
		if info, err := os.Stat(candidate); err == nil {
			v.farmDev, v.farmDevOK = fileDevice(info), true
			break
		}
	}
	return v.farmDev, v.farmDevOK
}

// fileDevice returns the device number recorded in info.
func fileDevice(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

// escapesView reports whether a cleaned slash-separated path climbs above its starting folder.
func escapesView(cleaned string) bool {
	return cleaned == ".." || strings.HasPrefix(cleaned, "../")
}

// viewAncestor reports whether the normalized view path ancestor equals vpath or contains it.
func viewAncestor(ancestor, vpath string) bool {
	return ancestor == "" || ancestor == vpath || strings.HasPrefix(vpath, ancestor+"/")
}

// validViewPath reports whether name is "." or a slash-separated relative path without empty, "." or ".." elements, allowing any bytes a Linux file name may hold.
func validViewPath(name string) bool {
	if name == "." {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// resolve walks a slash-separated view path from the root.
func (v *farmView) resolve(name string) (viewNode, error) {
	return v.resolveDepth(name, 0)
}

// resolveDepth walks a slash-separated view path from the root, having followed depth symlinks so far.
func (v *farmView) resolveDepth(name string, depth int) (viewNode, error) {
	if !validViewPath(name) {
		return viewNode{}, fs.ErrInvalid
	}
	node := v.root()
	if name == "." {
		return node, nil
	}
	for _, part := range strings.Split(name, "/") {
		if !node.dir {
			return viewNode{}, fs.ErrNotExist
		}
		next, err := v.child(node, part, depth)
		if err != nil {
			if errors.Is(err, errViewLoop) {
				return viewNode{}, fs.ErrNotExist
			}
			return viewNode{}, err
		}
		node = next
	}
	return node, nil
}

// entries lists a resolved directory in name order, following symlinked entries and dropping loops.
func (v *farmView) entries(dir viewNode) ([]fs.DirEntry, error) {
	var nodes []viewNode
	if dir.merged {
		for key := range v.dirs[dir.vpath] {
			node, err := v.child(dir, key, 0)
			if err != nil {
				continue
			}
			nodes = append(nodes, node)
		}
	} else {
		listed, err := os.ReadDir(dir.real)
		if err != nil {
			if !v.unreadable[dir.real] {
				v.unreadable[dir.real] = true
				slog.Warn("treating an unreadable symlinked folder as empty in the SMAPI dependency report", "path", dir.real, "err", err)
			}
			listed = nil
		}
		for _, entry := range listed {
			node, err := v.leaf(entry.Name(), filepath.Join(dir.real, entry.Name()), dir.layer, dir.chain)
			if err != nil {
				continue
			}
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })
	result := make([]fs.DirEntry, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, viewEntry{info: node.info(v)})
	}
	return result, nil
}

// info describes a resolved node.
func (n viewNode) info(v *farmView) viewInfo {
	info := viewInfo{name: n.name, mode: n.mode}
	if !n.dir && n.mode.IsRegular() {
		if stat, err := v.stat(n.real); err == nil {
			info.size = stat.Size()
		}
	}
	return info
}

// Open opens a view file for reading or a view directory for listing.
func (v *farmView) Open(name string) (fs.File, error) {
	node, err := v.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if node.dir {
		entries, err := v.entries(node)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &viewDirFile{info: node.info(v), entries: entries}, nil
	}
	if !node.mode.IsRegular() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.Open(node.real)
}

// ReadDir lists a view directory in name order.
func (v *farmView) ReadDir(name string) ([]fs.DirEntry, error) {
	node, err := v.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if !node.dir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	entries, err := v.entries(node)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return entries, nil
}

// Stat describes a view path.
func (v *farmView) Stat(name string) (fs.FileInfo, error) {
	node, err := v.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return node.info(v), nil
}

// DirIdentity returns the folder a view path resolves to: its merged view path, or the device and inode of a real folder reached through a symlink.
func (v *farmView) DirIdentity(name string) (string, bool) {
	node, err := v.resolve(name)
	if err != nil || !node.dir {
		return "", false
	}
	if node.merged {
		return "view:" + node.vpath, true
	}
	info, err := v.stat(node.real)
	if err != nil {
		return "", false
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("inode:%d:%d", st.Dev, st.Ino), true
	}
	return "real:" + filepath.Clean(node.real), true
}

// layerOf returns the index of the layer supplying a view file, or -1 when it is not a layer file.
func (v *farmView) layerOf(name string) int {
	node, err := v.resolve(name)
	if err != nil || node.dir {
		return -1
	}
	return node.layer
}

// Stat describes the listed directory.
func (d *viewDirFile) Stat() (fs.FileInfo, error) {
	return d.info, nil
}

// Read refuses to read a directory.
func (d *viewDirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: errors.New("is a directory")}
}

// Close releases nothing.
func (d *viewDirFile) Close() error {
	return nil
}

// ReadDir returns the next n entries of the directory, or all remaining ones when n <= 0.
func (d *viewDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return remaining, nil
	}
	if len(remaining) == 0 {
		return nil, io.EOF
	}
	if n > len(remaining) {
		n = len(remaining)
	}
	d.offset += n
	return remaining[:n], nil
}

// Name returns the entry's base name.
func (i viewInfo) Name() string { return i.name }

// Size returns the resolved file size.
func (i viewInfo) Size() int64 { return i.size }

// Mode returns the resolved type bits.
func (i viewInfo) Mode() fs.FileMode { return i.mode }

// ModTime returns the zero time because the view does not track modification times.
func (i viewInfo) ModTime() time.Time { return time.Time{} }

// IsDir reports whether the entry resolves to a directory.
func (i viewInfo) IsDir() bool { return i.mode.IsDir() }

// Sys returns nil.
func (i viewInfo) Sys() any { return nil }

// Name returns the entry's base name.
func (e viewEntry) Name() string { return e.info.name }

// IsDir reports whether the entry resolves to a directory.
func (e viewEntry) IsDir() bool { return e.info.IsDir() }

// Type returns the resolved type bits.
func (e viewEntry) Type() fs.FileMode { return e.info.mode.Type() }

// Info describes the entry.
func (e viewEntry) Info() (fs.FileInfo, error) { return e.info, nil }
