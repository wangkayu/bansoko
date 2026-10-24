package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	rootanimepersistence "bansoko/internal/anime/adapter/persistence"
	rootapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/filemaintenance"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// not use another bucket's unexported fixtures, so these are copies of the
// shared ones under readmodel-local names; they are faithful, including the
// draft shapes, and only use exported symbols plus animetest primitives.

type FeedResult = rootapplication.FeedResult
type FeedFetcher = rootapplication.FeedFetcher
type SeedPayload = rootapplication.SeedPayload

// readmodelFakeFetcher is a test double for the kernel's FeedFetcher port.
type readmodelFakeFetcher struct {
	result FeedResult
	err    error
	calls  []string
}

func (f *readmodelFakeFetcher) Fetch(_ context.Context, locatorURL string) (FeedResult, error) {
	f.calls = append(f.calls, locatorURL)
	return f.result, f.err
}

// readmodelTestServices keeps the owning services explicit in tests. The
// embedded ownership service owns the owner projections and owner mutations;
// Draft owns the candidate draft APIs the fixtures prepare anime with, and the
// kernel service supplies the config and parser-policy reads.
type readmodelTestServices struct {
	*ownershipapplication.Service
	Kernel *rootapplication.Service
	Draft  *draftapplication.Service
	DB     *sql.DB
}

func (services *readmodelTestServices) SubscribeAnime(ctx context.Context, input generated.SubscribeAnimeInput, idempotencyKey string) (generated.SubscribeResult, error) {
	if input.Draft.Business == nil {
		b, err := frozenpolicy.SeedBusiness(ctx, services.DB, `vars.actual = {title: input.release_title};`)
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		digest, err := b.Digest()
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		var wire generated.BusinessSnapshot
		if err := json.Unmarshal(raw, &wire); err != nil {
			return generated.SubscribeResult{}, err
		}
		input.Draft.Business = &wire
		input.Draft.BusinessDigest = &digest
	}
	return services.Draft.SubscribeAnime(ctx, input, idempotencyKey)
}

func (services *readmodelTestServices) SaveAnimeConfigCandidate(ctx context.Context, input draftapplication.CandidateDraftInput) (draftapplication.AnimeConfigCandidate, error) {
	return services.Draft.SaveAnimeConfigCandidate(ctx, input)
}

func (services *readmodelTestServices) GetAnimeConfigCandidateRevision(ctx context.Context, candidateID string, revision int64) (rootapplication.CandidateRevision, error) {
	return services.Draft.GetAnimeConfigCandidateRevision(ctx, candidateID, revision)
}

