package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// PostgreSQL coverage for the human one-shot seed's exact feed snapshot
// membership and its frozen dual identity. A seed must belong to the exact
// persisted snapshot it declares; locator reuse across groups is not identity.

type fixedCatalogFeedFetcher struct {
	items []acquisitiondomain.FeedItem
}

func (f fixedCatalogFeedFetcher) FetchProviderFeed(context.Context, providerdomain.FeedLocator) ([]acquisitiondomain.FeedItem, error) {
	return f.items, nil
}

// humanOneShotFixture wires a real human one-shot admission with a real catalog
// feed snapshot store and returns everything needed to assert on the outcome.
func humanOneShotFixture(t *testing.T) (*oneShotTestServices, *catalogapplication.Service, *sql.DB, SelectedSeed, func(items []acquisitiondomain.FeedItem) animecatalogdomain.ProviderRSSPreviewResult) {
	t.Helper()
	db := animetest.OpenTestDB(t)
	payload := parseOneShotTestTorrent(t)
	service := oneShotNewTestService(t, db, &oneShotFakeTorrentFetcher{payload: payload}, nil)
	animetest.SeedQbConnection(t, db)

	const entryRef = "70011"
	seed := SelectedSeed{
		Provider:         animedomain.ProviderKindMikan,
		ProviderEntryRef: entryRef,
		ProviderGroupRef: "678",
		StableSeedKey:    "seed-one-shot-01",
		Locator:          "https://torrent.example.test/one-shot.torrent",
		Title:            "[Group] Example Anime - 01 [1080p]",
		PublishedAt:      time.Now().UTC().Truncate(time.Microsecond),
		InfoHash:         animetest.StringPtr(strings.ToLower(payload.Metadata.InfoHashV1)),
		// The preview states the size and payload digest of the torrent it
		// retained; admission compares them against the persisted snapshot, so
		// the selected seed has to declare the same values.
		SizeBytes:             animetest.Int64Ptr(payload.Metadata.TotalSize),
		ExpectedPayloadDigest: animetest.StringPtr(animecatalogdomain.DigestBytes(payload.Bytes)),
	}

	// A provider seed names its group by identity only; admission renders
	// anime.group from the display name the newest group observation published
	// for it, so the fixture records the observation every seed this helper
	// returns is bound to.
	recordOneShotSeedGroup(t, db, seed, "Group")

	fetcher := &fixedCatalogFeedFetcher{}
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetProviderFeedFetcher(fetcher)
	catalogService.SetFeedSnapshotStore(catalogpersistence.NewRepository(db))
	service.SetSeedPreviewSource(animeapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload))

	refresh := func(items []acquisitiondomain.FeedItem) animecatalogdomain.ProviderRSSPreviewResult {
		t.Helper()
		fetcher.items = withRetainedTorrent(items, payload)
		result, err := catalogService.RefreshProviderEntryRSS(context.Background(), "mikan", entryRef, "678")
		if err != nil {
			t.Fatalf("refresh snapshot: %v", err)
		}
		return result
	}
	return service, catalogService, db, seed, refresh
}

// withRetainedTorrent mirrors the acquisition fetcher for a feed it could
// fetch: every item carries the verified torrent bytes, the parser's file
// projection and the derived size/name/hash facts. The catalog persists those
// bytes and admission reads them back instead of fetching the locator again, so
// a fixture without them can only exercise the fail-closed refresh path. An
// item that already declares an acquisition error is left untouched, which is
// how the unavailable-torrent cases stay expressible.
func withRetainedTorrent(items []acquisitiondomain.FeedItem, payload animeapplication.TorrentPayload) []acquisitiondomain.FeedItem {
	enriched := make([]acquisitiondomain.FeedItem, 0, len(items))
	for _, item := range items {
		clone := item
		if strings.TrimSpace(item.TorrentErrorCode) == "" {
			clone.TorrentPayload = append([]byte(nil), payload.Bytes...)
			clone.TorrentVersion = payload.Metadata.TorrentVersion
			clone.TorrentFilename = payload.Metadata.TorrentName
			clone.TorrentSizeBytes = payload.Metadata.TotalSize
			clone.EnclosureLength = payload.Metadata.TotalSize
			clone.TorrentInfoHashV1 = payload.Metadata.InfoHashV1
			clone.TorrentInfoHashV2 = payload.Metadata.InfoHashV2
			clone.TorrentInfoHash = payload.Metadata.InfoHashV1
			if clone.TorrentInfoHash == "" {
				clone.TorrentInfoHash = payload.Metadata.InfoHashV2
			}
			clone.ReleaseFiles = make([]acquisitiondomain.ReleaseFile, 0, len(payload.Metadata.Files))
			for _, file := range payload.Metadata.Files {
				size := file.SizeBytes
				clone.ReleaseFiles = append(clone.ReleaseFiles, acquisitiondomain.ReleaseFile{
					SourcePath: file.Path,
					Filename:   file.Name,
					SizeBytes:  &size,
					MediaKind:  file.MediaKind,
					SourceHash: clone.TorrentInfoHash,
				})
			}
		}
		enriched = append(enriched, clone)
	}
	return enriched
}

