package httpapi

import (
	ownership "bansoko/internal/anime/ownership/application"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type availabilityOwnership struct{ OwnershipService }

func (availabilityOwnership) PatchAnimeSubgroupsApplication(context.Context, string, ownership.SubgroupsPatchInput) (ownership.SubgroupsOwnerResult, error) {
	return ownership.SubgroupsOwnerResult{EffectiveSelection: []ownership.ProviderGroupSelection{{Available: true, AvailabilityGeneration: 17}, {Available: false, AvailabilityGeneration: 23}}}, nil
}
func TestPatchSubgroupsHandlerResponsePreservesAvailability(t *testing.T) {
	server := &Server{deps: Dependencies{Ownership: availabilityOwnership{}}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("PATCH", "/anime/target/subgroups", strings.NewReader(`{"expected_version":1,"subgroups":{"entries":[],"max_rss":3}}`))
	server.PatchAnimeSubgroups(response, request, "target")
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Rows []struct {
			Available  *bool  `json:"available"`
			Generation *int64 `json:"availability_generation"`
		} `json:"effective_selection"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("body=%s", response.Body.String())
	}
	for i, want := range []struct {
		available  bool
		generation int64
	}{{true, 17}, {false, 23}} {
		row := body.Rows[i]
		if row.Available == nil || *row.Available != want.available || row.Generation == nil || *row.Generation != want.generation {
			t.Fatalf("row %d=%+v", i, row)
		}
	}
}
