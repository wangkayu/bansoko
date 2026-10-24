package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

func (x typedRuntimeFixture) subtitleWorks(t *testing.T) int {
	t.Helper()
	var n int
	if err := x.db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation' AND owner_id=$1 AND kind='file_execute'`, x.subtile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Through the real entries.
func TestTypedCancelOrdering(t *testing.T) {
	t.Run("late_cancel_after_finish_keeps_handoff", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, true)
		c, err := x.store.ClaimDue(context.Background(), "typed-late-cancel", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := x.s.Handle(context.Background(), c); err != nil || out.OwnerResult.State != ownerStateCompleted {
			t.Fatalf("normal finish: %+v %v", out, err)
		}
		if x.subtitleWorks(t) != 1 {
			t.Fatal("normal finish did not hand off the subtitle")
		}
		if err := x.store.RequestCancel(context.Background(), c, "late_cancel"); !errors.Is(err, workitems.ErrStaleClaim) {
			t.Fatalf("a cancel after the finish must be refused: %v", err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded || x.subtitleWorks(t) != 1 {
			t.Fatalf("late cancel undid the finish/handoff: %+v subtitle works=%d", got, x.subtitleWorks(t))
		}
	})
	t.Run("cancel_before_finish_refuses_normal_finish", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, true)
		x.s.SetPlacementSink(&recordingPlacementSink{})
		injectPlacementObligationFailure(t)
		c, err := x.store.ClaimDue(context.Background(), "typed-cancel-first", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = x.s.Handle(context.Background(), c) // proof closes, finish rolls back
		removePlacementObligationFailure(t)
		if err := x.store.RequestCancel(context.Background(), c, "operator_cancel"); err != nil {
			t.Fatal(err)
		}
		// The same claim retries the normal finish: refused, zero write.
		before := planGraphSnapshot(t, x.db)
		if out, _ := x.s.Handle(context.Background(), c); out.OwnerResult.State == ownerStateCompleted {
			t.Fatalf("normal finish accepted after cancel: %+v", out)
		}
		if planGraphSnapshot(t, x.db) != before || x.subtitleWorks(t) != 0 {
			t.Fatal("refused normal finish wrote the graph or handed off")
		}
		// The cancel-won recovery completes the proven video and suppresses
		// the subtitle without work.
		if err := x.store.BlockRecovery(context.Background(), c, "cancel_requested"); err != nil {
			t.Fatal(err)
		}
		x.expireWork(t)
		rc, err := x.store.ClaimRecovery(context.Background(), []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := x.s.ObserveRecovery(context.Background(), rc); err != nil || out.OwnerResult.State != ownerStateCompleted {
			t.Fatalf("cancel-won recovery: %+v %v", out, err)
		}
		if got := x.state(t, x.subtile); got.opState != ownerStateFailed || got.opCode != "DEPENDENCY_HANDOFF_CANCELLED" || x.subtitleWorks(t) != 0 {
			t.Fatalf("subtitle not suppressed: %+v", got)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
			t.Fatalf("cancelled finish: %+v", got)
		}
	})
}
