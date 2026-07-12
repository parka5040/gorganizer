package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/parka/gorganizer/internal/tools"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	home, _ := os.UserHomeDir()
	steamRoot := filepath.Join(home, ".local/share/Steam")
	if _, err := os.Stat(filepath.Join(steamRoot, "steamapps")); err != nil {
		fmt.Fprintf(os.Stderr, "no steamapps under %s\n", steamRoot)
		os.Exit(1)
	}

	common := filepath.Join(steamRoot, "steamapps", "common")
	entries, err := os.ReadDir(common)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", common, err)
		os.Exit(1)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 6 || name[:6] != "Proton" {
			continue
		}
		protonPath := filepath.Join(common, name, "proton")
		if _, err := os.Stat(protonPath); err != nil {
			continue
		}
		entry, runtimeName := tools.ResolveProtonRuntime(protonPath, steamRoot)
		if entry == "" {
			fmt.Printf("%-40s  -> direct (no runtime found)\n", name)
		} else {
			fmt.Printf("%-40s  -> %s/_v2-entry-point\n", name, runtimeName)
			if _, err := exec.LookPath(entry); err != nil {
				if info, statErr := os.Stat(entry); statErr == nil && info.Mode()&0111 == 0 {
					fmt.Printf("    WARNING: %s is not executable\n", entry)
				}
			}
		}
	}
}
