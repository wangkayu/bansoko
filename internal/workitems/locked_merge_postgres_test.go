package workitems

import (
	"context"
	"testing"
	"time"
)

func TestCreateOrMergeLockedDoesNotMergeNewLiveItem(t *testing.T) {
	db := openTestDB(t)
	s := newTestStore(t, db)
	ctx := context.Background()
	src := createSourceOwner(t, db, "locked-merge")
	if _, err := db.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('locked-rel',$1,'locked','{}','{}','discovered','new')`, src); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	spec := WorkItemSpec{Owner: OwnerRef{Type: OwnerRelease, ID: "locked-rel"}, Kind: KindReleaseEvaluate, IdempotencyKey: "locked-rel", MaxAttempts: 3, ExpectedOwnerVersion: 1}
	empty, err := s.LockLiveForOwners(ctx, tx, []OwnerRef{spec.Owner})
	if err != nil {
		t.Fatal(err)
	}
	var releaseVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='locked-rel' FOR UPDATE`).Scan(&releaseVersion); err != nil {
		t.Fatal(err)
	}
	id, created, err := s.CreateOrMergeLocked(ctx, tx, spec, empty)
	if err != nil || !created || id == "" {
		t.Fatalf("insert: %q %v %v", id, created, err)
	}
	spec.ExpectedOwnerVersion = 2
	skipped, created, err := s.CreateOrMergeLocked(ctx, tx, spec, empty)
	if err != nil || created || skipped != id {
		t.Fatalf("unlocked merge: %q %v %v", skipped, created, err)
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT expected_owner_version FROM work_item_history WHERE id=$1`, id).Scan(&version); err != nil || version != 1 {
		t.Fatalf("unlocked row changed: %d %v", version, err)
	}
	locked, err := s.LockLiveForOwners(ctx, tx, []OwnerRef{spec.Owner})
	if err != nil {
		t.Fatal(err)
	}
	merged, created, err := s.CreateOrMergeLocked(ctx, tx, spec, locked)
	if err != nil || created || merged != id {
		t.Fatalf("locked merge: %q %v %v", merged, created, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT expected_owner_version FROM work_item_history WHERE id=$1`, id).Scan(&version); err != nil || version != 2 {
		t.Fatalf("locked merge missing: %d %v", version, err)
	}
	if _, _, err := s.CreateOrMergeLocked(ctx, tx, spec, LockedWorkItems{}); err == nil {
		t.Fatal("zero lock scope accepted")
	}
}

func TestCreateOrMergeLockedConcurrentCreatorRollbackKeepsEvaluation(t *testing.T) {
	db := openTestDB(t)
	s := newTestStore(t, db)
	ctx := context.Background()
	src := createSourceOwner(t, db, "locked-rollback")
	if _, err := db.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('rollback-rel',$1,'rollback','{}','{}','discovered','new')`, src); err != nil {
		t.Fatal(err)
	}
	put, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer put.Rollback()
	spec := WorkItemSpec{Owner: OwnerRef{Type: OwnerRelease, ID: "rollback-rel"}, Kind: KindReleaseEvaluate, IdempotencyKey: "rollback-rel", MaxAttempts: 3, ExpectedOwnerVersion: 1}
	locked, err := s.LockLiveForOwners(ctx, put, []OwnerRef{spec.Owner})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Rollback()
	var version int64
	if err := creator.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='rollback-rel' FOR UPDATE`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateOrMerge(ctx, creator, spec); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var version int64
		err := put.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='rollback-rel' FOR UPDATE`).Scan(&version)
		if err == nil {
			_, _, err = s.CreateOrMergeLocked(ctx, put, spec, locked)
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Put must wait on creator Release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := creator.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := put.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE owner_id='rollback-rel' AND kind='release_evaluate' AND state IN ('available','leased')`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent creator rollback lost reevaluation: count=%d err=%v", count, err)
	}
}
