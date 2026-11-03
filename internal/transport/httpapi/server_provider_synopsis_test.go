package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func TestReadProviderEntrySynopsisReturnsSourceFacts(t *testing.T) {
	synopsis := "整段简介\n\n[简介原文]\n原文"
	weekday := "wed"
	season := "2026-fall"
	episodes := 13
	official := "https://official.example/"
	bgm := "https://bgm.tv/subject/301"
	spy := &catalogCallSpy{synopsis: catalogdomain.EntrySynopsis{
		Provider: "mikan", ProviderEntryRef: "301", SourceURL: "https://mikan.example/Home/Bangumi/301",
		Synopsis: &synopsis, Weekday: weekday, Episodes: &episodes, Season: season,
		Tags: []string{"科幻"}, OfficialSite: official, BGMURL: bgm,
	}}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/entries/301/synopsis", nil)

	server.ReadProviderEntrySynopsis(recorder, request, generated.ReadProviderEntrySynopsisParamsProviderMikan, generated.CatalogProviderEntryRef("301"))

	if recorder.Code != http.StatusOK || spy.synopsisCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, spy.synopsisCalls, recorder.Body.String())
	}
	var body generated.CatalogEntrySynopsis
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Provider != generated.CatalogEntrySynopsisProviderMikan || body.ProviderEntryRef != "301" || body.SourceUrl == "" {
		t.Fatalf("identity = %+v", body)
	}
	if body.Synopsis == nil || *body.Synopsis != synopsis || body.Episodes == nil || *body.Episodes != episodes {
		t.Fatalf("synopsis/episodes = %+v", body)
	}
	if body.Weekday == nil || string(*body.Weekday) != weekday || body.Season == nil || *body.Season != season {
		t.Fatalf("weekday/season = %+v", body)
	}
	if len(body.Tags) != 1 || body.Tags[0] != "科幻" || body.OfficialSite == nil || body.BgmUrl == nil {
		t.Fatalf("facts = %+v", body)
	}
}

func TestReadProviderEntrySynopsisEmptySourceIsANormalResult(t *testing.T) {
	spy := &catalogCallSpy{synopsis: catalogdomain.EntrySynopsis{Provider: "anibt", ProviderEntryRef: "5", SourceURL: "https://anibt.example/anime/5"}}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/anibt/entries/5/synopsis", nil)

	server.ReadProviderEntrySynopsis(recorder, request, generated.ReadProviderEntrySynopsisParamsProviderAnibt, generated.CatalogProviderEntryRef("5"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body generated.CatalogEntrySynopsis
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Synopsis != nil || body.Tags == nil || len(body.Tags) != 0 {
		t.Fatalf("empty synopsis must stay null with an empty tags array: %+v", body)
	}
}

func TestReadProviderEntrySynopsisUnknownProviderIs422(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/bad/entries/1/synopsis", nil)

	server.ReadProviderEntrySynopsis(recorder, request, generated.ReadProviderEntrySynopsisParamsProvider("bad"), generated.CatalogProviderEntryRef("1"))

	if recorder.Code != http.StatusUnprocessableEntity || spy.synopsisCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, spy.synopsisCalls, recorder.Body.String())
	}
	var body generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Code != "SYNOPSIS_PROVIDER_UNSUPPORTED" {
		t.Fatalf("error = %+v, err=%v", body, err)
	}
}

func TestReadProviderEntrySynopsisInvalidEntryIs422(t *testing.T) {
	spy := &catalogCallSpy{synopsisErr: fmt.Errorf("%w: bad", catalogapplication.ErrSynopsisEntryInvalid)}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/entries/01/synopsis", nil)

	server.ReadProviderEntrySynopsis(recorder, request, generated.ReadProviderEntrySynopsisParamsProviderMikan, generated.CatalogProviderEntryRef("01"))

	if recorder.Code != http.StatusUnprocessableEntity || spy.synopsisCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, spy.synopsisCalls, recorder.Body.String())
	}
	var body generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Code != "SYNOPSIS_ENTRY_INVALID" {
		t.Fatalf("error = %+v, err=%v", body, err)
	}
}

func TestReadProviderEntrySynopsisUpstreamFailureIs503(t *testing.T) {
	spy := &catalogCallSpy{synopsisErr: fmt.Errorf("%w: down", catalogapplication.ErrProviderUpstream)}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/entries/301/synopsis", nil)

	server.ReadProviderEntrySynopsis(recorder, request, generated.ReadProviderEntrySynopsisParamsProviderMikan, generated.CatalogProviderEntryRef("301"))

	if recorder.Code != http.StatusServiceUnavailable || spy.synopsisCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, spy.synopsisCalls, recorder.Body.String())
	}
	var body generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Code != "SYNOPSIS_UPSTREAM_FAILED" {
		t.Fatalf("error = %+v, err=%v", body, err)
	}
}
