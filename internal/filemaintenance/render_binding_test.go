package filemaintenance

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// These are PostgreSQL lineage tests, not file-effect acceptance: the fixture
// records a completed operation and placement but does not move a real file.
// Both public render entrances must reproduce the operation's recorded binding,
// even when the release snapshot and current metadata disagree with it.
func TestNamingTargetRendererFrozenBindingPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		snapshot string
		want     string
	}{
		{
			name:     "recorded season, years and work IDs win",
			snapshot: `{"binding_facts":{"media_kind":"tv","season":4,"tmdb_work_id":"effect-tmdb","bgm_work_id":"effect-bgm","tmdb_year":2020,"bgm_year":2019}}`,
			want:     "/library/TV/Season 04/effect-tmdb/effect-bgm/2020/episode.mkv",
		},
		{
			name:     "recorded empty IDs, years and zero season do not resurrect release values",
			snapshot: `{"binding_facts":{"media_kind":"tv","season":0,"tmdb_work_id":"","bgm_work_id":"","tmdb_year":null,"bgm_year":null}}`,
			want:     "/library/TV/no-season/no-tmdb/no-bgm/no-year/episode.mkv",
		},
		{
			name:     "absent binding keeps release snapshot fallback",
			snapshot: `{}`,
			want:     "/library/TV/Season 02/release-tmdb/release-bgm/no-year/episode.mkv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			renderer, chain, observation := seedBindingPrecedenceChain(t, db, tc.snapshot)
			assertBindingRenderTargets(t, renderer, chain, observation, filepath.FromSlash(tc.want))
		})
	}
}

func TestNamingTargetRendererFrozenBindingSurvivesMetadataRebinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot string
		want     string
	}{
		{
			name:     "recorded binding",
			snapshot: `{"binding_facts":{"media_kind":"tv","season":4,"tmdb_work_id":"effect-tmdb","bgm_work_id":"effect-bgm","tmdb_year":2020,"bgm_year":2019}}`,
			want:     "/library/TV/Season 04/effect-tmdb/effect-bgm/2020/episode.mkv",
		},
		{
			name:     "legacy release fallback",
			snapshot: `{}`,
			want:     "/library/TV/Season 02/release-tmdb/release-bgm/no-year/episode.mkv",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			renderer, chain, observation := seedBindingPrecedenceChain(t, db, tc.snapshot)
			want := filepath.FromSlash(tc.want)
			assertBindingRenderTargets(t, renderer, chain, observation, want)

			// Change the complete current binding after the first render. A live
			// metadata read would switch the naming branch as well as IDs/season.
			if _, err := db.Exec(`UPDATE anime_metadata
SET tmdb_media_kind = 'movie', tmdb_season = 9,
    tmdb_work_id = 'rebound-tmdb', bgm_work_id = 'rebound-bgm', version = version + 1
WHERE managed_anime_id = $1`, observation.AnimeID); err != nil {
				t.Fatal(err)
			}
			assertBindingRenderTargets(t, renderer, chain, observation, want)
		})
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
	// The seed gives the current metadata season 2; overwrite it with a third,
	// conflicting value so passing cannot be explained by a current-row read.
	if _, err := db.Exec(`UPDATE anime_metadata
SET tmdb_season = 7, tmdb_work_id = 'current-tmdb', bgm_work_id = 'current-bgm'
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
