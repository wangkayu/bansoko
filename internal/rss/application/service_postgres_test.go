package application

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// observedWorkYear is the year the metadata providers observed for the works a
// fixture binds. The shipped default directory names ${anime.year}, and that
// fact is exactly this observation, so a fixture that expects a rendered target
// states the year its provider search would have returned.
const observedWorkYear = 2025

// boundTVMetadata is the metadata binding a draft carries when its release must
// render a target: the shipped default TV templates name ${anime.year} and
// ${anime.season_folder}, so the fixture binds one TMDB tv work and states the
// owner-edited season the folder renders. observeBoundDraftYear then records
// the year a metadata search would have left behind for the bound works.
func boundTVMetadata() animedomain.AnimeMetadataDraft {
	season := 1
	return animedomain.AnimeMetadataDraft{
		Bgm:    &animedomain.BGMBinding{WorkId: "bgm-1"},
		Tmdb:   &animedomain.TMDBBinding{WorkId: "tmdb-1", MediaKind: animedomain.TMDBMediaKindTv, Season: 1},
		Season: &season,
	}
}

// observeBoundDraftYear sets the owner year of an activated anime. It is the
// only value ${anime.year} renders, so a fixture whose target must carry a year
// states it the way the owner would.
func observeBoundDraftYear(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	animetest.SetOwnerYear(t, animeID, observedWorkYear)
}

type rssFakeSourceFetcher struct {
	items []acquisitiondomain.FeedItem
	err   error
	calls int
}

func (f *rssFakeSourceFetcher) FetchSource(_ context.Context, _ SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	f.calls++
	return f.items, f.err
}

func openRSSDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path.
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

func newRSSService(t *testing.T, db *sql.DB, store *workitems.Store, sourceFetcher SourceFetcher) (*Service, error) {
	t.Helper()
	service, err := NewService(db, store, sourceFetcher)
	if err != nil {
		return nil, err
	}
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		return nil, err
	}
	service.SetActivityWriter(writer)
	return service, nil
}

func seedRSSOwner(t *testing.T, db *sql.DB, stopped bool, metadataComplete bool) (string, string) {
	t.Helper()
	state := "active"
	if stopped {
		state = "stopped"
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-rss-test', 'Test Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-rss-test', 'anime-rss-test', 'custom', 'Test', $1)`, state); err != nil {
		t.Fatal(err)
	}
	// Every production instance owns a frozen parser policy; the automatic path
	// now requires it rather than falling back to regex. Fixtures that
	// bypass Subscribe must seed it explicitly, exactly like the real creation
	// port does.
	seedInstanceParserPolicy(t, db, "anime-rss-test")
	if metadataComplete {
		if _, err := db.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, season)
VALUES ('anime-rss-test', 'bgm-1', 'tmdb-1', 'tv', 1, 1)`); err != nil {
			t.Fatal(err)
		}
		// The shipped default directory names ${anime.year}, which is the owner
		// year on the metadata row and never a provider year or a year read off a
		// release title.
		animetest.SetOwnerYear(t, "anime-rss-test", observedWorkYear)
	} else if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-rss-test')`); err != nil {
		t.Fatal(err)
	}
	for range []string{"subgroups", "rules"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-rss-test') ON CONFLICT (managed_anime_id) DO NOTHING`); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "anime-rss-test")
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-rss-test', 'sub-rss-test', 'custom_rss', 'https://feeds.example.test/rss.xml', 'https://feeds.example.test/rss.xml', 'Test Group', 'Test Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-rss-test')`); err != nil {
		t.Fatal(err)
	}
	seedRSSBusiness(t, db, "anime-rss-test")
	return "source-rss-test", "sub-rss-test"
}

