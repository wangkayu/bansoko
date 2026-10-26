package application

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"bansoko/internal/templatecatalog/notification"
)

// The Go payload builders (download, typed file success) and the SQL file
// projectors (271) project the same shared catalog facts from the same
// frozen values.
func TestNotificationFactsSQLParity(t *testing.T) {
	_, _ = preparedPlanPGFixture(t)
	db := ownerTestDB(t)
	inputs := []string{
		`{}`,
		`{"source":{"provider":{"state":"absent","value":null},"infohash_v1":{"state":"absent","value":null},"infohash_v2":{"state":"absent","value":null}},"media":{"bgm":{"state":"absent","value":null},"tmdb":{"state":"absent","value":null}},"config":{"identity":{"kind":"submitted","managed_anime_ref":{"state":"absent","value":null}}}}`,
		`{"source":{"provider":{"state":"known","value":"anibt"},"infohash_v1":{"state":"known","value":"0123456789abcdef0123456789abcdef01234567"},"infohash_v2":{"state":"unavailable","value":null,"reason_code":"x"}},"media":{"bgm":{"state":"known","value":{"ref":"12345","version":2}},"tmdb":{"state":"known","value":{"work_ref":"678","kind":"tv","season":2,"version":2}}},"config":{"identity":{"kind":"saved_revision","managed_anime_ref":{"state":"known","value":"anime-1"}}}}`,
		`{"source":{"provider":{"state":"known","value":""}},"media":{"tmdb":{"state":"known","value":{"work_ref":"9","kind":"movie","season":0,"version":1}}}}`,
		// Input schema 4 freezes the torrent totals.
		`{"schema_version":4,"source":{"torrent_size":{"state":"known","value":734003200,"reason_code":null},"torrent_file_count":{"state":"known","value":2,"reason_code":null}}}`,
	}
	for _, input := range inputs {
		var got []byte
		if err := db.QueryRow(`SELECT public.notification_input_facts($1::jsonb)`, input).Scan(&got); err != nil {
			t.Fatal(err)
		}
		assertSameFacts(t, input, got, notification.InputFacts([]byte(input)))
	}
	identities := []string{
		`null`,
		`{"state":"absent","value":null}`,
		`{"state":"known","value":{"state":"unresolved","raw_episode":{"state":"absent","value":null},"episode":{"state":"absent","value":null},"offset":0}}`,
		`{"state":"known","value":{"state":"resolved","raw_episode":{"state":"known","value":"13"},"episode":{"state":"known","value":"1"},"offset":-12}}`,
		`{"state":"known","value":{"state":"resolved","raw_episode":{"state":"absent","value":null},"episode":{"state":"known","value":"7.5"},"offset":0}}`,
	}
	for _, identity := range identities {
		var got []byte
		if err := db.QueryRow(`SELECT public.notification_episode_facts($1::jsonb)`, identity).Scan(&got); err != nil {
			t.Fatal(err)
		}
		parsed, _ := notification.DecodeFacts([]byte(`{"identity":` + identity + `}`))
		assertSameFacts(t, identity, got, notification.EpisodeFacts(parsed["identity"]))
	}
	// vars, rt and anibt (283): the frozen Vars output and source facts,
	// through the same text sanitizer on both sides.
	values := []struct{ vars, sourceFacts string }{
		{`null`, `null`},
		{`{}`, `{}`},
		{`{"info":{"tag":"WEB","source_url":"x","link":"https://x/y","long":"` + strings.Repeat("长", 200) + `"},"empty":{},"odd":"x"}`, `{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["resolution:1080p","sub_lang:CHT","sub_lang:chs","sub_lang:CHS"," sub_lang: x ",":nodim","nov:","codec"],"value_tags":["title:进击的巨人","group:SampleGroup","version:v2","content_rating:UNCENSORED","content_rating:R18"]},"anibt":{"schema":"anibt-tags-v1","tags":["language:CHT","language:CHS","format:MKV","tag:10bit","tag:AAC","tag:aac"]}}`},
		{`{"a":{"b":"c"}}`, `{"title_tags":{"schema":"reltitle-attrs-v0","implementation":"r","tags":["resolution:720p"],"value_tags":["title:ignored in unknown schema"]}}`},
		{`{"a":{"b":"c"}}`, `{"title_tags":{"tags":["resolution:720p",null]},"anibt":null}`},
		{`{"a":{"b":"c"}}`, `{"title_tags":{"tags":["resolution:720p",7]}}`},
		{`{"a":{"b":"c"}}`, `{"anibt":"broken"}`},
		{`{"a":{"b":"c"}}`, `[1]`},
	}
	for _, value := range values {
		var got []byte
		if err := db.QueryRow(`SELECT public.notification_value_facts($1::jsonb,$2::jsonb)`, value.vars, value.sourceFacts).Scan(&got); err != nil {
			t.Fatal(err)
		}
		var vars map[string]any
		_ = json.Unmarshal([]byte(value.vars), &vars)
		assertSameFacts(t, value.vars+" "+value.sourceFacts, got, notification.ValueFacts(vars, []byte(value.sourceFacts), notificationText))
	}
	// Native work titles (285, overview 047): input schema 5
	// media.bgm_titles/tmdb_titles; the optional overview member reads null
	// when the input predates it or the overview was never observed.
	titles := []string{
		`{}`,
		`{"schema_version":4,"media":{"bgm":{"state":"known","value":{"ref":"1","version":1}}}}`,
		`{"schema_version":5,"media":{"bgm_titles":{"state":"absent","value":null,"reason_code":null},"tmdb_titles":{"state":"absent","value":null,"reason_code":null}}}`,
		`{"schema_version":5,"media":{"bgm_titles":{"state":"known","value":{"title":"進撃の巨人","title_cn":"进击的巨人"},"reason_code":null},"tmdb_titles":{"state":"known","value":{"title_cn":"进击的巨人","original_title":null},"reason_code":null}}}`,
		`{"schema_version":5,"media":{"bgm_titles":{"state":"known","value":{"title":null,"title_cn":"see https://x/y"},"reason_code":null},"tmdb_titles":{"state":"known","value":{"title_cn":"` + strings.Repeat("长", 170) + `","original_title":"O"},"reason_code":null}}}`,
		`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","original_title":"Literary Arcana","overview":"中文名是标题的译名。","year":2024},"reason_code":null}}}`,
		`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","original_title":"Literary Arcana","overview":null,"year":2024},"reason_code":null}}}`,
		`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"` + strings.Repeat("长", 140) + `","original_title":"O","overview":"` + strings.Repeat("结", 180) + `"},"reason_code":null}}}`,
	}
	for _, input := range titles {
		var got []byte
		if err := db.QueryRow(`SELECT public.notification_title_facts($1::jsonb)`, input).Scan(&got); err != nil {
			t.Fatal(err)
		}
		assertSameFacts(t, input, got, notification.TitleFacts([]byte(input), notificationText))
	}
	// Episode titles are back in both catalogs: a frozen render snapshot's
	// episode_titles block projects bgm.ep_title / tmdb.ep_title on the two
	// sides through the same sanitizer (note 025 block, the 2026-10-22
	// revival). No query is ever involved — the block carries the states the
	// resolver froze, and everything else reads null.
	renders := []string{
		`{}`,
		`{"directory":"d","filename":"f"}`,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":"1","bgm":{"state":"known","value":"沉默的魔女","reason_code":null},"tmdb":{"state":"absent","value":null,"reason_code":null}}}`,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":null,"bgm":{"state":"unavailable","value":null,"reason_code":"no_episode"},"tmdb":{"state":"known","value":"see https://x/y","reason_code":null}}}`,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":"2","bgm":{"state":"known","value":"` + strings.Repeat("长", 170) + `","reason_code":null},"tmdb":{"state":"known","value":"Title TMDB 7","reason_code":null}}}`,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":"2","bgm":{"state":"unavailable","value":null,"reason_code":"not_requested"},"tmdb":{"state":"unavailable","value":null,"reason_code":"not_requested"}}}`,
	}
	for _, render := range renders {
		var got []byte
		if err := db.QueryRow(`SELECT public.notification_episode_title_facts($1::jsonb)`, render).Scan(&got); err != nil {
			t.Fatal(err)
		}
		assertSameFacts(t, render, got, notification.EpisodeTitleFacts([]byte(render), notificationText))
	}
}

