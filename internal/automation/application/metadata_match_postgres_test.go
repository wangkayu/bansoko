package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/metadata/backfill"
	"bansoko/internal/metadata/matcher"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/workitems"
)

func TestMetadataMatchCompletesOnceAndReleasesWaitingRelease(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-complete", "sub-meta-complete", "Show")
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-meta-complete', 'sub-meta-complete', 'custom_rss', 'https://example.test/rss', 'https://example.test/rss', 'Custom', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-meta-complete')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('release-meta-complete', 'source-meta-complete', 'item-1', '{}'::jsonb, '{}'::jsonb, 'waiting_metadata', 'metadata_incomplete')`); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	metadataIndex := matcher.NewBangumiDataIndex([]matcher.DataItem{{
		BgmID: "bgm-123", TmdbID: "movie/456", Titles: []string{"Show"},
	}})
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:              db,
		WorkItems:       store,
		MetadataMatcher: matcher.NewMatcher(metadataIndex, nil, nil, nil),
		ActivityWriter:  activityWriter,
	}

	counts, err := dispatcher.dispatchMetadataMatch(ctx)
	if err != nil {
		t.Fatalf("metadata dispatch: %v", err)
	}
	if counts.targetCount != 1 || counts.enqueued != 1 || counts.skipped != 0 || counts.errors != 0 {
		t.Fatalf("unexpected metadata dispatch counts: %+v", counts)
	}
	var bgm, tmdb, kind string
	var season, version int
	if err := db.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, version
FROM anime_metadata WHERE managed_anime_id = 'anime-meta-complete'`).Scan(&bgm, &tmdb, &kind, &season, &version); err != nil {
		t.Fatal(err)
	}
	if bgm != "bgm-123" || tmdb != "456" || kind != "movie" || season != 0 || version != 2 {
		t.Fatalf("unexpected matched metadata: %q %q %q %d v%d", bgm, tmdb, kind, season, version)
	}
	var releaseWorkItems, heldReleases int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'release' AND owner_id = 'release-meta-complete' AND kind = 'release_evaluate'`).Scan(&releaseWorkItems); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM releases
