package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testFeedSnapshotIdentity() ProviderRSSPreviewIdentity {
	return ProviderRSSPreviewIdentity{
		SnapshotID:      "snap-1",
		SnapshotVersion: 3,
		ContentHash:     "sha256:" + strings.Repeat("a", 64),
	}
}

func testFeedSnapshot() ProviderFeedSnapshot {
	return ProviderFeedSnapshot{
		ID: "snap-1", Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: "12",
		Version: 3, ContentHash: "sha256:" + strings.Repeat("a", 64),
		FetchedAt: time.Now().UTC(), FreshUntil: time.Now().UTC().Add(time.Minute),
	}
}

func TestProviderFeedSnapshotVerifyIdentityAcceptsExactMatch(t *testing.T) {
	if err := testFeedSnapshot().VerifyIdentity(testFeedSnapshotIdentity()); err != nil {
		t.Fatalf("exact identity was rejected: %v", err)
	}
}

func TestProviderFeedSnapshotVerifyIdentityRejectsEveryDrift(t *testing.T) {
	cases := map[string]func(*ProviderFeedSnapshot){
		"id":      func(s *ProviderFeedSnapshot) { s.ID = "snap-other" },
		"version": func(s *ProviderFeedSnapshot) { s.Version = 4 },
		"hash":    func(s *ProviderFeedSnapshot) { s.ContentHash = "sha256:" + strings.Repeat("b", 64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := testFeedSnapshot()
			mutate(&snapshot)
			err := snapshot.VerifyIdentity(testFeedSnapshotIdentity())
			if !errors.Is(err, ErrProviderFeedSnapshotIdentityMismatch) {
				t.Fatalf("drift %s error = %v, want identity mismatch", name, err)
			}
		})
	}
}

