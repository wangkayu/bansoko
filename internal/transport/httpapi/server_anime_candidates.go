package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	animetransport "bansoko/internal/anime/transport"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// AnimeConfigCandidate endpoints expose saved planning drafts. Every handler
// here except Subscribe is free of runtime effects: no owner, no work item and
// no provider call. Activation is the only transition that creates owners, and
// it delegates to the same Subscribe transaction as a direct subscribe.

// ListAnimeConfigCandidates returns saved drafts, optionally filtered to one
// provider entry.
func (s *Server) ListAnimeConfigCandidates(w http.ResponseWriter, r *http.Request, params generated.ListAnimeConfigCandidatesParams) {
	defer deferHandlerTiming(r, "list_anime_config_candidates")()
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	candidates, err := draftService.ListAnimeConfigCandidates(r.Context(), mapCandidateListInput(params))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped := make([]generated.AnimeConfigCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mapCandidate, err := mapAnimeConfigCandidate(candidate)
		if err != nil {
			writeError(w, r, err)
			return
		}
		mapped = append(mapped, mapCandidate)
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.AnimeConfigCandidateList{Candidates: mapped})
}

// GetAnimeConfigCandidate reads one saved draft.
func (s *Server) GetAnimeConfigCandidate(w http.ResponseWriter, r *http.Request, candidateID generated.CandidateID) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	candidate, err := draftService.GetAnimeConfigCandidate(r.Context(), candidateID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapCandidate, err := mapAnimeConfigCandidate(candidate)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCandidate)
}

// SaveAnimeConfigCandidate creates a NEW planning candidate (pure insert).
// Updating an existing draft goes through PATCH + If-Match.
func (s *Server) SaveAnimeConfigCandidate(w http.ResponseWriter, r *http.Request) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	var input generated.SaveAnimeConfigCandidateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	applicationInput, err := mapCandidateDraftInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	candidate, err := draftService.SaveAnimeConfigCandidate(r.Context(), applicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapCandidate, err := mapAnimeConfigCandidate(candidate)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapCandidate)
}

// PatchAnimeConfigCandidate replaces a draft under expected-version CAS.
func (s *Server) PatchAnimeConfigCandidate(w http.ResponseWriter, r *http.Request, candidateID generated.CandidateID, params generated.PatchAnimeConfigCandidateParams) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	var input generated.PatchAnimeConfigCandidateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	applicationInput, err := mapCandidatePatchInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	candidate, err := draftService.PatchAnimeConfigCandidate(r.Context(), candidateID, int64(params.IfMatch), applicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapCandidate, err := mapAnimeConfigCandidate(candidate)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCandidate)
}

// DeleteAnimeConfigCandidate is an idempotent explicit local cleanup command.
// Candidates linked to active ManagedAnime owners are refused with 409.
func (s *Server) DeleteAnimeConfigCandidate(w http.ResponseWriter, r *http.Request, candidateID generated.CandidateID) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	if err := draftService.DeleteAnimeConfigCandidate(r.Context(), candidateID); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SubscribeAnimeConfigCandidate activates one exact saved revision: the owner
// set and the candidate's activation link are committed in one transaction.
// If-Match must carry the candidate version the caller read; the activation
// identity also includes the required revision and effective digest.
func (s *Server) SubscribeAnimeConfigCandidate(w http.ResponseWriter, r *http.Request, candidateID generated.CandidateID, params generated.SubscribeAnimeConfigCandidateParams) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	var input generated.CandidateSubscribeInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	applicationInput := draftapplication.CandidateSubscribeInput{Revision: input.Revision, Start: input.Start != nil && *input.Start}
	if input.InitialState != nil {
		state := subscriptiondomain.SubscriptionState(*input.InitialState)
		applicationInput.InitialState = &state
	}
	result, err := draftService.ActivateAnimeConfigCandidate(r.Context(), candidateID, int64(params.IfMatch), string(params.IdempotencyKey), applicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapCandidateSubscribeResult(result))
}

