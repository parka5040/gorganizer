package smapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// GameVersionFromDeps reads a package version such as the game build from a .NET deps.json document.
func GameVersionFromDeps(data []byte, packageName string) (Version, error) {
	data, err := Standardize(data)
	if err != nil {
		return Version{}, fmt.Errorf("parsing deps JSON: %w", err)
	}
	var document struct {
		Targets   map[string]json.RawMessage `json:"targets"`
		Libraries map[string]json.RawMessage `json:"libraries"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return Version{}, fmt.Errorf("parsing deps JSON: %w", err)
	}
	var candidates []string
	for _, targetName := range sortedKeys(document.Targets) {
		var target map[string]json.RawMessage
		if json.Unmarshal(document.Targets[targetName], &target) != nil {
			continue
		}
		candidates = append(candidates, sortedKeys(target)...)
	}
	candidates = append(candidates, sortedKeys(document.Libraries)...)
	for _, key := range candidates {
		name, version, ok := strings.Cut(key, "/")
		if !ok || !strings.EqualFold(name, packageName) {
			continue
		}
		parsed, err := ParseVersion(version, true)
		if err != nil {
			return Version{}, fmt.Errorf("parsing %s version from deps JSON: %w", packageName, err)
		}
		return parsed, nil
	}
	return Version{}, fmt.Errorf("%w: package %q not found in deps JSON", ErrInvalidVersion, packageName)
}

// sortedKeys returns the keys of a JSON object in byte order.
func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
