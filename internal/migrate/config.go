package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
)

// configFilePath returns the active settings file path.
func configFilePath() string { return filepath.Join(config.ConfigDir(), "config.json") }

// configReferences lists absolute config strings inside the old folder and optionally rewrites them.
func configReferences(cfg *config.Config, source, dest string) ([]string, error) {
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("reading settings paths: %w", err)
	}
	var object any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	var refs []string
	var visit func(any, string) any
	visit = func(value any, location string) any {
		switch node := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(node))
			for key := range node {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				node[key] = visit(node[key], location+"."+key)
			}
		case []any:
			for i := range node {
				node[i] = visit(node[i], location+"."+strconv.Itoa(i))
			}
		case string:
			if filepath.IsAbs(node) && within(node, source) {
				refs = append(refs, location)
				if dest != "" {
					rel, _ := filepath.Rel(source, node)
					return filepath.Join(dest, rel)
				}
			}
		}
		return value
	}
	visit(object, "config")
	if dest != "" {
		body, err = json.Marshal(object)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, cfg); err != nil {
			return nil, fmt.Errorf("updating settings paths: %w", err)
		}
	}
	return refs, nil
}

// expectedConfig loads settings and calculates the final bytes and whether anything needs patching.
func expectedConfig(j *journal) (*config.Config, []byte, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, false, err
	}
	changed := false
	for _, item := range j.Items {
		refs, err := configReferences(cfg, item.Source, item.Destination)
		if err != nil {
			return nil, nil, false, err
		}
		changed = changed || len(refs) != 0
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	return cfg, body, changed, err
}

// settingsBytes reads the current settings without creating a missing file.
func settingsBytes() ([]byte, bool, error) {
	body, err := os.ReadFile(configFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading game settings: %w", err)
	}
	return body, true, nil
}

// patchConfig atomically rewrites planned paths after every folder is published.
func patchConfig(j *journal) error {
	current, exists, err := settingsBytes()
	if err != nil {
		return err
	}
	if exists != j.ConfigExists || !bytes.Equal(current, j.ConfigBefore) {
		if err := verifyPatchedConfig(j); err == nil {
			return atomicfile.SyncDir(config.ConfigDir())
		}
		return fmt.Errorf("game settings changed during the move; folders were kept and settings were not overwritten")
	}
	cfg, _, changed, err := expectedConfig(j)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("updating game settings: %w", err)
	}
	return atomicfile.SyncDir(config.ConfigDir())
}

// verifyPatchedConfig checks the committed settings or untouched preimage before finishing.
func verifyPatchedConfig(j *journal) error {
	current, exists, err := settingsBytes()
	if err != nil {
		return err
	}
	if !j.ConfigExists {
		if exists {
			return fmt.Errorf("settings appeared during the move; settings were not overwritten")
		}
		return nil
	}
	var cfg config.Config
	if err := json.Unmarshal(j.ConfigBefore, &cfg); err != nil {
		return fmt.Errorf("invalid settings preimage: %w", err)
	}
	changed := false
	for _, item := range j.Items {
		refs, err := configReferences(&cfg, item.Source, item.Destination)
		if err != nil {
			return err
		}
		changed = changed || len(refs) != 0
	}
	if !changed && bytes.Equal(current, j.ConfigBefore) {
		return nil
	}
	want, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(current, want) {
		return fmt.Errorf("game settings changed during the move; settings were not overwritten")
	}
	return nil
}
