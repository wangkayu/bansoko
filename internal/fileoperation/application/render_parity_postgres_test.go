package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
)

// parityReleaseTitle is the original release title the parity fixture freezes.
// It carries every fact the naming render is expected to reproduce: the source
// group, the resolution, the video codec and a subtitle language.
const parityReleaseTitle = "[Test Group] Fixture Anime - 01 [1080p HEVC][CHS]"

// parityMediaFacts is the inspected media-fact JSON the artifact file carries
// after acquisition, written the way the local-media inspector writes it.
const parityMediaFacts = `{"resolution":"1080p","video_codec":"HEVC","container":"mkv"}`

// openParityFixtureDB seeds the standard file-operation fixture with one frozen
// naming-rule set, so the acquisition render and the maintenance render share
// the same effective rules.
func openParityFixtureDB(t *testing.T, rules naming.NamingRules) (*sql.DB, fileFixture) {
	t.Helper()
	return openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		sections := policy["effective_sections"].(map[string]any)
		sections[animedomain.SectionNamingRules] = rules
		values := policy["fixture_media_values"].(map[string]any)
		values["bgm"] = map[string]any{"state": "known", "value": map[string]any{"ref": "bgm-original", "version": 1}, "reason_code": nil}
		values["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tmdb-original", "kind": "tv", "season": 1, "version": 1}, "reason_code": nil}
	})
}

// seedParityFrozenEvidence also sets the anime's current configuration to what
// acquisition rendered with (bindings, TMDB season, owner season 1, year 2025), so the
// maintenance render, which reads the current configuration, has the same
// inputs. It writes the durable evidence acquisition consumes,
// exactly as production writes it: the original release title on the release
// row, the inspected media facts on the artifact file, and one release media
// fact snapshot carrying the frozen release facts, one resolved TV episode
// identity and the per-file evidence row.
func seedParityFrozenEvidence(t *testing.T, db *sql.DB, fixture fileFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE anime_metadata SET bgm_work_id='bgm-original', tmdb_work_id='tmdb-original', tmdb_media_kind='tv', tmdb_season=1, season=1, year=2025 WHERE managed_anime_id=$1`, fixture.animeID); err != nil {
		t.Fatal(err)
	}
	rawTitle, err := json.Marshal(map[string]string{"title": parityReleaseTitle})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET raw_facts = $2::jsonb WHERE id = $1`, "release-file-test", rawTitle); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE artifact_files SET facts = $2::jsonb WHERE id = $1`, fixture.fileID, parityMediaFacts); err != nil {
		t.Fatal(err)
	}

	provenance := parser.Provenance{
		Kind:      string(parser.ParserRegex),
		MediaKind: "tv",
		Season:    1,
		Offset:    0,
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind: "tv",

		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "1"),
	})
	releaseFactsRaw, err := json.Marshal(facts.ReleaseFacts{
		BatchPlan:        fixtureBatchPlan(t, db, map[string]episodeidentity.EpisodeIdentity{"primary": identity}),
		Title:            parityReleaseTitle,
		GroupName:        "Test Group",
		Resolution:       "1080p",
		Languages:        []string{"sc"},
		MediaKind:        "tv",
		Season:           1,
		SeasonBound:      true,
		BGMWorkID:        "bgm-original",
		TMDBWorkID:       "tmdb-original",
		ParserProvenance: &provenance,
	})
	if err != nil {
		t.Fatal(err)
	}
	identityRaw, err := facts.MarshalIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	fileEvidenceRaw, err := json.Marshal(facts.FileEvidence{
		Name:      "source.mkv",
		MediaKind: "video",
		SizeBytes: fixture.size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, input_digest, release_identity, release_facts
)
VALUES ('snapshot-file-parity', 'release-file-test', 'media-facts-v1', $1, 'regex', $2, 'sha256:input', $3::jsonb, $4::jsonb)`,
		episodeidentity.ResolverVersion, parser.NilConfigFingerprint(), identityRaw, releaseFactsRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_files(
    snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts
)
VALUES ('snapshot-file-parity', 'primary', 'video', $1, NULL, $2::jsonb, $3::jsonb)`,
		fixture.size, fileEvidenceRaw, mustMarshalEffectiveFacts(t, identity)); err != nil {
		t.Fatal(err)
	}
}

// prepareParityPlacement prepares the artifact through the real acquisition
// path and records the completed operation as an active placement, returning
// the placement id and the acquisition-rendered target path.
func prepareParityPlacement(t *testing.T, db *sql.DB, fixture fileFixture) (string, string) {
	t.Helper()
	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	prepareFixture(t, db, service, fixture.artifactID)

	var operationID, target string
	if err := db.QueryRow(`SELECT id, target_path FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operationID, &target); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(target) == "" {
		t.Fatal("acquisition render produced no target path")
	}
	placementID := "placement-render-parity"
	if _, err := db.Exec(`
INSERT INTO placements(id, managed_anime_id, physical_path, source_evidence_type, source_owner_ref,
    source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state,
    replacement_eligible, version, created_at, updated_at)
SELECT $1, $2, target_path, 'file_operation', id, source_fingerprint,
    COALESCE(target_fingerprint, source_fingerprint, '{}'::jsonb), NULL, 'parser-v1', 'verified', 'active', true, 1,
    CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM file_operations WHERE id = $3`, placementID, fixture.animeID, operationID); err != nil {
		t.Fatal(err)
	}
	return placementID, target
}

