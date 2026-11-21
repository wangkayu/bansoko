package eventactions

import (
	"context"
	"os"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

func TestRecordReceiptsCurrentOperationPostgres(t *testing.T) {
	if os.Getenv("BANSOKO_TEST_DATABASE_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db := disposabledb.ResetDisposableSchema(t)
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	const root = "acq-task-rel-ep-1"
	const op = "op-rel-ep-1"
	animetest.SeedReleaseOwnerChain(t, db, "receipt-source")
	animetest.SeedCompletedFileOp(t, db, "receipt-source", 1, false, "failed")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
	 delivery_source_cleanup='not_required',delivery_reason_code='typed_publication_failed',
	 evidence_digest='sha256:'||repeat('0',64),operation_id=$2,receipt='{"reason_code":"typed_publication_failed","schema_version":1}',
	 delivered_at=clock_timestamp() WHERE acquisition_id=$1 AND file_ref='primary'`, root, op)
	exec(`INSERT INTO event_action_attempts (id,event_id,kind,binding_version,script,occurred_at,state,acquisition_id,source_id,source_version,process_started_at)
	 SELECT gen_random_uuid(),'receipt-submit','download_submitted',0,'record-download.sh',now(),'completed',acquisition_id,id,version,now()
	 FROM download_tasks WHERE acquisition_id=$1`, root)
	addReceipt := func(id string, started bool, summary *string) {
		t.Helper()
		exec(`INSERT INTO event_action_attempts (id,event_id,kind,binding_version,script,occurred_at,state,acquisition_id,source_id,source_version,process_started_at,script_summary,exit_code,http_status)
	 SELECT gen_random_uuid(),$2,'file_operation_failed',0,'report.sh',clock_timestamp(),'failed',acquisition_id,id,version,
	 CASE WHEN $3 THEN now() END,$4::text,CASE WHEN $3 THEN 1 END,CASE WHEN $4::text IS NOT NULL THEN 503 END
	 FROM file_operations WHERE id=$1`, op, id, started, summary)
	}
	check := func(expectFile bool, summary string) {
		t.Helper()
		rows, err := New(db, nil, nil).RecordReceipts(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		var submit, file *RecordReceipt
		for i := range rows {
			if rows[i].FileRef == nil {
				submit = &rows[i]
			} else if *rows[i].FileRef == "primary" {
				file = &rows[i]
			}
		}
		if submit == nil || submit.Script != "record-download.sh" || submit.Summary != nil || (file != nil) != expectFile {
			t.Fatalf("unexpected receipts: %+v", rows)
		}
		if file == nil {
			return
		}
		if summary == "" {
			if file.Summary != nil || file.ExitCode != nil || file.HTTPStatus != nil {
				t.Fatalf("trigger-only receipt included a response: %+v", file)
			}
		} else if file.Summary == nil || *file.Summary != summary || file.ExitCode == nil || *file.ExitCode != 1 || file.HTTPStatus == nil || *file.HTTPStatus != 503 {
			t.Fatalf("wrong response: %+v", file)
		}
	}
	first := "Initial response"
	addReceipt("receipt-old", true, &first)
	check(true, first)
	// Retry reuses this operation's ID but advances its version. No stale response
	// may surface while retrying, when it did not start, or when it didn't report.
	exec(`UPDATE file_operations SET version=version+1 WHERE id=$1`, op)
	check(false, "")
	addReceipt("receipt-skipped", false, nil)
	check(false, "")
	exec(`UPDATE file_operations SET version=version+1 WHERE id=$1`, op)
	addReceipt("receipt-unreported", true, nil)
	check(true, "")
	exec(`UPDATE file_operations SET version=version+1 WHERE id=$1`, op)
	latest := "Latest response"
	addReceipt("receipt-latest", true, &latest)
	check(true, latest)
	rows, err := New(db, nil, nil).RecordReceipts(ctx, "other-root")
	if err != nil || len(rows) != 0 {
		t.Fatalf("unrelated record receipts: %+v %v", rows, err)
	}
}