func createRSSClaim(t *testing.T, db *sql.DB, store *workitems.Store, owner workitems.OwnerRef, kind string, version int64) workitems.Claim {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Create(context.Background(), tx, workitems.WorkItemSpec{
		Owner: owner, Kind: kind, IdempotencyKey: kind + ":test-owner", InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 3, ExpectedOwnerVersion: version,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(context.Background(), "rss-test-worker", []string{kind}, time.Minute)
	if err != nil || claim.ID != id {
		t.Fatalf("claim: id=%s claim=%+v err=%v%s", id, claim, err, describeWorkItem(db, id))
	}
	return claim
}

// describeWorkItem reports the row a failed claim was supposed to lease. The
// claim runs immediately after a committed Create, so ErrNoDueWork here means
// the row was invisible, not due, or locked -- and the bare error says which
// of those it was. This has fired once without reproducing; the dump is what
// makes the next occurrence diagnosable instead of another guess.
func describeWorkItem(db *sql.DB, id string) string {
	var state, availableAt, leaseUntil, dbNow string
	err := db.QueryRowContext(context.Background(), `
SELECT state, available_at::text, COALESCE(lease_until::text, 'null'), CURRENT_TIMESTAMP::text
FROM work_items WHERE id = $1`, id).Scan(&state, &availableAt, &leaseUntil, &dbNow)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Sprintf(" row=absent (id %s is not in work_items)", id)
	}
	if err != nil {
		return fmt.Sprintf(" row=unreadable (%v)", err)
	}
	return fmt.Sprintf(" row={state:%s available_at:%s lease_until:%s} db_now=%s", state, availableAt, leaseUntil, dbNow)
}

func TestRefreshAndEvaluateCustomRSSFullFeed(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	torrentPayload := []byte("d4:infod4:name11:show-01.mkv6:lengthi1eee")
	torrentMetadata, err := torrent.Parse(torrentPayload)
	if err != nil {
		t.Fatal(err)
	}
	fileSize := torrentMetadata.Files[0].SizeBytes
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "guid-1", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/show-01.torrent",
		TorrentFilename: torrentMetadata.TorrentName, TorrentInfoHash: torrentMetadata.InfoHashV1, TorrentInfoHashV1: torrentMetadata.InfoHashV1,
		TorrentVersion: torrentMetadata.TorrentVersion, TorrentSizeBytes: torrentMetadata.TotalSize, TorrentPayload: torrentPayload,
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{
			SourcePath: torrentMetadata.Files[0].Path, Filename: torrentMetadata.Files[0].Name,
			SizeBytes: &fileSize, MediaKind: torrentMetadata.Files[0].MediaKind, SourceHash: torrentMetadata.InfoHashV1,
		}},
	}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refreshClaim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	refreshOutcome, err := service.Handle(context.Background(), refreshClaim)
	if err != nil || refreshOutcome.Kind != worker.OutcomeFinish {
		t.Fatalf("refresh outcome=%+v err=%v", refreshOutcome, err)
	}
	if err := store.FinishWithResult(context.Background(), refreshClaim, workitems.StateSucceeded, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var refreshActivityCount int
	var refreshKind, refreshReason, refreshSubjectType, refreshSubjectID, refreshSourceID, refreshGroupRef, refreshDedupe string
	var refreshVersion int64
	var refreshDetails string
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*), MIN(kind), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version),
       MIN(COALESCE(rss_source_id, '')), MIN(COALESCE(group_ref, '')), MIN(dedupe_key),
       (array_agg(details::text ORDER BY id))[1]
FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'rss_new_content'`, subscriptionID).Scan(
		&refreshActivityCount, &refreshKind, &refreshReason, &refreshSubjectType, &refreshSubjectID, &refreshVersion,
		&refreshSourceID, &refreshGroupRef, &refreshDedupe, &refreshDetails); err != nil {
		t.Fatal(err)
	}
	if refreshActivityCount != 1 || refreshKind != "rss_new_content" || refreshReason != "rss_new_content" ||
		refreshSubjectType != "rss_source" || refreshSubjectID != sourceID || refreshVersion < 1 ||
		refreshSourceID != sourceID || refreshGroupRef != "Test Group" ||
		!strings.HasPrefix(refreshDedupe, "rss_new_content:rss_source:"+sourceID+":v") ||
		!strings.Contains(string(refreshDetails), `"item_count": 1`) {
		t.Fatalf("unexpected refresh Activity: count=%d kind=%q reason=%q subject=%s/%s/v%d source=%q group=%q dedupe=%q details=%s",
			refreshActivityCount, refreshKind, refreshReason, refreshSubjectType, refreshSubjectID, refreshVersion,
			refreshSourceID, refreshGroupRef, refreshDedupe, refreshDetails)
	}
	var releaseID, verdict string
	if err := db.QueryRowContext(context.Background(), `SELECT id, verdict FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID, &verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "accepted_pending_download" {
		t.Fatalf("refresh should persist the shared evaluator verdict, got %q", verdict)
	}
	var evalClaim workitems.Claim
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM work_items WHERE owner_type = 'release' AND owner_id = $1 AND state = 'available'`, releaseID).Scan(&evalClaim.ID); err != nil {
		t.Fatal(err)
	}
	// The work item was created by refresh and is claimed through the store to
	// exercise the same typed release handler boundary.
	evalClaim, err = store.ClaimDue(context.Background(), "rss-test-worker-2", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), evalClaim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("evaluate outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(context.Background(), evalClaim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var taskCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("accepted release should create one download task, got %d", taskCount)
	}
	var activityCount int
	var activityKind, activityReason, activitySubjectType, activitySubjectID, activitySourceID, activityReleaseID, activityGroupRef, activityDedupe, activityMessage string
	var activityVersion int64
	var activityDetails []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*), MIN(kind), MIN(reason_code), MIN(subject_type), MIN(subject_id), MIN(subject_version),
       MIN(COALESCE(rss_source_id, '')), MIN(COALESCE(release_id, '')), MIN(COALESCE(group_ref, '')), MIN(dedupe_key),
       (array_agg(details::text ORDER BY id))[1], MIN(message_code)
FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'release_processed'`, subscriptionID).Scan(
		&activityCount, &activityKind, &activityReason, &activitySubjectType, &activitySubjectID, &activityVersion,
		&activitySourceID, &activityReleaseID, &activityGroupRef, &activityDedupe, &activityDetails, &activityMessage); err != nil {
		t.Fatal(err)
	}
	if activityCount != 1 || activityKind != "release_processed" || activityReason != "accepted" || activityMessage != "release_accepted" ||
		activitySubjectType != "release" || activitySubjectID != releaseID || activityVersion < 1 ||
		activitySourceID != sourceID || activityReleaseID != releaseID || activityGroupRef != "Test Group" ||
		!strings.HasPrefix(activityDedupe, "release_processed:release:"+releaseID+":v") ||
		strings.Contains(string(activityDetails), "downloads.example.test") || strings.Contains(string(activityDetails), "magnet:") ||
		strings.Contains(string(activityDetails), "source_url") || strings.Contains(string(activityDetails), "torrent") {
		t.Fatalf("unexpected release Activity: count=%d kind=%q reason=%q subject=%s/%s/v%d source=%q release=%q group=%q dedupe=%q details=%s",
			activityCount, activityKind, activityReason, activitySubjectType, activitySubjectID, activityVersion,
			activitySourceID, activityReleaseID, activityGroupRef, activityDedupe, activityDetails)
	}
	var frozenInput struct {
		SelectedRelease struct {
			ReleaseID      string `json:"release_id"`
			RSSSourceID    string `json:"rss_source_id"`
			StableItemKey  string `json:"stable_item_key"`
			ReleaseVersion int64  `json:"release_version"`
			SubscriptionID string `json:"subscription_id"`
			ManagedAnimeID string `json:"managed_anime_id"`
		} `json:"selected_release"`
		TorrentInputID       string            `json:"torrent_input_id"`
		TorrentPayloadDigest string            `json:"torrent_payload_digest"`
		TorrentFilename      string            `json:"torrent_filename"`
		TorrentParserVersion string            `json:"torrent_parser_version"`
		TorrentVersion       string            `json:"torrent_version"`
		InfoHashV1           string            `json:"info_hash_v1"`
		InfoHashV2           string            `json:"info_hash_v2"`
		ReleaseFiles         []json.RawMessage `json:"release_files"`
	}
	var publicInput []byte
	if err := db.QueryRowContext(context.Background(), `SELECT frozen_input FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&publicInput); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(publicInput, &frozenInput); err != nil {
		t.Fatal(err)
	}
	if frozenInput.TorrentInputID == "" || frozenInput.TorrentPayloadDigest == "" || frozenInput.TorrentFilename != "show-01.mkv.torrent" ||
		frozenInput.TorrentParserVersion != torrentInputParserVersion || frozenInput.TorrentVersion != torrentMetadata.TorrentVersion ||
		frozenInput.InfoHashV1 != torrentMetadata.InfoHashV1 || frozenInput.InfoHashV2 != torrentMetadata.InfoHashV2 || strings.Contains(string(publicInput), `"source_url"`) {
		t.Fatalf("frozen input must reference one validated payload without a URL effect field, got %+v raw=%s", frozenInput, publicInput)
	}
	if len(frozenInput.ReleaseFiles) != 1 {
		t.Fatalf("frozen input must retain the parsed torrent file list, got %d files: %s", len(frozenInput.ReleaseFiles), publicInput)
	}
	if frozenInput.SelectedRelease.ReleaseID != releaseID || frozenInput.SelectedRelease.RSSSourceID != sourceID || frozenInput.SelectedRelease.SubscriptionID != subscriptionID || frozenInput.SelectedRelease.ManagedAnimeID == "" || frozenInput.SelectedRelease.StableItemKey == "" || frozenInput.SelectedRelease.ReleaseVersion < 1 {
		t.Fatalf("frozen input must identify the exact selected release and owner chain: %+v", frozenInput.SelectedRelease)
	}
	var frozenPolicyRaw []byte
	if err := db.QueryRowContext(context.Background(), `SELECT frozen_policy FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&frozenPolicyRaw); err != nil {
		t.Fatal(err)
	}
	var frozenPolicy frozenPolicySnapshot
	if err := json.Unmarshal(frozenPolicyRaw, &frozenPolicy); err != nil {
		t.Fatal(err)
	}
	if frozenPolicy.ConfigIdentity.IdentityKind != "instance_materialized" || frozenPolicy.ConfigIdentity.ManagedAnimeID == nil || *frozenPolicy.ConfigIdentity.ManagedAnimeID != frozenInput.SelectedRelease.ManagedAnimeID || frozenPolicy.ConfigIdentity.CandidateID != nil || frozenPolicy.ConfigIdentity.Revision != nil || frozenPolicy.EffectiveDigest == "" || len(frozenPolicy.SectionVersions) != len(animedomain.EffectiveSectionOrder) || len(frozenPolicy.SectionSchemas) != len(animedomain.EffectiveSectionOrder) {
		t.Fatalf("ordinary admission must freeze the complete materialized config: %+v raw=%s", frozenPolicy, frozenPolicyRaw)
	}
	var effectiveSections map[string]json.RawMessage
	if err := json.Unmarshal(frozenPolicy.EffectiveSections, &effectiveSections); err != nil || len(effectiveSections) != len(animedomain.EffectiveSectionOrder) {
		t.Fatalf("ordinary frozen effective sections are incomplete: sections=%v err=%v", effectiveSections, err)
	}
	var persistedPayload []byte
	if err := db.QueryRowContext(context.Background(), `SELECT payload FROM release_torrent_inputs WHERE id = $1`, frozenInput.TorrentInputID).Scan(&persistedPayload); err != nil {
		t.Fatal(err)
	}
	if string(persistedPayload) != string(torrentPayload) {
		t.Fatalf("persisted torrent payload differs: got %q want %q", persistedPayload, torrentPayload)
	}
}

