package httpapi

import (
	"bansoko/internal/apperrors"
	catalog "bansoko/internal/catalog/domain"
	transport "bansoko/internal/catalog/transport"
	"bansoko/internal/transport/httpapi/generated"
	"context"
	"net/http"
)

type customSnapshotFetcher interface {
	FetchCustomSnapshot(context.Context, string, int64, int64) (catalog.ProviderRSSPreviewResult, error)
}

func (s *Server) FetchCustomSnapshot(w http.ResponseWriter, r *http.Request) {
	var input generated.CustomSnapshotFetchInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	app, ok := s.catalogService().(customSnapshotFetcher)
	if !ok {
		writeError(w, r, &apperrors.Business{Status: 503, Code: "CUSTOM_FEED_SNAPSHOT_UNAVAILABLE", Message: "Custom snapshots are not configured."})
		return
	}
	result, err := app.FetchCustomSnapshot(r.Context(), input.CandidateId, input.Revision, input.ExpectedVersion)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, transport.ProviderRSSPreviewResult(result))
}

type ownerCustomSnapshotFetcher interface {
	FetchOwnerCustomSnapshot(context.Context, string) (catalog.ProviderRSSPreviewResult, error)
}

// FetchOwnerCustomSnapshot is the pending custom subscription's test-bench
// fetch: one snapshot write, never a runtime, refresh work or admission.
func (s *Server) FetchOwnerCustomSnapshot(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	app, ok := s.catalogService().(ownerCustomSnapshotFetcher)
	if !ok {
		writeError(w, r, &apperrors.Business{Status: 503, Code: "CUSTOM_FEED_SNAPSHOT_UNAVAILABLE", Message: "Custom snapshots are not configured."})
		return
	}
	result, err := app.FetchOwnerCustomSnapshot(r.Context(), string(managedAnimeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, transport.ProviderRSSPreviewResult(result))
}
