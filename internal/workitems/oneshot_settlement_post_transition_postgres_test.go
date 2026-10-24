package workitems

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/persistence/txscope"
)

// TestFinishOneShotWithResultPostTransitionCommitsHookWithSettlement proves
// the marker-first finish path can carry the post-transition hook that commits
// a durable follow-up row (the placement obligation and its continuation
// WorkItem). The hook must run in the SAME transaction, after the WorkItem
// terminal transition and the owner write, and see the terminal row.
func TestFinishOneShotWithResultPostTransitionCommitsHookWithSettlement(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-post-transition")
	ctx := context.Background()

	hookCalls := 0
	var sawTerminalItem bool
	err := fixture.store.FinishOneShotWithResultPostTransition(
		ctx, fixture.claim, fixture.lease,
		DispositionConfirmed, StateSucceeded, OwnerResult{State: StateSucceeded},
		oneShotSettlementOwnerWriter(fixture.foID),
		func(hookCtx context.Context, scope txscope.Tx) error {
			hookCalls++
			var state string
			if err := scope.SQLTx().QueryRowContext(hookCtx,
				`SELECT state FROM work_item_history WHERE id = $1`, fixture.itemID).Scan(&state); err != nil {
				return err
			}
			sawTerminalItem = state == StateSucceeded
			return nil
		},
	)
	if err != nil {
		t.Fatalf("one-shot finish with post-transition: %v", err)
	}
	if hookCalls != 1 || !sawTerminalItem {
		t.Fatalf("hook calls=%d sawTerminalItem=%v, want one call observing the terminal item", hookCalls, sawTerminalItem)
	}
	// The settlement half committed too: the lease is consumed and the owner
	// version advanced.
	marker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if marker.count != 0 || len(marker.leases) != 0 {
		t.Fatalf("consuming finish must drain the marker lease: %+v", marker)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != 2 {
		t.Fatalf("one-shot settlement owner version = %d, want 2", got)
	}
}

// TestFinishOneShotPostTransitionHookFailureRollsBackSettlement proves a
// failing hook aborts the whole one-shot settlement: nothing may commit —
// neither the marker lease consumption, nor the owner version, nor the
// terminal WorkItem — so the follow-up stage can be re-driven instead of being
// silently lost while the effect is already committed.
func TestFinishOneShotPostTransitionHookFailureRollsBackSettlement(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-post-transition-fail")
	ctx := context.Background()
	beforeMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	beforeOwnerVersion := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID)

	hookErr := errors.New("placement obligation insert failed")
	err := fixture.store.FinishOneShotWithResultPostTransition(
		ctx, fixture.claim, fixture.lease,
		DispositionConfirmed, StateSucceeded, OwnerResult{State: StateSucceeded},
		oneShotSettlementOwnerWriter(fixture.foID),
		func(context.Context, txscope.Tx) error { return hookErr },
	)
	if !errors.Is(err, hookErr) {
		t.Fatalf("failing post-transition hook = %v, want the hook error", err)
	}

	afterMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if afterMarker.state != beforeMarker.state || afterMarker.epoch != beforeMarker.epoch || afterMarker.version != beforeMarker.version {
		t.Fatalf("failed hook must not settle the marker: before=%+v after=%+v", beforeMarker, afterMarker)
	}
	assertOneShotSettlementLeaseUnchanged(t, afterMarker, beforeMarker)
	row := readWorkItem(t, fixture.db, fixture.itemID)
	if row.state != StateLeased || row.fencingToken != fixture.claim.FencingToken || row.terminalAt != nil {
		t.Fatalf("failed hook must leave the claim leased for reclaim: %+v", row)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != beforeOwnerVersion {
		t.Fatalf("failed hook rolled back the owner write, got version %d want %d", got, beforeOwnerVersion)
	}
}

// TestFinishOneShotPostTransitionRequiresHook pins the guard: the
// post-transition variant without a hook is a programming error, not a silent
// degradation to the plain settlement.
func TestFinishOneShotPostTransitionRequiresHook(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-post-transition-nil")
	err := fixture.store.FinishOneShotWithResultPostTransition(
		context.Background(), fixture.claim, fixture.lease,
		DispositionConfirmed, StateSucceeded, OwnerResult{State: StateSucceeded}, nil, nil,
	)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("nil post-transition hook = %v, want ErrInvalidSpec", err)
	}
}
