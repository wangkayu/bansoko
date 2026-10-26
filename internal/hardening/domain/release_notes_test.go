package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestCompareSemVerPrecedence(t *testing.T) {
	ordered := []string{"0.0.0-test.9", "0.0.0-test.10", "0.0.0", "0.1.0-dev", "0.1.0", "0.1.1", "0.2.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta", "1.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		a, err := ParseSemVer(ordered[i])
		if err != nil {
			t.Fatal(ordered[i], err)
		}
		b, err := ParseSemVer(ordered[i+1])
		if err != nil {
			t.Fatal(ordered[i+1], err)
		}
		if CompareSemVer(a, b) != -1 || CompareSemVer(b, a) != 1 || CompareSemVer(a, a) != 0 {
			t.Fatalf("%s must sort before %s", ordered[i], ordered[i+1])
		}
	}
	for _, bad := range []string{"", "v0.1.0", "0.1", "01.0.0", "0.1.0+build", "0.1.0-", "0.1.0-01", "0.1.0-a..b", "latest"} {
		if _, err := ParseSemVer(bad); err == nil {
			t.Fatalf("%q parsed as a semantic version", bad)
		}
	}
}

func notesEntry(version string) ReleaseNotesEntry {
	return ReleaseNotesEntry{Version: version, PublishedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), Notes: ReleaseNotesText{ZhCN: "## 修复\n- 修复一项\n", En: "## Fixed\n- Fixed one\n"}}
}

func TestValidateReleaseNotes(t *testing.T) {
	if err := ValidateReleaseNotes("0.2.0", nil); err != nil {
		t.Fatalf("absent notes must be valid: %v", err)
	}
	if err := ValidateReleaseNotes("0.2.0", []ReleaseNotesEntry{notesEntry("0.2.0"), notesEntry("0.1.1"), notesEntry("0.1.0")}); err != nil {
		t.Fatalf("valid history rejected: %v", err)
	}
	missingEn := notesEntry("0.2.0")
	missingEn.Notes.En = " "
	oversize := notesEntry("0.2.0")
	oversize.Notes.ZhCN = strings.Repeat("a", MaxReleaseNotesLanguageBytes+1)
	undated := notesEntry("0.2.0")
	undated.PublishedAt = time.Time{}
	many := []ReleaseNotesEntry{}
	for i := 11; i >= 0; i-- {
		many = append(many, notesEntry("0."+string(rune('0'+i/10))+string(rune('0'+i%10))+".0"))
	}
	total := []ReleaseNotesEntry{}
	for i := 5; i >= 1; i-- {
		entry := notesEntry("0." + string(rune('0'+i)) + ".0")
		entry.Notes.ZhCN = strings.Repeat("a", MaxReleaseNotesLanguageBytes)
		entry.Notes.En = strings.Repeat("b", MaxReleaseNotesLanguageBytes)
		total = append(total, entry)
	}
	for name, entries := range map[string][]ReleaseNotesEntry{
		"wrong first version": {notesEntry("0.1.0")},
		"not newest first":    {notesEntry("0.2.0"), notesEntry("0.1.0"), notesEntry("0.1.1")},
		"duplicate":           {notesEntry("0.2.0"), notesEntry("0.2.0")},
		"missing language":    {missingEn},
		"oversize language":   {oversize},
		"undated":             {undated},
		"too many":            many,
		"total budget":        total,
	} {
		version := entries[0].Version
		if name == "wrong first version" {
			version = "0.2.0"
		}
		if err := ValidateReleaseNotes(version, entries); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestReleaseNotesNewerThan(t *testing.T) {
	entries := []ReleaseNotesEntry{notesEntry("0.3.0"), notesEntry("0.2.0"), notesEntry("0.1.0")}
	got := ReleaseNotesNewerThan("0.1.0", entries)
	if len(got) != 2 || got[0].Version != "0.3.0" || got[1].Version != "0.2.0" {
		t.Fatalf("newer than 0.1.0: %+v", got)
	}
	if got := ReleaseNotesNewerThan("0.0.0-test.120", entries); len(got) != 3 {
		t.Fatalf("a test build precedes every release: %+v", got)
	}
	if got := ReleaseNotesNewerThan("dev", entries); len(got) != 3 {
		t.Fatalf("an unordered running version shows every entry: %+v", got)
	}
	if got := ReleaseNotesNewerThan("0.3.0", entries); len(got) != 0 {
		t.Fatalf("nothing is newer than the newest: %+v", got)
	}
}

func TestReleaseNotesAreSignedManifestBytes(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validEmbeddedManifestForNotes()
	manifest.Version = "0.2.0"
	manifest.ReleaseNotes = []ReleaseNotesEntry{notesEntry("0.2.0")}
	if err := manifest.Sign(private); err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature(public); err != nil {
		t.Fatalf("signed notes rejected: %v", err)
	}
	tampered := manifest
	tampered.ReleaseNotes = []ReleaseNotesEntry{notesEntry("0.2.0")}
	tampered.ReleaseNotes[0].Notes.En = "## Fixed\n- Something else\n"
	if err := tampered.VerifySignature(public); err == nil {
		t.Fatal("altered notes kept a valid signature")
	}
	invalid := manifest
	invalid.ReleaseNotes = []ReleaseNotesEntry{notesEntry("0.1.0")}
	if err := invalid.Sign(private); err == nil {
		t.Fatal("notes that do not start with the manifest version were signed")
	}
}

func validEmbeddedManifestForNotes() ReleaseManifest {
	digest := strings.Repeat("a", 64)
	return ReleaseManifest{
		SchemaVersion: "release-manifest-1", ReleaseSequence: 3, ReleaseID: "v0.2.0", Version: "0.2.0",
		Channel: ReleaseChannelStable, SourceCommit: strings.Repeat("b", 40), APIContractVersion: "v1",
		Rollback: RestoreRequired, Immutable: true, SBOMRef: "sha256:" + digest, ProvenanceRef: "sha256:" + digest,
		ArtifactDigest: digest, Artifacts: []ArtifactRef{{Platform: "linux", Architecture: "amd64", Digest: digest, Signature: "sig"}},
	}
}