func TestRefreshUnsupportedLocatorPersistsStableRuntimeReasonWithoutFetch(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	rawLocator := "opaque://feed/DO_NOT_DISCLOSE_SECRET_MARKER"
	if _, err := db.ExecContext(context.Background(), `UPDATE rss_sources SET locator = $2 WHERE id = $1`, sourceID, rawLocator); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.OwnerResult.ReasonCode != "RSS_LOCATOR_SCHEME_UNSUPPORTED" {
		t.Fatalf("unexpected unsupported refresh outcome=%+v err=%v", outcome, err)
	}
	if fake.calls != 0 {
		t.Fatalf("unsupported refresh reached fetcher %d time(s)", fake.calls)
	}
	if err := store.FinishWithResult(context.Background(), claim, workitems.StateFailed, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	// A refresh failure is a diagnostic condition, not a journal entry: the
	// failed refresh writes no subscription log entry.
	var activityCount int
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM subscription_log_entries
WHERE subscription_id = 'sub-rss-test' AND (subject_type = 'rss_source' OR rss_source_id = $1)`, sourceID).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if activityCount != 0 {
		t.Fatalf("a refresh failure wrote %d journal entries, want none", activityCount)
	}
	var health, reasonCode, reasonDetail string
	if err := db.QueryRowContext(context.Background(), `
SELECT health, reason_code, reason_detail FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&health, &reasonCode, &reasonDetail); err != nil {
		t.Fatal(err)
	}
	if health != "failed" || reasonCode != "RSS_LOCATOR_SCHEME_UNSUPPORTED" || reasonDetail != "RSS locator scheme is not supported for execution" {
		t.Fatalf("unexpected persisted runtime reason: health=%q code=%q detail=%q", health, reasonCode, reasonDetail)
	}
	if strings.Contains(reasonDetail, rawLocator) || strings.Contains(reasonDetail, "DO_NOT_DISCLOSE_SECRET_MARKER") {
		t.Fatalf("runtime reason leaked raw locator: %q", reasonDetail)
	}
}

