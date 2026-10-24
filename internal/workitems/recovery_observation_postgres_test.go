package workitems

import (
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGAutomaticRecoveryClaimSettlesNoEffectOwner(t *testing.T) {
	db := openTestDB(t)
	s := newTestStore(t, db)
	ctx := context.Background()
	source := createSourceOwner(t, db, "auto-recovery")
	id := createRefreshItem(t, s, source, "auto-effect", 1)
	old, err := s.ClaimDue(ctx, "old", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDue(ctx, "ordinary", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
		t.Fatal(err)
	}
	c, err := s.ClaimRecovery(ctx, []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c.Attempt != 1 || c.IdempotencyKey != "auto-effect" || c.FencingToken <= old.FencingToken {
		t.Fatalf("observation mutated business budget/identity: %+v", c)
	}
	if err := s.SettleRecovery(ctx, c, StateFailed, OwnerResult{State: StateFailed, ReasonCode: "WORKER_EXECUTION_STOPPED"}, DispositionNoExternalEffect, runtimeOwnerWriter(source, c.ExpectedOwnerVersion), nil); err != nil {
		t.Fatal(err)
	}
	row := readWorkItem(t, db, id)
	version, health := readRuntimeVersion(t, db, source)
	if row.state != StateFailed || row.attempt != 1 || version != 2 || health != "failed" {
		t.Fatalf("not atomic: %+v %d %s", row, version, health)
	}
}

func TestPGAutomaticUnknownRecoveryDefersWithoutBusinessAttempt(t *testing.T) {
	f := newOneShotSettlementFixture(t, "auto-unknown")
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
	before := readOneShotSettlementMarker(t, f.db, f.markerID)
	if err := f.store.DeferRecovery(ctx, c, "recovery_effect_unknown", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimRecovery(ctx, []string{KindFileExecute}, time.Minute); !errors.Is(err, ErrNoDueWork) {
		t.Fatalf("ignored database observation backoff: %v", err)
	}
	after := readOneShotSettlementMarker(t, f.db, f.markerID)
	row := readWorkItem(t, f.db, f.itemID)
	if before.count != 1 || after.count != 1 || row.attempt != 1 || row.terminalAt != nil || row.state != StateAvailable {
		t.Fatalf("unknown evidence lost: marker=%+v row=%+v", after, row)
	}
	var reason string
	if err := f.db.QueryRow(`SELECT last_error_code FROM work_item_history WHERE id=$1`, f.itemID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "recovery_effect_unknown" {
		t.Fatal(reason)
	}
}

func TestPGAutomaticOneShotRecoverySettlesExistingLedgerAtomically(t *testing.T) {
	f := newOneShotSettlementFixture(t, "auto-confirmed")
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
	if err := f.store.SettleRecovery(ctx, c, StateFailed, OwnerResult{State: "failed", ReasonCode: "TEST_CONFIRMED_EFFECT"}, DispositionConfirmed, func(ctx context.Context, scope txscope.Tx, result OwnerResult) (int64, error) {
		var version int64
		err := scope.SQLTx().QueryRowContext(ctx, `UPDATE file_operations SET state='failed',failure_code=$2,version=version+1 WHERE id=$1 AND version=$3 RETURNING version`, f.foID, result.ReasonCode, c.ExpectedOwnerVersion).Scan(&version)
		return version, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	marker := readOneShotSettlementMarker(t, f.db, f.markerID)
	row := readWorkItem(t, f.db, f.itemID)
	if marker.count != 0 || len(marker.leases) != 0 || row.state != StateFailed || row.attempt != 1 {
		t.Fatalf("marker/owner/work not settled: marker=%+v row=%+v", marker, row)
	}
	if err := f.store.SettleRecovery(ctx, c, StateFailed, OwnerResult{State: "failed"}, DispositionConfirmed, runtimeOwnerWriter("unused", 1), nil); err == nil {
		t.Fatal("double confirmed settlement accepted")
	}
}
