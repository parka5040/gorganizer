package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/release"
)

// repoRoot locates the checkout from the caller's working directory.
func repoRoot() string {
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			return ""
		}
		root = parent
	}
}

// copyFixtureFile copies a regular source file into the fixture bundle.
func copyFixtureFile(from, to string, mode os.FileMode) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("fixture input is not a regular file: %s", from)
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// writeManifest lists every regular bundle file other than the manifest itself.
func writeManifest(bundle string) error {
	var lines strings.Builder
	err := filepath.WalkDir(bundle, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || name == filepath.Join(bundle, "MANIFEST.sha256") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported fixture entry: %s", name)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		rel, err := filepath.Rel(bundle, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(&lines, "%x  %s\n", digest, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundle, "MANIFEST.sha256"), []byte(lines.String()), 0o644)
}

// archiveBundle writes the release tree with parents preceding their children.
func archiveBundle(bundle, archive string) (err error) {
	out, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	writer := tar.NewWriter(gz)
	err = filepath.WalkDir(bundle, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported fixture entry: %s", name)
		}
		rel, err := filepath.Rel(filepath.Dir(bundle), name)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		fd, err := os.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, fd)
		if closeErr := fd.Close(); err == nil {
			err = closeErr
		}
		return err
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	return err
}

// signFixtureSums signs one canonical statement with test-only key K1.
func signFixtureSums(tag string, sums []byte) []byte {
	seed := sha256.Sum256([]byte("gorganizer test-only release signing key 1"))
	key := ed25519.NewKeyFromSeed(seed[:])
	id := sha256.Sum256(key.Public().(ed25519.PublicKey))
	return []byte(fmt.Sprintf("gorganizer-sig-v1 %s %s\n", hex.EncodeToString(id[:8]), base64.StdEncoding.EncodeToString(ed25519.Sign(key, release.SignedStatement(tag, sums)))))
}

// makeBundle assembles and signs a local test release without writing a private key.
func makeBundle(version, bin, gui, out string) error {
	if err := release.ValidateTag("v" + version); err != nil {
		return err
	}
	bundle := filepath.Join(out, "gorganizer-"+version)
	if err := os.MkdirAll(filepath.Join(bundle, "bin"), 0o755); err != nil {
		return err
	}
	root := repoRoot()
	for _, file := range []struct {
		from, to string
		mode     os.FileMode
	}{
		{filepath.Join(bin, "gorganizerctl"), "bin/gorganizerctl", 0o755},
		{filepath.Join(bin, "gorganizerd"), "bin/gorganizerd", 0o755},
		{gui, "bin/gorganizer-gui", 0o755},
		{filepath.Join(root, "gorganizer.sh"), "gorganizer.sh", 0o755},
		{filepath.Join(root, "cleaner.sh"), "cleaner.sh", 0o755},
		{filepath.Join(root, "resources/icons/tmp_logo.png"), "resources/icons/tmp_logo.png", 0o644},
	} {
		if err := copyFixtureFile(file.from, filepath.Join(bundle, file.to), file.mode); err != nil {
			return fmt.Errorf("copying %s: %w", file.to, err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "release.json"), []byte(fmt.Sprintf("{\"version\":%q}\n", version)), 0o644); err != nil {
		return err
	}
	if err := writeManifest(bundle); err != nil {
		return err
	}
	asset := "gorganizer-" + version + "-linux-x86_64.tar.gz"
	archive := filepath.Join(out, asset)
	if err := archiveBundle(bundle, archive); err != nil {
		return err
	}
	fd, err := os.Open(archive)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	_, err = io.Copy(hasher, fd)
	if closeErr := fd.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	sums := []byte(fmt.Sprintf("%x  %s\n", hasher.Sum(nil), asset))
	if err := os.WriteFile(filepath.Join(out, "SHA256SUMS"), sums, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS.sig"), signFixtureSums("v"+version, sums), 0o644)
}
