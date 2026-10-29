package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/filemaintenance/planner"
	releasefacts "bansoko/internal/release/facts"
)

// ReconciliationSnapshotReader adapts the immutable reconciliation
// observations table into the planner/placement snapshot ports. Coverage comes
// from the immutable observation facts (or the frozen Placement coverage set);
// the reader never re-parses a filename. An observation written before coverage
// persistence has no provable coverage and is reported unresolved, so it stays
// visible and is excluded from automatic replacement instead of being
// re-derived from a title. The reader is read-only and never scans, hashes or
// mutates the library.
type ReconciliationSnapshotReader struct {
	db *sql.DB
}

// NewReconciliationSnapshotReader wires the read-only snapshot adapter.
func NewReconciliationSnapshotReader(db *sql.DB) *ReconciliationSnapshotReader {
	return &ReconciliationSnapshotReader{db: db}
}

// GenerationUsable reports whether the generation is the completed immutable
// scan generation for the anime.
func (r *ReconciliationSnapshotReader) GenerationUsable(ctx context.Context, animeID string, generation int64) (bool, error) {
	var state string
	var ownerGeneration int64
	err := r.db.QueryRowContext(ctx, `
SELECT state, generation
FROM reconciliation_scans
WHERE managed_anime_id = $1`, animeID).Scan(&state, &ownerGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read reconciliation scan owner: %w", err)
	}
	return state == "completed" && ownerGeneration == generation, nil
}

// ListObservations pages every immutable observation of one generation with
// computed coverage keys.
func (r *ReconciliationSnapshotReader) ListObservations(ctx context.Context, animeID string, generation int64) ([]planner.SnapshotObservation, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT file_path, fingerprint, size_bytes, facts
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND NOT derived_missing
ORDER BY file_path`, animeID, generation)
	if err != nil {
		return nil, fmt.Errorf("list reconciliation observations: %w", err)
	}
	defer rows.Close()
	movie, err := r.animeIsMovie(ctx, animeID)
	if err != nil {
		return nil, err
	}
	placementCoverage, err := r.placementCoverageByGeneration(ctx, animeID, generation)
	if err != nil {
		return nil, err
	}
	var observations []planner.SnapshotObservation
	for rows.Next() {
		var filePath, fingerprint string
		var size int64
		var facts []byte
		if err := rows.Scan(&filePath, &fingerprint, &size, &facts); err != nil {
			return nil, fmt.Errorf("scan reconciliation observation: %w", err)
		}
		placement := decodePlacementObservation(facts)
		quality := observationQuality(facts)
		observations = append(observations, planner.SnapshotObservation{
			AnimeID:     animeID,
			PlacementID: placement.PlacementID,
			FilePath:    filePath,
			Fingerprint: fingerprint,
			SizeBytes:   size,
			Coverage:    coverageForObservation(placementCoverageFor(placementCoverage, facts), movie, facts),
			Resolution:  quality.resolution,
			Group:       quality.group,
			Language:    quality.language,
		})
	}
	return observations, rows.Err()
}

// ReadObservation resolves one frozen observation by its planner ref
// ("<animeID>:<generation>:<filePath>").
func (r *ReconciliationSnapshotReader) ReadObservation(ctx context.Context, animeID string, generation int64, observationRef string) (*SnapshotObservation, bool, error) {
	prefix := fmt.Sprintf("%s:%d:", animeID, generation)
	if !strings.HasPrefix(observationRef, prefix) {
		return nil, false, nil
	}
	filePath := strings.TrimPrefix(observationRef, prefix)
	if strings.TrimSpace(filePath) == "" {
		return nil, false, nil
	}
	var fingerprint string
	var size int64
	var facts []byte
	err := r.db.QueryRowContext(ctx, `
SELECT fingerprint, size_bytes, facts
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND file_path = $3 AND NOT derived_missing`,
		animeID, generation, filePath).Scan(&fingerprint, &size, &facts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read reconciliation observation: %w", err)
	}
	movie, err := r.animeIsMovie(ctx, animeID)
	if err != nil {
		return nil, false, err
	}
	placement := decodePlacementObservation(facts)
	quality := observationQuality(facts)
	return &planner.SnapshotObservation{
		AnimeID:     animeID,
		PlacementID: placement.PlacementID,
		FilePath:    filePath,
		Fingerprint: fingerprint,
		SizeBytes:   size,
		Coverage:    coverageForObservation(r.placementCoverage(ctx, facts), movie, facts),
		Resolution:  quality.resolution,
		Group:       quality.group,
		Language:    quality.language,
	}, true, nil
}

// placementCoverageByGeneration loads the frozen placement coverage set of
// every Placement-owned observation of one generation in a single query,
// keyed by placement id. A missing key means the observation is legacy
// (full-library, no placement owner) and falls back to its persisted facts.
func (r *ReconciliationSnapshotReader) placementCoverageByGeneration(ctx context.Context, animeID string, generation int64) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT o.facts->>'placement_id', c.observation_key
FROM reconciliation_files o
JOIN placement_coverage c ON c.placement_id = o.facts->>'placement_id'
WHERE o.managed_anime_id = $1 AND o.generation = $2 AND NOT o.derived_missing AND (o.facts->>'placement_id') IS NOT NULL
ORDER BY c.placement_id, c.coverage_ordinal`, animeID, generation)
	if err != nil {
		return nil, fmt.Errorf("list placement coverage for generation: %w", err)
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var placementID, key string
		if err := rows.Scan(&placementID, &key); err != nil {
			return nil, fmt.Errorf("scan placement coverage: %w", err)
		}
		result[placementID] = append(result[placementID], key)
	}
	return result, rows.Err()
}

