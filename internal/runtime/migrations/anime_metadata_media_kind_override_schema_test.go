package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

// The owner media-kind pin (owner decision 2026-10-16) is an additive nullable
// text column on anime_metadata with exactly the TMDB media-kind vocabulary. It
// is stored next to the TMDB tuple but never part of it: the tuple keeps its own
// atomic check and the pin may disagree with it (a movie pin on a TV tuple is
// the point of the field).
func TestAnimeMetadataMediaKindOverrideSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "005_anime_metadata_media_kind_override.sql"))
	if err != nil {
		t.Fatal(err)
	}
	requireFragments(t, string(raw), "media kind override migration",
		"ALTER TABLE anime_metadata",
		"ADD COLUMN media_kind_override TEXT",
		"anime_metadata_media_kind_override_check",
		"CHECK (media_kind_override IS NULL OR media_kind_override IN ('tv', 'movie'))",
	)

	probe := newConstraintProbe(t)
	probe.accept("seed owner", `INSERT INTO managed_anime(id, title) VALUES ('anime-kind-override', 'Kind Override')`)
	t.Cleanup(func() {
		// anime_metadata references managed_anime: the dependent row must go
		// first or the owner delete trips the foreign key.
		if _, err := probe.db.ExecContext(probe.ctx, `DELETE FROM anime_metadata WHERE managed_anime_id = 'anime-kind-override'`); err != nil {
			t.Errorf("clean seed metadata: %v", err)
		}
		if _, err := probe.db.ExecContext(probe.ctx, `DELETE FROM managed_anime WHERE id = 'anime-kind-override'`); err != nil {
			t.Errorf("clean seed owner: %v", err)
		}
	})
	probe.accept("nullable default", `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-kind-override')`)
	probe.accept("tv pin", `UPDATE anime_metadata SET media_kind_override = 'tv' WHERE managed_anime_id = 'anime-kind-override'`)
	probe.accept("movie pin", `UPDATE anime_metadata SET media_kind_override = 'movie' WHERE managed_anime_id = 'anime-kind-override'`)
	probe.accept("clear pin", `UPDATE anime_metadata SET media_kind_override = NULL WHERE managed_anime_id = 'anime-kind-override'`)
	probe.reject("unknown kind", `UPDATE anime_metadata SET media_kind_override = 'ova' WHERE managed_anime_id = 'anime-kind-override'`)
	probe.reject("blank kind", `UPDATE anime_metadata SET media_kind_override = '' WHERE managed_anime_id = 'anime-kind-override'`)
	probe.reject("padded kind", `UPDATE anime_metadata SET media_kind_override = ' tv ' WHERE managed_anime_id = 'anime-kind-override'`)
	// The pin stays independent of the TMDB tuple: a movie pin on a TV binding
	// is exactly the override this field exists for, and the atomic tuple check
	// still holds on its own.
	probe.accept("movie pin on a tv tuple", `UPDATE anime_metadata
		SET tmdb_work_id = '1', tmdb_media_kind = 'tv', tmdb_season = 2, media_kind_override = 'movie'
		WHERE managed_anime_id = 'anime-kind-override'`)
	probe.reject("partial tuple stays refused", `UPDATE anime_metadata
		SET tmdb_work_id = '1', tmdb_media_kind = NULL, tmdb_season = 2
		WHERE managed_anime_id = 'anime-kind-override'`)
}
