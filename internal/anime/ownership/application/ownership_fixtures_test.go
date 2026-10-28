package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	animepersistence "bansoko/internal/anime/adapter/persistence"
	rootapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/filemaintenance"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/animetest"
	draftapplication "bansoko/internal/testutil/draftfixture"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// This file holds the ownership bucket's own fixture copies. Ownership tests
// build the anime they operate on through the exported draft API, which lives in
// the root anime package until the draft bucket moves; the two services stay
// explicit here so an ownership test never depends on the root service holding
// ownership commands.

type FeedResult = rootapplication.FeedResult
type FeedFetcher = rootapplication.FeedFetcher

// ownershipTestServices keeps the two owning services explicit in tests. The
// embedded service owns the ownership commands; Draft owns the candidate draft
// and direct-subscribe operations the fixtures prepare anime with.
type ownershipTestServices struct {
	*Service
	Draft *draftapplication.Service
}

func (services *ownershipTestServices) SubscribeAnime(ctx context.Context, input generated.SubscribeAnimeInput, idempotencyKey string) (generated.SubscribeResult, error) {
	if input.Draft.Business == nil {
		business, err := frozenpolicy.SeedBusiness(ctx, services.Service.db, `vars.test = {marker: input.release_title};`)
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		// Direct Subscribe copies the current Global parser policy into the
		// instance row (parser_kind, parser_source_default_version), and the
		// count==0 branch below pastes this business in as the frozen snapshot.
		// SeedBusiness hard-codes a regex v1 envelope; align it with the Global
		// row the instance actually got, or the fixture starts with a snapshot
		// that already disagrees with its own row (the Global default is
		// reltitle since migration 040).
		if err := services.Service.db.QueryRowContext(ctx, `SELECT value->>'parser_kind', version FROM global_config_sections WHERE section_name = 'parser_policy'`).Scan(&business.Parser.Kind, &business.Parser.PolicyVersion); err != nil {
			return generated.SubscribeResult{}, err
		}
		if business, err = business.Freeze(); err != nil {
			return generated.SubscribeResult{}, err
		}
		digest, err := business.Digest()
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		raw, err := json.Marshal(business)
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
	result, err := services.Draft.SubscribeAnime(ctx, input, idempotencyKey)
	if err != nil {
		return result, err
	}
	var count int
	if err := services.Service.db.QueryRowContext(ctx, `SELECT count(*) FROM anime_business_configs WHERE managed_anime_id=$1 AND business_snapshot IS NOT NULL`, result.ManagedAnime.Id).Scan(&count); err != nil {
		return result, err
	}
	if count == 0 {
		value, err := animetransport.Draft(input.Draft)
		if err != nil {
			return result, err
		}
		candidate, err := services.Draft.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: value})
		if err != nil {
			return result, err
		}
		raw, err := json.Marshal(value.Business)
		if err != nil {
			return result, err
		}
		if _, err := services.Service.db.ExecContext(ctx, `UPDATE anime_business_configs SET candidate_id=$2, revision=$3, business_snapshot=$4, business_digest=$5, business_version=business_version+1, updated_at=NOW() WHERE managed_anime_id=$1`, result.ManagedAnime.Id, candidate.Id, *candidate.CurrentRevision, raw, *value.BusinessDigest); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (services *ownershipTestServices) ActivateAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input draftapplication.CandidateSubscribeInput) (draftapplication.SubscribeResult, error) {
	return services.Draft.ActivateAnimeConfigCandidate(ctx, candidateID, expectedVersion, idempotencyKey, input)
}

func (services *ownershipTestServices) SaveAnimeConfigCandidate(ctx context.Context, input draftapplication.CandidateDraftInput) (draftapplication.AnimeConfigCandidate, error) {
	return services.Draft.SaveAnimeConfigCandidate(ctx, input)
}

func (services *ownershipTestServices) GetAnimeConfigCandidate(ctx context.Context, candidateID string) (draftapplication.AnimeConfigCandidate, error) {
	return services.Draft.GetAnimeConfigCandidate(ctx, candidateID)
}

