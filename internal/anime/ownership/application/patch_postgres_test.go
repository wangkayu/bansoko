package application

import (
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPatchMetadataCASAndReevaluationScheduling(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-0006")
	animeID := subscribe.ManagedAnime.Id

	// A held release belonging to this anime.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-held', $1, 'key-1', '{}', '{}', 'waiting_metadata', 'metadata_incomplete')`, subscribe.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}

	stale, err := service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 99, Bgm: nil, Tmdb: nil,
	})
	var conflictErr *apperrors.VersionConflict
	if !errors.As(err, &conflictErr) || conflictErr.CurrentVersion != 1 {
		t.Fatalf("expected owner version conflict with current version 1, got %v (%+v)", err, stale)
	}

	season := 2
	updated, err := service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "bgm-999"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "tmdb-999", MediaKind: generated.TMDBMediaKindTv, Season: season},
	})
	if err != nil {
		t.Fatalf("metadata patch: %v", err)
	}
	if updated.Version != 2 || updated.Bgm == nil || updated.Tmdb == nil {
		t.Fatalf("metadata must be replaced with CAS: %+v", updated)
	}
	// Completing metadata schedules typed re-evaluation for the held release.
	var evalItems int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND kind = 'release_evaluate' AND owner_id = 'rel-held'`).Scan(&evalItems); err != nil {
		t.Fatal(err)
	}
	if evalItems != 1 {
		t.Fatalf("expected one release_evaluate work item, got %d", evalItems)
	}
	var logKind, logOutcome, logCode, logParams, dedupeKey string
	var logReasonCode, logSubjectType, logSubjectID string
	var logSubjectVersion int64
	var rssSourceID, releaseID, groupRef sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT kind, outcome, message_code, message_params::text, dedupe_key, reason_code, subject_type, subject_id, subject_version,
       rss_source_id, release_id, group_ref
FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribe.Subscription.Id).Scan(
		&logKind, &logOutcome, &logCode, &logParams, &dedupeKey, &logReasonCode, &logSubjectType, &logSubjectID, &logSubjectVersion,
		&rssSourceID, &releaseID, &groupRef); err != nil {
		t.Fatalf("read metadata application log: %v", err)
	}
	if logKind != "metadata_applied" || logOutcome != "success" || logCode != "metadata_applied" ||
		logParams != "{}" || logReasonCode != "metadata_applied" ||
		logSubjectType != "metadata" || logSubjectID != animeID || logSubjectVersion != 2 ||
		dedupeKey != "metadata_applied:metadata:"+animeID+":v2:metadata_applied" {
		t.Fatalf("unexpected metadata application log: kind=%s outcome=%s code=%s params=%s reason=%s subject=%s/%s/v%d dedupe=%s",
			logKind, logOutcome, logCode, logParams, logReasonCode, logSubjectType, logSubjectID, logSubjectVersion, dedupeKey)
	}
	if rssSourceID.Valid || releaseID.Valid || groupRef.Valid {
		t.Fatalf("metadata application log must not contain RSS facts: rss=%v release=%v group=%v", rssSourceID, releaseID, groupRef)
	}

	// Replacing the same complete value still follows the existing PATCH version
	// semantics, but it is not a new metadata application or evaluation request.
	repeated, err := service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 2,
		Bgm:             &generated.NullableBGMBinding{WorkId: "bgm-999"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "tmdb-999", MediaKind: generated.TMDBMediaKindTv, Season: season},
	})
	if err != nil {
		t.Fatalf("repeat metadata patch: %v", err)
	}
	if repeated.Version != 3 {
		t.Fatalf("repeat patch must retain existing version increment semantics: %+v", repeated)
	}
	var appliedLogs int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribe.Subscription.Id).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 {
		t.Fatalf("repeat complete metadata must not duplicate metadata_applied log, got %d", appliedLogs)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND kind = 'release_evaluate' AND owner_id = 'rel-held'`).Scan(&evalItems); err != nil {
		t.Fatal(err)
	}
	if evalItems != 1 {
		t.Fatalf("repeat complete metadata must not duplicate release evaluation, got %d", evalItems)
	}

	// Partial TMDB is rejected without touching any field or writing a log.
	_, err = service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 3,
		Bgm:             nil,
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "", MediaKind: generated.TMDBMediaKindTv, Season: 0},
	})
	var validationErr *apperrors.Validation
	if !errors.As(err, &validationErr) || validationErr.Code != "TMDB_BINDING_INCOMPLETE" {
		t.Fatalf("expected TMDB_BINDING_INCOMPLETE, got %v", err)
	}
	var storedVersion int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&storedVersion); err != nil {
		t.Fatal(err)
	}
	if storedVersion != 3 {
		t.Fatalf("rejected patch must not change the owner version, got %d", storedVersion)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribe.Subscription.Id).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 {
		t.Fatalf("rejected patch must not write metadata_applied log, got %d", appliedLogs)
	}
}

// TestPatchMetadataTitleOverrideSaveClearAndCAS covers the title_override
// field carried by the metadata patch: the title fixed at subscribe, setting,
// a JSON null keeping the fixed title (it is never cleared), the CAS
// conflict path, and that a title-only change does not fire metadata_applied
// re-evaluation (that path is BGM/TMDB completion only).

