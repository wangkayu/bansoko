package application

import (
	"context"
	"database/sql"
	"testing"
)

func verifyStepAuthorizationFacts(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	owner := ownerTestDB(t)
	var columns int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='file_operations' AND column_name IN ('publication_stage_temp_started_at','publication_reserve_version_started_at','publication_publish_target_started_at','publication_source_cleanup_started_at','publication_temp_cleanup_started_at','publication_closure_receipt_observed_at') AND is_nullable='YES'`).Scan(&columns); err != nil || columns != 6 {
		t.Fatalf("step authorization columns=%d err=%v", columns, err)
	}
	var valid bool
	if err := owner.QueryRowContext(ctx, `SELECT publication_publish_target_started_at IS NOT NULL AND publication_dispatch_started_at<=publication_publish_target_started_at AND publication_publish_target_started_at<=(publication_step_checkpoint->'publish_target'->>'observed_at')::timestamptz FROM file_operations WHERE publication_permit_id IS NOT NULL LIMIT 1`).Scan(&valid); err != nil || !valid {
		t.Fatalf("production StartStep authorization=%v err=%v", valid, err)
	}
}
