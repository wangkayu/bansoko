package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	animeconfig "bansoko/internal/anime/animeconfig"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// providerDraft returns a draft that carries a provider identity, which is what
// makes the pending-candidate reuse rule apply. testDraft is custom-origin and
// therefore always creates a fresh candidate. A provider draft carries no RSS
// sources: provider_group sources are supplied by the subgroups selection after
// discovery (feeding a provider RSS URL directly would bypass discovery).
func providerDraft(t *testing.T, entryRef, title string) generated.AnimeDraft {
	t.Helper()
	draft := draftTestDraft(t)
	draft.Origin = generated.SubscriptionOriginMikan
	draft.OriginEntryRef = animetest.StringPtr(entryRef)
	draft.InstanceLabel = nil
	draft.Title = title
	draft.RssSources = []generated.RSSSourceDraft{}
	return draft
}

// customDraft returns a custom-origin draft. Custom/oneshot drafts never carry
// an origin_entry_ref (the final origin shape from migration 027).
func customDraft(t *testing.T, title string) generated.AnimeDraft {
	t.Helper()
	draft := draftTestDraft(t)
	draft.OriginEntryRef = nil
	draft.Title = title
	return draft
}

// candidateInput maps a wire draft and gives it the business snapshot a real
// draft carries from the moment it is created (the copy of Global).
func candidateInput(t *testing.T, db *sql.DB, draft generated.AnimeDraft) CandidateDraftInput {
	t.Helper()
	mapped, err := domainDraft(draft)
	if err != nil {
		t.Fatalf("map candidate draft: %v", err)
	}
	return CandidateDraftInput{Draft: presetdraft.LoadDomain(t, db, mapped)}
}

func saveCandidate(t *testing.T, service *Service, draft generated.AnimeDraft) AnimeConfigCandidate {
	t.Helper()
	candidate, err := service.SaveAnimeConfigCandidate(context.Background(), candidateInput(t, service.db, draft))
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	return candidate
}

type recordingGroupDiscoveryProvider struct {
	mu           sync.Mutex
	db           *sql.DB
	result       DiscoverResult
	err          error
	calls        []string
	commitChecks []bool
}

func (p *recordingGroupDiscoveryProvider) DiscoverGroups(ctx context.Context, managedAnimeID string) (DiscoverResult, error) {
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

func (p *recordingGroupDiscoveryProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *recordingGroupDiscoveryProvider) committed(index int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return index >= 0 && index < len(p.commitChecks) && p.commitChecks[index]
}

type panickingGroupDiscoveryProvider struct {
	calls int
}

func (p *panickingGroupDiscoveryProvider) DiscoverGroups(context.Context, string) (DiscoverResult, error) {
	p.calls++
	panic("provider discovery panic")
}

type recordingRuntimeLogger struct {
	mu     sync.Mutex
	events []observabilityport.RuntimeEvent
}

func (l *recordingRuntimeLogger) record(event observabilityport.RuntimeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *recordingRuntimeLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.record(event)
}

func (l *recordingRuntimeLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.record(event)
}

func (l *recordingRuntimeLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.record(event)
}

func (l *recordingRuntimeLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.record(event)
}

func (l *recordingRuntimeLogger) snapshot() []observabilityport.RuntimeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]observabilityport.RuntimeEvent(nil), l.events...)
}

func TestSubscribeAnimeConfigCandidateDiscoversProviderAfterCommit(t *testing.T) {
	for _, initialState := range []generated.SubscriptionState{
		generated.SubscriptionStateActive,
		generated.SubscriptionStateStopped,
	} {
		t.Run(string(initialState), func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := draftNewTestService(t, db)
			provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
			service.SetGroupDiscoveryProvider(provider)
			candidate := saveCandidate(t, service, providerDraft(t, "mikan-after-commit", "After Commit"))

			result, err := service.SubscribeAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, "candidate-after-commit", generated.CandidateSubscribeInput{InitialState: &initialState})
			if err != nil {
				t.Fatalf("activate provider candidate: %v", err)
			}
			if result.Subscription.State != initialState {
				t.Fatalf("subscription state = %q, want %q", result.Subscription.State, initialState)
			}
			if provider.count() != 1 || !provider.committed(0) {
				t.Fatalf("discovery calls = %d, committed check = %t; want one post-commit call", provider.count(), provider.committed(0))
			}
			if got := animetest.CountRows(t, db, "work_items"); got != 0 {
				t.Fatalf("no-groups discovery must not create work items, got %d", got)
			}
		})
	}
}

func TestSubscribeAnimeConfigCandidateDiscoveryReplayRunsOnce(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	candidate := saveCandidate(t, service, providerDraft(t, "mikan-replay-discovery", "Replay Discovery"))
	ctx := context.Background()

	first, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-discovery-replay", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("first activation: %v", err)
	}
	before := animetest.ProductRowCounts(t, db)
	replay, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-discovery-replay", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("replay activation: %v", err)
	}
	if replay.ManagedAnime.Id != first.ManagedAnime.Id {
		t.Fatalf("replay anime = %s, want %s", replay.ManagedAnime.Id, first.ManagedAnime.Id)
	}
	if provider.count() != 1 {
		t.Fatalf("replay must not rediscover provider groups, got %d calls", provider.count())
	}
	for table, want := range before {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("replay changed %s = %d, want %d", table, got, want)
		}
	}
}

func TestSubscribeAnimeConfigCandidateDiscoveryDegradesWithoutRollback(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		provider GroupDiscoveryProvider
		wantCode string
	}{
		{name: "nil provider", wantCode: animeapplication.ProviderDiscoveryReasonCode},
		{name: "provider error", provider: &recordingGroupDiscoveryProvider{err: errors.New("provider payload https://private.example/secret")}, wantCode: animeapplication.ProviderDiscoveryReasonCode},
		{name: "provider outcome", provider: &recordingGroupDiscoveryProvider{result: DiscoverResult{Outcome: "provider_error", Reason: "raw provider detail"}}, wantCode: animeapplication.ProviderDiscoveryReasonCode},
		{name: "provider panic", provider: &panickingGroupDiscoveryProvider{}, wantCode: animeapplication.ProviderDiscoveryReasonCode},
		{name: "typed nil provider", provider: (*recordingGroupDiscoveryProvider)(nil), wantCode: animeapplication.ProviderDiscoveryReasonCode},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := draftNewTestService(t, db)
			logger := &recordingRuntimeLogger{}
			service.SetLogger(logger)
			if testCase.provider != nil {
				service.SetGroupDiscoveryProvider(testCase.provider)
			}
			candidate := saveCandidate(t, service, providerDraft(t, "mikan-degraded", "Degraded Discovery"))

			result, err := service.SubscribeAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, "candidate-discovery-degraded", generated.CandidateSubscribeInput{Start: new(true)})
			if err != nil {
				t.Fatalf("activation must remain successful: %v", err)
			}
			if result.ManagedAnime.Id == "" || animetest.CountRows(t, db, "managed_anime") != 1 || animetest.CountRows(t, db, "subscriptions") != 1 {
				t.Fatal("owner transaction did not commit")
			}
			events := logger.snapshot()
			if len(events) != 1 {
				t.Fatalf("warning events = %d, want 1: %+v", len(events), events)
			}
			event := events[0]
			if event.Message != animeapplication.ProviderDiscoveryMessage || event.Component != animeapplication.ProviderDiscoveryComponent ||
				event.Operation != animeapplication.ProviderDiscoveryOperation || event.Outcome != "warning" ||
				event.ReasonCode != testCase.wantCode || event.OwnerType != "managed_anime" ||
				event.OwnerID != result.ManagedAnime.Id || event.ErrorClass != animeapplication.ProviderDiscoveryErrorClass {
				t.Fatalf("unexpected bounded warning: %+v", event)
			}
		})
	}
}

