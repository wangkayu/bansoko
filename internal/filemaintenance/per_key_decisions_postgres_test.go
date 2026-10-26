package filemaintenance

import (
	"context"
	"database/sql"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
)

// seedReleasePlacement adds one more verified active file placement of a
// release (another file of the same batch) with its coverage.
func seedReleasePlacement(t *testing.T, db *sql.DB, id, releaseID, path, fingerprint string, coverage ...string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ($1, $2, 'anime-1', 'file_operation', $1 || '-op', $3, $3, $4, 'candidate-facts-v1', 'verified', 'active', 1, '2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z')`,
		id, path, fingerprint, releaseID); err != nil {
		t.Fatal(err)
	}
	for ordinal, key := range coverage {
		if _, err := db.Exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref) VALUES ($1, $2, $3, 'candidate-facts-v1', $1 || '-op')`, id, key, ordinal); err != nil {
			t.Fatal(err)
		}
	}
}

func setCandidateCoverage(t *testing.T, db *sql.DB, releaseID, coverage string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE release_candidate_facts SET coverage_keys = $2::jsonb WHERE release_id = $1`, releaseID, coverage); err != nil {
		t.Fatal(err)
	}
}

func decisionsByKey(t *testing.T, views []ReplacementDecisionView) map[string]ReplacementDecisionView {
	t.Helper()
	out := map[string]ReplacementDecisionView{}
	for _, view := range views {
		if _, dup := out[view.CoverageKey]; dup {
			t.Fatalf("two decisions for %s", view.CoverageKey)
		}
		out[view.CoverageKey] = view
	}
	return out
}

func viewLosers(view ReplacementDecisionView) []string {
	ids := []string{}
	if view.Pools == nil {
		return ids
	}
	for _, pool := range *view.Pools {
		if pool.Losers == nil {
			continue
		}
		for _, loser := range *pool.Losers {
			ids = append(ids, loser.PlacementID)
		}
	}
	return ids
}

func decisionState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// A batch release decides per key: the placed E01 file replaces the present
// E01 while the placed E02 file fills E02, and each placement settles only
// the keys it covers.
func TestPerKeyDecisionsReplaceOneEpisodeAndFillAnother(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	old := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"}, Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	e1 := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	seedReleasePlacement(t, db, "plc-new-e2", "release-new", "/library/Show/E02.mkv", "fp-e2", "tv:s1:e2")
	setCandidateCoverage(t, db, "release-new", `["tv:s1:e1","tv:s1:e2"]`)

	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := decisionsByKey(t, views)
	first, second := byKey["tv:s1:e1"], byKey["tv:s1:e2"]
	if len(views) != 2 || first.Verdict != "replace" || second.Verdict != "replace" {
		t.Fatalf("decisions = %+v", views)
	}
	if first.CandidatePlacementID == nil || *first.CandidatePlacementID != e1 || len(viewLosers(first)) != 1 || viewLosers(first)[0] != old {
		t.Fatalf("E01 decision = %+v losers %v", first, viewLosers(first))
	}
	if second.CandidatePlacementID == nil || *second.CandidatePlacementID != "plc-new-e2" || len(viewLosers(second)) != 0 {
		t.Fatalf("E02 decision = %+v", second)
	}
	again, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(again) != 2 || decisionsByKey(t, again)["tv:s1:e1"].ID != first.ID {
		t.Fatalf("re-evaluate must reuse the decisions: %+v %v", again, err)
	}

	if err := service.SettleForPlacement(ctx, "plc-new-e2"); err != nil {
		t.Fatal(err)
	}
	if decisionState(t, db, second.ID) != "settled" || decisionState(t, db, first.ID) != "admitted" || placementState(t, db, old) != "active" {
		t.Fatal("settling the E02 placement must settle only the E02 decision")
	}
	if err := service.SettleForPlacement(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if decisionState(t, db, first.ID) != "settled" || placementState(t, db, old) != "superseded" {
		t.Fatal("settling the E01 placement must replace the present E01")
	}
	// seedFilePlacement puts both E01 files at one path: the new file
	// overwrote the old one in place, so (as for a single episode) the loser's
	// path holds the winner and is never handed to cleanup. A loser at its own
	// path is handed off in TestPerKeyDecisionsShareOneMultiEpisodeLoser.
	_, deletions, _ := decisionCounts(t, db, "anime-1")
	if deletions != 0 {
		t.Fatalf("deletions = %d, want 0 for an in-place loser", deletions)
	}
}

// A multi-episode candidate replacing a multi-episode file freezes one
// decision per key with the same loser; settling the placement supersedes the
// loser once and hands it to cleanup once.
func TestPerKeyDecisionsShareOneMultiEpisodeLoser(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-old-e1e2", "anime-1", "/library/Show/E01-E02.mkv", "fp-old", []string{"tv:s1:e1", "tv:s1:e2"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1", "tv:s1:e2"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("decisions = %+v", views)
	}
	for _, view := range views {
		if view.Verdict != "replace" || view.CandidatePlacementID == nil || *view.CandidatePlacementID != candidate || len(viewLosers(view)) != 1 || viewLosers(view)[0] != "plc-old-e1e2" {
			t.Fatalf("decision %s = %+v losers %v", view.CoverageKey, view, viewLosers(view))
		}
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if state := decisionState(t, db, view.ID); state != "settled" {
			t.Fatalf("decision %s = %s", view.CoverageKey, state)
		}
	}
	var version int64
	if err := db.QueryRow(`SELECT version FROM placements WHERE id = 'plc-old-e1e2' AND state = 'superseded'`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("loser superseded once: version=%d err=%v", version, err)
	}
	var deletions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-old-e1e2' AND companion_id IS NULL`).Scan(&deletions); err != nil || deletions != 1 {
		t.Fatalf("loser deletions = %d err=%v", deletions, err)
	}
}

