package namingvars

import (
	"encoding/json"
	"testing"

	"bansoko/internal/templatefacts"
)

func TestBuildInputFactsAndPayloadContracts(t *testing.T) {
	season := int64(2)
	evidence := ReleaseEvidence{
		AnimeEvidence: AnimeEvidence{AnimeID: "a1", AnimeTitle: "Saved", BgmID: "123", BgmTitleCN: "中文", TmdbID: "9", TmdbKind: "tv", TmdbSeason: &season},
		ReleaseTitle:  "[G] Show - 03", ReleaseID: "r1",
		Files:   []File{{Name: "a.mkv", Path: "a.mkv", Size: 10}, {Name: "b.ass", Path: "b.ass", Size: 5}},
		Episode: &EpisodeEvidence{Raw: "03", Number: "3", Display: "3", Offset: 0},
		HashV1:  "0123456789abcdef0123456789abcdef01234567",
		Tags:    templatefacts.Facts{Reltitle: templatefacts.Values{"Resolution": "1080p"}},
	}
	input := BuildInput(evidence)
	payload := input.Payload()
	anime := payload["anime"].(map[string]any)
	// anime.title is the effective chain value (BGM Chinese wins over the stored
	// base here); title_base keeps the stored one.
	if anime["id"] != "a1" || anime["title"] != "中文" || anime["title_base"] != "Saved" || anime["title_override"] != nil || anime["provider_title"] != nil {
		t.Fatalf("anime = %+v", anime)
	}
	if bgm := payload["bgm"].(map[string]any); bgm["id"] != "123" || bgm["title"] != nil || bgm["title_cn"] != "中文" {
		t.Fatalf("bgm = %+v", bgm)
	}
	if tmdb := payload["tmdb"].(map[string]any); tmdb["season"] != int64(2) || tmdb["title_cn"] != nil {
		t.Fatalf("tmdb = %+v", tmdb)
	}
	torrent := payload["torrent"].(map[string]any)
	if torrent["size"] != int64(15) || torrent["file_count"] != 2 || torrent["hash_v1"] != evidence.HashV1 || torrent["hash_v2"] != nil {
		t.Fatalf("torrent = %+v", torrent)
	}
	if ep := payload["ep"].(map[string]any); ep["number"] != "3" || ep["offset"] != int64(0) {
		t.Fatalf("ep = %+v", ep)
	}
	if rt := payload["rt"].(map[string]any); rt["resolution"] != "1080p" || rt["title"] != nil {
		t.Fatalf("rt = %+v", rt)
	}
	if _, ok := payload["mediainfo"]; ok {
		t.Fatal("payload carries mediainfo")
	}
	// An input without facts reads only the title and the torrent totals.
	bare := Input{ReleaseTitle: input.ReleaseTitle, Files: input.Files}.Payload()
	for _, root := range []string{"anime", "bgm", "tmdb", "ep", "rt", "anibt"} {
		if _, ok := bare[root]; ok {
			t.Fatalf("bare payload reads %s", root)
		}
	}
	// A frozen input (JSON round trip, as the plan reads it) yields the same
	// program input.
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var frozen Input
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(input.Payload())
	b, _ := json.Marshal(frozen.Payload())
	if string(a) != string(b) {
		t.Fatalf("frozen payload differs:\n%s\n%s", a, b)
	}
}
