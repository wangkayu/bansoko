package domain

import "testing"

func TestReleaseDetailsDoNotReuseDownloadURL(t *testing.T) {
	for _, tc := range []struct{ name, item, want string }{
		{"enclosure only", `<title>Sample</title><enclosure url="https://feed.example.test/sample.torrent" type="application/x-bittorrent"/>`, ""},
		{"explicit page", `<title>Sample</title><link>https://feed.example.test/item/7</link><enclosure url="https://feed.example.test/sample.torrent" type="application/x-bittorrent"/>`, "https://feed.example.test/item/7"},
		{"download link", `<title>Sample</title><link>https://feed.example.test/sample.torrent</link>`, ""},
		{"comments page", `<title>Sample</title><comments>https://feed.example.test/item/7</comments><link>https://feed.example.test/sample.torrent</link>`, "https://feed.example.test/item/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFeedDetailed([]byte(`<rss version="2.0"><channel><item>` + tc.item + `</item></channel></rss>`))
			if err != nil || len(got.Items) != 1 {
				t.Fatalf("parse=%+v err=%v", got, err)
			}
			if got.Items[0].DetailsURL != tc.want {
				t.Fatalf("details=%q want=%q", got.Items[0].DetailsURL, tc.want)
			}
			if got.Items[0].DownloadURL() == "" {
				t.Fatal("download source lost")
			}
		})
	}
}

func TestAtomDetailsRequireIndependentAlternate(t *testing.T) {
	for _, page := range []string{"", "https://feed.example.test/item/7", "https://feed.example.test/sample.torrent"} {
		links := []atomLink{{Rel: "enclosure", Type: "application/x-bittorrent", Href: "https://feed.example.test/sample.torrent"}}
		if page != "" {
			links = append(links, atomLink{Rel: "alternate", Href: page})
		}
		item, _ := normalizeAtomEntry(atomEntry{Title: "Sample", Links: links})
		want := page
		if page == item.DownloadURL() {
			want = ""
		}
		if item.DetailsURL != want {
			t.Fatalf("page=%q details=%q", page, item.DetailsURL)
		}
	}
}

func TestProviderDetailsURL(t *testing.T) {
	for _, tc := range []struct {
		provider, raw string
		valid         bool
	}{
		{"anibt", "https://anibt.net/release/123", true},
		{"mikan", "https://mikanani.me/Home/Episode/abc", true},
		{"mikan", "https://mikanime.tv/Home/Episode/abc", true},
		{"nyaa", "https://nyaa.si/view/123", true},
		{"nyaa", "https://nyaa.si/download/123.torrent", false},
		{"anibt", "https://proxy.example.test/release/123", false},
		{"anibt", "https://anibt.net/release/", false},
		{"mikan", "https://anibt.net/release/123", false},
		{"anibt", "https://user@anibt.net/release/123", false},
	} {
		if got := ProviderDetailsURL(tc.provider, tc.raw); (got != "") != tc.valid {
			t.Fatalf("%s %s => %q", tc.provider, tc.raw, got)
		}
	}
}
