package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/metadata/matcher"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

type fakeMetadataMatcher struct {
	result *matcher.MatchResult
	err    error
	calls  int
}

func (f *fakeMetadataMatcher) Match(_ context.Context, _, _, _ string, _, _ bool) (*matcher.MatchResult, error) {
	f.calls++
	return f.result, f.err
}

func TestMatchAnimeMetadataNoResultDoesNotWrite(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	fake := &fakeMetadataMatcher{}
	service.SetMetadataMatcher(fake)
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetObservabilityRecorder(observability)

	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "match-no-result-0001")
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, "match-key-1")
	if err != nil {
		t.Fatalf("metadata match: %v", err)
	}
	if result.Outcome != MetadataMatchOutcomeNoResult || result.Metadata.Version != 1 {
		t.Fatalf("unexpected no-result response: %+v", result)
	}
	if result.Metadata.BGM != nil || result.Metadata.TMDB != nil || fake.calls != 1 {
		t.Fatalf("no-result must preserve metadata and call matcher once: %+v calls=%d", result.Metadata, fake.calls)
	}

	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, subscribed.ManagedAnime.Id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("no-result must not bump metadata version, got %d", version)
	}
	var outcome, resultCode string
	if err := db.QueryRowContext(context.Background(), `
SELECT outcome, last_result_code FROM routine_activities
WHERE kind = 'metadata_match' AND scope_kind = 'subscription' AND scope_id = $1`, subscribed.ManagedAnime.Id).Scan(&outcome, &resultCode); err != nil {
		t.Fatal(err)
	}
	if outcome != "warning" || resultCode != "metadata_match_no_result" {
		t.Fatalf("no-result must be observable without RSS log mixing: %s/%s", outcome, resultCode)
	}
	var appliedLogs int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribed.Subscription.Id).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 0 {
		t.Fatalf("no-result must not write metadata_applied log, got %d", appliedLogs)
	}
}

func TestMatchAnimeMetadataCompleteWritesAndSchedulesHeldRelease(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	bgmID := "bgm-match-1"
	tmdbID := "tmdb-match-1"
	kind := "tv"
	season := 2
	service.SetMetadataMatcher(&fakeMetadataMatcher{result: &matcher.MatchResult{
		BgmID:      &bgmID,
		TmdbWorkID: &tmdbID,
		TmdbKind:   &kind,
		TmdbSeason: &season,
	}})

	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "match-complete-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('match-held-release', $1, 'match-key', '{}', '{}', 'waiting_metadata', 'metadata_incomplete')`, subscribed.RssSources[0].Id); err != nil {
		t.Fatal(err)
	}

	// The matched work carries a cached name; a match never turns it into the title.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO metadata_work_titles(provider, media_kind, work_id, title_cn, original_title)
