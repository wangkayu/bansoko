package filemaintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lib/pq"
)

// RenderPlacementTargets renders, with the anime's current naming rules, the
// target each given active placement would be aligned to, and the target of
// each of its active companions: the RSS row of an imported file shows the new
// path when the current template renders a different one. It is the files
// dialog's render without a scan: the same renderer (RenderCalibratedTarget)
// over the same facts a scan observation carries for the placement — its
// coverage episode and the release's latest
// candidate facts (resolution, language, group) — and the same companion
// rule. Keys are placement ids and companion ids; a placement without an
// episode identity or whose render fails is absent (never guessed), and so
// are its companions. Paths are absolute.
func (r *NamingTargetRenderer) RenderPlacementTargets(ctx context.Context, animeID string, placementIDs []string) (map[string]string, error) {
	targets := map[string]string{}
	if r == nil || r.db == nil {
		return nil, errors.New("naming target renderer is not wired")
	}
	if strings.TrimSpace(animeID) == "" || len(placementIDs) == 0 {
		return targets, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT p.id, p.physical_path, COALESCE(fo.observed_target_size_bytes, fo.expected_size_bytes, 0),
       COALESCE((SELECT array_agg(pc.observation_key ORDER BY pc.coverage_ordinal) FROM placement_coverage pc WHERE pc.placement_id = p.id), '{}'),
       COALESCE(f.resolution, ''), COALESCE(f.language, ''), COALESCE(f."group", '')
FROM placements p
LEFT JOIN file_operations fo ON fo.id = p.source_owner_ref AND p.source_evidence_type = 'file_operation'
LEFT JOIN release_candidate_facts f ON f.release_id = p.quality_facts_ref
  AND f.release_version = (SELECT MAX(f2.release_version) FROM release_candidate_facts f2 WHERE f2.release_id = f.release_id)
WHERE p.managed_anime_id = $1 AND p.id = ANY($2::text[]) AND p.state = 'active' AND p.verification_state = 'verified'
ORDER BY p.id`, animeID, pq.Array(placementIDs))
	if err != nil {
		return nil, fmt.Errorf("read placements to render: %w", err)
	}
	type placementFacts struct {
		id, path                    string
		size                        int64
		coverage                    []string
		resolution, language, group string
	}
	var placements []placementFacts
	for rows.Next() {
		var p placementFacts
		if err := rows.Scan(&p.id, &p.path, &p.size, pq.Array(&p.coverage), &p.resolution, &p.language, &p.group); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan placement to render: %w", err)
		}
		placements = append(placements, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rendered := []string{}
	for _, p := range placements {
		episode := episodeFromCoverage(p.coverage)
		if episode == "" {
			continue
		}
		target, err := r.RenderCalibratedTarget(ctx, animeID, p.path, p.size, CalibratedRenderFacts{
			EpisodeDisplay: episode, Resolution: p.resolution, Language: p.language, PlacementID: p.id,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			continue
		}
		targets[p.id] = filepath.Clean(target)
		rendered = append(rendered, p.id)
	}
	if len(rendered) == 0 {
		return targets, nil
	}
	companions, err := r.db.QueryContext(ctx, `
SELECT placement_id, id, suffix, original_extension FROM placement_companions
WHERE placement_id = ANY($1::text[]) AND state = 'active' ORDER BY placement_id, id`, pq.Array(rendered))
	if err != nil {
		return nil, fmt.Errorf("read companions to render: %w", err)
	}
	defer companions.Close()
	for companions.Next() {
		var placementID, id, suffix, extension string
		if err := companions.Scan(&placementID, &id, &suffix, &extension); err != nil {
			return nil, fmt.Errorf("scan companion to render: %w", err)
		}
		if video, ok := targets[placementID]; ok && extension != "" {
			targets[id] = CompanionPath(video, suffix, extension)
		}
	}
	return targets, companions.Err()
}
