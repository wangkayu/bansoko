package application

import (
	"context"
	"fmt"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

func TestDiagnosticCatalogConformsToPostgres(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for index, spec := range observabilityport.ConditionCatalog() {
		targets := []string{fmt.Sprintf("catalog-%d", index)}
		if spec.Audience == observabilityport.AudienceInternal {
			targets = observabilityport.InternalSources()
		}
		for _, target := range targets {
			for _, severity := range []observabilityport.DiagnosticSeverity{observabilityport.SeverityWarning, observabilityport.SeverityError} {
				req := observabilityport.DiagnosticFailureRequest{Condition: spec.Condition, Severity: severity, TargetID: target, TargetName: "Catalog", AffectedID: "affected-1"}
				if err := service.RecordDiagnosticFailure(ctx, req); err != nil {
					t.Fatalf("insert catalog condition %+v target=%s severity=%s: %v", spec.Condition, target, severity, err)
				}
				if err := service.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Kind: spec.Kind, TargetID: target}); err != nil {
					t.Fatalf("close catalog condition: %v", err)
				}
			}
		}
	}
	for _, identity := range observabilityport.RoutineIdentities() {
		scopeID := "catalog-routine"
		if identity.ScopeKind == observabilityport.RoutineScopeGlobal {
			scopeID = "global"
		}
		if err := service.UpsertRoutine(ctx, observabilityport.RoutineRequest{Identity: identity, ScopeID: scopeID, Summary: "catalog conformance"}); err != nil {
			t.Fatalf("insert routine identity %s/%s/%s: %v", identity.Kind, identity.ScopeKind, identity.ResultCode, err)
		}
	}
	unknown := observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: "unknown_variant"},
		Severity:  observabilityport.SeverityError, TargetID: "tmdb",
	}
	if err := service.RecordDiagnosticFailure(ctx, unknown); err == nil {
		t.Fatal("uncatalogued variant accepted")
	}
}

