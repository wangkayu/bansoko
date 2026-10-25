package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// SeedSearchService is the keyword torrent search boundary consumed by the
// transport. It is implemented by catalogapplication.SeedSearchApplication, so
// the snapshot a fetch persists is the snapshot an expand/download resolves.
type SeedSearchService interface {
	Fetch(ctx context.Context, input catalogapplication.FetchSearchSnapshotInput) (catalogapplication.SearchSeedPreview, error)
	ReadWindow(ctx context.Context, provider, scopeRef, queryRef, snapshotID, contentHash string, version int64, limit int, cursor string) (catalogapplication.SearchSeedPreview, error)
}

// CatalogService is the provider-scoped catalog application boundary consumed
// by the transport. It is implemented by internal/catalog/application.
type CatalogService interface {
	WorkspaceProvider(ctx context.Context, provider, season string) (catalogdomain.Workspace, error)
	SearchProvider(ctx context.Context, provider, query string) (catalogdomain.ReadResult, error)
	RefreshProviderSeason(ctx context.Context, provider, season string) (catalogdomain.Snapshot, error)
	ReadProviderPreference(ctx context.Context, provider, season string) (catalogdomain.Preference, error)
	SetProviderPreference(ctx context.Context, provider string, preference catalogdomain.Preference, expectedVersion int64) (catalogdomain.Preference, error)
	ListSuppressions(ctx context.Context) ([]catalogdomain.Suppression, error)
	SuppressEntry(ctx context.Context, provider, providerEntryRef, titleSnapshot string) (catalogdomain.Suppression, error)
	UnsuppressEntry(ctx context.Context, provider, providerEntryRef string) error
	DiscoverProviderEntryGroups(ctx context.Context, provider, catalogEntryRef string) (catalogdomain.ProviderGroupDiscoveryResult, error)
	ReadProviderEntryRSS(ctx context.Context, provider, catalogEntryRef, groupRef string) (catalogdomain.ProviderRSSPreviewResult, error)
	PreviewProviderEntryRSS(ctx context.Context, provider, catalogEntryRef, groupRef string) (catalogdomain.ProviderRSSPreviewResult, error)
	ListGroupDictionary(ctx context.Context, listing catalogapplication.GroupDictionaryListing) (catalogapplication.GroupDictionaryPage, error)
	RetrieveGroupDictionary(ctx context.Context, provider, query string) ([]catalogdomain.GroupDictionaryEntry, error)
	DeleteGroupDictionaryEntry(ctx context.Context, provider, groupRef string) error
	// EnsureSeedTorrent expands one seed of one exact snapshot on demand: it
	// verifies the snapshot identity, resolves the locator server-side and
	// answers from the locator cache or fetches, verifies and caches the bytes
	// once. The outcome is a state, not an error.
	EnsureSeedTorrent(ctx context.Context, input catalogapplication.EnsureSeedTorrentInput) (catalogapplication.SeedTorrentFact, error)
}

