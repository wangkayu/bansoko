package httpapi

import (
	"errors"
	"net/http"

	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/transport/httpapi/generated"
)

// ListSubgroupGroupCandidates lists one page of the subgroup dictionary of one
// provider: the collected snapshot that ships with the release, union the
// entries a user retrieved and added.
//
// The listing is purely local — it contacts no provider — so it answers on an
// install that has saved no connection, and it never reports the dictionary as
// unavailable for that reason.
func (s *Server) ListSubgroupGroupCandidates(w http.ResponseWriter, r *http.Request, params generated.ListSubgroupGroupCandidatesParams) {
	listing := catalogapplication.GroupDictionaryListing{Provider: string(params.Provider)}
	if params.Q != nil {
		listing.Query = *params.Q
	}
	if params.Source != nil {
		listing.Source = string(*params.Source)
	}
	if params.HideRetired != nil {
		listing.HideRetired = *params.HideRetired
	}
	if params.Offset != nil {
		listing.Offset = *params.Offset
	}
	if params.Limit != nil {
		if *params.Limit < 1 {
			writeError(w, r, mapSubgroupDictionaryError(catalogapplication.ErrGroupDictionaryRequestInvalid))
			return
		}
		listing.Limit = *params.Limit
	}
	page, err := s.catalogService().ListGroupDictionary(r.Context(), listing)
	if err != nil {
		writeError(w, r, mapSubgroupDictionaryError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, subgroupGroupCandidatePage(page))
}

// RetrieveSubgroupGroupCandidate resolves one subgroup key against the
// dictionary and, only for a key the dictionary does not hold, against the
// provider; a group the provider confirms is added to the dictionary's user
// layer. A query that names no key is 400 SUBGROUP_DISCOVER_KEY_REQUIRED. It is idempotent: a key the dictionary already holds is returned as it
// stands, so a repeated retrieval succeeds without writing anything. An empty
// candidate list is a valid answer, not an error.
//
// A retrieval that needs a live provider read and finds no configured
// connection fails closed as 503 PROVIDER_GROUP_LOOKUP_FAILED, raised by the
// provider adapter.
func (s *Server) RetrieveSubgroupGroupCandidate(w http.ResponseWriter, r *http.Request) {
	var input generated.SubgroupGroupRetrievalInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	entries, err := s.catalogService().RetrieveGroupDictionary(r.Context(), string(input.Provider), input.Query)
	if err != nil {
		writeError(w, r, mapSubgroupDictionaryError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, subgroupGroupCandidateList(entries))
}

// DeleteSubgroupGroupDictionaryEntry removes one entry from the user layer of
// the dictionary. The collected layer is reference data published with the
// release: a request to remove one of its entries is 409, and a request for an
// entry neither layer holds is 404.
func (s *Server) DeleteSubgroupGroupDictionaryEntry(w http.ResponseWriter, r *http.Request, provider generated.ProviderKind, groupRef string) {
	if err := s.catalogService().DeleteGroupDictionaryEntry(r.Context(), string(provider), groupRef); err != nil {
		writeError(w, r, mapSubgroupDictionaryError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusNoContent, nil)
}

// mapSubgroupDictionaryError gives the dictionary's stable outcomes their
// contract statuses: a rejected request (including a discover query that names
// no key) is 400, an entry that belongs to the
// collected snapshot is 409, an entry no layer holds is 404. Everything else —
// including the provider adapter's 503 PROVIDER_GROUP_LOOKUP_FAILED — keeps the
// shape it was raised with.
func mapSubgroupDictionaryError(err error) error {
	switch {
	case errors.Is(err, catalogapplication.ErrGroupDictionaryQueryNotKey):
		return badRequest("SUBGROUP_DISCOVER_KEY_REQUIRED", "Subgroup discovery needs a Mikan group id, an AniBT slug or a group page link.")
	case errors.Is(err, catalogapplication.ErrGroupDictionaryRequestInvalid):
		return badRequest("SUBGROUP_DICTIONARY_REQUEST_INVALID", "The subgroup dictionary request is invalid.")
	case errors.Is(err, catalogapplication.ErrGroupDictionaryEntryImmutable):
		return conflict("GROUP_DICTIONARY_ENTRY_IMMUTABLE", "This subgroup belongs to the dictionary published with the release and cannot be removed.", nil)
	case errors.Is(err, catalogapplication.ErrGroupDictionaryEntryNotFound):
		return notFound("GROUP_DICTIONARY_ENTRY_NOT_FOUND", "This subgroup is not in the dictionary.")
	}
	return err
}