func (services *ownershipTestServices) DeleteAnimeConfigCandidate(ctx context.Context, candidateID string) error {
	return services.Draft.DeleteAnimeConfigCandidate(ctx, candidateID)
}

func (services *ownershipTestServices) GetCurrentAnimeConfigCandidateRevision(ctx context.Context, candidateID string) (rootapplication.CandidateRevision, error) {
	return services.Draft.GetCurrentAnimeConfigCandidateRevision(ctx, candidateID)
}

// ownershipActivateCurrentCandidate activates a candidate's current revision
// through the draft API the root package owns. The root test facade that used to
// resolve the revision is not importable across packages, so the owner-local
// fixture resolves it explicitly instead of duplicating the resolution rule.
func ownershipActivateCurrentCandidate(t *testing.T, service *ownershipTestServices, candidate draftapplication.AnimeConfigCandidate, key string) draftapplication.SubscribeResult {
	t.Helper()
	if candidate.CurrentRevision == nil {
		t.Fatalf("candidate %s has no current revision", candidate.Id)
	}
	result, err := service.ActivateAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, key, draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	return result
}

// SetGroupDiscoveryProvider wires one provider into both owning services: draft
// activation runs its after-commit discovery through the root service and the
// ownership resume path through this package, and the tests observe both.
func (services *ownershipTestServices) SetGroupDiscoveryProvider(provider GroupDiscoveryProvider) {
	if services == nil {
		return
	}
	services.Service.SetGroupDiscoveryProvider(provider)
	if services.Draft != nil {
		services.Draft.SetGroupDiscoveryProvider(provider)
	}
}

// ownershipFakeFetcher is a test double for the kernel's FeedFetcher port.
type ownershipFakeFetcher struct {
	result FeedResult
	err    error
	calls  []string
}

func (f *ownershipFakeFetcher) Fetch(_ context.Context, locatorURL string) (FeedResult, error) {
	f.calls = append(f.calls, locatorURL)
	return f.result, f.err
}

func ownershipNewTestService(t *testing.T, db *sql.DB, fetcher FeedFetcher) *ownershipTestServices {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store, Connections: ownershipNoConnections{}})
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	// The draft subscribe path provisions the instance parser policy, which the
	// ownership source actions then require.
	parserPolicies := parserpolicy.NewService(db)
	draftService.SetActivityWriter(activityWriter)
	// Subscribe provisions every axis from the draft's business snapshot; the
	// frozen parser port writes the instance parser row, as in production.
	draftService.SetFrozenParserPolicy(parserPolicies)
	service, err := NewService(db, Dependencies{
		WorkItems:       store,
		OneShotMarkers:  animepersistence.NewRepository(db),
		ActivityWriter:  activityWriter,
		OffsetAlignment: filemaintenance.NewOffsetAlignmentService(),
		Connections:     ownershipNoConnections{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ownershipTestServices{Service: service, Draft: draftService}
}

func ownershipLocatorInput(_ *testing.T, url string) string { return url }

func ownershipDefaultSubgroups(t *testing.T) generated.SubgroupsSection {
	t.Helper()
	return generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3}
}

// ownershipDefaultRules returns rules carrying every section at its code
// default: the complete copy a draft created from a never-saved Global holds.
func ownershipDefaultRules(t *testing.T) generated.RulesDraft {
	t.Helper()
	return presetdraft.DefaultRules(t)
}

func ownershipTestDraft(t *testing.T) generated.AnimeDraft {
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
		Subgroups: ownershipDefaultSubgroups(t),
		Rules:     ownershipDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{
			{SourceKind: generated.SourceKindCustomRss, Locator: ownershipLocatorInput(t, "https://feeds.example.com/rss.xml"), GroupName: "TestGroup", Title: "Test Show"},
		},
	}
}

func ownershipSubscribeTestDraft(t *testing.T, service *ownershipTestServices, key string) generated.SubscribeResult {
	t.Helper()
	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, key)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return result
}