// ReadCatalogWorkspace returns the provider-scoped catalog workspace for one
// season: the latest snapshot plus the projected item list.
func (s *Server) ReadCatalogWorkspace(w http.ResponseWriter, r *http.Request, provider generated.ReadCatalogWorkspaceParamsProvider, season generated.CatalogSeason) {
	catalog := s.catalogService()
	workspace, err := catalog.WorkspaceProvider(r.Context(), string(provider), string(season))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped := mapCatalogWorkspace(workspace)
	s.proxyCatalogCovers(r.Context(), mapped.Items)
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// SearchProviderCatalog runs a side-effect-free provider keyword search and
// projects results onto the catalog item shape. Providers without a public
// search surface (Mikan) return a typed 422 SEARCH_NOT_SUPPORTED.
func (s *Server) SearchProviderCatalog(w http.ResponseWriter, r *http.Request, provider generated.SearchProviderCatalogParamsProvider, params generated.SearchProviderCatalogParams) {
	catalog := s.catalogService()
	// The generated wrapper binds the path parameter but does not enforce the
	// OpenAPI CatalogProvider enum [anibt, mikan], so validate it explicitly
	// here like server_metadata.go does; the endpoint declares only the
	// Unprocessable error response in api/openapi.yaml, so client mistakes
	// must surface there instead of falling through to a generic 500 INTERNAL.
	switch provider {
	case generated.SearchProviderCatalogParamsProviderAnibt, generated.SearchProviderCatalogParamsProviderMikan:
	default:
		writeError(w, r, unprocessable("SEARCH_PROVIDER_UNSUPPORTED", "The catalog search provider is unknown.", []generated.ErrorDetail{{
			Code:    "PROVIDER_UNSUPPORTED",
			Message: "provider must be one of: anibt, mikan.",
		}}))
		return
	}
	result, err := catalog.SearchProvider(r.Context(), string(provider), params.Q)
	if err != nil {
		if errors.Is(err, catalogapplication.ErrSearchNotSupported) {
			writeError(w, r, unprocessable("SEARCH_NOT_SUPPORTED", "This provider does not expose a keyword search endpoint.", nil))
			return
		}
		// A query outside the contract bounds (q: 1..256) is caller input and
		// maps to a structured 422 instead of the generic 500 (issue #9).
		if errors.Is(err, catalogapplication.ErrSearchQueryInvalid) {
			writeError(w, r, unprocessable("SEARCH_QUERY_INVALID", "The search query must contain 1 to 256 characters.", []generated.ErrorDetail{{
				Code:    "QUERY_OUT_OF_RANGE",
				Message: "q must contain 1 to 256 characters.",
			}}))
			return
		}
		// Provider/upstream failures (unreachable upstream, non-2xx status,
		// unreadable payload) are structured provider outcomes, never an
		// INTERNAL 500. Status stays within the endpoint's declared response
		// set (200/401/422), mirroring the issue #10 boundary-validation fix.
		if errors.Is(err, catalogapplication.ErrProviderUpstream) {
			writeError(w, r, unprocessable("SEARCH_UPSTREAM_FAILED", "The provider search failed at or behind the upstream endpoint.", []generated.ErrorDetail{{
				Code:    "PROVIDER_UPSTREAM",
				Message: "The provider upstream returned no usable search response.",
			}}))
			return
		}
		writeError(w, r, err)
		return
	}
	items := make([]generated.CatalogItem, 0, len(result.Entries))
	for _, entry := range result.Entries {
		items = append(items, mapSearchEntry(string(provider), entry))
	}
	s.proxyCatalogCovers(r.Context(), items)
	writeJSON(w, r.Context(), http.StatusOK, items)
}

// mapSearchEntry projects one provider search hit onto the catalog item shape,
// shared by the REST handler and the MCP search_anime tool so both surfaces
// produce identical results for the same query.
func mapSearchEntry(provider string, entry catalogdomain.Entry) generated.CatalogItem {
	kind, origin := catalogdomain.ItemAniBTEntry, "anibt_search"
	if provider == catalogapplication.ProviderMikan {
		kind, origin = catalogdomain.ItemMikanEntry, "mikan_search"
	}
	// AniBT search hits may carry an empty ProviderEntryRef with only a BGMID;
	// normalize with the same fallback as the browse path
	// (catalog/application/provider_service.go) so downstream subscribe flows
	// receive a usable entry ref.
	entryRef := strings.TrimSpace(entry.ProviderEntryRef)
	if entryRef == "" {
		entryRef = strings.TrimSpace(entry.BGMID)
	}
	item := catalogdomain.Item{
		Kind: kind, Origin: origin, Provider: provider, Title: entry.Title,
		OriginalTitle: entry.OriginalTitle, ProviderEntryRef: entryRef,
		BGMID: entry.BGMID, SourceHint: entry.SourceHint, CatalogState: string(entry.State),
		AllowedActions: []string{"prepare_subscribe"},
	}
	item.CoverURL = catalogapplication.HintString(entry.SourceHint, "cover")
	if entry.BGMID != "" {
		item.PosterURL = "/api/v1/images/poster/" + entry.BGMID
	}
	catalogapplication.ApplyReleaseAvailability(&item)
	item.ReleaseFormat = catalogapplication.ReleaseFormatKey(provider, entry.SourceHint)
	return mapCatalogItem(item)
}

// RefreshCatalogProvider triggers an immediate provider-scoped catalog refresh
// and returns the resulting snapshot.
func (s *Server) RefreshCatalogProvider(w http.ResponseWriter, r *http.Request, provider generated.RefreshCatalogProviderParamsProvider, season generated.CatalogSeason) {
	catalog := s.catalogService()
	snapshot, err := catalog.RefreshProviderSeason(r.Context(), string(provider), string(season))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCatalogSnapshot(snapshot))
}

// ReadCatalogPreference returns the provider-scoped refresh preference for one
// season.
func (s *Server) ReadCatalogPreference(w http.ResponseWriter, r *http.Request, provider generated.ReadCatalogPreferenceParamsProvider, season generated.CatalogSeason) {
	catalog := s.catalogService()
	pref, err := catalog.ReadProviderPreference(r.Context(), string(provider), string(season))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCatalogPreference(pref))
}