type panickingRuntimeLogger struct{}

func (panickingRuntimeLogger) Debug(context.Context, observabilityport.RuntimeEvent) {
	panic("debug logger panic")
}
func (panickingRuntimeLogger) Info(context.Context, observabilityport.RuntimeEvent) {
	panic("info logger panic")
}
func (panickingRuntimeLogger) Warn(context.Context, observabilityport.RuntimeEvent) {
	panic("warn logger panic")
}
func (panickingRuntimeLogger) Error(context.Context, observabilityport.RuntimeEvent) {
	panic("error logger panic")
}

func TestProviderDiscoveryWarningDoesNotPropagateLoggerPanic(t *testing.T) {
	animeapplication.SafeProviderDiscoveryWarning(context.Background(), panickingRuntimeLogger{}, "anime-logger-panic")
	var typedNil *recordingRuntimeLogger
	if !animeapplication.IsTypedNil(typedNil) || animeapplication.IsTypedNil(observabilityport.NopRuntimeLogger{}) {
		t.Fatal("typed nil detection did not distinguish nil pointer from no-op logger")
	}
}

func TestSaveAnimeConfigCandidateCreatesNoRuntimeOwner(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	before := animetest.ProductRowCounts(t, db)
	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))

	if candidate.Version != 1 {
		t.Fatalf("new candidate version = %d, want 1", candidate.Version)
	}
	if candidate.CurrentRevision == nil || *candidate.CurrentRevision != 1 || candidate.CurrentRevisionDigest == nil || *candidate.CurrentRevisionDigest == "" {
		t.Fatalf("new candidate must return its immutable revision identity: %+v", candidate)
	}
	if candidate.ActivatedManagedAnimeId != nil {
		t.Fatalf("new candidate is activated: %v", *candidate.ActivatedManagedAnimeId)
	}
	if candidate.Draft.Title != "Show A" {
		t.Fatalf("stored draft title = %q, want %q", candidate.Draft.Title, "Show A")
	}
	// Saving a candidate is a planning action. Any owner or work item created
	// here would mean a draft silently started acquiring.
	for table, want := range before {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("save candidate wrote runtime rows: %s = %d, want %d", table, got, want)
		}
	}
}

func TestSaveAnimeConfigCandidateRoundTripsDatabaseDigest(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	draft := customDraft(t, "Show < A \u2028 \u2029 日本")
	draft.InstanceLabel = animetest.StringPtr("实例 \u2028")
	draft.Metadata.Tmdb.Season = 2
	draft.Subgroups.MaxRss = 42
	draft.Rules.Sections.Parser = &generated.ParserRulesOverride{}
	if err := draft.Rules.Sections.Parser.FromParserRulesReplace(generated.ParserRulesReplace{
		Mode:  generated.ParserRulesReplaceMode("replace"),
		Value: generated.ParserRules{EpisodePatterns: []string{"<>&", "é\u2028"}},
	}); err != nil {
		t.Fatalf("set parser replacement: %v", err)
	}

	candidate := saveCandidate(t, service, draft)
	want, err := canonicalAnimeDraftHash(candidate.Draft)
	if err != nil {
		t.Fatalf("hash saved draft: %v", err)
	}
	if candidate.DraftDigest != want {
		t.Fatalf("saved draft digest = %q, want Go canonical hash %q", candidate.DraftDigest, want)
	}
	var databaseDigest string
	if err := db.QueryRowContext(context.Background(), `
SELECT 'sha256:' || encode(
    digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'),
    'hex'
)
FROM anime_config_candidates
WHERE id = $1`, candidate.Id).Scan(&databaseDigest); err != nil {
		t.Fatalf("read database canonical digest: %v", err)
	}
	if databaseDigest != want {
		t.Fatalf("database canonical digest = %q, want Go digest %q", databaseDigest, want)
	}

	updatedDraft := customDraft(t, "Show < B \u2028 \u2029 日本")
	updatedDraft.InstanceLabel = animetest.StringPtr("实例 B")
	updated, err := service.PatchAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, candidateInput(t, service.db, updatedDraft))
	if err != nil {
		t.Fatalf("patch candidate: %v", err)
	}
	want, err = canonicalAnimeDraftHash(updated.Draft)
	if err != nil {
		t.Fatalf("hash patched draft: %v", err)
	}
	if updated.DraftDigest != want {
		t.Fatalf("patched draft digest = %q, want Go canonical hash %q", updated.DraftDigest, want)
	}
}

func TestCandidateDigestTriggerRejectsSuppliedMismatch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	draft := candidateInput(t, db, customDraft(t, "Digest trigger")).Draft
	draftJSON, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `
INSERT INTO anime_config_candidates(
    id, origin, origin_entry_ref, title, instance_label, draft, draft_digest
)
VALUES ('candidate-wrong-digest', 'custom', NULL, 'Digest trigger', 'digest-trigger', $1, $2)`, draftJSON, "sha256:"+strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "candidate draft digest does not match canonical draft") {
		t.Fatalf("wrong supplied digest error = %v, want canonical digest rejection", err)
	}
	if got := animetest.CountRows(t, db, "anime_config_candidates"); got != 0 {
		t.Fatalf("rejected candidate insert left %d rows", got)
	}
}

