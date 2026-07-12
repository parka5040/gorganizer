package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writePluginFile(t *testing.T, dir, name string, masters []string, isLight bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	flags := uint16(0)
	if isLight {
		flags |= 0x0200
	}
	data := buildTES4(flags, masters)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func findIssue(out []PluginStatus, filename string, kind DepKind) *DepIssue {
	for _, p := range out {
		if p.Plugin.Filename != filename {
			continue
		}
		for i := range p.HardIssues {
			if p.HardIssues[i].Kind == kind {
				return &p.HardIssues[i]
			}
		}
	}
	return nil
}

func findStatus(out []PluginStatus, filename string) *PluginStatus {
	for i := range out {
		if out[i].Plugin.Filename == filename {
			return &out[i]
		}
	}
	return nil
}

func TestAnalyzeHardDeps_AllPresent(t *testing.T) {
	dir := t.TempDir()
	modA := filepath.Join(dir, "ModA")
	writePluginFile(t, modA, "ModA.esp", []string{"Skyrim.esm"}, false)

	plugins := []Plugin{
		{Filename: "ModA.esp", Ext: ".esp", Source: modA, FromMod: "ModA", Enabled: true},
	}
	mods := []ModEntry{{Name: "ModA", Path: modA}}
	spec, _ := SpecFor("skyrim")

	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, mods, spec, nil)
	if len(out) != 1 || len(out[0].HardIssues) != 0 {
		t.Errorf("expected zero issues, got %#v", out)
	}
}

func TestAnalyzeHardDeps_AbsentMaster(t *testing.T) {
	dir := t.TempDir()
	modA := filepath.Join(dir, "ModA")
	writePluginFile(t, modA, "ModA.esp", []string{"USSEP.esp"}, false)

	plugins := []Plugin{
		{Filename: "ModA.esp", Ext: ".esp", Source: modA, FromMod: "ModA", Enabled: true},
	}
	mods := []ModEntry{{Name: "ModA", Path: modA}}
	spec, _ := SpecFor("skyrimse")

	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, mods, spec, nil)
	if findIssue(out, "ModA.esp", DepMasterAbsent) == nil {
		t.Errorf("expected DepMasterAbsent for USSEP.esp, got %#v", out)
	}
}

func TestAnalyzeHardDeps_DisabledMaster(t *testing.T) {
	dir := t.TempDir()
	modA := filepath.Join(dir, "ModA")
	modUssep := filepath.Join(dir, "USSEP")
	writePluginFile(t, modA, "ModA.esp", []string{"USSEP.esp"}, false)
	writePluginFile(t, modUssep, "USSEP.esp", []string{}, false)

	plugins := []Plugin{
		{Filename: "ModA.esp", Ext: ".esp", Source: modA, FromMod: "ModA", Enabled: true},
	}
	mods := []ModEntry{
		{Name: "ModA", Path: modA},
		{Name: "USSEP", Path: modUssep},
	}
	spec, _ := SpecFor("skyrimse")

	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, mods, spec, nil)
	if findIssue(out, "ModA.esp", DepMasterDisabled) == nil {
		t.Errorf("expected DepMasterDisabled, got %#v", out)
	}
	if findIssue(out, "ModA.esp", DepMasterAbsent) != nil {
		t.Errorf("did not expect DepMasterAbsent — file exists in mod folder")
	}
}

func TestAnalyzeHardDeps_OutOfOrder(t *testing.T) {
	dir := t.TempDir()
	modBase := filepath.Join(dir, "Base")
	modPatch := filepath.Join(dir, "Patch")
	writePluginFile(t, modBase, "Base.esp", []string{}, false)
	writePluginFile(t, modPatch, "Patch.esp", []string{"Base.esp"}, false)

	plugins := []Plugin{
		{Filename: "Patch.esp", Ext: ".esp", Source: modPatch, FromMod: "Patch", Enabled: true},
		{Filename: "Base.esp", Ext: ".esp", Source: modBase, FromMod: "Base", Enabled: true},
	}
	mods := []ModEntry{
		{Name: "Base", Path: modBase},
		{Name: "Patch", Path: modPatch},
	}
	spec, _ := SpecFor("skyrim")

	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, mods, spec, nil)
	if findIssue(out, "Patch.esp", DepMasterOutOfOrder) == nil {
		t.Errorf("expected DepMasterOutOfOrder on Patch.esp, got %#v", out)
	}
}

func TestAnalyzeHardDeps_ImplicitMasterNeverOutOfOrder(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "Mod")
	writePluginFile(t, mod, "Mod.esp", []string{"Skyrim.esm"}, false)

	plugins := []Plugin{
		{Filename: "Mod.esp", Ext: ".esp", Source: mod, FromMod: "Mod", Enabled: true},
	}
	spec, _ := SpecFor("skyrim")

	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, []ModEntry{{Name: "Mod", Path: mod}}, spec, nil)
	if len(out[0].HardIssues) != 0 {
		t.Errorf("expected no issues, got %#v", out[0].HardIssues)
	}
}

func TestAnalyzeHardDeps_ESLFlagDetected(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "ESLMod")
	writePluginFile(t, mod, "Light.esp", []string{}, true)

	plugins := []Plugin{
		{Filename: "Light.esp", Ext: ".esp", Source: mod, FromMod: "ESLMod", Enabled: true},
	}
	spec, _ := SpecFor("skyrimse")
	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(context.Background(), cache, plugins, []ModEntry{{Name: "ESLMod", Path: mod}}, spec, nil)
	st := findStatus(out, "Light.esp")
	if st == nil || !st.IsLight {
		t.Errorf("expected IsLight=true on Light.esp, got %#v", st)
	}
}

func TestAnalyzeHardDeps_ExtraMastersAcceptedAsImplicit(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "TTWMod")
	writePluginFile(t, mod, "TTWMod.esp", []string{"Fallout3.esm"}, false)

	plugins := []Plugin{
		{Filename: "TTWMod.esp", Ext: ".esp", Source: mod, FromMod: "TTWMod", Enabled: true},
	}
	spec, _ := SpecFor("falloutnv")
	cache := NewHeaderCache(0)
	out := AnalyzeHardDeps(
		context.Background(), cache, plugins,
		[]ModEntry{{Name: "TTWMod", Path: mod}},
		spec, []string{"Fallout3.esm"},
	)
	if len(out[0].HardIssues) != 0 {
		t.Errorf("extraMasters should suppress missing-master, got %#v", out[0].HardIssues)
	}
}

func TestAnalyzeHardDeps_DisabledDiscoveredMaster(t *testing.T) {
	dir := t.TempDir()
	masterDir := filepath.Join(dir, "Master")
	patchDir := filepath.Join(dir, "Patch")
	writePluginFile(t, masterDir, "Optional.esm", nil, false)
	writePluginFile(t, patchDir, "Patch.esp", []string{"Optional.esm"}, false)

	ordered := []Plugin{
		{Filename: "Optional.esm", Ext: ".esm", Source: masterDir, Enabled: false},
		{Filename: "Patch.esp", Ext: ".esp", Source: patchDir, Enabled: true},
	}
	out := AnalyzeHardDeps(context.Background(), NewHeaderCache(0), ordered, nil, Spec{}, nil)
	if findIssue(out, "Patch.esp", DepMasterDisabled) == nil {
		t.Fatalf("expected disabled-master issue, got %#v", out)
	}
	if status := findStatus(out, "Optional.esm"); status == nil || len(status.HardIssues) != 0 {
		t.Fatalf("disabled plugin should not be analyzed: %#v", status)
	}
}