func TestPatchMetadataTitleOverrideSaveClearAndCAS(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-title-override")
	animeID := subscribe.ManagedAnime.Id

	// A held release so we can assert title-only changes do not re-evaluate.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-title', $1, 'key-title', '{}', '{}', 'waiting_metadata', 'metadata_incomplete')`, subscribe.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}

	// Subscribe fixes the title: title_override starts as the draft title.
	var subscribedTitle, subscribedOverride sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT title, title_override FROM managed_anime WHERE id = $1`, animeID).Scan(&subscribedTitle, &subscribedOverride); err != nil {
		t.Fatal(err)
	}
	if !subscribedOverride.Valid || subscribedOverride.String != subscribedTitle.String {
		t.Fatalf("subscribe must fix the title to the draft title %q, got %+v", subscribedTitle.String, subscribedOverride)
	}

	// Read the current metadata owner version (subscribe creates version 1).
	var metadataVersion int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&metadataVersion); err != nil {
		t.Fatalf("read metadata owner version: %v", err)
	}

	// Set a title override. A title-only change must not schedule release
	// evaluation or write a metadata_applied log.
	override := "自定义标题"
	patched, err := service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: metadataVersion,
		Bgm:             nil,
		Tmdb:            nil,
		TitleOverride:   &override,
	})
	if err != nil {
		t.Fatalf("set title override: %v", err)
	}
	if patched.TitleOverride == nil || *patched.TitleOverride != override {
		t.Fatalf("response must echo the title override, got %+v", patched.TitleOverride)
	}
	if patched.Version != metadataVersion+1 {
		t.Fatalf("metadata owner version must advance, got %d", patched.Version)
	}
	var storedOverride sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT title_override FROM managed_anime WHERE id = $1`, animeID).Scan(&storedOverride); err != nil {
		t.Fatal(err)
	}
	if !storedOverride.Valid || storedOverride.String != override {
		t.Fatalf("title_override must be persisted, got %+v", storedOverride)
	}
	// Title-only change must not schedule re-evaluation or write metadata_applied.
	var evalItems, appliedLogs int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND kind = 'release_evaluate' AND owner_id = 'rel-title'`).Scan(&evalItems); err != nil {
		t.Fatal(err)
	}
	if evalItems != 0 {
		t.Fatalf("title-only change must not schedule release evaluation, got %d", evalItems)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribe.Subscription.Id).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 0 {
		t.Fatalf("title-only change must not write metadata_applied log, got %d", appliedLogs)
	}

	// A JSON null (nil *string) keeps the fixed title; it is never cleared.
	cleared, err := service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: patched.Version,
		Bgm:             nil,
		Tmdb:            nil,
		TitleOverride:   nil,
	})
	if err != nil {
		t.Fatalf("null title override: %v", err)
	}
	if cleared.TitleOverride == nil || *cleared.TitleOverride != override {
		t.Fatalf("null must keep the fixed title in the response, got %+v", cleared.TitleOverride)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT title_override FROM managed_anime WHERE id = $1`, animeID).Scan(&storedOverride); err != nil {
		t.Fatal(err)
	}
	if !storedOverride.Valid || storedOverride.String != override {
		t.Fatalf("title_override must keep the fixed title after a null, got %+v", storedOverride)
	}

	// A stale expected_version is rejected without touching title_override.
	staleValue := "stale-value"
	_, err = service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: patched.Version, // stale: cleared.Version advanced past patched.Version
		Bgm:             nil,
		Tmdb:            nil,
		TitleOverride:   &staleValue,
	})
	var conflictErr *apperrors.VersionConflict
	if !errors.As(err, &conflictErr) || conflictErr.CurrentVersion != cleared.Version {
		t.Fatalf("expected CAS conflict at version %d, got %v", cleared.Version, err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT title_override FROM managed_anime WHERE id = $1`, animeID).Scan(&storedOverride); err != nil {
		t.Fatal(err)
	}
	if !storedOverride.Valid || storedOverride.String != override {
		t.Fatalf("rejected CAS patch must not change title_override, got %+v", storedOverride)
	}

	// An explicit blank string is a validation error, not a silent clear.
	blank := ""
	_, err = service.PatchAnimeMetadata(context.Background(), animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: cleared.Version,
		Bgm:             nil,
		Tmdb:            nil,
		TitleOverride:   &blank,
	})
	var validationErr *apperrors.Validation
	if !errors.As(err, &validationErr) || validationErr.Code != "DRAFT_INVALID" {
		t.Fatalf("expected DRAFT_INVALID for blank title_override, got %v", err)
	}
}

func TestPatchRulesReplaceAndCAS(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-0007")
	animeID := subscribe.ManagedAnime.Id

	patched, err := service.PatchAnimeRules(context.Background(), animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: 1,
		Sections:        ownershipReplaceParserRules(t, []string{`\[(\d{1,4})\]`}).Sections,
	}))
	if err != nil {
		t.Fatalf("rules patch: %v", err)
	}
	if patched.Rules.Version != 2 {
		t.Fatalf("rules owner version must advance, got %+v", patched.Rules)
	}
	if len(patched.Rules.Effective.Parser.EpisodePatterns) != 1 || patched.Rules.Effective.Parser.EpisodePatterns[0] != `\[(\d{1,4})\]` {
		t.Fatalf("replace must win effective resolution: %+v", patched.Rules.Effective.Parser)
	}
	// The other sections carry the submitted complete values.
	if patched.Rules.Effective.Naming.Tv.VideoFilenameTemplate == "" || patched.Rules.Effective.FileEffect.Operation != "copy" {
		t.Fatalf("submitted sections must be stored: %+v", patched.Rules.Effective)
	}
	if patched.Reevaluation.ManagedAnimeID != animeID || patched.Reevaluation.ScheduledCount != 0 {
		t.Fatalf("rules patch reevaluation summary = %+v, want anime %s with zero releases", patched.Reevaluation, animeID)
	}
	var policyKind, policyReason, policySubjectType, policySubjectID, policyCode string
	var policyVersion int64
	var policyParser bool
	if err := db.QueryRowContext(context.Background(), `
SELECT kind, reason_code, subject_type, subject_id, subject_version, message_code,
       COALESCE(details->'changed_sections' ? 'parser', false)
FROM subscription_log_entries
WHERE subscription_id = $1 AND managed_anime_id = $2`, subscribe.Subscription.Id, animeID).Scan(
		&policyKind, &policyReason, &policySubjectType, &policySubjectID, &policyVersion, &policyCode, &policyParser); err != nil {
		t.Fatalf("read rules policy activity: %v", err)
	}
	if policyKind != "policy_changed" || policyReason != "rules_policy_changed" || policySubjectType != "rules" || policySubjectID != animeID || policyVersion != 2 || policyCode != "rules_changed" || !policyParser {
		t.Fatalf("unexpected rules policy activity: kind=%q reason=%q subject=%s/%s v%d code=%q parser_changed=%v", policyKind, policyReason, policySubjectType, policySubjectID, policyVersion, policyCode, policyParser)
	}
	// Every section is stored as a complete value.
	var parserNull, namingNull bool
	if err := db.QueryRowContext(context.Background(), `
SELECT (value IS NULL) FROM anime_config_overrides WHERE managed_anime_id = $1 AND section_name = 'parser'`, animeID).Scan(&parserNull); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT (value IS NULL) FROM anime_config_overrides WHERE managed_anime_id = $1 AND section_name = 'naming_rules'`, animeID).Scan(&namingNull); err != nil {
		t.Fatal(err)
	}
	if parserNull || namingNull {
		t.Fatalf("every stored section must carry a value: parserNull=%v namingNull=%v", parserNull, namingNull)
	}

	_, err = service.PatchAnimeRules(context.Background(), animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: 1, Sections: ownershipDefaultRules(t).Sections,
	}))
	var conflictErr *apperrors.VersionConflict
	if !errors.As(err, &conflictErr) || conflictErr.CurrentVersion != 2 {
		t.Fatalf("stale CAS must report the current version, got %v", err)
	}
}

