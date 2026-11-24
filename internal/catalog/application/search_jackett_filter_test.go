package application

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/jackett"
)

func TestJackettCategoryBlacklistMatrix(t *testing.T) {
	cat := func(id, name string) acquisitiondomain.SeedCategory {
		return acquisitiondomain.SeedCategory{ID: id, Name: name}
	}
	cases := []struct {
		name       string
		rawIDs     []string
		categories []acquisitiondomain.SeedCategory
		wantBlock  bool
	}{
		// --- Kept: U2 video releases ---
		{name: "u2 bdrip", rawIDs: []string{"5070", "100012", "2040"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100012", "BDRip"), cat("2040", "Movies/HD")}, wantBlock: false},
		{name: "u2 dvdrip", rawIDs: []string{"5070", "100013", "2020"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100013", "DVDRip"), cat("2020", "Movies/Other")}, wantBlock: false},
		{name: "u2 hdtv", rawIDs: []string{"5070", "100014", "2020"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100014", "HDTV"), cat("2020", "Movies/Other")}, wantBlock: false},
		{name: "u2 lqrip", rawIDs: []string{"5070", "100017", "2020"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100017", "LQRip"), cat("2020", "Movies/Other")}, wantBlock: false},
		{name: "u2 web", rawIDs: []string{"5070", "100413", "2020"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100413", "Web"), cat("2020", "Movies/Other")}, wantBlock: false},
		{name: "u2 others", rawIDs: []string{"8000", "100040"}, categories: []acquisitiondomain.SeedCategory{cat("8000", "Other"), cat("100040", "Others")}, wantBlock: false},

		// --- Kept: MonikaDesign video & live releases ---
		{name: "monika movie", rawIDs: []string{"2000", "100001"}, categories: []acquisitiondomain.SeedCategory{cat("2000", "Movies"), cat("100001", "Movie")}, wantBlock: false},
		{name: "monika tv", rawIDs: []string{"5000", "100002"}, categories: []acquisitiondomain.SeedCategory{cat("5000", "TV"), cat("100002", "TV")}, wantBlock: false},
		{name: "monika action live", rawIDs: []string{"2000", "100005"}, categories: []acquisitiondomain.SeedCategory{cat("2000", "Movies"), cat("100005", "Action Live")}, wantBlock: false},
		{name: "monika anime movie", rawIDs: []string{"2020", "100006"}, categories: []acquisitiondomain.SeedCategory{cat("2020", "Movies/Other"), cat("100006", "Anime Movie")}, wantBlock: false},
		{name: "monika anime live", rawIDs: []string{"2020", "100007"}, categories: []acquisitiondomain.SeedCategory{cat("2020", "Movies/Other"), cat("100007", "Anime Live")}, wantBlock: false},
		{name: "monika anime tv", rawIDs: []string{"5070", "100008"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100008", "Anime TV")}, wantBlock: false},

		// --- Kept: Qingwa general video, MV, and Other ---
		{name: "qingwa movie", rawIDs: []string{"2000", "100401"}, categories: []acquisitiondomain.SeedCategory{cat("2000", "Movies"), cat("100401", "电影")}, wantBlock: false},
		{name: "qingwa tv", rawIDs: []string{"5000", "100402"}, categories: []acquisitiondomain.SeedCategory{cat("5000", "TV"), cat("100402", "剧集")}, wantBlock: false},
		{name: "qingwa variety", rawIDs: []string{"5000", "100403"}, categories: []acquisitiondomain.SeedCategory{cat("5000", "TV"), cat("100403", "综艺")}, wantBlock: false},
		{name: "qingwa documentary", rawIDs: []string{"5080", "100404"}, categories: []acquisitiondomain.SeedCategory{cat("5080", "TV/Documentary"), cat("100404", "纪录片")}, wantBlock: false},
		{name: "qingwa anime", rawIDs: []string{"5070", "100405"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100405", "动漫")}, wantBlock: false},
		{name: "qingwa mv audio-video", rawIDs: []string{"3020", "100406"}, categories: []acquisitiondomain.SeedCategory{cat("3020", "Audio/Video"), cat("100406", "MV")}, wantBlock: false},
		{name: "qingwa sport", rawIDs: []string{"5060", "100407"}, categories: []acquisitiondomain.SeedCategory{cat("5060", "TV/Sport"), cat("100407", "体育")}, wantBlock: false},
		{name: "qingwa other", rawIDs: []string{"8000", "100409"}, categories: []acquisitiondomain.SeedCategory{cat("8000", "Other"), cat("100409", "其他")}, wantBlock: false},

		// --- Kept: common tracker false-positive guards ---
		{name: "movies bluray rip", rawIDs: []string{"2050"}, categories: []acquisitiondomain.SeedCategory{cat("2050", "Movies/BluRay")}, wantBlock: false},
		{name: "anime music video", rawIDs: []string{"5070", "100001"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100001", "Anime - Anime Music Video")}, wantBlock: false},
		{name: "anime raw", rawIDs: []string{"5072"}, categories: []acquisitiondomain.SeedCategory{cat("5072", "TV/Anime RAW")}, wantBlock: false},
		{name: "uncategorized fails open", rawIDs: nil, categories: nil, wantBlock: false},

		// --- Blocked: U2 discs, external disc structures, comics, audio ---
		{name: "u2 bdmv", rawIDs: []string{"5070", "100016", "2050"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100016", "BDMV"), cat("2050", "Movies/BluRay")}, wantBlock: true},
		{name: "u2 dvdiso", rawIDs: []string{"5070", "100015", "2020"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100015", "DVDISO"), cat("2020", "Movies/Other")}, wantBlock: true},
		{name: "u2 modded bluray", rawIDs: []string{"5070", "100412", "2050"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100412", "加流重灌 (Modded Blu-rays)"), cat("2050", "Movies/BluRay")}, wantBlock: true},
		{name: "u2 external structure", rawIDs: []string{"5070", "100410"}, categories: []acquisitiondomain.SeedCategory{cat("5070", "TV/Anime"), cat("100410", "外挂结构 (TV Series)")}, wantBlock: true},
		{name: "u2 raw books", rawIDs: []string{"7000", "100021"}, categories: []acquisitiondomain.SeedCategory{cat("7000", "Books"), cat("100021", "原版漫画 (Raw Books)")}, wantBlock: true},
		{name: "u2 translated books", rawIDs: []string{"7000", "100022"}, categories: []acquisitiondomain.SeedCategory{cat("7000", "Books"), cat("100022", "翻译漫画 (Translated Books)")}, wantBlock: true},
		{name: "u2 lossless music", rawIDs: []string{"3000", "100030"}, categories: []acquisitiondomain.SeedCategory{cat("3000", "Audio"), cat("100030", "Lossless Music")}, wantBlock: true},

		// --- Blocked: MonikaDesign & Qingwa pure audio ---
		{name: "monika music of movie", rawIDs: []string{"3000", "100003"}, categories: []acquisitiondomain.SeedCategory{cat("3000", "Audio"), cat("100003", "Music of Movie")}, wantBlock: true},
		{name: "monika music of tv", rawIDs: []string{"3000", "100009"}, categories: []acquisitiondomain.SeedCategory{cat("3000", "Audio"), cat("100009", "Music of TV")}, wantBlock: true},
		{name: "qingwa music", rawIDs: []string{"3000", "100408"}, categories: []acquisitiondomain.SeedCategory{cat("3000", "Audio"), cat("100408", "音乐")}, wantBlock: true},

		// --- Blocked: standard IDs even without caps ---
		{name: "standard books without caps", rawIDs: []string{"7030"}, categories: nil, wantBlock: true},
		{name: "standard console without caps", rawIDs: []string{"1180"}, categories: nil, wantBlock: true},
		{name: "standard pc games without caps", rawIDs: []string{"4050"}, categories: nil, wantBlock: true},
		{name: "standard lossless audio without caps", rawIDs: []string{"3040"}, categories: nil, wantBlock: true},

		// --- Blocked: other common tracker custom category names ---
		{name: "mteam movie raw disc", rawIDs: []string{"2050", "100421"}, categories: []acquisitiondomain.SeedCategory{cat("2050", "Movies/BluRay"), cat("100421", "Movie(原盘)")}, wantBlock: true},
		{name: "generic bdiso", rawIDs: []string{"2050", "100100"}, categories: []acquisitiondomain.SeedCategory{cat("2050", "Movies/BluRay"), cat("100100", "BDISO")}, wantBlock: true},
		{name: "generic bd50", rawIDs: []string{"2050", "100101"}, categories: []acquisitiondomain.SeedCategory{cat("2050", "Movies/BluRay"), cat("100101", "Movies/BD50")}, wantBlock: true},
		{name: "nyaa literature under other", rawIDs: []string{"8000", "100301"}, categories: []acquisitiondomain.SeedCategory{cat("8000", "Other"), cat("100301", "Literature - English-translated")}, wantBlock: true},
		{name: "nyaa pictures", rawIDs: []string{"8000", "100501"}, categories: []acquisitiondomain.SeedCategory{cat("8000", "Other"), cat("100501", "Pictures - Graphics")}, wantBlock: true},
		{name: "ebook custom", rawIDs: []string{"8000", "100701"}, categories: []acquisitiondomain.SeedCategory{cat("8000", "Other"), cat("100701", "电子书 / E-Book")}, wantBlock: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := acquisitiondomain.FeedItem{
				SeedCategoryIDs: tc.rawIDs,
				SeedCategories:  tc.categories,
			}
			got := isJackettItemBlacklistedByCategory(item)
			if got != tc.wantBlock {
				t.Fatalf("isJackettItemBlacklistedByCategory(%s) = %v, want %v", tc.name, got, tc.wantBlock)
			}
		})
	}
}

func TestJackettSearchActivelyFiltersBlacklistedCategories(t *testing.T) {
	searchBody := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>Jackett</title>
<item><title>[BDRip] Kept Anime</title><guid>kept_bdrip</guid>
<link>https://tracker.example.test/download/kept_bdrip.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate>
<torznab:attr name="category" value="5070" /><torznab:attr name="category" value="100012" />
<jackettindexer id="u2-filter-test">U2</jackettindexer><type>private</type>
</item>
<item><title>[BDMV] Blocked Disc</title><guid>blocked_bdmv</guid>
<link>https://tracker.example.test/download/blocked_bdmv.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:01:47 +0800</pubDate>
<torznab:attr name="category" value="5070" /><torznab:attr name="category" value="100016" /><torznab:attr name="category" value="2050" />
<jackettindexer id="u2-filter-test">U2</jackettindexer><type>private</type>
</item>
<item><title>[外挂结构] Blocked External Structure</title><guid>blocked_ext</guid>
<link>https://tracker.example.test/download/blocked_ext.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:02:47 +0800</pubDate>
<torznab:attr name="category" value="5070" /><torznab:attr name="category" value="100410" />
<jackettindexer id="u2-filter-test">U2</jackettindexer><type>private</type>
</item>
<item><title>[漫画] Blocked Comic</title><guid>blocked_comic</guid>
<link>https://tracker.example.test/download/blocked_comic.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:03:47 +0800</pubDate>
<torznab:attr name="category" value="7000" /><torznab:attr name="category" value="100022" />
<jackettindexer id="u2-filter-test">U2</jackettindexer><type>private</type>
</item>
<item><title>[MV] Kept Concert Video</title><guid>kept_mv</guid>
<link>https://tracker.example.test/download/kept_mv.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:04:47 +0800</pubDate>
<torznab:attr name="category" value="3020" /><torznab:attr name="category" value="100406" />
<jackettindexer id="qingwa-filter-test">Qingwa</jackettindexer><type>private</type>
</item>
</channel></rss>`

	u2Caps := `<?xml version="1.0" encoding="UTF-8"?>
<caps><categories>
<category id="2000" name="Movies"><subcat id="2050" name="Movies/BluRay" /></category>
<category id="5000" name="TV"><subcat id="5070" name="TV/Anime" /></category>
<category id="7000" name="Books" />
<category id="100012" name="BDRip" />
<category id="100016" name="BDMV" />
<category id="100022" name="翻译漫画 (Translated Books)" />
<category id="100410" name="外挂结构 (TV Series)" />
</categories></caps>`

	qingwaCaps := `<?xml version="1.0" encoding="UTF-8"?>
<caps><categories>
<category id="3000" name="Audio"><subcat id="3020" name="Audio/Video" /></category>
<category id="100406" name="MV" />
</categories></caps>`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case jackett.AggregateTorznabPath:
			_, _ = io.WriteString(w, searchBody)
		case "/api/v2.0/indexers/u2-filter-test/results/torznab/api":
			_, _ = io.WriteString(w, u2Caps)
		case "/api/v2.0/indexers/qingwa-filter-test/results/torznab/api":
			_, _ = io.WriteString(w, qingwaCaps)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	searcher := HTTPJackettSearcher{Endpoint: upstream.URL, APIKey: jackettTestAPIKey, Client: upstream.Client()}
	result, err := searcher.SearchJackettSeeds(context.Background(), "葬送的芙莉莲")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(result.Items) != 2 || len(result.Parse.Items) != 2 {
		t.Fatalf("filtered items = %d (parse=%d), want 2", len(result.Items), len(result.Parse.Items))
	}
	if result.Items[0].GUID != "kept_bdrip" || result.Items[1].GUID != "kept_mv" {
		t.Fatalf("kept GUIDs = [%s, %s], want [kept_bdrip, kept_mv]", result.Items[0].GUID, result.Items[1].GUID)
	}
}

func TestJackettSearchAllFilteredReturnsEmptyResultNotFeedError(t *testing.T) {
	searchBody := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>Jackett</title>
<item><title>[漫画] Only Comic</title><guid>only_comic</guid>
<link>https://tracker.example.test/download/only_comic.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate>
<torznab:attr name="category" value="7030" />
<jackettindexer id="caps-down-site">Caps Down</jackettindexer><type>private</type>
</item>
</channel></rss>`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == jackett.AggregateTorznabPath {
			_, _ = io.WriteString(w, searchBody)
			return
		}
		// Caps failure must still allow Layer 1 standard Torznab ID blacklist to work.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	searcher := HTTPJackettSearcher{Endpoint: upstream.URL, APIKey: jackettTestAPIKey, Client: upstream.Client()}
	result, err := searcher.SearchJackettSeeds(context.Background(), "漫画")
	if err != nil {
		t.Fatalf("all-filtered search returned error instead of empty result: %v", err)
	}
	if len(result.Items) != 0 || len(result.Parse.Items) != 0 {
		t.Fatalf("expected 0 kept items, got %d", len(result.Items))
	}
}
