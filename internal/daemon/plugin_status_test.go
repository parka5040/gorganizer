package daemon

import (
	"context"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

func TestStreamPluginStatusWithoutPluginSpec(t *testing.T) {
	pl := &PluginStatusService{s: &session{
		config: &config.Config{Games: map[string]config.GameConfig{
			"stardewvalley": {InstallPath: t.TempDir(), DataSubpath: "Mods"},
		}},
	}}
	out, err := pl.StreamPluginStatus(context.Background(), "stardewvalley", "Default")
	if err != nil {
		t.Fatal(err)
	}
	event, ok := <-out
	if !ok {
		t.Fatal("stream closed before its snapshot")
	}
	if event.Snapshot == nil || event.Update != nil || len(event.Snapshot) != 0 {
		t.Fatalf("event = %+v, want one empty snapshot", event)
	}
	if _, ok := <-out; ok {
		t.Fatal("stream produced more than one event")
	}
}