// seedFeedItem mirrors what the catalog snapshot mapper persists for a seed, so
// membership comparison has exactly one source of truth.
func seedFeedItem(seed SelectedSeed) acquisitiondomain.FeedItem {
	return acquisitiondomain.FeedItem{
		GUID:              seed.StableSeedKey,
		Title:             seed.Title,
		Enclosure:         seed.Locator,
		Published:         seed.PublishedAt,
		TorrentInfoHashV1: optionalStringValue(seed.InfoHash),
	}
}

func bindSeedIdentity(seed SelectedSeed, snapshot animecatalogdomain.ProviderRSSPreviewResult) SelectedSeed {
	seed.SnapshotID = snapshot.SnapshotID
	seed.SnapshotVersion = snapshot.SnapshotVersion
	seed.SnapshotContentHash = snapshot.ContentHash
	return seed
}

// A seed absent from the declared snapshot must produce a typed refusal and
// zero qB work: no DownloadTask, no WorkItem, no temporary owner graph.
func TestAcquireSeedOnceSeedOutsideSnapshotCreatesNoQBWork(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	input := humanCandidateForSeed(t, service, seed)

	// The snapshot for this group exists but carries a different seed.
	other := seedFeedItem(seed)
	other.GUID = "seed-other"
	other.Title = "[Group] Another Release - 07 [1080p]"
	other.Enclosure = "https://torrent.example.test/other.torrent"
	snapshot := refresh([]acquisitiondomain.FeedItem{other})
	input.Seed = bindSeedIdentity(seed, snapshot)

	_, err := service.AcquireSeedOnce(ctx, input, "one-shot-feed-membership-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "SEED_PREVIEW_STALE" {
		t.Fatalf("error = %v, want SEED_PREVIEW_STALE", err)
	}
	assertNoOneShotOwnerRows(t, service)
	if animetest.CountRows(t, db, "download_tasks") != 0 || animetest.CountRows(t, db, "work_items") != 0 {
		t.Fatal("a seed outside its snapshot admitted qB work")
	}
}

// A locator reused by another group is not identity: the same URL under a
// different snapshot/stable key must not satisfy membership.
func TestAcquireSeedOnceLocatorReuseIsNotIdentity(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	input := humanCandidateForSeed(t, service, seed)

	// Same locator, different stable seed key and title.
	impostor := seedFeedItem(seed)
	impostor.GUID = "seed-impostor"
	impostor.Title = "[Group] Example Anime - 02 [1080p]"
	snapshot := refresh([]acquisitiondomain.FeedItem{impostor})
	input.Seed = bindSeedIdentity(seed, snapshot)

	_, err := service.AcquireSeedOnce(ctx, input, "one-shot-locator-reuse-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "SEED_PREVIEW_STALE" {
		t.Fatalf("error = %v, want SEED_PREVIEW_STALE for a reused locator", err)
	}
	assertNoOneShotOwnerRows(t, service)
	if animetest.CountRows(t, db, "download_tasks") != 0 {
		t.Fatal("a reused locator admitted a download task")
	}
}

// A one-shot names its seed directly: a human seed from a provider group the
// locked revision did not select is admitted, because its exact snapshot
// proves it exists.
func TestAcquireSeedOnceAdmitsUnselectedGroupSeed(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	// Selected group is 999; the seed's group 678 is not selected.
	draft := oneShotProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, seed.ProviderEntryRef, "999")
	candidate, err := service.Draft.SaveAnimeConfigCandidate(ctx, candidateInput(t, db, draft))
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	input := AcquireSeedInput{
		CandidateID: string(candidate.Id),
		Revision:    *candidate.CurrentRevision,
		Seed:        bindSeedIdentity(seed, snapshot),
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-unselected-group-0001")
	if err != nil {
		t.Fatalf("unselected-group admission: %v", err)
	}
	if result.DownloadTaskID == nil || animetest.CountRows(t, db, "download_tasks") != 1 {
		t.Fatal("an unselected-group admission created no download task")
	}
}