func TestCanonicalJSONHashMatchesPostgresJSONBCanonicalizer(t *testing.T) {
	db := animetest.OpenTestDB(t)
	cases := []struct {
		name  string
		value any
		input string
	}{
		{
			name:  "unicode key and special strings",
			value: map[string]any{"é": "<>& \u2028 \u2029 日本", "a": "literal \\u2028"},
			input: `{"a":"literal \\u2028","é":"<>& \u2028 \u2029 日本"}`,
		},
		{
			name:  "integer numbers",
			value: map[string]any{"negative": json.Number("-7"), "zero": json.Number("0"), "large": json.Number("9007199254740991")},
			input: `{"large":9007199254740991,"negative":-7,"zero":0}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := canonicalJSONHash(tc.value)
			if err != nil {
				t.Fatalf("hash Go canonical JSON: %v", err)
			}
			var got string
			if err := db.QueryRowContext(context.Background(), `
SELECT 'sha256:' || encode(
    digest(convert_to(anime_config_candidate_canonical_json($1::jsonb), 'UTF8'), 'sha256'),
    'hex'
)`, tc.input).Scan(&got); err != nil {
				t.Fatalf("hash PostgreSQL canonical JSON: %v", err)
			}
			if got != want {
				t.Fatalf("PostgreSQL digest = %q, Go digest = %q", got, want)
			}
		})
	}
}

func TestSaveAnimeConfigCandidateRejectsWorkingProviderDuplicate(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	// Provider identity allows one unlinked working row. A duplicate save is a
	// stable conflict and leaves the existing draft unchanged.
	first := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	_, err := service.SaveAnimeConfigCandidate(context.Background(), candidateInput(t, service.db, providerDraft(t, "mikan-entry-1", "Show A renamed")))
	assertCandidateBusinessCode(t, err, "CANDIDATE_WORKING_DUPLICATE")
	if first.Version != 1 {
		t.Fatalf("first candidate version = %d, want 1", first.Version)
	}
	if got := animetest.CountRows(t, db, "anime_config_candidates"); got != 1 {
		t.Fatalf("candidate rows = %d, want 1", got)
	}
}

func TestSaveAnimeConfigCandidateCustomWithoutEntryRefAlwaysCreates(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	// A custom draft without origin_entry_ref has no stable identity: even two
	// identical saves must stay two candidates, because there is no key that
	// could tell them apart from an unrelated draft.
	first := saveCandidate(t, service, draftTestDraft(t))
	second := saveCandidate(t, service, draftTestDraft(t))

	if second.Id == first.Id {
		t.Fatalf("second save reused candidate %s without any stable identity", first.Id)
	}
	if got := animetest.CountRows(t, db, "anime_config_candidates"); got != 2 {
		t.Fatalf("candidate rows = %d, want 2", got)
	}
}

func TestSaveAnimeConfigCandidateDoesNotReuseActivatedCandidate(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	activated := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, activated.Id, activated.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}

	// Re-planning the same provider entry must produce a fresh candidate.
	// Writing back into the activated one would leave a draft that can be saved
	// but never activated, because activation refuses an activated candidate.
	replanned := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A season 2"))
	if replanned.Id == activated.Id {
		t.Fatalf("re-plan wrote back into the activated candidate %s", activated.Id)
	}
	if replanned.ActivatedManagedAnimeId != nil {
		t.Fatalf("re-planned candidate is already activated")
	}
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, replanned.Id, replanned.Version, "candidate-activate-2", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("the re-planned candidate must be activatable: %v", err)
	}
}

func TestPatchAnimeConfigCandidateRejectsStaleVersion(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	updatedDraft := providerDraft(t, "mikan-entry-1", "Show A edited")
	updated, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, candidateInput(t, service.db, updatedDraft))
	if err != nil {
		t.Fatalf("patch candidate: %v", err)
	}
	if updated.Version != candidate.Version+1 {
		t.Fatalf("patched version = %d, want %d", updated.Version, candidate.Version+1)
	}

	staleDraft := providerDraft(t, "mikan-entry-1", "Show A stale edit")
	_, err = service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, candidateInput(t, service.db, staleDraft))
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale patch error = %v, want VersionConflict", err)
	}
	if conflict.CurrentVersion != updated.Version {
		t.Fatalf("conflict reports version %d, want %d", conflict.CurrentVersion, updated.Version)
	}
	// The stale write must not have landed.
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if current.Title != "Show A edited" {
		t.Fatalf("stale patch overwrote the newer draft: title = %q", current.Title)
	}
}

func TestSubscribeAnimeConfigCandidateLinksInSameTransaction(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}

	linked, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if linked.ActivatedManagedAnimeId == nil {
		t.Fatal("activated candidate has no link")
	}
	if *linked.ActivatedManagedAnimeId != result.ManagedAnime.Id {
		t.Fatalf("candidate links to %s, want %s", *linked.ActivatedManagedAnimeId, result.ManagedAnime.Id)
	}
	// The link is only meaningful if the owner it names actually exists.
	var ownerCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, *linked.ActivatedManagedAnimeId).Scan(&ownerCount); err != nil {
		t.Fatalf("count linked owner: %v", err)
	}
	if ownerCount != 1 {
		t.Fatalf("linked managed anime rows = %d, want 1", ownerCount)
	}
}

func TestSubscribeAnimeConfigCandidateRejectsSecondActivation(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	before := animetest.ProductRowCounts(t, db)

	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-2", generated.CandidateSubscribeInput{Start: new(true)})
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CANDIDATE_ALREADY_ACTIVATED" {
		t.Fatalf("second activation error = %v, want CANDIDATE_ALREADY_ACTIVATED", err)
	}
	// A refused activation must not leave a half-built owner set behind.
	for table, want := range before {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("refused activation wrote %s = %d, want %d", table, got, want)
		}
	}
}

func TestSubscribeAnimeConfigCandidateReplaysSameKey(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	first, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("first activation: %v", err)
	}
	after := animetest.ProductRowCounts(t, db)

	// Replaying the key that performed the activation is the same activation,
	// not a second one, so it returns the snapshot rather than a 409.
	replay, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("replay activation: %v", err)
	}
	if replay.ManagedAnime.Id != first.ManagedAnime.Id {
		t.Fatalf("replay returned anime %s, want %s", replay.ManagedAnime.Id, first.ManagedAnime.Id)
	}
	for table, want := range after {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("replay wrote %s = %d, want %d", table, got, want)
		}
	}
}

func TestDeleteAnimeConfigCandidateRefusesActivated(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	pending := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	if err := service.DeleteAnimeConfigCandidate(ctx, pending.Id); err != nil {
		t.Fatalf("delete pending candidate: %v", err)
	}
	if err := service.DeleteAnimeConfigCandidate(ctx, pending.Id); err != nil {
		t.Fatalf("idempotent delete of an absent candidate: %v", err)
	}

	activated := saveCandidate(t, service, providerDraft(t, "mikan-entry-2", "Show B"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, activated.Id, activated.Version, "candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	err := service.DeleteAnimeConfigCandidate(ctx, activated.Id)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CANDIDATE_ALREADY_ACTIVATED" {
		t.Fatalf("delete of an activated candidate = %v, want CANDIDATE_ALREADY_ACTIVATED", err)
	}
	if _, err := service.GetAnimeConfigCandidate(ctx, activated.Id); err != nil {
		t.Fatalf("refused delete removed the candidate anyway: %v", err)
	}
}

func TestListAnimeConfigCandidatesFiltersByProviderEntry(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	saveCandidate(t, service, providerDraft(t, "mikan-entry-2", "Show B"))
	saveCandidate(t, service, draftTestDraft(t))

	all, err := service.ListAnimeConfigCandidates(ctx, ListAnimeConfigCandidatesInput{})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered list returned %d candidates, want 3", len(all))
	}

	origin := animedomain.SubscriptionOriginMikan
	entryRef := "mikan-entry-2"
	filtered, err := service.ListAnimeConfigCandidates(ctx, ListAnimeConfigCandidatesInput{
		Origin: &origin, OriginEntryRef: &entryRef,
	})
	if err != nil {
		t.Fatalf("list filtered candidates: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Title != "Show B" {
		t.Fatalf("filtered list = %+v, want only Show B", filtered)
	}
}

func TestSubscribeAnimeConfigCandidateEqualDraftDifferentCandidateSameKeyConflicts(t *testing.T) {
	db := animetest.OpenTestDB(t)
	firstService := draftNewTestService(t, db)
	secondService := draftNewTestService(t, db)
	ctx := context.Background()

	firstCandidate := saveCandidate(t, firstService, draftTestDraft(t))
	secondCandidate := saveCandidate(t, secondService, draftTestDraft(t))
	firstLocked := make(chan struct{})
	secondLocked := make(chan struct{})
	release := make(chan struct{})
	firstService.candidateLockHook = func(operation string) {
		if operation == "activate" {
			close(firstLocked)
			<-release
		}
	}
	secondService.candidateLockHook = func(operation string) {
		if operation == "activate" {
			close(secondLocked)
			<-release
		}
	}

	errorsByCandidate := make(chan error, 2)
	go func() {
		_, err := firstService.SubscribeAnimeConfigCandidate(ctx, firstCandidate.Id, firstCandidate.Version, "candidate-shared-key", generated.CandidateSubscribeInput{Start: new(true)})
		errorsByCandidate <- err
	}()
	go func() {
		_, err := secondService.SubscribeAnimeConfigCandidate(ctx, secondCandidate.Id, secondCandidate.Version, "candidate-shared-key", generated.CandidateSubscribeInput{Start: new(true)})
		errorsByCandidate <- err
	}()
	<-firstLocked
	<-secondLocked
	close(release)

	var successCount, conflictCount int
	for range 2 {
		err := <-errorsByCandidate
		if err == nil {
			successCount++
			continue
		}
		var business *apperrors.Business
		if errors.As(err, &business) && business.Code == "IDEMPOTENCY_KEY_REUSED" {
			conflictCount++
			continue
		}
		t.Fatalf("concurrent activation error = %v", err)
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent outcomes: success=%d conflict=%d, want one each", successCount, conflictCount)
	}
	if got := animetest.CountRows(t, db, "managed_anime"); got != 1 {
		t.Fatalf("managed anime rows = %d, want 1", got)
	}
	if got := animetest.CountRows(t, db, "idempotency_records"); got != 1 {
		t.Fatalf("idempotency rows = %d, want 1", got)
	}
	var activatedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_candidates WHERE activated_managed_anime_id IS NOT NULL`).Scan(&activatedCount); err != nil {
		t.Fatal(err)
	}
	if activatedCount != 1 {
		t.Fatalf("activated candidates = %d, want 1", activatedCount)
	}
}

func TestSubscribeAnimeConfigCandidateReplayKeepsOneOwnerSetAndOneVersionBump(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftTestDraft(t))
	first, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-replay-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	var snapshot []byte
	if err := db.QueryRowContext(ctx, `SELECT result_snapshot FROM idempotency_records WHERE kind = 'subscribe' AND idempotency_key = $1`, "candidate-replay-key").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(snapshot, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["rss_sources"].([]any)[0].(map[string]any)["locator_display"] = "https://user:pass@example.test/private-passkey?foo=secret"
	snapshot, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE idempotency_records SET result_snapshot = $2 WHERE kind = 'subscribe' AND idempotency_key = $1`, "candidate-replay-key", snapshot); err != nil {
		t.Fatal(err)
	}

	replay, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-replay-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("replay candidate: %v", err)
	}
	if replay.ManagedAnime.Id != first.ManagedAnime.Id || replay.Subscription.Id != first.Subscription.Id {
		t.Fatalf("replay returned a different owner set: first=%+v replay=%+v", first, replay)
	}
	if got := replay.RssSources[0].LocatorDisplay; got != "https://example.test" {
		t.Fatalf("candidate replay locator display = %q, want sanitized origin", got)
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != candidate.Version+1 {
		t.Fatalf("candidate version = %d, want exactly one bump to %d", current.Version, candidate.Version+1)
	}
	if current.CurrentRevision == nil || current.CurrentRevisionDigest == nil {
		t.Fatalf("activated candidate has no immutable revision identity: %+v", current)
	}
	wantIdentityHash, err := canonicalJSONHash(struct {
		Kind            string `json:"kind"`
		CandidateID     string `json:"candidate_id"`
		IdempotencyKey  string `json:"idempotency_key"`
		ExpectedVersion int64  `json:"expected_version"`
		Revision        int64  `json:"revision"`
		EffectiveDigest string `json:"effective_digest"`
		InitialState    string `json:"initial_state"`
	}{Kind: "candidate_activation", CandidateID: candidate.Id, IdempotencyKey: "candidate-replay-key", ExpectedVersion: candidate.Version, Revision: *current.CurrentRevision, EffectiveDigest: *current.CurrentRevisionDigest, InitialState: string(generated.SubscriptionStateActive)})
	if err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := db.QueryRowContext(ctx, `SELECT request_hash FROM idempotency_records WHERE kind = 'subscribe' AND idempotency_key = $1`, "candidate-replay-key").Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != wantIdentityHash {
		t.Fatalf("stored activation hash = %q, want canonical identity hash %q", storedHash, wantIdentityHash)
	}
	for table, want := range map[string]int{
		"managed_anime": 1, "subscriptions": 1, "anime_metadata": 1,
		"anime_business_configs": 1, "anime_config_overrides": 7,
		"rss_sources": 1, "rss_source_runtime": 1, "work_items": 1,
		"subscription_log_entries": 1, "idempotency_records": 1,
	} {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestSubscribeAnimeConfigCandidateSameKeyVersionOrDraftConflict(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	first := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Show A"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, first.Id, first.Version, "candidate-version-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate first candidate: %v", err)
	}

	second := saveCandidate(t, service, providerDraft(t, "mikan-entry-2", "Show A"))
	patched, err := service.PatchAnimeConfigCandidate(ctx, second.Id, second.Version, candidateInput(t, service.db, providerDraft(t, "mikan-entry-2", "Show A edited")))
	if err != nil {
		t.Fatalf("patch second candidate: %v", err)
	}
	_, err = service.SubscribeAnimeConfigCandidate(ctx, patched.Id, patched.Version, "candidate-version-key", generated.CandidateSubscribeInput{Start: new(true)})
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	current, err := service.GetAnimeConfigCandidate(ctx, patched.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != patched.Version || current.ActivatedManagedAnimeId != nil {
		t.Fatalf("conflicting activation mutated candidate: %+v", current)
	}
}

func TestSubscribeAnimeConfigCandidateReusedKeyConflictPrecedesAlreadyActivated(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftTestDraft(t))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-first-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	other := saveCandidate(t, service, draftTestDraft(t))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, other.Id, other.Version, "candidate-other-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate other candidate: %v", err)
	}

	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-other-key", generated.CandidateSubscribeInput{Start: new(true)})
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")
}

func TestSubscribeAnimeConfigCandidateNewKeyRejectsAlreadyActivated(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftTestDraft(t))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-first-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("activate candidate: %v", err)
	}

	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-new-key", generated.CandidateSubscribeInput{Start: new(true)})
	assertCandidateBusinessCode(t, err, "CANDIDATE_ALREADY_ACTIVATED")
}

func TestSubscribeAnimeConfigCandidatePatchBeforeActivationConsumesLockedLatestDraft(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Before patch"))
	staleRead, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}

	patchLocked := make(chan struct{})
	releasePatch := make(chan struct{})
	service.candidateLockHook = func(operation string) {
		if operation == "patch" {
			close(patchLocked)
			<-releasePatch
		}
	}
	patchDone := make(chan error, 1)
	go func() {
		_, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, staleRead.Version, candidateInput(t, service.db, providerDraft(t, "mikan-entry-1", "After patch")))
		patchDone <- err
	}()
	<-patchLocked

	// A caller holding a stale read (expected version 1 while the locked
	// current version is 2 after the patch) must be rejected: the activation
	// identity CASes on the caller's If-Match, it does not silently consume
	// whatever version it locks.
	staleActivationDone := make(chan error, 1)
	go func() {
		_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, staleRead.Version, "candidate-patch-first", generated.CandidateSubscribeInput{Start: new(true)})
		staleActivationDone <- err
	}()
	waitForCandidateLockWait(t, db, "SELECT current_revision, current_revision_digest, version, activated_managed_anime_id")
	close(releasePatch)
	if err := <-patchDone; err != nil {
		t.Fatalf("patch candidate: %v", err)
	}
	var conflict *apperrors.VersionConflict
	if err := <-staleActivationDone; !errors.As(err, &conflict) || conflict.CurrentVersion != staleRead.Version+1 {
		t.Fatalf("stale activation error = %v, want current version %d", err, staleRead.Version+1)
	}

	// The same key with the current expected version consumes the locked
	// latest draft (the patch landed before this activation).
	activationDone := make(chan error, 1)
	go func() {
		_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, staleRead.Version+1, "candidate-patch-first", generated.CandidateSubscribeInput{Start: new(true)})
		activationDone <- err
	}()
	if err := <-activationDone; err != nil {
		t.Fatalf("activate candidate with current version: %v", err)
	}
	var activatedID string
	if err := db.QueryRowContext(ctx, `SELECT activated_managed_anime_id FROM anime_config_candidates WHERE id = $1`, candidate.Id).Scan(&activatedID); err != nil {
		t.Fatal(err)
	}
	var activatedTitle string
	if err := db.QueryRowContext(ctx, `SELECT title FROM managed_anime WHERE id = $1`, activatedID).Scan(&activatedTitle); err != nil {
		t.Fatal(err)
	}
	if activatedTitle != "After patch" {
		t.Fatalf("activation consumed title %q, want locked latest draft", activatedTitle)
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != staleRead.Version+2 {
		t.Fatalf("candidate version = %d, want patch + activation bumps to %d", current.Version, staleRead.Version+2)
	}
}

func TestSubscribeAnimeConfigCandidateActivationBeforePatchSerializes(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-1", "Before activation"))

	activationLocked := make(chan struct{})
	releaseActivation := make(chan struct{})
	service.candidateLockHook = func(operation string) {
		if operation == "activate" {
			close(activationLocked)
			<-releaseActivation
		}
	}
	activationDone := make(chan error, 1)
	go func() {
		_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-activate-first", generated.CandidateSubscribeInput{Start: new(true)})
		activationDone <- err
	}()
	<-activationLocked

	patchDone := make(chan error, 1)
	go func() {
		_, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, candidateInput(t, service.db, providerDraft(t, "mikan-entry-1", "Must not land")))
		patchDone <- err
	}()
	waitForCandidateLockWait(t, db, "SELECT version, activated_managed_anime_id")
	close(releaseActivation)
	if err := <-activationDone; err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	var conflict *apperrors.VersionConflict
	if err := <-patchDone; !errors.As(err, &conflict) || conflict.CurrentVersion != candidate.Version+1 {
		t.Fatalf("patch after activation error = %v, want current version %d", err, candidate.Version+1)
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "Before activation" || current.Version != candidate.Version+1 {
		t.Fatalf("patch changed activated candidate: %+v", current)
	}
}

func waitForCandidateLockWait(t *testing.T, db *sql.DB, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRowContext(context.Background(), `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity
    WHERE datname = current_database()
      AND pid <> pg_backend_pid()
      AND state = 'active'
      AND wait_event_type = 'Lock'
      AND cardinality(pg_blocking_pids(pid)) > 0
      AND query LIKE '%' || $1 || '%'
)`, queryFragment).Scan(&waiting)
		if err != nil {
			t.Fatalf("inspect candidate lock wait: %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	rows, err := db.QueryContext(context.Background(), `
SELECT pid, state, COALESCE(wait_event_type, ''), COALESCE(wait_event, ''),
       pg_blocking_pids(pid)::text, query
FROM pg_stat_activity
WHERE datname = current_database() AND pid <> pg_backend_pid()
ORDER BY pid`)
	if err != nil {
		t.Fatalf("candidate lock wait for %q timed out; inspect sessions: %v", queryFragment, err)
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var pid int
		var state, waitType, waitEvent, blockers, query string
		if err := rows.Scan(&pid, &state, &waitType, &waitEvent, &blockers, &query); err != nil {
			t.Fatalf("scan candidate lock wait diagnostics: %v", err)
		}
		sessions = append(sessions, fmt.Sprintf("pid=%d state=%s wait=%s/%s blockers=%s query=%q", pid, state, waitType, waitEvent, blockers, query))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read candidate lock wait diagnostics: %v", err)
	}
	t.Fatalf("candidate query %q never entered PostgreSQL lock wait; sessions: %s", queryFragment, strings.Join(sessions, "; "))
}

const (
	candidateFailureManagedAnimeTable        = "managed_anime"
	candidateFailureSubscriptionTable        = "subscriptions"
	candidateFailureMetadataTable            = "anime_metadata"
	candidateFailureConfigOwnerTable         = "anime_business_configs"
	candidateFailureOverrideTable            = "anime_config_overrides"
	candidateFailureRSSSourceTable           = "rss_sources"
	candidateFailureRSSRuntimeTable          = "rss_source_runtime"
	candidateFailureWorkItemTable            = "work_items"
	candidateFailureIdempotencyTable         = "idempotency_records"
	candidateFailureCandidateTable           = "anime_config_candidates"
	candidateFailureSubscriptionHistoryTable = "subscription_log_entries"
)

func TestSubscribeAnimeConfigCandidateOwnedWriteFailuresRollBackEverything(t *testing.T) {
	cases := []struct {
		name          string
		table         string
		candidateLink bool
	}{
		{name: "early/managed_anime", table: candidateFailureManagedAnimeTable},
		{name: "early/subscription", table: candidateFailureSubscriptionTable},
		{name: "early/metadata", table: candidateFailureMetadataTable},
		{name: "mid/config_owner", table: candidateFailureConfigOwnerTable},
		{name: "mid/override", table: candidateFailureOverrideTable},
		{name: "mid/rss_source", table: candidateFailureRSSSourceTable},
		{name: "mid/rss_runtime", table: candidateFailureRSSRuntimeTable},
		{name: "mid/work_item", table: candidateFailureWorkItemTable},
		{name: "late/idempotency", table: candidateFailureIdempotencyTable},
		{name: "late/candidate_link", table: candidateFailureCandidateTable, candidateLink: true},
		{name: "late/subscription_history", table: candidateFailureSubscriptionHistoryTable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := draftNewTestService(t, db)
			candidate := saveCandidate(t, service, draftTestDraft(t))
			installCandidateActivationFailureTrigger(t, db, tc.table, tc.candidateLink)

			if _, err := service.SubscribeAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, "candidate-boundary-failure", generated.CandidateSubscribeInput{Start: new(true)}); err == nil {
				t.Fatal("activation unexpectedly succeeded despite injected owned-write failure")
			}
			assertCandidateActivationRolledBack(t, db, service, candidate)
		})
	}
}

func installCandidateActivationFailureTrigger(t *testing.T, db *sql.DB, table string, candidateLink bool) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_candidate_activation_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected candidate activation write failure';
END;
$$;
CREATE TRIGGER reject_candidate_activation_write
BEFORE INSERT ON %s
FOR EACH ROW EXECUTE FUNCTION reject_candidate_activation_write();`, table)
	if candidateLink {
		trigger = `
CREATE FUNCTION reject_candidate_activation_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected candidate activation link failure';
END;
$$;
CREATE TRIGGER reject_candidate_activation_write
BEFORE UPDATE OF activated_managed_anime_id ON anime_config_candidates
FOR EACH ROW
WHEN (NEW.activated_managed_anime_id IS NOT NULL)
EXECUTE FUNCTION reject_candidate_activation_write();`
	}
	if _, err := animetest.OwnerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", table, err)
	}
}

