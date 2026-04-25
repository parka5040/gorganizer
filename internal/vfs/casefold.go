package vfs

import "strings"

// NormalizePath lowercases a virtual path for use as map keys.
// All MergedTree map keys must go through this function.
func NormalizePath(p string) string {
	return strings.ToLower(CleanVPath(p))
}

// NormalizeName lowercases a single path component (file or directory name).
func NormalizeName(name string) string {
	return strings.ToLower(name)
}

// JoinVPath joins a parent virtual path with a child name and normalizes the
// result. Avoids the overhead of filepath.Join + ToLower for the hot path.
func JoinVPath(parent, child string) string {
	if parent == "" {
		return strings.ToLower(child)
	}
	return strings.ToLower(parent + "/" + child)
}

// CleanVPath normalizes separators to forward slashes and removes trailing
// slashes. Returns "" for the root path. Backslashes are converted because
// Wine/Proton paths may contain them.
func CleanVPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	return p
}