func TestPatchRulesSchedulesOnlyCurrentAnimeUnsubmittedReleasesAndMergesLiveWork(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	first := ownershipSubscribeTestDraft(t, service, "subscribe-key-rules-scope-1")
	second := ownershipSubscribeTestDraft(t, service, "subscribe-key-rules-scope-2")
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES
    ('rules-current-new', $1, 'rules-current-new', '{}', '{}', 'discovered', 'new'),
    ('rules-current-merge', $1, 'rules-current-merge', '{}', '{}', 'filtered', 'filtered'),
    ('rules-current-submitted', $1, 'rules-current-submitted', '{}', '{}', 'filtered', 'submitted'),
    ('rules-other-anime', $2, 'rules-other-anime', '{}', '{}', 'discovered', 'new')`,
		first.RssSources[0].Id, second.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "rules-root", "rules-task", "rules-current-submitted", "rules-task-key", `{}`, `{}`, "queued", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'rules-task' WHERE id = 'rules-current-submitted'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE releases SET version = 3 WHERE id = 'rules-current-merge'`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "rules-current-merge"}, Kind: workitems.KindReleaseEvaluate,
		IdempotencyKey: "release_evaluate:rules-current-merge", InputSnapshot: json.RawMessage(`{"trigger":"stale_rules"}`),
		AvailableAfter: time.Hour, MaxAttempts: animeapplication.DefaultMaxAttempts, ExpectedOwnerVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	result, err := service.PatchAnimeRules(ctx, first.ManagedAnime.Id, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: 1, Sections: ownershipReplaceParserRules(t, []string{`S(\d+)`}).Sections,
	}))
	if err != nil {
		t.Fatalf("rules patch: %v", err)
	}
	if result.Reevaluation.ScheduledCount != 1 {
		t.Fatalf("rules patch scheduled_count=%d, want one newly scheduled evaluation", result.Reevaluation.ScheduledCount)
	}
	for releaseID, want := range map[string]int{
		"rules-current-new": 1, "rules-current-merge": 1, "rules-current-submitted": 0, "rules-other-anime": 0,
	} {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("release %s work item count=%d, want %d", releaseID, got, want)
		}
	}
	var mergedState string
	var mergedVersion int64
	var mergedSnapshot []byte
	if err := db.QueryRowContext(ctx, `
SELECT state, expected_owner_version, input_snapshot
FROM work_item_history
WHERE owner_type = 'release' AND owner_id = 'rules-current-merge' AND kind = 'release_evaluate'`).Scan(
		&mergedState, &mergedVersion, &mergedSnapshot); err != nil {
		t.Fatal(err)
	}
	if mergedState != workitems.StateAvailable || mergedVersion != 3 || string(mergedSnapshot) != `{"trigger": "rules_patch"}` {
		t.Fatalf("existing work item must be refreshed to the current release version: state=%s version=%d snapshot=%s", mergedState, mergedVersion, mergedSnapshot)
	}

	if _, err := service.PatchAnimeRules(ctx, first.ManagedAnime.Id, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: 1, Sections: ownershipDefaultRules(t).Sections,
	})); err == nil {
		t.Fatal("stale rules CAS must fail")
	}
	var releaseItems int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND kind = 'release_evaluate'`).Scan(&releaseItems); err != nil {
		t.Fatal(err)
	}
	if releaseItems != 2 {
		t.Fatalf("stale rules CAS must not create work, got %d release items", releaseItems)
	}
}

func TestPatchRSSSourceOffsetSchedulesOnlySourceUnsubmittedReleasesAndMergesLiveWork(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	first := ownershipSubscribeTestDraft(t, service, "subscribe-key-offset-scope-1")
	second := ownershipSubscribeTestDraft(t, service, "subscribe-key-offset-scope-2")
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES
    ('offset-new', $1, 'offset-new', '{}', '{}', 'discovered', 'new'),
    ('offset-merge', $1, 'offset-merge', '{}', '{}', 'filtered', 'filtered'),
    ('offset-submitted', $1, 'offset-submitted', '{}', '{}', 'filtered', 'submitted'),
    ('offset-other-source', $2, 'offset-other-source', '{}', '{}', 'discovered', 'new')`, first.RssSources[0].Id, second.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, disposabledb.Owner(t), "offset-root", "offset-task", "offset-submitted", "offset-task-key", `{}`, `{}`, "queued", `null`)
	if _, err := db.ExecContext(ctx, `UPDATE releases SET version = 4 WHERE id = 'offset-merge'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "offset-merge"}, Kind: workitems.KindReleaseEvaluate,
		IdempotencyKey: "release_evaluate:offset-merge", InputSnapshot: json.RawMessage(`{"trigger":"stale_offset"}`),
		AvailableAfter: time.Hour, MaxAttempts: animeapplication.DefaultMaxAttempts, ExpectedOwnerVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	offset := 2
	patched, err := service.PatchRSSSourceOffset(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 1, EpisodeOffset: offset,
	})
	if err != nil {
		t.Fatalf("offset patch: %v", err)
	}
	if patched.Version != 2 || patched.EpisodeOffset != offset {
		t.Fatalf("unexpected patched source: %+v", patched)
	}
	assertOffsetActivity := func(wantVersion int64) {
		var kind, reasonCode, subjectType, subjectID, messageCode, messageParams, dedupeKey string
		var subjectVersion int64
		var groupName sql.NullString
		if err := db.QueryRowContext(ctx, `
	SELECT l.kind, l.reason_code, l.subject_type, l.subject_id, l.subject_version, l.message_code, l.message_params::text, l.dedupe_key, s.group_name
	FROM subscription_log_entries l
	JOIN rss_sources s ON s.id = l.subject_id
	WHERE l.subscription_id = $1 AND l.subject_type = 'rss_source' AND l.subject_id = $2 AND l.subject_version = $3`,
			first.Subscription.Id, first.RssSources[0].Id, wantVersion).Scan(
			&kind, &reasonCode, &subjectType, &subjectID, &subjectVersion, &messageCode, &messageParams, &dedupeKey, &groupName); err != nil {
			t.Fatalf("read RSS source offset policy activity v%d: %v", wantVersion, err)
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(messageParams), &params); err != nil {
			t.Fatalf("decode offset message params %q: %v", messageParams, err)
		}
		wantParams := map[string]any{}
		if name := strings.TrimSpace(groupName.String); name != "" {
			wantParams["group_name"] = name
		}
		wantDedupe := "policy_changed:rss_source:" + first.RssSources[0].Id + ":v" + fmt.Sprint(wantVersion) + ":rss_source_offset_changed"
		if kind != "policy_changed" || reasonCode != "rss_source_offset_changed" || subjectType != "rss_source" ||
			subjectID != first.RssSources[0].Id || subjectVersion != wantVersion || messageCode != "episode_offset_changed" ||
			fmt.Sprint(params) != fmt.Sprint(wantParams) || dedupeKey != wantDedupe {
			t.Fatalf("unexpected RSS source offset policy activity: kind=%q reason=%q subject=%s/%s v%d code=%q params=%s (want %v) dedupe=%q",
				kind, reasonCode, subjectType, subjectID, subjectVersion, messageCode, messageParams, wantParams, dedupeKey)
		}
	}
	assertStoredOffset := func(want int) {
		var stored int
		if err := db.QueryRowContext(ctx, `SELECT episode_offset FROM rss_sources WHERE id = $1`, first.RssSources[0].Id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != want {
			t.Fatalf("source offset=%d, want %d", stored, want)
		}
	}
	assertOffsetActivity(2)
	assertStoredOffset(offset)

	patched, err = service.PatchRSSSourceOffset(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 2, EpisodeOffset: 0,
	})
	if err != nil {
		t.Fatalf("zero offset patch: %v", err)
	}
	if patched.Version != 3 || patched.EpisodeOffset != 0 {
		t.Fatalf("zero offset patch must persist 0: %+v", patched)
	}
	assertOffsetActivity(3)
	assertStoredOffset(0)

	zero := 0
	patched, err = service.PatchRSSSourceOffset(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 3, EpisodeOffset: zero,
	})
	if err != nil {
		t.Fatalf("unchanged zero offset patch: %v", err)
	}
	if patched.Version != 4 || patched.EpisodeOffset != 0 {
		t.Fatalf("unchanged zero offset patch must keep 0 and bump the version: %+v", patched)
	}
	assertOffsetActivity(4)
	assertStoredOffset(zero)

	negative := -2
	patched, err = service.PatchRSSSourceOffset(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 4, EpisodeOffset: negative,
	})
	if err != nil {
		t.Fatalf("negative offset patch: %v", err)
	}
	if patched.Version != 5 || patched.EpisodeOffset != negative {
		t.Fatalf("negative offset patch must persist %d: %+v", negative, patched)
	}
	assertOffsetActivity(5)
	assertStoredOffset(negative)

	var activityCount int
	if err := db.QueryRowContext(ctx, `
	SELECT COUNT(*)
	FROM subscription_log_entries
	WHERE subscription_id = $1 AND subject_type = 'rss_source' AND subject_id = $2 AND reason_code = 'rss_source_offset_changed'`,
		first.Subscription.Id, first.RssSources[0].Id).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if activityCount != 4 {
		t.Fatalf("expected one central offset policy activity per successful CAS save, got %d", activityCount)
	}
	for releaseID, want := range map[string]int{"offset-new": 1, "offset-merge": 1, "offset-submitted": 0, "offset-other-source": 0} {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("release %s work item count=%d, want %d", releaseID, got, want)
		}
	}
	var mergedState string
	var mergedVersion int64
	var mergedSnapshot []byte
	if err := db.QueryRowContext(ctx, `
SELECT state, expected_owner_version, input_snapshot
FROM work_item_history
WHERE owner_type = 'release' AND owner_id = 'offset-merge' AND kind = 'release_evaluate'`).Scan(
		&mergedState, &mergedVersion, &mergedSnapshot); err != nil {
		t.Fatal(err)
	}
	if mergedState != workitems.StateAvailable || mergedVersion != 4 || string(mergedSnapshot) != `{"trigger": "offset_patch"}` {
		t.Fatalf("existing offset work item must be refreshed to the current release version: state=%s version=%d snapshot=%s", mergedState, mergedVersion, mergedSnapshot)
	}
	if _, err := service.PatchRSSSourceOffset(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 1, EpisodeOffset: 0,
	}); err == nil {
		t.Fatal("stale offset CAS must fail")
	}
}

func TestPatchRSSSourceOffsetWithoutActivityWriterPreservesOwnerSave(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-offset-no-writer")
	service.SetActivityWriter(nil)

	offset := 4
	patched, err := service.PatchRSSSourceOffset(context.Background(), subscribe.ManagedAnime.Id, subscribe.RssSources[0].Id, RSSSourceOffsetPatchInput{
		ExpectedVersion: 1, EpisodeOffset: offset,
	})
	if err != nil {
		t.Fatalf("offset patch without central writer must preserve legacy save behavior: %v", err)
	}
	if patched.Version != 2 || patched.EpisodeOffset != offset {
		t.Fatalf("offset owner save was not preserved without central writer: %+v", patched)
	}
	var activityCount int
	if err := db.QueryRowContext(context.Background(), `
	SELECT COUNT(*)
	FROM subscription_log_entries
	WHERE subscription_id = $1 AND subject_type = 'rss_source' AND subject_id = $2 AND reason_code = 'rss_source_offset_changed'`,
		subscribe.Subscription.Id, subscribe.RssSources[0].Id).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if activityCount != 0 {
		t.Fatalf("nil central writer must skip only the new offset Activity, got %d rows", activityCount)
	}
}

func TestPatchSubgroupsWholeSectionCAS(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-0008")
	animeID := subscribe.ManagedAnime.Id
	originalSource := subscribe.RssSources[0]

	patched, err := service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 1,
		Subgroups:       ownershipReplaceSubgroups(t, 5),
	})
	if err != nil {
		t.Fatalf("subgroups patch: %v", err)
	}
	if patched.Version != 2 || patched.Subgroups.MaxRss != 5 || len(patched.RssSources) != 1 {
		t.Fatalf("subgroups owner must replace only its complete section: %+v", patched)
	}
	if patched.RssSources[0].Id != originalSource.Id || patched.RssSources[0].LocatorDisplay != originalSource.LocatorDisplay {
		t.Fatalf("subgroups patch must not edit RSS source configuration: %+v", patched.RssSources)
	}
	var storedSourceCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM rss_sources WHERE subscription_id = $1`, subscribe.Subscription.Id).Scan(&storedSourceCount); err != nil {
		t.Fatal(err)
	}
	if storedSourceCount != 1 {
		t.Fatalf("subgroups patch must not create or remove sources, got %d", storedSourceCount)
	}

	_, err = service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 1,
		Subgroups:       ownershipReplaceSubgroups(t, 6),
	})
	var conflictErr *apperrors.VersionConflict
	if !errors.As(err, &conflictErr) || conflictErr.CurrentVersion != 2 {
		t.Fatalf("stale subgroups CAS must report current version, got %v", err)
	}

	forced, err := service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 2,
		Subgroups: generated.SubgroupsSection{
			MaxRss: 6,
			Entries: []generated.SubgroupScoreEntry{{
				Provider:      generated.ProviderKindMikan,
				GroupRef:      "101",
				DisplayName:   "Test Group",
				SelectionMode: generated.SelectionModeAuto,
				Score:         50,
			}},
		},
		GroupActions: []generated.GroupAction{{
			Provider: generated.ProviderKindMikan,
			GroupRef: "101",
			Action:   generated.GroupActionActionForceInclude,
		}},
	})
	if err != nil {
		t.Fatalf("apply group action: %v", err)
	}
	if len(forced.Subgroups.Entries) != 1 {
		t.Fatalf("group action must preserve entries, got %+v", forced.Subgroups.Entries)
	}
	entry := forced.Subgroups.Entries[0]
	if entry.SelectionMode != generated.SelectionModeManual || entry.EffectiveSelected != true || entry.ManualSelected == nil || *entry.ManualSelected != true {
		t.Fatalf("force_include must set manual/selected, got %+v", entry)
	}
}