func TestSubscribeAnimeConfigCandidateObservabilityFailureRollsBackEverything(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetObservabilityRecorder(observability)
	draft := draftTestDraft(t)
	draft.Metadata = generated.AnimeMetadataDraft{}
	candidate := saveCandidate(t, service, draft)
	installCandidateActivationFailureTrigger(t, db, "routine_activities", false)

	if _, err := service.SubscribeAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, "candidate-observability-failure", generated.CandidateSubscribeInput{Start: new(true)}); err == nil {
		t.Fatal("activation unexpectedly succeeded despite injected observability failure")
	}
	assertCandidateActivationRolledBack(t, db, service, candidate)
}

func TestSubscribeAnimeConfigCandidateLateCommitFailureRollsBackEverything(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	candidate := saveCandidate(t, service, draftTestDraft(t))

	if _, err := animetest.OwnerTestDB(t).ExecContext(ctx, `
CREATE FUNCTION reject_candidate_activation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.activated_managed_anime_id IS NOT NULL THEN
        RAISE EXCEPTION 'injected late candidate activation failure';
    END IF;
    RETURN NEW;
END;
$$;
CREATE CONSTRAINT TRIGGER reject_candidate_activation
AFTER UPDATE OF activated_managed_anime_id ON anime_config_candidates
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION reject_candidate_activation();`); err != nil {
		t.Fatalf("install deferred failure trigger: %v", err)
	}

	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-rollback-key", generated.CandidateSubscribeInput{Start: new(true)}); err == nil {
		t.Fatal("activation unexpectedly succeeded despite deferred commit failure")
	}
	assertCandidateActivationRolledBack(t, db, service, candidate)
}