func TestWriterPersistsFileAlignmentActivityEnvelopeAndDedupesReplay(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('file-activity-anime', 'File Activity Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('file-activity-subscription', 'file-activity-anime', 'custom', 'File Activity test', 'active')`); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	record := observabilityport.ActivityRecord{
		SubscriptionID: "file-activity-subscription", ManagedAnimeID: "file-activity-anime",
		Kind: "file_alignment_completed", Outcome: "success", ReasonCode: "file_alignment_completed",
		SubjectType: "file_operation", SubjectID: "operation-file-activity", SubjectVersion: 2,
		StableResultKey: "file_alignment_completed:operation-file-activity",
		MessageCode:     observabilityport.MessageFileImported, MessageParams: map[string]any{observabilityport.ParamFileName: "Show - 01.mkv"},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
		t.Fatal(err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	var kind, outcome, reasonCode, subjectType, subjectID, dedupeKey string
	var subjectVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MIN(kind), MIN(outcome), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version), MIN(dedupe_key)
FROM subscription_log_entries WHERE subscription_id = 'file-activity-subscription' AND kind = 'file_alignment_completed'`).Scan(
		&count, &kind, &outcome, &reasonCode, &subjectType, &subjectID, &subjectVersion, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if count != 1 || kind != "file_alignment_completed" || outcome != "success" || reasonCode != "file_alignment_completed" ||
		subjectType != "file_operation" || subjectID != "operation-file-activity" || subjectVersion != 2 ||
		dedupeKey != "file_alignment_completed:file_operation:operation-file-activity:v2:file_alignment_completed:operation-file-activity" {
		t.Fatalf("unexpected file Activity envelope: count=%d kind=%q outcome=%q reason=%q subject=%s/%s/v%d dedupe=%q",
			count, kind, outcome, reasonCode, subjectType, subjectID, subjectVersion, dedupeKey)
	}
}

func TestFileAlignmentWriterRequiresValidLegacyKind(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('file-kind-anime', 'File Kind Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('file-kind-subscription', 'file-kind-anime', 'custom', 'File Kind test', 'active')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
		SubscriptionID: "file-kind-subscription", ManagedAnimeID: "file-kind-anime",
		Kind: "file_alignment_failed", Outcome: "failure", ReasonCode: "file_alignment_failed",
		SubjectType: "file_operation", SubjectID: "file-kind-operation", SubjectVersion: 1,
		StableResultKey: "file_alignment_failed:file-kind-operation", MessageCode: observabilityport.MessageFileImportFailed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterPersistsMetadataActivityEnvelopeAndDedupesReplay(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('activity-anime', 'Activity Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('activity-subscription', 'activity-anime', 'custom', 'Activity test', 'active')`); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	record := observabilityport.ActivityRecord{
		SubscriptionID: "activity-subscription", ManagedAnimeID: "activity-anime", Kind: "metadata_applied", Outcome: "success",
		ReasonCode: "metadata_applied", SubjectType: "metadata", SubjectID: "activity-anime", SubjectVersion: 2,
		MessageCode: observabilityport.MessageMetadataApplied,
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
		t.Fatalf("append metadata activity: %v", err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
		t.Fatalf("replay metadata activity: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var count int
	var kind, outcome, reasonCode, subjectType, subjectID, dedupeKey string
	var subjectVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MIN(kind), MIN(outcome), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version), MIN(dedupe_key)
FROM subscription_log_entries
WHERE subscription_id = 'activity-subscription' AND kind = 'metadata_applied'`).Scan(
		&count, &kind, &outcome, &reasonCode, &subjectType, &subjectID, &subjectVersion, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if count != 1 || kind != "metadata_applied" || outcome != "success" || reasonCode != "metadata_applied" ||
		subjectType != "metadata" || subjectID != "activity-anime" || subjectVersion != 2 ||
		dedupeKey != "metadata_applied:metadata:activity-anime:v2:metadata_applied" {
		t.Fatalf("unexpected metadata Activity envelope: count=%d kind=%q outcome=%q reason=%q subject=%s/%s/v%d dedupe=%q",
			count, kind, outcome, reasonCode, subjectType, subjectID, subjectVersion, dedupeKey)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	record.SubjectVersion = 3
	if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
		t.Fatalf("append rollback candidate: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = 'activity-subscription' AND kind = 'metadata_applied'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rolled-back Activity must not persist, got %d rows", count)
	}
}

func TestWriterPersistsRSSAndReleaseActivityEnvelopeAcrossSources(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed Activity fixture: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('rss-activity-anime', 'RSS Activity Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('rss-activity-subscription', 'rss-activity-anime', 'custom', 'RSS Activity test', 'active')`)
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES
	('rss-activity-source-a', 'rss-activity-subscription', 'custom_rss', 'https://feeds.example.test/a.xml', 'https://feeds.example.test/a.xml', 'Group A', 'RSS Activity Anime'),
	('rss-activity-source-b', 'rss-activity-subscription', 'custom_rss', 'https://feeds.example.test/b.xml', 'https://feeds.example.test/b.xml', 'Group B', 'RSS Activity Anime')`)
	exec(`INSERT INTO releases(
	id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code
) VALUES ('rss-activity-release', 'rss-activity-source-a', 'guid:rss-activity-release',
	'{"title":"RSS Activity Anime - 01"}', '{"identity_kind":"guid","identity_value":"rss-activity-release"}',
	'discovered', 'discovered')`)

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	longGroupRef := strings.Repeat("g", 256)
	sourceRecord := observabilityport.ActivityRecord{
		SubscriptionID: "rss-activity-subscription", ManagedAnimeID: "rss-activity-anime",
		Kind: "rss_new_content", Outcome: "info", ReasonCode: "rss_new_content",
		SubjectType: "rss_source", SubjectID: "rss-activity-source-a", SubjectVersion: 2,
		StableResultKey: "same-result-key", RSSSourceID: "rss-activity-source-a", GroupRef: longGroupRef,
		MessageCode: observabilityport.MessageRSSNewContent, MessageParams: map[string]any{observabilityport.ParamCount: 1},
		Details: []byte(`{"item_count":1}`),
	}

	appendRecord := func(record observabilityport.ActivityRecord) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
			_ = tx.Rollback()
			t.Fatalf("append %s activity: %v", record.Kind, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	appendRecord(sourceRecord)
	appendRecord(sourceRecord)
	otherSourceRecord := sourceRecord
	otherSourceRecord.SubjectID = "rss-activity-source-b"
	otherSourceRecord.RSSSourceID = "rss-activity-source-b"
	otherSourceRecord.GroupRef = "Group B"
	appendRecord(otherSourceRecord)

	releaseRecord := observabilityport.ActivityRecord{
		SubscriptionID: "rss-activity-subscription", ManagedAnimeID: "rss-activity-anime",
		Kind: "release_processed", Outcome: "success", ReasonCode: "accepted_pending_download",
		SubjectType: "release", SubjectID: "rss-activity-release", SubjectVersion: 2,
		StableResultKey: "evaluator-v1:accepted_pending_download", RSSSourceID: "rss-activity-source-a",
		ReleaseID: "rss-activity-release", GroupRef: "Group A", MessageCode: observabilityport.MessageReleaseAccepted,
		Details: []byte(`{"verdict":"accepted_pending_download","reason_code":"accepted_pending_download"}`),
	}
	appendRecord(releaseRecord)

	// A refresh failure is a diagnostic condition, not a journal entry: the
	// retired kind is refused before any row is written.
	failureRecord := sourceRecord
	failureRecord.Kind = "rss_refresh_failed"
	failureRecord.Outcome = "failure"
	failureRecord.ReasonCode = "feed_locator_unsafe"
	failureRecord.SubjectVersion = 3
	failureRecord.StableResultKey = "failure-result"
	failureRecord.Details = []byte(`{"error_count":1,"reason_code":"feed_locator_unsafe"}`)
	failureTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AppendTx(ctx, txscope.Wrap(failureTx), failureRecord); err == nil {
		t.Fatal("the retired rss_refresh_failed kind must be refused")
	}
	_ = failureTx.Rollback()

	var newContentCount, sourceACount, sourceBCount, releaseCount, failureCount int
	if err := db.QueryRowContext(ctx, `
SELECT
	(SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_new_content'),
	(SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_new_content' AND subject_id = 'rss-activity-source-a'),
	(SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_new_content' AND subject_id = 'rss-activity-source-b'),
	(SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = 'rss-activity-subscription' AND kind = 'release_processed'),
	(SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_refresh_failed')`).Scan(
		&newContentCount, &sourceACount, &sourceBCount, &releaseCount, &failureCount); err != nil {
		t.Fatal(err)
	}
	if newContentCount != 2 || sourceACount != 1 || sourceBCount != 1 || releaseCount != 1 || failureCount != 0 {
		t.Fatalf("unexpected RSS Activity counts: new=%d sourceA=%d sourceB=%d release=%d failure=%d",
			newContentCount, sourceACount, sourceBCount, releaseCount, failureCount)
	}

	var kind, reasonCode, subjectType, subjectID, rssSourceID, releaseID, groupRef, dedupeKey, messageCode string
	var subjectVersion int64
	var details, messageParams []byte
	if err := db.QueryRowContext(ctx, `
SELECT kind, reason_code, subject_type, subject_id, subject_version,
       COALESCE(rss_source_id, ''), COALESCE(release_id, ''), COALESCE(group_ref, ''), dedupe_key, details,
       message_code, message_params
FROM subscription_log_entries
WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_new_content'
  AND subject_id = 'rss-activity-source-a'`).Scan(
		&kind, &reasonCode, &subjectType, &subjectID, &subjectVersion,
		&rssSourceID, &releaseID, &groupRef, &dedupeKey, &details, &messageCode, &messageParams); err != nil {
		t.Fatal(err)
	}
	if messageCode != "rss_new_content" || string(messageParams) != `{"count": 1}` {
		t.Fatalf("unexpected message: code=%q params=%s", messageCode, messageParams)
	}
	if kind != "rss_new_content" || reasonCode != "rss_new_content" || subjectType != "rss_source" ||
		subjectID != "rss-activity-source-a" || subjectVersion != 2 || rssSourceID != "rss-activity-source-a" ||
		releaseID != "" || groupRef != longGroupRef ||
		dedupeKey != "rss_new_content:rss_source:rss-activity-source-a:v2:same-result-key" ||
		!strings.Contains(string(details), `"item_count": 1`) {
		t.Fatalf("unexpected source Activity envelope: kind=%q reason=%q subject=%s/%s/v%d source=%q release=%q group_len=%d dedupe=%q details=%s",
			kind, reasonCode, subjectType, subjectID, subjectVersion, rssSourceID, releaseID, len(groupRef), dedupeKey, details)
	}

	if err := db.QueryRowContext(ctx, `
SELECT subject_type, subject_id, subject_version, reason_code,
       COALESCE(rss_source_id, ''), COALESCE(release_id, ''), COALESCE(group_ref, ''), dedupe_key
FROM subscription_log_entries
WHERE subscription_id = 'rss-activity-subscription' AND kind = 'release_processed'`).Scan(
		&subjectType, &subjectID, &subjectVersion, &reasonCode, &rssSourceID, &releaseID, &groupRef, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if subjectType != "release" || subjectID != "rss-activity-release" || subjectVersion != 2 ||
		reasonCode != "accepted_pending_download" || rssSourceID != "rss-activity-source-a" ||
		releaseID != "rss-activity-release" || groupRef != "Group A" ||
		dedupeKey != "release_processed:release:rss-activity-release:v2:evaluator-v1:accepted_pending_download" {
		t.Fatalf("unexpected release Activity envelope: subject=%s/%s/v%d reason=%q source=%q release=%q group=%q dedupe=%q",
			subjectType, subjectID, subjectVersion, reasonCode, rssSourceID, releaseID, groupRef, dedupeKey)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rollbackRecord := sourceRecord
	rollbackRecord.SubjectVersion = 9
	rollbackRecord.StableResultKey = "rollback-only"
	if err := service.AppendTx(ctx, txscope.Wrap(tx), rollbackRecord); err != nil {
		t.Fatalf("append rollback candidate: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = 'rss-activity-subscription' AND dedupe_key = 'rss_new_content:rss_source:rss-activity-source-a:v9:rollback-only'`).Scan(&newContentCount); err != nil {
		t.Fatal(err)
	}
	if newContentCount != 0 {
		t.Fatalf("rolled-back RSS Activity must not persist, got %d rows", newContentCount)
	}
}

func TestWriterPersistsRoutineAndDiagnosticEpisodeLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	routine := observabilityport.RoutineRequest{
		Identity: observabilityport.AutomationDispatchRoutine(observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultDispatchCompleted),
		ScopeID:  "rss_refresh", Summary: "first run",
	}
	if err := service.UpsertRoutine(ctx, routine); err != nil {
		t.Fatalf("insert routine: %v", err)
	}
	routine.Identity.Outcome = observabilityport.RoutineOutcomeWarning
	routine.Identity.ResultCode = observabilityport.RoutineResultDispatchPartial
	routine.Summary = "second run"
	if err := service.UpsertRoutine(ctx, routine); err != nil {
		t.Fatalf("update routine: %v", err)
	}

	var routineCount int
	var routineOutcome, routineSummary string
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(outcome), MAX(summary)
FROM routine_activities
WHERE kind = 'automation_dispatch' AND scope_kind = 'automation' AND scope_id = 'rss_refresh'`).Scan(&routineCount, &routineOutcome, &routineSummary); err != nil {
		t.Fatalf("read routine: %v", err)
	}
	if routineCount != 1 || routineOutcome != "warning" || routineSummary != "second run" {
		t.Fatalf("routine row: count=%d outcome=%q summary=%q", routineCount, routineOutcome, routineSummary)
	}

	failure := observabilityport.InternalFailure(observabilityport.InternalAutomationDispatch, "request-1")
	if err := service.RecordDiagnosticFailure(ctx, failure); err != nil {
		t.Fatalf("first diagnostic failure: %v", err)
	}
	if err := service.RecordDiagnosticFailure(ctx, failure); err != nil {
		t.Fatalf("repeated diagnostic failure: %v", err)
	}

	var episodeCount, occurrences int
	var succeededAfter any
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(occurrence_count), MAX(succeeded_after_at)
FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'automation.dispatch'`).Scan(&episodeCount, &occurrences, &succeededAfter); err != nil {
		t.Fatalf("read open diagnostic episode: %v", err)
	}
	if episodeCount != 1 || occurrences != 2 || succeededAfter != nil {
		t.Fatalf("open episode: count=%d occurrences=%d succeeded_after=%v", episodeCount, occurrences, succeededAfter)
	}

	if err := service.RecordDiagnosticSuccess(ctx, observabilityport.InternalSuccess(observabilityport.InternalAutomationDispatch)); err != nil {
		t.Fatalf("close diagnostic episode: %v", err)
	}
	var closedCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'automation.dispatch' AND succeeded_after_at IS NOT NULL`).Scan(&closedCount); err != nil {
		t.Fatalf("read closed diagnostic episode: %v", err)
	}
	if closedCount != 1 {
		t.Fatalf("expected one closed diagnostic episode, got %d", closedCount)
	}
	// A new failure after recovery opens a second episode on the same target.
	if err := service.RecordDiagnosticFailure(ctx, failure); err != nil {
		t.Fatalf("failure after recovery: %v", err)
	}
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostic_episodes WHERE kind = 'internal' AND target_id = 'automation.dispatch'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("episodes after recovery = %d, want closed history plus a new open one", total)
	}
}

// TestWriterMasksUpstreamTitleFragmentsInsteadOfFailing proves that the
// Activity writer commits an upstream name param carrying an alias slash, a
// URL, an absolute path or a token, storing the masked text instead of failing
// the caller's owner transaction.
func TestWriterMasksUpstreamTitleFragmentsInsteadOfFailing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('title-anime', 'Title Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('title-subscription', 'title-anime', 'custom', 'Title test', 'active')`); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ title, want string }{
		{realAniBTTitle, realAniBTTitle},
		{"[Group] Show - 01 http://tracker.example/announce", "[Group] Show - 01 <redacted>"},
		{"[Group] Show - 01 /home/alice/show.mkv", "[Group] Show - 01 <redacted>"},
		{"[Group] Show - 01 token=0123456789abcdef", "[Group] Show - 01 <redacted>"},
	}
	for index, tc := range cases {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		record := observabilityport.ActivityRecord{
			SubscriptionID: "title-subscription", ManagedAnimeID: "title-anime", Kind: "download_changed", Outcome: "info",
			ReasonCode: "download_submitted", SubjectType: "download_task", SubjectID: fmt.Sprintf("task-%d", index), SubjectVersion: 1,
			MessageCode: observabilityport.MessageDownloadSubmitted, MessageParams: map[string]any{observabilityport.ParamName: tc.title},
		}
		if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
			_ = tx.Rollback()
			t.Fatalf("append %q: %v", tc.title, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var name string
		if err := db.QueryRowContext(ctx, `
SELECT message_params->>'name' FROM subscription_log_entries WHERE subject_id = $1`, record.SubjectID).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != tc.want {
			t.Fatalf("stored name=%q, want %q", name, tc.want)
		}
	}
}
