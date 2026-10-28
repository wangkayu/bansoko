package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/testutil/disposabledb"
)

// seedCandidateRelease inserts the minimal rss_sources + releases chain a
// pre-acquisition replacement admission needs. The candidate has no
// candidate-facts row and no media-fact snapshot: exactly the pristine state of
// a Release's first accepted evaluation.
func seedCandidateRelease(t *testing.T, db *sql.DB, subID, releaseID, title string) {
	t.Helper()
	sourceID := "src-" + releaseID
	if _, err := db.Exec(`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', $3, $3, 'G', 'Show')`, sourceID, subID, "https://feeds.example/rss/"+releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, $4::jsonb, '{}', 'discovered', 'new')`,
		releaseID, sourceID, "key-"+releaseID, fmt.Sprintf(`{"title":%q}`, title)); err != nil {
		t.Fatal(err)
	}
}

// resolvedTVEvidence builds the plan coverage of a single planned TV episode
// with a title that carries the candidate quality dimensions.
func resolvedTVEvidence(t *testing.T, title, coverageKey, _ string) *acquisitiontarget.CandidateEvidence {
	t.Helper()
	return &acquisitiontarget.CandidateEvidence{Title: title, Coverage: []string{coverageKey}}
}

// unresolvedTVEvidence is a plan with no placeable coverage key.
func unresolvedTVEvidence(title string) *acquisitiontarget.CandidateEvidence {
	return &acquisitiontarget.CandidateEvidence{Title: title}
}

// TestCandidateIntentProjectsSelectedEvidenceOnFirstAdmission proves a fresh
// Release (no candidate facts, no media snapshot) can freeze a replacement
// decision from the frozen selected evidence, without waiting for the Release
// owner writer's later projection.
func TestCandidateIntentProjectsSelectedEvidenceOnFirstAdmission(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")

	service := NewReplacementDecisionService(db)
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new",
		resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:e1", "1")))
	if err != nil {
		t.Fatalf("candidate intent: %v", err)
	}
	if intent != acquisitiontarget.IntentReplace {
		t.Fatalf("intent = %q, want replace", intent)
	}
	var coverageRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT coverage_keys FROM release_candidate_facts
WHERE release_id = 'release-new' ORDER BY release_version DESC LIMIT 1`).Scan(&coverageRaw); err != nil {
		t.Fatalf("candidate facts were not projected: %v", err)
	}
	var coverage []string
	if err := json.Unmarshal(coverageRaw, &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage) != 1 || coverage[0] != "tv:e1" {
		t.Fatalf("projected coverage = %v, want [tv:e1]", coverage)
	}
	var verdict, state string
	if err := db.QueryRowContext(ctx, `
SELECT verdict, state FROM replacement_decisions
WHERE managed_anime_id = 'anime-1' AND candidate_release_id = 'release-new'
ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&verdict, &state); err != nil {
		t.Fatalf("decision was not frozen: %v", err)
	}
	if verdict != "replace" || state != "admitted" {
		t.Fatalf("decision = %s/%s, want replace/admitted", verdict, state)
	}
}

// TestCandidateIntentFailClosedWithoutSelectedEvidence proves a Release that
// needs a fresh decision but carries no frozen evidence is a typed fail-closed
// result (never a title re-parse and never a partial decision).
func TestCandidateIntentFailClosedWithoutSelectedEvidence(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")

	service := NewReplacementDecisionService(db)
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", nil))
	if !errors.Is(err, errCandidateEvidenceMissing) {
		t.Fatalf("err = %v, want errCandidateEvidenceMissing", err)
	}
	if intent != acquisitiontarget.IntentEnsurePresent {
		t.Fatalf("intent = %q, want ensure_present", intent)
	}
	var decisions, facts int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM replacement_decisions WHERE candidate_release_id = 'release-new'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM release_candidate_facts WHERE release_id = 'release-new'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || facts != 0 {
		t.Fatalf("fail-closed admission wrote decisions=%d facts=%d", decisions, facts)
	}
}

// TestCandidateIntentProjectionFailureRollsBackAndRetries proves a failed
// candidate-facts projection rolls back atomically (no decision, no partial
// facts) and a retry with the same evidence settles idempotently.
func TestCandidateIntentProjectionFailureRollsBackAndRetries(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")

	if _, err := owner.ExecContext(ctx, `ALTER TABLE release_candidate_facts RENAME TO release_candidate_facts_bak`); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if _, err := owner.ExecContext(ctx, `ALTER TABLE release_candidate_facts_bak RENAME TO release_candidate_facts`); err != nil {
			t.Fatalf("restore candidate facts table: %v", err)
		}
	}
	defer restore()

	service := NewReplacementDecisionService(db)
	evidence := resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:e1", "1")
	if _, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence)); err == nil {
		t.Fatal("projection failure must surface as an error")
	}
	var decisions int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM replacement_decisions WHERE candidate_release_id = 'release-new'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("failed projection persisted %d decisions", decisions)
	}

	restore()
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence))
	if err != nil {
		t.Fatalf("retry after projection failure: %v", err)
	}
	if intent != acquisitiontarget.IntentReplace {
		t.Fatalf("retry intent = %q, want replace", intent)
	}
	var facts int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM release_candidate_facts WHERE release_id = 'release-new'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("retry projected %d candidate facts rows, want exactly 1 (idempotent)", facts)
	}
}

// TestCandidateFactsArrivingAfterFreezeDoNotMoveDecision proves the Release
// owner writer's later projection (a second, later candidate-facts version)
// cannot move an already-frozen decision, and re-asking still returns the same
// intent without a new decision row.
func TestCandidateFactsArrivingAfterFreezeDoNotMoveDecision(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")

	service := NewReplacementDecisionService(db)
	evidence := resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:e1", "1")
	if _, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence)); err != nil {
		t.Fatalf("candidate intent: %v", err)
	}
	type frozenDecision struct {
		id, verdict, state, digest string
		pools                      []byte
	}
	read := func() frozenDecision {
		t.Helper()
		var frozen frozenDecision
		if err := db.QueryRowContext(ctx, `
SELECT id, verdict, state, input_digest, pools FROM replacement_decisions
WHERE managed_anime_id = 'anime-1' AND candidate_release_id = 'release-new'
ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(
			&frozen.id, &frozen.verdict, &frozen.state, &frozen.digest, &frozen.pools); err != nil {
			t.Fatal(err)
		}
		return frozen
	}
	before := read()

	// Mirror the Release owner writer's own candidate-facts projection: a new,
	// higher release_version with the same frozen input fingerprint.
	var parserVersion, inputFingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT parser_version, input_fingerprint FROM release_candidate_facts
