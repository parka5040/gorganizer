package ini

import (
	"strings"
	"testing"
)

func TestParseSerialize_RoundTrip(t *testing.T) {
	input := "# comment\n\n[General]\nfoo=bar\nqux=1\n\n[Archive]\nbInvalidateOlderFiles=1\nSInvalidationFile=\n"
	doc := ParseDocument(input)
	got := doc.Serialize()
	if got != input {
		t.Errorf("round-trip changed content:\nwant:\n%q\ngot:\n%q", input, got)
	}
}

func TestSet_InExistingSection(t *testing.T) {
	doc := ParseDocument("[Archive]\nbInvalidateOlderFiles=1\n\n[General]\nfoo=bar\n")
	doc.Set("Archive", "SInvalidationFile", "")
	out := doc.Serialize()
	if !strings.Contains(out, "SInvalidationFile=") {
		t.Fatalf("SInvalidationFile missing after Set:\n%s", out)
	}
	// Should insert inside the Archive section, not at EOF.
	archiveIdx := strings.Index(out, "[Archive]")
	generalIdx := strings.Index(out, "[General]")
	sInvIdx := strings.Index(out, "SInvalidationFile=")
	if !(archiveIdx < sInvIdx && sInvIdx < generalIdx) {
		t.Fatalf("SInvalidationFile not inside [Archive]:\n%s", out)
	}
}

func TestSet_CreatesMissingSection(t *testing.T) {
	doc := ParseDocument("[General]\nfoo=bar\n")
	doc.Set("Archive", "bInvalidateOlderFiles", "1")
	out := doc.Serialize()
	if !strings.Contains(out, "[Archive]") || !strings.Contains(out, "bInvalidateOlderFiles=1") {
		t.Fatalf("Set did not create [Archive]:\n%s", out)
	}
}

func TestRemove_DropsKey(t *testing.T) {
	doc := ParseDocument("[Archive]\nbInvalidateOlderFiles=1\nSInvalidationFile=\n")
	doc.Remove("Archive", "bInvalidateOlderFiles")
	out := doc.Serialize()
	if strings.Contains(out, "bInvalidateOlderFiles") {
		t.Fatalf("Remove left key behind:\n%s", out)
	}
	if !strings.Contains(out, "SInvalidationFile=") {
		t.Fatalf("Remove also dropped sibling key:\n%s", out)
	}
}

func TestMerge_OverlayWins(t *testing.T) {
	primary := ParseDocument("[Archive]\nbInvalidateOlderFiles=0\n")
	overlay := ParseDocument("[Archive]\nbInvalidateOlderFiles=1\nSInvalidationFile=\n")
	primary.Merge(overlay)
	got, _ := primary.Get("Archive", "bInvalidateOlderFiles")
	if got != "1" {
		t.Errorf("overlay didn't override: got=%q want=1", got)
	}
	if _, ok := primary.Get("Archive", "SInvalidationFile"); !ok {
		t.Error("new overlay key missing")
	}
}

func TestTweak_ApplyThenIsApplied(t *testing.T) {
	doc := ParseDocument("")
	tw, ok := FindTweak("falloutnv", "archive_invalidation")
	if !ok {
		t.Fatal("archive_invalidation tweak not registered for falloutnv")
	}
	if tw.IsApplied(doc) {
		t.Fatal("empty doc reports tweak as applied")
	}
	tw.Apply(doc)
	if !tw.IsApplied(doc) {
		t.Fatalf("tweak not detected as applied after Apply:\n%s", doc.Serialize())
	}
	tw.Unapply(doc)
	if tw.IsApplied(doc) {
		t.Fatalf("tweak still detected after Unapply:\n%s", doc.Serialize())
	}
}

func TestPerGameTweaksExist(t *testing.T) {
	for _, game := range []string{"oblivion", "fallout3", "falloutnv", "skyrim", "skyrimse"} {
		if len(AvailableTweaks(game)) == 0 {
			t.Errorf("%s has no tweaks defined", game)
		}
	}
}
