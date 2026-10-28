package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	acquisitionactivity "bansoko/internal/acquisition/terminal/activity"
	"bansoko/internal/acquisition/terminal/settlement"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/testutil/disposabledb"
)

// The file result reaches the run log through the independent Activity
// consumer, not the finish owner transaction. Success and failure each write one readable entry, a writer
// failure leaves the committed delivery untouched and is retried, and a
// retired owner gets no entry.

type fileLogEntry struct {
	kind, outcome, reason, message, fileName, correlation string
}

func fileLogEntries(t *testing.T, db *sql.DB) []fileLogEntry {
	t.Helper()
	rows, err := db.Query(`SELECT kind,outcome,COALESCE(reason_code,''),message_code,COALESCE(message_params->>'file_name',''),COALESCE(correlation_id,'') FROM subscription_log_entries WHERE kind IN ('file_alignment_completed','file_alignment_failed') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []fileLogEntry
	for rows.Next() {
		var e fileLogEntry
		if err := rows.Scan(&e.kind, &e.outcome, &e.reason, &e.message, &e.fileName, &e.correlation); err != nil {
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
// one-shot roots and these subtests exercise the subscription path (its
// settlement included); this test-only owner rewrite is the smallest honest
// arrangement.
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
		want := fileLogEntry{"file_alignment_completed", "success", "file_alignment_completed", "file_imported", baseOf(x.target), root}
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
		want := fileLogEntry{"file_alignment_failed", "failure", "source_missing", "file_import_failed", "source.mkv", acquisitionOf(t, x.db, x.opID)}
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

// TestAcquisitionActivityOneShotIsLoggedWithItsSource: a one-shot root is
// journalled like a subscription root, under its temporary anime and its
// oneshot-origin subscription, and the records feed labels the entries
// one_shot (read from the subscription's origin, not stored on the entry).
func TestAcquisitionActivityOneShotIsLoggedWithItsSource(t *testing.T) {
	ctx := context.Background()
	x := newTypedRuntimeFixture(t, nil, false)
	consumer := activityConsumer(t, x)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
	}
	root := acquisitionOf(t, x.db, x.opID)
	var subscriptionID string
	if err := x.db.QueryRow(`SELECT id FROM subscriptions WHERE managed_anime_id=$1`, x.f.animeID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	owner := disposabledb.Owner(t)
	defer owner.Close()
	for _, stmt := range []string{
		`ALTER TABLE subscriptions DISABLE TRIGGER USER`,
		`UPDATE subscriptions SET origin='oneshot', origin_entry_ref=NULL WHERE id='` + subscriptionID + `'`,
		`ALTER TABLE subscriptions ENABLE TRIGGER USER`,
	} {
		if _, err := owner.Exec(stmt); err != nil {
			t.Fatalf("arrange one-shot subscription: %v", err)
		}
	}
	result, err := consumer.ConsumeOnce(ctx)
	if err != nil || result.Written == 0 {
		t.Fatalf("one-shot pass = %+v, %v; want the root's facts written", result, err)
	}
	got := fileLogEntries(t, x.db)
	want := fileLogEntry{"file_alignment_completed", "success", "file_alignment_completed", "file_imported", baseOf(x.target), root}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("one-shot file log entries = %+v, want [%+v]", got, want)
	}
	if !deliveryLogged(t, x.db, root) || unloggedFacts(t, x.db, root) != 0 {
		t.Fatal("one-shot facts must be marked logged with their entries")
	}
	var scoped int
	if err := x.db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE correlation_id=$1 AND (subscription_id<>$2 OR managed_anime_id<>$3)`, root, subscriptionID, x.f.animeID).Scan(&scoped); err != nil || scoped != 0 {
		t.Fatalf("one-shot entries outside the temporary anime's scope: %d (%v)", scoped, err)
	}

	business := []string{"business"}
	page, err := x.recorderRecords(t, observabilityapplication.RecordQuery{Kind: &business})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) == 0 {
		t.Fatal("the records feed has no one-shot entries")
	}
	for _, entry := range page {
		if entry.Source != "one_shot" || entry.ManagedAnimeID != x.f.animeID {
			t.Fatalf("one-shot entry source=%q anime=%q, want one_shot/%s", entry.Source, entry.ManagedAnimeID, x.f.animeID)
		}
	}
}

type recordEntry struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	ManagedAnimeID string `json:"managed_anime_id"`
	MessageCode    string `json:"message_code"`
}

