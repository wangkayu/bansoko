package filemaintenance

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// seedPathDiffRows seeds one anime with a completed calibration generation of
// three observed files, each with the durable evidence its placement render
// needs: two placed files whose completed file operations froze the naming
// rules and the episode identity, and one unrecognized file without coverage.
//
// The first row's current path is exactly the path its frozen evidence
// renders; the second row's current path is not.
func seedPathDiffRows(t *testing.T, db *sql.DB, unclassifiedRoute string) {
	t.Helper()
	seedPathDiffRowsWithRules(t, db, unclassifiedRoute, frozenRulesForTest())
}

// seedPathDiffRowsWithRules is seedPathDiffRows with a caller-chosen frozen
// naming rule set, so a test can freeze rules that no longer render.
func seedPathDiffRowsWithRules(t *testing.T, db *sql.DB, unclassifiedRoute string, rules naming.NamingRules) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-diff', 'Diff Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season)
VALUES ('anime-diff', 'tmdb-1', 'tv', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs
(managed_anime_id, unclassified_route, file_effect_source_default_version, file_effect_version, updated_at)
VALUES ('anime-diff', $1, 0, 1, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET unclassified_route = EXCLUDED.unclassified_route, file_effect_version = EXCLUDED.file_effect_version, updated_at = CURRENT_TIMESTAMP`, unclassifiedRoute); err != nil {
		t.Fatal(err)
	}
	matching := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "diff-e1", AnimeID: "anime-diff", AnimeTitle: "Diff Anime",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Diff Anime - 01", PlacedPath: "/library/Diff Anime/Season 01/Diff Anime - 1 [Group]",
		DownloadName: "Diff Anime - 01.mkv", Rules: rules, FrozenEpisode: "1",
		Coverage: []string{"tv:e1"},
	})
	drifted := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "diff-e2", AnimeID: "anime-diff", AnimeTitle: "Diff Anime",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Diff Anime - 02", PlacedPath: "/library/Diff Anime/Season 01/Diff Anime - 02.mkv",
		DownloadName: "Diff Anime - 02.mkv", Rules: rules, FrozenEpisode: "2",
		Coverage: []string{"tv:e2"},
	})
	type seedRow struct {
		filePath, disposition, fingerprint, coverage, placement string
	}
	rows := []seedRow{
		{"/library/Diff Anime/Season 01/Diff Anime - 1 [Group]", "present", "fp-1", `["tv:e1"]`, matching.PlacementID},
		{"/library/Diff Anime/Season 01/Diff Anime - 02.mkv", "present", "fp-2", `["tv:e2"]`, drifted.PlacementID},
		{"/library/Diff Anime/Season 01/OVA.mkv", "unresolved", "fp-3", `[]`, ""},
	}
	for _, row := range rows {
		facts := `{}`
		if row.placement != "" {
			facts = fmt.Sprintf(`{"placement_id":%q}`, row.placement)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ('anime-diff', 1, $1, 0, $2, 1024, $3::jsonb, CURRENT_TIMESTAMP,
        $4, $5::jsonb, 'v1', $6, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			row.filePath, row.fingerprint, facts, row.disposition, row.coverage, "ff-"+row.fingerprint); err != nil {
			t.Fatalf("seed reconciliation file row: %v", err)
		}
	}
}

// capturingLogger records error events for assertion; it implements the
// observability runtime logger port.
type capturingLogger struct {
	errors []observabilityport.RuntimeEvent
}

func (c *capturingLogger) Debug(context.Context, observabilityport.RuntimeEvent) {}
func (c *capturingLogger) Info(context.Context, observabilityport.RuntimeEvent)  {}
func (c *capturingLogger) Warn(context.Context, observabilityport.RuntimeEvent)  {}
func (c *capturingLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	c.errors = append(c.errors, event)
}

func TestPathDiffRenderFactsUnavailableFallsBackAndLogs(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	logger := &capturingLogger{}
	seedPathDiffRows(t, db, "skip")
	// Regression: a placement admitted by the reconciliation index projection
	// has no completed file-operation lineage, so its target cannot render. The
	// preview used to abort the whole array on that row, which left the UI's
	// confirmation dialog without any path-diff and made enabling alignment
	// impossible; the row must degrade like every other render failure.
	ctx := context.Background()
	const indexedPlacement = "plc-indexed"
	const indexedPath = "/library/Diff Anime/Season 01/Indexed.mkv"
	if _, err := db.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, replacement_eligible, version, created_at, updated_at)
VALUES ($1, $2, 'anime-diff', 'reconciliation', 'anime-diff:1:' || $2, '{}'::jsonb, '{}'::jsonb, 'rel-e2e-2indexed', 'facts-v1', 'verified', 'active', true, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		indexedPlacement, indexedPath); err != nil {
		t.Fatalf("seed indexed placement: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ('anime-diff', 1, $1, 0, 'fp-indexed', 1024, ('{"placement_id":"' || $2 || '"}')::jsonb, CURRENT_TIMESTAMP,
        'present', '["tv:e3"]'::jsonb, 'v1', 'ff-indexed', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		indexedPath, indexedPlacement); err != nil {
		t.Fatalf("seed indexed reconciliation file row: %v", err)
	}

	service := NewPathDiffService(db, NewNamingTargetRenderer(db, fakeRulesSource{}), logger)
	views, err := service.ListPathDiffs(ctx, "anime-diff", 1, 100, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	var indexed *PathDiffView
	for i := range views {
		if views[i].FilePath == indexedPath {
			indexed = &views[i]
		}
	}
	if indexed == nil {
		t.Fatalf("indexed row missing from %d views", len(views))
	}
	if indexed.ExpectedPath == nil || *indexed.ExpectedPath != indexedPath {
		t.Fatalf("indexed expected=%v, want the actual-path fallback", indexed.ExpectedPath)
	}
	if indexed.Differs {
		t.Fatal("indexed row differs=true, want the no-false-diff fallback")
	}
	if len(logger.errors) != 1 || logger.errors[0].ReasonCode != "path_diff_render_failed" {
		t.Fatalf("error events=%+v, want one path_diff_render_failed event", logger.errors)
	}
}

func TestPathDiffRenderFailureFallsBackAndLogs(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	logger := &capturingLogger{}
	// Current rules that do not compile make every
	// calibrated render fail; the preview must keep the actual path as
	// expected (no false diff) AND surface the failure through the logger
	// instead of silently hiding it.
	broken := naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "${anime.title_base",
			VideoFilenameTemplate: "${file.ext}",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "${anime.title_base}",
			VideoFilenameTemplate: "${file.ext}",
		},
	}
	seedPathDiffRowsWithRules(t, db, "skip", broken)
	service := NewPathDiffService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: broken}), logger)
	views, err := service.ListPathDiffs(context.Background(), "anime-diff", 1, 100, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	// Both tv rows render-fail and fall back to their actual path; the
	// unresolved row is not a render candidate.
	for _, view := range views {
		if view.Disposition == "unresolved" {
			continue
		}
		if view.ExpectedPath == nil || *view.ExpectedPath != view.FilePath {
			t.Fatalf("row %q expected=%v, want actual-path fallback", view.FilePath, view.ExpectedPath)
		}
		if view.Differs {
			t.Fatalf("row %q differs=true, want false fallback", view.FilePath)
		}
	}
	if len(logger.errors) != 2 {
		t.Fatalf("error events=%d, want 2 render failures", len(logger.errors))
	}
	event := logger.errors[0]
	if event.ReasonCode != "path_diff_render_failed" || event.OwnerID != "anime-diff" || event.Operation != "path_diff_render" {
		t.Fatalf("event=%+v, want path_diff_render_failed for anime-diff", event)
	}
}

// TestPathDiffRendersPlacementMovedAfterCalibration is the regression for the
// read that showed a file the alignment had just placed at its target as
// unscanned. The calibration row keeps the path observed before the move while
// the placement already sits at the rendered target; the placement is still the
// same frozen lineage, so the preview renders its target from the placement id
// instead of degrading to the actual path.
func TestPathDiffRendersPlacementMovedAfterCalibration(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	logger := &capturingLogger{}
	ctx := context.Background()
	const (
		animeID  = "anime-moved"
		observed = "/library/Moved Anime/Moved Anime - 03.mkv"
		target   = "/library/Moved Anime/Season 01/Moved Anime - 3 [Group]"
	)
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "moved-e3", AnimeID: animeID, AnimeTitle: "Moved Anime",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Moved Anime - 03", PlacedPath: target,
		DownloadName: "Moved Anime - 03.mkv", Rules: frozenRulesForTest(), FrozenEpisode: "3",
		Coverage: []string{"tv:e3"},
	})
	if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ($1, 1, $2, 0, 'fp-moved', 1024, jsonb_build_object('placement_id', $3::text), CURRENT_TIMESTAMP,
        'present', '["tv:e3"]'::jsonb, 'v1', 'ff-moved', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		animeID, observed, chain.PlacementID); err != nil {
		t.Fatalf("seed moved reconciliation row: %v", err)
	}

	service := NewPathDiffService(db, NewNamingTargetRenderer(db, fakeRulesSource{}), logger)
	views, err := service.ListPathDiffs(ctx, animeID, 1, 100, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v, want the one moved row", views)
	}
	view := views[0]
	if view.TargetUnavailable != "" {
		t.Fatalf("the moved placement must render, got target_unavailable=%q", view.TargetUnavailable)
	}
	if view.ExpectedPath == nil || *view.ExpectedPath != target {
		t.Fatalf("expected=%v, want the rendered target %q of the pre-move observation", view.ExpectedPath, target)
	}
	if !view.Differs {
		t.Fatal("the pre-move observation differs from the placement's target")
	}
	if len(logger.errors) != 0 {
		t.Fatalf("error events=%+v, want none: the placement id alone identifies the lineage", logger.errors)
	}
}
