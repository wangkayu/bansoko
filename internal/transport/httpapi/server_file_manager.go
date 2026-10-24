package httpapi

import (
	"errors"
	"net/http"

	"bansoko/internal/filemanager"
	"bansoko/internal/transport/httpapi/generated"
)

// File manager (browse here; commands in server_file_manager_commands.go):
// browse of any absolute path the container user can read, with the derived
// ownership state and the lock of every entry. Every response is no-store so a
// re-read after an owner settles shows the new state.

// ListFileManagerMounts lists the quick entries the file manager offers.
func (s *Server) ListFileManagerMounts(w http.ResponseWriter, r *http.Request) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	mounts, err := s.deps.FileManager.Mounts(r.Context())
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	result := generated.FileManagerMountList{Mounts: make([]generated.FileManagerMount, 0, len(mounts))}
	for _, mount := range mounts {
		result.Mounts = append(result.Mounts, mapFileManagerMount(mount))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// ListFileManagerEntries returns one sorted page of a directory.
func (s *Server) ListFileManagerEntries(w http.ResponseWriter, r *http.Request, params generated.ListFileManagerEntriesParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	cursor, limit := "", 0
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	listing, err := s.deps.FileManager.List(r.Context(), params.Path, cursor, limit)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapFileManagerListing(listing))
}

// GetFileManagerEntry returns the read-only detail of one entry.
func (s *Server) GetFileManagerEntry(w http.ResponseWriter, r *http.Request, params generated.GetFileManagerEntryParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	detail, err := s.deps.FileManager.Detail(r.Context(), params.Path)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapFileManagerDetail(detail))
}

// LocateFileManagerAnime returns the directory holding one anime's files.
func (s *Server) LocateFileManagerAnime(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	location, err := s.deps.FileManager.Locate(r.Context(), managedAnimeID)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, generated.FileManagerLocation{Path: location.Path})
}

// writeFileManagerError maps the typed browse errors to their status and
// stable code; everything else takes the shared error boundary. A refusal that
// names specific files (the managed files inside a refused directory) carries
// them as the detail's paths.
func writeFileManagerError(w http.ResponseWriter, r *http.Request, err error) {
	var typed *filemanager.Error
	if !errors.As(err, &typed) {
		writeError(w, r, err)
		return
	}
	status := http.StatusBadRequest
	switch typed.Kind {
	case filemanager.ErrorNotFound:
		status = http.StatusNotFound
	case filemanager.ErrorConflict:
		status = http.StatusConflict
	case filemanager.ErrorUnavailable:
		status = http.StatusServiceUnavailable
	}
	apiErr := &APIError{Status: status, Code: typed.Code, Message: typed.Message}
	if len(typed.Paths) > 0 {
		paths := append([]string(nil), typed.Paths...)
		apiErr.Details = []generated.ErrorDetail{{Paths: &paths}}
	}
	writeError(w, r, apiErr)
}

func mapFileManagerMount(mount filemanager.Mount) generated.FileManagerMount {
	mapped := generated.FileManagerMount{
		Key: mount.Key, Kind: generated.FileManagerMountKind(mount.Kind), Path: mount.Path, Available: mount.Available,
	}
	if mount.ConnectionID != "" {
		id := mount.ConnectionID
		mapped.ConnectionId = &id
	}
	if mount.Label != "" {
		label := mount.Label
		mapped.Label = &label
	}
	if mount.ManagedAnimeID != "" {
		id := mount.ManagedAnimeID
		mapped.ManagedAnimeId = &id
	}
	if mount.Unavailable != "" {
		reason := generated.FileManagerMountUnavailable(mount.Unavailable)
		mapped.UnavailableReason = &reason
	}
	return mapped
}

func mapFileManagerEntry(entry filemanager.Entry) generated.FileManagerEntry {
	mapped := generated.FileManagerEntry{
		Name: entry.Name, Path: entry.Path, Kind: generated.FileManagerEntryKind(entry.Kind),
		State: generated.FileManagerEntryState(entry.State), SizeBytes: entry.SizeBytes, ModifiedAt: entry.ModifiedAt,
		Locked: entry.Locked,
	}
	if entry.ManagedAnimeID != "" {
		id := entry.ManagedAnimeID
		mapped.ManagedAnimeId = &id
	}
	if entry.Role != filemanager.RoleNone {
		role := generated.FileManagerEntryRole(entry.Role)
		mapped.Role = &role
	}
	if entry.CompanionOf != "" {
		companionOf := entry.CompanionOf
		mapped.CompanionOf = &companionOf
	}
	if entry.NotAligned {
		notAligned := true
		mapped.NotAligned = &notAligned
	}
	return mapped
}

