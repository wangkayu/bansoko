package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
)

func TestCurrentReleaseNotesComeFromTheInstalledManifest(t *testing.T) {
	manifest := embeddedManifestFixture(hardeningdomain.HashArtifact([]byte("p")))
	manifest.ReleaseID, manifest.Version = "v0.2.0", "0.2.0"
	manifest.EmbeddedUpdate.TargetReleaseID, manifest.EmbeddedUpdate.TargetVersion = "v0.2.0", "0.2.0"
	published := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	manifest.ReleaseNotes = []hardeningdomain.ReleaseNotesEntry{
		{Version: "0.2.0", PublishedAt: published, Notes: hardeningdomain.ReleaseNotesText{ZhCN: "## 改进\n- 一项\n", En: "## Improved\n- One\n"}},
		{Version: "0.1.0", PublishedAt: published.AddDate(0, -1, 0), Notes: hardeningdomain.ReleaseNotesText{ZhCN: "## 新增\n- 首个\n", En: "## New\n- First\n"}},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "releases", "v0.2.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SignedManifestEntryPath), body, 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{updatesRoot: root, buildID: "v0.2.0", buildVersion: "0.2.0"}
	notes := service.currentReleaseNotes()
	if notes == nil || notes.Version != "0.2.0" || notes.Notes.En != "## Improved\n- One\n" || !notes.PublishedAt.Equal(published) {
		t.Fatalf("current notes: %+v", notes)
	}

	for name, candidate := range map[string]*Service{
		"base image":         {updatesRoot: root, buildID: "base", buildVersion: "0.2.0"},
		"version not listed": {updatesRoot: root, buildID: "v0.2.0", buildVersion: "0.3.0"},
		"path escape":        {updatesRoot: root, buildID: "../releases/v0.2.0", buildVersion: "0.2.0"},
	} {
		if got := candidate.currentReleaseNotes(); got != nil {
			t.Fatalf("%s: notes %+v", name, got)
		}
	}
}
