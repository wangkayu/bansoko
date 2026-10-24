package domain

import "testing"

func TestReleaseFormatFromBangumiPlatform(t *testing.T) {
	cases := map[string]string{"TV": "tv", "OVA": "ova", "剧场版": "movie", "WEB": "ona", " TV ": "tv"}
	for platform, want := range cases {
		got := ReleaseFormatFromBangumiPlatform(platform)
		if got == nil || *got != want {
			t.Fatalf("platform %q: got %v, want %q", platform, got, want)
		}
	}
	for _, platform := range []string{"", "其他", "动态漫画", "tv", "Movie", "TVSP", "web"} {
		if got := ReleaseFormatFromBangumiPlatform(platform); got != nil {
			t.Fatalf("platform %q: got %q, want unknown", platform, *got)
		}
	}
}
