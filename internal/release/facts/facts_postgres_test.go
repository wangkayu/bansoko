package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func openFactsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

func seedCandidateRelease(t *testing.T, db *sql.DB, releaseID string) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-facts-test', 'Facts Test')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-facts-test', 'anime-facts-test', 'custom', 'Facts', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('src-facts-test', 'sub-facts-test', 'custom_rss', 'https://facts.test/rss', 'https://facts.test/rss', 'G', 'Facts')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
		 VALUES ('` + releaseID + `', 'src-facts-test', 'guid:facts', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'accepted_pending_download', 'accepted')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func testFacts(releaseID string, version int64) ReleaseCandidateFacts {
	evidence, _ := json.Marshal(map[string]any{"title": "[G] Show - S01E01"})
	facts, _ := json.Marshal(map[string]any{"season": 1, "episode": "1"})
	return ReleaseCandidateFacts{
		ReleaseID:        releaseID,
		ReleaseVersion:   version,
		ParserVersion:    ParserVersion,
		InputFingerprint: "fp-" + releaseID + "-" + itoa(version),
		RawEvidence:      evidence,
		Facts:            facts,
		CoverageKeys:     []string{"tv:e1"},
		Resolution:       "1080p",
		Language:         "ja",
		Group:            "G",
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestUpsertIdempotentSameFingerprint(t *testing.T) {
	db := openFactsTestDB(t)
	defer db.Close()
	seedCandidateRelease(t, db, "release-facts-1")
	repo := NewPGRepository(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertForRelease(ctx, txscope.Wrap(tx), testFacts("release-facts-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Same fingerprint: no-op (row count unchanged, timestamp preserved).
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertForRelease(ctx, txscope.Wrap(tx), testFacts("release-facts-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM release_candidate_facts WHERE release_id = 'release-facts-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent upsert must keep one row, got %d", count)
	}
	facts, err := repo.GetForRelease(ctx, "release-facts-1")
	if err != nil || facts == nil {
		t.Fatalf("read facts: %v", err)
	}
	if facts.ReleaseVersion != 1 || len(facts.CoverageKeys) != 1 || facts.CoverageKeys[0] != "tv:e1" {
		t.Fatalf("unexpected facts: %+v", facts)
	}
	if facts.Resolution != "1080p" || facts.Group != "G" {
		t.Fatalf("quality facts missing: %+v", facts)
	}
}

func TestUpsertKeepsOldVersionsReadable(t *testing.T) {
	db := openFactsTestDB(t)
	defer db.Close()
	seedCandidateRelease(t, db, "release-facts-2")
	repo := NewPGRepository(db)
	ctx := context.Background()

	tx, _ := db.BeginTx(ctx, nil)
	if err := repo.UpsertForRelease(ctx, txscope.Wrap(tx), testFacts("release-facts-2", 1)); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	// Parser upgrade: new version with a different fingerprint.
	tx, _ = db.BeginTx(ctx, nil)
	factsUpgraded := testFacts("release-facts-2", 2)
	factsUpgraded.InputFingerprint = "fp-upgraded"
	if err := repo.UpsertForRelease(ctx, txscope.Wrap(tx), factsUpgraded); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()

	latest, err := repo.GetForRelease(ctx, "release-facts-2")
	if err != nil || latest == nil || latest.ReleaseVersion != 2 {
		t.Fatalf("latest must be v2: %+v err=%v", latest, err)
	}
	old, err := repo.GetVersion(ctx, "release-facts-2", 1)
	if err != nil || old == nil || old.ReleaseVersion != 1 {
		t.Fatalf("old version must stay readable: %+v err=%v", old, err)
	}
}
