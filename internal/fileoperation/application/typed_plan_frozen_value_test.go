package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/nativeinput"
)

// TestTypedPlanFrozenValuesPreserveUnknownAndKnownZero — a frozen media value
// that is absent or unavailable must stay unknown rather than being defaulted
// into the plan, and a known zero (season 0) must survive as zero.
//
// The delivery route root is deliberately not covered: naming templates render
// absolute paths, so the frozen route root reaches no plan field and has no
// value here to preserve.
func TestTypedPlanFrozenValuesPreserveUnknownAndKnownZero(t *testing.T) {
	for _, field := range []string{"title", "title_override", "bgm", "tmdb"} {
		for _, state := range []string{"absent", "unavailable"} {
			t.Run(field+"_"+state, func(t *testing.T) {
				var body map[string]any
				raw := nativeinput.Canonical(t, "sha256:"+stringRepeatA(64), "sha256:"+stringRepeatA(64))
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					t.Fatal(err)
				}
				value := map[string]any{"state": state, "value": nil, "reason_code": nil}
				if state == "unavailable" {
					value["reason_code"] = "FIXTURE_UNAVAILABLE"
				}
				body["media"].(map[string]any)[field] = value
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				b := &artifactPreparedBatch{}
				b.artifact.animeTitle = "today"
				b.artifact.tmdbSeason.Valid = true
				b.artifact.tmdbSeason.Int64 = 9
				if err := applyTypedPlanInput(data, b); err != nil {
					t.Fatal(err)
				}
				switch field {
				case "title":
					if b.artifact.animeTitle != "" {
						t.Fatal("unknown title substituted")
					}
				case "title_override":
					if b.artifact.titleOverride.Valid {
						t.Fatal("unknown override became known")
					}
				case "bgm":
					if b.artifact.metadataBGM.Valid {
						t.Fatal("unknown BGM became known")
					}
				case "tmdb":
					if b.artifact.tmdbWorkID.Valid || b.artifact.tmdbMediaKind.Valid || b.artifact.tmdbSeason.Valid {
						t.Fatal("unknown TMDB became known zero")
					}
				}
			})
		}
	}
	t.Run("known_zero_season", func(t *testing.T) {
		var body map[string]any
		if err := json.Unmarshal([]byte(nativeinput.Canonical(t, "sha256:"+stringRepeatA(64), "sha256:"+stringRepeatA(64))), &body); err != nil {
			t.Fatal(err)
		}
		body["media"].(map[string]any)["tmdb"] = map[string]any{"state": "known", "value": map[string]any{"work_ref": "tmdb-fixture", "kind": "tv", "season": 0, "version": 1}, "reason_code": nil}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		b := &artifactPreparedBatch{}
		if err := applyTypedPlanInput(raw, b); err != nil {
			t.Fatal(err)
		}
		if !b.artifact.tmdbSeason.Valid || b.artifact.tmdbSeason.Int64 != 0 {
			t.Fatal("known season zero lost")
		}
	})
}
func TestTypedPlanUsesAcceptedSourceEvidence(t *testing.T) {
	for _, withTags := range []bool{false, true} {
		var body map[string]any
		if err := json.Unmarshal([]byte(nativeinput.Canonical(t, "sha256:"+stringRepeatA(64), "sha256:"+stringRepeatA(64))), &body); err != nil {
			t.Fatal(err)
		}
		source := body["source"].(map[string]any)
		source["title"] = map[string]any{"state": "known", "value": "Accepted title", "reason_code": nil}
		source["group"] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
		delete(source, "tag_evidence")
		if withTags {
			source["tag_evidence"] = map[string]any{"title_tags": map[string]any{"schema": "v2", "implementation": "accepted-parser", "tags": []string{"resolution:720p"}}, "anibt": nil}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		b := &artifactPreparedBatch{}
		b.artifact.sourceFacts = []byte(`{"title_tags":{"schema":"v2","implementation":"today","tags":["resolution:2160p"]}}`)
		b.artifact.rawFacts = []byte(`{"title":"Changed current title"}`)
		b.artifact.groupName = "Current group"
		if err := applyTypedPlanInput(raw, b); err != nil {
			t.Fatal(err)
		}
		resolved, err := buildNamingContext(b.artifact, artifactFileContext{sourcePath: "/d/a.mkv", mediaKind: "video", sizeBytes: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		facts := naming.UnifiedFacts(resolved.Context)
		want := ""
		if withTags {
			want = "720p"
		}
		if facts.Rt["Resolution"] != want {
			t.Fatalf("accepted tags=%v resolution=%q want=%q", withTags, facts.Rt["Resolution"], want)
		}
		if resolved.Context.Release.OriginalTitle != "Accepted title" || resolved.Context.Source.GroupName != "" {
			t.Fatalf("current source leaked: %+v %+v", resolved.Context.Release, resolved.Context.Source)
		}
	}
}

func TestTypedPlanOwnerFactsDistinguishLegacyAndExplicitAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, year, season, legacyAnime string
		wantYear                        string
		wantSeason                      *int
	}{
		{name: "legacy edited", legacyAnime: `{"year":"2024","season":2}`, wantYear: "2024", wantSeason: intPtrForOwnerFacts(2)},
		{name: "legacy explicit unset", legacyAnime: `{"season":null}`},
		{name: "legacy before owner season", legacyAnime: `{}`, wantSeason: intPtrForOwnerFacts(3)},
		{name: "new explicit unset", year: `{"state":"absent","value":null}`, season: `{"state":"absent","value":null}`, legacyAnime: `{"year":"2024","season":2}`},
		{name: "new known zero", year: `{"state":"known","value":2025}`, season: `{"state":"known","value":0}`, wantYear: "2025", wantSeason: intPtrForOwnerFacts(0)},
		{name: "mixed fields fall back independently", season: `{"state":"known","value":0}`, legacyAnime: `{"year":"2024","season":"ignored"}`, wantYear: "2024", wantSeason: intPtrForOwnerFacts(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body struct {
				Config map[string]json.RawMessage `json:"config"`
				Media  map[string]json.RawMessage `json:"media"`
			}
			if err := json.Unmarshal([]byte(nativeinput.Canonical(t, "sha256:"+stringRepeatA(64), "sha256:"+stringRepeatA(64))), &body); err != nil {
				t.Fatal(err)
			}
			delete(body.Config, "vars")
			if tc.legacyAnime != "" {
				body.Config["vars"] = json.RawMessage(`{"mode":"frozen","status":"error","output":"unused","known_inputs":{"facts":{"anime":` + tc.legacyAnime + `}}}`)
			}
			delete(body.Media, "year")
			delete(body.Media, "season")
			body.Media["tmdb"] = json.RawMessage(`{"state":"known","value":{"work_ref":"tmdb-fixture","kind":"tv","season":3}}`)
			if tc.year != "" {
				body.Media["year"] = json.RawMessage(tc.year)
			}
			if tc.season != "" {
				body.Media["season"] = json.RawMessage(tc.season)
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			var b artifactPreparedBatch
			if err := applyTypedPlanInput(raw, &b); err != nil {
				t.Fatal(err)
			}
			resolved, err := buildNamingContext(b.artifact, artifactFileContext{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := naming.UnifiedFacts(resolved.Context).Anime.Year; got != tc.wantYear {
				t.Fatalf("owner year = %q, want %q", got, tc.wantYear)
			}
			got := resolved.Context.Anime.Season
			if tc.wantSeason == nil {
				if got != nil {
					t.Fatalf("owner season = %d, want unset", *got)
				}
			} else if got == nil || *got != *tc.wantSeason {
				t.Fatalf("owner season = %v, want %d", got, *tc.wantSeason)
			}
		})
	}
}

func intPtrForOwnerFacts(n int) *int { return &n }

func stringRepeatA(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// The native work titles (schema 5) and torrent metadata (schema 4) frozen in
// the accepted input reach the naming facts and the render binding
// record; an input without them leaves every value unknown.
func TestTypedPlanFreezesWorkTitlesAndTorrentFacts(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(nativeinput.Canonical(t, "sha256:"+stringRepeatA(64), "sha256:"+stringRepeatA(64))), &body); err != nil {
		t.Fatal(err)
	}
	// The flattened single schema always carries the torrent totals, so the
	// "input without them" case is expressed by absent frozen values.
	absent := map[string]any{"state": "absent", "value": nil, "reason_code": nil}
	body["source"].(map[string]any)["torrent_size"] = absent
	body["source"].(map[string]any)["torrent_file_count"] = absent
	// The canonical input already freezes the golden vector's titles; the
	// older-input case is the same input without them.
	body["media"].(map[string]any)["bgm_titles"] = absent
	body["media"].(map[string]any)["tmdb_titles"] = absent
	plain, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	media := body["media"].(map[string]any)
	media["year"] = map[string]any{"state": "known", "value": 2024, "reason_code": nil}
	media["bgm_titles"] = map[string]any{"state": "known", "value": map[string]any{"title": "進撃の巨人", "title_cn": nil, "year": 2019}, "reason_code": nil}
	media["tmdb_titles"] = map[string]any{"state": "known", "value": map[string]any{"title_cn": "进击的巨人", "original_title": "進撃の巨人", "year": 2020}, "reason_code": nil}
	source := body["source"].(map[string]any)
	source["torrent_size"] = map[string]any{"state": "known", "value": 734003200, "reason_code": nil}
	source["torrent_file_count"] = map[string]any{"state": "known", "value": 2, "reason_code": nil}
	source["infohash_v1"] = map[string]any{"state": "known", "value": "0123456789abcdef0123456789abcdef01234567", "reason_code": nil}
	frozen, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	b := &artifactPreparedBatch{}
	if err := applyTypedPlanInput(frozen, b); err != nil {
		t.Fatal(err)
	}
	resolved, err := buildNamingContext(b.artifact, artifactFileContext{sourcePath: "/d/a.mkv", mediaKind: "video", sizeBytes: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	facts := naming.UnifiedFacts(resolved.Context)
	if facts.Bgm.Title != "進撃の巨人" || facts.Bgm.TitleCN != "" || facts.Tmdb.TitleCN != "进击的巨人" || facts.Tmdb.OriginalTitle != "進撃の巨人" {
		t.Fatalf("titles = %+v %+v", facts.Bgm, facts.Tmdb)
	}
	if facts.Torrent.Size == nil || *facts.Torrent.Size != 734003200 || facts.Torrent.FileCount == nil || *facts.Torrent.FileCount != 2 || facts.Torrent.HashV1 != "0123456789abcdef0123456789abcdef01234567" || facts.Torrent.HashV2 != "" {
		t.Fatalf("torrent = %+v", facts.Torrent)
	}
	// anime.year is the owner year frozen with the Vars input, never a
	// provider year; the binding record keeps it (and the provider years) so
	// a maintenance re-render reads them.
	if facts.Anime.Year != "2024" {
		t.Fatalf("anime.year = %q", facts.Anime.Year)
	}
	if facts.Bgm.Year == nil || *facts.Bgm.Year != 2019 || facts.Tmdb.Year == nil || *facts.Tmdb.Year != 2020 {
		t.Fatalf("year facts = %+v %+v", facts.Bgm, facts.Tmdb)
	}
	record := renderBindingFacts(resolved.Context)
	if record["tmdb_title_cn"] != "进击的巨人" || record["tmdb_title"] != nil || record["tmdb_original_title"] != "進撃の巨人" || record["torrent_file_count"] == nil {
		t.Fatalf("binding record = %+v", record)
	}
	if year, ok := record["bgm_year"].(*int); !ok || year == nil || *year != 2019 {
		t.Fatalf("binding record bgm year = %#v", record["bgm_year"])
	}
	if year, ok := record["tmdb_year"].(*int); !ok || year == nil || *year != 2020 {
		t.Fatalf("binding record tmdb year = %#v", record["tmdb_year"])
	}
	if year, ok := record["anime_year"].(*int); !ok || year == nil || *year != 2024 {
		t.Fatalf("binding record anime year = %#v", record["anime_year"])
	}
	// An older input froze none of them.
	b = &artifactPreparedBatch{}
	if err := applyTypedPlanInput(plain, b); err != nil {
		t.Fatal(err)
	}
	if b.artifact.bindingTitles != (frozenBindingTitles{}) || b.artifact.torrent.Size != nil || b.artifact.torrent.FileCount != nil {
		t.Fatalf("older input produced values: %+v %+v", b.artifact.bindingTitles, b.artifact.torrent)
	}
}
