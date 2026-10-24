package httpapi

import (
	"net/http"

	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/transport/httpapi/generated"
)

// ExpandSnapshotSeed expands one seed of one exact persisted snapshot
// (operationId expandSnapshotSeed). It is the lazy half of the RSS list read:
// the list never fetches a torrent, and this handler resolves a seed's locator
// from the named snapshot server-side, serves it from the locator cache when the
// seed was already expanded, and otherwise fetches, verifies and caches the
// bytes once before returning the derived file projection.
//
// The outcome is a state, not an error, exactly as the read model renders it: an
// unreachable, corrupt or missing upstream torrent answers 200 with
// state=unavailable and a bounded error_code, because the caller asked a question
// about an external resource and the answer is "not available". A snapshot
// identity the server can no longer prove stays a typed failure, so a client can
// never be handed a manifest from a different snapshot than the one it named.
func (s *Server) ExpandSnapshotSeed(w http.ResponseWriter, r *http.Request, snapshotId generated.ID, stableSeedKey string) {
	var body generated.SnapshotSeedExpandInput
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	fact, err := s.catalogService().EnsureSeedTorrent(r.Context(), catalogapplication.EnsureSeedTorrentInput{
		Provider:         string(body.Provider),
		ProviderEntryRef: body.ProviderEntryRef,
		ProviderGroupRef: body.ProviderGroupRef,
		SnapshotID:       string(snapshotId),
		SnapshotVersion:  int64(body.SnapshotVersion),
		ContentHash:      body.ContentHash,
		StableSeedKey:    stableSeedKey,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapSnapshotSeedExpandResult(fact))
}

// mapSnapshotSeedExpandResult projects one expansion outcome. The nested torrent
// holds the same state as the result and, when the seed is available, the
// manifest the row renders, so a browser renders files and targets from this
// server-side projection instead of assembling them itself. Cached reports
// whether this call touched the provider at all, which is what lets a client
// distinguish "already expanded" from "just fetched".
func mapSnapshotSeedExpandResult(fact catalogapplication.SeedTorrentFact) generated.SnapshotSeedExpandResult {
	torrent := generated.RSSListTorrentState{State: generated.RSSListTorrentStateState(fact.State)}
	if fact.Manifest != nil {
		manifest := mapTorrentManifestView(*fact.Manifest)
		torrent.Manifest = &manifest
	}
	// An unavailable expansion explains itself with the same bounded reason the
	// list read would carry; an available one has no reason to give.
	if fact.ErrorCode != "" {
		code := fact.ErrorCode
		torrent.ErrorCode = &code
	}
	if fact.ErrorReason != "" {
		reason := fact.ErrorReason
		torrent.ErrorReason = &reason
	}
	result := generated.SnapshotSeedExpandResult{
		State:   generated.SnapshotSeedExpandResultState(fact.State),
		Torrent: torrent,
		Cached:  fact.Cached,
	}
	if torrent.ErrorCode != nil {
		result.ErrorCode = torrent.ErrorCode
	}
	if torrent.ErrorReason != nil {
		result.ErrorReason = torrent.ErrorReason
	}
	return result
}
