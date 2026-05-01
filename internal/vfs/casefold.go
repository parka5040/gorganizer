package vfs

import "strings"

// NormalizePath lowercases a virtual path for use as map keys.
func NormalizePath(p string) string {
	return strings.ToLower(CleanVPath(p))
}

// NormalizeName lowercases a single path component.
func NormalizeName(name string) string {
	return strings.ToLower(name)
}

// JoinVPath joins parent and child paths and normalizes the result.
func JoinVPath(parent, child string) string {
	if parent == "" {
		return strings.ToLower(child)
	}
	return strings.ToLower(parent + "/" + child)
}

// CleanVPath normalizes separators to forward slashes and trims slashes.
func CleanVPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	return p
}
