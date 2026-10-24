package migrations

import (
	"context"
	"testing"
)

func TestDownloadQueuedBudgetGuard(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, tag := range []string{"budget-guard-a", "budget-guard-b"} {
		seedAcceptableRelease(t, db, tag)
		if _, err := callQueuedAccept(t, db, validQueued(t, tag, "key-"+tag, tag)); err != nil {
			t.Fatal(err)
		}
	}
	var valid bool
	if err := db.QueryRowContext(ctx, `SELECT r.execution_branch='download' AND r.input_schema_version=1 AND t.stage_policy_version=(r.input->'limits'->>'policy_version')::integer AND t.submit_max_attempts=(r.input->'limits'->'submit'->>'max_attempts')::integer AND t.max_polls=(r.input->'limits'->'sync'->>'max_polls')::integer FROM download_tasks t JOIN acquisitions r ON r.id=t.acquisition_id WHERE t.id='dt-budget-guard-a'`).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("queued budget differs from accepted input")
	}
	cases := []struct{ name, query, code string }{
		{"frozen limit", `UPDATE download_tasks SET submit_max_attempts=submit_max_attempts+1 WHERE id='dt-budget-guard-a'`, "23514"},
		{"poll ordinal", `UPDATE download_tasks SET next_poll_ordinal=2 WHERE id='dt-budget-guard-a'`, "23514"},
		{"ungranted attempt index", `UPDATE download_tasks SET submit_attempt_index='[{}]' WHERE id='dt-budget-guard-a'`, "23514"},
		{"null task work root", `UPDATE work_items SET acquisition_id=NULL WHERE id='wi-sub-budget-guard-a'`, "23514"},
		{"cross root task work", `UPDATE work_items SET acquisition_id='budget-guard-b' WHERE id='wi-sub-budget-guard-a'`, "23514"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, tc.query); sqlState(err) != tc.code {
				t.Fatalf("guard error %v, want %s", err, tc.code)
			}
		})
	}
}
