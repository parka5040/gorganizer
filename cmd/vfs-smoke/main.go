// vfs-smoke is an integration test that activates and deactivates the overlay and asserts byte-identity round-trip.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parka/gorganizer/internal/vfs"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: vfs-smoke /path/to/Game/Data")
		os.Exit(2)
	}
	dataPath := os.Args[1]

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	pre, err := hashTree(dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash pre-activate: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "pre-activate fingerprint: %s\n", pre)

	mm := vfs.NewMountManager(dataPath, "")
	layers := []vfs.Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	if err := mm.Activate(layers); err != nil {
		fmt.Fprintf(os.Stderr, "Activate failed: %v\n", err)
		os.Exit(1)
	}

	mid, err := hashTree(dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash mid-activate: %v\n", err)
		_ = mm.Deactivate()
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "mid-activate fingerprint (excluding sentinel): %s\n", mid)
	if mid != pre {
		fmt.Fprintf(os.Stderr, "MISMATCH: materialized view diverges from source\n")
		_ = mm.Deactivate()
		os.Exit(1)
	}

	if err := mm.Deactivate(); err != nil {
		fmt.Fprintf(os.Stderr, "Deactivate failed: %v\n", err)
		os.Exit(1)
	}

	post, err := hashTree(dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash post-deactivate: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "post-deactivate fingerprint: %s\n", post)
	if post != pre {
		fmt.Fprintf(os.Stderr, "MISMATCH: Data/ not byte-identical after round-trip\n")
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "smoke OK: pre == mid == post")
}

// hashTree returns a deterministic fingerprint of dataPath built from each file's path, size, and content hash.
func hashTree(dataPath string) (string, error) {
	type entry struct {
		rel    string
		size   int64
		sum256 string
	}
	var entries []entry

	walkErr := filepath.Walk(dataPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(p) == vfs.SentinelFilename {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataPath, p)
		entries = append(entries, entry{
			rel:    rel,
			size:   n,
			sum256: hex.EncodeToString(h.Sum(nil)),
		})
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%d\t%s\n", e.rel, e.size, e.sum256)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}