func assertCandidateActivationRolledBack(t *testing.T, db *sql.DB, service *Service, candidate AnimeConfigCandidate) {
	t.Helper()
	current, err := service.GetAnimeConfigCandidate(context.Background(), candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.ActivatedManagedAnimeId != nil || current.Version != candidate.Version {
		t.Fatalf("candidate link/version escaped rollback: %+v", current)
	}
	for _, table := range []string{
		candidateFailureIdempotencyTable,
		candidateFailureManagedAnimeTable,
		candidateFailureSubscriptionTable,
		candidateFailureMetadataTable,
		candidateFailureConfigOwnerTable,
		candidateFailureOverrideTable,
		candidateFailureRSSSourceTable,
		candidateFailureRSSRuntimeTable,
		candidateFailureSubscriptionHistoryTable,
		candidateFailureWorkItemTable,
		"routine_activities",
	} {
		if got := animetest.CountRows(t, db, table); got != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, got)
		}
	}
}

func assertCandidateBusinessCode(t *testing.T, err error, code string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != code {
		t.Fatalf("error = %v, want business code %s", err, code)
	}
}

func TestSubscribeCandidateInitialStateStopped(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	ctx := context.Background()
	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-stopped", "Show Stopped"))

	stopped := generated.SubscriptionStateStopped
	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-stopped-key", generated.CandidateSubscribeInput{InitialState: &stopped})
	if err != nil {
		t.Fatalf("subscribe stopped one-off instance: %v", err)
	}
	if result.Subscription.State != generated.SubscriptionStateStopped {
		t.Fatalf("subscription state = %v, want stopped", result.Subscription.State)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id = $1`, result.Subscription.Id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" {
		t.Fatalf("persisted state = %q, want stopped", state)
	}
	if provider.count() != 1 || !provider.committed(0) {
		t.Fatalf("stopped activation discovery = %d calls, committed = %t; want one post-commit call", provider.count(), provider.committed(0))
	}
	// A provider draft carries no RSS sources, so there is nothing to schedule
	// an automatic refresh for on a stopped one-off instance. rss_refresh work
	// items are owned by an rss_source_runtime; the subscription exposes no source,
	// so none can exist.
	var refreshItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE kind = 'rss_refresh' AND owner_type = 'rss_source_runtime'
  AND EXISTS (
    SELECT 1 FROM rss_sources s
    JOIN subscriptions sub ON sub.id = s.subscription_id
    WHERE s.id = work_item_history.owner_id AND sub.id = $1
  )`, result.Subscription.Id).Scan(&refreshItems); err != nil {
		t.Fatal(err)
	}
	if refreshItems != 0 {
		t.Fatalf("stopped one-off instance must not schedule automatic refreshes, got %d", refreshItems)
	}

	// The initial state is part of the activation identity: the same
	// idempotency key with a different state is a different request.
	active := generated.SubscriptionStateActive
	_, err = service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-stopped-key", generated.CandidateSubscribeInput{InitialState: &active})
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	// An exact replay returns the original stopped subscription.
	replay, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-stopped-key", generated.CandidateSubscribeInput{InitialState: &stopped})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Subscription.Id != result.Subscription.Id || replay.Subscription.State != generated.SubscriptionStateStopped {
		t.Fatalf("replay must return the original stopped subscription: %+v", replay.Subscription)
	}
}

