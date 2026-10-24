package namingfacts

// The resolver's own contract, over the disposable schema: only-if-used, the
// four block states, the cache-first property of a render, and the shape the
// snapshot's episode_titles block carries. The whitelisted callers' wiring is
// asserted in their own packages; here the Querier is driven directly.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// countingClient is one provider episode list whose answer the test sets and
// whose call count the assertions read.
type countingClient struct {
	mu    sync.Mutex
	list  episodetitle.List
	err   error
	calls int
}

func (f *countingClient) Episodes(context.Context, episodetitle.Key) (episodetitle.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return episodetitle.List{}, f.err
	}
	return f.list, nil
}

func (f *countingClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// titleRules compiles two naming rule sets: one whose TV video template
// references both ep_title facts and one that reads neither. The references —
// not the values — are what only-if-used reads.
func compileRules(t *testing.T, videoTemplate string) naming.CompiledNamingRules {
	t.Helper()
	engine := naming.NewEngine()
	rules := naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}",
			VideoFilenameTemplate: videoTemplate,
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)}.${file.ext}",
		},
	}
	compiled, err := engine.CompileRulesWithVars(rules, map[string]map[string]string{})
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	return compiled
}

func ctxWith(titleBgmID, titleTmdbID, tmdbKind string, tmdbSeason int, episodeDisplay string) naming.NamingContext {
	tmdb := naming.TMDBNamingFacts{WorkID: titleTmdbID, MediaKind: tmdbKind, Season: tmdbSeason, SeasonPresent: tmdbKind != ""}
	return naming.NamingContext{
		Anime: naming.AnimeNamingFacts{
			Title: "Show",
			BGM:   naming.BGMNamingFacts{WorkID: titleBgmID},
			TMDB:  tmdb,
		},
		Parsed: naming.ParsedNamingVariables{
			Episode: naming.EpisodeNamingVariables{Display: episodeDisplay, Raw: episodeDisplay, Number: 7},
		},
	}
}

func variantTemplates(rules naming.CompiledNamingRules) []naming.CompiledTemplate {
	return []naming.CompiledTemplate{rules.TV.Directory, rules.TV.VideoFilename}
}

func bothRules(t *testing.T) naming.CompiledNamingRules {
	return compileRules(t, "${sanitize(anime.title)} - ${ep.display} [${bgm.ep_title}] [tmdb: ${tmdb.ep_title}]")
}

func noRules(t *testing.T) naming.CompiledNamingRules {
	return compileRules(t, "${sanitize(anime.title)} - ${ep.display}")
}

func TestResolveOnlyQueriesReferencedFacts(t *testing.T) {
	bgm, tmdb := &countingClient{}, &countingClient{}
	db := disposabledb.ResetDisposableSchema(t)
	cache := &episodetitle.PersistentCache{
		DB:     db,
		Lookup: episodetitle.New(episodetitle.Options{Clients: map[episodetitle.Source]episodetitle.Client{episodetitle.SourceBGM: bgm, episodetitle.SourceTMDB: tmdb}}),
	}
	querier := &Querier{Cache: cache}

	// Neither template references an ep_title: nothing is assessed, no block
	// is written and the provider hears nothing even though the rendering
	// context is fully bound and carries an episode number.
	resolution, err := querier.Resolve(context.Background(), variantTemplates(noRules(t)), ctxWith("10", "42", "tv", 1, "1"))
	if err != nil {
		t.Fatalf("no-reference resolve: unexpected error %v", err)
	}
	if resolution.Assessed() || resolution.EpisodeTitlesBlock() != nil {
		t.Fatalf("no-reference resolve must stay unassessed: %+v", resolution)
	}
	if bgm.callCount()+tmdb.callCount() != 0 {
		t.Fatalf("no-reference resolve must never query, got bgm=%d tmdb=%d", bgm.callCount(), tmdb.callCount())
	}
	if resolution.Facts.BGM != "" || resolution.Facts.TMDB != "" {
		t.Fatalf("no-reference resolve must stay fact-free: %+v", resolution.Facts)
	}
}

