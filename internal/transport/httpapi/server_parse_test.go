package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/attrrules"
	"bansoko/internal/transport/httpapi/generated"
)

func testAttrRules() attrrules.Rules {
	return attrrules.Rules{Version: 1, Resolution: []string{"2160p", "1440p", "1080p", "720p", "480p"}, Quality: []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"}, Language: []string{"chs", "cht", "sc", "tc", "jpn", "eng"}, SpecialMarkers: []string{"special", "ncop", "nced"}, GroupBrackets: []string{"[", "]"}}
}

func ensureTestAttrRules(t *testing.T) {
	t.Helper()
	rules := testAttrRules()
	fingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := attrrules.SetActive(rules, fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestParseReleaseTitlePure(t *testing.T) {
	ensureTestAttrRules(t)
	result, err := parseReleaseTitlePure(context.Background(), "[SampleGroup] 进击的巨人 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT]")
	if err != nil {
		t.Fatal(err)
	}
	if result.ParserVersion != "parser-v1" {
		t.Fatalf("parser version: %q", result.ParserVersion)
	}
	if result.Facts.GroupName == nil || *result.Facts.GroupName != "SampleGroup" {
		t.Fatalf("group: %+v", result.Facts.GroupName)
	}
	if result.Facts.Quality == nil || *result.Facts.Quality != "web-dl" {
		t.Fatalf("quality: %+v", result.Facts.Quality)
	}
	if result.Episode == nil || !result.Episode.Matched || int(result.Episode.EpisodeNumber) != 1 {
		t.Fatalf("episode: %+v", result.Episode)
	}
}

func TestParseReleaseTitleRequiresTitle(t *testing.T) {
	if _, err := parseReleaseTitlePure(context.Background(), "   "); err == nil {
		t.Fatal("blank title must fail")
	}
}

// TestParseReleaseTitleOverHTTP asserts the endpoint is reachable through the
// generated handler path and returns a typed body.
func TestParseReleaseTitleOverHTTP(t *testing.T) {
	ensureTestAttrRules(t)
	server := &Server{deps: Dependencies{SkipAuth: true}}
	recorder := httptest.NewRecorder()
	body := strings.NewReader(`{"title":"[SampleGroup] Show - 02 [720P]"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/parse/release-title", body)
	request.Header.Set("Content-Type", "application/json")
	server.ParseReleaseTitle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result generated.ParseReleaseTitleResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ParserVersion == "" || result.Facts.GroupName == nil {
		t.Fatalf("unexpected result: %+v", result)
	}
}
