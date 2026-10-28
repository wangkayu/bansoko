package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// These are PostgreSQL lineage tests, not file-effect acceptance: the fixture
// records a completed operation and placement but does not move a real file.
// Both public render entrances render with the anime's current configuration
// (binding, owner season and year, media kind), whatever the release snapshot
// and the binding the operation recorded say.
func TestNamingTargetRendererCurrentConfigPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot string
	}{
		{name: "recorded binding does not win", snapshot: `{"binding_facts":{"media_kind":"tv","season":4,"tmdb_work_id":"effect-tmdb","bgm_work_id":"effect-bgm","tmdb_year":2020,"bgm_year":2019,"anime_season":5,"anime_year":2018}}`},
		{name: "release snapshot does not win", snapshot: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			renderer, chain, observation := seedBindingPrecedenceChain(t, db, tc.snapshot)
			assertBindingRenderTargets(t, renderer, chain, observation, filepath.FromSlash("/library/TV/Season 03/current-tmdb/current-bgm/2021/episode.mkv"))
		})
	}
}

func TestNamingTargetRendererFollowsMetadataChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update string
		want   string
	}{
		{
			name:   "rebinding and kind switch the naming branch",
			update: `media_kind_override = 'movie', tmdb_media_kind = 'movie', tmdb_season = 9, tmdb_work_id = 'rebound-tmdb', bgm_work_id = 'rebound-bgm'`,
			want:   "/library/MOVIE/rebound-tmdb/rebound-bgm/2021/episode.mkv",
		},
		{
			name:   "owner season change moves the season folder",
			update: `season = 6`,
			want:   "/library/TV/Season 06/current-tmdb/current-bgm/2021/episode.mkv",
		},
		{
			name:   "unset owner season and year render empty",
			update: `season = NULL, year = NULL`,
			want:   "/library/TV/no-season/current-tmdb/current-bgm/no-year/episode.mkv",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			renderer, chain, observation := seedBindingPrecedenceChain(t, db, `{}`)
			if _, err := db.Exec(`UPDATE anime_metadata SET `+tc.update+`, version = version + 1 WHERE managed_anime_id = $1`, observation.AnimeID); err != nil {
				t.Fatal(err)
			}
			assertBindingRenderTargets(t, renderer, chain, observation, filepath.FromSlash(tc.want))
		})
	}
}

func TestNamingTargetRendererRefusesIncompleteConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update string
		want   string
	}{
		{name: "tmdb binding cleared", update: `tmdb_work_id = NULL, tmdb_media_kind = NULL, tmdb_season = NULL`, want: "config_incomplete:binding_missing:tmdb"},
		{name: "bgm binding cleared", update: `bgm_work_id = NULL`, want: "config_incomplete:binding_missing:bgm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			renderer, chain, observation := seedBindingPrecedenceChain(t, db, `{"binding_facts":{"media_kind":"tv","season":4,"tmdb_work_id":"effect-tmdb","bgm_work_id":"effect-bgm"}}`)
			if _, err := db.Exec(`UPDATE anime_metadata SET `+tc.update+`, version = version + 1 WHERE managed_anime_id = $1`, observation.AnimeID); err != nil {
				t.Fatal(err)
			}
			_, err := renderer.RenderTarget(context.Background(), observation)
			if got := RenderExclusionReason(err); got != tc.want {
				t.Fatalf("RenderTarget reason = %q (%v), want %q", got, err, tc.want)
			}
			_, err = renderer.RenderCalibratedTarget(context.Background(), observation.AnimeID, observation.FilePath, observation.SizeBytes, CalibratedRenderFacts{PlacementID: chain.PlacementID, EpisodeDisplay: "1"})
			if got := RenderExclusionReason(err); got != tc.want {
				t.Fatalf("RenderCalibratedTarget reason = %q (%v), want %q", got, err, tc.want)
			}
			targets, err := renderer.RenderPlacementTargets(context.Background(), observation.AnimeID, []string{chain.PlacementID})
			if err != nil || len(targets) != 0 {
				t.Fatalf("placement targets = %v, %v; want none", targets, err)
			}
		})
	}
}

