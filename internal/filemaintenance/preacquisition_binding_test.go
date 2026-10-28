package filemaintenance

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBindPreAcquisitionDecisionReplacesSyntheticWinner(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	pools, err := json.Marshal([]PoolEvidence{{
		PoolKey: "tv:e1", Verdict: "replace",
		Winner: &PlacementRef{PlacementID: "release:rel-pre", Version: 1, Fingerprint: "facts-fp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
 policy_version, profile_version, subscription_version, input_digest,
 candidate_facts_ref, snapshot_generation, coverage_key, coverage_keys, admission_mode, pools,
 verdict, state, version, created_at, updated_at)
VALUES ('dec-pre', 'key-pre', 'anime-pre', 'rel-pre', NULL,
 2, 'profile-v1', 3, 'old-digest', 'rel-pre:facts', NULL,
 'tv:e1', '["tv:e1"]', 'single_copy', $1::jsonb, 'replace', 'admitted', 1,
 CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, string(pools)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-pre', 'Show')`); err != nil {
		t.Fatal(err)
	}
	seedPlacement(t, db, "plc-candidate", "anime-pre", "/library/Show/ep.mkv", "target-fp", []string{"tv:e1"}, "2026-08-01T00:00:00Z")

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewReplacementDecisionService(db)
	view, bound, err := service.bindPreAcquisitionDecisionTxApplication(context.Background(), tx,
		bindingEvaluation("anime-pre", "rel-pre", 3, 2), "tv:e1", []string{"tv:e1"},
		PlacementRef{PlacementID: "plc-candidate", Version: 2, Fingerprint: "target-fp"}, "corr-1")
	if err != nil || !bound {
		_ = tx.Rollback()
		t.Fatalf("bind = bound=%t view=%+v err=%v", bound, view, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if view.CandidatePlacementID == nil || *view.CandidatePlacementID != "plc-candidate" || view.State != "admitted" || view.CoverageKey != "tv:e1" {
		t.Fatalf("unexpected bound view: %+v", view)
	}
	var storedPools []PoolEvidence
	var raw []byte
	if err := db.QueryRow(`SELECT pools FROM replacement_decisions WHERE id = 'dec-pre'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &storedPools); err != nil {
		t.Fatal(err)
	}
	if len(storedPools) != 1 || storedPools[0].Winner == nil || storedPools[0].Winner.PlacementID != "plc-candidate" {
		t.Fatalf("synthetic winner was not rebound: %+v", storedPools)
	}
}

func TestBindPreAcquisitionDecisionStalesVersionMismatch(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	pools := `[ {"pool_key":"tv:e1","verdict":"replace","winner":{"placement_id":"release:rel-stale","version":1,"fingerprint":"fp"}} ]`
	if _, err := db.Exec(`
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
 policy_version, profile_version, subscription_version, input_digest,
 candidate_facts_ref, snapshot_generation, coverage_key, coverage_keys, admission_mode, pools,
 verdict, state, version, created_at, updated_at)
VALUES ('dec-stale', 'key-stale', 'anime-stale', 'rel-stale', NULL,
 2, 'profile-v1', 3, 'old-digest', 'rel-stale:facts', NULL,
 'tv:e1', '["tv:e1"]', 'single_copy', $1::jsonb, 'replace', 'admitted', 1,
 CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, pools); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewReplacementDecisionService(db)
	_, bound, err := service.bindPreAcquisitionDecisionTxApplication(context.Background(), tx,
		bindingEvaluation("anime-stale", "rel-stale", 4, 2), "tv:e1", []string{"tv:e1"},
		PlacementRef{PlacementID: "plc-stale", Version: 1, Fingerprint: "target-fp"}, "")
	if err != nil || bound {
		_ = tx.Rollback()
		t.Fatalf("version mismatch bind = bound=%t err=%v", bound, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := db.QueryRow(`SELECT state, stale_reason FROM replacement_decisions WHERE id = 'dec-stale'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "stale" || reason != staleSubscriptionVersionChanged {
		t.Fatalf("version mismatch must stale decision: state=%s reason=%s", state, reason)
	}
}

// bindingEvaluation is the frozen evaluate input the bind compares against.
func bindingEvaluation(animeID, releaseID string, subscriptionVersion, policyVersion int64) keyEvaluation {
	return keyEvaluation{
		request:             EvaluateRequest{AnimeID: animeID, CandidateRelease: releaseID},
		subscriptionVersion: subscriptionVersion, policyVersion: policyVersion, mode: ModeSingleCopy,
		profile: Profile{Version: "profile-v1"}, factsVersion: "facts-v1", factsFingerprint: "facts-fp",
	}
}
