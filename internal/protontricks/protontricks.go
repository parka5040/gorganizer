package protontricks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const flatpakApp = "com.github.Matoking.protontricks"

type Kind string

const (
	Native  Kind = "native"
	Flatpak Kind = "flatpak"
)

type Options struct {
	LookPath func(string) (string, error)
	RunInfo  func(context.Context, string, ...string) error
}

type Invocation struct {
	kind Kind
	path string
}

// Resolve selects an installed native or Flatpak Protontricks executable.
func Resolve(ctx context.Context, opts Options) (Invocation, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if path, err := lookPath("protontricks"); err == nil {
		return Invocation{kind: Native, path: path}, nil
	}
	flatpak, err := lookPath("flatpak")
	if err != nil {
		return Invocation{}, &NotInstalledError{}
	}
	runInfo := opts.RunInfo
	if runInfo == nil {
		runInfo = func(ctx context.Context, path string, args ...string) error {
			return exec.CommandContext(ctx, path, args...).Run()
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if runInfo(checkCtx, flatpak, "info", flatpakApp) != nil || checkCtx.Err() != nil {
		return Invocation{}, &NotInstalledError{}
	}
	return Invocation{kind: Flatpak, path: flatpak}, nil
}

// Kind returns the installation type of the selected Protontricks executable.
func (i Invocation) Kind() Kind {
	return i.kind
}

// Command builds a Protontricks command for a Proton prefix and Steam libraries.
func (i Invocation) Command(ctx context.Context, appID int, compatDataPath string, libraryRoots []string, packages []string) *exec.Cmd {
	args := []string{}
	if i.kind == Flatpak {
		args = append(args, "run", "--env=STEAM_COMPAT_DATA_PATH="+compatDataPath)
		seen := make(map[string]bool)
		for _, root := range append(append([]string(nil), libraryRoots...), compatDataPath) {
			if root == "" {
				continue
			}
			root = filepath.Clean(root)
			if !seen[root] {
				args = append(args, "--filesystem="+root)
				seen[root] = true
			}
		}
		args = append(args, flatpakApp)
	} else {
		args = append(args, "--no-bwrap")
	}
	args = append(args, strconv.Itoa(appID), "-q")
	args = append(args, packages...)
	cmd := exec.CommandContext(ctx, i.path, args...)
	if i.kind == Native {
		cmd.Env = append(os.Environ(), "STEAM_COMPAT_DATA_PATH="+compatDataPath)
	}
	return cmd
}
