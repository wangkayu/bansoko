package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/postprocessing/naming"
)

// PathDiffService renders the expected target path of every calibration row
// of one reconciliation generation and compares it with the actual file path
// (side-effect free preview). Facts come from the frozen calibration rows;
// the renderer never re-parses filenames. Rendered targets are absolute by
// construction, so the comparison is absolute-vs-absolute without a root.
type PathDiffService struct {
	db       *sql.DB
	renderer *NamingTargetRenderer
	logger   observabilityport.RuntimeLogger
}

// NewPathDiffService wires the preview owner. A nil logger is replaced with
// the no-op sink so optional wiring never panics.
func NewPathDiffService(db *sql.DB, renderer *NamingTargetRenderer, logger observabilityport.RuntimeLogger) *PathDiffService {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &PathDiffService{db: db, renderer: renderer, logger: logger}
}

var coverageEpisodePattern = regexp.MustCompile(`^tv:s(\d+):e(.+)$`)

// ListPathDiffs returns the calibration rows of one generation (0 = latest)
// with their rendered expected target and the differs flag.
func (s *PathDiffService) ListPathDiffs(ctx context.Context, animeID string, generation int64, limit, offset int) ([]PathDiffView, error) {
	if s.renderer == nil {
		return nil, fmt.Errorf("path diff renderer is not wired")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	effectiveGeneration, err := s.resolveGeneration(ctx, animeID, generation)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT file_path, COALESCE(disposition, ''), COALESCE(coverage, '[]'::jsonb)::text,
	       COALESCE(resolution, ''), COALESCE(language, ''), COALESCE("group", ''), size_bytes, COALESCE(facts->>'placement_id',''),
	       COALESCE(facts->'companions', '[]'::jsonb)::text
	FROM reconciliation_files
	WHERE managed_anime_id = $1 AND generation = $2 AND disposition IS NOT NULL
	ORDER BY file_path LIMIT $3 OFFSET $4`, animeID, effectiveGeneration, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("read calibration rows: %w", err)
	}
	defer rows.Close()

	views := make([]PathDiffView, 0, limit)
	for rows.Next() {
		var filePath, disposition, coverageRaw, resolution, language, group, placementID, companionsRaw string
		var sizeBytes int64
		if err := rows.Scan(&filePath, &disposition, &coverageRaw, &resolution, &language, &group, &sizeBytes, &placementID, &companionsRaw); err != nil {
			return nil, fmt.Errorf("scan calibration row: %w", err)
		}
		view := PathDiffView{
			FilePath:     filePath,
			Disposition:  disposition,
			ExpectedPath: &filePath,
			Differs:      false,
			PlacementID:  placementID,
			SizeBytes:    sizeBytes,
		}
		coverage, _ := parseCoverageList(coverageRaw)
		episode := episodeFromCoverage(coverage)
		if episode == "" {
			// Unrecognized content has no expected target: alignment never
			// moves a file without an episode identity.
			view.ExpectedPath = nil
			view.TargetUnavailable = "coverage_unresolved"
			views = append(views, withCompanionDiffs(view, companionsRaw))
			continue
		}
		expected, err := s.renderer.RenderCalibratedTarget(ctx, animeID, filePath, sizeBytes, CalibratedRenderFacts{
			EpisodeDisplay: episode,
			Resolution:     resolution,
			Language:       language,
			PlacementID:    placementID,
		})
		if err != nil {
			// Every render failure degrades that row instead of failing the
			// whole preview: the response is the UI's only view of what
			// alignment would move, and one row without frozen acquisition
			// evidence (a placement admitted by the reconciliation index
			// projection, for example) must not make the surface unusable.
			s.logger.Error(ctx, observabilityport.RuntimeEvent{Message: "path diff render failed, keeping the actual path", Component: "filemaintenance", Operation: "path_diff_render", Outcome: "failure", ReasonCode: "path_diff_render_failed", OwnerID: animeID, ErrorClass: "render"})
			view.ExpectedPath = &filePath
			view.Differs = false
			view.TargetUnavailable = "target_render_failed"
			if errors.Is(err, naming.ErrRenderFactsUnavailable) {
				view.TargetUnavailable = "render_facts_unavailable"
			}
			views = append(views, withCompanionDiffs(view, companionsRaw))
			continue
		}
		expected = filepath.Clean(expected)
		view.ExpectedPath = &expected
		view.Differs = !samePath(expected, filePath)
		views = append(views, withCompanionDiffs(view, companionsRaw))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return views, nil
}

// observedCompanion is one companion fact recorded by the scan
// (reconciliation scanhandler companionObservation).
type observedCompanion struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	MediaKind string `json:"media_kind"`
	Suffix    string `json:"suffix"`
	Extension string `json:"extension"`
	State     string `json:"state"`
}

// withCompanionDiffs attaches the generation's observed companions to a video
// row. A companion's expected path derives from the video's expected path;
// when the video has no expected path the companion has none either and
// follows the video's differs flag.
func withCompanionDiffs(view PathDiffView, raw string) PathDiffView {
	var observed []observedCompanion
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &observed) != nil || len(observed) == 0 {
		return view
	}
	view.Companions = make([]CompanionPathDiffView, 0, len(observed))
	for _, companion := range observed {
		diff := CompanionPathDiffView{ID: companion.ID, FilePath: companion.Path, MediaKind: companion.MediaKind, State: companion.State, Differs: view.Differs}
		if view.ExpectedPath != nil && companion.Extension != "" {
			expected := CompanionPath(*view.ExpectedPath, companion.Suffix, companion.Extension)
			diff.ExpectedPath = &expected
			diff.Differs = !samePath(expected, companion.Path)
		}
		view.Companions = append(view.Companions, diff)
	}
	return view
}

// CompanionIssues lists the companions of the latest generation that are
// missing, have a different size, or are not at their expected path.
func (s *PathDiffService) CompanionIssues(ctx context.Context, animeID string) ([]CompanionIssueView, error) {
	views, err := s.ListPathDiffs(ctx, animeID, 0, 500, 0)
	if err != nil {
		return nil, err
	}
	issues := []CompanionIssueView{}
	for _, view := range views {
		for _, companion := range view.Companions {
			reason := ""
			switch {
			case companion.State == "missing":
				reason = "companion_missing"
			case companion.State == "size_mismatch":
				reason = "companion_size_mismatch"
			case companion.Differs:
				reason = "companion_not_aligned"
			default:
				continue
			}
			issues = append(issues, CompanionIssueView{
				ExpectedPath: companion.ExpectedPath, FilePath: companion.FilePath, MediaKind: companion.MediaKind,
				ReasonCode: reason, VideoPath: view.FilePath,
			})
			if len(issues) >= 200 {
				return issues, nil
			}
		}
	}
	return issues, nil
}

func (s *PathDiffService) resolveGeneration(ctx context.Context, animeID string, generation int64) (int64, error) {
	if generation > 0 {
		return generation, nil
	}
	var latest int64
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(generation), 0) FROM reconciliation_files WHERE managed_anime_id = $1 AND disposition IS NOT NULL`, animeID).Scan(&latest)
	if err != nil {
		return 0, fmt.Errorf("resolve latest calibration generation: %w", err)
	}
	return latest, nil
}

func parseCoverageList(raw string) ([]string, error) {
	var coverage []string
	if strings.TrimSpace(raw) == "" || raw == "null" {
		return coverage, nil
	}
	if err := json.Unmarshal([]byte(raw), &coverage); err != nil {
		return nil, err
	}
	return coverage, nil
}

// episodeFromCoverage extracts the canonical episode display from the first
// tv coverage key (tv:s2:e5 -> "5"); movie/empty coverage returns "".
func episodeFromCoverage(coverage []string) string {
	for _, key := range coverage {
		if match := coverageEpisodePattern.FindStringSubmatch(key); match != nil {
			return match[2]
		}
	}
	return ""
}

func samePath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}
