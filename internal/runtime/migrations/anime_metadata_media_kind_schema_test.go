package migrations

import "testing"

// The owner media kind (owner decision 2026-10-16) is a nullable text column on
// anime_metadata with exactly the TMDB media-kind vocabulary. It is stored next
// to the TMDB tuple but never part of it: the tuple keeps its own atomic check
// and the kind may disagree with it (a movie kind on a TV tuple is the point of
// the field).
func TestAnimeMetadataMediaKindSchema(t *testing.T) {
	probe := newConstraintProbe(t)
	probe.accept("seed owner", `INSERT INTO managed_anime(id, title) VALUES ('anime-owner-kind', 'Owner Kind')`)
	t.Cleanup(func() {
		// anime_metadata references managed_anime: the dependent row must go
		// first or the owner delete trips the foreign key.
		if _, err := probe.db.ExecContext(probe.ctx, `DELETE FROM anime_metadata WHERE managed_anime_id = 'anime-owner-kind'`); err != nil {
			t.Errorf("clean seed metadata: %v", err)
		}
		if _, err := probe.db.ExecContext(probe.ctx, `DELETE FROM managed_anime WHERE id = 'anime-owner-kind'`); err != nil {
			t.Errorf("clean seed owner: %v", err)
		}
	})
	probe.accept("nullable default", `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-owner-kind')`)
	probe.accept("tv kind", `UPDATE anime_metadata SET media_kind = 'tv' WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.accept("movie kind", `UPDATE anime_metadata SET media_kind = 'movie' WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.accept("clear kind", `UPDATE anime_metadata SET media_kind = NULL WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.reject("unknown kind", `UPDATE anime_metadata SET media_kind = 'ova' WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.reject("blank kind", `UPDATE anime_metadata SET media_kind = '' WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.reject("padded kind", `UPDATE anime_metadata SET media_kind = ' tv ' WHERE managed_anime_id = 'anime-owner-kind'`)
	// The kind stays independent of the TMDB tuple: a movie kind on a TV binding
	// is exactly what this field exists for, and the atomic tuple check
	// still holds on its own.
	probe.accept("movie kind on a tv tuple", `UPDATE anime_metadata
		SET tmdb_work_id = '1', tmdb_media_kind = 'tv', tmdb_season = 2, media_kind = 'movie'
		WHERE managed_anime_id = 'anime-owner-kind'`)
	probe.reject("partial tuple stays refused", `UPDATE anime_metadata
		SET tmdb_work_id = '1', tmdb_media_kind = NULL, tmdb_season = 2
		WHERE managed_anime_id = 'anime-owner-kind'`)
}
