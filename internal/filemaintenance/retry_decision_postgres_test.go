package filemaintenance

import (
	"context"
	"encoding/json"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/testutil/disposabledb"
)

// A Release accepted again after its acquisition failed (RSS retry) freezes a
// fresh replacement decision: the stale decision of the failed acquisition
// neither shadows the new evaluation nor replays through the idempotency key
// of identical inputs.
func TestCandidateIntentAfterStaleDecisionFreezesANewOne(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")
	service := NewReplacementDecisionService(db)
	evidence := resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:e1", "1")
	if intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence)); err != nil || intent != acquisitiontarget.IntentReplace {
		t.Fatalf("first intent = %q, %v; want replace", intent, err)
	}
	// The failed acquisition stales its pre-acquisition decision (migration 314).
	if _, err := owner.ExecContext(ctx, `
UPDATE replacement_decisions SET state = 'stale', stale_reason = 'candidate_acquisition_failed', version = version + 1
WHERE candidate_release_id = 'release-new'`); err != nil {
		t.Fatal(err)
	}
	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence))
	if err != nil {
		t.Fatal(err)
	}
	if intent != acquisitiontarget.IntentReplace {
		t.Fatalf("retried intent = %q, want replace from a fresh decision", intent)
	}
	var total, admitted int
	if err := db.QueryRowContext(ctx, `
SELECT count(*), count(*) FILTER (WHERE state = 'admitted')
FROM replacement_decisions WHERE candidate_release_id = 'release-new'`).Scan(&total, &admitted); err != nil {
		t.Fatal(err)
	}
	if total != 2 || admitted != 1 {
		t.Fatalf("decisions = %d (admitted %d), want the stale one kept and one new admitted", total, admitted)
	}
}

// Before acquisition the pools follow the plan being admitted, not the latest
// media fact snapshot: on a retry that snapshot is the failed acquisition's
// plan, and an episode this admission no longer places must never get a
// replacement decision (decisions are per key, so a key outside the
// admitted plan would freeze its own row).
func TestCandidateIntentPoolsFollowTheAdmittedPlan(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01-02 [1080p]")
	// The earlier acquisition's frozen plan covered both episodes.
	if _, err := owner.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-old-plan', 'release-new', 'media-facts-v1', 'episode-identity-v1', 'regex', 'digest-old-plan', '{}'::jsonb,
        '{"title":"[G] Show - 01-02 [1080p]","batch_plan":{"coverage_keys":["tv:e1","tv:e2"]}}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	service := NewReplacementDecisionService(db)
	evidence := &acquisitiontarget.CandidateEvidence{Title: "[G] Show - 01-02 [1080p]", Coverage: []string{"tv:e1"}}
	intents, err := service.CandidateIntents(ctx, "anime-1", "release-new", evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents["tv:e1"] != acquisitiontarget.IntentReplace {
		t.Fatalf("intents = %v, want only tv:e1 replace", intents)
	}
	rows, err := db.QueryContext(ctx, `SELECT coverage_key, coverage_keys FROM replacement_decisions WHERE candidate_release_id = 'release-new'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		var coverage []string
		if err := json.Unmarshal(raw, &coverage); err != nil {
			t.Fatal(err)
		}
		if key != "tv:e1" || len(coverage) != 1 || coverage[0] != "tv:e1" {
			t.Fatalf("decision for %s %v, want only the admitted plan's tv:e1", key, coverage)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decisions = %d, want 1", count)
	}
}