func mapCandidateSubscribeResult(value draftapplication.SubscribeResult) generated.SubscribeResult {
	result := generated.SubscribeResult{
		EffectiveConfigVersions: generated.EffectiveConfigVersions{
			FileEffect: generated.Version(value.EffectiveConfigVersions.FileEffect), FileFilter: generated.Version(value.EffectiveConfigVersions.FileFilter), Naming: generated.Version(value.EffectiveConfigVersions.Naming), Parser: generated.Version(value.EffectiveConfigVersions.Parser), ReleaseFilter: generated.Version(value.EffectiveConfigVersions.ReleaseFilter), Subgroups: generated.Version(value.EffectiveConfigVersions.Subgroups),
		},
		ManagedAnime: generated.ManagedAnime{Id: generated.ID(value.ManagedAnime.ID), Title: value.ManagedAnime.Title, TitleOverride: copyCandidateString(value.ManagedAnime.TitleOverride), Version: generated.Version(value.ManagedAnime.Version)},
		Metadata:     generated.AnimeMetadata{ManagedAnimeId: generated.ID(value.Metadata.ManagedAnimeID), Bgm: mapCandidateBGM(value.Metadata.BGM), Tmdb: mapCandidateTMDB(value.Metadata.TMDB), TitleOverride: copyCandidateString(value.Metadata.TitleOverride), TotalEpisodes: copyCandidateInt(value.Metadata.TotalEpisodes), MediaKindOverride: (*generated.AnimeMetadataMediaKindOverride)(copyCandidateString(value.Metadata.MediaKindOverride)), Year: copyCandidateInt(value.Metadata.Year), Season: copyCandidateInt(value.Metadata.Season), Version: generated.Version(value.Metadata.Version)},
		Subscription: generated.Subscription{Id: generated.ID(value.Subscription.ID), ManagedAnimeId: generated.ID(value.Subscription.ManagedAnimeID), Origin: generated.SubscriptionOrigin(value.Subscription.Origin), OriginEntryRef: copyCandidateString(value.Subscription.OriginEntryRef), InstanceLabel: copyCandidateString(value.Subscription.InstanceLabel), State: generated.SubscriptionState(value.Subscription.State), LifecycleState: generated.SubscriptionLifecycleState(value.Subscription.LifecycleState), LifecycleEpoch: value.Subscription.LifecycleEpoch, RetiredAt: value.Subscription.RetiredAt, Version: generated.Version(value.Subscription.Version), NewReleasesOnly: value.Subscription.NewReleasesOnly, StartBaselineAt: value.Subscription.StartBaselineAt},
	}
	if value.RssSources != nil {
		result.RssSources = make([]generated.RSSSource, len(value.RssSources))
	}
	for i, source := range value.RssSources {
		mapped := generated.RSSSource{GroupName: source.GroupName, HttpFeedConnectionRef: copyCandidateString(source.HttpFeedConnectionRef), Id: generated.ID(source.ID), LocatorDisplay: source.LocatorDisplay, NetworkRouteRef: copyCandidateString(source.NetworkRouteRef), EpisodeOffset: source.EpisodeOffset, SourceKind: generated.SourceKind(source.SourceKind), SubscriptionId: generated.ID(source.SubscriptionID), Title: source.Title, Version: generated.Version(source.Version)}
		if source.AnibtAuxMode != nil {
			mode := generated.RSSSourceAnibtAuxMode(*source.AnibtAuxMode)
			mapped.AnibtAuxMode = &mode
		}
		if source.ProviderProvenance != nil {
			mapped.ProviderProvenance = &generated.ProviderProvenance{DiscoverySnapshotVersion: source.ProviderProvenance.DiscoverySnapshotVersion, Provider: generated.ProviderKind(source.ProviderProvenance.Provider), ProviderEntryRef: source.ProviderProvenance.ProviderEntryRef, ProviderGroupRef: source.ProviderProvenance.ProviderGroupRef}
		}
		result.RssSources[i] = mapped
	}
	return result
}

func mapCandidateBGM(value *animeapplication.BGMBinding) *generated.NullableBGMBinding {
	if value == nil {
		return nil
	}
	return &generated.NullableBGMBinding{WorkId: value.WorkID}
}