func TestPatchSubgroupsSyncsExistingSelectionsWithoutCreatingMissingRows(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-subgroups-sync")
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_group_selections(
    id, subscription_id, provider, group_ref, display_name, selection_mode,
    manual_selected, effective_selected, effective_score) VALUES
    ('selection-alpha', $1, 'mikan', 'alpha', 'Old Alpha', 'auto', NULL, FALSE, 1),
    ('selection-beta', $1, 'mikan', 'beta', 'Old Beta', 'auto', NULL, TRUE, 1)`, subscribe.Subscription.Id); err != nil {
		t.Fatal(err)
	}
	selected := true
	patched, err := service.PatchAnimeSubgroups(ctx, subscribe.ManagedAnime.Id, generated.SubgroupsPatch{
		ExpectedVersion: 1,
		Subgroups: generated.SubgroupsSection{MaxRss: 1, Entries: []generated.SubgroupScoreEntry{
			{Provider: generated.ProviderKindMikan, GroupRef: "alpha", DisplayName: "Alpha", SelectionMode: generated.SelectionModeAuto, Score: 90},
			{Provider: generated.ProviderKindMikan, GroupRef: "beta", DisplayName: "Beta", SelectionMode: generated.SelectionModeAuto, Score: 80},
			{Provider: generated.ProviderKindMikan, GroupRef: "manual", DisplayName: "Manual", SelectionMode: generated.SelectionModeManual, ManualSelected: &selected, EffectiveSelected: true, Score: 70},
		}},
	})
	if err != nil {
		t.Fatalf("subgroups sync patch: %v", err)
	}
	if patched.Version != 2 {
		t.Fatalf("subgroups owner version=%d, want 2", patched.Version)
	}
	var selectionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_selections WHERE subscription_id = $1`, subscribe.Subscription.Id).Scan(&selectionCount); err != nil {
		t.Fatal(err)
	}
	if selectionCount != 2 {
		t.Fatalf("subgroups patch must not create missing selection rows, got %d", selectionCount)
	}
	rows, err := db.QueryContext(ctx, `
SELECT group_ref, display_name, selection_mode, manual_selected, effective_selected, effective_score, version
FROM provider_group_selections WHERE subscription_id = $1 ORDER BY group_ref`, subscribe.Subscription.Id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var groupRef, displayName, mode string
		var manual sql.NullBool
		var effective bool
		var score int
		var version int64
		if err := rows.Scan(&groupRef, &displayName, &mode, &manual, &effective, &score, &version); err != nil {
			t.Fatal(err)
		}
		if manual.Valid || mode != "auto" || version != 2 {
			t.Fatalf("unexpected synced selection %s: mode=%s manual=%v version=%d", groupRef, mode, manual, version)
		}
		switch groupRef {
		case "alpha":
			if displayName != "Alpha" || !effective || score != 90 {
				t.Fatalf("unexpected alpha selection: display=%s effective=%t score=%d", displayName, effective, score)
			}
		case "beta":
			if displayName != "Beta" || effective || score != 80 {
				t.Fatalf("unexpected beta selection: display=%s effective=%t score=%d", displayName, effective, score)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()

	excluded, err := service.PatchAnimeSubgroups(ctx, subscribe.ManagedAnime.Id, generated.SubgroupsPatch{
		ExpectedVersion: 2,
		Subgroups:       patched.Subgroups,
		GroupActions: []generated.GroupAction{{
			Provider: generated.ProviderKindMikan,
			GroupRef: "alpha",
			Action:   generated.GroupActionActionForceExclude,
		}},
	})
	if err != nil {
		t.Fatalf("force-exclude selection: %v", err)
	}
	var alphaDisplay, alphaMode string
	var alphaManual sql.NullBool
	var alphaEffective bool
	var alphaVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT display_name, selection_mode, manual_selected, effective_selected, version
FROM provider_group_selections
WHERE subscription_id = $1 AND provider = 'mikan' AND group_ref = 'alpha'`, subscribe.Subscription.Id).Scan(
		&alphaDisplay, &alphaMode, &alphaManual, &alphaEffective, &alphaVersion); err != nil {
		t.Fatal(err)
	}
	if alphaMode != "manual" || !alphaManual.Valid || alphaManual.Bool || alphaEffective || alphaVersion != 3 {
		t.Fatalf("force-exclude did not synchronize selection: mode=%s manual=%v effective=%t version=%d", alphaMode, alphaManual, alphaEffective, alphaVersion)
	}

	restored, err := service.PatchAnimeSubgroups(ctx, subscribe.ManagedAnime.Id, generated.SubgroupsPatch{
		ExpectedVersion: 3,
		Subgroups:       excluded.Subgroups,
		GroupActions: []generated.GroupAction{{
			Provider: generated.ProviderKindMikan,
			GroupRef: "alpha",
			Action:   generated.GroupActionActionRestoreAuto,
		}},
	})
	if err != nil {
		t.Fatalf("restore-auto selection: %v", err)
	}
	if restored.Version != 4 {
		t.Fatalf("restored subgroups owner version=%d, want 4", restored.Version)
	}
	if err := db.QueryRowContext(ctx, `
SELECT display_name, selection_mode, manual_selected, effective_selected, version
FROM provider_group_selections
WHERE subscription_id = $1 AND provider = 'mikan' AND group_ref = 'alpha'`, subscribe.Subscription.Id).Scan(
		&alphaDisplay, &alphaMode, &alphaManual, &alphaEffective, &alphaVersion); err != nil {
		t.Fatal(err)
	}
	if alphaMode != "auto" || alphaManual.Valid || !alphaEffective || alphaVersion != 4 {
		t.Fatalf("restore-auto did not synchronize selection: mode=%s manual=%v effective=%t version=%d", alphaMode, alphaManual, alphaEffective, alphaVersion)
	}

	if _, err := service.PatchAnimeSubgroups(ctx, subscribe.ManagedAnime.Id, generated.SubgroupsPatch{
		ExpectedVersion: 1,
		Subgroups: generated.SubgroupsSection{MaxRss: 2, Entries: []generated.SubgroupScoreEntry{
			{Provider: generated.ProviderKindMikan, GroupRef: "alpha", DisplayName: "Stale Alpha", SelectionMode: generated.SelectionModeManual, ManualSelected: &selected, EffectiveSelected: true, Score: 1},
		}},
	}); err == nil {
		t.Fatal("stale subgroups CAS must fail")
	}
	if err := db.QueryRowContext(ctx, `
SELECT display_name, selection_mode, manual_selected, effective_selected, version
FROM provider_group_selections
WHERE subscription_id = $1 AND provider = 'mikan' AND group_ref = 'alpha'`, subscribe.Subscription.Id).Scan(
		&alphaDisplay, &alphaMode, &alphaManual, &alphaEffective, &alphaVersion); err != nil {
		t.Fatal(err)
	}
	if alphaDisplay != "Alpha" || alphaMode != "auto" || alphaManual.Valid || !alphaEffective || alphaVersion != 4 {
		t.Fatalf("stale subgroups CAS changed selection facts: display=%s mode=%s manual=%v effective=%t version=%d", alphaDisplay, alphaMode, alphaManual, alphaEffective, alphaVersion)
	}
}

func TestPatchSubscriptionCASAndResumeScheduling(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-0009")
	animeID := subscribe.ManagedAnime.Id

	stopped, err := service.PatchAnimeSubscription(context.Background(), animeID, SubscriptionPatchInput{
		ExpectedVersion: 1, State: subscriptiondomain.SubscriptionState("stopped"),
	})
	if err != nil {
		t.Fatalf("stop subscription: %v", err)
	}
	if stopped.State != "stopped" || stopped.Version != 2 {
		t.Fatalf("subscription must stop with CAS: %+v", stopped)
	}
	var resumedAt sql.NullTime
	if err := db.QueryRowContext(context.Background(), `SELECT resumed_at FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&resumedAt); err != nil || resumedAt.Valid {
		t.Fatalf("a stop must not stamp the resume clock: %v %v", resumedAt, err)
	}
	// Clear the initial live refresh item to prove resume schedules fresh work.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `
SELECT public.work_item_close(id,'succeeded') FROM work_items
WHERE owner_id = $1`, subscribe.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.PatchAnimeSubscription(context.Background(), animeID, SubscriptionPatchInput{
		ExpectedVersion: 2, State: subscriptiondomain.SubscriptionState("active"),
	})
	if err != nil {
		t.Fatalf("resume subscription: %v", err)
	}
	if resumed.State != "active" || resumed.Version != 3 {
		t.Fatalf("subscription must resume with CAS: %+v", resumed)
	}
	var live int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_items WHERE owner_id = $1 AND kind = 'rss_refresh' AND state = 'available'`, subscribe.RssSources[0].Id).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("resume must schedule one refresh for the custom source, got %d", live)
	}
	// The resume stamps the clock its sources' first refreshes answer with a
	// baseline; the runtime rows predate it.
	var pending int
	if err := db.QueryRowContext(context.Background(), `
SELECT count(*) FROM rss_source_runtime rt
JOIN rss_sources src ON src.id = rt.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1 AND sub.resumed_at IS NOT NULL AND rt.created_at < sub.resumed_at
  AND rt.resume_baseline_at IS NULL`, animeID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != len(subscribe.RssSources) {
		t.Fatalf("resume must leave every existing source due a baseline, got %d of %d", pending, len(subscribe.RssSources))
	}
}

