package migrations

import (
	"context"
	"database/sql"
	"testing"
)

// 044 adds the owner-edited naming season as a nullable column next to the
// TMDB tuple, never part of it, and carries over the season a bound TV row
// named before (season 0 included: naming-render-3 named it Season 00). A row
// with no binding or a movie binding stays unset, as render-3 named no season.
func TestAnimeMetadataSeasonUpgradeCarriesTheNamedTMDBSeason(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "043_new_releases_only.sql")

	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('s-tv2', 'TV S2'), ('s-tv0', 'TV S0'), ('s-movie', 'Movie'), ('s-none', 'None')`,
		`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season) VALUES
		   ('s-tv2', '1', 'tv', 2), ('s-tv0', '2', 'tv', 0), ('s-movie', '3', 'movie', 0)`,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES ('s-none')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed the installed rows: %v\n%s", err, statement)
		}
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	want := map[string]sql.NullInt64{
		"s-tv2":   {Int64: 2, Valid: true},
		"s-tv0":   {Int64: 0, Valid: true},
		"s-movie": {},
		"s-none":  {},
	}
	for id, expected := range want {
		var season, tmdbSeason sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT season, tmdb_season FROM anime_metadata WHERE managed_anime_id = $1`, id).Scan(&season, &tmdbSeason); err != nil {
			t.Fatal(err)
		}
		if season != expected {
			t.Fatalf("%s season = %+v, want %+v", id, season, expected)
		}
		if id != "s-none" && !tmdbSeason.Valid {
			t.Fatalf("%s lost its TMDB season", id)
		}
	}

	for name, statement := range map[string]string{
		"specials":                  `UPDATE anime_metadata SET season = 0 WHERE managed_anime_id = 's-none'`,
		"season without TMDB tuple": `UPDATE anime_metadata SET season = 3 WHERE managed_anime_id = 's-none'`,
		"season differs from TMDB":  `UPDATE anime_metadata SET season = 5 WHERE managed_anime_id = 's-tv2'`,
		"clear":                     `UPDATE anime_metadata SET season = NULL WHERE managed_anime_id = 's-tv2'`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE anime_metadata SET season = -1 WHERE managed_anime_id = 's-none'`); err == nil {
		t.Fatal("a negative season was accepted")
	}
}