// ownershipProviderDraft returns a provider-origin draft, which is what makes
// the pending-candidate reuse rule apply.
func ownershipProviderDraft(t *testing.T, entryRef, title string) generated.AnimeDraft {
	t.Helper()
	draft := ownershipTestDraft(t)
	draft.Origin = generated.SubscriptionOriginMikan
	draft.OriginEntryRef = animetest.StringPtr(entryRef)
	draft.InstanceLabel = nil
	draft.Title = title
	draft.RssSources = []generated.RSSSourceDraft{}
	return draft
}

// ownershipCustomDraft returns a custom-origin draft without an origin entry
// reference (the final origin shape from migration 027).
func ownershipCustomDraft(t *testing.T, title string) generated.AnimeDraft {
	t.Helper()
	draft := ownershipTestDraft(t)
	draft.OriginEntryRef = nil
	draft.Title = title
	return draft
}

func ownershipSaveCandidate(t *testing.T, service *ownershipTestServices, draft generated.AnimeDraft) draftapplication.AnimeConfigCandidate {
	t.Helper()
	candidate, err := service.SaveAnimeConfigCandidate(context.Background(), draftapplication.CandidateDraftInput{Draft: presetdraft.LoadDomain(t, service.Service.db, mustDomainDraftForOwnership(t, draft))})
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	return candidate
}

// mustDomainDraftForOwnership converts a transport draft through the exported
// anime transport mapper; the shared fixture used the draft bucket's unexported
// domainDraft, which an ownership test may not reference.
func mustDomainDraftForOwnership(t *testing.T, draft generated.AnimeDraft) animedomain.AnimeDraft {
	t.Helper()
	mapped, err := animetransport.Draft(draft)
	if err != nil {
		t.Fatalf("map candidate draft: %v", err)
	}
	return mapped
}

// ownershipRecordingDiscoveryProvider records the discovery calls and whether
// the owner rows were already committed when the provider was asked.
type ownershipRecordingDiscoveryProvider struct {
	mu           sync.Mutex
	db           *sql.DB
	result       DiscoverResult
	err          error
	calls        []string
	commitChecks []bool
}

func (p *ownershipRecordingDiscoveryProvider) DiscoverGroups(ctx context.Context, managedAnimeID string) (DiscoverResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, managedAnimeID)
	p.mu.Unlock()

	committed := false
	if p.db != nil {
		checkCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var managedAnimeCount, subscriptionCount int
		managedErr := p.db.QueryRowContext(checkCtx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, managedAnimeID).Scan(&managedAnimeCount)
		subscriptionErr := p.db.QueryRowContext(checkCtx, `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, managedAnimeID).Scan(&subscriptionCount)
		committed = managedErr == nil && subscriptionErr == nil && managedAnimeCount == 1 && subscriptionCount == 1
	}
	p.mu.Lock()
	p.commitChecks = append(p.commitChecks, committed)
	result, err := p.result, p.err
	p.mu.Unlock()
	return result, err
}

func (p *ownershipRecordingDiscoveryProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *ownershipRecordingDiscoveryProvider) committed(index int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return index >= 0 && index < len(p.commitChecks) && p.commitChecks[index]
}

func ownershipMustRulesPatch(t *testing.T, patch generated.RulesPatch) RulesPatchInput {
	t.Helper()
	sections, err := animetransport.RulesSections(patch.Sections)
	if err != nil {
		t.Fatal(err)
	}
	return RulesPatchInput{ExpectedVersion: int64(patch.ExpectedVersion), Sections: sections}
}

func ownershipReplaceSubgroups(t *testing.T, maxRSS int) generated.SubgroupsSection {
	t.Helper()
	return generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: maxRSS}
}

func ownershipReplaceParserRules(t *testing.T, patterns []string) generated.RulesDraft {
	t.Helper()
	draft := ownershipDefaultRules(t)
	draft.Sections.Parser = &generated.ParserRulesOverride{}
	if err := draft.Sections.Parser.FromParserRulesReplace(generated.ParserRulesReplace{
		Mode:  generated.ParserRulesReplaceMode("replace"),
		Value: generated.ParserRules{EpisodePatterns: patterns},
	}); err != nil {
		t.Fatal(err)
	}
	return draft
}