// placementCoverage loads the frozen placement coverage set of one
// Placement-owned observation; nil marks a legacy observation.
func (r *ReconciliationSnapshotReader) placementCoverage(ctx context.Context, facts []byte) []string {
	placementID := decodePlacementObservation(facts).PlacementID
	if strings.TrimSpace(placementID) == "" {
		return nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT observation_key FROM placement_coverage
WHERE placement_id = $1 ORDER BY coverage_ordinal`, placementID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	coverage := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil
		}
		coverage = append(coverage, key)
	}
	return coverage
}

// placementCoverageFor resolves one observation's frozen placement coverage
// from the preloaded generation map.
func placementCoverageFor(byPlacement map[string][]string, facts []byte) []string {
	placementID := decodePlacementObservation(facts).PlacementID
	if strings.TrimSpace(placementID) == "" {
		return nil
	}
	if coverage, ok := byPlacement[placementID]; ok {
		return coverage
	}
	return []string{}
}

// animeIsMovie resolves the owner media kind: a movie anime makes the
// observation key movie:0 with one unique episode slot.
func (r *ReconciliationSnapshotReader) animeIsMovie(ctx context.Context, animeID string) (bool, error) {
	var override sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT media_kind
FROM anime_metadata
WHERE managed_anime_id = $1`, animeID).Scan(&override)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read anime media kind: %w", err)
	}
	return ownerMediaKind(override.String) == "movie", nil
}

// ownerMediaKind is the owner media kind (anime_metadata.media_kind)
// with unset as tv, the same rule FrozenMediaBinding applies. The TMDB kind is
// never read: a metadata binding only locates the work.
func ownerMediaKind(override string) string {
	if override == "movie" {
		return "movie"
	}
	return "tv"
}

// coverageForObservation resolves canonical coverage keys for one frozen file.
// A Placement-owned observation takes its coverage from the frozen Placement
// coverage set the selected parser's per-file identity was admitted with, and
// that frozen set is authoritative: a metadata edit after submission must never
// re-classify an already-placed file. A legacy full-library observation carries
// coverage only when persisted scan facts carry it; there the metadata movie
// classification is the caller's non-parsing evidence. No observation is ever
// re-parsed from its filename: an observation with no provable coverage is
// reported unresolved, visible and excluded from automatic replacement.
func coverageForObservation(placementCoverage []string, isMovie bool, facts []byte) []string {
	if placementCoverage != nil {
		if len(placementCoverage) == 0 {
			return []string{releasefacts.CoverageUnresolved}
		}
		return placementCoverage
	}
	if isMovie {
		return []string{"movie:0"}
	}
	if len(facts) > 0 && string(facts) != "{}" && string(facts) != "null" {
		var persisted struct {
			Coverage           []string `json:"coverage"`
			CoverageUnresolved bool     `json:"coverage_unresolved"`
		}
		if err := json.Unmarshal(facts, &persisted); err == nil && len(persisted.Coverage) > 0 {
			if persisted.CoverageUnresolved {
				return []string{releasefacts.CoverageUnresolved}
			}
			return persisted.Coverage
		}
	}
	return []string{releasefacts.CoverageUnresolved}
}

// observationQuality extracts the frozen quality evidence from persisted scan
// facts (empty when absent).
func observationQuality(facts []byte) struct{ resolution, group, language string } {
	var out struct{ resolution, group, language string }
	if len(facts) == 0 || string(facts) == "{}" || string(facts) == "null" {
		return out
	}
	var value map[string]any
	if err := json.Unmarshal(facts, &value); err != nil {
		return out
	}
	if raw, ok := value["resolution"].(string); ok {
		out.resolution = raw
	}
	if raw, ok := value["group"].(string); ok {
		out.group = raw
	}
	if raw, ok := value["language"].(string); ok {
		out.language = raw
	}
	return out
}
