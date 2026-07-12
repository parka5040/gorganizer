package transfer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

// TestManifestRoundTrip locks Encode → Decode field fidelity.
func TestManifestRoundTrip(t *testing.T) {
	in := &Manifest{
		SchemaVersion:     1,
		GorganizerVersion: "1.2.3+abc",
		GameID:            "skyrimse",
		ExportedAt:        time.Date(2026, 7, 11, 10, 30, 0, 0, time.UTC),
		Mods: []ModEntry{
			{Folder: "Alpha Mod", Name: "Alpha", FileCount: 3, TotalBytes: 1024, NexusModID: 12604, NexusFileID: 35407},
			{Folder: "Beta", Name: "Beta", FileCount: 1, TotalBytes: 7},
		},
		Profiles:             []string{"Default", "Survival"},
		IncludesOverwrite:    true,
		IncludesGameSettings: true,
	}
	data, err := EncodeManifest(in)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	out, err := DecodeManifest(data)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

// TestImportRejectsGameMismatch locks the typed error for a wrong-game archive.
func TestImportRejectsGameMismatch(t *testing.T) {
	m := craftedManifest()
	m.GameID = "falloutnv"
	archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
	setRoot(t, t.TempDir())

	_, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
	}, nil)
	var mismatch *TransferGameMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want TransferGameMismatchError", err)
	}
	if mismatch.Want != testGame || mismatch.Got != "falloutnv" {
		t.Errorf("mismatch = %+v", mismatch)
	}
	if _, perr := Preview(testGame, archive); !errors.As(perr, &mismatch) {
		t.Errorf("Preview err = %v, want TransferGameMismatchError", perr)
	}
}

// TestImportRejectsNewerSchema locks the typed error for schema versions this build cannot read.
func TestImportRejectsNewerSchema(t *testing.T) {
	m := craftedManifest()
	m.SchemaVersion = 2
	archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
	setRoot(t, t.TempDir())

	_, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
	}, nil)
	var schema *TransferSchemaError
	if !errors.As(err, &schema) {
		t.Fatalf("err = %v, want TransferSchemaError", err)
	}
	if schema.Version != 2 {
		t.Errorf("schema version = %d, want 2", schema.Version)
	}
}

// TestImportRejectsZeroSchema locks that schema_version 0 (corrupt/absent) is refused.
func TestImportRejectsZeroSchema(t *testing.T) {
	m := craftedManifest()
	m.SchemaVersion = 0
	archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
	setRoot(t, t.TempDir())

	_, err := Preview(testGame, archive)
	var schema *TransferSchemaError
	if !errors.As(err, &schema) {
		t.Fatalf("err = %v, want TransferSchemaError", err)
	}
}

// TestPreviewReportsManifestAndCollisions locks preview fields, Nexus IDs, and collision flags.
func TestPreviewReportsManifestAndCollisions(t *testing.T) {
	archive := exportTestArchive(t)

	setRoot(t, t.TempDir())
	seedCollisions(t)

	preview, err := Preview(testGame, archive)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.SchemaVersion != 1 || preview.GameID != testGame {
		t.Errorf("preview header = %+v", preview)
	}
	if !preview.IncludesOverwrite || !preview.IncludesGameSettings {
		t.Errorf("preview flags = %+v", preview)
	}
	if len(preview.Mods) != 3 || len(preview.Profiles) != 1 {
		t.Fatalf("preview counts = %d mods / %d profiles", len(preview.Mods), len(preview.Profiles))
	}
	byFolder := map[string]dto.ImportPreviewMod{}
	for _, m := range preview.Mods {
		byFolder[m.Folder] = m
	}
	if !byFolder["Alpha Mod"].Collision {
		t.Errorf("Alpha Mod collision not detected")
	}
	if byFolder["Beta"].Collision || byFolder["Gamma"].Collision {
		t.Errorf("false collision: %+v", byFolder)
	}
	gamma := byFolder["Gamma"]
	if gamma.NexusModID != 111 || gamma.NexusFileID != 222 {
		t.Errorf("Gamma nexus ids = %d/%d, want 111/222", gamma.NexusModID, gamma.NexusFileID)
	}
	if gamma.Name != "Gamma Display Name" {
		t.Errorf("Gamma name = %q", gamma.Name)
	}
	if gamma.FileCount != 3 {
		t.Errorf("Gamma file count = %d, want 3 (Data payload + root payload + metadata.yaml)", gamma.FileCount)
	}
	if !preview.Profiles[0].Collision || preview.Profiles[0].Name != "Default" {
		t.Errorf("profile preview = %+v", preview.Profiles)
	}
}
