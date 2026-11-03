package httpapi

import (
	"errors"
	"net/http"

	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	catalogtransport "bansoko/internal/catalog/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// DiscoverProviderEntryGroups runs subscription-free read-only provider group
// discovery for one catalog entry. It writes only a subscription-independent
// snapshot and never creates an RSS source or schedules a WorkItem.
func (s *Server) DiscoverProviderEntryGroups(w http.ResponseWriter, r *http.Request, provider generated.DiscoverProviderEntryGroupsParamsProvider, providerEntryRef generated.CatalogProviderEntryRef) {
	catalog := s.catalogService()
	result, err := catalog.DiscoverProviderEntryGroups(r.Context(), string(provider), string(providerEntryRef))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, catalogtransport.ProviderGroupDiscoveryResult(result))
}

// ReadProviderEntrySynopsis reads one catalog entry's whole synopsis and the
// facts its provider page states. It contacts the provider only when the
// per-entry cache has no fresh answer and never writes a row.
func (s *Server) ReadProviderEntrySynopsis(w http.ResponseWriter, r *http.Request, provider generated.ReadProviderEntrySynopsisParamsProvider, providerEntryRef generated.CatalogProviderEntryRef) {
	switch provider {
	case generated.ReadProviderEntrySynopsisParamsProviderAnibt, generated.ReadProviderEntrySynopsisParamsProviderMikan:
	default:
		writeError(w, r, unprocessable("SYNOPSIS_PROVIDER_UNSUPPORTED", "The catalog provider is unknown.", []generated.ErrorDetail{{
			Code:    "PROVIDER_UNSUPPORTED",
			Message: "provider must be one of: anibt, mikan.",
		}}))
		return
	}
	result, err := s.catalogService().ProviderEntrySynopsis(r.Context(), string(provider), string(providerEntryRef))
	if err != nil {
		switch {
		case errors.Is(err, catalogapplication.ErrSynopsisEntryInvalid):
			writeError(w, r, unprocessable("SYNOPSIS_ENTRY_INVALID", "The catalog entry reference is invalid.", nil))
		case errors.Is(err, catalogapplication.ErrProviderUpstream), errors.Is(err, catalogapplication.ErrSynopsisUnsupported):
			writeError(w, r, unavailable("SYNOPSIS_UPSTREAM_FAILED", "The provider page could not be read."))
		default:
			writeError(w, r, err)
		}
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapCatalogEntrySynopsis(result))
}

func mapCatalogEntrySynopsis(value catalogdomain.EntrySynopsis) generated.CatalogEntrySynopsis {
	result := generated.CatalogEntrySynopsis{
		Provider:         generated.CatalogEntrySynopsisProvider(value.Provider),
		ProviderEntryRef: value.ProviderEntryRef,
		SourceUrl:        value.SourceURL,
		Synopsis:         value.Synopsis,
		Episodes:         value.Episodes,
		Tags:             value.Tags,
	}
	if result.Tags == nil {
		result.Tags = []string{}
	}
	if value.Weekday != "" {
		weekday := generated.BroadcastDay(value.Weekday)
		result.Weekday = &weekday
	}
	if value.Season != "" {
		result.Season = &value.Season
	}
	if value.OfficialSite != "" {
		result.OfficialSite = &value.OfficialSite
	}
	if value.BGMURL != "" {
		result.BgmUrl = &value.BGMURL
	}
	return result
}

// ReadProviderEntryRSS reads the latest persisted RSS snapshot without provider I/O.
func (s *Server) ReadProviderEntryRSS(w http.ResponseWriter, r *http.Request, provider generated.ReadProviderEntryRSSParamsProvider, providerEntryRef generated.CatalogProviderEntryRef, params generated.ReadProviderEntryRSSParams) {
	catalog := s.catalogService()
	result, err := catalog.ReadProviderEntryRSS(r.Context(), string(provider), string(providerEntryRef), params.GroupRef)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, catalogtransport.ProviderRSSPreviewResult(result))
}

// PreviewProviderEntryRSS explicitly refreshes and persists one provider feed snapshot.
func (s *Server) PreviewProviderEntryRSS(w http.ResponseWriter, r *http.Request, provider generated.PreviewProviderEntryRSSParamsProvider, providerEntryRef generated.CatalogProviderEntryRef) {
	catalog := s.catalogService()
	var input generated.ProviderRSSPreviewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := catalog.PreviewProviderEntryRSS(r.Context(), string(provider), string(providerEntryRef), input.GroupRef)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, catalogtransport.ProviderRSSPreviewResult(result))
}
