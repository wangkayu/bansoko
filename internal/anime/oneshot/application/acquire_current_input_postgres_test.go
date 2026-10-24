package application

import (
	"context"
	"errors"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// A finite one-shot admission may capture the request's
// complete unsaved draft (input_source=current_input) without any saved
// candidate row. The captured input — the draft, the resolved effective
// sections and the global parser-policy default read inside the admission
// transaction — is the execution identity; the replay identity is
// request-derived (idempotency key + canonical draft digest + selected seed)
// so a replay never re-resolves against changed globals.

func currentInputForSeed(t *testing.T, service *oneShotTestServices, seed SelectedSeed, mutate func(generated.AnimeDraft) generated.AnimeDraft) (animedomain.AnimeDraft, AcquireSeedInput) {
	t.Helper()
	draft := oneShotProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, seed.ProviderEntryRef, seed.ProviderGroupRef)
	if mutate != nil {
		draft = mutate(draft)
	}
	domainValue := candidateInput(t, service.Service.db, draft).Draft
	input := AcquireSeedInput{
		InputSource: AcquireSeedInputSourceCurrentInput,
		Draft:       &domainValue,
		Seed:        seed,
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	return domainValue, input
}

func TestAcquireSeedOnceCurrentInputAdmitsWithoutCandidate(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-current-input-0001")
	if err != nil {
		t.Fatalf("AcquireSeedOnce current_input: %v", err)
	}
	if result.State != string(OneShotAcquisitionStateRunning) {
		t.Fatalf("state = %q, want running", result.State)
	}
	if result.DownloadTaskID == nil {
		t.Fatal("current_input admission admitted no download task")
	}

	// The captured input snapshot records the draft provenance, not a candidate.
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil {
		t.Fatalf("read one-shot view: %v", err)
	}
	snap := view.InputSnapshot
	if snap == nil {
		t.Fatal("admission stored no input snapshot")
	}
	if snap.InputSource != AcquireSeedInputSourceCurrentInput {
		t.Fatalf("input_source = %q, want current_input", snap.InputSource)
	}
	if snap.CandidateID != "" || snap.Revision != 0 {
		t.Fatalf("current_input snapshot carries candidate %q/%d", snap.CandidateID, snap.Revision)
	}
	if snap.DraftDigest == "" || len(snap.Draft) == 0 {
		t.Fatal("current_input snapshot lost the captured draft")
	}
	if snap.ConfigIdentity.IdentityKind != "current_input" || snap.ConfigIdentity.EffectiveDigest == "" {
		t.Fatalf("config identity = %+v, want current_input with an effective digest", snap.ConfigIdentity)
	}
	// The created instance parser policy mirrors the global creation default the
	// captured input resolved with.
	var kind string
	if err := db.QueryRowContext(ctx, `
SELECT parser_kind FROM anime_business_configs WHERE managed_anime_id = $1`,
		view.ManagedAnimeID).Scan(&kind); err != nil {
		t.Fatalf("read instance parser policy: %v", err)
	}
	var wantKind string
	if err := db.QueryRowContext(ctx, `
SELECT value->>'parser_kind' FROM global_config_sections WHERE section_name = 'parser_policy'`).Scan(&wantKind); err != nil {
		t.Fatalf("read global parser policy default: %v", err)
	}
	if kind != wantKind {
		t.Fatalf("instance parser policy kind = %s, want the global default %s", kind, wantKind)
	}
}

func TestAcquireSeedOnceCurrentInputReplaysOriginalHandle(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)
	const key = "one-shot-current-input-replay-1"

	first, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}

	// The exact same request under the same key replays the original handle,
	// even though the torrent fetch and owner writes would otherwise run again.
	second, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatalf("replay admission: %v", err)
	}
	if second.OneShotID != first.OneShotID {
		t.Fatalf("replay returned one_shot_id %q, want the original %q", second.OneShotID, first.OneShotID)
	}
	if animetest.CountRows(t, db, "one_shot_acquisitions") != 1 {
		t.Fatalf("one-shot markers = %d, want 1", animetest.CountRows(t, db, "one_shot_acquisitions"))
	}
}

func TestAcquireSeedOnceCurrentInputDifferentDraftConflicts(t *testing.T) {
	service, _, _, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, first := currentInputForSeed(t, service, seed, nil)
	first.Seed = bindSeedIdentity(seed, snapshot)
	const key = "one-shot-current-input-conflict-1"
	if _, err := service.AcquireSeedOnce(ctx, first, key); err != nil {
		t.Fatalf("first admission: %v", err)
	}

	// A different captured draft under the same key is a typed conflict.
	_, second := currentInputForSeed(t, service, seed, func(draft generated.AnimeDraft) generated.AnimeDraft {
		draft.Title = "Renamed Show"
		return draft
	})
	second.Seed = bindSeedIdentity(seed, snapshot)
	_, err := service.AcquireSeedOnce(ctx, second, key)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("error = %v, want IDEMPOTENCY_KEY_REUSED", err)
	}
}

func TestAcquireSeedOnceCurrentInputMixedVariantsRejected(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	domainDraftValue, _ := currentInputForSeed(t, service, seed, nil)

	// current_input + candidate fields is a structural 400 with zero writes.
	mixed := AcquireSeedInput{
		InputSource: AcquireSeedInputSourceCurrentInput,
		Draft:       &domainDraftValue,
		CandidateID: "candidate-not-required",
		Revision:    1,
		Seed:        bindSeedIdentity(seed, snapshot),
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	_, err := service.AcquireSeedOnce(ctx, mixed, "one-shot-current-input-mixed-1")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 400 || business.Code != "INPUT_SOURCE_INVALID" {
		t.Fatalf("error = %v, want 400 INPUT_SOURCE_INVALID", err)
	}

	// A draft carried by a legacy (undiscriminated) body is the same mixing.
	legacy := AcquireSeedInput{
		Draft:  &domainDraftValue,
		Seed:   bindSeedIdentity(seed, snapshot),
		Caller: AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	_, err = service.AcquireSeedOnce(ctx, legacy, "one-shot-current-input-mixed-2")
	if !errors.As(err, &business) || business.Status != 400 || business.Code != "INPUT_SOURCE_INVALID" {
		t.Fatalf("error = %v, want 400 INPUT_SOURCE_INVALID", err)
	}

	if animetest.CountRows(t, db, "one_shot_acquisitions") != 0 || animetest.CountRows(t, db, "download_tasks") != 0 {
		t.Fatal("a mixed variant admission wrote owner rows")
	}
}

// A one-shot names its seed directly: a seed from a group the captured draft
// does not select is admitted (the snapshot proves it exists).
func TestAcquireSeedOnceCurrentInputAdmitsUnselectedGroupSeed(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	// The captured draft selects group 999; the seed belongs to group 678.
	draft := oneShotProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, seed.ProviderEntryRef, "999")
	domainValue := candidateInput(t, db, draft).Draft
	input := AcquireSeedInput{
		InputSource: AcquireSeedInputSourceCurrentInput,
		Draft:       &domainValue,
		Seed:        bindSeedIdentity(seed, snapshot),
		Caller:      AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-current-input-unselected-1")
	if err != nil {
		t.Fatalf("unselected-group current_input admission: %v", err)
	}
	if result.DownloadTaskID == nil || animetest.CountRows(t, db, "one_shot_acquisitions") != 1 {
		t.Fatal("an unselected-group current_input admission created no one-shot")
	}
}
