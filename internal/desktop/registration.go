package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// Register writes the desktop entries, launcher and NXM association for a checkout.
func Register(paths Paths, checkout, icon string) error {
	if !filepath.IsAbs(checkout) || !filepath.IsAbs(icon) {
		return fmt.Errorf("the checkout and icon must be full paths")
	}
	script, err := os.Stat(filepath.Join(checkout, "gorganizer.sh"))
	if err != nil {
		return fmt.Errorf("finding Gorganizer in the checkout: %w", err)
	}
	if !script.Mode().IsRegular() {
		return fmt.Errorf("Gorganizer's launcher is not a file")
	}
	if err := WriteLauncher(paths.Launcher, checkout); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Application), 0o755); err != nil {
		return fmt.Errorf("creating applications folder: %w", err)
	}
	for _, entry := range []struct{ path, content string }{
		{paths.Application, RenderApplication(paths.Launcher, icon)},
		{paths.NXM, RenderNXM(paths.Launcher, icon)},
	} {
		if err := atomicfile.WriteFile(entry.path, []byte(entry.content), 0o644); err != nil {
			return fmt.Errorf("writing desktop entry: %w", err)
		}
	}
	return UpdateMimeapps(paths.Mimeapps, true)
}

// Unregister removes only desktop entries and a launcher identified with this checkout.
func Unregister(paths Paths, checkout string) error {
	if !filepath.IsAbs(checkout) {
		return fmt.Errorf("the checkout must be a full path")
	}
	nxmOwned, nxmForeign := false, false
	for _, entry := range []struct{ path, name string }{{paths.Application, "gorganizer.desktop"}, {paths.NXM, Handler}} {
		info, err := os.Lstat(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking desktop entry: %w", err)
		}
		if !info.Mode().IsRegular() {
			if entry.name == Handler {
				nxmForeign = true
			}
			continue
		}
		body, err := os.ReadFile(entry.path)
		if err != nil {
			return fmt.Errorf("reading desktop entry: %w", err)
		}
		if !OwnsEntry(entry.name, body, checkout, paths.Launcher) {
			if entry.name == Handler {
				nxmForeign = true
			}
			continue
		}
		if entry.name == Handler {
			nxmOwned = true
		}
		if err := atomicfile.RemoveDurable(entry.path); err != nil {
			return fmt.Errorf("removing desktop entry: %w", err)
		}
	}
	launcherOwned, err := LauncherBelongsTo(paths.Launcher, checkout)
	if err != nil {
		return err
	}
	if launcherOwned {
		if err := atomicfile.RemoveDurable(paths.Launcher); err != nil {
			return fmt.Errorf("removing launcher: %w", err)
		}
	}
	if !nxmForeign && (nxmOwned || launcherOwned) {
		return UpdateMimeapps(paths.Mimeapps, false)
	}
	return nil
}

// LauncherBelongsTo checks that an installed launcher records the given checkout.
func LauncherBelongsTo(path, checkout string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking launcher: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading launcher: %w", err)
	}
	return string(body) == RenderLauncher(checkout), nil
}

// Status reports whether both entries, launcher and MIME settings match this checkout.
func Status(paths Paths, checkout, icon string) (bool, error) {
	if !filepath.IsAbs(checkout) || !filepath.IsAbs(icon) {
		return false, fmt.Errorf("the checkout and icon must be full paths")
	}
	if info, err := os.Stat(filepath.Join(checkout, "gorganizer.sh")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("checking checkout: %w", err)
	} else if !info.Mode().IsRegular() {
		return false, nil
	}
	launcher, err := LauncherBelongsTo(paths.Launcher, checkout)
	if err != nil || !launcher {
		return false, err
	}
	launcherInfo, err := os.Stat(paths.Launcher)
	if err != nil {
		return false, fmt.Errorf("checking launcher: %w", err)
	}
	if !launcherInfo.Mode().IsRegular() || launcherInfo.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	for _, entry := range []struct{ path, content string }{
		{paths.Application, RenderApplication(paths.Launcher, icon)},
		{paths.NXM, RenderNXM(paths.Launcher, icon)},
	} {
		entryInfo, err := os.Lstat(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("checking desktop entry: %w", err)
		}
		if !entryInfo.Mode().IsRegular() {
			return false, nil
		}
		body, err := os.ReadFile(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("reading desktop entry: %w", err)
		}
		if string(body) != entry.content {
			return false, nil
		}
	}
	body, err := os.ReadFile(paths.Mimeapps)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading NXM settings: %w", err)
	}
	return HasAssociation(body), nil
}
