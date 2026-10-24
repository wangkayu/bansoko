package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
)

func TestPatchScheduleUsesExactDatabaseInterval(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM automation_schedules WHERE kind = 'rss_refresh'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	patched, err := service.PatchSchedule(ctx, "rss_refresh", SchedulePatch{
		Enabled: true, IntervalValue: 5, IntervalUnit: "minute", ExpectedVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched.NextRunAt == nil {
		t.Fatal("enabled schedule must have next_run_at")
	}
	var exact bool
	if err := db.QueryRowContext(ctx, `
SELECT next_run_at = updated_at + INTERVAL '5 minutes'
FROM automation_schedules
WHERE kind = 'rss_refresh'`).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if !exact {
		t.Fatal("schedule patch did not derive next_run_at from its transaction database timestamp")
	}
}

// TestListSchedulesAttachesLatestRunOutcome proves each schedule carries the
// most recent dispatch run as last_run_outcome (DISTINCT ON kind, latest by
// created_at), and kinds with no run keep it null.
func TestListSchedulesAttachesLatestRunOutcome(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.RunNow(ctx, "rss_refresh", "outcome-run-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE automation_dispatch_runs
SET state = 'completed',
    started_at = created_at + INTERVAL '1 second',
    finished_at = created_at + INTERVAL '2 seconds'
WHERE id = $1`, first.RunID); err != nil {
		t.Fatal(err)
	}

	second, err := service.RunNow(ctx, "rss_refresh", "outcome-run-key-2")
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID == first.RunID {
		t.Fatal("second RunNow must create a new run")
	}

	schedules, err := service.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 4 {
		t.Fatalf("schedules = %d, want 4", len(schedules))
	}
	var found bool
	for _, schedule := range schedules {
		switch schedule.Kind {
		case "rss_refresh":
			found = true
			if schedule.LastRunOutcome == nil {
				t.Fatal("rss_refresh must expose last_run_outcome")
			}
			if schedule.LastRunOutcome.State != "queued" {
				t.Fatalf("latest outcome must be the queued second run, got %s", schedule.LastRunOutcome.State)
			}
			if schedule.LastRunOutcome.StartedAt != nil || schedule.LastRunOutcome.FinishedAt != nil {
				t.Fatalf("queued outcome must have no start/finish times, got %+v", schedule.LastRunOutcome)
			}
		case "group_discovery", "metadata_match", "catalog_refresh":
			if schedule.LastRunOutcome != nil {
				t.Fatalf("kind %s without runs must keep last_run_outcome null", schedule.Kind)
			}
		}
	}
	if !found {
		t.Fatal("rss_refresh schedule not returned")
	}
}

func TestRunNowIsIdempotentAndPreservesLiveRunConflict(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.RunNow(ctx, "metadata_match", "metadata-run-key-1")
	if err != nil {
		t.Fatalf("first RunNow: %v", err)
	}
	if first.RunID == "" || string(first.State) != "queued" {
		t.Fatalf("unexpected first run: %+v", first)
	}

	replay, err := service.RunNow(ctx, "metadata_match", "metadata-run-key-1")
	if err != nil {
		t.Fatalf("idempotent RunNow replay: %v", err)
	}
	if replay.RunID != first.RunID || replay.State != first.State {
		t.Fatalf("replay must return the original run: first=%+v replay=%+v", first, replay)
	}

	_, err = service.RunNow(ctx, "metadata_match", "metadata-run-key-2")
	var liveRunErr *apperrors.Business
	if !errors.As(err, &liveRunErr) || liveRunErr.Code != "LIVE_RUN_EXISTS" {
		t.Fatalf("different key must respect the live-run invariant, got %v", err)
	}

	var key string
	if err := db.QueryRowContext(ctx, `SELECT request_idempotency_key FROM automation_dispatch_runs WHERE id = $1`, first.RunID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "metadata-run-key-1" {
		t.Fatalf("request key was not persisted: %q", key)
	}
}

// TestRetiredKindRejectedFailClosed proves the retired replacement_scan kind
// and any unknown kind are rejected by the same allowlist used for schedule
// patch and manual run, and that the rejection creates no WorkItem and no
// dispatch-run row (P0.4: no fallback, no alias, no fourth schedule).
func TestRetiredKindRejectedFailClosed(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"replacement_scan", "unknown_kind"} {
		_, err := service.PatchSchedule(ctx, kind, SchedulePatch{
			Enabled: true, IntervalValue: 24, IntervalUnit: "hour", ExpectedVersion: 1,
		})
		var badReq *apperrors.BadRequest
		if !errors.As(err, &badReq) || badReq.Code != "AUTOMATION_KIND_INVALID" {
			t.Fatalf("PatchSchedule(%q) must reject with AUTOMATION_KIND_INVALID, got %v", kind, err)
		}

		_, err = service.RunNow(ctx, kind, "reject-key-"+kind)
		if !errors.As(err, &badReq) || badReq.Code != "AUTOMATION_KIND_INVALID" {
			t.Fatalf("RunNow(%q) must reject with AUTOMATION_KIND_INVALID, got %v", kind, err)
		}
	}

	// Rejection is fail-closed: no dispatch-run row was created for the
	// retired kind, and the schedule set still has exactly three kinds.
	var runs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_dispatch_runs WHERE kind = 'replacement_scan'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("retired kind must not create dispatch runs, got %d", runs)
	}
	var kinds []string
	rows, err := db.QueryContext(ctx, `SELECT kind FROM automation_schedules ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"catalog_refresh", "group_discovery", "metadata_match", "rss_refresh"}
	if len(kinds) != len(want) {
		t.Fatalf("schedule kinds = %v, want exactly %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("schedule kinds = %v, want exactly %v", kinds, want)
		}
	}
}