WHERE id = 'release-meta-complete' AND verdict = 'waiting_metadata'`).Scan(&heldReleases); err != nil {
		t.Fatal(err)
	}
	if releaseWorkItems != 1 || heldReleases != 1 {
		t.Fatalf("metadata completion must enqueue one release evaluation: work=%d held=%d", releaseWorkItems, heldReleases)
	}
	var appliedLogs int
	var activityKind, activityOutcome, reasonCode, subjectType, subjectID, dedupeKey string
	var subjectVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), MIN(kind), MIN(outcome), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version), MIN(dedupe_key)
FROM subscription_log_entries
WHERE subscription_id = 'sub-meta-complete' AND kind = 'metadata_applied'`).Scan(
		&appliedLogs, &activityKind, &activityOutcome, &reasonCode, &subjectType, &subjectID, &subjectVersion, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 || activityKind != "metadata_applied" || activityOutcome != "success" || reasonCode != "metadata_applied" ||
		subjectType != "metadata" || subjectID != "anime-meta-complete" || subjectVersion != 2 ||
		dedupeKey != "metadata_applied:metadata:anime-meta-complete:v2:metadata_applied" {
		t.Fatalf("unexpected metadata Activity envelope: count=%d kind=%q outcome=%q reason=%q subject=%s/%s/v%d dedupe=%q",
			appliedLogs, activityKind, activityOutcome, reasonCode, subjectType, subjectID, subjectVersion, dedupeKey)
	}

	counts, err = dispatcher.dispatchMetadataMatch(ctx)
	if err != nil {
		t.Fatalf("second metadata dispatch: %v", err)
	}
	if counts.targetCount != 0 || counts.enqueued != 0 || counts.skipped != 0 {
		t.Fatalf("complete metadata must leave no target: %+v", counts)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = 'sub-meta-complete' AND kind = 'metadata_applied'`).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 1 {
		t.Fatalf("second metadata cycle must not duplicate metadata_applied log, got %d", appliedLogs)
	}
}

func TestMetadataMatchPartialResultPersistsBGMAlone(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-partial", "sub-meta-partial", "Only BGM")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:              db,
		WorkItems:       store,
		MetadataMatcher: matcher.NewMatcher(matcher.NewBangumiDataIndex([]matcher.DataItem{{BgmID: "bgm-only", Titles: []string{"Only BGM"}}}), nil, nil, nil),
		ActivityWriter:  activityWriter,
	}

	counts, err := dispatcher.dispatchMetadataMatch(ctx)
	if err != nil {
		t.Fatalf("partial metadata dispatch: %v", err)
	}
	if counts.targetCount != 1 || counts.enqueued != 0 || counts.errors != 0 {
		t.Fatalf("partial metadata must persist without enqueue: %+v", counts)
	}

	var bgm, tmdb, kind sql.NullString
	var season sql.NullInt64
	var version int
	if err := db.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, version
FROM anime_metadata WHERE managed_anime_id = 'anime-meta-partial'`).Scan(&bgm, &tmdb, &kind, &season, &version); err != nil {
		t.Fatal(err)
	}
	if bgm.String != "bgm-only" || tmdb.Valid || kind.Valid || season.Valid || version != 2 {
		t.Fatalf("BGM alone must be persisted, TMDB tuple untouched: bgm=%v tmdb=%v kind=%v season=%v version=%d", bgm, tmdb, kind, season, version)
	}
	// The next cycle re-queries only the missing TMDB side and never rewrites BGM.
	if _, err := dispatcher.dispatchMetadataMatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT bgm_work_id, version FROM anime_metadata WHERE managed_anime_id = 'anime-meta-partial'`).Scan(&bgm, &version); err != nil {
		t.Fatal(err)
	}
	if bgm.String != "bgm-only" || version != 2 {
		t.Fatalf("second cycle must not rewrite: bgm=%v version=%d", bgm, version)
	}
}

func TestMetadataMatchIgnoresPendingAndStoppedSubscriptions(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	for _, state := range []string{"pending", "stopped"} {
		insertMetadataSubscriptionFixture(t, db, "anime-meta-"+state, "sub-meta-"+state, "Show")
		if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = $2 WHERE id = $1`, "sub-meta-"+state, state); err != nil {
			t.Fatalf("set %s: %v", state, err)
		}
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:              db,
		WorkItems:       store,
		MetadataMatcher: matcher.NewMatcher(matcher.NewBangumiDataIndex([]matcher.DataItem{{BgmID: "bgm-1", TmdbID: "tv/2", Titles: []string{"Show"}}}), nil, nil, nil),
	}
	counts, err := dispatcher.dispatchMetadataMatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.targetCount != 0 {
		t.Fatalf("pending and stopped must not be targets: %+v", counts)
	}
	var changed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_metadata WHERE version <> 1 OR bgm_work_id IS NOT NULL`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("pending and stopped metadata must stay untouched, %d changed", changed)
	}
}

func TestMetadataBackfillFillsEmptyNeverOverwritesAndHonoursCAS(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-fill", "sub-meta-fill", "Fill")
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	match := &matcher.MatchResult{BgmID: str("b1"), TotalEpisodes: num(12), TmdbWorkID: str("t1"), TmdbKind: str("tv"), TmdbSeason: num(2), TmdbYear: num(2024)}

	apply := func(version int64) bool {
		t.Helper()
		var stored backfill.Stored
		var bgm, tmdb, kind, ownerKind sql.NullString
		var season, eps, year sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, total_episodes, year, media_kind_override FROM anime_metadata WHERE managed_anime_id='anime-meta-fill'`).Scan(&bgm, &tmdb, &kind, &season, &eps, &year, &ownerKind); err != nil {
			t.Fatal(err)
		}
		if ownerKind.Valid {
			stored.MediaKind = &ownerKind.String
		}
		if bgm.Valid {
			stored.BGMWorkID = &bgm.String
		}
		if tmdb.Valid && kind.Valid && season.Valid {
			s := int(season.Int64)
			stored.TMDBWorkID, stored.TMDBKind, stored.TMDBSeason = &tmdb.String, &kind.String, &s
		}
		if eps.Valid {
			e := int(eps.Int64)
			stored.TotalEpisodes = &e
		}
		if year.Valid {
			y := int(year.Int64)
			stored.Year = &y
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		plan := backfill.Merge(stored, match)
		ok, err := backfill.ApplyTx(ctx, tx, "anime-meta-fill", version, &plan, match)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Stale version: nothing is written.
	if apply(99) {
		t.Fatal("CAS conflict must not write")
	}
	var version int
	var bgm sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT version, bgm_work_id FROM anime_metadata WHERE managed_anime_id='anime-meta-fill'`).Scan(&version, &bgm); err != nil || version != 1 || bgm.Valid {
		t.Fatalf("CAS conflict wrote: v%d bgm=%v err=%v", version, bgm, err)
	}
	if !apply(1) {
		t.Fatal("empty metadata must be filled")
	}
	var eps, year, season int
	var tmdb, ownerKind string
	if err := db.QueryRowContext(ctx, `SELECT version, bgm_work_id, total_episodes, year, tmdb_work_id, tmdb_season, media_kind_override FROM anime_metadata WHERE managed_anime_id='anime-meta-fill'`).Scan(&version, &bgm, &eps, &year, &tmdb, &season, &ownerKind); err != nil {
		t.Fatal(err)
	}
	// Binding the TMDB tuple backfills the unset owner kind from the tuple kind.
	if version != 2 || bgm.String != "b1" || eps != 12 || year != 2024 || tmdb != "t1" || season != 2 || ownerKind != "tv" {
		t.Fatalf("unexpected fill: v%d %v %d %d %s %d kind=%s", version, bgm, eps, year, tmdb, season, ownerKind)
	}
	// A different result never overwrites stored values.
	match = &matcher.MatchResult{BgmID: str("b2"), TotalEpisodes: num(24), TmdbWorkID: str("t2"), TmdbKind: str("movie"), TmdbSeason: num(0), TmdbYear: num(2000)}
	if apply(2) {
		t.Fatal("full metadata must not be rewritten")
	}
	if err := db.QueryRowContext(ctx, `SELECT version, bgm_work_id, total_episodes, year, media_kind_override FROM anime_metadata WHERE managed_anime_id='anime-meta-fill'`).Scan(&version, &bgm, &eps, &year, &ownerKind); err != nil {
		t.Fatal(err)
	}
	if version != 2 || bgm.String != "b1" || eps != 12 || year != 2024 || ownerKind != "tv" {
		t.Fatalf("overwrite happened: v%d %v %d %d kind=%s", version, bgm, eps, year, ownerKind)
	}
}

func TestMetadataMatchNoResultKeepsVersionForNextCycle(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-empty", "sub-meta-empty", "Unknown")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:              db,
		WorkItems:       store,
		MetadataMatcher: matcher.NewMatcher(matcher.NewBangumiDataIndex(nil), nil, nil, nil),
	}
	for cycle := 0; cycle < 2; cycle++ {
		counts, err := dispatcher.dispatchMetadataMatch(ctx)
		if err != nil {
			t.Fatalf("metadata dispatch cycle %d: %v", cycle+1, err)
		}
		if counts.targetCount != 1 || counts.skipped != 1 || counts.enqueued != 0 || counts.errors != 0 {
			t.Fatalf("unmatched metadata cycle %d counts: %+v", cycle+1, counts)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, `
SELECT version FROM anime_metadata WHERE managed_anime_id = 'anime-meta-empty'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("unmatched metadata must not advance version, got %d", version)
	}
	var appliedLogs int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = 'sub-meta-empty' AND kind = 'metadata_applied'`).Scan(&appliedLogs); err != nil {
		t.Fatal(err)
	}
	if appliedLogs != 0 {
		t.Fatalf("unmatched metadata must not write metadata_applied log, got %d", appliedLogs)
	}
}

func TestMetadataMatchDisabledScheduleIsNotDue(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-disabled", "sub-meta-disabled", "Show")
	if _, err := db.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = FALSE, next_run_at = NULL
WHERE kind = 'metadata_match'`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:              db,
		WorkItems:       store,
		MetadataMatcher: matcher.NewMatcher(matcher.NewBangumiDataIndex([]matcher.DataItem{{BgmID: "bgm-123", TmdbID: "tv/456", Titles: []string{"Show"}}}), nil, nil, nil),
	}
	dispatched, err := dispatcher.DispatchDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dispatched != 0 {
		t.Fatalf("disabled metadata schedule must not dispatch, got %d", dispatched)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT version FROM anime_metadata WHERE managed_anime_id = 'anime-meta-disabled'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("disabled schedule must not update metadata, got version %d", version)
	}
}

