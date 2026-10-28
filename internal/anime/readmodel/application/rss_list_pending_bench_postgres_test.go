package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	rootapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/subscription/groupsource"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
)

// A pending subscription's owner RSS read is the test bench: the owner's own
// configuration evaluated over the supplied feed snapshots, through the same
// compose path a draft preview uses, with no download capability and no write.
// An unsaved rules patch previews on it; once started, the owner view returns.
func TestRSSListPendingOwnerReadIsTheDraftBench(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	_, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	fetcher := readmodelCatalogFeedFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "bench-seed-01", Title: "[Group] Parity Show - 01 [1080p]",
		Enclosure:       "https://torrent.example.test/bench-01.torrent",
		Published:       time.Now().UTC().Truncate(time.Microsecond),
		TorrentFilename: "Parity Show S1", TorrentVersion: "v1",
		TorrentInfoHashV1: strings.Repeat("b", 40),
		TorrentPayload:    parityShowTorrentPayload(),
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{SourcePath: "Parity Show S1/Parity Show - 01.mkv", Filename: "Parity Show - 01.mkv", SizeBytes: animetest.Int64Ptr(1234), MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		},
	}}}
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetProviderFeedFetcher(fetcher)
	catalogService.SetFeedSnapshotStore(catalogpersistence.NewRepository(db))
	readmodel := newReadmodelService(t, db, root.Kernel, root.Draft, root.Service, rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload))

	const entryRef = "70032"
	const groupRef = "679"
	submitted := readmodelProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, entryRef, groupRef)
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	subgroupRaw, err := json.Marshal(submitted.Subgroups)
	if err != nil {
		t.Fatal(err)
	}
	var subgroupValue animedomain.SubgroupsSection
	if err := json.Unmarshal(subgroupRaw, &subgroupValue); err != nil {
		t.Fatal(err)
	}
	normalized, err := animedomain.SubgroupsSectionWithEffectiveSelections(subgroupValue)
	if err != nil {
		t.Fatal(err)
	}
	if business.Sections[animedomain.SectionSubgroups], err = json.Marshal(normalized); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(business)
	if err != nil {
		t.Fatal(err)
	}
	var wire generated.BusinessSnapshot
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if raw, err = json.Marshal(wire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &business); err != nil {
		t.Fatal(err)
	}
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	submitted.Business = &wire
	submitted.BusinessDigest = &digest
	candidate := saveCandidate(t, root, submitted)
	snapshot, err := catalogService.RefreshProviderEntryRSS(ctx, "mikan", entryRef, groupRef)
	if err != nil {
		t.Fatalf("refresh provider snapshot: %v", err)
	}
	feed := []PreviewFeedSnapshotRef{{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: entryRef, ProviderGroupRef: groupRef,
		SnapshotID: snapshot.SnapshotID, SnapshotVersion: snapshot.SnapshotVersion, ContentHash: snapshot.ContentHash,
	}}
	revision, err := root.GetAnimeConfigCandidateRevision(ctx, string(candidate.Id), 1)
	if err != nil {
		t.Fatal(err)
	}
	draftView, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "current_input", Draft: &revision.Draft, Feed: feed})
	if err != nil {
		t.Fatalf("current_input read: %v", err)
	}

	activated, err := root.Draft.ActivateAnimeConfigCandidate(ctx, string(candidate.Id), candidate.Version, "pending-bench-activate", draftapplication.CandidateSubscribeInput{Revision: 1})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.Subscription.State != "pending" {
		t.Fatalf("state = %s, want pending", activated.Subscription.State)
	}
	animeID := activated.ManagedAnime.ID
	// Group discovery's runtime selection (the dispatcher writes it after commit).
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, effective_selected, effective_score) VALUES ('sel-bench', $1, 'mikan', $2, 'Group 679', 'auto', true, 50)`, activated.Subscription.ID, groupRef); err != nil {
		t.Fatal(err)
	}

	before := animetest.ProductRowCounts(t, db)
	bench, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner read: %v", err)
	}
	if !strings.HasPrefix(bench.ConfigIdentity, "owner_bench:") || len(bench.Groups) != 1 || len(bench.Groups[0].Items) != 1 {
		t.Fatalf("bench view = %+v", bench)
	}
	got, want := bench.Groups[0], draftView.Groups[0]
	if got.Scope.ProviderGroupRef != groupRef || !got.Selection.EffectiveSelected || !reflect.DeepEqual(got.Feed, want.Feed) {
		t.Fatalf("bench group = %+v, draft group = %+v", got, want)
	}
	item := got.Items[0]
	if item.StableSeedKey != want.Items[0].StableSeedKey || item.Evaluation.Verdict != want.Items[0].Evaluation.Verdict || item.Evaluation.Verdict != "accepted_pending_download" {
		t.Fatalf("bench evaluation = %+v, draft = %+v", item.Evaluation, want.Items[0].Evaluation)
	}
	if len(item.Files) != len(want.Items[0].Files) || len(item.Files) == 0 || item.Files[0].RenderedTarget != want.Items[0].Files[0].RenderedTarget {
		t.Fatalf("bench files = %+v, draft = %+v", item.Files, want.Items[0].Files)
	}
	if item.DownloadAction.Allowed || item.DownloadAction.Kind != RSSListDownloadNone || item.DownloadAction.ReasonCode != rssListReasonNotStarted {
		t.Fatalf("bench download action = %+v", item.DownloadAction)
	}

	// Unsaved rules edits preview on the bench exactly as owner_draft does.
	patch := rulesPreviewWithExclude(t, "Parity")
	edited, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, RulesPreview: &patch, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner_draft read: %v", err)
	}
	if len(edited.Groups) != 1 || len(edited.Groups[0].Items) != 1 || edited.Groups[0].Items[0].Evaluation.Verdict != "filtered" {
		t.Fatalf("edited bench = %+v", edited.Groups)
	}
	if edited.ConfigIdentity == bench.ConfigIdentity {
		t.Fatal("the unsaved patch did not change the bench identity")
	}
	// Unsaved metadata edits (year, owner season) render on the bench without a
	// save, alone or together with the rules patch, and the read writes none of
	// them.
	savedMetadata := benchMetadataRow(t, db, animeID)
	year, season := 2031, 3
	// The TMDB binding names its season 1; naming reads only the owner season.
	metadataPreview := MetadataPreviewInput{
		BGM:    &BGMBinding{WorkID: "bgm-bench"},
		TMDB:   &TMDBBinding{WorkID: "tmdb-bench", MediaKind: "tv", Season: 1},
		Year:   &year,
		Season: &season,
	}
	metadataView, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, MetadataPreview: &metadataPreview, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner_draft metadata read: %v", err)
	}
	if len(metadataView.Groups) != 1 || len(metadataView.Groups[0].Items) != 1 || len(metadataView.Groups[0].Items[0].Files) == 0 {
		t.Fatalf("metadata bench = %+v", metadataView.Groups)
	}
	if metadataView.MetadataHold.Held {
		t.Fatalf("the unsaved bindings must clear the hold: %+v", metadataView.MetadataHold)
	}
	file := metadataView.Groups[0].Items[0].Files[0]
	if file.RenderError != "" || !strings.Contains(file.RenderedTarget, "2031") || !strings.Contains(file.RenderedTarget, "S03") {
		t.Fatalf("unsaved year/season must render in the target: %+v", file)
	}
	if metadataView.ConfigIdentity == bench.ConfigIdentity {
		t.Fatal("the unsaved metadata did not change the bench identity")
	}
	both, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, RulesPreview: &patch, MetadataPreview: &metadataPreview, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner_draft rules+metadata read: %v", err)
	}
	if len(both.Groups) != 1 || len(both.Groups[0].Items) != 1 || both.Groups[0].Items[0].Evaluation.Verdict != "filtered" || both.ConfigIdentity == edited.ConfigIdentity {
		t.Fatalf("rules+metadata bench = %+v (%s)", both.Groups, both.ConfigIdentity)
	}
	if after := benchMetadataRow(t, db, animeID); after != savedMetadata {
		t.Fatalf("the preview wrote metadata: before=%q after=%q", savedMetadata, after)
	}
	if after := animetest.ProductRowCounts(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("the bench wrote product rows: before=%v after=%v", before, after)
	}

	// A pending provider group takes an offset save by group identity, so the
	// bench opens the pencil even with no link yet; once the link row exists the
	// bench reports its offset.
	if !got.Offset.Editable || got.Offset.Value != 0 {
		t.Fatalf("pending bench offset = %+v, want editable at 0 before any link", got.Offset)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := groupsource.UpsertTx(ctx, tx, groupsource.Source{
		SubscriptionID: activated.Subscription.ID, Provider: "mikan", CatalogEntryRef: entryRef, GroupExternalID: groupRef,
		DisplayName: "Group 679", AnimeTitle: "Parity Show",
		RSSURL: "https://mikanani.me/RSS/Bangumi?bangumiId=70032&subgroupid=679", SnapshotVersion: groupsource.PendingSnapshotVersion, EpisodeOffset: -3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	linked, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner read with a link: %v", err)
	}
	if len(linked.Groups) != 1 || !linked.Groups[0].Offset.Editable || linked.Groups[0].Offset.Value != -3 {
		t.Fatalf("bench offset with a link = %+v, want editable at the link row's -3", linked.Groups)
	}

	// New releases only: with the option on, a seed published before the instance
	// was created is marked start_baseline on the bench (still read-only); a
	// seed published after the creation stays not_started.
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET new_releases_only = true, start_baseline_at = created_at, version = version + 1 WHERE id = $1`, activated.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	baselined, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner read with new releases only: %v", err)
	}
	if action := baselined.Groups[0].Items[0].DownloadAction; action.Allowed || action.Kind != RSSListDownloadNone || action.ReasonCode != rssListReasonStartBaseline {
		t.Fatalf("a seed published before the creation must be marked start_baseline: %+v", action)
	}
	for _, statement := range []string{
		`UPDATE subscriptions SET new_releases_only = false, start_baseline_at = NULL WHERE id = $1`,
		`UPDATE subscriptions SET created_at = created_at - INTERVAL '1 day' WHERE id = $1`,
		`UPDATE subscriptions SET new_releases_only = true, start_baseline_at = created_at, version = version + 1 WHERE id = $1`,
	} {
		if _, err := db.ExecContext(ctx, statement, activated.Subscription.ID); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	fresh, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: feed})
	if err != nil {
		t.Fatalf("pending owner read with an earlier baseline: %v", err)
	}
	if action := fresh.Groups[0].Items[0].DownloadAction; action.ReasonCode != rssListReasonNotStarted {
		t.Fatalf("a seed published after the creation must stay not_started: %+v", action)
	}
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET new_releases_only = false, start_baseline_at = NULL WHERE id = $1`, activated.Subscription.ID); err != nil {
		t.Fatal(err)
	}

	// Started, the same read is the ordinary owner view again.
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = $1`, activated.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	owner, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: feed})
	if err != nil {
		t.Fatalf("active owner read: %v", err)
	}
	if strings.HasPrefix(owner.ConfigIdentity, "owner_bench:") {
		t.Fatalf("an active owner was read as the bench: %s", owner.ConfigIdentity)
	}
	for _, group := range owner.Groups {
		if len(group.Items) != 0 {
			t.Fatalf("an active owner with no releases listed snapshot seeds: %+v", group)
		}
	}
}

// benchMetadataRow is the persisted metadata owner (and title override) as one
// comparable text, so a test proves a preview read left it untouched.
func benchMetadataRow(t *testing.T, db *sql.DB, animeID string) string {
	t.Helper()
	var row string
	if err := db.QueryRowContext(context.Background(), `
SELECT concat_ws('|', m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season::text, m.year::text, m.season::text,
       m.media_kind_override, m.total_episodes::text, m.version::text, ma.title_override)
FROM anime_metadata m JOIN managed_anime ma ON ma.id = m.managed_anime_id
WHERE m.managed_anime_id = $1`, animeID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	return row
}
