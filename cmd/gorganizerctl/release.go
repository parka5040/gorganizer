package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/desktop"
	"github.com/parka/gorganizer/internal/release"
)

type releaseDeps struct {
	manager *release.Manager
	out     io.Writer
	errOut  io.Writer
	running func() bool
}

// runRelease manages verified prebuilt releases.
func runRelease(args []string) int {
	return runReleaseWith(args, releaseDeps{
		manager: &release.Manager{RuntimeDir: config.RuntimeDir()}, out: os.Stdout, errOut: os.Stderr,
		running: func() bool { _, ok := sessionHealth(config.SocketPath()); return ok },
	})
}

// runReleaseWith executes release commands with injectable local dependencies.
func runReleaseWith(args []string, deps releaseDeps) int {
	if len(args) == 0 {
		fmt.Fprintln(deps.errOut, "Choose release install, update, rollback or status.")
		return 2
	}
	command := args[0]
	if command != "install" && command != "update" && command != "rollback" && command != "status" {
		fmt.Fprintln(deps.errOut, "Choose release install, update, rollback or status.")
		return 2
	}
	fs := flag.NewFlagSet("release "+command, flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	from := fs.String("from", "", "folder containing an extracted release")
	tag := fs.String("tag", "", "release tag")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || ((command == "rollback" || command == "status") && (*from != "" || *tag != "")) || (*from != "" && (*tag != "" || command != "install")) {
		fmt.Fprintln(deps.errOut, "Check the release command options and try again.")
		return 2
	}
	manager := deps.manager
	if manager == nil {
		fmt.Fprintln(deps.errOut, "Release settings are missing.")
		return 1
	}
	ctx := context.Background()
	var version string
	var err error
	switch command {
	case "status":
		var state release.Status
		state, err = manager.Status()
		if err == nil {
			show := func(s string) string {
				if s == "" {
					return "none"
				}
				return s
			}
			fmt.Fprintf(deps.out, "Current: %s\nPrevious: %s\nAvailable: %s\nIn use: %s\n", show(state.Current), show(state.Previous), show(strings.Join(state.Available, ", ")), show(state.InUse))
		}
	case "rollback":
		version, err = manager.Rollback(ctx)
	case "install":
		if *from != "" {
			version, err = manager.Adopt(ctx, *from)
		} else {
			if *tag != "" {
				var state release.Status
				state, err = manager.Status()
				if err == nil {
					var cmp int
					cmp, err = release.Compare(strings.TrimPrefix(*tag, "v"), state.Current)
					if state.Current == "" {
						err = nil
					}
					if err == nil && state.Current != "" && cmp < 0 {
						fmt.Fprintln(deps.errOut, "Warning: this will install an older version of Gorganizer.")
					}
				}
			}
			if err == nil {
				version, err = manager.Install(ctx, *tag)
			}
		}
	case "update":
		var changed bool
		version, changed, err = manager.Update(ctx, *tag)
		if err == nil && !changed {
			fmt.Fprintln(deps.out, "Gorganizer is already up to date.")
			return 0
		}
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "Could not %s Gorganizer: %v\n", command, err)
		return 1
	}
	if command == "status" {
		return 0
	}
	if err := registerRelease(manager); err != nil {
		fmt.Fprintf(deps.errOut, "Gorganizer %s is installed, but its application menu could not be updated: %v\n", version, err)
		return 1
	}
	if deps.running != nil && deps.running() {
		fmt.Fprintln(deps.out, "Update installed. It will be used next time you open Gorganizer.")
	} else {
		fmt.Fprintf(deps.out, "Gorganizer %s is installed.\n", version)
	}
	return 0
}

// registerRelease installs desktop entries bound to the unresolved current symlink.
func registerRelease(manager *release.Manager) error {
	root := manager.Root
	if root == "" {
		var err error
		root, err = release.DataRoot()
		if err != nil {
			return err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		return err
	}
	checkout := filepath.Join(root, "current")
	icon := filepath.Join(checkout, "resources", "icons", "tmp_logo.png")
	if err := os.MkdirAll(filepath.Dir(paths.Icon), 0o755); err != nil {
		return fmt.Errorf("creating icon folder: %w", err)
	}
	if _, err := atomicfile.CopyFileDurable(icon, paths.Icon, 0o644, true); err != nil {
		return fmt.Errorf("installing Gorganizer icon: %w", err)
	}
	return desktop.Register(paths, checkout, paths.Icon)
}
