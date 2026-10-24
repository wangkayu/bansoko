package migrations

import (
	"context"
	"encoding/json"
	"testing"
)

// A replacement decision is one coverage key, and the column is required.
func TestReplacementDecisionRequiresCoverageKey(t *testing.T) {
	db := installedDB(t)
	var nullable string
	if err := db.QueryRow(`SELECT is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='replacement_decisions' AND column_name='coverage_key'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("replacement_decisions.coverage_key is_nullable = %s, want NO", nullable)
	}
}

// A failed candidate acquisition stales only the keys it never placed: a key a verified active placement of the release covers keeps
// its decision for the evaluation to bind and settle.
func TestFailedCandidateKeepsPlacedKeys(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "cand")
	p := validQueued(t, "cand", "cand-key", "cand")
	if _, err := callQueuedAccept(t, owner, p); err != nil {
		t.Fatal(err)
	}
	var releaseID, animeID string
	if err := owner.QueryRowContext(ctx, `SELECT source_release_ref, origin_managed_anime_ref FROM acquisitions WHERE id=$1 AND state='running'`, p.AcquisitionID).Scan(&releaseID, &animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, quality_facts_ref, facts_version)
VALUES ('plc-e1', '/library/Show/E01.mkv', $1, 'file_operation', 'op-e1', 'fp', 'fp', $2, 'facts-v1')`, animeID, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-e1', 'tv:s1:e1', 0, 'facts-v1', 'op-e1')`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tv:s1:e1", "tv:s1:e2"} {
		pools, _ := json.Marshal([]any{map[string]any{"pool_key": key, "verdict": "replace", "winner": map[string]any{"placement_id": "release:" + releaseID, "version": 1, "fingerprint": "candidate"}}})
		id := "decision-" + key[len(key)-2:]
		if _, err := owner.ExecContext(ctx, `
INSERT INTO replacement_decisions(id,idempotency_key,managed_anime_id,candidate_release_id,policy_version,profile_version,candidate_facts_ref,coverage_key,coverage_keys,admission_mode,pools,verdict,state,subscription_version,input_digest)
VALUES ($1,$1,$2,$3,1,'profile-v1','release:1',$4,jsonb_build_array($4::text),'single_copy',$5,'replace','admitted',1,'digest')`, id, animeID, releaseID, key, string(pools)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"decision-e1": "admitted/", "decision-e2": "stale/candidate_acquisition_failed"} {
		var state, reason string
		if err := owner.QueryRowContext(ctx, `SELECT state, COALESCE(stale_reason, '') FROM replacement_decisions WHERE id=$1`, id).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if got := state + "/" + reason; got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}
