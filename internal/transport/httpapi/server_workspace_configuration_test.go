package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/transport/httpapi/generated"
)

type configurationReadmodelSpy struct {
	ReadmodelService
	value readmodelapplication.AnimeWorkspaceConfiguration
	err   error
	ids   []string
}

func (s *configurationReadmodelSpy) GetAnimeWorkspaceConfigurationApplication(_ context.Context, id string) (readmodelapplication.AnimeWorkspaceConfiguration, error) {
	s.ids = append(s.ids, id)
	return s.value, s.err
}

func TestGetAnimeWorkspaceConfigurationHandler(t *testing.T) {
	spy := &configurationReadmodelSpy{value: readmodelapplication.AnimeWorkspaceConfiguration{
		ManagedAnime:   readmodelapplication.ManagedAnime{ID: "anime-config", Version: 7},
		Rules:          ownershipapplication.RulesOwner{ManagedAnimeID: "anime-config", Sections: presetdraft.DefaultDomainRules(t), Version: 11},
		ReleaseSummary: readmodelapplication.AnimeWorkspaceReleaseSummary{Count: 501, OwnerChainStable: true},
	}}
	server := &Server{deps: Dependencies{Readmodel: spy}}
	handler := generated.Handler(server)
	request := httptest.NewRequest(http.MethodGet, "/anime/anime-config/workspace-configuration", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(spy.ids) != 1 || spy.ids[0] != "anime-config" {
		t.Fatalf("status=%d calls=%v body=%s", response.Code, spy.ids, response.Body.String())
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["release_summary"]; !ok {
		t.Fatal("missing release_summary")
	}
	if _, ok := result["releases"]; ok {
		t.Fatal("configuration response must not include releases")
	}
	spy.err = &apperrors.NotFound{Resource: "managed_anime", ID: "anime-config"}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("not-found response=%d %s", response.Code, response.Body.String())
	}
}
