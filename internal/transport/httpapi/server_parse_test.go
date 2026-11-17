package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

func TestParseReleaseTitlePure(t *testing.T) {
	result, err := parseReleaseTitlePure(context.Background(), "[SampleGroup] 进击的巨人 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ParserVersion, "reltitle@") || result.ParserVersion != result.Facts.Implementation {
		t.Fatalf("parser version: %q / %q", result.ParserVersion, result.Facts.Implementation)
	}
	if !slices.Contains(result.Facts.Tags, "resolution:1080p") || !slices.Contains(result.Facts.Tags, "source_type:WEB-DL") {
		t.Fatalf("native tags: %+v", result.Facts.Tags)
	}
	if result.Facts.ValueTags == nil || !slices.Contains(*result.Facts.ValueTags, "group:SampleGroup") {
		t.Fatalf("native values: %+v", result.Facts.ValueTags)
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

func TestParseReleaseTitleOverHTTP(t *testing.T) {
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
	if result.ParserVersion == "" || !slices.Contains(result.Facts.Tags, "resolution:720p") {
		t.Fatalf("unexpected result: %+v", result)
	}
}
