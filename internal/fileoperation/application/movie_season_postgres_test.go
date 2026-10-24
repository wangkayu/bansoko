package application

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMovieTiePlansOnlyStableMainVideo(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "second", "relative_path": "z-second.mkv", "exact_size": 14, "media_kind": "video"})(p)
		p["fixture_media_values"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "movie-1", "kind": "movie", "season": nil, "version": 1}, "reason_code": nil}
	})
	ctx := context.Background()
	// Same size with no hint: stable manifest order selects source.mkv, not error.
	if _, err := db.ExecContext(ctx, `INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state) VALUES('movie-second',$1,(SELECT acquisition_id FROM artifacts WHERE id=$1),'second','z-second.mkv',$2,'video',$3,$4,'{}','not_applicable')`, fixture.artifactID, fixture.sourcePath, fixture.size, fingerprintJSONForTest(fixture.fingerprint, fixture.size)); err != nil {
		t.Fatal(err)
	}
	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)
	var count int
	var fileID string
	if err := db.QueryRowContext(ctx, `SELECT count(*),min(artifact_file_id) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&count, &fileID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || fileID != fixture.fileID {
		t.Fatalf("movie tie main count=%d file=%s", count, fileID)
	}
	// The non-main video is an unknown file; under the default skip route it
	// is declared excluded, not failed.
	assertRequirement(t, db, fixture.artifactID, "second", "excluded", "unknown_skipped")
	assertRequirement(t, db, fixture.artifactID, "primary", "required", "file_rules_selected")
	if failed := failedDeliveryResults(t, db, fixture.artifactID); failed != 0 {
		t.Fatalf("skipping an unknown file must not fail the acquisition: %d failed results", failed)
	}
}

// The shipped default TV template names the season folder through `${sf}` as
// its own folder level: a present season 0 is a real season and renders
// Season 00, while a TV item with no owner season has no implicit default
// (naming-render-4): the empty `${sf}` segment refuses the render and the
// import fails naming the season folder (fail-closed; the repair is setting
// the season).
func TestPrepareSeasonZeroPresenceAndMissingProduceDistinctOutcomes(t *testing.T) {
	t.Run("present-zero", func(t *testing.T) {
		db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
			values := p["fixture_media_values"].(map[string]any)
			values["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tv-1", "kind": "tv", "season": 0, "version": 1}, "reason_code": nil}
			// Naming reads the owner season frozen into the Vars input.
			p["fixture_anime_season"] = 0
		})
		ctx := context.Background()
		service := newFixtureService(t, db, fixture)
		prepareFixture(t, db, service, fixture.artifactID)
		var target string
		if err := db.QueryRowContext(ctx, `SELECT target_path FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&target); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(filepath.Dir(target)) != "Season 00" || !strings.HasPrefix(filepath.Base(target), "Fixture Anime S00E") {
			t.Fatalf("target=%s want folder=Season 00 and a Fixture Anime S00E… filename", target)
		}
	})

	// A missing owner season leaves the default directory's `${sf}` segment
	// empty: the render refuses and the file's delivery fails naming the fact.
	t.Run("missing-season-refuses", func(t *testing.T) {
		db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
			values := p["fixture_media_values"].(map[string]any)
			values["tmdb"] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
			p["fixture_anime_season"] = nil
		})
		ctx := context.Background()
		service := newFixtureService(t, db, fixture)
		prepareFixture(t, db, service, fixture.artifactID)
		var operations, failed, placements int
		if err := db.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM file_operations WHERE artifact_id=$1),
  (SELECT count(*) FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.file_ref='primary' AND r.outcome='failed' AND r.effect_class='no_external_effect'),
  (SELECT count(*) FROM placements)`, fixture.artifactID).Scan(&operations, &failed, &placements); err != nil {
			t.Fatal(err)
		}
		if operations != 0 || failed != 1 || placements != 0 {
			t.Fatalf("season-less tv must not place: operations=%d failed=%d placements=%d", operations, failed, placements)
		}
		var reason string
		if err := db.QueryRowContext(ctx, `SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.file_ref='primary'`, fixture.artifactID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != "naming_fact_missing_anime_season_folder" {
			t.Fatalf("the refusal names the empty season folder: reason=%q", reason)
		}
	})
}