func TestRefreshRSSSourceTransactionAdmission(t *testing.T) {
	tests := []struct {
		name         string
		provider     bool
		selected     *bool
		stopped      bool
		wantCode     string
		wantAccepted bool
	}{
		{name: "active custom", wantAccepted: true},
		{name: "stopped custom (explicit manual command allowed)", stopped: true, wantAccepted: true},
		{name: "provider selection missing", provider: true, wantCode: "RSS_SOURCE_NOT_SELECTED"},
		{name: "provider selection false", provider: true, selected: animeBoolPointer(false), wantCode: "RSS_SOURCE_NOT_SELECTED"},
		{name: "provider selection true", provider: true, selected: animeBoolPointer(true), wantAccepted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
			ctx := context.Background()
			result := ownershipSubscribeTestDraft(t, service, "refresh-admission-"+strings.ReplaceAll(test.name, " ", "-"))
			sourceID := result.RssSources[0].Id
			if _, err := workItemFixtureDB(t).ExecContext(ctx, `WITH o AS (DELETE FROM work_item_outcomes WHERE owner_id = $1 AND kind = 'rss_refresh') DELETE FROM work_items WHERE owner_id = $1 AND kind = 'rss_refresh'`, sourceID); err != nil {
				t.Fatal(err)
			}
			if test.provider {
				if _, err := db.ExecContext(ctx, `
				UPDATE rss_sources
				SET source_kind = 'provider_group', provider = 'anibt', provider_entry_ref = '123',
				    provider_group_ref = 'alpha', discovery_snapshot_version = 'v1'
				WHERE id = $1`, sourceID); err != nil {
					t.Fatal(err)
				}
			}
			if test.selected != nil {
				if _, err := db.ExecContext(ctx, `
				INSERT INTO provider_group_selections(
				    id, subscription_id, provider, group_ref, display_name, selection_mode,
				    manual_selected, effective_selected, effective_score) VALUES ($1, $2, 'anibt', 'alpha', 'Alpha', 'auto', NULL, $3, 50)`,
					"selection-"+sourceID, result.Subscription.Id, *test.selected); err != nil {
					t.Fatal(err)
				}
			}
			if test.stopped {
				if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'stopped' WHERE id = $1`, result.Subscription.Id); err != nil {
					t.Fatal(err)
				}
			}

			accepted, err := service.RefreshRSSSource(ctx, sourceID, "manual-refresh-key")
			if test.wantAccepted {
				if err != nil || !accepted.Accepted || accepted.WorkItemID == "" {
					t.Fatalf("refresh admission accepted=%+v err=%v", accepted, err)
				}
			} else {
				var businessErr *apperrors.Business
				if !errors.As(err, &businessErr) || businessErr.Status != 409 || businessErr.Code != test.wantCode {
					t.Fatalf("refresh admission error=%v, want 409 %s", err, test.wantCode)
				}
			}
			var workCount int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_id = $1 AND kind = 'rss_refresh'`, sourceID).Scan(&workCount); err != nil {
				t.Fatal(err)
			}
			wantWork := 0
			if test.wantAccepted {
				wantWork = 1
			}
			if workCount != wantWork {
				t.Fatalf("refresh work count=%d, want %d", workCount, wantWork)
			}
		})
	}
}