// A global parser-policy change after the candidate save must not alter a
// one-shot admission: the marker's frozen parser provenance keeps the revision's
// saved parser kind, and the created instance policy keeps that kind and the
// revision's policy version. The global default stays a valid (registered)
// policy: a one-shot without a frozen business snapshot still reads the
// current preset, which requires a consistent global state.
func TestAcquireSeedOnceFreezesRevisionParserPolicy(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	input := humanCandidateForSeed(t, service, seed)
	revision, err := service.Draft.GetAnimeConfigCandidateRevision(ctx, input.CandidateID, input.Revision)
	if err != nil {
		t.Fatalf("read saved revision: %v", err)
	}
	frozenKind := revision.ParserPolicy.Kind
	frozenVersion := revision.ParserPolicy.PolicyVersion

	// Change the global creation default after the save. Admission must ignore it.
	if _, err := db.ExecContext(ctx, `
UPDATE global_config_sections
SET version = version + 41
WHERE section_name = 'parser_policy'`); err != nil {
		t.Fatal(err)
	}

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	input.Seed = bindSeedIdentity(seed, snapshot)
	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-frozen-parser-0001")
	if err != nil {
		t.Fatalf("AcquireSeedOnce: %v", err)
	}

	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot FROM one_shot_acquisitions WHERE id = $1`, result.OneShotID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Parser *struct {
			Kind   string `json:"kind"`
			Offset int    `json:"offset"`
		} `json:"parser"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Parser == nil {
		t.Fatal("one-shot marker carried no frozen parser provenance")
	}
	if frozen.Parser.Kind != frozenKind {
		t.Fatalf("frozen parser kind = %q, want the revision's %q", frozen.Parser.Kind, frozenKind)
	}
	var instanceKind string
	var instanceSourceVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT parser_kind, parser_source_default_version FROM anime_business_configs WHERE managed_anime_id = $1`, result.ManagedAnimeID).Scan(&instanceKind, &instanceSourceVersion); err != nil {
		t.Fatal(err)
	}
	if instanceKind != frozenKind || instanceSourceVersion != frozenVersion {
		t.Fatalf("instance parser policy = %q v%d, want the frozen %q v%d, not the changed global default", instanceKind, instanceSourceVersion, frozenKind, frozenVersion)
	}
}

// A contained seed is admitted, and the marker freezes BOTH identities: the
// exact saved config revision and the exact feed snapshot.
func TestAcquireSeedOnceFreezesConfigAndFeedIdentity(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	input := humanCandidateForSeed(t, service, seed)
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	input.Seed = bindSeedIdentity(seed, snapshot)

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-freeze-identities-0001")
	if err != nil {
		t.Fatalf("AcquireSeedOnce: %v", err)
	}
	if result.OneShotID == "" {
		t.Fatal("admission returned no one-shot id")
	}

	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot FROM one_shot_acquisitions WHERE id = $1`, result.OneShotID).Scan(&raw); err != nil {
		t.Fatalf("read marker input snapshot: %v", err)
	}
	var frozen struct {
		CandidateID     string `json:"candidate_id"`
		Revision        int64  `json:"revision"`
		EffectiveDigest string `json:"effective_digest"`
		ConfigIdentity  struct {
			IdentityKind string `json:"identity_kind"`
			CandidateID  string `json:"candidate_id"`
			Revision     int64  `json:"revision"`
		} `json:"config_identity"`
		SelectedSeed struct {
			SnapshotID          string `json:"snapshot_id"`
			SnapshotVersion     int64  `json:"snapshot_version"`
			SnapshotContentHash string `json:"snapshot_content_hash"`
			StableSeedKey       string `json:"stable_seed_key"`
			Locator             string `json:"locator"`
		} `json:"selected_seed"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatalf("decode marker input snapshot: %v", err)
	}
	if frozen.CandidateID != input.CandidateID || frozen.Revision != input.Revision || frozen.EffectiveDigest == "" {
		t.Fatalf("frozen config identity = %+v, want the exact candidate revision", frozen)
	}
	if frozen.ConfigIdentity.IdentityKind != "candidate_revision" || frozen.ConfigIdentity.CandidateID != input.CandidateID || frozen.ConfigIdentity.Revision != input.Revision {
		t.Fatalf("frozen config identity block = %+v", frozen.ConfigIdentity)
	}
	if frozen.SelectedSeed.SnapshotID != snapshot.SnapshotID || frozen.SelectedSeed.SnapshotVersion != snapshot.SnapshotVersion || frozen.SelectedSeed.SnapshotContentHash != snapshot.ContentHash {
		t.Fatalf("frozen feed identity = %+v, want %s v%d", frozen.SelectedSeed, snapshot.SnapshotID, snapshot.SnapshotVersion)
	}
	if frozen.SelectedSeed.StableSeedKey != seed.StableSeedKey || frozen.SelectedSeed.Locator != seed.Locator {
		t.Fatalf("frozen selected seed = %+v", frozen.SelectedSeed)
	}
	// The admission produced exactly one download task and no more.
	if got := animetest.CountRows(t, db, "download_tasks"); got != 1 {
		t.Fatalf("download tasks = %d, want 1", got)
	}
	// The one-shot mount stores a complete value for every per-anime section,
	// tag_filters included, identical to a normal activation: nothing resolves
	// against current globals later.
	var totalOverrides, inheritOverrides int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE value IS NULL)
FROM anime_config_overrides
WHERE managed_anime_id = $1`, result.ManagedAnimeID).Scan(&totalOverrides, &inheritOverrides); err != nil {
		t.Fatal(err)
	}
	if want := len(animedomain.EffectiveSectionOrder) + 1; totalOverrides != want || inheritOverrides != 0 {
		t.Fatalf("one-shot overrides = %d (inherit %d), want %d complete values", totalOverrides, inheritOverrides, want)
	}
}
func TestCandidateAndOneShotSharedProvisionerProducesIdenticalOwnerRows(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	parser := parserpolicy.NewService(db)
	draftService := service.Draft
	draftService.SetFrozenParserPolicy(parser)
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draftService.SetActivityWriter(activityWriter)

	input := humanCandidateForSeed(t, service, seed)
	revision, err := service.Draft.GetAnimeConfigCandidateRevision(ctx, input.CandidateID, input.Revision)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := revision.Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Draft.ActivateAnimeConfigCandidate(ctx, input.CandidateID, 1, "provisioner-candidate-activation", draftapplication.CandidateSubscribeInput{Revision: input.Revision, Start: true}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	var candidateAnimeID string
	if err := db.QueryRowContext(ctx, `SELECT activated_managed_anime_id FROM anime_config_candidates WHERE id = $1`, input.CandidateID).Scan(&candidateAnimeID); err != nil {
		t.Fatal(err)
	}

	otherInput := AcquireSeedInput{
		InputSource: AcquireSeedInputSourceCurrentInput,
		Draft:       &revision.Draft,
		Seed:        seed,
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	otherInput.Seed = bindSeedIdentity(otherInput.Seed, snapshot)
	if _, err := service.AcquireSeedOnce(ctx, otherInput, "provisioner-oneshot-admission"); err != nil {
		t.Fatalf("admit one-shot: %v", err)
	}
	var oneShotAnimeID string
	if err := db.QueryRowContext(ctx, `SELECT managed_anime_id FROM one_shot_acquisitions ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&oneShotAnimeID); err != nil {
		t.Fatal(err)
	}

	ownerRows := func(animeID string) map[string][]string {
		t.Helper()
		queries := map[string]string{
			"retention":   `SELECT admission_mode, quality_profile::text, retention_source_default_version::text, retention_version::text FROM anime_business_configs WHERE managed_anime_id = $1`,
			"parser":      `SELECT parser_kind, parser_source_default_version::text, parser_version::text FROM anime_business_configs WHERE managed_anime_id = $1`,
			"file_effect": `SELECT unclassified_route, file_effect_source_default_version::text, file_effect_version::text FROM anime_business_configs WHERE managed_anime_id = $1`,
			"overrides":   `SELECT owner_type || ':' || section_name, schema_version, value::text FROM anime_config_overrides WHERE managed_anime_id = $1 ORDER BY owner_type, section_name`,
		}
		rows := make(map[string][]string, len(queries))
		for name, query := range queries {
			values, err := db.QueryContext(ctx, query, animeID)
			if err != nil {
				t.Fatalf("read %s owner rows: %v", name, err)
			}
			columns, err := values.Columns()
			if err != nil {
				values.Close()
				t.Fatal(err)
			}
			for values.Next() {
				record := make([]sql.NullString, len(columns))
				dest := make([]any, len(columns))
				for index := range record {
					dest[index] = &record[index]
				}
				if err := values.Scan(dest...); err != nil {
					values.Close()
					t.Fatal(err)
				}
				parts := make([]string, len(record))
				for index, value := range record {
					if value.Valid {
						parts[index] = value.String
					}
				}
				rows[name] = append(rows[name], strings.Join(parts, "|"))
			}
			if err := values.Err(); err != nil {
				values.Close()
				t.Fatal(err)
			}
			values.Close()
			sort.Strings(rows[name])
		}
		return rows
	}

	candidateRows := ownerRows(candidateAnimeID)
	oneShotRows := ownerRows(oneShotAnimeID)
	// Both paths share one complete provisioner and every per-anime section is
	// one-time input stored as a complete copy, so the candidate and one-shot
	// owner rows must be identical.
	if !reflect.DeepEqual(candidateRows, oneShotRows) {
		t.Fatalf("candidate owner rows differ from shared provisioning\ncandidate rows: %#v\none-shot rows: %#v\nfrozen sections: %#v", candidateRows, oneShotRows, resolved.Sections)
	}
	candidateJSON, err := json.Marshal(candidateRows)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("identical candidate/one-shot owner rows: %s", candidateJSON)
}
