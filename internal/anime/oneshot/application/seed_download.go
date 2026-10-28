package application

import (
	"context"
	"errors"
	"strings"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// SnapshotSeedDownloadInput names one persisted seed to download. It carries
// identities only: the locator, title, published time and strong identity are
// read from the exact snapshot server-side, so a browser never has to echo back
// (or be trusted with) the raw source location.
type SnapshotSeedDownloadInput struct {
	Provider         string
	ProviderEntryRef string
	ProviderGroupRef string
	SnapshotID       string
	SnapshotVersion  int64
	ContentHash      string
	StableSeedKey    string
	// Configuration identity: exactly one of the two input sources.
	InputSource string
	Draft       *animedomain.AnimeDraft
	CandidateID string
	Revision    int64
	// EpisodeOffset is the one-shot request's own episode offset (0 when the
	// caller omits it); see AcquireSeedInput.EpisodeOffset.
	EpisodeOffset int
	// FileSelection is the manual file selection (nil = the default files);
	// see AcquireSeedInput.FileSelection.
	FileSelection []string
}

// DownloadSnapshotSeed admits one finite one-shot download for one persisted
// seed. The seed's facts come from the exact snapshot the caller named, and the
// torrent bytes come from that same persisted blob (never a fresh provider
// request), so the content the user reviewed is the content the downloader
// receives. Every identity problem fails closed with zero owner writes.
func (s *Service) DownloadSnapshotSeed(ctx context.Context, input SnapshotSeedDownloadInput, idempotencyKey string) (OneShotAdmissionResult, error) {
	if err := validateOneShotEpisodeOffset(input.EpisodeOffset); err != nil {
		return OneShotAdmissionResult{}, err
	}
	if s.seedPreviews == nil {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "The persisted provider seed source is not configured."}
	}
	if strings.TrimSpace(input.StableSeedKey) == "" {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "SEED_IDENTITY_REQUIRED", Message: "The seed identity is required."}
	}
	identity := animecatalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: input.SnapshotID, SnapshotVersion: input.SnapshotVersion, ContentHash: input.ContentHash,
	}
	// The kind is derived from the refs the caller named and is only a routing
	// hint: the source proves the persisted row's own kind, so a forged ref pair
	// cannot reach another kind's snapshot.
	kind := catalogapplication.SnapshotKindFromRefs(input.ProviderEntryRef, input.ProviderGroupRef)
	var snapshot animeapplication.SeedPreviewSnapshot
	var err error
	if kind == animecatalogdomain.FeedSnapshotKindSearch {
		// A keyword search snapshot is read through the search read; a source
		// without it fails closed instead of falling back to a feed snapshot.
		searchSource, ok := s.seedPreviews.(animeapplication.SearchSeedPreviewSource)
		if !ok {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "Keyword search snapshots are not served by this server."}
		}
		snapshot, err = searchSource.FetchSearchSnapshotByID(ctx, input.Provider, input.ProviderEntryRef, input.ProviderGroupRef, identity)
	} else {
		snapshot, err = s.seedPreviews.FetchEntryGroupSnapshotByID(ctx, input.Provider, input.ProviderEntryRef, input.ProviderGroupRef, identity)
	}
	if err != nil {
		if errors.Is(err, animeapplication.ErrProviderFeedSnapshotRefreshRequired) {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: animeapplication.SafeReason(err.Error())}
		}
		return OneShotAdmissionResult{}, err
	}
	if snapshot.Freshness != "fresh" {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "预览快照已过期或不是最新，请先刷新种子再发起下载。"}
	}
	var fact *animeapplication.SeedPreviewFact
	for index := range snapshot.Seeds {
		if snapshot.Seeds[index].StableSeedKey == input.StableSeedKey {
			fact = &snapshot.Seeds[index]
			break
		}
	}
	if fact == nil {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子已不在当前快照中，请刷新种子后重新选择。"}
	}

	// The same builder resolves an acquireSeedOnce snapshot reference, so both
	// identity-only entry points freeze the identical locator-form seed.
	seed := seedFromSnapshotFact(SelectedSeed{
		Provider:            animedomain.ProviderKind(input.Provider),
		ProviderEntryRef:    input.ProviderEntryRef,
		ProviderGroupRef:    input.ProviderGroupRef,
		SnapshotID:          input.SnapshotID,
		SnapshotVersion:     input.SnapshotVersion,
		SnapshotContentHash: input.ContentHash,
		StableSeedKey:       input.StableSeedKey,
		SnapshotKind:        string(kind),
	}, *fact)

	admission := AcquireSeedInput{
		Seed:          seed,
		EpisodeOffset: input.EpisodeOffset,
		FileSelection: input.FileSelection,
		Caller:        AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	switch input.InputSource {
	case "current_input":
		if input.Draft == nil {
			return OneShotAdmissionResult{}, inputSourceInvalidError("input_source=current_input requires a draft.")
		}
		admission.InputSource = AcquireSeedInputSourceCurrentInput
		admission.Draft = input.Draft
	case "candidate_revision":
		if strings.TrimSpace(input.CandidateID) == "" || input.Revision < 1 {
			return OneShotAdmissionResult{}, inputSourceInvalidError("input_source=candidate_revision requires candidate_id and revision.")
		}
		admission.CandidateID = input.CandidateID
		admission.Revision = input.Revision
	case "":
		admission.CandidateID = input.CandidateID
		admission.Revision = input.Revision
	default:
		return OneShotAdmissionResult{}, inputSourceInvalidError("input_source must be current_input or candidate_revision.")
	}
	return s.AcquireSeedOnce(ctx, admission, idempotencyKey)
}