func TestPatchSubscriptionWritesStateTransitionLogs(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-statelogs")
	animeID := subscribe.ManagedAnime.Id
	subscriptionID := subscribe.Subscription.Id

	type logRow struct {
		kind    string
		outcome string
		code    string
		params  string
		dedupe  string
	}
	readTransitionLogs := func(t *testing.T) []logRow {
		t.Helper()
		rows, err := db.QueryContext(context.Background(), `
	SELECT kind, outcome, message_code, message_params::text, dedupe_key FROM subscription_log_entries
	WHERE subscription_id = $1 AND kind IN ('subscription_stopped', 'subscription_resumed')
	ORDER BY occurred_at, id`, subscriptionID)
		if err != nil {
			t.Fatalf("read transition logs: %v", err)
		}
		defer rows.Close()
		var logs []logRow
		for rows.Next() {
			var row logRow
			if err := rows.Scan(&row.kind, &row.outcome, &row.code, &row.params, &row.dedupe); err != nil {
				t.Fatalf("scan transition log: %v", err)
			}
			logs = append(logs, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate transition logs: %v", err)
		}
		return logs
	}

	stopped, err := service.PatchAnimeSubscription(context.Background(), animeID, SubscriptionPatchInput{
		ExpectedVersion: 1, State: subscriptiondomain.SubscriptionState("stopped"),
	})
	if err != nil {
		t.Fatalf("stop subscription: %v", err)
	}
	if stopped.Version != 2 {
		t.Fatalf("stop must bump CAS version: %+v", stopped)
	}
	logs := readTransitionLogs(t)
	if len(logs) != 1 || logs[0].kind != "subscription_stopped" || logs[0].outcome != "info" ||
		logs[0].code != "subscription_stopped" || logs[0].params != "{}" || logs[0].dedupe != "subscription_stopped:subscription:"+subscriptionID+":v2:subscription_stopped" {
		t.Fatalf("stop must write one stopped log: %+v", logs)
	}

	// A same-state save bumps the CAS version but is not a new transition.
	if _, err := service.PatchAnimeSubscription(context.Background(), animeID, SubscriptionPatchInput{
		ExpectedVersion: 2, State: subscriptiondomain.SubscriptionState("stopped"),
	}); err != nil {
		t.Fatalf("same-state subscription patch: %v", err)
	}
	if logs = readTransitionLogs(t); len(logs) != 1 {
		t.Fatalf("same-state save must not append a transition log: %+v", logs)
	}

	resumed, err := service.PatchAnimeSubscription(context.Background(), animeID, SubscriptionPatchInput{
		ExpectedVersion: 3, State: subscriptiondomain.SubscriptionState("active"),
	})
	if err != nil {
		t.Fatalf("resume subscription: %v", err)
	}
	if resumed.Version != 4 {
		t.Fatalf("resume must bump CAS version: %+v", resumed)
	}
	logs = readTransitionLogs(t)
	if len(logs) != 2 || logs[1].kind != "subscription_resumed" || logs[1].outcome != "info" ||
		logs[1].code != "subscription_resumed" || logs[1].params != "{}" || logs[1].dedupe != "subscription_resumed:subscription:"+subscriptionID+":v4:subscription_resumed" {
		t.Fatalf("resume must append one resumed log: %+v", logs)
	}
}

func TestPatchSubgroupsWritesGroupChangedLogOnActualChange(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-grouplogs")
	animeID := subscribe.ManagedAnime.Id
	subscriptionID := subscribe.Subscription.Id

	countGroupLogs := func(t *testing.T) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(context.Background(), `
	SELECT COUNT(*) FROM subscription_log_entries
	WHERE subscription_id = $1 AND kind = 'group_changed'`, subscriptionID).Scan(&count); err != nil {
			t.Fatalf("count group_changed logs: %v", err)
		}
		return count
	}

	patched, err := service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 1,
		Subgroups:       ownershipReplaceSubgroups(t, 5),
	})
	if err != nil {
		t.Fatalf("subgroups patch: %v", err)
	}
	if patched.Version != 2 {
		t.Fatalf("subgroups patch must bump CAS version: %+v", patched)
	}
	if count := countGroupLogs(t); count != 1 {
		t.Fatalf("changed subgroups must write one group_changed log, got %d", count)
	}
	var code, params, dedupe, outcome string
	if err := db.QueryRowContext(context.Background(), `
	SELECT message_code, message_params::text, dedupe_key, outcome FROM subscription_log_entries
	WHERE subscription_id = $1 AND kind = 'group_changed'`, subscriptionID).Scan(&code, &params, &dedupe, &outcome); err != nil {
		t.Fatalf("read group_changed log: %v", err)
	}
	if code != "group_changed" || params != "{}" || dedupe != "group_changed:subgroups:"+animeID+":v2:group_changed" || outcome != "info" {
		t.Fatalf("unexpected group_changed log: code=%q params=%s dedupe=%q outcome=%q", code, params, dedupe, outcome)
	}

	// Saving the same value bumps the CAS version but writes no new log.
	if _, err := service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 2,
		Subgroups:       ownershipReplaceSubgroups(t, 5),
	}); err != nil {
		t.Fatalf("same-value subgroups patch: %v", err)
	}
	if count := countGroupLogs(t); count != 1 {
		t.Fatalf("same-value save must not append a group_changed log, got %d", count)
	}

	// A real value change appends a fresh versioned entry.
	if _, err := service.PatchAnimeSubgroups(context.Background(), animeID, generated.SubgroupsPatch{
		ExpectedVersion: 3,
		Subgroups:       ownershipReplaceSubgroups(t, 7),
	}); err != nil {
		t.Fatalf("changed subgroups patch: %v", err)
	}
	if count := countGroupLogs(t); count != 2 {
		t.Fatalf("changed subgroups must append a second group_changed log, got %d", count)
	}
}

