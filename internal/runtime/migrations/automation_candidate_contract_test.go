package migrations

import (
	"context"
	"testing"
)

// TestAutomationScheduleAndCandidateContract proves the installed schema carries
// exactly the four automation schedules, no replacement_scan anywhere, the
// candidate lifecycle constraints, and one working provider owner per entry.
func TestAutomationScheduleAndCandidateContract(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Verify(ctx); err != nil {
		t.Fatalf("verify installed schema: %v", err)
	}

	wantSchedules := []struct {
		kind     string
		interval int
		unit     string
	}{
		{"catalog_refresh", 12, "hour"},
		{"group_discovery", 6, "hour"},
		{"metadata_match", 24, "hour"},
		{"rss_refresh", 15, "minute"},
	}
	rows, err := db.QueryContext(ctx, `SELECT kind, interval_value, interval_unit FROM automation_schedules ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		kind, unit string
		interval   int
	}
	for rows.Next() {
		var item struct {
			kind, unit string
			interval   int
		}
		if err := rows.Scan(&item.kind, &item.interval, &item.unit); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(wantSchedules) {
		t.Fatalf("schedule count = %d, want %d (%v)", len(got), len(wantSchedules), got)
	}
	for i, want := range wantSchedules {
		if got[i].kind != want.kind || got[i].interval != want.interval || got[i].unit != want.unit {
			t.Fatalf("schedule[%d] = %+v, want %+v", i, got[i], want)
		}
	}

	var replacementSchedules int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_schedules WHERE kind = 'replacement_scan'`).Scan(&replacementSchedules); err != nil {
		t.Fatal(err)
	}
	if replacementSchedules != 0 {
		t.Fatalf("replacement_scan schedule rows = %d, want 0", replacementSchedules)
	}

	// The pending provider unique index is gone for good.
	var indexExists *string
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('anime_config_candidates_pending_provider_uq')::text`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if indexExists != nil {
		t.Fatalf("pending provider unique index still exists: %v", *indexExists)
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, draft, version)
VALUES ('cand-mikan-1', 'mikan', '4012', 'Show A', '{"origin":"mikan","origin_entry_ref":"4012","title":"Show A"}'::jsonb, 1)`); err != nil {
		t.Fatalf("working provider candidate must persist: %v", err)
	}
	expectReject := func(name, statement string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("%s: expected constraint violation, got success", name)
		}
	}
	expectReject("second working candidate for one provider entry",
		`INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, draft, version)
		 VALUES ('cand-mikan-2', 'mikan', '4012', 'Show A replanned', '{"origin":"mikan","origin_entry_ref":"4012","title":"Show A replanned"}'::jsonb, 1)`)
	// custom/oneshot rows must carry NULL entry ref and a label.
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, instance_label, draft, version)
VALUES ('cand-custom-1', 'custom', NULL, 'Preset A', 'Preset A', '{"origin":"custom","origin_entry_ref":null,"title":"Preset A","instance_label":"Preset A"}'::jsonb, 1),
       ('cand-oneshot-1', 'oneshot', NULL, 'One-shot Run', 'oneshot-1', '{"origin":"oneshot","origin_entry_ref":null,"title":"One-shot Run","instance_label":"oneshot-1"}'::jsonb, 1)`); err != nil {
		t.Fatalf("custom/oneshot candidates with null entry ref must persist: %v", err)
	}
	expectReject("custom candidate with an entry ref",
		`INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, instance_label, draft, version)
		 VALUES ('cand-custom-bad', 'custom', 'self-ref', 'Bad', 'Bad', '{}'::jsonb, 1)`)
	expectReject("oneshot candidate without instance label",
		`INSERT INTO anime_config_candidates(id, origin, origin_entry_ref, title, draft, version)
		 VALUES ('cand-oneshot-bad', 'oneshot', NULL, 'Bad', '{}'::jsonb, 1)`)
	expectReject("replacement_scan schedule insertion",
		`INSERT INTO automation_schedules(kind, enabled, interval_value, interval_unit, next_run_at)
		 VALUES ('replacement_scan', FALSE, 24, 'hour', NULL)`)

	// Live subscription shape: custom/oneshot entry ref must stay null.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-shape', 'Shape')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
VALUES ('sub-shape', 'anime-shape', 'custom', NULL, 'Custom', 'active')`); err != nil {
		t.Fatalf("custom subscription with null entry ref must persist: %v", err)
	}
	expectReject("custom subscription with an entry ref",
		`INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
		 VALUES ('sub-shape-2', 'anime-shape', 'custom', 'self-ref', 'Other', 'active')`)
}
