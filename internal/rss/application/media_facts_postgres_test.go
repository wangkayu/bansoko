package application

import (
	"context"
	"testing"

	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

func TestReleaseEvaluatePersistsMediaFactSnapshot(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-facts-snap", "[Group] Test Show - 07 1080p", "https://downloads.example.test/show-07.torrent")
	seedNativeTorrentFile(t, db, "torrent-facts-snap", "release-facts-snap", "show-07.mkv", 1024, "show-07.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-facts-snap"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-facts-snap"))
	finishReleaseClaim(t, store, service, claim)

	// The accepted release freezes its media fact snapshot from the evaluated
	// release evidence together with the batch plan over its manifest.
	assertReleaseState(t, db, "release-facts-snap", "accepted_pending_download", "accepted", true)

	ctx := context.Background()
	var count int
	var planKeys string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX((release_facts->'batch_plan'->'coverage_keys')::text), '') FROM media_fact_snapshots WHERE release_id = 'release-facts-snap'`).Scan(&count, &planKeys); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("media fact snapshots = %d, want 1", count)
	}
	if planKeys != `["tv:s1:e7"]` {
		t.Fatalf("frozen batch plan coverage = %s, want [\"tv:s1:e7\"]", planKeys)
	}

	var identityRaw, parserConfigRaw []byte
	var parserKind, parserConfigFingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT release_identity, parser_kind, parser_config, parser_config_fingerprint
FROM media_fact_snapshots WHERE release_id = 'release-facts-snap'`).
		Scan(&identityRaw, &parserKind, &parserConfigRaw, &parserConfigFingerprint); err != nil {
		t.Fatal(err)
	}
	if parserKind != string(parser.ParserRegex) {
		t.Fatalf("parser_kind=%s, want regex", parserKind)
	}
	// The frozen config must reproduce the frozen fingerprint, so per-file
	// recognition can rebuild the exact selection from the snapshot row alone.
	frozen, err := parser.NewSelection(parser.ParserKind(parserKind), parserConfigRaw, 0)
	if err != nil {
		t.Fatalf("rebuild frozen selection from snapshot: %v", err)
	}
	if frozen.ConfigFingerprint != parserConfigFingerprint {
		t.Fatalf("frozen fingerprint %s does not match frozen config %s (fingerprint %s)",
			parserConfigFingerprint, parserConfigRaw, frozen.ConfigFingerprint)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	if identity.Episode == nil || string(*identity.Episode) != "7" {
		t.Fatalf("identity episode=%v, want 7", identity.Episode)
	}
	if identity.State != "resolved" {
		t.Fatalf("identity state=%s, want resolved", identity.State)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e7" {
		t.Fatalf("coverage=%v, want tv:s1:e7", identity.CoverageKey)
	}
}
