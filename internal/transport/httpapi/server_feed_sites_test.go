package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// The API list is the code declaration, entry for entry, and every id and
// identity the code declares is a value the contract enumerates.
func TestListFeedSitesServesTheDeclaredList(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).ListFeedSites(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/feed-sites", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body generated.FeedSiteList
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	declared := acquisitiondomain.FeedSites()
	if len(body.Sites) != len(declared) {
		t.Fatalf("sites = %d, want %d", len(body.Sites), len(declared))
	}
	ids := map[generated.FeedSiteId]bool{
		generated.FeedSiteIdMikan: true, generated.FeedSiteIdAnibt: true, generated.FeedSiteIdNyaa: true,
		generated.FeedSiteIdAcgrip: true, generated.FeedSiteIdBangumiMoe: true, generated.FeedSiteIdMiobt: true,
		generated.FeedSiteIdUnit3d: true,
	}
	for index, site := range body.Sites {
		if string(site.Id) != string(declared[index].ID) || site.Name != declared[index].Name || len(site.Hosts) != len(declared[index].Hosts) {
			t.Fatalf("site %d = %+v, want %+v", index, site, declared[index])
		}
		if !ids[site.Id] || (site.Identity != generated.FeedSiteIdentityFeed && site.Identity != generated.FeedSiteIdentityExpansion) {
			t.Fatalf("site %+v is outside the contract enums", site)
		}
		if string(site.Match) != string(declared[index].Match) || (site.Match != generated.FeedSiteMatchHost && site.Match != generated.FeedSiteMatchContent) {
			t.Fatalf("site %+v match is outside the contract enum", site)
		}
	}
	// A content-matched entry serves an empty host array, never null.
	var raw struct {
		Sites []map[string]json.RawMessage `json:"sites"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, site := range raw.Sites {
		if string(site["hosts"]) == "null" {
			t.Fatalf("hosts must be an array: %s", site["id"])
		}
	}
}