func TestResolveUnreachableSourcesCarryTheirOwnStates(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	bgm, tmdb := &countingClient{}, &countingClient{}
	cache := &episodetitle.PersistentCache{
		DB:     db,
		Lookup: episodetitle.New(episodetitle.Options{Clients: map[episodetitle.Source]episodetitle.Client{episodetitle.SourceBGM: bgm, episodetitle.SourceTMDB: tmdb}}),
	}
	querier := &Querier{Cache: cache}
	templates := variantTemplates(bothRules(t))

	// No episode number at all: both sources answer no_episode and the block
	// carries a null episode key.
	resolution, err := querier.Resolve(context.Background(), templates, ctxWith("10", "42", "tv", 1, ""))
	if err != nil {
		t.Fatalf("no-episode resolve: unexpected error %v", err)
	}
	if resolution.BGM.State != ReasonNoEpisode || resolution.TMDB.State != ReasonNoEpisode {
		t.Fatalf("want no_episode both, got %+v", resolution)
	}
	if resolution.Episode != "" {
		t.Fatalf("episode is not resolvable, want empty, got %q", resolution.Episode)
	}
	block := resolution.EpisodeTitlesBlock()
	if block == nil || block["episode"] != nil {
		t.Fatalf("assessed no-episode resolve must write the block with a null episode: %+v", block)
	}
	if bgm.callCount()+tmdb.callCount() != 0 {
		t.Fatalf("no-episode resolve must never query, got bgm=%d tmdb=%d", bgm.callCount(), tmdb.callCount())
	}

	// Unbound: a referenced fact with no binding id is not_bound.
	resolution, err = querier.Resolve(context.Background(), templates, ctxWith("", "", "", 0, "1"))
	if err != nil {
		t.Fatalf("unbound resolve: unexpected error %v", err)
	}
	if resolution.BGM.State != ReasonNotBound || resolution.TMDB.State != ReasonNotBound {
		t.Fatalf("want not_bound both, got %+v", resolution)
	}
	if bgm.callCount()+tmdb.callCount() != 0 {
		t.Fatalf("unbound resolve must never query, got bgm=%d tmdb=%d", bgm.callCount(), tmdb.callCount())
	}

	// A movie-kind TMDB binding is not an episode source: absent, and the
	// episode-aware lookup only ever concerns a tv binding.
	resolution, err = querier.Resolve(context.Background(), templates, ctxWith("10", "55", "movie", 0, "1"))
	if err != nil {
		t.Fatalf("movie binding: unexpected error %v", err)
	}
	if resolution.TMDB.State != ReasonNotBound {
		t.Fatalf("movie TMDB binding must read not_bound, got %+v", resolution)
	}
}

func TestResolveKnownTitleIsCachedProjectAndStopsTheProvider(t *testing.T) {
	bgm := &countingClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 7, Title: "炎の七夕 祭り"}}}}
	tmdb := &countingClient{list: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: ""}}}}
	db := disposabledb.ResetDisposableSchema(t)
	cache := &episodetitle.PersistentCache{
		DB:     db,
		Lookup: episodetitle.New(episodetitle.Options{Clients: map[episodetitle.Source]episodetitle.Client{episodetitle.SourceBGM: bgm, episodetitle.SourceTMDB: tmdb}}),
	}
	querier := &Querier{Cache: cache}
	templates := variantTemplates(bothRules(t))

	resolution, err := querier.Resolve(context.Background(), templates, ctxWith("10", "42", "tv", 1, "7"))
	if err != nil {
		t.Fatalf("resolve: unexpected error %v", err)
	}
	// BGM keys season 0; the TMDB binding carries its per-season key.
	if !resolution.BGM.Known() || resolution.BGM.Value != "炎の七夕 祭り" {
		t.Fatalf("want BGM known, got %+v", resolution.BGM)
	}
	if resolution.TMDB.State != StateAbsent {
		t.Fatalf("want TMDB absent, got %+v", resolution.TMDB)
	}
	if resolution.Facts.BGM != "炎の七夕 祭り" || resolution.Facts.TMDB != "" {
		t.Fatalf("projection must carry the known title only: %+v", resolution.Facts)
	}
	if resolution.Episode != "7" {
		t.Fatalf("block episode key is the looked-up episode, got %q", resolution.Episode)
	}
	block := resolution.EpisodeTitlesBlock()
	bgmNode := block["bgm"].(map[string]any)
	if bgmNode["state"] != StateKnown || bgmNode["value"] != "炎の七夕 祭り" || bgmNode["reason_code"] != nil {
		t.Fatalf("BGM block node shape: %+v", bgmNode)
	}
	tmdbNode := block["tmdb"].(map[string]any)
	if !strings.EqualFold(tmdbNode["state"].(string), StateAbsent) || tmdbNode["value"] != nil {
		t.Fatalf("TMDB block node shape: %+v", tmdbNode)
	}

	// The same render again reads the stored rows: one fetch each, then
	// cache-first for the second.
	resolution, err = querier.Resolve(context.Background(), templates, ctxWith("10", "42", "tv", 1, "7"))
	if err != nil {
		t.Fatalf("second resolve: unexpected error %v", err)
	}
	if !resolution.BGM.Known() || resolution.Facts.BGM != "炎の七夕 祭り" {
		t.Fatalf("second resolve lost the cached title: %+v", resolution)
	}
	if bgm.callCount() != 1 || tmdb.callCount() != 1 {
		t.Fatalf("second resolve must be served from the cache, got bgm=%d tmdb=%d", bgm.callCount(), tmdb.callCount())
	}
}

func TestResolveNilCacheIsTransient(t *testing.T) {
	querier := &Querier{}
	_, err := querier.Resolve(context.Background(), variantTemplates(bothRules(t)), ctxWith("10", "42", "tv", 1, "1"))
	if !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("nil cache: want not-wired transient, got %v", err)
	}
	var nilQuerier *Querier
	_, err = nilQuerier.Resolve(context.Background(), variantTemplates(bothRules(t)), ctxWith("10", "42", "tv", 1, "1"))
	if err == nil {
		t.Fatal("nil querier must error, got nil")
	}
}
