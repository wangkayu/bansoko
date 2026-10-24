package migrations

import (
	"context"
	"testing"
)

// A Release accepted again after its download failed owns a second
// download task while the failed one stays as a record; the Release still
// points at exactly one current task. The idempotency kind force_download is
// gone; release_retry records carry the release and the result only.
func TestReleaseOwnsTasksOverTime(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	for name, want := range map[string]bool{
		"download_tasks_release_id_key":   false,
		"download_tasks_release_owner_fk": false,
		"download_tasks_release_fk":       true,
		"releases_download_task_owner_fk": true,
		"idempotency_records_kind_check":  true,
		"idempotency_records_check":       true,
	} {
		var exists bool
		if err := owner.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname=$1)`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Errorf("constraint %s exists=%v, want %v", name, exists, want)
		}
	}

	seedAcceptableRelease(t, owner, "retry")
	first := validQueued(t, "acq-retry-1", "key-retry-1", "retry")
	if _, err := callQueuedAccept(t, owner, first); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, first.AcquisitionID); err != nil {
		t.Fatal(err)
	}

	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The retry detaches the failed task in its admission transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id=NULL WHERE id='rel-retry'`); err != nil {
		t.Fatal(err)
	}
	second := validQueued(t, "acq-retry-2", "key-retry-2", "retry")
	if _, err := callQueuedAccept(t, tx, second); err != nil {
		t.Fatalf("second accept of the same release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit a second task for the release: %v", err)
	}
	var tasks int
	var current string
	if err := owner.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM download_tasks WHERE release_id='rel-retry'), (SELECT download_task_id FROM releases WHERE id='rel-retry')`).Scan(&tasks, &current); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || current != second.TaskID {
		t.Fatalf("tasks=%d current=%s, want 2 tasks and the release on %s", tasks, current, second.TaskID)
	}

	// The current task must still belong to its Release.
	seedAcceptableRelease(t, owner, "other")
	if _, err := owner.ExecContext(ctx, `UPDATE releases SET download_task_id=$1 WHERE id='rel-other'`, second.TaskID); err == nil {
		t.Fatal("a release pointed at another release's task")
	}

	for _, tc := range []struct {
		name, statement string
		ok              bool
	}{
		{"release_retry", `INSERT INTO idempotency_records(kind,idempotency_key,request_hash,release_id,result_snapshot) VALUES('release_retry','k1','sha256:` + sixtyFourZeros + `','rel-retry','{}')`, true},
		{"release_retry_without_result", `INSERT INTO idempotency_records(kind,idempotency_key,request_hash,release_id) VALUES('release_retry','k3','sha256:` + sixtyFourZeros + `','rel-retry')`, false},
		{"force_download", `INSERT INTO idempotency_records(kind,idempotency_key,request_hash,release_id,result_snapshot) VALUES('force_download','k4','sha256:` + sixtyFourZeros + `','rel-retry','{}')`, false},
	} {
		_, err := owner.ExecContext(ctx, tc.statement)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

const sixtyFourZeros = "0000000000000000000000000000000000000000000000000000000000000000"
