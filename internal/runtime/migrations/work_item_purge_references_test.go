package migrations

import (
	"context"
	"testing"

	"bansoko/internal/testutil/disposabledb/guard"
)

// TestWorkItemPurgeRemovesOnlyExpiredOutcomes: retention purge deletes expired
// terminal outcomes (except one-shot evidence), leaves unexpired outcomes, and
// never touches the live queue.
func TestWorkItemPurgeRemovesOnlyExpiredOutcomes(t *testing.T) {
	runPlanAtomicCommit(t, false, false, false)
	owner := guard.MustDisposable(t)
	ctx := context.Background()

	var live int64
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_items`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live == 0 {
		t.Fatal("fixture has no live WorkItem")
	}
	// The fixture's own outcomes are fresh and stay.
	var before int64
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_item_outcomes`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Terminal copies of every live item: aged past retention, one-shot
	// evidence, and still-retained.
	for _, c := range []struct {
		suffix, snapshot, terminalAt string
	}{
		{"-expired", `input_snapshot`, `CURRENT_TIMESTAMP - interval '40 days'`},
		{"-oneshot", `jsonb_build_object('one_shot', true)`, `CURRENT_TIMESTAMP - interval '40 days'`},
		{"-retained", `input_snapshot`, `CURRENT_TIMESTAMP`},
	} {
		if _, err := owner.ExecContext(ctx, `INSERT INTO work_item_outcomes(id,owner_type,owner_id,kind,state,idempotency_key,max_attempts,input_snapshot,expected_owner_version,retention_days,acquisition_id,terminal_at,retention_until)
SELECT id||$1,owner_type,owner_id,kind,'succeeded',idempotency_key||$1,max_attempts,`+c.snapshot+`,expected_owner_version,retention_days,acquisition_id,`+c.terminalAt+`,`+c.terminalAt+` + retention_days * interval '1 day'
FROM work_items`, c.suffix); err != nil {
			t.Fatal(err)
		}
	}

	var expired int64
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_item_outcomes WHERE id LIKE '%-expired'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	var purged int64
	if err := owner.QueryRowContext(ctx, `SELECT public.work_item_purge_expired(500)`).Scan(&purged); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != expired {
		t.Fatalf("purged %d, want every expired outcome (%d)", purged, expired)
	}
	var kept, liveAfter int64
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_item_outcomes`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != before+2*live {
		t.Fatalf("outcomes kept = %d, want one-shot evidence and retained outcomes (%d)", kept, before+2*live)
	}
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_items`).Scan(&liveAfter); err != nil || liveAfter != live {
		t.Fatalf("live queue after purge = %d, %v; want %d", liveAfter, err, live)
	}
	if err := owner.QueryRowContext(ctx, `SELECT public.work_item_purge_expired(500)`).Scan(&purged); err != nil || purged != 0 {
		t.Fatalf("second purge = %d, %v; want 0, nil", purged, err)
	}
}
