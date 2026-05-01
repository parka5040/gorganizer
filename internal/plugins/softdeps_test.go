package plugins

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeV3 struct {
	resolveCalls atomic.Int32
	rangesCalls  atomic.Int32
	rangesByID   map[string]V3DepRangesFields
	resolveErr   error
}

func (f *fakeV3) ResolveGlobalFileID(_ context.Context, gameDomain, gameScopedID string) (string, error) {
	f.resolveCalls.Add(1)
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return "global-" + gameDomain + "-" + gameScopedID, nil
}

func (f *fakeV3) FetchDependencyRanges(_ context.Context, globalFileID string) (V3DepRangesFields, error) {
	f.rangesCalls.Add(1)
	if r, ok := f.rangesByID[globalFileID]; ok {
		return r, nil
	}
	return V3DepRangesFields{}, nil
}

func (f *fakeV3) RateLimitRemaining() (int, int) { return -1, -1 }

func TestSoftDepFetcher_CacheReusesGlobalID(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeV3{rangesByID: map[string]V3DepRangesFields{}}
	f := NewSoftDepFetcher(fake, dir)

	id1, err := f.resolveGlobalID(context.Background(), "skyrimspecialedition", "12345")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := f.resolveGlobalID(context.Background(), "skyrimspecialedition", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("expected same global id, got %q vs %q", id1, id2)
	}
	if got := fake.resolveCalls.Load(); got != 1 {
		t.Errorf("expected 1 resolve call, got %d", got)
	}
}

func TestSoftDepFetcher_PersistGlobalIDsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	fake1 := &fakeV3{rangesByID: map[string]V3DepRangesFields{}}
	f1 := NewSoftDepFetcher(fake1, dir)
	if _, err := f1.resolveGlobalID(context.Background(), "skyrimspecialedition", "12345"); err != nil {
		t.Fatal(err)
	}

	fake2 := &fakeV3{rangesByID: map[string]V3DepRangesFields{}}
	f2 := NewSoftDepFetcher(fake2, dir)
	if _, err := f2.resolveGlobalID(context.Background(), "skyrimspecialedition", "12345"); err != nil {
		t.Fatal(err)
	}
	if got := fake2.resolveCalls.Load(); got != 0 {
		t.Errorf("expected 0 resolve calls (disk hit), got %d", got)
	}
}

func TestSoftDepFetcher_RangesDiskCache(t *testing.T) {
	dir := t.TempDir()
	rng := V3DepRangesFields{Definitions: []V3DepDefinitionFields{
		{Ranges: []V3DepRangeFields{{TargetModID: "266", TargetModName: "USSEP"}}},
	}}
	fake := &fakeV3{rangesByID: map[string]V3DepRangesFields{"global-skyrimspecialedition-1": rng}}
	f := NewSoftDepFetcher(fake, dir)

	r1, err := f.fetchRanges(context.Background(), "global-skyrimspecialedition-1")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.fetchRanges(context.Background(), "global-skyrimspecialedition-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Definitions) != 1 || len(r2.Definitions) != 1 {
		t.Errorf("expected 1 def each, got %d / %d", len(r1.Definitions), len(r2.Definitions))
	}
	if got := fake.rangesCalls.Load(); got != 1 {
		t.Errorf("expected 1 fetch (second hit cache), got %d", got)
	}
}

func TestFilterSatisfiedSoftDeps_DropsSatisfied(t *testing.T) {
	res := &SoftDepResult{
		Filename: "MyMod.esp",
		Issues: []DepIssue{
			{Kind: DepSoftMissing, Master: "v1|266|800", SoftRef: &SoftDepRef{ModName: "USSEP"}},
			{Kind: DepSoftMissing, Master: "v1|999", SoftRef: &SoftDepRef{ModName: "OtherMod"}},
		},
	}
	installed := map[int]bool{266: true}
	FilterSatisfiedSoftDeps(res, installed)
	if len(res.Issues) != 1 {
		t.Fatalf("expected 1 remaining issue, got %d", len(res.Issues))
	}
	if res.Issues[0].SoftRef == nil || res.Issues[0].SoftRef.ModName != "OtherMod" {
		t.Errorf("expected OtherMod to remain, got %#v", res.Issues[0])
	}
	if res.Issues[0].Master != "" {
		t.Errorf("expected encoded alts to be stripped, got %q", res.Issues[0].Master)
	}
}

func TestSoftDepFetcher_ResolveErrorReturnsErr(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeV3{resolveErr: errors.New("network down")}
	f := NewSoftDepFetcher(fake, dir)
	res := f.resolveOne(context.Background(), SoftDepRequest{
		Filename: "x.esp", GameDomain: "skyrimspecialedition", FileID: 1,
	})
	if res.Err == nil {
		t.Error("expected Err to be set")
	}
}