func TestSubscribeAnimeConfigCandidateStaleExpectedVersionRejected(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-stale", "Show A"))
	before := animetest.ProductRowCounts(t, db)
	// The caller claims a version the candidate never reached: the new
	// activation CAS must refuse it instead of consuming the current version.
	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version+5, "candidate-stale-key", generated.CandidateSubscribeInput{Start: new(true)})
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != candidate.Version {
		t.Fatalf("stale expected activation error = %v, want VersionConflict at version %d", err, candidate.Version)
	}
	for table, want := range before {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("stale activation wrote %s = %d, want %d", table, got, want)
		}
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != candidate.Version || current.ActivatedManagedAnimeId != nil {
		t.Fatalf("stale activation mutated candidate: %+v", current)
	}
}

func TestSubscribeAnimeConfigCandidateSameKeyChangedExpectedVersionRejected(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-exp", "Show A"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-exp-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("first activation: %v", err)
	}

	// The expected version is part of the activation identity: replaying the
	// same key with a different expected version is a different request and
	// must never replay the first result.
	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version+1, "candidate-exp-key", generated.CandidateSubscribeInput{Start: new(true)})
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	// The exact replay (same key, same expected version) still returns the
	// original result even though the first activation bumped the version.
	replay, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-exp-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("exact replay after version bump: %v", err)
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != candidate.Version+1 {
		t.Fatalf("candidate version = %d, want exactly one bump to %d", current.Version, candidate.Version+1)
	}
	var ownerCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, replay.ManagedAnime.Id).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 1 {
		t.Fatalf("replay owner rows = %d, want 1", ownerCount)
	}
}