func mapFileManagerListing(listing filemanager.Listing) generated.FileManagerListing {
	mapped := generated.FileManagerListing{
		Path: listing.Path, Truncated: listing.Truncated,
		Entries: make([]generated.FileManagerEntry, 0, len(listing.Entries)),
	}
	for _, entry := range listing.Entries {
		mapped.Entries = append(mapped.Entries, mapFileManagerEntry(entry))
	}
	if listing.NextCursor != "" {
		cursor := listing.NextCursor
		mapped.NextCursor = &cursor
	}
	return mapped
}

// mapFileManagerLinkedFile maps one related file. Its path is always carried:
// a related file is known by where it is, so there is no separate "located".
func mapFileManagerLinkedFile(file filemanager.LinkedFile) generated.FileManagerLinkedFile {
	path := file.Path
	mapped := generated.FileManagerLinkedFile{Role: generated.FileManagerEntryRole(file.Role), Path: &path, Present: file.Present}
	if file.NotAligned {
		notAligned := true
		mapped.NotAligned = &notAligned
	}
	return mapped
}

func mapFileManagerDetail(detail filemanager.Detail) generated.FileManagerEntryDetail {
	mapped := generated.FileManagerEntryDetail{
		Entry:      mapFileManagerEntry(detail.Entry),
		InProgress: make([]generated.FileManagerInProgressOwner, 0, len(detail.InProgress)),
		Companions: make([]generated.FileManagerLinkedFile, 0, len(detail.Companions)),
		Operations: make([]generated.FileManagerOperationRule, 0, len(detail.Operations)),
		Revision:   detail.Revision,
	}
	if detail.LastOperation != nil {
		last := mapFileManagerOperation(*detail.LastOperation)
		mapped.LastOperation = &last
	}
	if anime := detail.Anime; anime != nil {
		ref := generated.FileManagerAnimeRef{ManagedAnimeId: anime.ID, Title: anime.Title, Ended: anime.Ended}
		if anime.SubscriptionState != "" {
			state := generated.SubscriptionState(anime.SubscriptionState)
			ref.SubscriptionState = &state
		}
		mapped.Anime = &ref
	}
	if anime := detail.MatchDefaultAnime; anime != nil {
		ref := generated.FileManagerAnimeRef{ManagedAnimeId: anime.ID, Title: anime.Title}
		if anime.SubscriptionState != "" {
			state := generated.SubscriptionState(anime.SubscriptionState)
			ref.SubscriptionState = &state
		}
		mapped.MatchDefaultAnime = &ref
	}
	if episode := detail.Episode; episode != nil {
		evidence := generated.FileManagerEpisodeEvidence{State: generated.FileManagerEpisodeEvidenceState(episode.State)}
		if episode.FailureCode != "" {
			code := episode.FailureCode
			evidence.FailureCode = &code
		}
		if episode.RawValue != "" {
			raw := episode.RawValue
			evidence.RawValue = &raw
		}
		if episode.Shape != "" {
			shape := generated.FileManagerEpisodeEvidenceShape(episode.Shape)
			evidence.Shape = &shape
		}
		mapped.Episode = &evidence
	}
	if detail.AcquisitionID != "" {
		id := detail.AcquisitionID
		mapped.AcquisitionId = &id
	}
	if detail.DetachedReason != "" && detail.Entry.State == filemanager.StateDetached {
		reason := generated.FileManagerDetachedReason(detail.DetachedReason)
		mapped.DetachedReason = &reason
	}
	for _, owner := range detail.InProgress {
		ref := generated.FileManagerInProgressOwner{Kind: generated.FileManagerInProgressKind(owner.Kind)}
		if owner.AcquisitionID != "" {
			id := owner.AcquisitionID
			ref.AcquisitionId = &id
		}
		mapped.InProgress = append(mapped.InProgress, ref)
	}
	if detail.Video != nil {
		video := mapFileManagerLinkedFile(*detail.Video)
		mapped.Video = &video
	}
	for _, companion := range detail.Companions {
		mapped.Companions = append(mapped.Companions, mapFileManagerLinkedFile(companion))
	}
	for _, rule := range detail.Operations {
		mappedRule := generated.FileManagerOperationRule{Operation: generated.FileManagerOperation(rule.Operation), Allowed: rule.Allowed}
		if rule.Block != filemanager.BlockNone {
			block := generated.FileManagerOperationBlock(rule.Block)
			mappedRule.BlockedReason = &block
		}
		mapped.Operations = append(mapped.Operations, mappedRule)
	}
	return mapped
}