WHERE release_id = 'release-new' ORDER BY release_version DESC LIMIT 1`).Scan(&parserVersion, &inputFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO release_candidate_facts
(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys,
 resolution, "group", size_bytes)
VALUES ('release-new', 2, $1, $2, '{"title":"[G] Show - 01 [1080p]"}', '{}', '["tv:e1"]'::jsonb,
 '1080p', 'G', 4096)`, parserVersion, inputFingerprint); err != nil {
		t.Fatal(err)
	}

	after := read()
	if after.id != before.id || after.verdict != before.verdict || after.state != before.state ||
		after.digest != before.digest || string(after.pools) != string(before.pools) {
		t.Fatalf("later candidate facts moved the frozen decision:\nbefore=%+v\nafter=%+v", before, after)
	}
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence))
	if err != nil {
		t.Fatalf("re-ask candidate intent: %v", err)
	}
	if intent != acquisitiontarget.IntentReplace {
		t.Fatalf("re-ask intent = %q, want replace", intent)
	}
	var decisions int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM replacement_decisions WHERE candidate_release_id = 'release-new'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 {
		t.Fatalf("re-ask created %d decisions, want the frozen 1", decisions)
	}
}

// TestCandidateIntentKeepsUnresolvedIdentityNonReplaceable proves a non-scalar
// (unresolved) selected identity never becomes a replacement candidate: it
// projects unresolved coverage and the decision stays blocked.
func TestCandidateIntentKeepsUnresolvedIdentityNonReplaceable(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01-12 [1080p]")

	service := NewReplacementDecisionService(db)
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new",
		unresolvedTVEvidence("[G] Show - 01-12 [1080p]")))
	if err != nil {
		t.Fatalf("candidate intent: %v", err)
	}
	if intent != acquisitiontarget.IntentEnsurePresent {
		t.Fatalf("unresolved identity intent = %q, want ensure_present", intent)
	}
	var verdict, state string
	if err := db.QueryRowContext(ctx, `
SELECT verdict, state FROM replacement_decisions
WHERE managed_anime_id = 'anime-1' AND candidate_release_id = 'release-new'
ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&verdict, &state); err != nil {
		t.Fatalf("unresolved candidate decision: %v", err)
	}
	if verdict != "blocked" || state != "blocked" {
		t.Fatalf("unresolved identity decision = %s/%s, want blocked/blocked", verdict, state)
	}
}
