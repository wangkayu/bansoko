package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	rootapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	observabilityapplication "bansoko/internal/observability/application"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

type ownerRSSFetcher struct{}

func (ownerRSSFetcher) FetchSource(context.Context, rssapplication.SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	return nil, nil
}

func insertReleaseForRulesPreview(t *testing.T, db *sql.DB, sourceID, releaseID, title, downloadURL string) {
	t.Helper()
	rawFacts, err := json.Marshal(map[string]any{"title": title})
	if err != nil {
		t.Fatal(err)
	}
	internal, err := json.Marshal(map[string]any{
		"title":        title,
		"download_url": downloadURL,
		"guid":         releaseID + "-guid",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceFacts, err := json.Marshal(map[string]string{
		"identity_kind":  "guid",
		"identity_value": releaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(
    id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts,
    verdict, reason_code, evaluation_version
)
VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, 'discovered', 'discovered', NULL)`,
		releaseID, sourceID, releaseID+"-guid", rawFacts, sourceFacts, internal); err != nil {
		t.Fatal(err)
	}
}

func rulesPreviewWithExclude(t *testing.T, keywords ...string) RulesPreviewInput {
	t.Helper()
	sections := readmodelDefaultRules(t).Sections
	sections.ReleaseFilter = &generated.ReleaseFilterRulesOverride{}
	if err := sections.ReleaseFilter.FromReleaseFilterRulesReplace(generated.ReleaseFilterRulesReplace{
		Mode: generated.ReleaseFilterRulesReplaceMode("replace"),
		Value: generated.ReleaseFilterRules{
			MustContain: "",
			NotIn:       strings.Join(keywords, "|"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	return RulesPreviewInput{Sections: mustRulesSectionsDomain(t, sections)}
}

func mustRulesSectionsDomain(t *testing.T, sections generated.RulesSections) animedomain.RulesPreviewSections {
	t.Helper()
	raw, err := json.Marshal(sections)
	if err != nil {
		t.Fatal(err)
	}
	return animedomain.RulesPreviewSections(raw)
}

func rulesPreviewInputFromSections(t *testing.T, sections generated.RulesSections) RulesPreviewInput {
	t.Helper()
	return RulesPreviewInput{Sections: mustRulesSectionsDomain(t, sections)}
}

func TestRSSListOwnerDraftFiltersWithoutWrites(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-draft-key")
	sourceID, animeID := string(subscribed.RssSources[0].Id), string(subscribed.ManagedAnime.Id)
	insertReleaseForRulesPreview(t, db, sourceID, "rss-draft-accepted", "Test Show - 01 [1080p]", "https://downloads.example.test/01.torrent")
	insertReleaseForRulesPreview(t, db, sourceID, "rss-draft-filtered", "Test Show - 02 SP [1080p]", "https://downloads.example.test/02.torrent")
	before := animetest.ProductRowCounts(t, db)
	rulesPreview := rulesPreviewWithExclude(t, "SP")
	view, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, RulesPreview: &rulesPreview})
	if err != nil {
		t.Fatalf("owner_draft read: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 2 {
		t.Fatalf("owner_draft items = %+v", view.Groups)
	}
	if view.Groups[0].Items[0].Evaluation.Verdict != "filtered" || view.Groups[0].Items[1].Evaluation.Verdict != "accepted_pending_download" {
		t.Fatalf("owner_draft verdicts = %+v", view.Groups[0].Items)
	}
	for _, item := range view.Groups[0].Items {
		if item.DownloadAction.Kind != RSSListDownloadNone || item.DownloadAction.ReasonCode != "preview_mode" {
			t.Fatalf("owner_draft action = %+v", item.DownloadAction)
		}
	}
	if after := animetest.ProductRowCounts(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("owner_draft wrote product rows: before=%v after=%v", before, after)
	}
}

func TestRSSListOwnerMatchesPersistedReleaseEvaluationAndFiles(t *testing.T) {
	db := animetest.OpenTestDB(t)
	rootWorkItems := readmodelNewWorkItems(t, db)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-verdict-key")
	ctx := context.Background()
	sourceID, animeID := string(subscribed.RssSources[0].Id), string(subscribed.ManagedAnime.Id)
	insertReleaseForRulesPreview(t, db, sourceID, "rss-owner-release", "[TestGroup] Test Show - 01 SP [1080p]", "https://downloads.example.test/01.torrent")
	if _, err := db.ExecContext(ctx, `UPDATE releases SET internal_facts='{"release_files":[{"source_path":"Test Show/01.mkv","filename":"01.mkv","size_bytes":1024,"media_kind":"video"},{"source_path":"Test Show/01.chs.ass","filename":"01.chs.ass","size_bytes":20,"media_kind":"subtitle"},{"source_path":"Test Show/02.mkv","filename":"02.mkv","size_bytes":1024,"media_kind":"video"}]}'::jsonb WHERE id='rss-owner-release'`); err != nil {
		t.Fatal(err)
	}
	// The release was parsed from an exact torrent. Its bytes are what bind
	// every file_ref the owner read model derives, so the fixture stores them
	// rather than inventing a digest. The input is written after the evaluation
	// settles: this test is about what the read model projects from a stored
	// payload, not about the acceptance path that consumes one.
	ownerPayload, ownerMetadata := ownerReleaseTorrentPayload(t)
	ownerPayloadDigest := animecatalogdomain.DigestBytes(ownerPayload)
	if _, err := db.ExecContext(ctx, `UPDATE anime_config_overrides SET owner_type='rules', schema_version='file-rules-v1', value='{"must_contain":"","not_in":"02"}'::jsonb WHERE managed_anime_id=$1 AND section_name='file_rules'`, animeID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootWorkItems.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "rss-owner-release"}, Kind: workitems.KindReleaseEvaluate, IdempotencyKey: "release_evaluate:rss-owner-release", InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 5, ExpectedOwnerVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rssService, err := rssapplication.NewService(db, rootWorkItems, ownerRSSFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	rssService.SetActivityWriter(activityWriter)
	claim, err := rootWorkItems.ClaimDue(ctx, "rss-owner-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := rssService.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("release_evaluate outcome=%+v err=%v", outcome, err)
	}
	if err := rootWorkItems.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO release_torrent_inputs(id,release_id,payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,payload_size_bytes) VALUES('torrent-rss-owner-release','rss-owner-release',$1,$2,$3,'01.torrent','torrent-bencode-v1',$4,$5,$6)`,
		ownerPayload, ownerPayloadDigest, animecatalogdomain.DigestBytes([]byte("https://downloads.example.test/01.torrent")), ownerMetadata.TorrentVersion, ownerMetadata.InfoHashV1, len(ownerPayload)); err != nil {
		t.Fatal(err)
	}
	owner, err := service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
	if err != nil {
		t.Fatalf("owner RSS view: %v", err)
	}
	if len(owner.Groups) != 1 || len(owner.Groups[0].Items) != 1 {
		t.Fatalf("owner groups = %+v", owner.Groups)
	}
	if owner.Groups[0].Items[0].Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("owner evaluation state=%q after evaluator completed", owner.Groups[0].Items[0].Evaluation.State)
	}
	var persistedVerdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id='rss-owner-release'`).Scan(&persistedVerdict); err != nil {
		t.Fatal(err)
	}
	if owner.Groups[0].Items[0].Evaluation.Verdict != persistedVerdict {
		t.Fatalf("owner verdict=%q differs from release_evaluate persisted verdict=%q", owner.Groups[0].Items[0].Evaluation.Verdict, persistedVerdict)
	}
	files := owner.Groups[0].Items[0].Files
	var excluded, bound bool
	for _, file := range files {
		// Every listed row carries the identity the contract requires, derived
		// from the payload this release was parsed from. The same derivation is
		// what preview mode applies to a seed's own manifest, so the two modes
		// name the same file the same way.
		if want := animecatalogdomain.TorrentFileRef(ownerPayloadDigest, file.SourcePath); file.FileRef != want {
			t.Fatalf("owner file row %q file_ref = %q, want %q", file.SourcePath, file.FileRef, want)
		}
		if file.SourcePath == "Test Show/02.mkv" {
			if !file.Included && file.ReasonCode != "" {
				excluded = true
			}
			// The owner row lists the files of the release we already parsed, so
			// every row reports that release's own size and kind. This is the live
			// defect: the rows read 0 and an empty media kind while the target row
			// of the same release held the parsed values.
			if file.SizeBytes == nil || *file.SizeBytes != 1024 || file.MediaKind == nil || *file.MediaKind != "video" {
				t.Fatalf("owner file row must carry the parsed release file's size and kind: %+v", file)
			}
		}
		if file.SourcePath == "Test Show/01.chs.ass" {
			// The frozen plan's binding travels with the subtitle row, so the
			// reader lists its output under that video instead of binding it
			// again.
			if file.BoundVideoSourcePath != "Test Show/01.mkv" {
				t.Fatalf("a bound subtitle must carry the video it follows: %+v", file)
			}
			bound = true
		}
	}
	if !excluded || !bound {
		t.Fatalf("owner file rows = %+v", files)
	}
}

func TestRSSListOwnerStalenessClearsAfterReleaseEvaluate(t *testing.T) {
	db := animetest.OpenTestDB(t)
	rootWorkItems := readmodelNewWorkItems(t, db)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-staleness-key")
	ctx := context.Background()
	managedAnimeID := string(subscribed.ManagedAnime.Id)
	insertReleaseForRulesPreview(t, db, string(subscribed.RssSources[0].Id), "rss-owner-stale-release", "[TestGroup] Test Show - 01 [1080p]", "https://downloads.example.test/01.torrent")
	patch := readmodelDefaultRules(t).Sections
	patch.ReleaseFilter = &generated.ReleaseFilterRulesOverride{}
	if err := patch.ReleaseFilter.FromReleaseFilterRulesReplace(generated.ReleaseFilterRulesReplace{Mode: generated.ReleaseFilterRulesReplaceMode("replace"), Value: generated.ReleaseFilterRules{MustContain: "", NotIn: ""}}); err != nil {
		t.Fatal(err)
	}
	if _, err := root.PatchAnimeRules(ctx, managedAnimeID, readmodelMustRulesPatch(t, generated.RulesPatch{ExpectedVersion: 1, Sections: patch})); err != nil {
		t.Fatalf("patch rules: %v", err)
	}
	view, err := service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: managedAnimeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 || view.Groups[0].Items[0].Evaluation.State != "reevaluating" {
		t.Fatalf("post-patch owner view=%+v", view.Groups)
	}
	rssService, err := rssapplication.NewService(db, rootWorkItems, ownerRSSFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	rssService.SetActivityWriter(activityWriter)
	claim, err := rootWorkItems.ClaimDue(ctx, "rss-owner-staleness-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := rssService.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("release_evaluate outcome=%+v err=%v", outcome, err)
	}
	if err := rootWorkItems.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	view, err = service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: managedAnimeID})
	if err != nil {
		t.Fatal(err)
	}
	if view.Groups[0].Items[0].Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("post-evaluation state=%q; want evaluated", view.Groups[0].Items[0].Evaluation.State)
	}
}
func TestRSSListOwnerReportsTheHoldAndStillRendersTheTarget(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	draft := readmodelTestDraft(t)
	draft.Metadata = generated.AnimeMetadataDraft{}
	subscribed, err := root.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "rss-owner-hold-key-0002")
	if err != nil {
		t.Fatalf("subscribe with incomplete metadata: %v", err)
	}
	insertReleaseForRulesPreview(t, db, subscribed.RssSources[0].Id, "rp-held", "[TestGroup] Test Show - 01 [1080p]", "https://downloads.example.test/show-01.torrent")

	view, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: string(subscribed.ManagedAnime.Id)})
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if !view.MetadataHold.Held {
		t.Fatalf("incomplete saved bindings must hold the read model: %+v", view.MetadataHold)
	}
	if len(view.MetadataHold.Missing) != 2 {
		t.Fatalf("hold missing = %+v, want bgm and tmdb", view.MetadataHold.Missing)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("owner groups = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	// The owner mode reports the persisted Release verdict: this release was
	// never evaluated under the current rules version, so the row is
	// reevaluating rather than silently presented as decided, and the hold is
	// carried as the reason.
	if item.Evaluation.State != "reevaluating" {
		t.Fatalf("unevaluated persisted release must report reevaluating: %+v", item.Evaluation)
	}
	if !strings.Contains(item.Evaluation.ReasonDetail, "bgm") || !strings.Contains(item.Evaluation.ReasonDetail, "tmdb") {
		t.Fatalf("hold reason must name the missing bindings: %+v", item.Evaluation)
	}
	// No exact persisted inventory was admitted, so no manifest file is known
	// and the row list is empty: the locator fallback is not a video or a
	// subtitle, so it failed the kind gate and is not a candidate. The hold
	// cannot invent a Vars sample, a file row or a target.
	if len(item.Files) != 0 {
		t.Fatalf("missing frozen inventory must list no file row: %+v", item.Files)
	}
}

// A running owner previews the unsaved metadata through owner_draft without a
// rules patch: the unsaved bindings clear the hold the saved (empty) ones
// raise, every action stays read-only, and nothing is written.
func TestRSSListOwnerDraftPreviewsUnsavedMetadataWithoutWrites(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	draft := readmodelTestDraft(t)
	draft.Metadata = generated.AnimeMetadataDraft{}
	subscribed, err := root.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "rss-owner-metadata-preview-key")
	if err != nil {
		t.Fatalf("subscribe with empty metadata: %v", err)
	}
	animeID := string(subscribed.ManagedAnime.Id)
	insertReleaseForRulesPreview(t, db, subscribed.RssSources[0].Id, "rp-metadata-preview", "[TestGroup] Test Show - 01 [1080p]", "https://downloads.example.test/show-01.torrent")
	ctx := context.Background()
	saved, err := service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
	if err != nil || !saved.MetadataHold.Held {
		t.Fatalf("saved owner read hold=%+v err=%v", saved.MetadataHold, err)
	}
	savedMetadata := benchMetadataRow(t, db, animeID)
	before := animetest.ProductRowCounts(t, db)
	year := 2031
	preview := MetadataPreviewInput{BGM: &BGMBinding{WorkID: "bgm-preview"}, TMDB: &TMDBBinding{WorkID: "tmdb-preview", MediaKind: "tv", Season: 3}, Year: &year}
	view, err := service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, MetadataPreview: &preview})
	if err != nil {
		t.Fatalf("owner_draft metadata read: %v", err)
	}
	if view.Mode != "owner_draft" || view.MetadataHold.Held || view.ConfigIdentity == saved.ConfigIdentity {
		t.Fatalf("owner_draft metadata view mode=%s hold=%+v identity=%s", view.Mode, view.MetadataHold, view.ConfigIdentity)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("owner_draft metadata groups = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	if item.DownloadAction.Kind != RSSListDownloadNone || item.DownloadAction.ReasonCode != "preview_mode" {
		t.Fatalf("owner_draft metadata action = %+v", item.DownloadAction)
	}
	if strings.Contains(item.Evaluation.ReasonDetail, "bgm") || strings.Contains(item.Evaluation.ReasonDetail, "tmdb") {
		t.Fatalf("the unsaved bindings must not report a hold: %+v", item.Evaluation)
	}
	if after := benchMetadataRow(t, db, animeID); after != savedMetadata {
		t.Fatalf("the preview wrote metadata: before=%q after=%q", savedMetadata, after)
	}
	if after := animetest.ProductRowCounts(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("owner_draft metadata wrote product rows: before=%v after=%v", before, after)
	}
}

func TestRSSListOwnerReadNotFoundAndValidation(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-validation-key-0003")

	if _, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: "anime-missing"}); err == nil {
		t.Fatal("unknown anime must fail")
	} else {
		var notFound *apperrors.NotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("unknown anime must map to NotFound, got %T: %v", err, err)
		}
	}

	invalid := readmodelDefaultRules(t).Sections
	invalid.ReleaseFilter = &generated.ReleaseFilterRulesOverride{}
	if err := invalid.ReleaseFilter.FromReleaseFilterRulesReplace(generated.ReleaseFilterRulesReplace{
		Mode:  generated.ReleaseFilterRulesReplaceMode("replace"),
		Value: generated.ReleaseFilterRules{MustContain: "[", NotIn: ""},
	}); err != nil {
		t.Fatal(err)
	}
	invalidPreview := rulesPreviewInputFromSections(t, invalid)
	if _, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: string(subscribed.ManagedAnime.Id), RulesPreview: &invalidPreview}); err == nil {
		t.Fatal("incomplete replace section must fail validation")
	} else {
		var validation *apperrors.Validation
		if !errors.As(err, &validation) {
			t.Fatalf("invalid sections must map to Validation, got %T: %v", err, err)
		}
	}
}

// TestRSSListViewCurrentInputMatchesCandidateRevision proves the two
// non-owner modes resolve the same content to the same read model: the same
// draft and the same advisory feed identity produce the same group structure,
// verdicts and files whether it is read as an unsaved current_input or as the
// exact saved revision. It is the surviving form of the retired flat preview's
// ephemeral-versus-saved equality check.
func TestRSSListViewCurrentInputMatchesCandidateRevision(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})

	// The feed item carries an acquired torrent: the exact bytes, and the file
	// tree the acquisition pipeline parsed out of them. The read model renders
	// its rows from the persisted projection of those facts, so the fixture
	// declares bytes and tree together (consistently by construction) — the
	// parser itself has its own tests.
	fetcher := readmodelCatalogFeedFetcher{items: []acquisitiondomain.FeedItem{{
		GUID: "parity-seed-01", Title: "[Group] Parity Show - 01 [1080p]",
		Enclosure:       "https://torrent.example.test/parity-01.torrent",
		Published:       time.Now().UTC().Truncate(time.Microsecond),
		TorrentFilename: "Parity Show S1", TorrentVersion: "v1",
		TorrentInfoHashV1: strings.Repeat("a", 40),
		TorrentPayload:    parityShowTorrentPayload(),
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{SourcePath: "Parity Show S1/Parity Show - 01.mkv", Filename: "Parity Show - 01.mkv", SizeBytes: animetest.Int64Ptr(1234), MediaKind: animecatalogdomain.TorrentFileMediaVideo},
			{SourcePath: "Parity Show S1/Parity Show - 01.zh-Hans.ass", Filename: "Parity Show - 01.zh-Hans.ass", SizeBytes: animetest.Int64Ptr(20), MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Facts: map[string]any{"language": "zh-Hans"}},
			{SourcePath: "Parity Show S1/cover.jpg", Filename: "cover.jpg", SizeBytes: animetest.Int64Ptr(7), MediaKind: animecatalogdomain.TorrentFileMediaOther},
		},
	}}}
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetProviderFeedFetcher(fetcher)
	catalogService.SetFeedSnapshotStore(catalogpersistence.NewRepository(db))
	readmodel = newReadmodelService(t, db, root.Kernel, root.Draft, root.Service, rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload))
	// The readmodel service owns the injected snapshot source.
	const entryRef = "70031"
	const groupRef = "678"
	submitted := readmodelProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, entryRef, groupRef)
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	// effective_selected is derived, never the client's false placeholder.
	subgroupRaw, err := json.Marshal(submitted.Subgroups)
	if err != nil {
		t.Fatal(err)
	}
	var subgroupValue animedomain.SubgroupsSection
	if err := json.Unmarshal(subgroupRaw, &subgroupValue); err != nil {
		t.Fatal(err)
	}
	normalizedSubgroups, err := animedomain.SubgroupsSectionWithEffectiveSelections(subgroupValue)
	if err != nil {
		t.Fatal(err)
	}
	business.Sections[animedomain.SectionSubgroups], err = json.Marshal(normalizedSubgroups)
	if err != nil {
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
	// Hash precisely the typed wire snapshot that public save will consume.
	raw, err = json.Marshal(wire)
	if err != nil {
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

	saved, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{
		InputSource: "candidate_revision", CandidateID: string(candidate.Id), Revision: 1, Feed: feed,
	})
	if err != nil {
		t.Fatalf("candidate_revision read: %v", err)
	}
	// This fixture compares the saved revision's current-input projection. The raw read is on the root owner service in a test, not a readmodel production path.
	revision, err := root.GetAnimeConfigCandidateRevision(ctx, string(candidate.Id), 1)
	if err != nil {
		t.Fatalf("read saved revision: %v", err)
	}
	draft := revision.Draft
	unsaved, err := readmodel.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{
		InputSource: "current_input", Draft: &draft, Feed: feed,
	})
	if err != nil {
		t.Fatalf("current_input read: %v", err)
	}

	if len(saved.Groups) != 1 || len(unsaved.Groups) != 1 {
		t.Fatalf("groups: saved=%+v unsaved=%+v", saved.Groups, unsaved.Groups)
	}
	if saved.Groups[0].Scope != unsaved.Groups[0].Scope || !reflect.DeepEqual(saved.Groups[0].Feed, unsaved.Groups[0].Feed) {
		t.Fatalf("group identity/feed diverged: saved=%+v unsaved=%+v", saved.Groups[0], unsaved.Groups[0])
	}
	if saved.MetadataHold.Held != unsaved.MetadataHold.Held || !reflect.DeepEqual(saved.MetadataHold.Missing, unsaved.MetadataHold.Missing) || saved.Versions != unsaved.Versions {
		t.Fatalf("hold/versions diverged: saved=%+v/%+v unsaved=%+v/%+v", saved.MetadataHold, saved.Versions, unsaved.MetadataHold, unsaved.Versions)
	}
	savedItems, unsavedItems := saved.Groups[0].Items, unsaved.Groups[0].Items
	if len(savedItems) != 1 || len(unsavedItems) != 1 {
		t.Fatalf("items: saved=%+v unsaved=%+v", savedItems, unsavedItems)
	}
	if savedItems[0].StableSeedKey != unsavedItems[0].StableSeedKey ||
		savedItems[0].Evaluation.Verdict != unsavedItems[0].Evaluation.Verdict ||
		savedItems[0].Evaluation.ReasonCode != unsavedItems[0].Evaluation.ReasonCode {
		t.Fatalf("evaluation diverged: saved=%+v unsaved=%+v", savedItems[0].Evaluation, unsavedItems[0].Evaluation)
	}
	if savedItems[0].Evaluation.Verdict != "accepted_pending_download" {
		t.Fatalf("verdict = %q, want accepted_pending_download", savedItems[0].Evaluation.Verdict)
	}
	// The persisted projection is the row source: the video and the subtitle
	// that binds it. The cover is a kind the product does not handle, so it
	// takes no row even though the manifest carries it. Both modes must list
	// the same rows, not merely the same count.
	if len(savedItems[0].Files) != 2 || len(unsavedItems[0].Files) != 2 {
		t.Fatalf("files diverged: saved=%+v unsaved=%+v", savedItems[0].Files, unsavedItems[0].Files)
	}
	for index := range savedItems[0].Files {
		savedFile, unsavedFile := savedItems[0].Files[index], unsavedItems[0].Files[index]
		if savedFile.SourcePath != unsavedFile.SourcePath || savedFile.RenderedTarget != unsavedFile.RenderedTarget ||
			savedFile.Included != unsavedFile.Included || savedFile.Safety != unsavedFile.Safety {
			t.Fatalf("file %d diverged: saved=%+v unsaved=%+v", index, savedFile, unsavedFile)
		}
	}
	// Reading an unsaved draft never persists the candidate or an owner.
	if candidate.CurrentRevision == nil || *candidate.CurrentRevision != 1 {
		t.Fatalf("current_input read changed the candidate revision: %v", candidate.CurrentRevision)
	}
}

// The owner read model reports the offset patch's own fence: the group's offset
// is editable exactly while its ManagedAnime is active, and retirement makes the
// same row read-only without changing its value.
func TestRSSListOwnerOffsetEditableFollowsLifecycle(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-offset-editable")
	animeID := string(subscribed.ManagedAnime.Id)
	ctx := context.Background()

	read := func() RSSListGroup {
		t.Helper()
		view, err := service.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
		if err != nil {
			t.Fatalf("owner read: %v", err)
		}
		if len(view.Groups) != 1 {
			t.Fatalf("owner groups = %+v", view.Groups)
		}
		return view.Groups[0]
	}
	active := read()
	if !active.Offset.Editable {
		t.Fatalf("an active ManagedAnime must expose an editable offset: %+v", active.Offset)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id=$1`, animeID); err != nil {
		t.Fatal(err)
	}
	retired := read()
	if retired.Offset.Editable {
		t.Fatalf("a retired ManagedAnime must report a read-only offset: %+v", retired.Offset)
	}
	if retired.Offset.Value != active.Offset.Value {
		t.Fatalf("retirement changed the offset value: %d -> %d", active.Offset.Value, retired.Offset.Value)
	}
}