// TestSaveCandidateRejectsProviderDraftWithoutEntryRef proves the draft
// validator requires an origin_entry_ref for anibt/mikan origins.
func TestSaveCandidateRejectsProviderDraftWithoutEntryRef(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	draft := providerDraft(t, "mikan-entry-1", "Show A")
	draft.OriginEntryRef = nil // remove the required ref

	_, err := service.SaveAnimeConfigCandidate(context.Background(), candidateInput(t, service.db, draft))
	var validation *apperrors.Validation
	if !errors.As(err, &validation) || validation.Code != "DRAFT_INVALID" {
		t.Fatalf("error = %v, want validation code DRAFT_INVALID", err)
	}
}

// TestListCandidatesNewestFirst proves the list endpoint returns candidates
// ordered by last_activity DESC, then updated_at DESC and id DESC.
func TestListCandidatesNewestFirst(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	first := saveCandidate(t, service, customDraft(t, "Show A"))
	// Custom drafts have no stable provider identity, so each explicit save is a
	// distinct server-owned row and the activity ordering remains observable.
	time.Sleep(10 * time.Millisecond)
	second := saveCandidate(t, service, customDraft(t, "Show B"))
	time.Sleep(10 * time.Millisecond)
	third := saveCandidate(t, service, customDraft(t, "Show C"))

	candidates, err := service.ListAnimeConfigCandidates(ctx, ListAnimeConfigCandidatesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidate count = %d, want 3", len(candidates))
	}
	if candidates[0].Id != third.Id {
		t.Fatalf("candidates[0] = %s, want %s (newest)", candidates[0].Id, third.Id)
	}
	if candidates[1].Id != second.Id {
		t.Fatalf("candidates[1] = %s, want %s", candidates[1].Id, second.Id)
	}
	if candidates[2].Id != first.Id {
		t.Fatalf("candidates[2] = %s, want %s (oldest)", candidates[2].Id, first.Id)
	}
}

// TestSubscribeCandidateReplayRejectsCorruptedManagedAnimeID proves the replay
// path rejects an idempotency record whose managed_anime_id doesn't match the
// persisted result snapshot. We simulate corruption by directly updating the
// idempotency record's managed_anime_id to a different real anime after a
// successful activation.
func TestSubscribeCandidateReplayRejectsCorruptedManagedAnimeID(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-corrupt-1", "Show A"))
	if _, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-corrupt-key", generated.CandidateSubscribeInput{Start: new(true)}); err != nil {
		t.Fatalf("first activation: %v", err)
	}

	// Create a second real anime so the FK on managed_anime_id is satisfied,
	// then repoint the idempotency record at it. The result_snapshot still
	// references the original anime, so the replay integrity check must fire.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('decoy-anime', 'Decoy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE idempotency_records SET managed_anime_id = 'decoy-anime' WHERE kind = 'subscribe' AND idempotency_key = $1`, "candidate-corrupt-key"); err != nil {
		t.Fatal(err)
	}

	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-corrupt-key", generated.CandidateSubscribeInput{Start: new(true)})
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")
}

// TestSubscribeAnimeConfigCandidateMaterializesCompleteRevision proves that
// activation locks the candidate's saved revision into complete instance-owned
// configuration: every canonical section is written as a replace override, and a
// later Global change cannot reach the locked instance.
// TestSubscribeAnimeConfigCandidateFreezesParserPolicy proves activation writes
// the parser kind the revision froze at save time, not the global
// creation default that may have changed afterwards.
func TestSubscribeAnimeConfigCandidateFreezesParserPolicy(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftTestDraft(t))
	revision, err := service.GetAnimeConfigCandidateRevision(ctx, candidate.Id, *candidate.CurrentRevision)
	if err != nil {
		t.Fatalf("read saved revision: %v", err)
	}
	if revision.ParserPolicy.Kind != "reltitle" {
		t.Fatalf("frozen parser kind = %q, want the reltitle default", revision.ParserPolicy.Kind)
	}
	if revision.ParserPolicyDigest == "" {
		t.Fatal("frozen parser policy digest is missing")
	}

	// Change the global creation default after the save. Activation must ignore it.
	if _, err := db.ExecContext(ctx, `
UPDATE global_config_sections
SET value = jsonb_build_object('parser_kind', 'regex'), version = version + 1
WHERE section_name = 'parser_policy'`); err != nil {
		t.Fatal(err)
	}

	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-parser-policy-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	var kind string
	if err := db.QueryRowContext(ctx, `
SELECT parser_kind FROM anime_business_configs WHERE managed_anime_id = $1`, result.ManagedAnime.Id).
		Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != revision.ParserPolicy.Kind {
		t.Fatalf("instance parser policy kind = %s, want the frozen %s", kind, revision.ParserPolicy.Kind)
	}
}

func TestSubscribeAnimeConfigCandidateMaterializesCompleteRevision(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, draftTestDraft(t))
	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-materialize-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}

	var totalOverrides, nullOverrides int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE value IS NULL)
FROM anime_config_overrides
WHERE managed_anime_id = $1`, result.ManagedAnime.Id).Scan(&totalOverrides, &nullOverrides); err != nil {
		t.Fatal(err)
	}
	// The six frozen sections plus tag_filters, each a complete one-time copy.
	if totalOverrides != len(animedomain.EffectiveSectionOrder)+1 {
		t.Fatalf("materialized section rows = %d, want %d", totalOverrides, len(animedomain.EffectiveSectionOrder)+1)
	}
	if nullOverrides != 0 {
		t.Fatalf("activated instance has %d sections without a value", nullOverrides)
	}

	before, err := animeconfig.LoadEffective(ctx, db, result.ManagedAnime.Id)
	if err != nil {
		t.Fatalf("read effective config before global change: %v", err)
	}
	if before.Selection.MaxRss != 3 {
		t.Fatalf("baseline subgroups max_rss = %d, want the materialized code default 3", before.Selection.MaxRss)
	}

	// Every per-anime section is one-time input: a later Global change never
	// reaches the instance.
	if _, err := db.ExecContext(ctx, `
INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('subgroups', 'subgroups-v1', '{"entries":[],"max_rss":99}', 1)`); err != nil {
		t.Fatal(err)
	}

	after, err := animeconfig.LoadEffective(ctx, db, result.ManagedAnime.Id)
	if err != nil {
		t.Fatalf("read effective config after global change: %v", err)
	}
	if after.Selection.MaxRss != before.Selection.MaxRss {
		t.Fatalf("global change reached locked instance: max_rss %d -> %d", before.Selection.MaxRss, after.Selection.MaxRss)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("global change altered locked instance effective config: %+v -> %+v", before, after)
	}
}

// TestSubscribeAnimeConfigCandidateWithoutRevisionFailsClosed proves a legacy
// candidate (no pointer, no fabricated revision) cannot be activated and that
// the refusal leaves no owner row behind: deriving a revision from current
// globals is forbidden, so the activation rolls back entirely.
func TestSubscribeAnimeConfigCandidateWithoutRevisionFailsClosed(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-legacy", "Legacy Draft"))
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision = NULL, current_revision_digest = NULL
WHERE id = $1`, candidate.Id); err != nil {
		t.Fatal(err)
	}
	before := animetest.ProductRowCounts(t, db)

	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-legacy-key", generated.CandidateSubscribeInput{Start: new(true)})
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CANDIDATE_REVISION_REQUIRED" {
		t.Fatalf("revision-less activation error = %v, want CANDIDATE_REVISION_REQUIRED", err)
	}
	for table, want := range before {
		if got := animetest.CountRows(t, db, table); got != want {
			t.Fatalf("revision-less activation wrote %s = %d, want %d", table, got, want)
		}
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.ActivatedManagedAnimeId != nil || current.Version != candidate.Version {
		t.Fatalf("revision-less activation mutated candidate: %+v", current)
	}
}

// TestSubscribeAnimeConfigCandidateIgnoresMutableDraftDivergence proves the
// execution identity is the saved revision, not the mutable candidate draft: a
// later edit of the candidate row (which the database accepts as a valid draft)
// must not change what an activation of the current revision builds.
func TestSubscribeAnimeConfigCandidateIgnoresMutableDraftDivergence(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-divergent", "Revision Title"))
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET draft = jsonb_set(draft, '{title}', '"Mutable Title"'),
    draft_digest = 'sha256:' || encode(
        digest(convert_to(anime_config_candidate_canonical_json(
            jsonb_set(draft, '{title}', '"Mutable Title"')
        ), 'UTF8'), 'sha256'),
        'hex'
    )
WHERE id = $1`, candidate.Id); err != nil {
		t.Fatal(err)
	}

	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-divergent-key", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}
	if result.ManagedAnime.Title != "Revision Title" {
		t.Fatalf("activation title = %q, want the saved revision draft title %q", result.ManagedAnime.Title, "Revision Title")
	}
}

// The write-in of a chosen metadata match for a custom/oneshot draft goes
// through this same candidate PATCH: it appends the candidate's next revision,
// and that revision is the one a one-shot creation references. The revision's
// metadata is what the naming render reads (title/year/kind), so a match that
// was never written cannot become a name, and an unmatched draft stays the
// fail-closed hold it was instead of gaining a fabricated default.
func TestPatchCustomCandidateWritesTheChosenMetadataMatch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	// The draft starts unmatched: no binding, so the metadata hold stays set.
	unmatched := customDraft(t, "Test Show")
	unmatched.Metadata = generated.AnimeMetadataDraft{}
	candidate := saveCandidate(t, service, unmatched)
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	first, err := service.GetAnimeConfigCandidateRevision(ctx, candidate.Id, *candidate.CurrentRevision)
	if err != nil {
		t.Fatalf("read first revision: %v", err)
	}
	if hold := animedomain.MetadataHoldFromBindings(first.Draft.Metadata.Bgm, first.Draft.Metadata.Tmdb); !hold.Held {
		t.Fatalf("unmatched draft hold = %+v, want a held draft", hold)
	}

	// A stale editor is refused with the typed conflict and appends nothing.
	staleDraft := customDraft(t, "Test Show")
	staleDraft.Metadata = generated.AnimeMetadataDraft{Bgm: &generated.NullableBGMBinding{WorkId: "bgm-stale"}}
	if _, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version+1, candidateInput(t, service.db, staleDraft)); err == nil {
		t.Fatal("a patch under a version the row does not have must fail")
	}
	var conflict *apperrors.VersionConflict
	if _, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version+1, candidateInput(t, service.db, staleDraft)); !errors.As(err, &conflict) {
		t.Fatalf("stale patch error = %v, want a typed VersionConflict", err)
	}
	if conflict.CurrentVersion != candidate.Version {
		t.Fatalf("conflict reports version %d, want %d", conflict.CurrentVersion, candidate.Version)
	}

	// The chosen match is written: a new revision whose metadata is the match.
	matched := customDraft(t, "Test Show")
	matched.Metadata = generated.AnimeMetadataDraft{
		Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-chosen"},
		Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-chosen", MediaKind: generated.TMDBMediaKindTv, Season: 2},
	}
	updated, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, candidateInput(t, service.db, matched))
	if err != nil {
		t.Fatalf("patch the chosen match: %v", err)
	}
	if updated.CurrentRevision == nil || *updated.CurrentRevision != first.Revision+1 {
		t.Fatalf("revision = %v, want the next one after %d", updated.CurrentRevision, first.Revision)
	}
	revision, err := service.GetAnimeConfigCandidateRevision(ctx, candidate.Id, *updated.CurrentRevision)
	if err != nil {
		t.Fatalf("read written revision: %v", err)
	}
	if revision.Draft.Metadata.Bgm == nil || revision.Draft.Metadata.Bgm.WorkId != "bgm-chosen" {
		t.Fatalf("written bgm = %+v, want the chosen subject", revision.Draft.Metadata.Bgm)
	}
	if revision.Draft.Metadata.Tmdb == nil || revision.Draft.Metadata.Tmdb.WorkId != "tmdb-chosen" {
		t.Fatalf("written tmdb = %+v, want the chosen work", revision.Draft.Metadata.Tmdb)
	}
	if hold := animedomain.MetadataHoldFromBindings(revision.Draft.Metadata.Bgm, revision.Draft.Metadata.Tmdb); hold.Held {
		t.Fatalf("matched revision hold = %+v, want no hold", hold)
	}
	// The match is the naming fact source: the same frozen media binding the
	// render resolves the tv branch and the season directory from.
	mediaKind, season, _ := animedomain.FrozenMediaBinding(revision.Draft.Metadata.Tmdb, revision.Draft.Metadata.MediaKindOverride)
	if mediaKind != string(animedomain.TMDBMediaKindTv) || season != 2 {
		t.Fatalf("naming media binding = %q/%d, want tv/2", mediaKind, season)
	}
	// The candidate's own custom locator identity is untouched by the match, so
	// the draft's frozen snapshot still belongs to this candidate.
	if len(revision.Draft.RssSources) != 1 || revision.Draft.RssSources[0].Locator != unmatched.RssSources[0].Locator {
		t.Fatalf("rss sources = %+v, want the draft's own untouched source", revision.Draft.RssSources)
	}
	// The stale attempt left no leaf behind: exactly the two revisions exist.
	var revisions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM anime_config_candidate_revisions WHERE candidate_id = $1`, candidate.Id).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("revisions = %d, want the saved and the written one only", revisions)
	}
}