func mapCandidateTMDB(value *animeapplication.TMDBBinding) *generated.NullableTMDBBinding {
	if value == nil {
		return nil
	}
	return &generated.NullableTMDBBinding{WorkId: value.WorkID, MediaKind: generated.TMDBMediaKind(value.MediaKind), Season: value.Season}
}

func copyCandidateString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyCandidateInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func mapCandidateListInput(input generated.ListAnimeConfigCandidatesParams) draftapplication.ListAnimeConfigCandidatesInput {
	var origin *animedomain.SubscriptionOrigin
	if input.Origin != nil {
		mapped := animedomain.SubscriptionOrigin(*input.Origin)
		origin = &mapped
	}
	return draftapplication.ListAnimeConfigCandidatesInput{Origin: origin, OriginEntryRef: input.OriginEntryRef}
}

func mapCandidateDraftInput(input generated.SaveAnimeConfigCandidateInput) (draftapplication.CandidateDraftInput, error) {
	draft, err := animetransport.Draft(input.Draft)
	if err != nil {
		return draftapplication.CandidateDraftInput{}, err
	}
	return draftapplication.CandidateDraftInput{Draft: draft}, nil
}

func mapCandidatePatchInput(input generated.PatchAnimeConfigCandidateInput) (draftapplication.CandidateDraftInput, error) {
	draft, err := animetransport.Draft(input.Draft)
	if err != nil {
		return draftapplication.CandidateDraftInput{}, err
	}
	return draftapplication.CandidateDraftInput{Draft: draft}, nil
}

func mapCandidateCleanupReason(reason *string) *generated.AnimeConfigCandidateCleanupReason {
	if reason == nil {
		return nil
	}
	mapped := generated.AnimeConfigCandidateCleanupReason(*reason)
	return &mapped
}

func copyCandidateTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// candidatePosterURL is the poster proxy path for the draft's own Bangumi
// binding, so a custom preset keeps its poster without a linked subscription.
func candidatePosterURL(bgm *animedomain.BGMBinding) *string {
	if bgm == nil {
		return nil
	}
	workID := strings.TrimSpace(bgm.WorkId)
	if workID == "" {
		return nil
	}
	path := imagePosterPathPrefix + workID
	return &path
}

func mapAnimeConfigCandidate(candidate draftapplication.AnimeConfigCandidate) (generated.AnimeConfigCandidate, error) {
	draftJSON, err := json.Marshal(candidate.Draft)
	if err != nil {
		return generated.AnimeConfigCandidate{}, fmt.Errorf("encode candidate draft: %w", err)
	}
	var draft generated.AnimeDraft
	if err := json.Unmarshal(draftJSON, &draft); err != nil {
		return generated.AnimeConfigCandidate{}, fmt.Errorf("map candidate draft: %w", err)
	}
	return generated.AnimeConfigCandidate{
		ActivatedManagedAnimeId: copyCandidateString(candidate.ActivatedManagedAnimeId),
		CleanupEligibleAt:       copyCandidateTime(candidate.CleanupEligibleAt),
		CleanupReason:           mapCandidateCleanupReason(candidate.CleanupReason),
		CreatedAt:               candidate.CreatedAt,
		CurrentRevision:         candidate.CurrentRevision,
		CurrentRevisionDigest:   copyCandidateString(candidate.CurrentRevisionDigest),
		Draft:                   draft,
		Id:                      generated.ID(candidate.Id),
		InstanceLabel:           copyCandidateString(candidate.InstanceLabel),
		Origin:                  generated.SubscriptionOrigin(candidate.Origin),
		OriginEntryRef:          copyCandidateString(candidate.OriginEntryRef),
		PosterUrl:               candidatePosterURL(candidate.Draft.Metadata.Bgm),
		RetentionUntil:          candidate.RetentionUntil,
		Status:                  generated.AnimeConfigCandidateStatus(candidate.Status),
		Title:                   candidate.Title,
		TitleOverride:           copyCandidateString(candidate.TitleOverride),
		UpdatedAt:               candidate.UpdatedAt,
		Version:                 generated.Version(candidate.Version),
		DraftDigest:             candidate.DraftDigest,
		LastActivity:            candidate.LastActivity,
	}, nil
}