// everyAdvertisedFactRules is a custom template referencing the frozen facts
// the placed-file context constructor supplies and the shipped catalog
// advertises, including binding IDs.
func everyAdvertisedFactRules() naming.NamingRules {
	return naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "${sanitize(anime.title)}/Season ${pad(tmdb.season, 2)}",
			VideoFilenameTemplate: "${sanitize(anime.title)} - E${pad(ep.display, 2)} [${media.resolution}] [${media.video_codec}] [${media.container}] [${release.lang}]@${anime.group} (${release.title}) (${file.name}) [${bgm.id}-${tmdb.id}].${file.ext}",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)} [${media.resolution}] [${media.video_codec}] [${release.lang}]@${anime.group} [${bgm.id}-${tmdb.id}].${file.ext}",
		},
		SubtitleSuffixTemplate: "${defaultValue('und', release.lang)}",
	}
}

func workIDParityRules() naming.NamingRules {
	rules := naming.DefaultNamingRules()
	rules.TV.VideoFilenameTemplate = "${bgm.id}-${tmdb.id}.${file.ext}"
	return rules
}

// TestMaintenanceRenderParityWithAcquisitionRender places one file through the
// real acquisition render and requires every maintenance renderer to reproduce
// that path byte-for-byte for the default, all-fact and work-ID templates.
func TestMaintenanceRenderParityWithAcquisitionRender(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules naming.NamingRules
	}{
		{name: "default template", rules: naming.DefaultNamingRules()},
		{name: "custom every-fact template", rules: everyAdvertisedFactRules()},
		{name: "custom work ID template", rules: workIDParityRules()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openParityFixtureDB(t, tc.rules)
			defer db.Close()
			seedParityFrozenEvidence(t, db, fixture)
			placementID, target := prepareParityPlacement(t, db, fixture)
			ctx := context.Background()
			// Both renders read the same frozen directory template, so a
			// re-anchor that silently stopped working would let them agree on the
			// deployment mount /library and still pass the equality below. Pin the
			// fixture root instead.
			if !strings.HasPrefix(target, fixture.root+"/") {
				t.Fatalf("acquisition target %q is not under the fixture root %q", target, fixture.root)
			}
			renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, tc.rules))
			// The observation deliberately carries no scan-provided facts: the
			// render must reproduce the acquisition path from the frozen
			// evidence of the placement alone.
			rendered, err := renderer.RenderTarget(ctx, filemaintenance.SnapshotObservation{
				AnimeID:     fixture.animeID,
				PlacementID: placementID,
				FilePath:    target,
				SizeBytes:   fixture.size,
				Coverage:    []string{"tv:e1"},
			})
			if err != nil {
				t.Fatalf("RenderTarget: %v", err)
			}
			// The frozen directory template carries the whole absolute path, so
			// the maintenance render must reproduce the acquisition target with
			// no anchor applied.
			if rendered != target {
				t.Fatalf("RenderTarget %q != acquisition target %q", rendered, target)
			}

			calibrated, err := renderer.RenderCalibratedTarget(ctx, fixture.animeID, target, fixture.size, filemaintenance.CalibratedRenderFacts{
				EpisodeDisplay: "1",
				PlacementID:    placementID,
			})
			if err != nil {
				t.Fatalf("RenderCalibratedTarget: %v", err)
			}
			if calibrated != target {
				t.Fatalf("RenderCalibratedTarget %q != acquisition target %q", calibrated, target)
			}

			frozen, err := renderer.RenderTargetWithFrozenEpisode(ctx, fixture.animeID, target, fixture.size, &naming.FrozenEpisodeNamingFacts{
				Raw: "1", Number: "1", Offset: 0, Display: "1",
			})
			if err != nil {
				t.Fatalf("RenderTargetWithFrozenEpisode: %v", err)
			}
			if frozen != target {
				t.Fatalf("RenderTargetWithFrozenEpisode %q != acquisition target %q", frozen, target)
			}

		})
	}
}

