package application

import (
	"context"
	"database/sql"
	"os"
	"testing"

	acquisitionactivity "bansoko/internal/acquisition/terminal/activity"
	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/testutil/disposabledb"
)

// The file result reaches the run log through the independent Activity
// consumer, not the finish owner transaction. Success and failure each write one readable entry, a writer
// failure leaves the committed delivery untouched and is retried, and a
// retired owner gets no entry.

type fileLogEntry struct {
	kind, outcome, reason, summary, correlation string
}

func fileLogEntries(t *testing.T, db *sql.DB) []fileLogEntry {
	t.Helper()
	rows, err := db.Query(`SELECT kind,outcome,COALESCE(reason_code,''),summary,COALESCE(correlation_id,'') FROM subscription_log_entries WHERE kind IN ('file_alignment_completed','file_alignment_failed') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []fileLogEntry
	for rows.Next() {
		var e fileLogEntry
		if err := rows.Scan(&e.kind, &e.outcome, &e.reason, &e.summary, &e.correlation); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func activityConsumer(t *testing.T, x typedRuntimeFixture) *acquisitionactivity.Consumer {
	t.Helper()
	recorder := wireRealDiagnostics(t, x.db, x.s)
	return &acquisitionactivity.Consumer{DB: x.db, Writer: recorder, Diagnostics: recorder}
}

// asSubscriptionRoot turns the fixture's one-shot root into an automatic
// subscription root after the fact. The shared acquisition fixture only builds
// one-shot roots, and one-shot roots get no subscription log by contract
// (the one-shot path never fabricates subscription logs); the consumer reads nothing else of the
// entry kind, so this test-only owner rewrite is the smallest honest arrangement.
func asSubscriptionRoot(t *testing.T, x typedRuntimeFixture) {
	t.Helper()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	for _, stmt := range []string{
		`ALTER TABLE acquisitions DISABLE TRIGGER USER`,
		`UPDATE acquisitions SET entry_kind='automatic', request_namespace='auto_acquire_v1' WHERE id='` + acquisitionOf(t, x.db, x.opID) + `'`,
		`ALTER TABLE acquisitions ENABLE TRIGGER USER`,
	} {
		if _, err := owner.Exec(stmt); err != nil {
			t.Fatalf("arrange subscription root: %v", err)
		}
	}
}

func acquisitionOf(t *testing.T, db *sql.DB, operationID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT acquisition_id FROM file_operations WHERE id=$1`, operationID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func activityEpisodes(t *testing.T, db *sql.DB) (open, total int) {
	t.Helper()
	if err := db.QueryRow(`
SELECT count(*) FILTER (WHERE succeeded_after_at IS NULL), count(*) FROM diagnostic_episodes
WHERE component='activity' AND operation='consume' AND error_code='activity_consume_failed' AND scope_kind='global'`).Scan(&open, &total); err != nil {
		t.Fatal(err)
	}
	return open, total
}

// unloggedFacts counts the root's facts the consumer still has to log: the
// predicates of the consumer's partial indexes (PRUNE-046).
func unloggedFacts(t *testing.T, db *sql.DB, root string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`
SELECT (SELECT count(*) FROM download_tasks WHERE acquisition_id=$1 AND provider_correlation IS NOT NULL AND activity_submit_logged_at IS NULL)
     + (SELECT count(*) FROM download_tasks WHERE acquisition_id=$1 AND state IN ('completed','failed') AND activity_terminal_logged_at IS NULL)
     + (SELECT count(*) FROM artifacts WHERE acquisition_id=$1 AND state='verified' AND activity_logged_at IS NULL)
     + (SELECT count(*) FROM acquisition_files WHERE acquisition_id=$1 AND outcome IS NOT NULL AND activity_logged_at IS NULL)
     + (SELECT count(*) FROM acquisitions WHERE id=$1 AND terminal_at IS NOT NULL AND activity_logged_at IS NULL)`, root).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func deliveryLogged(t *testing.T, db *sql.DB, root string) bool {
	t.Helper()
	var logged bool
	if err := db.QueryRow(`SELECT bool_and(activity_logged_at IS NOT NULL) FROM acquisition_files WHERE acquisition_id=$1 AND outcome IS NOT NULL`, root).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	return logged
}

func TestAcquisitionActivityFileResult(t *testing.T) {
	ctx := context.Background()

	t.Run("success_writes_entry", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
		}
		assertOperationCompleted(t, x.db, x.opID)
		asSubscriptionRoot(t, x)
		root := acquisitionOf(t, x.db, x.opID)
		if unloggedFacts(t, x.db, root) == 0 || deliveryLogged(t, x.db, root) {
			t.Fatal("committed facts are already marked logged before any pass")
		}
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := fileLogEntries(t, x.db)
		want := fileLogEntry{"file_alignment_completed", "success", "file_alignment_completed", "文件已入库：" + baseOf(x.target), root}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("file log entries = %+v, want [%+v]", got, want)
		}
		// The entry is one step of the download run: linked to its Release and
		// carrying the run context and the file facts (log readability, 10-21).
		var releaseLinked bool
		var fileName, groupName string
		var sizeBytes sql.NullInt64
		if err := x.db.QueryRow(`
SELECT release_id IS NOT NULL, COALESCE(details->>'file_name', ''), COALESCE(details->>'group_name', ''), (details->>'size_bytes')::bigint
FROM subscription_log_entries WHERE kind = 'file_alignment_completed'`).Scan(&releaseLinked, &fileName, &groupName, &sizeBytes); err != nil {
			t.Fatal(err)
		}
		if !releaseLinked || fileName != baseOf(x.target) || groupName == "" || !sizeBytes.Valid {
			t.Fatalf("file entry run facts: release=%v file=%q group=%q size=%v", releaseLinked, fileName, groupName, sizeBytes)
		}
		// Every logged fact left the scan with its entry.
		if n := unloggedFacts(t, x.db, root); n != 0 || !deliveryLogged(t, x.db, root) {
			t.Fatalf("after the pass %d facts are unlogged (delivery logged=%v)", n, deliveryLogged(t, x.db, root))
		}
		again, err := consumer.ConsumeOnce(ctx)
		if err != nil || again.Written != 0 || again.Skipped != 0 || len(fileLogEntries(t, x.db)) != 1 {
			t.Fatalf("repeated pass = %+v, %v; want nothing read", again, err)
		}
	})

	t.Run("settled_root_enters_and_leaves_once", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
		}
		asSubscriptionRoot(t, x)
		root := acquisitionOf(t, x.db, x.opID)
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		settler, err := settlement.NewSettlementService(x.db)
		if err != nil {
			t.Fatal(err)
		}
		if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
			t.Fatalf("settle root: status=%q err=%v", status, err)
		}
		var state string
		var completionOpen bool
		if err := x.db.QueryRow(`SELECT state, completion_open FROM acquisitions WHERE id=$1`, root).Scan(&state, &completionOpen); err != nil {
			t.Fatal(err)
		}
		// acquisition_write_terminal opens the completion scan for a
		// succeeded download root.
		if state != "succeeded" || !completionOpen {
			t.Fatalf("settled root state=%s completion_open=%v, want succeeded/true", state, completionOpen)
		}
		if n := unloggedFacts(t, x.db, root); n != 1 {
			t.Fatalf("unlogged facts after settle = %d, want the root's terminal state", n)
		}
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var settled int
		if err := x.db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE dedupe_key=$1`, "acquisition:settled:"+root).Scan(&settled); err != nil || settled != 1 {
			t.Fatalf("settled entries = %d (%v), want 1", settled, err)
		}
		if n := unloggedFacts(t, x.db, root); n != 0 {
			t.Fatalf("unlogged facts after the settled pass = %d", n)
		}
	})

	t.Run("failure_writes_entry_and_admits_notification", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		if err := os.Remove(x.source); err != nil {
			t.Fatal(err)
		}
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed failure run: handled=%v err=%v", handled, err)
		}
		if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "SOURCE_MISSING" {
			t.Fatalf("operation not failed: %+v", got)
		}
		// 221: the failure command admitted its notification in the same
		// transaction, with no current-owner binding.
		var admissions int
		var managed sql.NullString
		var outcome, event string
		if err := x.db.QueryRow(`SELECT count(*) OVER (), managed_anime_id, payload->>'result.outcome', payload->>'event.type' FROM notification_admissions WHERE trigger_kind='file_operation_failed' AND source_owner_type='file_operation' AND source_owner_id=$1`, x.opID).Scan(&admissions, &managed, &outcome, &event); err != nil {
			t.Fatalf("failure notification admission: %v", err)
		}
		if admissions != 1 || managed.Valid || outcome != "failure" || event != "file_operation_failed" {
			t.Fatalf("failure admission count=%d managed=%v outcome=%q event=%q", admissions, managed, outcome, event)
		}
		asSubscriptionRoot(t, x)
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := fileLogEntries(t, x.db)
		want := fileLogEntry{"file_alignment_failed", "failure", "source_missing", "文件入库失败：source.mkv", acquisitionOf(t, x.db, x.opID)}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("file log entries = %+v, want [%+v]", got, want)
		}
	})

	t.Run("writer_failure_keeps_delivery_and_retries", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
		}
		asSubscriptionRoot(t, x)
		owner := disposabledb.Owner(t)
		defer owner.Close()
		if _, err := owner.Exec(`CREATE FUNCTION reject_file_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected activity failure'; END $$;