func TestProviderFeedSnapshotIdentityShapeValidation(t *testing.T) {
	valid := testFeedSnapshotIdentity()
	if err := valid.ValidateIdentityShape(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	broken := map[string]ProviderRSSPreviewIdentity{
		"empty id":      {SnapshotID: "", SnapshotVersion: 1, ContentHash: valid.ContentHash},
		"blank id":      {SnapshotID: "   ", SnapshotVersion: 1, ContentHash: valid.ContentHash},
		"zero version":  {SnapshotID: "snap-1", SnapshotVersion: 0, ContentHash: valid.ContentHash},
		"negative":      {SnapshotID: "snap-1", SnapshotVersion: -1, ContentHash: valid.ContentHash},
		"empty hash":    {SnapshotID: "snap-1", SnapshotVersion: 1, ContentHash: ""},
		"short hash":    {SnapshotID: "snap-1", SnapshotVersion: 1, ContentHash: "sha256:abc"},
		"uppercase hex": {SnapshotID: "snap-1", SnapshotVersion: 1, ContentHash: "sha256:" + strings.Repeat("A", 64)},
	}
	for name, identity := range broken {
		t.Run(name, func(t *testing.T) {
			if err := identity.ValidateIdentityShape(); err == nil {
				t.Fatalf("identity %+v was accepted", identity)
			}
		})
	}
}

func TestProviderFeedSnapshotFreshnessAtUsesPersistedState(t *testing.T) {
	snapshot := testFeedSnapshot()
	if got := snapshot.FreshnessAt(time.Now().UTC()); got != FeedSnapshotFresh {
		t.Fatalf("fresh snapshot reported %q", got)
	}
	expired := snapshot
	expired.FreshUntil = time.Now().UTC().Add(-time.Second)
	if got := expired.FreshnessAt(time.Now().UTC()); got != FeedSnapshotStale {
		t.Fatalf("expired snapshot reported %q", got)
	}
	missing := ProviderFeedSnapshot{}
	if got := missing.FreshnessAt(time.Now().UTC()); got != FeedSnapshotMissing {
		t.Fatalf("identity-less snapshot reported %q", got)
	}
}

func TestProviderFeedSnapshotSeedValidatesTorrentFilename(t *testing.T) {
	seed := ProviderFeedSnapshotSeed{
		StableSeedKey: "seed-1", Locator: "https://example.test/a.torrent",
		LocatorDisplay: "https://example.test/a.torrent", Title: "Release",
	}
	if err := seed.Validate(); err != nil {
		t.Fatalf("seed without a torrent filename was rejected: %v", err)
	}
	blank := ""
	seed.TorrentFilename = &blank
	if err := seed.Validate(); err == nil {
		t.Fatal("blank torrent filename was accepted")
	}
	long := strings.Repeat("x", MaxProviderFeedTitleLength+1)
	seed.TorrentFilename = &long
	if err := seed.Validate(); err == nil {
		t.Fatal("oversized torrent filename was accepted")
	}
	valid := "Release - 01.mkv"
	seed.TorrentFilename = &valid
	if err := seed.Validate(); err != nil {
		t.Fatalf("bounded torrent filename was rejected: %v", err)
	}
}

func TestCanonicalFeedSnapshotContentHashCoversSelectionAndTargetInputs(t *testing.T) {
	base := []ProviderFeedSnapshotSeed{{
		StableSeedKey: "seed-1", Locator: "https://example.test/a.torrent",
		LocatorDisplay: "https://example.test/a.torrent", Title: "Release",
	}}
	first, err := CanonicalFeedSnapshotContentHash(base)
	if err != nil {
		t.Fatal(err)
	}
	// The v2 schema covers the stable seed key and the torrent filename: both
	// are inputs the persisted snapshot needs to replay its own selection and
	// rendered target, so a change to either must change the identity.
	renamed := append([]ProviderFeedSnapshotSeed(nil), base...)
	renamed[0].StableSeedKey = "seed-2"
	if renamedHash, err := CanonicalFeedSnapshotContentHash(renamed); err != nil {
		t.Fatal(err)
	} else if renamedHash == first {
		t.Fatalf("stable seed key did not change the content hash: %s", first)
	}
	withName := append([]ProviderFeedSnapshotSeed(nil), base...)
	name := "Release - 01.mkv"
	withName[0].TorrentFilename = &name
	second, err := CanonicalFeedSnapshotContentHash(withName)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("torrent filename did not change the content hash: %s", first)
	}
	// A snapshot that reproduces its declared hash verifies; a tampered seed
	// fact fails closed.
	snapshot := ProviderFeedSnapshot{ContentHash: second, Seeds: withName}
	if err := snapshot.VerifyContentHash(); err != nil {
		t.Fatalf("self-consistent snapshot failed verification: %v", err)
	}
	tampered := append([]ProviderFeedSnapshotSeed(nil), withName...)
	other := "Release - 02.mkv"
	tampered[0].TorrentFilename = &other
	if err := (ProviderFeedSnapshot{ContentHash: second, Seeds: tampered}).VerifyContentHash(); err == nil {
		t.Fatal("tampered seed facts verified")
	}
}

// A seed that never opts into torrent acquisition leaves the state empty and is
// read as not_applicable. The canonical content hash must use that same reading,
// or the identity a caller computed would no longer verify after the snapshot
// round-tripped through a column whose stored default is the normalized state.
func TestCanonicalContentHashIsStableAcrossTheEmptyAcquisitionState(t *testing.T) {
	implicit := ProviderFeedSnapshotSeed{
		StableSeedKey: "seed-implicit", Locator: "https://example.test/a.torrent",
		LocatorDisplay: "https://example.test/", Title: "Release a",
	}
	explicit := implicit
	explicit.TorrentState = TorrentAcquisitionNotApplicable

	implicitHash, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{implicit})
	if err != nil {
		t.Fatalf("hash an implicit state: %v", err)
	}
	explicitHash, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{explicit})
	if err != nil {
		t.Fatalf("hash an explicit state: %v", err)
	}
	if implicitHash != explicitHash {
		t.Fatalf("implicit state hashes to %s, explicit to %s", implicitHash, explicitHash)
	}
	if implicit.NormalizedTorrentState() != TorrentAcquisitionNotApplicable {
		t.Fatalf("empty state normalizes to %q", implicit.NormalizedTorrentState())
	}
}
