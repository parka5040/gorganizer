package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
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

// runMigrateDataWith checks, plans, or resumes a move into the personal data folder.
func runMigrateDataWith(args []string, deps migrateDeps) int {
	fs := flag.NewFlagSet("migrate-data", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	from := fs.String("from", "", "old source checkout")
	dryRun := fs.Bool("dry-run", false, "show changes without moving")
	jsonOutput := fs.Bool("json", false, "print the dry-run plan as JSON")
	countOutput := fs.Bool("count", false, "print how many old folders the dry run found")
	listOutput := fs.Bool("list", false, "print validated old folders, one per line")
	yes := fs.Bool("yes", false, "confirm move without a prompt")
	resume := fs.Bool("resume", false, "finish an interrupted move")
	status := fs.Bool("status", false, "check for an unfinished move")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *listOutput && (*countOutput || *jsonOutput) {
		fmt.Fprintln(deps.errOut, "Choose --list without --count or --json.")
		return 2
	}
	if *countOutput || *listOutput {
		*jsonOutput = true
	}
	if len(fs.Args()) != 0 || *jsonOutput && !*dryRun || *resume && (*status || *from != "" || *dryRun || *yes || *jsonOutput) || *status && (*from != "" || *dryRun || *yes || *jsonOutput) || !*resume && !*status && *from == "" {
		fmt.Fprintln(deps.errOut, "Use --from with an old folder, --status to check for a move, or --resume to finish one.")
		return 2
	}
	if *status {
		waiting, err := migrate.JournalExists()
		if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot check the move: %v\n", err)
			return 1
		}
		if waiting {
			fmt.Fprintln(deps.out, "pending")
		} else {
			fmt.Fprintln(deps.out, "none")
		}
		return 0
	}
	if *dryRun && *jsonOutput {
		absolute, err := filepath.Abs(*from)
		if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot find the old folder: %v\n", err)
			return 1
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot find the old folder: %v\n", err)
			return 1
		}
		if !info.IsDir() {
			fmt.Fprintln(deps.errOut, "The old folder must be a real directory.")
			return 1
		}
		sources := make([]string, 0)
		for _, name := range config.AllModsDirNames() {
			if name == "" {
				continue
			}
			source := filepath.Join(absolute, name)
			if _, err := os.Lstat(source); err == nil {
				sources = append(sources, source)
			} else if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(deps.errOut, "Cannot check the old folders: %v\n", err)
				return 1
			}
		}
		if len(sources) == 0 {
			waiting, err := migrate.JournalExists()
			if err != nil {
				fmt.Fprintf(deps.errOut, "Cannot check the move: %v\n", err)
				return 1
			}
			if waiting {
				fmt.Fprintln(deps.errOut, "An unfinished move needs to be resumed first.")
				return 2
			}
			if err := printMigrationSources(deps.out, sources, *countOutput, *listOutput); err != nil {
				fmt.Fprintf(deps.errOut, "Cannot show the move: %v\n", err)
				return 1
			}
			return 0
		}
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
	if *jsonOutput {
		sources := make([]string, 0, len(plan.Items))
		for _, item := range plan.Items {
			sources = append(sources, item.Source)
		}
		if err := printMigrationSources(deps.out, sources, *countOutput, *listOutput); err != nil {
			fmt.Fprintf(deps.errOut, "Cannot show the move: %v\n", err)
			return 1
		}
	} else {
		printMigrationPlan(deps.out, plan)
	}
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

// printMigrationSources prints detected old folders as JSON, a count, or validated paths.
func printMigrationSources(out io.Writer, sources []string, count, list bool) error {
	if count {
		_, err := fmt.Fprintln(out, len(sources))
		return err
	}
	if list {
		for _, source := range sources {
			if !filepath.IsAbs(source) || filepath.Clean(source) != source || strings.ContainsAny(source, "\n\r\x00") {
				return fmt.Errorf("invalid old folder path: %q", source)
			}
			info, err := os.Lstat(source)
			if err != nil {
				return fmt.Errorf("checking old folder %s: %w", source, err)
			}
			owner, ok := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || !ok || int(owner.Uid) != os.Getuid() {
				return fmt.Errorf("old folder is not a real owned directory: %s", source)
			}
		}
		for _, source := range sources {
			if _, err := fmt.Fprintln(out, source); err != nil {
				return err
			}
		}
		return nil
	}
	return json.NewEncoder(out).Encode(struct {
		Sources []string `json:"sources"`
	}{Sources: sources})
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
