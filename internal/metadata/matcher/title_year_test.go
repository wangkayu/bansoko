package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSplitTrailingTitleYear(t *testing.T) {
	for _, tt := range []struct {
		title, base string
		year        int
	}{
		{"Ranma 1/2 (2024)", "Ranma 1/2", 2024},
		{"水果篮子（2019）", "水果篮子", 2019},
		{"Berserk [2016]", "Berserk", 2016},
		{"魔法骑士雷阿斯【2026】", "魔法骑士雷阿斯", 2026},
		{"Fruits Basket 2nd Season (2020)", "Fruits Basket 2nd Season", 2020},
		{"\tShow  ( 1900 ) ", "Show", 1900},
		{"Show【\u30002099\u3000】", "Show", 2099},
		{"Demon Lord 2099", "", 0},
		{"魔王2099", "", 0},
		{"Japan Sinks: 2020", "", 0},
		{"Space Battleship Yamato 2199", "", 0},
		{"Ghost in the Shell: SAC_2045", "", 0},
		{"NIGHT HEAD 2041", "", 0},
		{"狼与香辛料 2024", "", 0},
		{"Rurouni Kenshin 1996", "", 0},
		{"2024", "", 0},
		{"  (2024) ", "", 0},
		{"Show [2024)", "", 0},
		{"Show （2024)", "", 0},
		{"Show (２０２４)", "", 0},
		{"Show (1899)", "", 0},
		{"Show (2100)", "", 0},
		{"Show (2024) Encore", "", 0},
	} {
		t.Run(tt.title, func(t *testing.T) {
			if base, year := splitTrailingTitleYear(tt.title); base != tt.base || year != tt.year {
				t.Errorf("got (%q, %d), want (%q, %d)", base, year, tt.base, tt.year)
			}
		})
	}
}

type titleYearRoundTripper func(*http.Request) (*http.Response, error)

func (f titleYearRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBGMMatcherTrailingTitleYear(t *testing.T) {
	const (
		old         = `{"id":1,"name":"Hunter x Hunter","date":"1999-10-16"}`
		remake      = `{"id":2,"name":"Hunter x Hunter","date":"2024-10-06"}`
		other       = `{"id":3,"name":"Hunter x Hunter","date":"2024-01-01"}`
		normalized  = `{"id":4,"name":"Ｈunter x Hunter","date":"2024-10-06"}`
		original    = `{"id":5,"name":"Hunter x Hunter (2024)","date":"1999-10-16"}`
		unknown     = `{"id":6,"name":"Hunter x Hunter"}`
		conflicting = `{"id":2,"name":"Hunter x Hunter","date":"1999-10-16"}`
		base        = "Hunter x Hunter"
		qualified   = base + " (2024)"
	)
	search := func(items ...string) string { return `{"data":[` + strings.Join(items, ",") + `]}` }
	type call struct {
		query, detailID, body string
	}
	for _, tt := range []struct {
		name, title, wantID, wantMethod string
		calls                           []call
		wantErr                         bool
	}{
		{name: "reuse first page and prefer direct to normalized", title: qualified, wantID: "2", wantMethod: "direct", calls: []call{
			{query: qualified, body: search(old, remake, normalized)}, {detailID: "2", body: remake},
		}},
		{name: "select older release on one base search", title: base + " (1999)", wantID: "1", wantMethod: "direct", calls: []call{
			{query: base + " (1999)", body: search(remake)}, {query: base, body: search(remake, old)}, {detailID: "1", body: old},
		}},
		{name: "keep normalized equivalence", title: qualified, wantID: "4", wantMethod: "normalized", calls: []call{
			{query: qualified, body: search(old, normalized)}, {detailID: "4", body: normalized},
		}},
		{name: "reject wrong or unknown year", title: qualified, calls: []call{
			{query: qualified, body: search(old)}, {query: base, body: search(old, unknown)},
		}},
		{name: "reject highest-tier ties without another request", title: qualified, calls: []call{
			{query: qualified, body: search(remake, other, normalized)},
		}},
		{name: "original match wins regardless of suffix year", title: qualified, wantID: "5", wantMethod: "direct", calls: []call{
			{query: qualified, body: search(original, remake)}, {detailID: "5", body: original},
		}},
		{name: "reject conflicting detail year", title: qualified, calls: []call{
			{query: qualified, body: search(remake)}, {detailID: "2", body: conflicting},
		}},
		{name: "no fallback for bare year", title: base + " 2024", calls: []call{
			{query: base + " 2024", body: search(remake)},
		}},
		{name: "no fallback after search error", title: qualified, wantErr: true, calls: []call{
			{query: qualified},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var count int
			transport := titleYearRoundTripper(func(r *http.Request) (*http.Response, error) {
				if count >= len(tt.calls) {
					t.Fatalf("unexpected extra request: %s %s", r.Method, r.URL)
				}
				want := tt.calls[count]
				count++
				if want.query != "" {
					defer r.Body.Close()
					var body struct{ Keyword string }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Keyword != want.query || r.Method != http.MethodPost || r.URL.Path != "/v0/search/subjects" {
						t.Fatalf("unexpected search: %s %s, keyword %q, error %v", r.Method, r.URL, body.Keyword, err)
					}
				} else if r.Method != http.MethodGet || r.URL.Path != "/v0/subjects/"+want.detailID {
					t.Fatalf("unexpected detail request: %s %s", r.Method, r.URL)
				}
				if want.body == "" {
					return nil, errors.New("test transport error")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(want.body))}, nil
			})
			m := &BGMMatcher{Client: &http.Client{Transport: transport}}
			result, err := m.Match(context.Background(), tt.title)
			if count != len(tt.calls) || (err != nil) != tt.wantErr {
				t.Fatalf("got %d requests and err=%v, want %d requests and error=%v", count, err, len(tt.calls), tt.wantErr)
			}
			if tt.wantID == "" {
				if result != nil {
					t.Fatalf("want no binding, got %+v", result)
				}
			} else if result == nil || result.BgmID != tt.wantID || result.Method != tt.wantMethod || !result.trusted {
				t.Fatalf("want a trusted %s match for %s, got %+v", tt.wantMethod, tt.wantID, result)
			}
		})
	}
}
