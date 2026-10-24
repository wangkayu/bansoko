package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/filekinds"
	facts "bansoko/internal/release/facts"
)

// A one-shot admission freezes the Release media fact snapshot in its own
// transaction through the same writer RSS evaluation uses: the request's
// episode_offset, the bound media kind and season, the metadata work ids and
// the file kind list. Artifact inspection resolves every file's own identity
// from this row, so without it every one-shot video would be blocked.
func TestAcquireSeedOnceFreezesTheReleaseMediaFactSnapshot(t *testing.T) {
	service, _, _, input := movieOneShotFixture(t, "Example Movie - 07",
		&animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 2})
	ctx := context.Background()
	input.EpisodeOffset = 2
	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-media-facts")
	if err != nil {
		t.Fatalf("admission: %v", err)
	}
	var snapshots int
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("one-shot release has %d media fact snapshots, want 1", snapshots)
	}
	var parserKind, mediaKind, tmdbWorkID, kindsVersion string
	var season, offset int
	var identityRaw []byte
	if err := service.db.QueryRowContext(ctx, `
SELECT parser_kind, release_facts->>'media_kind', (release_facts->>'season')::int,
       (release_facts->'parser_provenance'->>'offset')::int, release_facts->>'tmdb_work_id',
       release_facts->>'file_kinds_version', release_identity
FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID).Scan(&parserKind, &mediaKind, &season, &offset, &tmdbWorkID, &kindsVersion, &identityRaw); err != nil {
		t.Fatal(err)
	}
	if parserKind == "" || mediaKind != "tv" || season != 2 || offset != 2 || tmdbWorkID != "265010" || kindsVersion != filekinds.Version {
		t.Fatalf("snapshot = kind %q media %q season %d offset %d tmdb %q kinds %q", parserKind, mediaKind, season, offset, tmdbWorkID, kindsVersion)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Episode == nil || string(*identity.Episode) != "9" {
		t.Fatalf("release identity episode = %v, want 7 shifted by the request offset 2", identity.Episode)
	}
}
