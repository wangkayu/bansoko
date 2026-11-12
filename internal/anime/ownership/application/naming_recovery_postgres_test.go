package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/metadata/matcher"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMatchMetadataRecoversOnlyFailuresSuppliedByTheMatch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	bgmID, tmdbID, kind, season := "bgm-recovery", "tmdb-recovery", "tv", 2
	service.SetMetadataMatcher(&fakeMetadataMatcher{result: &matcher.MatchResult{
		BgmID: &bgmID, TmdbWorkID: &tmdbID, TmdbKind: &kind, TmdbSeason: &season,
	}})
	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "match-naming-recovery")
	if err != nil {
		t.Fatal(err)
	}
	for id, reason := range map[string]string{
		"match-recover-season":     "naming_fact_missing_anime_season_folder",
		"match-recover-wrong-fact": "naming_fact_missing_anime_year",
		"match-recover-collision":  "target_collision_blocked",
	} {
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $1, '{}', '{}', 'acquisition_failed', $3)`, id, subscribed.RssSources[0].Id, reason); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 1}, "match-naming-recovery-1")
	if err != nil {
		t.Fatalf("match metadata: %v", err)
	}
	if result.Metadata.Season == nil || *result.Metadata.Season != season {
		t.Fatalf("matched season was not persisted: %+v", result.Metadata)
	}
	assertNamingRecoveryWorkItems(t, db, map[string]int{
		"match-recover-season":     1,
		"match-recover-wrong-fact": 0,
		"match-recover-collision":  0,
	})

	if _, err := service.MatchAnimeMetadata(context.Background(), subscribed.ManagedAnime.Id, MetadataMatchInput{ExpectedVersion: 2}, "match-naming-recovery-2"); err != nil {
		t.Fatalf("repeat complete match: %v", err)
	}
	assertNamingRecoveryWorkItems(t, db, map[string]int{"match-recover-season": 1})
}

func TestPatchMetadataRecoversOnlyFailuresSuppliedByThePatch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribed := ownershipSubscribeTestDraft(t, service, "patch-naming-recovery")
	if _, err := db.ExecContext(context.Background(), `UPDATE anime_metadata SET season = NULL WHERE managed_anime_id = $1`, subscribed.ManagedAnime.Id); err != nil {
		t.Fatal(err)
	}
	for id, reason := range map[string]string{
		"patch-recover-season":     "naming_fact_missing_anime_season_folder",
		"patch-recover-wrong-fact": "naming_fact_missing_anime_year",
		"patch-recover-render":     "target_render_failed",
	} {
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $1, '{}', '{}', 'acquisition_failed', $3)`, id, subscribed.RssSources[0].Id, reason); err != nil {
			t.Fatal(err)
		}
	}

	season := 3
	patched, err := service.PatchAnimeMetadataApplication(context.Background(), subscribed.ManagedAnime.Id, MetadataPatchInput{ExpectedVersion: 1, Season: &season})
	if err != nil {
		t.Fatalf("patch season: %v", err)
	}
	if patched.ReevaluatedReleases == nil || *patched.ReevaluatedReleases != 1 {
		t.Fatalf("recovery count = %+v, want 1", patched.ReevaluatedReleases)
	}
	assertNamingRecoveryWorkItems(t, db, map[string]int{
		"patch-recover-season":     1,
		"patch-recover-wrong-fact": 0,
		"patch-recover-render":     0,
	})

	if _, err := service.PatchAnimeMetadataApplication(context.Background(), subscribed.ManagedAnime.Id, MetadataPatchInput{ExpectedVersion: patched.Version, Season: &season}); err != nil {
		t.Fatalf("repeat unchanged season: %v", err)
	}
	assertNamingRecoveryWorkItems(t, db, map[string]int{"patch-recover-season": 1})
}

func assertNamingRecoveryWorkItems(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, expected map[string]int) {
	t.Helper()
	for releaseID, want := range expected {
		var got int
		if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s release evaluations = %d, want %d", releaseID, got, want)
		}
	}
}
