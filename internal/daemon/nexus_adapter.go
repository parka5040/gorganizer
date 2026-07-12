package daemon

import (
	"context"
	"os"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/plugins"
)

type nexusV3Adapter struct {
	client *download.NexusClient
}

func (a *nexusV3Adapter) ResolveGlobalFileID(ctx context.Context, gameDomain, gameScopedID string) (string, error) {
	mf, err := a.client.GetModFile(ctx, gameDomain, gameScopedID)
	if err != nil {
		return "", err
	}
	return mf.ID, nil
}

func (a *nexusV3Adapter) FetchDependencyRanges(ctx context.Context, globalFileID string) (plugins.V3DepRangesFields, error) {
	r, err := a.client.GetModFileDependencyRanges(ctx, globalFileID)
	if err != nil {
		return plugins.V3DepRangesFields{}, err
	}
	out := plugins.V3DepRangesFields{}
	for _, def := range r.DependencyDefinitions {
		converted := plugins.V3DepDefinitionFields{}
		for _, rng := range def.Ranges {
			converted.Ranges = append(converted.Ranges, plugins.V3DepRangeFields{
				TargetModID:   rng.TargetGroup.Mod.ID,
				TargetModName: rng.TargetGroup.Mod.Name,
				TargetModSlug: rng.TargetGroup.Mod.GameScopedID,
			})
		}
		out.Definitions = append(out.Definitions, converted)
	}
	return out, nil
}

func (a *nexusV3Adapter) RateLimitRemaining() (int, int) {
	return a.client.RateLimitRemaining()
}

// readSubdirs returns the names of immediate subdirectories under root.
func readSubdirs(root string) ([]string, error) {
	d, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.Readdir(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || name[0] == '.' || name == "Downloads" {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}
