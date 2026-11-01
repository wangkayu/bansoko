package application

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lib/pq"
)

// releaseLibraryFile is one file of an owner release whose file operation
// produced a placement: either the placement (video) or the companion
// (subtitle) holding it now, or the placement it has since ended in.
type releaseLibraryFile struct {
	ReleaseID    string
	SourcePath   string
	FrozenTarget string
	CompanionID  string
	// State is the placement's state (active, superseded, displaced or
	// released); empty on companion rows. Cause is a displaced placement's
	// cause (overwrite or redownload).
	State string
	Cause string
}

// ended reports whether this row is a placement that has ended rather than a
// holder of the file. A file operation has one placement
// (placements_source_uq), so its state alone decides it; a companion still
// holding the file keeps it held.
func (f releaseLibraryFile) ended() bool {
	if f.CompanionID != "" {
		return false
	}
	switch f.State {
	case RSSListPlacementEndSuperseded, RSSListPlacementEndDisplaced, RSSListPlacementEndReleased:
		return true
	}
	return false
}

// matchLibraryFile finds the row for one RSS file: by its source path, else by
// the frozen target the plan compared. An empty rendered target matches
// nothing.
func matchLibraryFile(bySource, byTarget map[string]releaseLibraryFile, releaseID, sourcePath, renderedTarget string) (releaseLibraryFile, bool) {
	if found, ok := bySource[releaseID+"\x00"+sourcePath]; ok {
		return found, true
	}
	if strings.TrimSpace(renderedTarget) == "" {
		return releaseLibraryFile{}, false
	}
	found, ok := byTarget[releaseID+"\x00"+filepath.Clean(renderedTarget)]
	return found, ok
}

// applyLibraryFacts records which files of an owner release the library already
// holds and how a placement that has ended ended.
//
// A file an active verified placement or an active companion holds reads
// InLibrary true, wherever the file sits now: the overlap compares the frozen
// rendered target with placement paths, which an alignment move (or a
// companion, which is no placement path at all) leaves behind, so the owner row
// would otherwise read pending-import for a file the library holds. The seed's
// overlap counts follow. A file whose placement has ended since (superseded,
// displaced or released) is in no library: it keeps its rendered target and
// gains PlacementEnd, the outcome of that one file.
//
// The view reports the rendered result and each file's state and never a
// location: where a file actually sits belongs to the anime's files. It reads
// and writes nothing else.
func (s *Service) applyLibraryFacts(ctx context.Context, animeID string, view *RSSListView) error {
	if s == nil || s.db == nil || view == nil || strings.TrimSpace(animeID) == "" {
		return nil
	}
	releaseIDs := []string{}
	for _, group := range view.Groups {
		for _, item := range group.Items {
			if id, ok := strings.CutPrefix(item.SnapshotID, "release:"); ok && id != "" {
				releaseIDs = append(releaseIDs, id)
			}
		}
	}
	if len(releaseIDs) == 0 {
		return nil
	}
	files, err := s.readReleaseLibraryFiles(ctx, animeID, releaseIDs)
	if err != nil || len(files) == 0 {
		return err
	}
	bySource := map[string]releaseLibraryFile{}
	byTarget := map[string]releaseLibraryFile{}
	endedBySource := map[string]releaseLibraryFile{}
	endedByTarget := map[string]releaseLibraryFile{}
	for _, file := range files {
		if file.ended() {
			endedBySource[file.ReleaseID+"\x00"+file.SourcePath] = file
			endedByTarget[file.ReleaseID+"\x00"+filepath.Clean(file.FrozenTarget)] = file
			continue
		}
		bySource[file.ReleaseID+"\x00"+file.SourcePath] = file
		byTarget[file.ReleaseID+"\x00"+filepath.Clean(file.FrozenTarget)] = file
	}
	for gi := range view.Groups {
		for ii := range view.Groups[gi].Items {
			item := &view.Groups[gi].Items[ii]
			releaseID, ok := strings.CutPrefix(item.SnapshotID, "release:")
			if !ok {
				continue
			}
			for fi := range item.Files {
				file := &item.Files[fi]
				if _, held := matchLibraryFile(bySource, byTarget, releaseID, file.SourcePath, file.RenderedTarget); held {
					markHeldInLibrary(item, file)
					continue
				}
				// The file is in no library: if the placement it produced has
				// ended, the row states that file's own outcome instead of
				// reading "waiting to import".
				if ended, ok := matchLibraryFile(endedBySource, endedByTarget, releaseID, file.SourcePath, file.RenderedTarget); ok {
					file.PlacementEnd = &RSSListPlacementEnd{State: ended.State, Cause: ended.Cause}
				}
			}
		}
	}
	return nil
}

// markHeldInLibrary records that an active placement (or companion) holds
// this file. A file the overlap compared and found not held flips to held and
// moves from pending to present; a file the overlap did not compare (outside
// the write set, or an undetermined seed) is held as a fact too, but the
// seed's counts are left as the overlap decided them.
func markHeldInLibrary(item *RSSListItem, file *RSSListFile) {
	if file.InLibrary != nil && *file.InLibrary {
		return
	}
	compared := file.InLibrary != nil
	held := true
	file.InLibrary = &held
	overlap := &item.LibraryOverlap
	if !compared || overlap.Verdict == RSSListLibraryOverlapUndetermined || overlap.PresentFiles == nil || overlap.PendingFiles == nil {
		return
	}
	present := *overlap.PresentFiles + 1
	pending := max(*overlap.PendingFiles-1, 0)
	overlap.PresentFiles = &present
	overlap.PendingFiles = &pending
	overlap.Verdict = RSSListLibraryOverlapPresent
}

// readReleaseLibraryFiles lists the files of these releases that a placement
// produced by their file operation now holds (an active verified placement for
// a video, an active companion of one for a subtitle), and the placements that
// have ended since (superseded, displaced or released).
func (s *Service) readReleaseLibraryFiles(ctx context.Context, animeID string, releaseIDs []string) ([]releaseLibraryFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.source_release_ref, mf.relative_path, fo.target_path,
       COALESCE(c.id, ''),
       COALESCE(p.state, ''), COALESCE(p.displaced_cause, '')
FROM file_operations fo
JOIN acquisitions a ON a.id = fo.acquisition_id
JOIN acquisition_files mf ON mf.acquisition_id = fo.acquisition_id AND mf.file_ref = fo.file_ref
LEFT JOIN placements p ON p.source_evidence_type = 'file_operation' AND p.source_owner_ref = fo.id
    AND p.managed_anime_id = $2
    AND ((p.state = 'active' AND p.verification_state = 'verified')
        OR p.state IN ('superseded', 'displaced', 'released'))
LEFT JOIN placement_companions c ON c.source_ref = fo.id AND c.managed_anime_id = $2 AND c.state = 'active'
    AND EXISTS (SELECT 1 FROM placements cp WHERE cp.id = c.placement_id AND cp.state = 'active' AND cp.verification_state = 'verified')
WHERE a.source_release_ref = ANY($1::text[]) AND (p.id IS NOT NULL OR c.id IS NOT NULL)
ORDER BY fo.created_at, fo.id`, pq.Array(releaseIDs), animeID)
	if err != nil {
		return nil, fmt.Errorf("read release library files: %w", err)
	}
	defer rows.Close()
	files := []releaseLibraryFile{}
	for rows.Next() {
		var file releaseLibraryFile
		if err := rows.Scan(&file.ReleaseID, &file.SourcePath, &file.FrozenTarget, &file.CompanionID,
			&file.State, &file.Cause); err != nil {
			return nil, fmt.Errorf("scan release library file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}
