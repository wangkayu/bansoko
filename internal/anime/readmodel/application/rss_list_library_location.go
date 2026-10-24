package application

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lib/pq"

	"bansoko/internal/library/displaypath"
)

// PlacementTargetRenderer renders, with the anime's current naming rules, the
// target each active placement (keyed by placement id) and each of its active
// companions (keyed by companion id) would be aligned to. It is the file
// maintenance renderer the per-anime files dialog uses; an entry it cannot
// render is absent.
type PlacementTargetRenderer interface {
	RenderPlacementTargets(ctx context.Context, animeID string, placementIDs []string) (map[string]string, error)
}

// releaseLibraryFile is one imported file of an owner release: the file
// operation that placed it and the placement (video) or companion
// (subtitle) that holds it now.
type releaseLibraryFile struct {
	ReleaseID    string
	SourcePath   string
	FrozenTarget string
	// PlacementID is the video placement, or the placement owning the
	// companion.
	PlacementID string
	CompanionID string
	CurrentPath string
}

// applyLibraryLocations shows where each imported file of the owner view is
// now. A file an active verified placement or
// active companion holds gets LibraryPath, its current path relative to the
// library (displaypath root of the anime's files); when the anime's current
// naming rules render another path for it, LibraryNewPath is that path, from
// the files dialog's renderer. Files not in the library keep their rendered
// target only.
//
// A file it places in the library also reads InLibrary true, wherever the
// file sits now: the overlap compares the frozen rendered target with
// placement paths, which an alignment move (or a companion, which is no
// placement path at all) leaves behind, so the owner row would otherwise read
// pending-import for a file it shows a library location for. The seed's
// overlap counts
// follow. It reads and writes nothing else.
func (s *Service) applyLibraryLocations(ctx context.Context, animeID string, view *RSSListView) error {
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
	held, err := s.readReleaseLibraryFiles(ctx, animeID, releaseIDs)
	if err != nil || len(held) == 0 {
		return err
	}
	bySource := map[string]releaseLibraryFile{}
	byTarget := map[string]releaseLibraryFile{}
	placementIDs := []string{}
	seenPlacement := map[string]bool{}
	for _, file := range held {
		bySource[file.ReleaseID+"\x00"+file.SourcePath] = file
		byTarget[file.ReleaseID+"\x00"+filepath.Clean(file.FrozenTarget)] = file
		placementID := file.PlacementID
		if placementID != "" && !seenPlacement[placementID] {
			seenPlacement[placementID] = true
			placementIDs = append(placementIDs, placementID)
		}
	}
	var targets map[string]string
	if s.placementTargets != nil && len(placementIDs) > 0 {
		// A failed render only drops the new-path line; the current location
		// is a recorded fact and stays.
		if rendered, renderErr := s.placementTargets.RenderPlacementTargets(ctx, animeID, placementIDs); renderErr == nil {
			targets = rendered
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	type match struct {
		item   *RSSListItem
		file   *RSSListFile
		held   releaseLibraryFile
		target string
	}
	matches := []match{}
	libraryPaths, err := animeLibraryPaths(ctx, s.db, animeID)
	if err != nil {
		return err
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
				found, ok := bySource[releaseID+"\x00"+file.SourcePath]
				if !ok && strings.TrimSpace(file.RenderedTarget) != "" {
					found, ok = byTarget[releaseID+"\x00"+filepath.Clean(file.RenderedTarget)]
				}
				if !ok {
					continue
				}
				key := found.PlacementID
				if found.CompanionID != "" {
					key = found.CompanionID
				}
				target := ""
				if rendered, ok := targets[key]; ok && filepath.Clean(rendered) != filepath.Clean(found.CurrentPath) {
					target = rendered
					libraryPaths = append(libraryPaths, rendered)
				}
				libraryPaths = append(libraryPaths, found.CurrentPath)
				matches = append(matches, match{item: item, file: file, held: found, target: target})
			}
		}
	}
	root := displaypath.Root(libraryPaths)
	for _, m := range matches {
		current := displaypath.Relative(root, m.held.CurrentPath)
		m.file.LibraryPath = &current
		if m.target != "" {
			next := displaypath.Relative(root, m.target)
			m.file.LibraryNewPath = &next
		}
		markHeldInLibrary(m.item, m.file)
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

// readReleaseLibraryFiles lists the files of these releases that an active
// verified placement of the anime (video) or an active companion of one
// (subtitle) holds now, through the file operation that placed them.
func (s *Service) readReleaseLibraryFiles(ctx context.Context, animeID string, releaseIDs []string) ([]releaseLibraryFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.source_release_ref, mf.relative_path, fo.target_path,
       COALESCE(p.id, c.placement_id), COALESCE(c.id, ''), COALESCE(p.physical_path, c.current_path)
FROM file_operations fo
JOIN acquisitions a ON a.id = fo.acquisition_id
JOIN acquisition_files mf ON mf.acquisition_id = fo.acquisition_id AND mf.file_ref = fo.file_ref
LEFT JOIN placements p ON p.source_evidence_type = 'file_operation' AND p.source_owner_ref = fo.id
    AND p.managed_anime_id = $2 AND p.state = 'active' AND p.verification_state = 'verified'
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
		if err := rows.Scan(&file.ReleaseID, &file.SourcePath, &file.FrozenTarget, &file.PlacementID, &file.CompanionID, &file.CurrentPath); err != nil {
			return nil, fmt.Errorf("scan release library file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}
