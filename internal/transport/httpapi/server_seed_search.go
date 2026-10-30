package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/transport/httpapi/generated"
)

// Keyword seed search transport (operationId fetchSeedSearchSnapshot). The
// handler delegates to the one catalogapplication.SeedSearchApplication, so the
// search snapshot a fetch persists is exactly the snapshot an expand or download
// later resolves. The UI reads the snapshot through the one-shot read model, so
// no snapshot read endpoint exists: keyword search has exactly one surface.

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
	preview, err := search.Fetch(r.Context(), catalogapplication.FetchSearchSnapshotInput{
		Provider: string(provider), Query: body.Query,
	})
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
		if seed.SourceSite != nil {
			row.SourceSite = &generated.SeedSourceSite{Id: seed.SourceSite.ID, Name: seed.SourceSite.Name, Type: generated.SeedSourceSiteType(seed.SourceSite.Type)}
		}
		if seed.GroupName != "" {
			group := seed.GroupName
			row.GroupName = &group
		}
		if seed.TitleHint != "" {
			hint := seed.TitleHint
			row.TitleHint = &hint
		}
		if seed.Resolution != "" {
			resolution := seed.Resolution
			row.Resolution = &resolution
		}
		seeds = append(seeds, row)
	}
	view := generated.SeedSearchSnapshotView{
		Provider:         generated.SeedSearchSnapshotViewProvider(preview.Provider),
		ScopeRef:         preview.ScopeRef,
		QueryRef:         preview.QueryRef,
		SnapshotId:       generated.ID(preview.SnapshotID),
		SnapshotVersion:  generated.Version(preview.Version),
		ContentHash:      preview.ContentHash,
		Freshness:        generated.SeedSearchSnapshotViewFreshness(preview.Freshness),
		TotalSeeds:       preview.TotalSeeds,
		RetentionSeconds: retentionSeconds(preview),
		Seeds:            seeds,
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
// response set: a rate limit answers 429 with the provider's own Retry-After,
// and an out-of-contract query stays the typed 422.
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
