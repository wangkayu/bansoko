package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"testing"
)

func TestPGReplacementRecoveryLoserHandoffRollsBackAndCommitsAtomically(t *testing.T) {
	db := openFrozenTestDB(t)
	ctx := context.Background()
	loserID := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"}, Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	winnerID := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	// Distinct physical objects, so the loser requires actual cleanup admission.
	if _, err := db.Exec(`UPDATE placements SET physical_path=physical_path||'.old' WHERE id=$1`, loserID); err != nil {
		t.Fatal(err)
	}
	ref := func(id string) PlacementRef {
		var r PlacementRef
		r.PlacementID = id
		if err := db.QueryRow(`SELECT version,target_fingerprint FROM placements WHERE id=$1`, id).Scan(&r.Version, &r.Fingerprint); err != nil {
			t.Fatal(err)
		}
		return r
	}
	winner, loser := ref(winnerID), ref(loserID)
	var pv int64
	if err := db.QueryRow(`SELECT retention_version FROM anime_business_configs WHERE managed_anime_id='anime-1'`).Scan(&pv); err != nil {
		t.Fatal(err)
	}
	seedFrozenDecision(t, db, "loser-adopt", "anime-1", winnerID, 1, pv, ModeSingleCopy, &winner, []PlacementRef{loser}, "admitted")
	s := NewReplacementDecisionService(db)
	cleanup := NewCleanupService(db)
	cleanup.SetWorkItems(cleanupWorkItemStore(t, db))
	s.SetCleanupHandoff(cleanup)
	c := workitems.Claim{Kind: workitems.KindReplacementEvaluate, Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"}, ExpectedOwnerVersion: 1, InputSnapshot: json.RawMessage(`{"candidate_release_id":"release-new"}`)}
	o, err := s.EvaluateHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("adopt=%+v %v", o, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteOwner(ctx, txscope.Wrap(tx), o.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM cleanup_operations WHERE replacement_ref='loser-adopt'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("cleanup leaked rollback")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.WriteOwner(ctx, txscope.Wrap(tx), o.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleanup_operations c JOIN work_item_history w ON w.owner_id=c.id AND w.kind='cleanup_quarantine' WHERE c.replacement_ref='loser-adopt'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("cleanup obligation count=%d", count)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id='loser-adopt'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "settled" {
		t.Fatal(state)
	}
}