// recorderRecords lists business records through the observability read
// service and returns their entries.
func (x typedRuntimeFixture) recorderRecords(t *testing.T, query observabilityapplication.RecordQuery) ([]recordEntry, error) {
	t.Helper()
	service, err := observabilityapplication.NewService(x.db)
	if err != nil {
		return nil, err
	}
	page, err := service.ListRecords(context.Background(), query)
	if err != nil {
		return nil, err
	}
	var out []recordEntry
	for _, record := range page.Records {
		var envelope struct {
			Entry recordEntry `json:"entry"`
		}
		if err := json.Unmarshal(record.Raw, &envelope); err != nil {
			return nil, err
		}
		out = append(out, envelope.Entry)
	}
	return out, nil
}

func baseOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// replacementLine is one replacement_decided journal entry.
type replacementLine struct {
	subject, reason, message, cause, dedupe, release string
}

func replacementLines(t *testing.T, db *sql.DB) []replacementLine {
	t.Helper()
	rows, err := db.Query(`
SELECT COALESCE(subject_id,''), COALESCE(reason_code,''), message_code, COALESCE(details->>'reason_code',''), dedupe_key, COALESCE(release_id,'')
FROM subscription_log_entries WHERE kind='replacement_decided' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []replacementLine
	for rows.Next() {
		var line replacementLine
		if err := rows.Scan(&line.subject, &line.reason, &line.message, &line.cause, &line.dedupe, &line.release); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedPlannedReplacement freezes one pre-acquisition replace decision of the
// fixture root's (anime, Release): admitted, no candidate placement yet, the
// shape stale_failed_candidate_decisions stales when the root fails or is
// cancelled.
func seedPlannedReplacement(t *testing.T, x typedRuntimeFixture, id string) {
	t.Helper()
	root := acquisitionOf(t, x.db, x.opID)
	var anime, release string
	if err := x.db.QueryRow(`SELECT COALESCE(origin_managed_anime_ref,''), COALESCE(source_release_ref,'') FROM acquisitions WHERE id=$1`, root).Scan(&anime, &release); err != nil {
		t.Fatal(err)
	}
	if anime != x.f.animeID || release == "" {
		t.Fatalf("fixture root refs anime=%q release=%q; the stale trigger needs the origin anime and source Release", anime, release)
	}
	pools, _ := json.Marshal([]any{map[string]any{"pool_key": "tv:s1:e1", "verdict": "replace", "winner": map[string]any{"placement_id": "release:" + release, "version": 1, "fingerprint": "candidate"}}})
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.Exec(`
INSERT INTO replacement_decisions(id,idempotency_key,managed_anime_id,candidate_release_id,policy_version,profile_version,candidate_facts_ref,coverage_key,coverage_keys,admission_mode,pools,verdict,state,subscription_version,input_digest)
VALUES ($1,$1,$2,$3,1,'profile-v1','release:1','tv:s1:e1','["tv:s1:e1"]','single_copy',$4,'replace','admitted',1,'digest')`, id, anime, release, string(pools)); err != nil {
		t.Fatal(err)
	}
}

// failRootOnImport fails the fixture root the real way: the source is gone,
// the file operation fails and the settlement fails the root after its
// download completed.
func failRootOnImport(t *testing.T, x typedRuntimeFixture) string {
	t.Helper()
	if err := os.Remove(x.source); err != nil {
		t.Fatal(err)
	}
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed failure run: handled=%v err=%v", handled, err)
	}
	asSubscriptionRoot(t, x)
	root := acquisitionOf(t, x.db, x.opID)
	settler, err := settlement.NewSettlementService(x.db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(context.Background(), root); err != nil || status != "settled" {
		t.Fatalf("settle failed root: status=%q err=%v", status, err)
	}
	var state string
	if err := x.db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("root state=%q (%v), want failed", state, err)
	}
	return root
}

func ownerExec(t *testing.T, statements ...string) {
	t.Helper()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	for _, stmt := range statements {
		if _, err := owner.Exec(stmt); err != nil {
			t.Fatalf("arrange %q: %v", stmt, err)
		}
	}
}

// TestAcquisitionActivityNotReplaced: a planned replacement whose candidate
// acquisition fails or is cancelled gets exactly one not_replaced line, with
// the cause, in the settlement entry's transaction; it never gets a line that
// says the replacement will happen.
func TestAcquisitionActivityNotReplaced(t *testing.T) {
	ctx := context.Background()
	const decision = "decision-planned"
	assertOneNotReplaced := func(t *testing.T, x typedRuntimeFixture, cause string) {
		t.Helper()
		var state, staleReason string
		if err := x.db.QueryRow(`SELECT state, COALESCE(stale_reason,'') FROM replacement_decisions WHERE id=$1`, decision).Scan(&state, &staleReason); err != nil {
			t.Fatal(err)
		}
		if state != "stale" || staleReason != "candidate_acquisition_failed" {
			t.Fatalf("decision %s/%s, want stale by the failed candidate", state, staleReason)
		}
		want := replacementLine{decision, "not_replaced", "replacement_not_replaced", cause, "replacement:" + decision + ":not_replaced", "release-file-test"}
		if got := replacementLines(t, x.db); len(got) != 1 || got[0] != want {
			t.Fatalf("replacement lines = %+v, want [%+v]", got, want)
		}
	}
	run := func(t *testing.T, arrange func(t *testing.T, x typedRuntimeFixture, root string), cause string) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		seedPlannedReplacement(t, x, decision)
		if got := replacementLines(t, x.db); len(got) != 0 {
			t.Fatalf("a planned replacement wrote %+v before it ended", got)
		}
		root := failRootOnImport(t, x)
		if arrange != nil {
			arrange(t, x, root)
		}
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var settled int
		if err := x.db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE dedupe_key=$1`, "acquisition:settled:"+root).Scan(&settled); err != nil || settled != 1 {
			t.Fatalf("settled entries = %d (%v), want 1", settled, err)
		}
		assertOneNotReplaced(t, x, cause)
		if again, err := consumer.ConsumeOnce(ctx); err != nil || again.Written != 0 {
			t.Fatalf("repeated pass = %+v, %v; want nothing written", again, err)
		}
		assertOneNotReplaced(t, x, cause)
	}

	t.Run("import_failed", func(t *testing.T) {
		run(t, nil, "import_failed")
	})
	// The consumer tells the causes apart by the root's terminal state and
	// whether its download completed; the decision was staled by the real
	// terminal transition above, so these arrangements only rewrite what the
	// consumer reads.
	t.Run("download_failed", func(t *testing.T) {
		run(t, func(t *testing.T, x typedRuntimeFixture, root string) {
			ownerExec(t,
				`ALTER TABLE download_tasks DISABLE TRIGGER USER`,
				`UPDATE download_tasks SET state='failed', failure_code='QB_SYNC_FAILED' WHERE acquisition_id='`+root+`'`,
				`ALTER TABLE download_tasks ENABLE TRIGGER USER`)
		}, "download_failed")
	})
	t.Run("cancelled", func(t *testing.T) {
		run(t, func(t *testing.T, x typedRuntimeFixture, root string) {
			ownerExec(t,
				`ALTER TABLE acquisitions DISABLE TRIGGER USER`,
				`UPDATE acquisitions SET state='cancelled' WHERE id='`+root+`'`,
				`ALTER TABLE acquisitions ENABLE TRIGGER USER`)
		}, "cancelled")
	})

	// The line shares the settlement entry's transaction: when it cannot be
	// written, the settlement entry rolls back with it and both are retried;
	// the root's own settlement, committed before, is untouched.
	t.Run("rolls_back_with_the_settlement_entry", func(t *testing.T) {
		x := newTypedRuntimeFixture(t, nil, false)
		consumer := activityConsumer(t, x)
		seedPlannedReplacement(t, x, decision)
		root := failRootOnImport(t, x)
		ownerExec(t, `CREATE FUNCTION reject_replacement_line() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected replacement line failure'; END $$;
CREATE TRIGGER reject_replacement_line BEFORE INSERT ON subscription_log_entries FOR EACH ROW WHEN (NEW.kind='replacement_decided') EXECUTE FUNCTION reject_replacement_line()`)
		if result, err := consumer.ConsumeOnce(ctx); err == nil || result.Failed == 0 {
			t.Fatalf("injected line failure pass = %+v, %v; want a failure", result, err)
		}
		var settled int
		var logged bool
		if err := x.db.QueryRow(`SELECT (SELECT count(*) FROM subscription_log_entries WHERE dedupe_key=$1), activity_logged_at IS NOT NULL FROM acquisitions WHERE id=$2`, "acquisition:settled:"+root, root).Scan(&settled, &logged); err != nil {
			t.Fatal(err)
		}
		if settled != 0 || logged {
			t.Fatalf("settlement entry survived its failed line: entries=%d logged=%v", settled, logged)
		}
		var state string
		if err := x.db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil || state != "failed" {
			t.Fatalf("root state after the failed pass = %q (%v), want failed", state, err)
		}
		ownerExec(t, `DROP TRIGGER reject_replacement_line ON subscription_log_entries; DROP FUNCTION reject_replacement_line()`)
		if _, err := consumer.ConsumeOnce(ctx); err != nil {
			t.Fatalf("retry pass: %v", err)
		}
		if err := x.db.QueryRow(`SELECT count(*) FROM subscription_log_entries WHERE dedupe_key=$1`, "acquisition:settled:"+root).Scan(&settled); err != nil || settled != 1 {
			t.Fatalf("settled entries after retry = %d (%v), want 1", settled, err)
		}
		assertOneNotReplaced(t, x, "import_failed")
	})
}