// The render snapshot schema check: a snapshot without episode_titles is valid
// under every renderer version (025); one that still carries the key must be a
// fileoperation-render-v1 snapshot with exactly the closed object.
func TestRenderEpisodeTitlesSchemaCheck(t *testing.T) {
	_, _ = preparedPlanPGFixture(t)
	db := ownerTestDB(t)
	title := func(state, value, reason string) string {
		v, r := "null", "null"
		if value != "" {
			v = `"` + value + `"`
		}
		if reason != "" {
			r = `"` + reason + `"`
		}
		return `{"state":"` + state + `","value":` + v + `,"reason_code":` + r + `}`
	}
	v1 := func(episode, bgm, tmdb string) string {
		return `{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":` + episode + `,"bgm":` + bgm + `,"tmdb":` + tmdb + `}}`
	}
	notRequested := title("unavailable", "", "not_requested")
	for render, want := range map[string]bool{
		`{}`:                    true,
		`{"directory":"d"}`:     true,
		`{"episode_titles":{}}`: false,
		`{"renderer_version":"fileoperation-render-v1"}`:                                                             true,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{}}`:                                         false,
		v1(`"1"`, title("known", "秘密", ""), title("absent", "", "")):                                                 true,
		v1(`null`, title("unavailable", "", "no_episode"), notRequested):                                             true,
		v1(`"1"`, title("unavailable", "", "not_bound"), title("unavailable", "", "title_invalid")):                  true,
		v1(`"1"`, title("known", "", ""), notRequested):                                                              false,
		v1(`"1"`, title("known", " x", ""), notRequested):                                                            false,
		v1(`"1"`, title("known", "x", "not_bound"), notRequested):                                                    false,
		v1(`"1"`, title("absent", "x", ""), notRequested):                                                            false,
		v1(`"1"`, title("unavailable", "", "guessed"), notRequested):                                                 false,
		v1(`1`, notRequested, notRequested):                                                                          false,
		`{"renderer_version":"fileoperation-render-v1","episode_titles":{"episode":"1","bgm":` + notRequested + `}}`: false,
	} {
		var got bool
		if err := db.QueryRow(`SELECT public.render_episode_titles_valid($1::jsonb)`, render).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: valid=%v want %v", render, got, want)
		}
	}
}

func assertSameFacts(t *testing.T, input string, sqlFacts []byte, goFacts map[string]any) {
	t.Helper()
	goJSON, err := json.Marshal(goFacts)
	if err != nil {
		t.Fatal(err)
	}
	var left, right map[string]any
	if err := json.Unmarshal(sqlFacts, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(goJSON, &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("%s:\nsql=%s\ngo =%s", input, sqlFacts, goJSON)
	}
}