VALUES ('tmdb', 'tv', $1, '匹配作品中文名', 'Matched Work')`, tmdbID); err != nil {
		t.Fatal(err)
	}
	var titleBefore sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT title_override FROM managed_anime WHERE id = $1`, subscribed.ManagedAnime.Id).Scan(&titleBefore); err != nil {
		t.Fatal(err)
	}

	result, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, "match-key-1")
	if err != nil {
		t.Fatalf("metadata match: %v", err)
	}
	if result.Outcome != MetadataMatchOutcomeComplete || result.Metadata.Version != 2 {
		t.Fatalf("unexpected complete response: %+v", result)
	}
	var titleAfter sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT title_override FROM managed_anime WHERE id = $1`, subscribed.ManagedAnime.Id).Scan(&titleAfter); err != nil {
		t.Fatal(err)
	}
	if titleAfter != titleBefore {
		t.Fatalf("a metadata match must leave the title unchanged: %+v -> %+v", titleBefore, titleAfter)
	}
	if result.Metadata.BGM == nil || result.Metadata.BGM.WorkID != bgmID || result.Metadata.TMDB == nil || result.Metadata.TMDB.WorkID != tmdbID || result.Metadata.TMDB.Season != season {
		t.Fatalf("matched metadata was not persisted: %+v", result.Metadata)
	}

	var evalItems int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'release' AND owner_id = 'match-held-release' AND kind = 'release_evaluate'`).Scan(&evalItems); err != nil {
		t.Fatal(err)
	}
	if evalItems != 1 {
		t.Fatalf("complete metadata must schedule one release evaluation, got %d", evalItems)
	}
	var appliedLogs int
	var activityKind, activityOutcome, reasonCode, subjectType, subjectID, dedupeKey string
	var subjectVersion int64
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*), MIN(kind), MIN(outcome), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version), MIN(dedupe_key)
FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribed.Subscription.Id).Scan(
		&appliedLogs, &activityKind, &activityOutcome, &reasonCode, &subjectType, &subjectID, &subjectVersion, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 || activityKind != "metadata_applied" || activityOutcome != "success" || reasonCode != "metadata_applied" ||
		subjectType != "metadata" || subjectID != subscribed.ManagedAnime.Id || subjectVersion != 2 ||
		dedupeKey != "metadata_applied:metadata:"+subscribed.ManagedAnime.Id+":v2:metadata_applied" {
		t.Fatalf("unexpected metadata Activity envelope: count=%d kind=%q outcome=%q reason=%q subject=%s/%s/v%d dedupe=%q",
			appliedLogs, activityKind, activityOutcome, reasonCode, subjectType, subjectID, subjectVersion, dedupeKey)
	}

	repeated, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 2}, "match-key-2")
	if err != nil {
		t.Fatalf("repeated metadata match: %v", err)
	}
	if repeated.Outcome != MetadataMatchOutcomeComplete || repeated.Metadata.Version != 2 {
		t.Fatalf("repeated complete metadata must remain complete at version 2: %+v", repeated)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'metadata_applied'`, subscribed.Subscription.Id).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 {
		t.Fatalf("repeated complete metadata must not duplicate metadata_applied log, got %d", appliedLogs)
	}
	// The filled bindings are render inputs: one scan request, and the repeat
	// that fills nothing does not bump it.
	if rev := configScanRevision(t, db, subscribed.ManagedAnime.Id); rev != 1 {
		t.Fatalf("a backfill that fills the bindings requests one scan, revision %d", rev)
	}
}

func TestMatchAnimeMetadataRequiresActiveSubscription(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	fake := &fakeMetadataMatcher{result: &matcher.MatchResult{}}
	service.SetMetadataMatcher(fake)
	subscribed := ownershipSubscribeTestDraft(t, service, "match-stopped-0001")
	if _, err := service.PatchAnimeSubscription(context.Background(), subscribed.ManagedAnime.Id, SubscriptionPatchInput{
		ExpectedVersion: 1,
		State:           domain.SubscriptionState("stopped"),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, "match-key-1")
	var businessErr *apperrors.Business
	if !errors.As(err, &businessErr) || businessErr.Code != "SUBSCRIPTION_INACTIVE" {
		t.Fatalf("expected inactive subscription error, got %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("inactive subscription must not call matcher, got %d calls", fake.calls)
	}
}

func TestMatchAnimeMetadataIdempotencyReplay(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	bgmID := "bgm-999"
	tmdbID := "tmdb-999"
	kind := "tv"
	season := 1
	fake := &fakeMetadataMatcher{result: &matcher.MatchResult{
		BgmID:      &bgmID,
		TmdbWorkID: &tmdbID,
		TmdbKind:   &kind,
		TmdbSeason: &season,
	}}
	service.SetMetadataMatcher(fake)
	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "match-replay-0001")
	if err != nil {
		t.Fatal(err)
	}
	const key = "match-replay-key-1"

	first, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, key)
	if err != nil {
		t.Fatalf("first metadata match: %v", err)
	}
	if first.Outcome != MetadataMatchOutcomeComplete {
		t.Fatalf("first match outcome: %v", first.Outcome)
	}
	if fake.calls != 1 {
		t.Fatalf("first match must call matcher once, got %d", fake.calls)
	}

	// Same key + same body replay: returns the original result without calling
	// the matcher again or writing.
	replayed, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, key)
	if err != nil {
		t.Fatalf("replayed metadata match: %v", err)
	}
	if replayed.Outcome != first.Outcome || replayed.Metadata.Version != first.Metadata.Version {
		t.Fatalf("replay must return the original result: %+v vs %+v", replayed, first)
	}
	if fake.calls != 1 {
		t.Fatalf("replay must not call matcher again, got %d calls", fake.calls)
	}
}

func TestMatchAnimeMetadataIdempotencyKeyReuseConflict(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	bgmID := "bgm-111"
	tmdbID := "tmdb-111"
	kind := "tv"
	season := 1
	fake := &fakeMetadataMatcher{result: &matcher.MatchResult{
		BgmID:      &bgmID,
		TmdbWorkID: &tmdbID,
		TmdbKind:   &kind,
		TmdbSeason: &season,
	}}
	service.SetMetadataMatcher(fake)
	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "match-conflict-0001")
	if err != nil {
		t.Fatal(err)
	}
	const key = "match-conflict-key-1"

	if _, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, key); err != nil {
		t.Fatalf("first metadata match: %v", err)
	}

	// Same key, different body (different expected_version) -> 409
	// IDEMPOTENCY_KEY_REUSED.
	_, err = service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 2}, key)
	var businessErr *apperrors.Business
	if !errors.As(err, &businessErr) || businessErr.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("expected IDEMPOTENCY_KEY_REUSED, got %v", err)
	}
}
