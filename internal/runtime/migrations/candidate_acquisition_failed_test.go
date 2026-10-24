package migrations

import (
	"context"
	"encoding/json"
	"testing"
)

// When a running acquisition fails, the pre-acquisition replacement
// decisions its release froze (admitted, no candidate placement yet) become
// stale with candidate_acquisition_failed in the same transaction; other
// releases' decisions are untouched.
func TestFailedCandidateStalesItsDecisions(t *testing.T) {
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
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-other', 'src-cand', 'other-key', '{}', '{}', 'accepted_pending_download', 'new')`); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []struct{ id, release string }{{"decision-cand", releaseID}, {"decision-other", "rel-other"}} {
		pools, _ := json.Marshal([]any{map[string]any{"pool_key": "tv:s1:e1", "verdict": "replace", "winner": map[string]any{"placement_id": "release:" + decision.release, "version": 1, "fingerprint": "candidate"}}})
		if _, err := owner.ExecContext(ctx, `
INSERT INTO replacement_decisions(id,idempotency_key,managed_anime_id,candidate_release_id,policy_version,profile_version,candidate_facts_ref,coverage_key,coverage_keys,admission_mode,pools,verdict,state,subscription_version,input_digest)
VALUES ($1,$1,$2,$3,1,'profile-v1','release:1','tv:s1:e1','["tv:s1:e1"]','single_copy',$4,'replace','admitted',1,'digest')`, decision.id, animeID, decision.release, string(pools)); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"decision-cand": "stale/candidate_acquisition_failed/2", "decision-other": "admitted//1"} {
		var state, reason string
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT state, COALESCE(stale_reason, ''), version FROM replacement_decisions WHERE id=$1`, id).Scan(&state, &reason, &version); err != nil {
			t.Fatal(err)
		}
		if got := state + "/" + reason + "/" + string(rune('0'+version)); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}
