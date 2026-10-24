package migrations

import (
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
)

func TestAnimeRetirementCompleteSetRollback(t *testing.T) {
	owner, db, p := progressProbe(t, "retirement-set", nil, nil)
	ctx := context.Background()
	var anime string
	var epoch int64
	if err := owner.QueryRowContext(ctx, `SELECT managed_anime_id,owner_lifecycle_epoch FROM work_item_history WHERE id=$1`, p.SubmitWorkID).Scan(&anime, &epoch); err != nil {
		t.Fatal(err)
	}
	// The conflict is the last ID, so at least one earlier row was locked first.
	if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('set-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	var added string
	if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('z-conflict','artifact','set-artifact','artifact_inspect','set-audit',3,'{}',1,0::double precision,7::smallint)`).Scan(&added); err != nil {
		t.Fatal(err)
	}
	blocker, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT id FROM work_items WHERE id='z-conflict' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id=$1 FOR UPDATE`, anime); err != nil {
		t.Fatal(err)
	}
	if err := workitems.RetireUnadmittedTx(ctx, tx, anime, epoch); err == nil {
		t.Fatal("direct termination skipped conflict")
	} else if !isRetirementSetConflict(err) {
		t.Fatalf("direct termination conflict=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	probe, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback()
	if _, err := probe.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id=$1 FOR UPDATE NOWAIT`, anime); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `SELECT id FROM work_items WHERE managed_anime_id=$1 AND id<>'z-conflict' ORDER BY id COLLATE "C" FOR UPDATE NOWAIT`, anime); err != nil {
		t.Fatal(err)
	}
	if err := probe.Rollback(); err != nil {
		t.Fatal(err)
	}
	var failed int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE managed_anime_id=$1 AND state='failed'`, anime).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("partial termination=%d", failed)
	}
}

func isRetirementSetConflict(err error) bool {
	var state interface{ SQLState() string }
	return errors.As(err, &state) && state.SQLState() == "55P03"
}