func TestQueuedRefreshRechecksSourceEligibilityBeforeFetch(t *testing.T) {
	tests := []struct {
		name       string
		provider   bool
		selected   *bool
		delete     bool
		stopped    bool
		wantReason string
		wantCalls  int
	}{
		{name: "provider selection missing", provider: true, wantReason: "policy_not_selected"},
		{name: "provider selection false", provider: true, selected: rssBoolPointer(false), wantReason: "policy_not_selected"},
		{name: "provider selection deleted", provider: true, selected: rssBoolPointer(true), delete: true, wantReason: "policy_not_selected"},
		{name: "subscription stopped", stopped: true, wantReason: "subscription_stopped"},
		{name: "provider selected", provider: true, selected: rssBoolPointer(true), wantReason: "refresh_succeeded", wantCalls: 1},
		{name: "active custom", wantReason: "refresh_succeeded", wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
			ctx := context.Background()
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
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
				    manual_selected, effective_selected, effective_score) VALUES ('selection-rss-test', $1, 'anibt', 'alpha', 'Alpha', 'auto', NULL, $2, 50)`, subscriptionID, *test.selected); err != nil {
					t.Fatal(err)
				}
			}
			if test.delete {
				if _, err := db.ExecContext(ctx, `DELETE FROM provider_group_selections WHERE subscription_id = $1`, subscriptionID); err != nil {
					t.Fatal(err)
				}
			}
			if test.stopped {
				if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'stopped' WHERE id = $1`, subscriptionID); err != nil {
					t.Fatal(err)
				}
			}
			sourceFetcher := &rssFakeSourceFetcher{}
			service, err := newRSSService(t, db, store, sourceFetcher)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := service.Handle(ctx, claim)
			if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded || outcome.OwnerResult.ReasonCode != test.wantReason {
				t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
			}
			if sourceFetcher.calls != test.wantCalls {
				t.Fatalf("fetch calls source=%d, want %d", sourceFetcher.calls, test.wantCalls)
			}
			if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id = $1`, claim.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != workitems.StateSucceeded {
				t.Fatalf("work item state=%q, want succeeded", state)
			}
		})
	}
}

func TestRefreshPersistsLinkEpisodeOffset(t *testing.T) {
	tests := []struct {
		name   string
		offset int
		want   int
	}{
		{name: "zero keeps the episode", offset: 0, want: 1},
		{name: "positive adds", offset: 3, want: 4},
		{name: "negative subtracts", offset: -1, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, _ := seedRSSOwner(t, db, false, true)
			ctx := context.Background()
			// The RSS link's episode_offset is the one offset authority.
			seedInstanceParserPolicy(t, db, "anime-rss-test")
			if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET episode_offset = $2 WHERE id = $1`, sourceID, test.offset); err != nil {
				t.Fatal(err)
			}
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{GUID: "offset-" + test.name, Title: "Test Show - 01"}}}
			service, err := newRSSService(t, db, store, fake)
			if err != nil {
				t.Fatal(err)
			}
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
			outcome, err := service.Handle(ctx, claim)
			if err != nil || outcome.Kind != worker.OutcomeFinish {
				t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
			}
			if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			var episode int
			if err := db.QueryRowContext(ctx, `SELECT (parsed_facts->>'episode')::int FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&episode); err != nil {
				t.Fatal(err)
			}
			if episode != test.want {
				t.Fatalf("persisted episode=%d, want %d", episode, test.want)
			}
		})
	}
}

func rssBoolPointer(value bool) *bool { return &value }
func rssIntPointer(value int) *int    { return &value }

func TestRefreshFailureOpensAndSuccessClosesDiagnosticEpisode(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator = 'opaque://feed/DO_NOT_DISCLOSE' WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDiagnosticRecorder(recorder, nil)

	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("expected terminal refresh failure: outcome=%+v err=%v", outcome, err)
	}
	if outcome.AfterCommit == nil {
		t.Fatal("terminal refresh failure must carry the diagnostic episode after-commit work")
	}
	if err := store.FinishWithResult(ctx, claim, workitems.StateFailed, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	outcome.AfterCommit(ctx)

	var kind, targetID string
	var occurrences int
	var succeededAfter sql.NullTime
	if err := db.QueryRowContext(ctx, `
SELECT kind, target_id, occurrence_count, succeeded_after_at
FROM diagnostic_episodes`).Scan(&kind, &targetID, &occurrences, &succeededAfter); err != nil {
		t.Fatalf("read diagnostic episode: %v", err)
	}
	if kind != "internal" || targetID != "rss.refresh" || occurrences != 1 || succeededAfter.Valid {
		t.Fatalf("unexpected open episode: %s/%s count=%d succeeded=%v", kind, targetID, occurrences, succeededAfter)
	}

	// A later successful refresh closes the open episode for the same identity.
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator = 'https://feeds.example.test/rss.xml' WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
		Kind:  workitems.KindRSSRefresh, IdempotencyKey: "rss_refresh:test-owner-recovered",
		InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 3, ExpectedOwnerVersion: 2,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("create recovery refresh: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	recoveryClaim, err := store.ClaimDue(ctx, "rss-test-worker", []string{workitems.KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recoveryOutcome, err := service.Handle(ctx, recoveryClaim)
	if err != nil || recoveryOutcome.Kind != worker.OutcomeFinish || recoveryOutcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("expected successful recovery refresh: outcome=%+v err=%v", recoveryOutcome, err)
	}
	if recoveryOutcome.AfterCommit == nil {
		t.Fatal("successful refresh must carry the diagnostic close after-commit work")
	}
	if err := store.FinishWithResult(ctx, recoveryClaim, workitems.StateSucceeded, recoveryOutcome.OwnerResult, recoveryOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	recoveryOutcome.AfterCommit(ctx)

	if err := db.QueryRowContext(ctx, `SELECT succeeded_after_at FROM diagnostic_episodes`).Scan(&succeededAfter); err != nil {
		t.Fatal(err)
	}
	if !succeededAfter.Valid {
		t.Fatal("successful refresh must close the open diagnostic episode")
	}
}

func TestRefreshTransientTorrentFailurePreservesVerifiedMetadataAndRequeuesEvaluation(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("d4:infod4:name11:show-01.mkv6:lengthi1eee")
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	fileSize := metadata.Files[0].SizeBytes
	item := acquisitiondomain.FeedItem{
		GUID: "guid-reconcile", Title: "[Group] Test Show - 01 1080p", Enclosure: "https://downloads.example.test/show-01.torrent", TorrentURL: "https://downloads.example.test/show-01.torrent",
		TorrentFilename: metadata.TorrentName, TorrentInfoHash: metadata.InfoHashV1, TorrentInfoHashV1: metadata.InfoHashV1, TorrentVersion: metadata.TorrentVersion, TorrentSizeBytes: metadata.TotalSize, TorrentPayload: payload,
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{SourcePath: metadata.Files[0].Path, Filename: metadata.Files[0].Name, SizeBytes: &fileSize, MediaKind: metadata.Files[0].MediaKind}},
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{item}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(version int64) {
		t.Helper()
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(context.Background(), claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}
	refresh(1)
	var releaseID string
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	var releaseVersion, evaluationVersion int64
	if err := db.QueryRowContext(context.Background(), `SELECT r.version,w.expected_owner_version FROM releases r JOIN work_item_history w ON w.owner_id=r.id AND w.owner_type='release' AND w.kind='release_evaluate' WHERE r.id=$1`, releaseID).Scan(&releaseVersion, &evaluationVersion); err != nil {
		t.Fatal(err)
	}
	if releaseVersion != evaluationVersion {
		t.Fatalf("refresh scheduled stale evaluation: release=%d evaluation=%d", releaseVersion, evaluationVersion)
	}
	failure := item
	failure.TorrentPayload = nil
	failure.ReleaseFiles = nil
	failure.TorrentInfoHash = ""
	failure.TorrentInfoHashV1 = ""
	failure.TorrentVersion = ""
	failure.TorrentFilename = ""
	failure.TorrentSizeBytes = 0
	failure.TorrentErrorCode = "torrent_metadata_unavailable"
	failure.TorrentErrorReason = "temporary metadata outage"
	fake.items = []acquisitiondomain.FeedItem{failure}
	refresh(2)

	var internalRaw []byte
	var reasonCode, reasonDetail string
	if err := db.QueryRowContext(context.Background(), `SELECT internal_facts, reason_code, reason_detail FROM releases WHERE id = $1`, releaseID).Scan(&internalRaw, &reasonCode, &reasonDetail); err != nil {
		t.Fatal(err)
	}
	var stored storedFeedFacts
	if err := json.Unmarshal(internalRaw, &stored); err != nil {
		t.Fatal(err)
	}
	if reasonCode != "torrent_metadata_unavailable" || reasonDetail != "temporary metadata outage" || stored.TorrentInfoHashV1 != metadata.InfoHashV1 || len(stored.ReleaseFiles) != 1 {
		t.Fatalf("failure refresh lost verified group or diagnostics: reason=%q/%q facts=%+v", reasonCode, reasonDetail, stored)
	}
	var evalState string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM work_item_history WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&evalState); err != nil {
		t.Fatal(err)
	}
	if evalState != workitems.StateAvailable {
		t.Fatalf("reused metadata evaluation state=%q, want available", evalState)
	}

	claim, err := store.ClaimDue(context.Background(), "rss-reconcile-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("reused metadata evaluation outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}

	var recovered storedFeedFacts
	var recoveredReason string
	var recoveredDetail sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT internal_facts, reason_code, reason_detail FROM releases WHERE id = $1`, releaseID).Scan(&internalRaw, &recoveredReason, &recoveredDetail); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(internalRaw, &recovered); err != nil {
		t.Fatal(err)
	}
	if recoveredReason != "accepted" || recoveredDetail.Valid || recovered.TorrentErrorCode != "" || recovered.TorrentErrorReason != "" || recovered.TorrentInfoHashV1 != metadata.InfoHashV1 || len(recovered.ReleaseFiles) != 1 {
		t.Fatalf("evaluator did not clear transient error in persisted facts: reason=%q/%v facts=%+v", recoveredReason, recoveredDetail, recovered)
	}

	var frozenRaw []byte
	if err := db.QueryRowContext(context.Background(), `SELECT frozen_input FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&frozenRaw); err != nil {
		t.Fatal(err)
	}
	var frozen internalFrozenDownloadInput
	if err := json.Unmarshal(frozenRaw, &frozen); err != nil {
		t.Fatal(err)
	}
	if len(frozen.ReleaseFiles) != 1 || frozen.InfoHashV1 != metadata.InfoHashV1 {
		t.Fatalf("reused metadata frozen input=%+v, want one file and hash %q", frozen, metadata.InfoHashV1)
	}

	// Once the download task exists, a later successful refresh must not replace
	// the release facts or the validated torrent input used by that task.
	payloadB := []byte("d4:infod4:name15:show-02.torrent6:lengthi1eee")
	metadataB, err := torrent.Parse(payloadB)
	if err != nil {
		t.Fatal(err)
	}
	fileSizeB := metadataB.Files[0].SizeBytes
	successB := item
	successB.Enclosure = "https://downloads.example.test/show-02.torrent"
	successB.TorrentURL = successB.Enclosure
	successB.TorrentFilename = metadataB.TorrentName
	successB.TorrentInfoHash = metadataB.InfoHashV1
	successB.TorrentInfoHashV1 = metadataB.InfoHashV1
	successB.TorrentVersion = metadataB.TorrentVersion
	successB.TorrentSizeBytes = metadataB.TotalSize
	successB.TorrentPayload = payloadB
	successB.ReleaseFiles = []acquisitiondomain.ReleaseFile{{SourcePath: metadataB.Files[0].Path, Filename: metadataB.Files[0].Name, SizeBytes: &fileSizeB, MediaKind: metadataB.Files[0].MediaKind}}
	fake.items = []acquisitiondomain.FeedItem{successB}
	refresh(3)
	var frozenFacts storedFeedFacts
	var inputFilename, inputHash string
	if err := db.QueryRowContext(context.Background(), `SELECT internal_facts FROM releases WHERE id = $1`, releaseID).Scan(&internalRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(internalRaw, &frozenFacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT filename, COALESCE(info_hash_v1, '') FROM release_torrent_inputs WHERE release_id = $1`, releaseID).Scan(&inputFilename, &inputHash); err != nil {
		t.Fatal(err)
	}
	if frozenFacts.TorrentFilename != metadata.TorrentName || frozenFacts.TorrentInfoHashV1 != metadata.InfoHashV1 || len(frozenFacts.ReleaseFiles) != 1 || inputFilename != metadata.TorrentName+".torrent" || inputHash != metadata.InfoHashV1 {
		t.Fatalf("download-bound release facts were replaced: facts=%+v input=%q/%q", frozenFacts, inputFilename, inputHash)
	}
}