func TestMaintenanceWorkIDTemplateFollowsCurrentBindings(t *testing.T) {
	rules := workIDParityRules()

	// A placed file whose template needs ids the effect never had can only
	// exist if acquisition placed it. Acquisition must refuse that render
	// (naming spec 2.4.1: templates reject missing interpolation values; never
	// guess a target): a failed declaration, no operation, no placement.
	t.Run("the effect never had the ids", func(t *testing.T) {
		db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
			p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
			v := p["fixture_media_values"].(map[string]any)
			v["bgm"] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
			v["tmdb"] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
		})
		defer db.Close()
		seedParityFrozenEvidence(t, db, fixture)
		if _, err := db.Exec(`UPDATE anime_metadata SET bgm_work_id = NULL, tmdb_work_id = NULL, tmdb_media_kind = NULL, tmdb_season = NULL WHERE managed_anime_id = $1`, fixture.animeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE media_fact_snapshots SET release_facts = release_facts - 'bgm_work_id' - 'tmdb_work_id' WHERE id='snapshot-file-parity'`); err != nil {
			t.Fatal(err)
		}
		service := newFixtureService(t, db, fixture)
		service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
		prepareFixture(t, db, service, fixture.artifactID)
		var operations, failed, placements int
		if err := db.QueryRow(`SELECT
  (SELECT count(*) FROM file_operations WHERE artifact_id=$1),
  (SELECT count(*) FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.file_ref='primary' AND r.outcome='failed' AND r.effect_class='no_external_effect'),
  (SELECT count(*) FROM placements)`, fixture.artifactID).Scan(&operations, &failed, &placements); err != nil {
			t.Fatal(err)
		}
		if operations != 0 || failed != 1 || placements != 0 {
			t.Fatalf("missing ids admitted a degraded target: operations=%d failed=%d placements=%d", operations, failed, placements)
		}
		var reason string
		if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.file_ref='primary'`, fixture.artifactID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		// Without a binding bgm.id is an undefined variable, not an empty value.
		if reason != "naming_fact_undefined_bgm_id" {
			t.Fatalf("the refusal names the undefined variable: reason=%q", reason)
		}
	})

	// A maintenance render reads the current binding, never the ids the release
	// snapshot or the effect recorded: losing the ids from the snapshot changes
	// nothing, a rebinding moves the target, and a cleared binding fails closed
	// with the file left where it is.
	t.Run("the release snapshot lost the ids the effect recorded", func(t *testing.T) {
		db, fixture := openParityFixtureDB(t, rules)
		defer db.Close()
		seedParityFrozenEvidence(t, db, fixture)
		if _, err := db.Exec(`UPDATE media_fact_snapshots SET release_facts = release_facts - 'bgm_work_id' - 'tmdb_work_id' WHERE id='snapshot-file-parity'`); err != nil {
			t.Fatal(err)
		}
		placementID, target := prepareParityPlacement(t, db, fixture)
		renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, rules))
		observation := filemaintenance.SnapshotObservation{AnimeID: fixture.animeID, PlacementID: placementID, FilePath: target, SizeBytes: fixture.size, Coverage: []string{"tv:e1"}}
		rendered, err := renderer.RenderTarget(context.Background(), observation)
		if err != nil {
			t.Fatalf("the current binding must render: %v", err)
		}
		if rendered != target {
			t.Fatalf("render = %q, want the acquisition name %q", rendered, target)
		}
	})

	t.Run("rebinding moves the target", func(t *testing.T) {
		db, fixture := openParityFixtureDB(t, rules)
		defer db.Close()
		seedParityFrozenEvidence(t, db, fixture)
		placementID, target := prepareParityPlacement(t, db, fixture)
		if _, err := db.Exec(`UPDATE anime_metadata SET bgm_work_id='bgm-current', tmdb_work_id='tmdb-current' WHERE managed_anime_id=$1`, fixture.animeID); err != nil {
			t.Fatal(err)
		}
		renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, rules))
		observation := filemaintenance.SnapshotObservation{AnimeID: fixture.animeID, PlacementID: placementID, FilePath: target, SizeBytes: fixture.size, Coverage: []string{"tv:e1"}}
		rendered, err := renderer.RenderTarget(context.Background(), observation)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(rendered, "bgm-current") || !strings.Contains(rendered, "tmdb-current") {
			t.Fatalf("render = %q, want the current work ids", rendered)
		}
	})

	for _, tc := range []struct {
		name   string
		update string
		want   string
	}{
		{name: "the BGM binding was cleared", update: `bgm_work_id = NULL`, want: "config_incomplete:binding_missing:bgm"},
		{name: "the TMDB binding was cleared", update: `tmdb_work_id = NULL, tmdb_media_kind = NULL, tmdb_season = NULL`, want: "config_incomplete:binding_missing:tmdb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openParityFixtureDB(t, rules)
			defer db.Close()
			seedParityFrozenEvidence(t, db, fixture)
			placementID, target := prepareParityPlacement(t, db, fixture)
			if _, err := db.Exec(`UPDATE anime_metadata SET `+tc.update+` WHERE managed_anime_id=$1`, fixture.animeID); err != nil {
				t.Fatal(err)
			}
			renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, rules))
			observation := filemaintenance.SnapshotObservation{AnimeID: fixture.animeID, PlacementID: placementID, FilePath: target, SizeBytes: fixture.size, Coverage: []string{"tv:e1"}}
			rendered, err := renderer.RenderTarget(context.Background(), observation)
			if got := filemaintenance.RenderExclusionReason(err); got != tc.want {
				t.Fatalf("render = %q, %v; want reason %q", rendered, err, tc.want)
			}
			planned, err := filemaintenance.NewPlanner(paritySnapshotReader{observation: observation}, renderer).Plan(context.Background(), fixture.animeID, 1, 1)
			if err != nil || len(planned.Candidates) != 1 {
				t.Fatalf("plan = %+v, %v", planned, err)
			}
			if candidate := planned.Candidates[0]; !candidate.Excluded || candidate.ExcludeReasonCode != tc.want || candidate.TargetPath != "" {
				t.Fatalf("candidate = %+v, want excluded with %q and no target", candidate, tc.want)
			}
		})
	}
}

