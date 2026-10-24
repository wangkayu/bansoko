package httpapi

import (
	"net/http"

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