// TestDeleteAnimeSubscriptionRetainsAllOwnerRows proves irreversible retirement
// preserves the complete local graph and independent catalog suppression while
// stale expected_version is still refused.

func TestPatchRSSSourceAniBTModeSchedulesUnsubmittedReleases(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	first := ownershipSubscribeTestDraft(t, service, "subscribe-key-anibt-mode-1")
	second := ownershipSubscribeTestDraft(t, service, "subscribe-key-anibt-mode-2")
	ctx := context.Background()
	// Turn the first source into an AniBT provider group so the aux mode is
	// accepted; the second source stays a plain custom source.
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources
SET source_kind = 'provider_group',
    provenance = '{"provider":"anibt","provider_entry_ref":"bgm-1","provider_group_ref":"group-1","discovery_snapshot_version":"v1"}'::jsonb
WHERE id = $1`, first.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES
    ('anibt-new', $1, 'anibt-new', '{}', '{}', 'discovered', 'new'),
    ('anibt-submitted', $1, 'anibt-submitted', '{}', '{}', 'filtered', 'submitted'),
    ('anibt-other-source', $2, 'anibt-other-source', '{}', '{}', 'discovered', 'new')`,
		first.RssSources[0].Id, second.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, disposabledb.Owner(t), "anibt-root", "anibt-task", "anibt-submitted", "anibt-task-key", `{}`, `{}`, "queued", `null`)

	mode := animedomain.RSSSourceDraftAnibtAuxModeOverride
	patched, err := service.PatchRSSSourceAniBTMode(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceAniBTModePatchInput{
		ExpectedVersion: 1, AnibtAuxMode: &mode,
	})
	if err != nil {
		t.Fatalf("anibt mode patch: %v", err)
	}
	if patched.Version != 2 || patched.AnibtAuxMode == nil || *patched.AnibtAuxMode != animedomain.RSSSourceDraftAnibtAuxMode(mode) {
		t.Fatalf("unexpected patched source: %+v", patched)
	}
	var storedMode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT anibt_aux_mode FROM rss_sources WHERE id = $1`, first.RssSources[0].Id).Scan(&storedMode); err != nil {
		t.Fatal(err)
	}
	if !storedMode.Valid || storedMode.String != string(mode) {
		t.Fatalf("stored anibt_aux_mode=%v, want %s", storedMode, mode)
	}
	// Only unsubmitted releases of the owning source are re-evaluated; a
	// submitted release and every release of other sources stay untouched.
	for releaseID, want := range map[string]int{"anibt-new": 1, "anibt-submitted": 0, "anibt-other-source": 0} {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("release %s work item count=%d, want %d", releaseID, got, want)
		}
	}

	// Stale CAS fails without side effects.
	if _, err := service.PatchRSSSourceAniBTMode(ctx, first.ManagedAnime.Id, first.RssSources[0].Id, RSSSourceAniBTModePatchInput{
		ExpectedVersion: 1, AnibtAuxMode: &mode,
	}); err == nil {
		t.Fatal("stale anibt mode CAS must fail")
	}
	var countAfter int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND owner_id = 'anibt-new' AND kind = 'release_evaluate'`).Scan(&countAfter); err != nil {
		t.Fatal(err)
	}
	if countAfter != 1 {
		t.Fatalf("stale CAS created work items: count=%d, want 1", countAfter)
	}
}

func animeBoolPointer(value bool) *bool { return &value }