// SetCatalogPreference saves the provider-scoped refresh preference with
// If-Match version CAS.
func (s *Server) SetCatalogPreference(w http.ResponseWriter, r *http.Request, provider generated.SetCatalogPreferenceParamsProvider, season generated.CatalogSeason, params generated.SetCatalogPreferenceParams) {
	catalog := s.catalogService()
	var input generated.CatalogPreferenceInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	pref := catalogdomain.Preference{
		Season:         string(season),
		RefreshEnabled: input.RefreshEnabled,
		Schedule:       strPtrToString(input.Schedule),
	}
	saved, err := catalog.SetProviderPreference(r.Context(), string(provider), pref, int64(params.IfMatch))
	if err != nil {
		if errors.Is(err, catalogapplication.ErrPreferenceVersionConflict) {
			current := saved.Version
			writeError(w, r, conflict("CATALOG_PREFERENCE_VERSION_CONFLICT", "The catalog preference changed before this request completed.", &current))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCatalogPreference(saved))
}

// ListCatalogSuppressions returns every suppressed provider catalog entry.
// Suppressions are not season-scoped, so the whole list is returned.
func (s *Server) ListCatalogSuppressions(w http.ResponseWriter, r *http.Request) {
	catalog := s.catalogService()
	suppressions, err := catalog.ListSuppressions(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := make([]generated.CatalogSuppression, 0, len(suppressions))
	for _, suppression := range suppressions {
		body = append(body, mapCatalogSuppression(suppression))
	}
	writeJSON(w, r.Context(), http.StatusOK, body)
}

// SuppressCatalogEntry hides a provider catalog entry. It is idempotent: an
// already suppressed entry has its stored title refreshed and returns 200.
func (s *Server) SuppressCatalogEntry(w http.ResponseWriter, r *http.Request, provider generated.SuppressCatalogEntryParamsProvider, providerEntryRef generated.CatalogProviderEntryRef) {
	catalog := s.catalogService()
	var input generated.CatalogSuppressionInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	suppression, err := catalog.SuppressEntry(r.Context(), string(provider), string(providerEntryRef), input.TitleSnapshot)
	if err != nil {
		writeError(w, r, mapCatalogSuppressionError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCatalogSuppression(suppression))
}

// UnsuppressCatalogEntry restores a provider catalog entry. It is idempotent:
// removing a suppression that does not exist still returns 204.
func (s *Server) UnsuppressCatalogEntry(w http.ResponseWriter, r *http.Request, provider generated.UnsuppressCatalogEntryParamsProvider, providerEntryRef generated.CatalogProviderEntryRef) {
	catalog := s.catalogService()
	if err := catalog.UnsuppressEntry(r.Context(), string(provider), string(providerEntryRef)); err != nil {
		writeError(w, r, mapCatalogSuppressionError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusNoContent, nil)
}

// mapCatalogSuppressionError keeps rejected suppression requests at 422 instead
// of letting a validation failure fall through to the generic 500 mapping.
func mapCatalogSuppressionError(err error) error {
	if errors.Is(err, catalogapplication.ErrSuppressionInvalid) {
		return unprocessable("CATALOG_SUPPRESSION_INVALID", "The catalog suppression request failed typed validation.", nil)
	}
	return err
}

func (s *Server) catalogService() CatalogService {
	return s.deps.Catalog
}

// --- domain -> generated mappers ---

func mapCatalogWorkspace(ws catalogdomain.Workspace) generated.CatalogWorkspace {
	items := make([]generated.CatalogItem, 0, len(ws.Items))
	for _, item := range ws.Items {
		items = append(items, mapCatalogItem(item))
	}
	return generated.CatalogWorkspace{
		Items:    items,
		Season:   ws.Season,
		Snapshot: mapCatalogSnapshot(ws.Snapshot),
	}
}

func mapCatalogSnapshot(snap catalogdomain.Snapshot) generated.CatalogSnapshot {
	return generated.CatalogSnapshot{
		ContractVersion: snap.ContractVersion,
		Cursor:          stringToPtr(snap.Cursor),
		ErrorCode:       stringToPtr(snap.ErrorCode),
		ErrorMessage:    stringToPtr(snap.ErrorMessage),
		Etag:            stringToPtr(snap.ETag),
		HealthCheckedAt: snap.HealthCheckedAt,
		HealthCode:      stringToPtr(snap.HealthCode),
		HealthMessage:   stringToPtr(snap.HealthMessage),
		HealthState:     generated.CatalogSnapshotHealthState(snap.HealthState),
		Id:              snap.ID,
		LastModified:    stringToPtr(snap.LastModified),
		ObservedAt:      snap.ObservedAt,
		Provider:        generated.CatalogSnapshotProvider(snap.Provider),
		RefreshId:       stringToPtr(snap.RefreshID),
		Season:          snap.Season,
		SourceVersion:   snap.SourceVersion,
		Status:          generated.CatalogSnapshotStatus(snap.Status),
		TraceId:         stringToPtr(snap.TraceID),
		Version:         snap.Version,
		LastSuccessAt:   snap.LastSuccessAt,
		Stale:           snap.Stale,
	}
}

func mapCatalogItem(item catalogdomain.Item) generated.CatalogItem {
	instances := make([]generated.CatalogItemInstance, 0, len(item.Instances))
	for _, instance := range item.Instances {
		instances = append(instances, generated.CatalogItemInstance{
			ManagedAnimeRef:     generated.ID(instance.ManagedAnimeRef),
			SubscriptionRef:     generated.ID(instance.SubscriptionRef),
			SubscriptionState:   generated.SubscriptionState(instance.SubscriptionState),
			LifecycleState:      generated.CatalogItemInstanceLifecycleState(instance.LifecycleState),
			LifecycleEpoch:      instance.LifecycleEpoch,
			SubscriptionVersion: instance.SubscriptionVersion,
		})
	}
	result := generated.CatalogItem{
		AllowedActions:   item.AllowedActions,
		BgmId:            stringToPtr(item.BGMID),
		CatalogState:     item.CatalogState,
		Instances:        instances,
		Kind:             generated.CatalogItemKind(item.Kind),
		ManagedAnimeRef:  stringToPtr(item.ManagedAnimeRef),
		ObservedAt:       copyTimePtr(item.ObservedAt),
		Origin:           stringToPtr(item.Origin),
		OriginalTitle:    stringToPtr(item.OriginalTitle),
		Preference:       generated.CatalogItemPreference(item.Preference),
		Provider:         generated.CatalogItemProvider(item.Provider),
		ProviderEntryRef: item.ProviderEntryRef,
		SubscriptionRef:  stringToPtr(item.SubscriptionRef),
		Title:            item.Title,
	}
	if item.SubscriptionState != "" {
		state := generated.SubscriptionState(item.SubscriptionState)
		result.SubscriptionState = &state
	}
	if item.PosterURL != "" {
		poster := item.PosterURL
		result.PosterUrl = &poster
	}
	if item.CoverURL != "" {
		cover := item.CoverURL
		result.CoverUrl = &cover
	}
	// Release availability is provider-stated only; unknown stays absent.
	if item.ReleaseState != "" {
		state := generated.CatalogItemReleaseState(item.ReleaseState)
		result.ReleaseState = &state
	}
	if item.ReleaseCount != nil {
		count := *item.ReleaseCount
		result.ReleaseCount = &count
	}
	result.UpdateText = stringToPtr(item.UpdateText)
	// Search hits and any entry without a schedule fact read as none; the key
	// vocabulary never carries an empty value.
	day := item.BroadcastDay
	if day == "" {
		day = "none"
	}
	result.BroadcastDay = generated.BroadcastDay(day)
	if item.ReleaseFormat != "" {
		format := generated.ReleaseFormat(item.ReleaseFormat)
		result.ReleaseFormat = &format
	}
	return result
}

func mapCatalogSuppression(suppression catalogdomain.Suppression) generated.CatalogSuppression {
	return generated.CatalogSuppression{
		Id:               suppression.ID,
		Provider:         generated.ProviderKind(suppression.Provider),
		ProviderEntryRef: suppression.ProviderEntryRef,
		SuppressedAt:     suppression.SuppressedAt,
		TitleSnapshot:    suppression.TitleSnapshot,
		Version:          suppression.Version,
	}
}

func mapCatalogPreference(pref catalogdomain.Preference) generated.CatalogPreference {
	return generated.CatalogPreference{
		RefreshEnabled: pref.RefreshEnabled,
		Schedule:       stringToPtr(pref.Schedule),
		Season:         pref.Season,
		UpdatedAt:      pref.UpdatedAt,
		Version:        pref.Version,
	}
}

// stringToPtr returns nil for empty strings so the JSON field is omitted from
// the response body. Non-empty strings are returned as *string.
func stringToPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := s
	return &v
}

// strPtrToString dereferences a *string, returning "" for nil.
func strPtrToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// copyTimePtr returns a copy of the pointed-to time, or nil.
func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
