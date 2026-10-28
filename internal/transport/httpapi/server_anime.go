package httpapi

import (
	"errors"
	"net/http"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

// draftService resolves the candidate-draft boundary for the candidate and
// direct-subscribe handlers.
func (s *Server) draftService(w http.ResponseWriter, r *http.Request) DraftService {
	return s.deps.Draft
}

// ownershipService resolves the anime owner-mutation boundary for the owner
// command handlers. Candidate and direct-subscribe handlers keep using
// draftService, so the two owners cannot be silently swapped.
func (s *Server) ownershipService(w http.ResponseWriter, r *http.Request) OwnershipService {
	return s.deps.Ownership
}

func mapSubscriptionDeleteInput(value generated.SubscriptionDelete) ownershipapplication.SubscriptionDeleteInput {
	return ownershipapplication.SubscriptionDeleteInput{ExpectedVersion: int64(value.ExpectedVersion)}
}

// ListAnime returns the managed anime list for direct catalog navigation.
func (s *Server) ListAnime(w http.ResponseWriter, r *http.Request) {
	readmodel := s.deps.Readmodel
	if readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	items, err := readmodel.ListAnimeApplication(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped := mapAnimeListItems(items)
	s.proxyAnimeListCovers(r.Context(), mapped)
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// SubscribeAnime atomically creates the complete owner set from the draft.
func (s *Server) SubscribeAnime(w http.ResponseWriter, r *http.Request, params generated.SubscribeAnimeParams) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	var input generated.SubscribeAnimeInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := mapAnimeDraft(input.Draft)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := draftService.SubscribeAnimeApplication(r.Context(), draftapplication.SubscribeAnimeInput{Draft: draft, Start: input.Start != nil && *input.Start}, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapCandidateSubscribeResult(result))
}

// DuplicateAnimeSubscription copies one instance's saved configuration into a
// new pending subscription with the given label.
func (s *Server) DuplicateAnimeSubscription(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, params generated.DuplicateAnimeSubscriptionParams) {
	draftService := s.draftService(w, r)
	if draftService == nil {
		return
	}
	var input generated.SubscriptionDuplicateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := draftService.DuplicateAnimeSubscription(r.Context(), string(managedAnimeID), draftapplication.DuplicateSubscriptionInput{InstanceLabel: input.InstanceLabel}, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapCandidateSubscribeResult(result))
}

// GetAnimeWorkspace returns the owner-chain read model for one anime.
func (s *Server) GetAnimeWorkspace(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	readmodel := s.deps.Readmodel
	if readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	workspace, err := readmodel.GetAnimeWorkspaceApplication(r.Context(), string(managedAnimeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapAnimeWorkspace(workspace)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// ReevaluateAnimeReleases schedules current-rule evaluation for unsubmitted
// releases and leaves submitted download work untouched.
func (s *Server) ReevaluateAnimeReleases(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	result, err := ownership.ReevaluateAnimeReleasesApplication(r.Context(), managedAnimeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, mapReleaseReevaluationResult(result))
}

// PatchAnimeMetadata replaces the metadata owner with expected-version CAS.
func (s *Server) PatchAnimeMetadata(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.AnimeMetadataPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	metadata, err := ownership.PatchAnimeMetadataApplication(r.Context(), managedAnimeID, mapMetadataPatchInput(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	response, err := mapMetadataPatchResult(metadata)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, response)
}

// RefreshAnimeMetadata re-pulls the bound works bypassing the positive caches
// and answers the unchanged metadata owner view. The metadata itself is never
// edited here; typed failures (no binding, provider unreachable) surface as-is.
func (s *Server) RefreshAnimeMetadata(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	metadata, err := ownership.RefreshAnimeMetadataApplication(r.Context(), managedAnimeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	response, err := mapMetadataPatchResult(metadata)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, response)
}

// MatchAnimeMetadata runs the provider-backed one-click metadata action.
// The Idempotency-Key dedupes repeated clicks / retries at the service layer.
func (s *Server) MatchAnimeMetadata(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, params generated.MatchAnimeMetadataParams) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var input generated.MetadataMatchInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := ownership.MatchAnimeMetadata(r.Context(), managedAnimeID, mapMetadataMatchInput(input), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	response, err := mapMetadataMatchResult(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, response)
}

// PatchAnimeSubgroups replaces the complete subgroups owner value with CAS.
func (s *Server) PatchAnimeSubgroups(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.SubgroupsPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := ownership.PatchAnimeSubgroupsApplication(r.Context(), managedAnimeID, mapSubgroupsPatchInput(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSubgroupsOwnerResult(result))
}

// DiscoverAnimeSubgroups runs the provider-backed one-click group discovery action.
func (s *Server) DiscoverAnimeSubgroups(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var input generated.DiscoverSubgroupsInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := ownership.DiscoverAnimeSubgroups(r.Context(), string(managedAnimeID), mapDiscoverSubgroupsInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapDiscoverSubgroupsResult(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// PatchAnimeRules replaces the rules owner with expected-version CAS.
func (s *Server) PatchAnimeRules(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.RulesPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	applicationPatch, err := mapRulesPatchInput(patch)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := ownership.PatchAnimeRules(r.Context(), string(managedAnimeID), applicationPatch)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapRulesPatchResult(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// PatchAnimeSubscription saves the unique subscription state with CAS.
func (s *Server) PatchAnimeSubscription(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.SubscriptionPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	subscription, err := ownership.PatchAnimeSubscription(r.Context(), string(managedAnimeID), mapSubscriptionPatch(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSubscription(subscription))
}

// SetAnimeNewReleasesOnly saves the new-releases-only option of a pending
// subscription with CAS; a started one answers NEW_RELEASES_ONLY_LOCKED.
func (s *Server) SetAnimeNewReleasesOnly(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var input generated.NewReleasesOnlyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	subscription, err := ownership.SetNewReleasesOnly(r.Context(), string(managedAnimeID), ownershipapplication.NewReleasesOnlyInput{
		ExpectedVersion: int64(input.ExpectedVersion),
		Enabled:         input.Enabled,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSubscription(subscription))
}

// DeleteAnimeSubscription is the reviewed compatibility command for irreversible
// ManagedAnime retirement. It preserves the local graph and advances its fence.
func (s *Server) DeleteAnimeSubscription(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var input generated.SubscriptionDelete
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := ownership.DeleteAnimeSubscription(r.Context(), string(managedAnimeID), mapSubscriptionDeleteInput(input)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CleanupAnimeLocalData is the explicit, separately authorized physical local
// cleanup of one retired ManagedAnime's local records. It never touches qB
// torrents/data or media-library files.
func (s *Server) CleanupAnimeLocalData(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, params generated.CleanupAnimeLocalDataParams) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var input generated.LocalDataCleanup
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := ownership.CleanupAnimeLocalData(r.Context(), string(managedAnimeID), mapLocalDataCleanupInput(input), string(params.IdempotencyKey)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PatchRSSSourceOffset replaces the RSS link episode offset with CAS.
func (s *Server) PatchRSSSourceOffset(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, rssSourceID generated.RSSSourceID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.RSSSourceOffsetPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	source, err := ownership.PatchRSSSourceOffset(r.Context(), managedAnimeID, rssSourceID, mapRSSSourceOffsetPatch(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapRSSSource(source))
}

// SetProviderGroupOffset sets a listed provider group's episode offset by group
// identity; expected_version 0 creates the group's RSS source.
func (s *Server) SetProviderGroupOffset(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, provider generated.SetProviderGroupOffsetParamsProvider, providerGroupRef string) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var body generated.ProviderGroupOffsetPut
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	source, err := ownership.SetProviderGroupOffset(r.Context(), managedAnimeID, string(provider), providerGroupRef, ownershipapplication.ProviderGroupOffsetInput{
		ExpectedVersion: body.ExpectedVersion,
		EpisodeOffset:   body.EpisodeOffset,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapRSSSource(source))
}

func mapRSSSourceOffsetPatch(value generated.RSSSourceOffsetPatch) ownershipapplication.RSSSourceOffsetPatchInput {
	return ownershipapplication.RSSSourceOffsetPatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		EpisodeOffset:   value.EpisodeOffset,
	}
}

func mapRSSSourceAniBTModePatch(value generated.RSSSourceAniBTModePatch) ownershipapplication.RSSSourceAniBTModePatchInput {
	var mode *animedomain.RSSSourceDraftAnibtAuxMode
	if value.AnibtAuxMode != nil {
		mapped := animedomain.RSSSourceDraftAnibtAuxMode(*value.AnibtAuxMode)
		mode = &mapped
	}
	return ownershipapplication.RSSSourceAniBTModePatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		AnibtAuxMode:    mode,
	}
}

func mapRSSSource(value animeapplication.RSSSource) generated.RSSSource {
	result := generated.RSSSource{
		AnibtAuxMode:          mapRSSSourceAniBTMode(value.AnibtAuxMode),
		GroupName:             value.GroupName,
		HttpFeedConnectionRef: value.HttpFeedConnectionRef,
		Id:                    generated.ID(value.ID),
		LocatorDisplay:        value.LocatorDisplay,
		NetworkRouteRef:       value.NetworkRouteRef,
		EpisodeOffset:         value.EpisodeOffset,
		ProviderProvenance:    mapProviderProvenance(value.ProviderProvenance),
		SourceKind:            generated.SourceKind(value.SourceKind),
		SubscriptionId:        generated.ID(value.SubscriptionID),
		Title:                 value.Title,
		Version:               generated.Version(value.Version),
	}
	return result
}

func mapRSSSourceAniBTMode(value *animedomain.RSSSourceDraftAnibtAuxMode) *generated.RSSSourceAnibtAuxMode {
	if value == nil {
		return nil
	}
	mapped := generated.RSSSourceAnibtAuxMode(*value)
	return &mapped
}

func mapProviderProvenance(value *animedomain.ProviderProvenance) *generated.ProviderProvenance {
	if value == nil {
		return nil
	}
	return &generated.ProviderProvenance{
		DiscoverySnapshotVersion: value.DiscoverySnapshotVersion,
		Provider:                 generated.ProviderKind(value.Provider),
		ProviderEntryRef:         value.ProviderEntryRef,
		ProviderGroupRef:         value.ProviderGroupRef,
	}
}

func mapRSSRefreshAccepted(result ownershipapplication.RSSRefreshAccepted) generated.RSSRefreshAccepted {
	return generated.RSSRefreshAccepted{
		Accepted:   generated.RSSRefreshAcceptedAccepted(result.Accepted),
		WorkItemId: generated.ID(result.WorkItemID),
	}
}

// RefreshRSSSource schedules one manual rss_refresh WorkItem.
func (s *Server) RefreshRSSSource(w http.ResponseWriter, r *http.Request, rssSourceID generated.RSSSourceID, params generated.RefreshRSSSourceParams) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	result, err := ownership.RefreshRSSSource(r.Context(), rssSourceID, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, mapRSSRefreshAccepted(result))
}

// PatchRSSSourceAniBTMode replaces the per-source AniBT auxiliary merge mode
// with CAS (AniBT provider sources only).
func (s *Server) PatchRSSSourceAniBTMode(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, rssSourceID generated.RSSSourceID) {
	ownership := s.ownershipService(w, r)
	if ownership == nil {
		return
	}
	var patch generated.RSSSourceAniBTModePatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	source, err := ownership.PatchRSSSourceAniBTMode(r.Context(), managedAnimeID, rssSourceID, mapRSSSourceAniBTModePatch(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapRSSSource(source))
}
