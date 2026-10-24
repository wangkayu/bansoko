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
	for index, identity := range observabilityport.DiagnosticIdentities() {
		scopeID := fmt.Sprintf("catalog-%d", index)
		if identity.ScopeKind == observabilityport.ScopeGlobal {
			scopeID = "global"
		}
		for _, severity := range []observabilityport.DiagnosticSeverity{observabilityport.SeverityWarning, observabilityport.SeverityError} {
			if err := service.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{Identity: identity, Severity: severity, ScopeID: scopeID, LastError: "catalog conformance"}); err != nil {
				t.Fatalf("insert catalog identity %s/%s/%s/%s severity=%s: %v", identity.Component, identity.Operation, identity.ErrorCode, identity.ScopeKind, severity, err)
			}
			if err := service.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Identity: identity, ScopeID: scopeID}); err != nil {
				t.Fatalf("close catalog identity: %v", err)
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
	// Every catalog identity above must be accepted by PostgreSQL. Removing a
	// constructor-covered map entry therefore makes this test red; adding a Go
	// scope absent from the schema fails the matching real INSERT.
	unknownScope := observabilityport.DiagnosticFailureRequest{Identity: observabilityport.ReplacementTriggerFailure(), Severity: observabilityport.SeverityError, ScopeID: "catalog-negative", LastError: "negative"}
	managedAnimeScope := observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.ReplacementTriggerFailure(), Severity: observabilityport.SeverityError,
		ScopeID: "catalog-negative", LastError: "negative",
	}
	managedAnimeScope.Identity.ScopeKind = "managed_anime"
	if err := validateDiagnosticFailure(managedAnimeScope); err == nil {
		t.Fatal("Go diagnostic catalog accepts managed_anime although the PostgreSQL scope CHECK rejects it")
	}
	unknownScope.Identity.ScopeKind = "unknown_scope"
	if err := service.RecordDiagnosticFailure(ctx, unknownScope); err == nil {
		t.Fatal("unknown scope accepted")
	}
	unknownOperation := observabilityport.ReplacementTriggerFailure()
	unknownOperation.Operation = observabilityport.DiagnosticOperation("unknown_operation")
	if err := service.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{Identity: unknownOperation, Severity: observabilityport.SeverityError, ScopeID: "catalog-negative", LastError: "negative"}); err == nil {
		t.Fatal("unknown operation accepted")
	}
	unknownCode := observabilityport.ReplacementTriggerFailure()
	unknownCode.ErrorCode = observabilityport.DiagnosticErrorCode("unknown_code")
	if err := service.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{Identity: unknownCode, Severity: observabilityport.SeverityError, ScopeID: "catalog-negative", LastError: "negative"}); err == nil {
		t.Fatal("unknown error code accepted")
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
		StableResultKey: "file_alignment_completed:operation-file-activity", Title: "File Activity Anime", Summary: "文件已入库",
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
		StableResultKey: "file_alignment_failed:file-kind-operation", Title: "File Kind Anime", Summary: "文件入库失败：file_alignment_failed",
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
		Title: "Activity Anime", Summary: "已应用完整的 BGM/TMDB 元数据",
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
		Title: "RSS Activity Anime", Summary: "发现 1 条新 RSS 内容",
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
		ReleaseID: "rss-activity-release", GroupRef: "Group A", Title: "RSS Activity Anime",
		Summary: "发布评估完成：accepted_pending_download",
		Details: []byte(`{"verdict":"accepted_pending_download","reason_code":"accepted_pending_download"}`),
	}
	appendRecord(releaseRecord)

	failureRecord := sourceRecord
	failureRecord.Kind = "rss_refresh_failed"
	failureRecord.Outcome = "failure"
	failureRecord.ReasonCode = "feed_locator_unsafe"
	failureRecord.SubjectVersion = 3
	failureRecord.StableResultKey = "failure-result"
	failureRecord.Summary = "RSS source refresh failed"
	failureRecord.Details = []byte(`{"error_count":1,"reason_code":"feed_locator_unsafe"}`)
	appendRecord(failureRecord)

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
	if newContentCount != 2 || sourceACount != 1 || sourceBCount != 1 || releaseCount != 1 || failureCount != 1 {
		t.Fatalf("unexpected RSS Activity counts: new=%d sourceA=%d sourceB=%d release=%d failure=%d",
			newContentCount, sourceACount, sourceBCount, releaseCount, failureCount)
	}

	var kind, reasonCode, subjectType, subjectID, rssSourceID, releaseID, groupRef, dedupeKey string
	var subjectVersion int64
	var details []byte
	if err := db.QueryRowContext(ctx, `
SELECT kind, reason_code, subject_type, subject_id, subject_version,
       COALESCE(rss_source_id, ''), COALESCE(release_id, ''), COALESCE(group_ref, ''), dedupe_key, details
FROM subscription_log_entries
WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_new_content'
  AND subject_id = 'rss-activity-source-a'`).Scan(
		&kind, &reasonCode, &subjectType, &subjectID, &subjectVersion,
		&rssSourceID, &releaseID, &groupRef, &dedupeKey, &details); err != nil {
		t.Fatal(err)
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

	if err := db.QueryRowContext(ctx, `
SELECT reason_code, subject_type, subject_id, subject_version, COALESCE(rss_source_id, ''), dedupe_key
FROM subscription_log_entries
WHERE subscription_id = 'rss-activity-subscription' AND kind = 'rss_refresh_failed'`).Scan(
		&reasonCode, &subjectType, &subjectID, &subjectVersion, &rssSourceID, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if reasonCode != "feed_locator_unsafe" || subjectType != "rss_source" || subjectID != "rss-activity-source-a" ||
		subjectVersion != 3 || rssSourceID != "rss-activity-source-a" ||
		dedupeKey != "rss_refresh_failed:rss_source:rss-activity-source-a:v3:failure-result" {
		t.Fatalf("unexpected failure Activity envelope: reason=%q subject=%s/%s/v%d source=%q dedupe=%q",
			reasonCode, subjectType, subjectID, subjectVersion, rssSourceID, dedupeKey)
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

	failure := observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.AutomationDispatchFailure(observabilityport.ScopeWorker),
		Severity: observabilityport.SeverityError, ScopeID: "rss_refresh", LastError: "first failure",
	}
	if err := service.RecordDiagnosticFailure(ctx, failure); err != nil {
		t.Fatalf("first diagnostic failure: %v", err)
	}
	failure.LastError = "second failure"
	if err := service.RecordDiagnosticFailure(ctx, failure); err != nil {
		t.Fatalf("repeated diagnostic failure: %v", err)
	}

	var episodeCount, occurrences int
	var succeededAfter any
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(occurrence_count), MAX(succeeded_after_at)
FROM diagnostic_episodes
WHERE component = 'automation' AND operation = 'dispatch'
  AND error_code = 'dispatch_failed' AND scope_kind = 'worker' AND scope_id = 'rss_refresh'`).Scan(&episodeCount, &occurrences, &succeededAfter); err != nil {
		t.Fatalf("read open diagnostic episode: %v", err)
	}
	if episodeCount != 1 || occurrences != 2 || succeededAfter != nil {
		t.Fatalf("open episode: count=%d occurrences=%d succeeded_after=%v", episodeCount, occurrences, succeededAfter)
	}

	if err := service.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{
		Identity: observabilityport.AutomationDispatchFailure(observabilityport.ScopeWorker), ScopeID: "rss_refresh",
	}); err != nil {
		t.Fatalf("close diagnostic episode: %v", err)
	}
	var closedCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM diagnostic_episodes
WHERE component = 'automation' AND operation = 'dispatch'
  AND error_code = 'dispatch_failed' AND scope_kind = 'worker' AND scope_id = 'rss_refresh'
  AND succeeded_after_at IS NOT NULL`).Scan(&closedCount); err != nil {
		t.Fatalf("read closed diagnostic episode: %v", err)
	}
	if closedCount != 1 {
		t.Fatalf("expected one closed diagnostic episode, got %d", closedCount)
	}
}

// TestWriterMasksUpstreamTitleFragmentsInsteadOfFailing proves that the
// Activity writer commits an upstream title carrying an alias slash, a URL, an
// absolute path or a token, storing the masked text instead of failing the
// caller's owner transaction.
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
			SubscriptionID: "title-subscription", ManagedAnimeID: "title-anime", Kind: "release_processed", Outcome: "info",
			ReasonCode: "submitted", SubjectType: "release", SubjectID: fmt.Sprintf("release-%d", index), SubjectVersion: 1,
			Title: tc.title, Summary: "下载任务已提交：" + tc.title,
		}
		if err := service.AppendTx(ctx, txscope.Wrap(tx), record); err != nil {
			_ = tx.Rollback()
			t.Fatalf("append %q: %v", tc.title, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var title, summary string
		if err := db.QueryRowContext(ctx, `
SELECT title, summary FROM subscription_log_entries WHERE subject_id = $1`, record.SubjectID).Scan(&title, &summary); err != nil {
			t.Fatal(err)
		}
		if title != tc.want || summary != "下载任务已提交："+tc.want {
			t.Fatalf("stored title=%q summary=%q, want %q", title, summary, tc.want)
		}
	}
}