// TestMaintenanceOffsetChangeTouchesOnlyEpisode shifts the frozen episode
// identity by one and requires the episode-derived substrings to be the only
// difference from the acquisition path.
func TestMaintenanceOffsetChangeTouchesOnlyEpisode(t *testing.T) {
	db, fixture := openParityFixtureDB(t, naming.DefaultNamingRules())
	defer db.Close()
	seedParityFrozenEvidence(t, db, fixture)
	_, target := prepareParityPlacement(t, db, fixture)

	renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, naming.DefaultNamingRules()))
	// The offset path freezes the original raw capture and applies the new
	// offset exactly once: Raw stays "1", the resolved episode moves to "2".
	shifted, err := renderer.RenderTargetWithFrozenEpisode(context.Background(), fixture.animeID, target, fixture.size, &naming.FrozenEpisodeNamingFacts{
		Raw: "1", Number: "2", Offset: 1, Display: "2",
	})
	if err != nil {
		t.Fatalf("offset render: %v", err)
	}
	if shifted == target {
		t.Fatal("offset change produced the unchanged target")
	}
	if filepath.Dir(shifted) != filepath.Dir(target) {
		t.Fatalf("offset change moved the directory: %q != %q", filepath.Dir(shifted), filepath.Dir(target))
	}
	wantName := strings.Replace(filepath.Base(target), "E01", "E02", 1)
	if wantName == filepath.Base(target) {
		t.Fatalf("fixture target %q carries no episode-derived token", target)
	}
	if filepath.Base(shifted) != wantName {
		t.Fatalf("offset change altered more than the episode: %q, want %q", filepath.Base(shifted), wantName)
	}
}

