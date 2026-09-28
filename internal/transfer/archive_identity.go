package transfer

import (
	"fmt"
	"os"
	"syscall"
)

// ArchiveIdentity returns the opaque filesystem identity of an opened archive.
func ArchiveIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("checking archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("archive is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("archive has no filesystem identity")
	}
	return fmt.Sprintf("v1:%d:%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano(), stat.Ctim.Sec*1e9+stat.Ctim.Nsec), nil
}
