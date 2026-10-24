package httpapi

import (
	"testing"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
)

func TestMapRSSListSeedFactsAniBTTags(t *testing.T) {
	if mapped := mapRSSListSeedFacts(readmodelapplication.RSSListSeedFacts{Title: "x"}); mapped.AnibtTags != nil {
		t.Fatal("non-AniBT seed must not carry anibt_tags")
	}
	tags := []string{}
	if mapped := mapRSSListSeedFacts(readmodelapplication.RSSListSeedFacts{Title: "x", AniBTTags: &tags}); mapped.AnibtTags == nil || len(*mapped.AnibtTags) != 0 {
		t.Fatal("an empty AniBT tag set must be present and empty")
	}
	tags = []string{"language:CHS", "tag:HEVC"}
	mapped := mapRSSListSeedFacts(readmodelapplication.RSSListSeedFacts{Title: "x", AniBTTags: &tags})
	if mapped.AnibtTags == nil || len(*mapped.AnibtTags) != 2 || (*mapped.AnibtTags)[1] != "tag:HEVC" {
		t.Fatalf("anibt_tags = %v", mapped.AnibtTags)
	}
}

func TestMapRSSListSeedFactsTitleTags(t *testing.T) {
	tags := []string{"resolution:1080p", "sub_lang:SC"}
	mapped := mapRSSListSeedFacts(readmodelapplication.RSSListSeedFacts{Title: "x", TitleTags: &tags})
	if mapped.TitleTags == nil || len(*mapped.TitleTags) != 2 || (*mapped.TitleTags)[1] != "sub_lang:SC" {
		t.Fatalf("title_tags = %v", mapped.TitleTags)
	}
}