CREATE TRIGGER reject_file_activity BEFORE INSERT ON subscription_log_entries FOR EACH ROW WHEN (NEW.kind='file_alignment_completed') EXECUTE FUNCTION reject_file_activity()`); err != nil {
			t.Fatal(err)
		}
		result, err := consumer.ConsumeOnce(ctx)
		if err == nil || result.Failed == 0 {
			t.Fatalf("injected writer failure pass = %+v, %v; want a failure", result, err)
		}
		assertOperationCompleted(t, x.db, x.opID)
		if d := x.delivery(t, "primary"); d.outcome != "succeeded" {
			t.Fatalf("delivery result after Activity failure = %+v", d)
		}
		if len(fileLogEntries(t, x.db)) != 0 {
			t.Fatal("failed Activity write left an entry")
		}
		root := acquisitionOf(t, x.db, x.opID)
		if deliveryLogged(t, x.db, root) {
			t.Fatal("a rolled-back entry marked its delivery result logged")
		}
		if open, total := activityEpisodes(t, x.db); open != 1 || total != 1 {
			t.Fatalf("activity diagnostic open=%d total=%d, want 1/1", open, total)
		}
		if _, err := owner.Exec(`DROP TRIGGER reject_file_activity ON subscription_log_entries; DROP FUNCTION reject_file_activity()`); err != nil {
			t.Fatal(err)
		}
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatalf("retry pass: %v", err)
		}
		if got := fileLogEntries(t, x.db); len(got) != 1 || got[0].kind != "file_alignment_completed" {
			t.Fatalf("retried file log entries = %+v", got)
		}
		if !deliveryLogged(t, x.db, root) || unloggedFacts(t, x.db, root) != 0 {
			t.Fatal("the retried entry did not mark its fact logged")
		}
		if open, total := activityEpisodes(t, x.db); open != 0 || total != 1 {
			t.Fatalf("activity diagnostic after retry open=%d total=%d, want 0/1", open, total)
		}
	})

	t.Run("retired_owner_is_skipped", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
		}
		asSubscriptionRoot(t, x)
		owner := disposabledb.Owner(t)
		defer owner.Close()
		if _, err := owner.Exec(`UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,retired_at=CURRENT_TIMESTAMP,retired_subscription_version=1,lifecycle_version=lifecycle_version+1 WHERE id=$1`, x.f.animeID); err != nil {
			t.Fatal(err)
		}
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := fileLogEntries(t, x.db); len(got) != 0 {
			t.Fatalf("retired owner got file entries %+v", got)
		}
		if deliveryLogged(t, x.db, acquisitionOf(t, x.db, x.opID)) {
			t.Fatal("a skipped retired-owner fact was marked logged")
		}
	})
}

func TestAcquisitionActivityOneShotHasNoSubscriptionLog(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, false)
	consumer := activityConsumer(t, x)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
	}
	result, err := consumer.ConsumeOnce(context.Background())
	if err != nil || result.Written != 0 {
		t.Fatalf("one-shot pass = %+v, %v; want nothing written", result, err)
	}
	var n int
	if err := x.db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE correlation_id=$1`, acquisitionOf(t, x.db, x.opID)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("one-shot root has %d run-log entries (%v)", n, err)
	}
	// One-shot facts have no scope: they are never logged, so never marked.
	if deliveryLogged(t, x.db, acquisitionOf(t, x.db, x.opID)) {
		t.Fatal("a one-shot fact was marked logged")
	}
}

func baseOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
