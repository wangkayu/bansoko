package filemaintenance

// The whitelisted maintenance renders and the alignment variant over the
// disposable schema. Two contract points the owner decree of 2026-10-22 pins
// on this package:
//
//   - every plain alignment render is cache-first: the first provider fetch
//     seeds the store once and everything after that — a second plain
//     alignment included — hears no provider request, and a render whose
//     templates never reference an ep_title fact stays network-free anyway;
//   - the 对齐（刷新元数据）variant is the only positive-cache bypass: it
//     re-pulls through the metadata refresher exactly once before admitting
//     alignment ops, a failed re-pull admits nothing, and the unwired refresher
//     is a typed conflict that leaves plain alignment untouched.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/episodetitle"
	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/filemaintenance/planner"
	naming "bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// countingEpisodeClient is one provider episode list whose call count the
// assertions read.
type countingEpisodeClient struct {
	mu    sync.Mutex
	list  episodetitle.List
	calls int
}

func (f *countingEpisodeClient) Episodes(context.Context, episodetitle.Key) (episodetitle.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.list, nil
}

func (f *countingEpisodeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingRefresher is the metadata re-pull port the variant reaches; the
// count pins "exactly one re-pull per click".
type countingRefresher struct {
	mu    sync.Mutex
	calls int
	fail  error
}

func (r *countingRefresher) RefreshAnimeMetadataApplication(context.Context, string) (struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return struct{}{}, r.fail
}

func (r *countingRefresher) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// episodeTitleRules are the alignment rules carrying one TMDB episode title
// reference in the video filename.
func episodeTitleRules() naming.NamingRules {
	rules := rendererRules()
	rules.TV.VideoFilenameTemplate = "${sanitize(anime.title)} - E${pad(ep.display, 2)} [${tmdb.ep_title}]"
	return rules
}

// wireRenderQuerier hands the renderer the same persistent cache lane, with
// the caller's fake provider, over the fixture's own database.
func wireRenderQuerier(db *sql.DB, renderer *NamingTargetRenderer, tmdb episodetitle.Client) {
	renderer.SetEpisodeTitleQueries(&namingfacts.Querier{Cache: &episodetitle.PersistentCache{
		DB:     db,
		Lookup: episodetitle.New(episodetitle.Options{Clients: map[episodetitle.Source]episodetitle.Client{episodetitle.SourceTMDB: tmdb}}),
	}})
}

// seedVariantAnime seeds one started anime with one drifted placed chain and
// settles its calibration, the routine the plain align test runs. The chain is
// bound: its render snapshot records the tmdb binding the naming context
// reads, so the episode-title lookup the render whitelists is able to run.
func seedVariantAnime(t *testing.T, fixture *calibrationFixture, animeID, subKey string, rules naming.NamingRules) {
	t.Helper()
	seedAnimeSubscription(t, fixture.db, animeID, "sub-"+subKey, "stopped")
	seedAnimeForRenderer(t, fixture.db, animeID, "Show", "tv")
	seedInstanceReplacement(t, fixture.db, animeID, ModeAll, ``, 1)
	seedCompletedGenerationWithRenderChains(t, fixture.db, animeID, 1, map[string]string{
		"/library/EP 01.mkv": "fp-" + subKey,
	}, rules, &frozenRenderBindingFacts{MediaKind: "tv", Season: 1, SeasonBound: true, TMDBWorkID: "work-" + animeID})
	if _, err := fixture.db.Exec(`
UPDATE reconciliation_files rf SET fingerprint = (p.source_fingerprint::jsonb)->>'value'
FROM placements p
WHERE p.id = rf.facts->>'placement_id' AND rf.managed_anime_id = $1`, animeID); err != nil {
		t.Fatal(err)
	}
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
}

// TestAlignVariantRePullsOnceBeforeAdmitting runs both actions over one
// calibrated generation.
func TestAlignVariantRePullsOnceBeforeAdmitting(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	ctx := context.Background()
	const animeID = "anime-align-variant"
	rules := episodeTitleRules()
	tmdb := &countingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Cached Title"}}}}

	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(fixture.db, renderer, tmdb)
	fixture.calibration.planner = NewPlanner(NewReconciliationSnapshotReader(fixture.db), renderer)
	seedVariantAnime(t, fixture, animeID, "align-variant", rules)

	service := NewService(fixture.calibration.policies, nil, fixture.calibration.planner, fixture.calibration.placements, nil)
	service.SetOwnerTruthWires(fixture.calibration, nil, fixture.calibration.alignment, nil)
	service.SetPathDiff(NewPathDiffService(fixture.db, renderer, nil))
	refresher := &countingRefresher{}
	service.SetMetadataWorkRefresher(refresher)

	plain, err := service.AlignAnimeFilesApplication(ctx, animeID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Admitted != 1 {
		t.Fatalf("plain alignment must admit the file, got %+v", plain)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("the plain action must never re-pull, got %d", refresher.callCount())
	}
	if tmdb.callCount() != 1 {
		t.Fatalf("planning must fetch the season once, got %d", tmdb.callCount())
	}

	// The variant re-pulls exactly once per click, before it admits. Its own
	// re-pull does not re-fetch the render cache: the store answers.
	result, err := service.AlignAnimeFilesWithRefreshApplication(ctx, animeID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Admitted != 0 {
		t.Fatalf("a replayed admission stays empty: %+v", result)
	}
	if refresher.callCount() != 1 {
		t.Fatalf("the variant re-pulls exactly once per click, got %d", refresher.callCount())
	}
	if tmdb.callCount() != 1 {
		t.Fatalf("the kind of re-pull the variant triggers leaves the render cache in place, got %d", tmdb.callCount())
	}

	// A second click re-pulls again. A failed re-pull admits nothing.
	refresher.mu.Lock()
	refresher.fail = errors.New("offline")
	refresher.mu.Unlock()
	if _, err := service.AlignAnimeFilesWithRefreshApplication(ctx, animeID, 1, nil); err == nil {
		t.Fatal("a failed re-pull must fail the variant")
	}
	if refresher.callCount() != 2 {
		t.Fatalf("every click re-pulls, calls=%d", refresher.callCount())
	}
	if admissions := countAlignmentOperations(t, fixture.db, animeID); admissions != 1 {
		t.Fatalf("a failed re-pull admits nothing, admissions=%d", admissions)
	}

	// No wired refresher: the variant is a typed conflict.
	bare := NewService(fixture.calibration.policies, nil, fixture.calibration.planner, fixture.calibration.placements, nil)
	bare.SetOwnerTruthWires(fixture.calibration, nil, fixture.calibration.alignment, nil)
	bare.SetPathDiff(NewPathDiffService(fixture.db, renderer, nil))
	_, err = bare.AlignAnimeFilesWithRefreshApplication(ctx, animeID, 1, nil)
	var conflict *apperrors.Business
	if err == nil || !errors.As(err, &conflict) || conflict.Code != "METADATA_REFRESH_UNAVAILABLE" || conflict.Status != 409 {
		t.Fatalf("unwired refresher must carry METADATA_REFRESH_UNAVAILABLE, got %v", err)
	}
	if plain2, err2 := bare.AlignAnimeFilesApplication(ctx, animeID, 1, nil); err2 != nil || plain2.Admitted != 0 {
		t.Fatalf("the plain action ignores the refresher entirely: %+v %v", plain2, err2)
	}
}

func countAlignmentOperations(t *testing.T, db *sql.DB, animeID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alignment_operations WHERE managed_anime_id = $1`, animeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// renderTargetOnce renders one placed chain through the whitelisted alignment
// candidate render.
func renderTargetOnce(t *testing.T, renderer *NamingTargetRenderer, animeID, path, placementID string) (string, error) {
	t.Helper()
	return renderer.RenderTarget(context.Background(), planner.SnapshotObservation{
		AnimeID: animeID, PlacementID: placementID, FilePath: path, SizeBytes: 1024,
	})
}

// TestAlignmentRenderIsCacheFirst seeds a bound chain and renders it twice:
// the first render fetches the season from the provider once through the
// store, the second is served entirely from the stored rows and renders the
// same target.
func TestAlignmentRenderIsCacheFirst(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	rules := episodeTitleRules()
	tmdb := &countingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Cached Title"}}}}
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(db, renderer, tmdb)

	const animeID = "anime-eptitle-cache"
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "ep-title-cache", AnimeID: animeID, AnimeTitle: "Show",
		TMDBMediaKind: "tv", TMDBSeason: 1,
		ReleaseTitle: "[Group] Show - 1",
		PlacedPath:   "/library/EP 01.mkv", Rules: rules,
		BindingFacts: &frozenRenderBindingFacts{MediaKind: "tv", Season: 1, SeasonBound: true, TMDBWorkID: "work-" + animeID},
		Coverage:     []string{"tv:s1:e1"},
	})
	target, err := renderTargetOnce(t, renderer, animeID, "/library/EP 01.mkv", chain.PlacementID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "[Cached Title]") {
		t.Fatalf("first render must carry the looked-up title, got %q", target)
	}
	if got := tmdb.callCount(); got != 1 {
		t.Fatalf("first render fetches the season once, got %d", got)
	}
	again, err := renderTargetOnce(t, renderer, animeID, "/library/EP 01.mkv", chain.PlacementID)
	if err != nil {
		t.Fatal(err)
	}
	if again != target {
		t.Fatalf("the second render must reproduce the same target: %q vs %q", again, target)
	}
	if got := tmdb.callCount(); got != 1 {
		t.Fatalf("the second render must be served from the store, provider calls=%d", got)
	}
}

// TestAlignmentRenderUnboundRefusesUndefinedAndNeverQueries: a referenced
// ep_title fact on a chain whose tmdb namespace has no binding is a template
// reading an undefined variable — the render is refused without any network
// call, the refused name is the resolver's not_bound state and never a query.
func TestAlignmentRenderUnboundRefusesUndefinedAndNeverQueries(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	rules := episodeTitleRules()
	tmdb := &countingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "Cached Title"}}}}
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(db, renderer, tmdb)

	const animeID = "anime-eptitle-unbound"
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "ep-title-unbound", AnimeID: animeID, AnimeTitle: "Show",
		TMDBMediaKind: "tv", TMDBSeason: 1,
		ReleaseTitle: "[Group] Show - 1",
		PlacedPath:   "/library/EP 01.mkv", Rules: rules,
		Coverage: []string{"tv:s1:e1"},
	})
	_, err := renderTargetOnce(t, renderer, animeID, "/library/EP 01.mkv", chain.PlacementID)
	var undefined *naming.Error
	if err == nil || !errors.As(err, &undefined) || undefined.Code != naming.ErrCodeNamingFactUndefined {
		t.Fatalf("a chain whose tmdb namespace is unbound refuses as undefined, got %v", err)
	}
	if got := tmdb.callCount(); got != 0 {
		t.Fatalf("an unbound chain never queries, got %d", got)
	}
}
