package redact

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// Logs (observability runtime logger), diagnostics, notification and
// activity text pass through Apply: any URL, a path-keyed one included, is
// masked there whole.
func TestTextMasksTrackerURLs(t *testing.T) {
	for _, value := range []string{
		"refresh failed for https://t.example.test/torrents/download/7." + fakeHexKey,
		"feed https://t.example.test/rss/12." + fakeHexKey + " unreachable",
	} {
		if got := Text(value, 512); got != Redacted {
			t.Fatalf("Text(%q) = %q", value, got)
		}
		if got, _ := Apply(value, 512, MaskFragments, Redacted); strings.Contains(got, fakeHexKey) {
			t.Fatalf("MaskFragments kept the key: %q", got)
		}
	}
}

func TestURLErrorRedactsTheRequestURL(t *testing.T) {
	inner := errors.New("dial tcp: i/o timeout")
	err := URLError(&url.Error{Op: "Get", URL: "https://pt.example.test/download.php?id=1&passkey=" + fakeHexKey, Err: inner})
	if strings.Contains(err.Error(), fakeHexKey) || !strings.Contains(err.Error(), "passkey=REDACTED") || !errors.Is(err, inner) {
		t.Fatalf("URLError = %v", err)
	}
	other := errors.New("plain")
	if URLError(other) != other || URLError(nil) != nil {
		t.Fatal("non-url errors pass through")
	}
}

// Synthetic keys only: none of these is a real tracker credential.
const (
	fakeHexKey = "0123456789abcdef0123456789abcdef"
	fakeToken  = "Zx9Qw8Er7Ty6Ui5Op4As3Df2Gh1Jk0Lm"
)

func TestURLRedactsTrackerCredentials(t *testing.T) {
	cases := map[string]struct{ raw, want string }{
		"unit3d download":   {"https://t.example.test/torrents/download/29577." + fakeHexKey, "https://t.example.test/torrents/download/29577.REDACTED"},
		"unit3d rss path":   {"https://t.example.test/rss/12." + fakeHexKey, "https://t.example.test/rss/12.REDACTED"},
		"unit3d rsskey":     {"https://t.example.test/rss?rsskey=abc123&cat=5", "https://t.example.test/rss?rsskey=REDACTED&cat=5"},
		"nexusphp passkey":  {"https://pt.example.test/download.php?id=1234&passkey=" + fakeHexKey + "&https=1", "https://pt.example.test/download.php?id=1234&passkey=REDACTED&https=1"},
		"nexusphp rss":      {"https://pt.example.test/torrentrss.php?rows=10&linktype=dl&passkey=abc", "https://pt.example.test/torrentrss.php?rows=10&linktype=dl&passkey=REDACTED"},
		"apikey":            {"https://x.example.test/api?apikey=abc&q=show", "https://x.example.test/api?apikey=REDACTED&q=show"},
		"api_key":           {"https://x.example.test/api?api_key=abc", "https://x.example.test/api?api_key=REDACTED"},
		"token":             {"https://x.example.test/feed?token=abc", "https://x.example.test/feed?token=REDACTED"},
		"auth":              {"https://x.example.test/feed?auth=abc", "https://x.example.test/feed?auth=REDACTED"},
		"authkey":           {"https://x.example.test/feed?authkey=abc", "https://x.example.test/feed?authkey=REDACTED"},
		"key":               {"https://x.example.test/feed?key=abc", "https://x.example.test/feed?key=REDACTED"},
		"sign":              {"https://x.example.test/feed?sign=abc&t=1", "https://x.example.test/feed?sign=REDACTED&t=1"},
		"secret":            {"https://x.example.test/feed?secret=abc", "https://x.example.test/feed?secret=REDACTED"},
		"torrent_pass":      {"https://x.example.test/dl?torrent_pass=abc", "https://x.example.test/dl?torrent_pass=REDACTED"},
		"uid and pass pair": {"https://x.example.test/rss?uid=42&pass=abc", "https://x.example.test/rss?uid=REDACTED&pass=REDACTED"},
		"opaque value":      {"https://x.example.test/rss?x=" + fakeHexKey, "https://x.example.test/rss?x=REDACTED"},
		"opaque path token": {"https://x.example.test/feeds/" + fakeToken + "/rss.xml", "https://x.example.test/feeds/REDACTED/rss.xml"},
		"userinfo":          {"https://user:pw@x.example.test/rss", "https://x.example.test/rss"},
		"fragment":          {"https://x.example.test/rss#k=abc", "https://x.example.test/rss"},
		"unparseable":       {"not a url", URLKey},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := URL(tc.raw); got != tc.want {
				t.Fatalf("URL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestURLKeepsPublicURLs(t *testing.T) {
	for _, raw := range []string{
		"https://mikanani.me/RSS/Bangumi?bangumiId=3519&subgroupid=583",
		"https://mikanani.me/Download/20261019/084c615f82f1e9577ce3bf115069cf3c59111065.torrent",
		"https://nyaa.si/download/1234567.torrent",
		"https://nyaa.si/?page=rss&q=show&c=1_2",
		"https://anibt.net/api/torrent/rel_gXq43BXIlSAT.torrent",
		"https://acg.rip/t/321123.torrent",
		"https://pt.example.test/download.php?id=1234",
	} {
		if got := URL(raw); got != raw {
			t.Fatalf("URL(%q) = %q, want unchanged", raw, got)
		}
	}
}

func TestURLsInTextRedactsEmbeddedURLs(t *testing.T) {
	text := `fetch torrent metadata: Get "https://t.example.test/torrents/download/7.` + fakeHexKey + `": dial tcp: timeout`
	got := URLsInText(text)
	if strings.Contains(got, fakeHexKey) || !strings.Contains(got, `Get "https://t.example.test/torrents/download/7.REDACTED": dial tcp: timeout`) {
		t.Fatalf("URLsInText = %q", got)
	}
	plain := "RSS source returned HTTP 503"
	if URLsInText(plain) != plain {
		t.Fatal("text without a URL must be unchanged")
	}
	sentence := "see https://pt.example.test/rss?passkey=abc."
	if got := URLsInText(sentence); got != "see https://pt.example.test/rss?passkey=REDACTED." {
		t.Fatalf("trailing punctuation: %q", got)
	}
	if !ContainsCredential("https://pt.example.test/rss?passkey=abc") || ContainsCredential("https://nyaa.si/?page=rss") {
		t.Fatal("ContainsCredential")
	}
}