func TestNamingTargetRendererSkipsExcludedPlacement(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	renderer, chain, observation := seedBindingPrecedenceChain(t, db, `{}`)
	if _, err := db.Exec(`UPDATE placements SET replacement_eligible = false WHERE id = $1`, chain.PlacementID); err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.RenderTarget(context.Background(), observation); !errors.Is(err, ErrPlacementExcluded) {
		t.Fatalf("RenderTarget error = %v, want ErrPlacementExcluded", err)
	}
	if _, err := renderer.RenderCalibratedTarget(context.Background(), observation.AnimeID, observation.FilePath, observation.SizeBytes, CalibratedRenderFacts{PlacementID: chain.PlacementID, EpisodeDisplay: "1"}); !errors.Is(err, ErrPlacementExcluded) {
		t.Fatalf("RenderCalibratedTarget error = %v, want ErrPlacementExcluded", err)
	}
	targets, err := renderer.RenderPlacementTargets(context.Background(), observation.AnimeID, []string{chain.PlacementID})
	if err != nil || len(targets) != 0 {
		t.Fatalf("placement targets = %v, %v; want none", targets, err)
	}
}

func seedBindingPrecedenceChain(t *testing.T, db *sql.DB, renderSnapshot string) (*NamingTargetRenderer, placedRenderChain, SnapshotObservation) {
	t.Helper()
	// Conditional reads let explicit absence render a visible sentinel instead
	// of failing the required-fact guard. The TV/movie marker also makes a live
	// media-kind fallback observable through the public target path.
	// The fixtures name the deployment mount explicitly: a rendered target is
	// always absolute, so the expectations below carry the whole path.
	rules := naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/TV/${anime.season_folder ? anime.season_folder : 'no-season'}/${tmdb.id ? tmdb.id : 'no-tmdb'}/${bgm.id ? bgm.id : 'no-bgm'}/${anime.year ? anime.year : 'no-year'}",
			VideoFilenameTemplate: "episode.mkv",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "/library/MOVIE/${tmdb.id ? tmdb.id : 'no-tmdb'}/${bgm.id ? bgm.id : 'no-bgm'}/${anime.year ? anime.year : 'no-year'}",
			VideoFilenameTemplate: "episode.mkv",
		},
	}
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "binding-precedence", AnimeID: "anime-binding-precedence", AnimeTitle: "Binding Show",
		TMDBMediaKind: "tv", TMDBSeason: 2, SourceGroup: "Group",
		ReleaseTitle: "[Group] Binding Show - 01", PlacedPath: "/library/Binding Show/episode.mkv",
		DownloadName: "episode.mkv", Rules: rules, FrozenEpisode: "1",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	if _, err := db.Exec(`UPDATE media_fact_snapshots
SET release_facts = release_facts || '{"tmdb_work_id":"release-tmdb","bgm_work_id":"release-bgm"}'::jsonb
WHERE id = $1`, chain.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE file_operations SET frozen_render_snapshot = $1::jsonb WHERE id = $2`, renderSnapshot, chain.OperationID); err != nil {
		t.Fatal(err)
	}
	// The current configuration, deliberately different from the release
	// snapshot and the recorded binding: this is what the render must name.
	if _, err := db.Exec(`UPDATE anime_metadata
SET tmdb_season = 7, season = 3, year = 2021, tmdb_work_id = 'current-tmdb', bgm_work_id = 'current-bgm'
WHERE managed_anime_id = 'anime-binding-precedence'`); err != nil {
		t.Fatal(err)
	}
	observation := SnapshotObservation{
		AnimeID: "anime-binding-precedence", PlacementID: chain.PlacementID,
		FilePath: "/library/Binding Show/episode.mkv", SizeBytes: 1024,
	}
	return NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules}), chain, observation
}

func assertBindingRenderTargets(t *testing.T, renderer *NamingTargetRenderer, chain placedRenderChain, observation SnapshotObservation, want string) {
	t.Helper()
	t.Run("RenderTarget", func(t *testing.T) {
		got, err := renderer.RenderTarget(context.Background(), observation)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("rendered target = %q, want %q", got, want)
		}
	})
	t.Run("RenderCalibratedTarget", func(t *testing.T) {
		got, err := renderer.RenderCalibratedTarget(context.Background(), observation.AnimeID, observation.FilePath, observation.SizeBytes, CalibratedRenderFacts{
			PlacementID: chain.PlacementID, EpisodeDisplay: "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("calibrated target = %q, want %q", got, want)
		}
	})
}