// A single-episode candidate never replaces a present multi-episode file: the
// pool keeps it and nothing is superseded.
func TestPerKeyDecisionKeepsPartlyCoveredMultiEpisodeFile(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-old-e1e2", "anime-1", "/library/Show/E01-E02.mkv", "fp-old", []string{"tv:s1:e1", "tv:s1:e2"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Verdict != "keep" || len(viewLosers(views[0])) != 0 {
		t.Fatalf("decisions = %+v", views)
	}
	if pools := *views[0].Pools; pools[0].Reason == nil || *pools[0].Reason != ReasonReplacementPartialCoverage {
		t.Fatalf("pool = %+v", pools[0])
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if placementState(t, db, "plc-old-e1e2") != "active" {
		t.Fatal("the multi-episode file must stay")
	}
}

// Before acquisition each planned key freezes its own decision; when one file
// of the batch is placed, only its key's decision is bound, and the other key
// keeps its unbound decision.
func TestPerKeyPreAcquisitionDecisionsBindPerFile(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"}, Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01-02 [1080p]")

	service := NewReplacementDecisionService(db)
	intents, err := service.CandidateIntents(ctx, "anime-1", "release-new", &acquisitiontarget.CandidateEvidence{
		Title: "[G] Show - 01-02 [1080p]", Coverage: []string{"tv:s1:e1", "tv:s1:e2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if intents["tv:s1:e1"] != acquisitiontarget.IntentReplace || intents["tv:s1:e2"] != acquisitiontarget.IntentReplace {
		t.Fatalf("intents = %v", intents)
	}
	before := map[string]string{}
	rows, err := db.Query(`SELECT coverage_key, id FROM replacement_decisions WHERE candidate_release_id = 'release-new' AND candidate_placement_id IS NULL AND state = 'admitted'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			t.Fatal(err)
		}
		before[key] = id
	}
	rows.Close()
	if len(before) != 2 {
		t.Fatalf("pre-acquisition decisions = %v", before)
	}
	// Asking again freezes nothing new.
	if _, err := service.CandidateIntents(ctx, "anime-1", "release-new", &acquisitiontarget.CandidateEvidence{
		Title: "[G] Show - 01-02 [1080p]", Coverage: []string{"tv:s1:e1", "tv:s1:e2"},
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE candidate_release_id = 'release-new'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("decisions after a second ask = %d err=%v", count, err)
	}

	seedReleasePlacement(t, db, "plc-new-e1", "release-new", "/library/Show/E01.v2.mkv", "fp-new-e1", "tv:s1:e1")
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := decisionsByKey(t, views)
	if got := byKey["tv:s1:e1"]; got.ID != before["tv:s1:e1"] || got.CandidatePlacementID == nil || *got.CandidatePlacementID != "plc-new-e1" {
		t.Fatalf("E01 must bind its own pre-acquisition decision: %+v", got)
	}
	if got := byKey["tv:s1:e2"]; got.ID != before["tv:s1:e2"] || got.CandidatePlacementID != nil {
		t.Fatalf("E02 keeps its unbound decision: %+v", got)
	}
}
