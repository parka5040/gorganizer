package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/desktop"
)

// runDesktop handles desktop registration, removal and status checks.
func runDesktop(args []string) int {
	return runDesktopWith(args, os.Stdout, os.Stderr)
}

// runDesktopWith runs desktop registration against the current XDG directories.
func runDesktopWith(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "Choose desktop register, unregister or status.")
		return 2
	}
	command := args[0]
	if command != "register" && command != "unregister" && command != "status" {
		fmt.Fprintln(errOut, "Choose desktop register, unregister or status.")
		return 2
	}
	fs := flag.NewFlagSet("desktop "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	checkout := fs.String("checkout", "", "folder containing gorganizer.sh")
	icon := fs.String("icon", "", "installed icon path")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *checkout == "" {
		fmt.Fprintln(errOut, "Pass --checkout with the folder containing gorganizer.sh.")
		return 2
	}
	absolute, err := filepath.Abs(*checkout)
	if err != nil {
		fmt.Fprintf(errOut, "Could not find the checkout: %v\n", err)
		return 1
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		fmt.Fprintf(errOut, "Could not find your desktop settings: %v\n", err)
		return 1
	}
	if *icon == "" {
		*icon = paths.Icon
	}
	switch command {
	case "register":
		err = desktop.Register(paths, absolute, *icon)
	case "unregister":
		err = desktop.Unregister(paths, absolute)
	case "status":
		var registered bool
		registered, err = desktop.Status(paths, absolute, *icon)
		if err == nil {
			if registered {
				fmt.Fprintln(out, "Gorganizer's desktop shortcut and Nexus download links are ready.")
				return 0
			}
			fmt.Fprintln(out, "Gorganizer's desktop shortcut needs updating. Run ./gorganizer.sh register from this folder.")
			return 1
		}
	}
	if err != nil {
		fmt.Fprintf(errOut, "Could not update Gorganizer's desktop shortcut: %v\n", err)
		return 1
	}
	if command == "register" {
		fmt.Fprintln(out, "Gorganizer's desktop shortcut and Nexus download links are ready.")
	} else {
		fmt.Fprintln(out, "Gorganizer's desktop shortcut was removed.")
	}
	return 0
}