func TestRefreshFirstTransientTorrentFailureDoesNotScheduleEvaluation(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{{GUID: "guid-first-failure", Title: "[Group] Test Show - 01 1080p", TorrentURL: "https://downloads.example.test/show-01.torrent", TorrentErrorCode: "torrent_metadata_unavailable", TorrentErrorReason: "first metadata failure"}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var releaseID string
	var internalRaw []byte
	if err := db.QueryRowContext(context.Background(), `SELECT id, internal_facts FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID, &internalRaw); err != nil {
		t.Fatal(err)
	}
	var facts storedFeedFacts
	if err := json.Unmarshal(internalRaw, &facts); err != nil {
		t.Fatal(err)
	}
	if len(facts.ReleaseFiles) != 0 || facts.TorrentVersion != "" {
		t.Fatalf("first metadata failure unexpectedly has verified facts: %+v", facts)
	}
	var evaluationCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&evaluationCount); err != nil {
		t.Fatal(err)
	}
	if evaluationCount != 0 {
		t.Fatalf("first metadata failure scheduled %d release evaluation items", evaluationCount)
	}
}

// seedInstanceParserPolicy writes the instance parser-policy row that owns the
// runtime parser kind. Hand-written fixtures that bypass Subscribe must seed it
// explicitly: the automatic path requires the row (a missing policy fails
// closed with INSTANCE_PARSER_POLICY_REQUIRED instead of assuming regex).
func seedInstanceParserPolicy(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_business_configs(managed_anime_id, parser_kind, parser_source_default_version, parser_version)
VALUES ($1, 'regex', 1, 1)
ON CONFLICT (managed_anime_id) DO UPDATE
SET parser_version = anime_business_configs.parser_version + 1`,
		animeID); err != nil {
		t.Fatalf("seed instance parser policy: %v", err)
	}
}
