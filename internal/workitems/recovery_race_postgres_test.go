package workitems

import (
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGRecoveryStaleFenceRollsBackOneShotLedgerConsumption(t *testing.T) {
	f := newOneShotSettlementFixture(t, "recovery-stale-ledger")
	ctx := context.Background()
	if err := f.store.BlockRecovery(ctx, f.claim, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, f.itemID); err != nil {
		t.Fatal(err)
	}
	c, err := f.store.ClaimRecovery(ctx, []string{KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET fencing_token=fencing_token+1,cancel_requested=true WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	err = f.store.SettleRecovery(ctx, c, StateFailed, OwnerResult{State: StateFailed}, DispositionConfirmed, func(context.Context, txscope.Tx, OwnerResult) (int64, error) {
		called = true
		return c.ExpectedOwnerVersion, nil
	}, nil)
	if !errors.Is(err, ErrStaleClaim) || called {
		t.Fatalf("stale recovery reached owner: %v called=%v", err, called)
	}
	marker := readOneShotSettlementMarker(t, f.db, f.markerID)
	row := readWorkItem(t, f.db, f.itemID)
	if marker.count != 1 || len(marker.leases) != 1 || row.state != StateLeased {
		t.Fatalf("stale/cancel race consumed evidence: %+v %+v", marker, row)
	}
}
