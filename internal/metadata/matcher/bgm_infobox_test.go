package matcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

const foolNightBGM = `{"id":661692,"name":"フールナイト","name_cn":"愚者之夜","date":"2026-11-26","infobox":[{"key":"别名","value":[{"v":" Fool Night "}]},{"key":"话数","value":"99"}]}`
const otherFoolSubjects = `,{"id":1901,"name":"The Jazz Fool"},{"id":2712,"name":"ノブナガ・ザ・フール"}`

func TestBGMV0MatchesUniqueFullAlias(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		wantMatch   bool
	}{
		{"unique", "", true},
		{"duplicate ID", "," + foolNightBGM, true},
		{"ambiguous IDs", `,{"id":998,"name":"Fool Night"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/v0/search/subjects":
					var body struct {
						Keyword string
						Filter  struct{ Type []int }
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Keyword != "Fool Night" || !slices.Equal(body.Filter.Type, []int{2}) ||
						r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.URL.Query().Get("limit") != "25" {
						t.Errorf("unexpected v0 request: %s %s, body=%+v, error=%v", r.Method, r.URL, body, err)
					}
					fmt.Fprint(w, `{"data":[`+foolNightBGM+otherFoolSubjects+tc.extra+`]}`)
				case "/v0/subjects/661692":
					fmt.Fprint(w, foolNightBGM)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			got, err := (&BGMMatcher{Endpoint: server.URL, Client: server.Client()}).Match(t.Context(), "Fool Night")
			if err != nil || (got != nil) != tc.wantMatch {
				t.Fatalf("match = %+v, error = %v", got, err)
			}
			wantPaths := []string{"/v0/search/subjects"}
			if tc.wantMatch {
				wantPaths = append(wantPaths, "/v0/subjects/661692")
				if !got.trusted || got.BgmID != "661692" || got.Subject.NameCN != "愚者之夜" || got.TotalEpisodes != nil || got.Year == nil || *got.Year != 2026 {
					t.Fatalf("incorrect provider facts: %+v", got)
				}
			}
			if !slices.Equal(paths, wantPaths) {
				t.Fatalf("paths = %v, want %v", paths, wantPaths)
			}
		})
	}
}

func TestBGMInfoboxAliasSanitization(t *testing.T) {
	var subject BGMSubject
	if err := json.Unmarshal([]byte(`{"infobox":[{"key":"备注","value":"Unlisted"},{"key":"别名","value":[{"v":"*"},{"v":"{}"},{"v":"[]"},{"v":"{{}}"},{"v":"Foo, Bar"},{"k":"en","v":"Exact Alias"}]},{"key":"英文名称","value":" Another Alias "}]}`), &subject); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Exact Alias", "Another Alias", "Foo, Bar"} {
		if !subject.matchesTitle(bgmTitleKey(name)) {
			t.Errorf("missing alias %q", name)
		}
	}
	for _, name := range []string{"Unlisted", "Foo", "Bar", "*", "{}", "[]", "{{}}"} {
		if subject.matchesTitle(bgmTitleKey(name)) {
			t.Errorf("unexpected alias %q", name)
		}
	}
}

func TestBGMInfoboxDatePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, payload, start, source string
		precision                    datePrecision
		conflict                     bool
	}{
		{"top-level date", `{"date":"2024-02-29","infobox":[{"key":"首播","value":"2024年2月"}]}`, "2024-02-29", "bangumi.date", dateDay, false},
		{"calendar fallback", `{"date":"2024-02-30","infobox":[{"key":"放送開始","value":[{"v":"2024年2月29日"}]}]}`, "2024-02-29", "bangumi.infobox:放送開始", dateDay, false},
		{"conflicting source", `{"date":"2024-02-29","infobox":[{"key":"开播日期","value":"2024年3月"}]}`, "2024-02-29", "bangumi.date", dateDay, true},
		{"annual before release", `{"platform":"OVA","infobox":[{"key":"发售日","value":"2025-07-01"},{"key":"上映年度","value":"2024年"}]}`, "2024-01-01", "bangumi.infobox:上映年度", dateYear, false},
		{"OVA release", `{"platform":"OVA","infobox":[{"key":"发售日","value":"2024年2月"}]}`, "2024-02-01", "bangumi.infobox:发售日", dateMonth, false},
		{"movie release", `{"platform":"剧场版","infobox":[{"key":"发售日","value":"2024-02-29"}]}`, "2024-02-29", "bangumi.infobox:发售日", dateDay, false},
		{"TV release ignored", `{"platform":"TV","infobox":[{"key":"发售日","value":"2024-02-29"}]}`, "0001-01-01", "", dateUnknown, false},
		{"WEB release ignored", `{"platform":"WEB","infobox":[{"key":"发售日","value":"2024-02-29"}]}`, "0001-01-01", "", dateUnknown, false},
		{"unknown platform release ignored", `{"infobox":[{"key":"发售日","value":"2024-02-29"}]}`, "0001-01-01", "", dateUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subject BGMSubject
			if err := json.Unmarshal([]byte(tc.payload), &subject); err != nil {
				t.Fatal(err)
			}
			got := subject.airingDate()
			if got.precision != tc.precision || got.conflict != tc.conflict || got.start.Format("2006-01-02") != tc.start || got.source != tc.source {
				t.Fatalf("unexpected date: %+v", got)
			}
		})
	}
}
