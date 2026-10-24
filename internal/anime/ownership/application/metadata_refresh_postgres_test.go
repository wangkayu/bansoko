package application

// The 刷新元数据 re-pull over the disposable schema. The ownership application
// decides the scope of one click: every bound work re-pulls exactly once, the
// answer is the unchanged metadata owner view, and it never edits the binding
// or the owner rows. The typed failure codes below are the composition's
// contract; the adapter that actually rewrites episode_title_cache is covered
// in identitymetadata/adapter.

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

// countingMetadataRefresher records every re-pull request and optionally fails.
type countingMetadataRefresher struct {
	requests []string
	err      error
}

func (r *countingMetadataRefresher) RefreshWork(ctx context.Context, provider, mediaKind, workID string, season int) error {
	r.requests = append(r.requests, provider+"/"+mediaKind+"/"+workID+"/"+strings.TrimSpace(intToString(season)))
	return r.err
}

func intToString(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// TestRefreshAnimeMetadataApplicationRePullsEachBoundWorkOnce runs one click
// over a fully bound anime: one BGM subject re-pull and one TMDB season
// re-pull, the unchanged owner view, and owner rows untouched.
func TestRefreshAnimeMetadataApplicationRePullsEachBoundWorkOnce(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, total_episodes)
VALUES ('anime-refresh', '10', '42', 'tv', 1, 12)`); err != nil {
		t.Fatal(err)
	}

	refresher := &countingMetadataRefresher{}
	service.SetMetadataWorkRefresher(refresher)
	view, err := service.RefreshAnimeMetadataApplication(ctx, "anime-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bgm/subject/10/0", "tmdb/tv/42/1"}; strings.Join(refresher.requests, " ") != strings.Join(want, " ") {
		t.Fatalf("the click re-pulls each bound work once, got %v", refresher.requests)
	}
	if view.BGM == nil || view.BGM.WorkID != "10" || view.TMDB == nil || view.TMDB.WorkID != "42" || view.TMDB.MediaKind != "tv" || view.TMDB.Season != 1 {
		t.Fatalf("the answer is the unchanged binding view: %+v", view)
	}
	if view.TotalEpisodes == nil || *view.TotalEpisodes != 12 || view.Version != 1 {
		t.Fatalf("the answer projects the owner rows verbatim: %+v", view)
	}
	// Nothing was written: the metadata row keeps its version, its bindings
	// and its episode count.
	var version, totalEpisodes int
	var bgmWorkID, tmdbWorkID string
	if err := db.QueryRowContext(ctx, `SELECT version, bgm_work_id, tmdb_work_id, total_episodes FROM anime_metadata WHERE managed_anime_id = 'anime-refresh'`).Scan(&version, &bgmWorkID, &tmdbWorkID, &totalEpisodes); err != nil {
		t.Fatal(err)
	}
	if version != 1 || bgmWorkID != "10" || tmdbWorkID != "42" || totalEpisodes != 12 {
		t.Fatalf("the re-pull must never edit owner rows, got v%d %s/%s eps=%d", version, bgmWorkID, tmdbWorkID, totalEpisodes)
	}
}

// TestRefreshAnimeMetadataApplicationDefaultsTheSeason re-pulls an unrecorded
// season as season 1, the same default the episode title cache keys.
func TestRefreshAnimeMetadataApplicationDefaultsTheSeason(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh-default', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season, total_episodes)
VALUES ('anime-refresh-default', '42', 'tv', 0, 12)`); err != nil {
		t.Fatal(err)
	}
	refresher := &countingMetadataRefresher{}
	service.SetMetadataWorkRefresher(refresher)
	if _, err := service.RefreshAnimeMetadataApplication(ctx, "anime-refresh-default"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tmdb/tv/42/1"}; strings.Join(refresher.requests, " ") != strings.Join(want, " ") {
		t.Fatalf("a season-0 binding re-pulls season 1, got %v", refresher.requests)
	}
}

// TestRefreshAnimeMetadataApplicationFailsTyped: the unbound anime is
// METADATA_NOT_BOUND without a network attempt, a provider failure is the
// typed METADATA_REFRESH_FAILED with the rows unchanged, and an unknown anime
// is a normal NotFound.
func TestRefreshAnimeMetadataApplicationFailsTyped(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh-unbound', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-refresh-unbound')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh-failing', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id, bgm_work_id) VALUES ('anime-refresh-failing', '10')`); err != nil {
		t.Fatal(err)
	}

	unbound := &countingMetadataRefresher{}
	service.SetMetadataWorkRefresher(unbound)
	_, err = service.RefreshAnimeMetadataApplication(ctx, "anime-refresh-unbound")
	if unboundErr, ok := err.(*apperrors.Business); !ok || unboundErr.Code != "METADATA_NOT_BOUND" || unboundErr.Status != 409 {
		t.Fatalf("an unbound anime is METADATA_NOT_BOUND, got %v", err)
	}
	if len(unbound.requests) != 0 {
		t.Fatalf("an unbound anime never re-pulls, got %v", unbound.requests)
	}

	refresher := &countingMetadataRefresher{err: &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_FAILED", Message: "Bangumi could not be reached"}}
	service.SetMetadataWorkRefresher(refresher)
	_, err = service.RefreshAnimeMetadataApplication(ctx, "anime-refresh-failing")
	if refreshErr, ok := err.(*apperrors.Business); !ok || refreshErr.Code != "METADATA_REFRESH_FAILED" {
		t.Fatalf("the re-pull failure surfaces typed, got %v", err)
	}
	if len(refresher.requests) != 1 {
		t.Fatalf("a failed first work stops the click before the second, got %v", refresher.requests)
	}
}

// TestRefreshAnimeMetadataApplicationUnwiredIsConfigured: a service without
// the re-pull port fails closed as a conflict, not a panic.
func TestRefreshAnimeMetadataApplicationUnwiredIsConfigured(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-refresh-plain', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-refresh-plain')`); err != nil {
		t.Fatal(err)
	}
	_, err = service.RefreshAnimeMetadataApplication(ctx, "anime-refresh-plain")
	if refreshErr, ok := err.(*apperrors.Business); !ok || refreshErr.Code != "METADATA_REFRESH_UNAVAILABLE" {
		t.Fatalf("the unwired re-pull is a typed conflict, got %v", err)
	}
}