// ownerReleaseTorrentPayload is the bencoded v1 torrent the owner fixture's
// release was parsed from. Its three files are exactly the release_files the
// fixture freezes, so the payload digest that binds every file_ref is computed
// from real bytes. The torrent name is the first path segment the parser builds
// each file path from.
func ownerReleaseTorrentPayload(t *testing.T) ([]byte, torrent.Metadata) {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("d4:infod4:name9:Test Show5:filesl")
	for _, file := range []struct {
		Name string
		Size int64
	}{
		{"01.mkv", 1024},
		{"01.chs.ass", 20},
		{"02.mkv", 1024},
	} {
		builder.WriteString("d6:lengthi" + strconv.FormatInt(file.Size, 10) + "e4:pathl")
		builder.WriteString(strconv.Itoa(len(file.Name)) + ":" + file.Name)
		builder.WriteString("ee")
	}
	builder.WriteString("eee") // close the file list, the info dictionary, the root dictionary
	payload := []byte(builder.String())
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatalf("parse fixture torrent: %v", err)
	}
	if len(metadata.Files) != 3 || metadata.Files[0].Path != "Test Show/01.mkv" || metadata.Files[1].Path != "Test Show/01.chs.ass" || metadata.Files[2].Path != "Test Show/02.mkv" {
		t.Fatalf("fixture torrent does not describe the release files: %+v", metadata.Files)
	}
	return payload, metadata
}

// parityShowTorrentPayload is the bencoded v1 torrent the parity fixture's feed
// item is fetched as, so the payload digest that binds every projected file
// ref is computed from real bytes instead of a placeholder. Its three files are
// exactly the ones the same item's parsed file tree declares.
func parityShowTorrentPayload() []byte {
	var builder strings.Builder
	builder.WriteString("d4:infod4:name14:Parity Show S15:filesl")
	for _, file := range []struct {
		Name string
		Size int64
	}{
		{"Parity Show - 01.mkv", 1234},
		{"Parity Show - 01.zh-Hans.ass", 20},
		{"cover.jpg", 7},
	} {
		builder.WriteString("d6:lengthi" + strconv.FormatInt(file.Size, 10) + "e4:pathl")
		builder.WriteString(strconv.Itoa(len(file.Name)) + ":" + file.Name)
		builder.WriteString("ee")
	}
	builder.WriteString("eee") // close the file list, the info dictionary, the root dictionary
	return []byte(builder.String())
}
