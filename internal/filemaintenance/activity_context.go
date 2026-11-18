package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/observability/businesslog"
)

// releaseRunContextTx reads the run context of one Release for a run-log entry
// written by a placement or replacement step: the subgroup display name, the
// torrent title and the episodes of the newest frozen batch plan. The same
// facts are frozen by the acquisition consumer on the other steps of the run,
// so the log can show the download as one row. An empty release id or a
// missing Release yields an empty context (the entry stays a single row).
func releaseRunContextTx(ctx context.Context, tx *sql.Tx, releaseID string) (map[string]any, error) {
	values, _, err := releaseRunContextFoundTx(ctx, tx, releaseID)
	return values, err
}

// releaseRunContextFoundTx is releaseRunContextTx that also reports whether
// the Release exists, so a writer only links an entry to a real Release.
func releaseRunContextFoundTx(ctx context.Context, tx *sql.Tx, releaseID string) (map[string]any, bool, error) {
	if releaseID == "" {
		return map[string]any{}, false, nil
	}
	var groupName, releaseTitle string
	var coverageKeys sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(btrim(src.group_name), ''), COALESCE(btrim(r.raw_facts->>'title'), ''),
       (SELECT (snap.release_facts->'batch_plan'->'coverage_keys')::text
        FROM media_fact_snapshots snap
        WHERE snap.release_id = r.id
        ORDER BY snap.created_at DESC, snap.id DESC
        LIMIT 1)
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE r.id = $1`, releaseID).Scan(&groupName, &releaseTitle, &coverageKeys)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read release run context: %w", err)
	}
	return businesslog.RunContext(groupName, releaseTitle, coverageKeys), true, nil
}

// replacementActivityDetailsTx is the run-log details of one key decision:
// the run context of the candidate Release plus the actual result — which
// versions the candidate replaces, which existing version is kept, or how
// many versions of the episode remain. It also returns the Release id to link
// the entry to (empty when the candidate is not a stored Release).
func replacementActivityDetailsTx(ctx context.Context, tx *sql.Tx, animeID, releaseID, key, verdict string, pools []PoolEvidence, candidatePlacementID string) (json.RawMessage, string, error) {
	values, found, err := releaseRunContextFoundTx(ctx, tx, releaseID)
	if err != nil {
		return nil, "", err
	}
	if !found {
		releaseID = ""
	}
	result := replacementResultOf(verdict, pools, candidatePlacementID)
	step := map[string]any{}
	if len(result.replaced) > 0 {
		names, err := placementFileNamesTx(ctx, tx, result.replaced)
		if err != nil {
			return nil, "", err
		}
		step["replaced_files"] = names
		targets, err := placementTargetsTx(ctx, tx, result.replaced)
		if err != nil {
			return nil, "", err
		}
		step["replaced_targets"] = targets
	}
	if result.kept != "" {
		names, err := placementFileNamesTx(ctx, tx, []string{result.kept})
		if err != nil {
			return nil, "", err
		}
		if len(names) == 1 {
			step["kept_file"] = names[0]
		}
		targets, err := placementTargetsTx(ctx, tx, []string{result.kept})
		if err != nil {
			return nil, "", err
		}
		if len(targets) == 1 {
			step["kept_target"] = targets[0]
		}
	}
	if result.countVersions && key != unresolvedCoverageKey {
		var count int64
		if err := tx.QueryRowContext(ctx, `
SELECT count(DISTINCT p.id)
FROM placements p
JOIN placement_coverage pc ON pc.placement_id = p.id
WHERE p.managed_anime_id = $1 AND p.state = 'active' AND pc.observation_key = $2`, animeID, key).Scan(&count); err != nil {
			return nil, "", fmt.Errorf("count episode versions: %w", err)
		}
		step["version_count"] = count
	}
	details, err := runDetails(values, step)
	if err != nil {
		return nil, "", fmt.Errorf("encode replacement activity details: %w", err)
	}
	return details, releaseID, nil
}

// replacementResult is what one key decision actually did, as placement ids.
type replacementResult struct {
	// replaced are the versions the candidate replaces (verdict replace).
	replaced []string
	// kept is the existing version that beat the candidate (keep/retain).
	kept string
	// countVersions asks for the number of versions kept for the episode:
	// nothing was replaced and the candidate is already placed.
	countVersions bool
}

// replacementResultOf projects the frozen pool evidence of one decision onto
// its result. Losers are deduplicated in pool order; the candidate itself is
// never reported as replaced or kept.
func replacementResultOf(verdict string, pools []PoolEvidence, candidatePlacementID string) replacementResult {
	var result replacementResult
	switch verdict {
	case "replace":
		seen := map[string]bool{}
		for _, pool := range pools {
			if pool.Verdict != "replace" {
				continue
			}
			for _, loser := range pool.Losers {
				if loser.PlacementID == "" || loser.PlacementID == candidatePlacementID || seen[loser.PlacementID] {
					continue
				}
				seen[loser.PlacementID] = true
				result.replaced = append(result.replaced, loser.PlacementID)
			}
		}
	case "keep", "retain":
		for _, pool := range pools {
			if pool.Winner != nil && pool.Winner.PlacementID != "" && pool.Winner.PlacementID != candidatePlacementID {
				result.kept = pool.Winner.PlacementID
				break
			}
		}
		result.countVersions = result.kept == "" && candidatePlacementID != ""
	case "queue":
		result.countVersions = candidatePlacementID != ""
	}
	return result
}

// runDetails merges step facts into a run context and encodes the details.
func runDetails(values map[string]any, step map[string]any) (json.RawMessage, error) {
	for key, value := range step {
		values[key] = value
	}
	return businesslog.MarshalDetails(values)
}

// placementFileNamesTx returns the file names of the given placements, in
// the given order, skipping placements that no longer exist.
func placementFileNamesTx(ctx context.Context, tx *sql.Tx, ids []string) ([]string, error) {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		var path string
		err := tx.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, id).Scan(&path)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read placement path: %w", err)
		}
		if name := businesslog.FileName(path); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// placementSourceFacts is the frozen download source identity of one
// placement: the release title and group of its accepted acquisition input and
// the original file name inside its artifact manifest. Empty fields mean the
// source chain is gone or never existed; callers hide the line rather than
// borrowing a library path, a planned target or a live Release row.
type placementSourceFacts struct {
	Title string
	Group string
	File  string
}

// placementSourceFactsTx reads one placement's frozen source through the
// chain the placement already records: source_owner_ref names the file
// operation, whose acquisition input froze the release title and group and
// whose artifact file froze the torrent-relative original file name. Nothing
// is read from the current Release/RSS source rows. A placement admitted from
// reconciliation/recovery has no download source; an operation compacted by
// detail retention no longer resolves. Both yield zero facts and the caller
// hides the missing lines. The boolean reports whether the placement row
// exists.
func placementSourceFactsTx(ctx context.Context, tx *sql.Tx, id string) (placementSourceFacts, bool, error) {
	var evidenceType, ownerRef string
	err := tx.QueryRowContext(ctx, `SELECT source_evidence_type, source_owner_ref FROM placements WHERE id = $1`, id).Scan(&evidenceType, &ownerRef)
	if errors.Is(err, sql.ErrNoRows) {
		return placementSourceFacts{}, false, nil
	}
	if err != nil {
		return placementSourceFacts{}, false, fmt.Errorf("read placement source evidence: %w", err)
	}
	if evidenceType != "file_operation" || ownerRef == "" {
		return placementSourceFacts{}, true, nil
	}
	var title, group, file sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT CASE WHEN i.input->'source'->'title'->>'state' = 'known'
             AND btrim(i.input->'source'->'title'->>'value') <> ''
            THEN i.input->'source'->'title'->>'value' END,
       CASE WHEN i.input->'source'->'group'->>'state' = 'known'
             AND btrim(i.input->'source'->'group'->>'value') <> ''
            THEN i.input->'source'->'group'->>'value' END,
       NULLIF(regexp_replace(rtrim(af.relative_path, '/'), '^.*/', ''), '')
FROM public.file_operations o
LEFT JOIN public.acquisitions i ON i.id = o.acquisition_id
LEFT JOIN public.artifact_files af ON af.id = o.artifact_file_id
WHERE o.id = $1`, ownerRef).Scan(&title, &group, &file)
	if errors.Is(err, sql.ErrNoRows) {
		// The detail-expiry compaction deletes the operation (and its artifact
		// file) while the placement stays: no fact is invented for it.
		return placementSourceFacts{}, true, nil
	}
	if err != nil {
		return placementSourceFacts{}, true, fmt.Errorf("read placement source facts: %w", err)
	}
	return placementSourceFacts{Title: title.String, Group: group.String, File: file.String}, true, nil
}

// placementTargetsTx names the versions behind the given placements, in the
// given order: the file name plus, when the placement records the Release it
// came from, that Release's subgroup and title. A placement without a Release
// (an index projection, a recovered file) still yields its file name, so the
// entry never loses the target it is about.
func placementTargetsTx(ctx context.Context, tx *sql.Tx, ids []string) ([]map[string]any, error) {
	targets := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		var path, releaseID string
		err := tx.QueryRowContext(ctx, `SELECT physical_path, COALESCE(quality_facts_ref, '') FROM placements WHERE id = $1`, id).Scan(&path, &releaseID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read placement target: %w", err)
		}
		target := map[string]any{"file_name": businesslog.FileName(path)}
		context, err := releaseRunContextTx(ctx, tx, releaseID)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"group_name", "release_title"} {
			if value, ok := context[key]; ok {
				target[key] = value
			}
		}
		targets = append(targets, target)
	}
	return targets, nil
}
