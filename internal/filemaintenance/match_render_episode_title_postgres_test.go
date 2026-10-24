package filemaintenance

// The file-manager match render is the third whitelisted episode-title render
// point: a loaded template that reads tmdb.ep_title / bgm.ep_title is resolved
// from the persistent cache before the render — strictly cache-first, one
// (source, work, season, episode) key per the chosen scope, and it writes
// nothing. A lookup failure that is not a definite answer is this match's
// typed episode_title_lookup_failed diagnostic, and an undefined namespace
// (a reference with no TMDB binding) is the naming refusal, never a query.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// matchCountingEpisodeClient is a swap-able, failing episode list whose call
// count the assertions read; the alignment fixture's plain counting client
// never fails, which these diagnostics have to model too.
type matchCountingEpisodeClient struct {
	mu    sync.Mutex
	calls int
	list  episodetitle.List
	err   error
}

func (c *matchCountingEpisodeClient) Episodes(context.Context, episodetitle.Key) (episodetitle.List, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return episodetitle.List{}, c.err
	}
	return c.list, nil
}

func (c *matchCountingEpisodeClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *matchCountingEpisodeClient) swap(list episodetitle.List, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list, c.err = list, err
}

func matchEpisodeTitleRules(filename string) naming.NamingRules {
	rules := matchRules(filename)
	rules.Unknown = rules.TV
	return rules
}

// One lineage match whose chosen season and episode (2, 7) resolve the chosen
// title from the provider, then re-resolve cache-first for every further
// match that names the same episode.
func TestRenderMatchEpisodeTitleResolvesCacheFirst(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "match-title", AnimeID: "anime-match-title", AnimeTitle: "测试番组", TMDBMediaKind: "tv", TMDBSeason: 1,
		SourceGroup: "Group", ReleaseTitle: "[Group] 测试番组 - 03", PlacedPath: "/library/Anime/EP07.mkv",
		DownloadName: "EP07.mkv", Rules: rendererRules(), FrozenEpisode: "3",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)

	client := &matchCountingEpisodeClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 7, Title: "Chosen 7"}}}}
	rules := matchEpisodeTitleRules("[${tmdb.ep_title}] ${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}")
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(db, renderer, client)
	input := MatchRenderInput{ManagedAnimeID: "anime-match-title", LineagePlacementID: chain.PlacementID, FileName: "EP07.mkv", SizeBytes: 1024, Season: 2, Episode: 7}

	got, err := renderer.RenderMatch(ctx, input)
	if err != nil || got.Path != "/library/测试番组/Season 02/[Chosen 7] 测试番组 - E07.mkv" || !got.Lineage {
		t.Fatalf("first match = %+v, %v", got, err)
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("first match ran %d provider calls, want 1", calls)
	}

	// The chosen key is (tmdb, bound work, chosen season 2, episode 7): a
	// changed provider answer cannot leak into a cache-first re-match.
	client.swap(episodetitle.List{Episodes: []episodetitle.Episode{{Number: 7, Title: "Changed"}}}, nil)
	for round := 0; round < 2; round++ {
		got, err = renderer.RenderMatch(ctx, input)
		if err != nil || got.Path != "/library/测试番组/Season 02/[Chosen 7] 测试番组 - E07.mkv" {
			t.Fatalf("round %d = %+v, %v", round, got, err)
		}
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("the stored answer never expires, got %d calls", calls)
	}
}

// A transient lookup failure is the match's typed diagnostic, not a guessed
// target and not a facts refusal.
func TestRenderMatchEpisodeTitleLookupStaysTyped(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "match-lookup", AnimeID: "anime-match-lookup", AnimeTitle: "测试番组", TMDBMediaKind: "tv", TMDBSeason: 1,
		SourceGroup: "Group", ReleaseTitle: "[Group] 测试番组 - 03", PlacedPath: "/library/Anime/EP07.mkv",
		DownloadName: "EP07.mkv", Rules: rendererRules(), FrozenEpisode: "3",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)

	client := &matchCountingEpisodeClient{err: errors.New("provider unreachable")}
	rules := matchEpisodeTitleRules("[${tmdb.ep_title}] ${sanitize(anime.title)}.${file.ext}")
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(db, renderer, client)
	_, err := renderer.RenderMatch(ctx, MatchRenderInput{
		ManagedAnimeID: "anime-match-lookup", LineagePlacementID: chain.PlacementID, FileName: "EP07.mkv", SizeBytes: 1024, Season: 1, Episode: 7,
	})
	var failure *MatchRenderError
	if !errors.As(err, &failure) || failure.Code != MatchRenderEpisodeTitleLookup {
		t.Fatalf("a transient lookup = %v, want %s", err, MatchRenderEpisodeTitleLookup)
	}
	if !strings.Contains(err.Error(), episodetitle.ErrTransient.Error()) {
		t.Fatalf("the diagnostic must carry the transient cause: %v", err)
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("the failed lookup ran %d calls, want 1", calls)
	}
}

// ${tmdb.*} without a TMDB binding is the naming undefined refusal and never a
// query — the match refuses the same way the rename render does.
func TestRenderMatchUnboundEpisodeTitleRefersUndefinedAndNeverQueries(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeForRenderer(t, db, "anime-match-unbound", "无出处番", "")
	client := &matchCountingEpisodeClient{err: errors.New("an undefined namespace never queries")}
	rules := matchEpisodeTitleRules("[${tmdb.ep_title}] ${sanitize(anime.title)}.${file.ext}")
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	wireRenderQuerier(db, renderer, client)
	_, err := renderer.RenderMatch(ctx, MatchRenderInput{
		ManagedAnimeID: "anime-match-unbound", FileName: "EP07.mkv", SizeBytes: 1024, Season: 1, Episode: 7,
	})
	var failure *MatchRenderError
	if !errors.As(err, &failure) || failure.Code != MatchRenderFactsMissing || len(failure.Facts) != 1 || failure.Facts[0] != "tmdb.ep_title" {
		t.Fatalf("unbound reference = %v, want the undefined refusal for tmdb.ep_title", err)
	}
	if calls := client.callCount(); calls != 0 {
		t.Fatalf("the refusal happens before any lookup, got %d calls", calls)
	}
}
