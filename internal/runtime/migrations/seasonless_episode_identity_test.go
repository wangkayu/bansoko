package migrations

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// 095 makes the file fact identity coverage accept both resolver versions
// (episode-identity-v1 keys tv:s<S>:e<N>, v2 keys tv:e<N>) and gives every
// TMDB-bound row the one-time owner media kind backfill.
func TestSeasonlessEpisodeIdentityMigrationFragments(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "095_seasonless_episode_identity.sql"))
	if err != nil {
		t.Fatal(err)
	}
	requireFragments(t, string(raw), "seasonless episode identity migration",
		"CREATE OR REPLACE FUNCTION filefact_source_relation_valid",
		"NOT IN ('episode-identity-v1','episode-identity-v2')",
		"UPDATE anime_metadata",
		"SET media_kind_override = tmdb_media_kind",
		"WHERE media_kind_override IS NULL",
		"AND tmdb_media_kind IS NOT NULL;",
	)
}

// The upgrade backfills an unset owner media kind from the bound TMDB kind and
// leaves a set kind, and a row without a tuple, as they were.
func TestSeasonlessEpisodeIdentityUpgradeBackfillsTheOwnerMediaKind(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "093_diagnostic_conditions.sql")

	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('k-tv', 'TV'), ('k-movie', 'Movie'), ('k-set', 'Set'), ('k-none', 'None')`,
		`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season) VALUES
		   ('k-tv', '1', 'tv', 2), ('k-movie', '2', 'movie', 0)`,
		`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind_override) VALUES
		   ('k-set', '3', 'movie', 0, 'tv')`,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES ('k-none')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed the installed rows: %v\n%s", err, statement)
		}
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	want := map[string]sql.NullString{
		"k-tv":    {String: "tv", Valid: true},
		"k-movie": {String: "movie", Valid: true},
		"k-set":   {String: "tv", Valid: true},
		"k-none":  {},
	}
	for id, expected := range want {
		var kind sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT media_kind_override FROM anime_metadata WHERE managed_anime_id = $1`, id).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != expected {
			t.Fatalf("%s media_kind_override = %+v, want %+v", id, kind, expected)
		}
	}
}
