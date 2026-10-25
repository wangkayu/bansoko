package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// Keyword seed search transport (operationIds fetchSeedSearchSnapshot /
// readSeedSearchSnapshot). Both handlers delegate to the one
// catalogapplication.SeedSearchApplication, so the search snapshot a fetch
// persists is exactly the snapshot an expand or download later resolves.

// FetchSeedSearchSnapshot runs one keyword torrent search and returns its
// snapshot plus the first seed window. The provider path segment is the only
// other caller input: the endpoint, the query normalization and the snapshot
// scope are derived server-side.
func (s *Server) FetchSeedSearchSnapshot(w http.ResponseWriter, r *http.Request, provider generated.FetchSeedSearchSnapshotParamsProvider) {
	search := s.deps.SeedSearch
	if search == nil {
		writeError(w, r, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_SEARCH_NOT_CONFIGURED", Message: "Provider keyword search is not configured on this server."})
		return
	}
	var body generated.SeedSearchSnapshotInput
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	limit := 0
	if body.Limit != nil {
		limit = *body.Limit
	}
	preview, err := search.Fetch(r.Context(), catalogapplication.FetchSearchSnapshotInput{
		Provider: string(provider), Query: body.Query, Limit: limit,
	})
	if err != nil {
		writeSeedSearchError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSeedSearchPreview(preview))
}

// ReadSeedSearchSnapshot reads a bounded window of one exact search snapshot. It
// never contacts a provider and never falls back to the latest snapshot.
func (s *Server) ReadSeedSearchSnapshot(w http.ResponseWriter, r *http.Request, provider generated.ReadSeedSearchSnapshotParamsProvider, snapshotId generated.ID, params generated.ReadSeedSearchSnapshotParams) {
	search := s.deps.SeedSearch
	if search == nil {
		writeError(w, r, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_SEARCH_NOT_CONFIGURED", Message: "Provider keyword search is not configured on this server."})
		return
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	preview, err := search.ReadWindow(r.Context(), string(provider), string(params.ScopeRef), string(params.QueryRef), string(snapshotId), params.ContentHash, int64(params.SnapshotVersion), limit, cursor)
	if err != nil {
		writeSeedSearchError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSeedSearchPreview(preview))
}

// mapSeedSearchPreview projects one search snapshot window onto the contract
// shape. A missing value stays absent instead of becoming a zero that a client
// would render as a real fact.
func mapSeedSearchPreview(preview catalogapplication.SearchSeedPreview) generated.SeedSearchSnapshotView {
	seeds := make([]generated.SeedSearchSnapshotSeed, 0, len(preview.Seeds))
	for _, seed := range preview.Seeds {
		row := generated.SeedSearchSnapshotSeed{
			StableSeedKey: seed.StableSeedKey,
			Title:         seed.Title,
			DownloadUrl:   seed.LocatorDisplay,
			TorrentState:  generated.SeedSearchSnapshotSeedTorrentState("not_applicable"),
		}
		if !seed.PublishedAt.IsZero() {
			published := seed.PublishedAt.UTC()
			row.PublishedAt = &published
		}
		row.SizeBytes = seed.SizeBytes
		row.InfoHash = seed.InfoHash
		if seed.GroupName != "" {
			group := seed.GroupName
			row.GroupName = &group
		}
		if seed.Resolution != "" {
			resolution := seed.Resolution
			row.Resolution = &resolution
		}
		seeds = append(seeds, row)
	}
	view := generated.SeedSearchSnapshotView{
		Provider:         generated.SeedSearchSnapshotViewProvider(preview.Provider),
		ScopeRef:         generated.SeedSearchScopeRef(preview.ScopeRef),
		QueryRef:         generated.SeedSearchQueryRef(preview.QueryRef),
		SnapshotId:       generated.ID(preview.SnapshotID),
		SnapshotVersion:  generated.Version(preview.Version),
		ContentHash:      preview.ContentHash,
		Freshness:        generated.SeedSearchSnapshotViewFreshness(preview.Freshness),
		TotalSeeds:       preview.TotalSeeds,
		Window:           preview.Window,
		RetentionSeconds: retentionSeconds(preview),
		Seeds:            seeds,
	}
	if preview.NextCursor != "" {
		cursor := preview.NextCursor
		view.NextCursor = &cursor
	}
	if preview.Query != "" {
		query := preview.Query
		view.Query = &query
	}
	if !preview.FetchedAt.IsZero() {
		fetched := preview.FetchedAt.UTC()
		view.FetchedAt = &fetched
	}
	if !preview.FreshUntil.IsZero() {
		fresh := preview.FreshUntil.UTC()
		view.FreshUntil = &fresh
	}
	if preview.Parser != "" {
		parser := preview.Parser
		view.Parser = &parser
	}
	return view
}

// writeSeedSearchError maps the search-specific outcomes onto the declared
// response set: a rate limit answers 429 with the provider's own Retry-After, an
// out-of-contract query stays the typed 422, and every exact-snapshot refusal
// becomes the 409 this endpoint declares.
//
// The exact-snapshot refusals are domain sentinels (not typed business errors),
// so the shared boundary would otherwise answer them as an unknown 500 INTERNAL.
// That is wrong for this read: a missing, superseded, mismatching, differently
// kinded or stale snapshot is an ordinary client state — the page held an
// identity the server can no longer prove — and the caller's remedy is to search
// again. They are mapped here exactly like the feed surfaces map them, and the
// cursor refusal keeps its own typed 422 because it describes the position, not
// the snapshot.
func writeSeedSearchError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *catalogapplication.ProviderRateLimited
	if errors.As(err, &limited) {
		if limited.RetryAfter > 0 {
			w.Header().Set("Retry-After", retryAfterSeconds(limited.RetryAfter))
		}
		writeError(w, r, &apperrors.Business{Status: http.StatusTooManyRequests, Code: "PROVIDER_RATE_LIMITED", Message: limited.Error()})
		return
	}
	if errors.Is(err, catalogapplication.ErrSearchQueryInvalid) {
		writeError(w, r, unprocessable("SEARCH_QUERY_INVALID", "The search query must contain 1 to 256 characters.", []generated.ErrorDetail{{
			Code:    "QUERY_OUT_OF_RANGE",
			Message: "query must contain 1 to 256 characters.",
		}}))
		return
	}
	if errors.Is(err, catalogdomain.ErrProviderFeedSnapshotNotFound) ||
		errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) ||
		errors.Is(err, catalogdomain.ErrProviderFeedSnapshotStale) {
		writeError(w, r, &apperrors.Business{Status: http.StatusConflict, Code: "FEED_SNAPSHOT_REFRESH_REQUIRED", Message: "The named search snapshot is no longer current. Search again and retry."})
		return
	}
	writeError(w, r, err)
}

// retentionSeconds reports the derived retention the application applied to this
// snapshot. It is read from the snapshot itself, so a caller never chooses it and
// the contract cannot drift from the publication function's own value.
func retentionSeconds(preview catalogapplication.SearchSeedPreview) int {
	if preview.FetchedAt.IsZero() || preview.RetainedTill.IsZero() {
		return 0
	}
	seconds := int(preview.RetainedTill.Sub(preview.FetchedAt) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func retryAfterSeconds(wait time.Duration) string {
	seconds := int(wait / time.Second)
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
