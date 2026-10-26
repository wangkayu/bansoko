package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPGReplacementRecoveryCommittedUnknownAndFence(t *testing.T) {
	db := openFrozenTestDB(t)
	ctx := context.Background()
	placementID := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	if _, err := db.Exec(`UPDATE anime_business_configs SET admission_mode='single_copy',quality_profile='{"version":"profile-v1","items":[]}' WHERE managed_anime_id='anime-1'`); err != nil {
		t.Fatal(err)
	}
	var policyVersion int64
	if err := db.QueryRow(`SELECT replacement_version FROM anime_business_configs WHERE managed_anime_id='anime-1'`).Scan(&policyVersion); err != nil {
		t.Fatal(err)
	}
	s := NewReplacementDecisionService(db)
	observer := s.EvaluateHandler().(worker.RecoveryObserver)
	c := workitems.Claim{Kind: workitems.KindReplacementEvaluate, Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"}, ExpectedOwnerVersion: 1, InputSnapshot: json.RawMessage(`{"candidate_release_id":"release-new"}`)}
	o, err := observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("missing commit must remain unknown: %+v %v", o, err)
	}
	// A replacement decision with submitted DB settlement obligation is not yet
	// a completed result; no recovery may re-run SettleForPlacement blindly.
	var fp string
	var version int64
	if err := db.QueryRow(`SELECT source_fingerprint,version FROM placements WHERE id=$1 AND managed_anime_id='anime-1'`, placementID).Scan(&fp, &version); err != nil {
		t.Fatal(err)
	}
	winner := &PlacementRef{PlacementID: placementID, Version: version, Fingerprint: fp}
	seedFrozenDecision(t, db, "recovery-decision", "anime-1", placementID, 1, policyVersion, ModeSingleCopy, winner, nil, "admitted")
	o, err = observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("admitted reservation adoption: %+v %v", o, err)
	}
	txAdopt, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteOwner(ctx, txscope.Wrap(txAdopt), o.OwnerResult); err != nil {
		_ = txAdopt.Rollback()
		t.Fatal(err)
	}
	if err := txAdopt.Commit(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id='recovery-decision'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "settled" {
		t.Fatalf("adopted state=%s", state)
	}
	o, err = observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("committed=%+v %v", o, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE subscriptions SET version=version+1 WHERE managed_anime_id='anime-1'`); err != nil {
		t.Fatal(err)
	}
	_, err = o.WriteOwner(ctx, txscope.Wrap(tx), o.OwnerResult)
	_ = tx.Rollback()
	if !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("fence=%v", err)
	}
}
