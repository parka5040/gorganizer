package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/migrate"
	"golang.org/x/sys/unix"
)

type migrateDeps struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
	isTTY  bool
}

// runMigrateData dispatches the offline migration command with terminal input.
func runMigrateData(args []string) int {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return runMigrateDataWith(args, migrateDeps{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, isTTY: err == nil})
}

// runMigrateDataWith plans or resumes a migration while holding the daemon instance lock.
func runMigrateDataWith(args []string, deps migrateDeps) int {
	fs := flag.NewFlagSet("migrate-data", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	from := fs.String("from", "", "old source checkout")
	dryRun := fs.Bool("dry-run", false, "show changes without moving")
	yes := fs.Bool("yes", false, "confirm move without a prompt")
	resume := fs.Bool("resume", false, "finish an interrupted move")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 || *resume && (*from != "" || *dryRun || *yes) || !*resume && *from == "" {
		fmt.Fprintln(deps.errOut, "Use --from with an old folder, or use --resume to finish an interrupted move.")
		return 2
	}
	release, err := instancelock.Acquire()
	if errors.Is(err, instancelock.ErrHeld) {
		fmt.Fprintln(deps.errOut, "Close Gorganizer first, then try again.")
		return 2
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot start the move: %v\n", err)
		return 1
	}
	defer release()
	if *resume {
		waiting, err := migrate.JournalExists()
		if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot check the move: %v\n", err)
			return 1
		}
		if !waiting {
			fmt.Fprintln(deps.out, "No unfinished move was found.")
			return 0
		}
		if err := migrate.ResumeWithOptions(migrate.ExecuteOptions{CopyProgress: printCopyProgress(deps.out, time.Now)}); err != nil {
			fmt.Fprintf(deps.errOut, "Could not finish moving your mods: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.out, "Your mods and downloads are in your personal data folder.")
		return 0
	}
	absolute, err := filepath.Abs(*from)
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot find the old folder: %v\n", err)
		return 1
	}
	plan, err := migrate.Plan(filepath.Clean(absolute))
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot plan the move: %v\n", err)
		return 1
	}
	printMigrationPlan(deps.out, plan)
	if plan.HasBlockers() {
		return 2
	}
	if *dryRun || len(plan.Items) == 0 {
		return 0
	}
	if !*yes {
		if !deps.isTTY {
			fmt.Fprintln(deps.errOut, "Run again with --yes to confirm the move.")
			return 2
		}
		fmt.Fprint(deps.out, "Move your mods to your personal data folder? Downloads and Overwrite will move too. [y/N] ")
		line, err := bufio.NewReader(deps.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(deps.errOut, "Cannot read your answer: %v\n", err)
			return 1
		}
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			fmt.Fprintln(deps.out, "Nothing was moved.")
			return 2
		}
	}
	if err := migrate.Execute(plan, migrate.ExecuteOptions{CopyProgress: printCopyProgress(deps.out, time.Now)}); err != nil {
		fmt.Fprintf(deps.errOut, "Could not move your mods: %v\n", err)
		return 1
	}
	fmt.Fprintln(deps.out, "Your mods and downloads are in your personal data folder.")
	return 0
}

// printCopyProgress prints copied bytes at most once per second.
func printCopyProgress(out io.Writer, now func() time.Time) func(int64, int64) {
	var last time.Time
	return func(copied, total int64) {
		current := now()
		if !last.IsZero() && current.Sub(last) < time.Second {
			return
		}
		last = current
		fmt.Fprintf(out, "Copied %.1f GB of %.1f GB…\n", float64(copied)/1e9, float64(total)/1e9)
	}
}

// readableSize formats a byte count with decimal units for people.
func readableSize(n uint64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// printMigrationPlan describes folders, available space and problems in plain words.
func printMigrationPlan(out io.Writer, plan *migrate.MigrationPlan) {
	if len(plan.Items) == 0 {
		fmt.Fprintln(out, "No old mod folders were found. Nothing needs to move.")
		return
	}
	for _, item := range plan.Items {
		fmt.Fprintf(out, "%s: %s → %s\n", item.Name, item.Source, item.Destination)
		fmt.Fprintf(out, "  %d files, %s; %s free in the new location", item.Files, readableSize(uint64(item.Bytes)), readableSize(item.FreeBytes))
		if item.Mode == "copy" {
			fmt.Fprintln(out, "; will copy and check every file before removing the old folder.")
		} else {
			fmt.Fprintln(out, "; will move the folder on the same drive.")
		}
		for _, link := range item.OutsideLinks {
			fmt.Fprintf(out, "  Link kept as-is: %s\n", link)
		}
		for _, ref := range item.References {
			fmt.Fprintf(out, "  Game setting to update: %s\n", ref)
		}
		for _, blocker := range item.Blockers {
			fmt.Fprintf(out, "  Cannot move yet: %s\n", blocker)
		}
	}
}