// The scheduled backfill never turns an instance with files into a movie: the
// matched movie TMDB tuple is still bound and the unset owner kind (tv) stays
// unset, while the same match on an instance without files fills movie.
func TestMetadataMatchKeepsABlockedMediaKindAndFillsTheRest(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-kind-held", "sub-kind-held", "Show")
	insertMetadataSubscriptionFixture(t, db, "anime-kind-free", "sub-kind-free", "Other")
	if _, err := db.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, verification_state, replacement_eligible)
VALUES ('plc-kind-held', '/library/plc-kind-held.mkv', 'anime-kind-held', 'reconciliation', 'scan:plc-kind-held', 'source', 'target', 'facts-v1', 'verified', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-kind-held', 'tv:e1', 0, 'facts-v1', 'plc-kind-held')`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{
		DB:        db,
		WorkItems: store,
		MetadataMatcher: matcher.NewMatcher(matcher.NewBangumiDataIndex([]matcher.DataItem{
			{BgmID: "bgm-held", TmdbID: "movie/41", Titles: []string{"Show"}},
			{BgmID: "bgm-free", TmdbID: "movie/42", Titles: []string{"Other"}},
		}), nil, nil, nil),
		ActivityWriter: activityWriter,
	}
	counts, err := dispatcher.dispatchMetadataMatch(ctx)
	if err != nil {
		t.Fatalf("metadata dispatch: %v", err)
	}
	if counts.targetCount != 2 || counts.enqueued != 2 || counts.errors != 0 {
		t.Fatalf("unexpected metadata dispatch counts: %+v", counts)
	}
	for _, want := range []struct {
		anime string
		bgm   string
		kind  sql.NullString
	}{
		{anime: "anime-kind-held", bgm: "bgm-held"},
		{anime: "anime-kind-free", bgm: "bgm-free", kind: sql.NullString{String: "movie", Valid: true}},
	} {
		var bgm, tupleKind sql.NullString
		var kind sql.NullString
		var version int64
		if err := db.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_media_kind, media_kind_override, version
FROM anime_metadata WHERE managed_anime_id = $1`, want.anime).Scan(&bgm, &tupleKind, &kind, &version); err != nil {
			t.Fatal(err)
		}
		if bgm.String != want.bgm || tupleKind.String != "movie" || kind != want.kind || version != 2 {
			t.Fatalf("%s: bgm=%v tuple=%v kind=%v version=%d, want kind %v beside the bound movie tuple",
				want.anime, bgm, tupleKind, kind, version, want.kind)
		}
	}
}

func insertMetadataSubscriptionFixture(t *testing.T, db *sql.DB, animeID, subscriptionID, title string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ($1, $2)`, animeID, title); err != nil {
		t.Fatalf("insert metadata anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'custom', 'Metadata test', 'active')`, subscriptionID, animeID); err != nil {
		t.Fatalf("insert metadata subscription: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id) VALUES ($1)`, animeID); err != nil {
		t.Fatalf("insert metadata owner: %v", err)
	}
}