// TestPathDiffReportsNoDriftAfterAcquisitionPlacement runs the real path-diff
// preview over a library that still holds the acquisition-rendered path and
// requires zero drift.
func TestPathDiffReportsNoDriftAfterAcquisitionPlacement(t *testing.T) {
	db, fixture := openParityFixtureDB(t, naming.DefaultNamingRules())
	defer db.Close()
	seedParityFrozenEvidence(t, db, fixture)
	placementID, target := prepareParityPlacement(t, db, fixture)
	ctx := context.Background()

	facts, err := json.Marshal(map[string]string{"placement_id": placementID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, resolution, source, language, "group", parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ($1, 1, $2, 0, 'fp-parity', $3, $4::jsonb, CURRENT_TIMESTAMP,
        'present', '["tv:e1"]'::jsonb, '1080p', NULL, 'sc', 'Test Group', 'v1', 'ff-parity', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		fixture.animeID, target, fixture.size, facts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, unclassified_route, file_effect_source_default_version, file_effect_version, updated_at)
VALUES ($1, 'skip', 0, 1, CURRENT_TIMESTAMP) ON CONFLICT (managed_anime_id) DO UPDATE SET unclassified_route = 'skip', file_effect_source_default_version = COALESCE(anime_business_configs.file_effect_source_default_version, 0), file_effect_version = COALESCE(anime_business_configs.file_effect_version, 0) + 1, updated_at = CURRENT_TIMESTAMP`, fixture.animeID); err != nil {
		t.Fatal(err)
	}

	service := filemaintenance.NewPathDiffService(db,
		filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, naming.DefaultNamingRules())),
		nil)
	views, err := service.ListPathDiffs(ctx, fixture.animeID, 1, 50, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("path diff rows = %d, want 1", len(views))
	}
	if views[0].FilePath != target {
		t.Fatalf("path diff row = %q, want %q", views[0].FilePath, target)
	}
	if views[0].Differs {
		t.Fatalf("unchanged library reported drift for %q (expected %v)", target, *views[0].ExpectedPath)
	}
}

// TestMaintenanceRenderBlockedWhenTemplateFactsAreMissing removes the video
// codec from the frozen evidence and requires the maintenance render to report
// missing render facts instead of producing a degraded name — and requires the
// planner to turn that into an excluded (blocked) candidate, never a move.
func TestMaintenanceRenderBlockedWhenTemplateFactsAreMissing(t *testing.T) {
	db, fixture := openParityFixtureDB(t, naming.DefaultNamingRules())
	defer db.Close()
	seedParityFrozenEvidence(t, db, fixture)
	placementID, target := prepareParityPlacement(t, db, fixture)
	// The evidence *loses* the codec the effect rendered as `[HEVC]`: the file
	// effect recorded it on the operation, and a maintenance re-render must not
	// emit the same name without the tag. Removing the fact before the effect
	// ran would be a different case — a file whose inspected facts never carried
	// a codec renders without the tag, exactly as acquisition rendered it, and
	// must stay alignable.
	if _, err := db.Exec(`UPDATE artifact_files SET facts = '{"resolution":"1080p","container":"mkv"}'::jsonb WHERE id = $1`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	renderer := filemaintenance.NewNamingTargetRenderer(db, parityRulesSourceUnder(fixture, naming.DefaultNamingRules()))
	observation := filemaintenance.SnapshotObservation{
		AnimeID:     fixture.animeID,
		PlacementID: placementID,
		FilePath:    target,
		SizeBytes:   fixture.size,
		Coverage:    []string{"tv:e1"},
	}

	rendered, err := renderer.RenderTarget(ctx, observation)
	if !errors.Is(err, naming.ErrRenderFactsUnavailable) {
		t.Fatalf("missing codec rendered %q with error %v, want ErrRenderFactsUnavailable", rendered, err)
	}

	planned, err := filemaintenance.NewPlanner(paritySnapshotReader{observation: observation}, renderer).
		Plan(ctx, fixture.animeID, 1, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(planned.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(planned.Candidates))
	}
	candidate := planned.Candidates[0]
	if !candidate.Excluded || candidate.ExcludeReasonCode != "render_facts_unavailable" || candidate.TargetPath != "" {
		t.Fatalf("missing template fact admitted a degraded target: %+v", candidate)
	}
}

// paritySnapshotReader serves one fixed observation, the shape a completed
// reconciliation generation provides to the planner.
type paritySnapshotReader struct {
	observation filemaintenance.SnapshotObservation
}

func (paritySnapshotReader) GenerationUsable(context.Context, string, int64) (bool, error) {
	return true, nil
}

func (r paritySnapshotReader) ListObservations(context.Context, string, int64) ([]filemaintenance.SnapshotObservation, error) {
	return []filemaintenance.SnapshotObservation{r.observation}, nil
}
