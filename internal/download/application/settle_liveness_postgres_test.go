package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/workitems"
)

// A running root always holds a work item, and its stage work leaving the queue
// is what wakes settlement (migration 091): no stage arms the settle item by
// hand, an incomplete graph keeps its settle item and stays visibly blocked, and
// a transaction that leaves the root with no work at all gets a fresh settle
// item.
func TestSettleLivenessFollowsStageWorkExit(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-settle-liveness"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	_, store := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	root := "acq-" + taskID
	owner := ownerTestDB(t)

	// The submit is done and nothing else is live or definite: the graph is
	// incomplete. The settle item stays, due later, and the root shows the block.
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "deferred" {
		t.Fatalf("incomplete settlement = %q, %v; want deferred", status, err)
	}
	if items, due, blocked := settleLiveness(t, db, root); items != 1 || due || blocked != "EXECUTION_GRAPH_INCOMPLETE" {
		t.Fatalf("incomplete graph: items=%d due=%t blocked=%q; want one parked item, blocked", items, due, blocked)
	}
	if result, err := settler.RunPass(ctx); err != nil || result.Deferred != 0 || result.Settled != 0 {
		t.Fatalf("a parked settle item was picked up: %+v, %v", result, err)
	}

	// Live stage work: a settle defers on it, consumes the item and clears the
	// block.
	claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync)
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "deferred" {
		t.Fatalf("settlement on live work = %q, %v; want deferred", status, err)
	}
	if items, _, blocked := settleLiveness(t, db, root); items != 0 || blocked != "" {
		t.Fatalf("defer on live work: items=%d blocked=%q; want consumed, unblocked", items, blocked)
	}

	// The stage work ends without writing its owner (a writer-less terminal
	// failure): its exit still wakes the root at once.
	if _, err := owner.ExecContext(ctx, `SELECT work_item_close($1,'failed')`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if items, due, _ := settleLiveness(t, db, root); items != 1 || !due {
		t.Fatalf("stage exit: items=%d due=%t; want one due settle item", items, due)
	}

	// The settle item is now the root's only work: removing it leaves a fresh,
	// due one in its place.
	var removed string
	if err := owner.QueryRowContext(ctx, `DELETE FROM work_items WHERE owner_type='acquisition' AND owner_id=$1 AND kind='acquisition_settle' RETURNING id`, root).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	var fresh string
	if err := db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE owner_type='acquisition' AND owner_id=$1 AND kind='acquisition_settle' AND available_at<=clock_timestamp()`, root).Scan(&fresh); err != nil || fresh == removed {
		t.Fatalf("removing the last work of a running root left settle item %q (removed %q), %v; want a fresh due one", fresh, removed, err)
	}
}

// settleLiveness reads the root's settle items, whether one is due and the
// settlement block code.
func settleLiveness(t *testing.T, db *sql.DB, root string) (items int, due bool, blocked string) {
	t.Helper()
	var code sql.NullString
	if err := db.QueryRow(`SELECT count(w.id), COALESCE(bool_or(w.available_at<=clock_timestamp()),false), r.settlement_blocked_code
FROM acquisitions r LEFT JOIN work_items w ON w.owner_type='acquisition' AND w.owner_id=r.id AND w.kind='acquisition_settle'
WHERE r.id=$1 GROUP BY r.settlement_blocked_code`, root).Scan(&items, &due, &code); err != nil {
		t.Fatal(err)
	}
	return items, due, code.String
}