// readmodelNewTestServices builds independent ownership and readmodel services.
// Owner writes use the ownership service, candidate setup uses the draft
// service, the config and parser-policy readers use the kernel service, and
// projections use the readmodel service with the ownership service as its owner
// reader and the draft service as its revision reader.
func readmodelNewTestServices(t *testing.T, db *sql.DB, fetcher rootapplication.FeedFetcher) (*Service, *readmodelTestServices) {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := rootapplication.NewService(db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draftService.SetActivityWriter(activityWriter)
	// Wired like the server composition root: a subscribed instance stores the
	// retention, parser and file-effect axes its draft's business snapshot froze.
	draftService.SetFrozenParserPolicy(parserpolicy.NewService(db))
	owners, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{
		WorkItems:                store,
		OneShotMarkers:           rootanimepersistence.NewRepository(db),
		ActivityWriter:           activityWriter,
		OffsetMigrationAdmission: filemaintenance.NewOffsetMigrationAdmissionService(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return newReadmodelService(t, db, kernel, draftService, owners, nil), &readmodelTestServices{Service: owners, Kernel: kernel, Draft: draftService, DB: db}
}

func newReadmodelService(t *testing.T, db *sql.DB, kernel *rootapplication.Service, draft *draftapplication.Service, owners OwnerReader, previews SeedPreviewSource) *Service {
	t.Helper()
	service, err := NewService(db, Dependencies{
		Config: kernel, Owners: owners, Revisions: draft,
		SeedPreviews: previews, MediaFacts: facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// readmodelCandidateInput maps a wire draft and gives it the business snapshot
// a real draft carries from the moment it is created (the copy of Global).
func readmodelCandidateInput(t *testing.T, db *sql.DB, draft generated.AnimeDraft) draftapplication.CandidateDraftInput {
	t.Helper()
	mapped, err := animetransport.Draft(draft)
	if err != nil {
		t.Fatalf("map candidate draft: %v", err)
	}
	return draftapplication.CandidateDraftInput{Draft: presetdraft.LoadDomain(t, db, mapped)}
}

func saveCandidate(t *testing.T, service *readmodelTestServices, draft generated.AnimeDraft) draftapplication.AnimeConfigCandidate {
	t.Helper()
	candidate, err := service.SaveAnimeConfigCandidate(context.Background(), readmodelCandidateInput(t, service.DB, draft))
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	return candidate
}

func readmodelLocatorInput(_ *testing.T, url string) string { return url }
func readmodelNewTestService(t *testing.T, db *sql.DB, fetcher FeedFetcher) *Service {
	readmodel, _ := readmodelNewTestServices(t, db, fetcher)
	return readmodel
}

func readmodelNewWorkItems(t *testing.T, db *sql.DB) *workitems.Store {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func readmodelMustRulesPatch(t *testing.T, patch generated.RulesPatch) ownershipapplication.RulesPatchInput {
	t.Helper()
	sections, err := animetransport.RulesSections(patch.Sections)
	if err != nil {
		t.Fatal(err)
	}
	return ownershipapplication.RulesPatchInput{ExpectedVersion: int64(patch.ExpectedVersion), Sections: sections}
}

func readmodelDefaultSubgroups(t *testing.T) generated.SubgroupsSection {
	t.Helper()
	return generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3}
}

// readmodelDefaultRules returns rules carrying every section at its code
// default: the complete copy a draft created from a never-saved Global holds.
func readmodelDefaultRules(t *testing.T) generated.RulesDraft {
	t.Helper()
	return presetdraft.DefaultRules(t)
}

func readmodelTestDraft(t *testing.T) generated.AnimeDraft {
	t.Helper()
	season := 1
	return generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		InstanceLabel: animetest.StringPtr("test-instance"),
		Title:         "Test Show",
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-123"},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-456", MediaKind: generated.TMDBMediaKindTv, Season: season},
		},
		Subgroups: readmodelDefaultSubgroups(t),
		Rules:     readmodelDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{
			{SourceKind: generated.SourceKindCustomRss, Locator: readmodelLocatorInput(t, "https://feeds.example.com/rss.xml"), GroupName: "TestGroup", Title: "Test Show"},
		},
	}
}

func readmodelProviderDraftWithGroups(t *testing.T, origin generated.SubscriptionOrigin, entryRef string, groups ...string) generated.AnimeDraft {
	t.Helper()
	draft := readmodelTestDraft(t)
	draft.Origin = origin
	draft.OriginEntryRef = animetest.StringPtr(entryRef)
	draft.InstanceLabel = nil
	draft.Title = "Preview Show"
	draft.RssSources = []generated.RSSSourceDraft{}
	entries := make([]generated.SubgroupScoreEntry, 0, len(groups))
	for _, group := range groups {
		provider := generated.ProviderKindAnibt
		if origin == generated.SubscriptionOriginMikan {
			provider = generated.ProviderKindMikan
		}
		entries = append(entries, generated.SubgroupScoreEntry{
			Provider: provider, GroupRef: group, DisplayName: "Group " + group,
			SelectionMode: generated.SelectionModeAuto, Score: 50, CountInLimit: true,
			EffectiveSelected: false,
		})
	}
	draft.Subgroups = generated.SubgroupsSection{MaxRss: 3, Entries: entries}
	return draft
}

func readmodelSubscribeTestDraft(t *testing.T, service *readmodelTestServices, key string) generated.SubscribeResult {
	t.Helper()
	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: readmodelTestDraft(t), Start: new(true)}, key)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return result
}
