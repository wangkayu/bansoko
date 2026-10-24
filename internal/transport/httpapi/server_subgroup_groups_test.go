package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

// subgroupDictionarySpy is the transport-boundary stub for the subgroup
// dictionary: it records what the generated route handed to the handler, so a
// test can prove the provider, the raw query and the layer filter survive the
// HTTP boundary. It embeds catalogCallSpy to satisfy the rest of CatalogService.
type subgroupDictionarySpy struct {
	catalogCallSpy
	entries     []catalogdomain.GroupDictionaryEntry
	nextOffset  *int
	total       int
	listing     catalogapplication.GroupDictionaryListing
	listErr     error
	retrieveErr error
	deleteErr   error
	listCalls   int
	listQuery   string
	listSource  string
	retrieve    string
	deletions   []string
}

func (s *subgroupDictionarySpy) ListGroupDictionary(_ context.Context, listing catalogapplication.GroupDictionaryListing) (catalogapplication.GroupDictionaryPage, error) {
	s.listCalls++
	s.listing = listing
	s.listQuery, s.listSource = listing.Provider+"|"+listing.Query, listing.Source
	return catalogapplication.GroupDictionaryPage{Entries: s.entries, NextOffset: s.nextOffset, Total: s.total}, s.listErr
}

func (s *subgroupDictionarySpy) RetrieveGroupDictionary(_ context.Context, provider, query string) ([]catalogdomain.GroupDictionaryEntry, error) {
	s.retrieve = provider + "|" + query
	return s.entries, s.retrieveErr
}

func (s *subgroupDictionarySpy) DeleteGroupDictionaryEntry(_ context.Context, provider, groupRef string) error {
	s.deletions = append(s.deletions, provider+":"+groupRef)
	return s.deleteErr
}

func newSubgroupDictionaryTestServer(dictionary CatalogService) *Server {
	return &Server{deps: Dependencies{
		Catalog:       dictionary,
		SkipAuth:      true,
		RuntimeLogger: observabilityport.NopRuntimeLogger{},
	}}
}

func dictionaryEntry(provider, groupRef, name string, available bool, source catalogdomain.GroupDictionarySource) catalogdomain.GroupDictionaryEntry {
	return catalogdomain.GroupDictionaryEntry{Provider: provider, GroupRef: groupRef, DisplayName: name, Available: available, Source: source}
}

func listRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "/api/v1/config/subgroups/groups?"+query, nil)
}

func retrieveRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/config/subgroups/groups", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func decodeErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder) generated.ErrorResponse {
	t.Helper()
	var decoded generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, recorder.Body.String())
	}
	return decoded
}

func TestListSubgroupGroupCandidatesMapsEveryCandidateField(t *testing.T) {
	dictionary := &subgroupDictionarySpy{entries: []catalogdomain.GroupDictionaryEntry{
		dictionaryEntry("mikan", "58", "SampleGroup", true, catalogdomain.GroupDictionarySourceCollected),
		// A retired collected id is reported with its key and no name, never hidden.
		dictionaryEntry("mikan", "68", "", false, catalogdomain.GroupDictionarySourceCollected),
		dictionaryEntry("mikan", "700", "New Group", true, catalogdomain.GroupDictionarySourceUser),
	}}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=mikan&q="+url.QueryEscape("loli")+"&source=user"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	validateAgainstOpenAPI(t, "SubgroupGroupCandidatePage", recorder.Body.Bytes())
	if dictionary.listQuery != "mikan|loli" || dictionary.listSource != "user" {
		t.Fatalf("the route handed provider/query/source as %q source=%q", dictionary.listQuery, dictionary.listSource)
	}
	var decoded generated.SubgroupGroupCandidatePage
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode candidates: %v; body=%s", err, recorder.Body.String())
	}
	if len(decoded.Candidates) != 3 {
		t.Fatalf("unexpected candidate count: %+v", decoded.Candidates)
	}
	first := decoded.Candidates[0]
	if first.Provider != generated.ProviderKindMikan || first.GroupRef != "58" || first.DisplayName != "SampleGroup" ||
		first.Source != generated.SubgroupGroupCandidateSourceCollected || !first.Available {
		t.Fatalf("collected candidate drifted: %+v", first)
	}
	if retired := decoded.Candidates[1]; retired.GroupRef != "68" || retired.DisplayName != "" || retired.Available {
		t.Fatalf("retired candidate drifted: %+v", retired)
	}
	if added := decoded.Candidates[2]; added.GroupRef != "700" || added.Source != generated.SubgroupGroupCandidateSourceUser {
		t.Fatalf("user candidate drifted: %+v", added)
	}
}

// Every filter is optional, and an unfiltered, unsourced listing is legal: the
// handler hands the absence through as zero values and the service pages it.
func TestListSubgroupGroupCandidatesPassesAbsentFiltersThrough(t *testing.T) {
	dictionary := &subgroupDictionarySpy{}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=anibt"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	want := catalogapplication.GroupDictionaryListing{Provider: "anibt"}
	if dictionary.listing != want {
		t.Fatalf("absent filters reached the service as %+v", dictionary.listing)
	}
	validateAgainstOpenAPI(t, "SubgroupGroupCandidatePage", recorder.Body.Bytes())
	if body := strings.TrimSpace(recorder.Body.String()); body != `{"candidates":[],"next_offset":null,"total":0}` {
		t.Fatalf("empty result body = %s", body)
	}
}

// The paging parameters and hide_retired cross the boundary, and the page's
// continuation and total come back in the body.
func TestListSubgroupGroupCandidatesPagesThroughTheService(t *testing.T) {
	next := 100
	dictionary := &subgroupDictionarySpy{
		entries:    []catalogdomain.GroupDictionaryEntry{dictionaryEntry("mikan", "58", "SampleGroup", true, catalogdomain.GroupDictionarySourceCollected)},
		nextOffset: &next,
		total:      367,
	}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=mikan&hide_retired=true&offset=50&limit=50"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	want := catalogapplication.GroupDictionaryListing{Provider: "mikan", HideRetired: true, Offset: 50, Limit: 50}
	if dictionary.listing != want {
		t.Fatalf("paging reached the service as %+v", dictionary.listing)
	}
	validateAgainstOpenAPI(t, "SubgroupGroupCandidatePage", recorder.Body.Bytes())
	var decoded generated.SubgroupGroupCandidatePage
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode page: %v; body=%s", err, recorder.Body.String())
	}
	if decoded.NextOffset == nil || *decoded.NextOffset != 100 || decoded.Total != 367 || len(decoded.Candidates) != 1 {
		t.Fatalf("page drifted: %+v", decoded)
	}
}

// A limit below one is caller input the service never sees.
func TestListSubgroupGroupCandidatesRejectsAZeroLimit(t *testing.T) {
	dictionary := &subgroupDictionarySpy{}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=mikan&limit=0"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if dictionary.listCalls != 0 {
		t.Fatalf("a zero limit reached the service")
	}
}

// The listing is local, so it answers with no provider connection configured.
// The transport proves it by never consulting the live resolver: no
// provider-adapter error can appear on this path.
func TestListSubgroupGroupCandidatesAnswersWithoutAProviderConnection(t *testing.T) {
	dictionary := &subgroupDictionarySpy{entries: []catalogdomain.GroupDictionaryEntry{
		dictionaryEntry("mikan", "58", "SampleGroup", true, catalogdomain.GroupDictionarySourceCollected),
	}}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=mikan&q=58"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("a local listing failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if dictionary.retrieve != "" || len(dictionary.deletions) != 0 {
		t.Fatalf("a listing touched the retrieval path: %q %v", dictionary.retrieve, dictionary.deletions)
	}
}

func TestListSubgroupGroupCandidatesMapsARejectedRequestTo400(t *testing.T) {
	dictionary := &subgroupDictionarySpy{listErr: catalogapplication.ErrGroupDictionaryRequestInvalid}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, listRequest(t, "provider=mikan"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decoded := decodeErrorResponse(t, recorder); decoded.Code != "SUBGROUP_DICTIONARY_REQUEST_INVALID" {
		t.Fatalf("error code drifted: %+v", decoded)
	}
}

func TestRetrieveSubgroupGroupCandidatePostsTheQuery(t *testing.T) {
	dictionary := &subgroupDictionarySpy{entries: []catalogdomain.GroupDictionaryEntry{
		dictionaryEntry("anibt", "newsub", "New Sub", true, catalogdomain.GroupDictionarySourceUser),
	}}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, retrieveRequest(t, `{"provider":"anibt","query":"New Sub"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	validateAgainstOpenAPI(t, "SubgroupGroupCandidateList", recorder.Body.Bytes())
	if dictionary.retrieve != "anibt|New Sub" {
		t.Fatalf("the body reached the service as %q", dictionary.retrieve)
	}
	var decoded generated.SubgroupGroupCandidateList
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode candidates: %v; body=%s", err, recorder.Body.String())
	}
	if len(decoded.Candidates) != 1 || decoded.Candidates[0].Source != generated.SubgroupGroupCandidateSourceUser {
		t.Fatalf("retrieved candidates drifted: %+v", decoded.Candidates)
	}
}

// A retrieval that needs a live provider read is the one path that can fail on
// configuration: it keeps the provider adapter's 503 and its code.
func TestRetrieveSubgroupGroupCandidateKeepsTheProviderUnavailable503(t *testing.T) {
	dictionary := &subgroupDictionarySpy{retrieveErr: &apperrors.Business{
		Status: http.StatusServiceUnavailable, Code: "PROVIDER_GROUP_LOOKUP_FAILED", Message: "no connection configured",
	}}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, retrieveRequest(t, `{"provider":"mikan","query":"700"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decoded := decodeErrorResponse(t, recorder); decoded.Code != "PROVIDER_GROUP_LOOKUP_FAILED" {
		t.Fatalf("error code drifted: %+v", decoded)
	}
}

// Discovery takes a key only: the service's refusal of a name is a 400 with
// its own code, so the client can say what to enter.
func TestRetrieveSubgroupGroupCandidateMapsANameTo400(t *testing.T) {
	dictionary := &subgroupDictionarySpy{retrieveErr: catalogapplication.ErrGroupDictionaryQueryNotKey}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, retrieveRequest(t, `{"provider":"anibt","query":"示例协作组"}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decoded := decodeErrorResponse(t, recorder); decoded.Code != "SUBGROUP_DISCOVER_KEY_REQUIRED" {
		t.Fatalf("error code drifted: %+v", decoded)
	}
}

func TestRetrieveSubgroupGroupCandidateRejectsABrokenBody(t *testing.T) {
	dictionary := &subgroupDictionarySpy{}
	recorder := httptest.NewRecorder()
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, retrieveRequest(t, `{"provider":`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if dictionary.retrieve != "" {
		t.Fatalf("a broken body reached the service as %q", dictionary.retrieve)
	}
}

func TestDeleteSubgroupGroupDictionaryEntryMapsItsOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "user entry removed", status: http.StatusNoContent},
		{
			name: "collected entry is immutable", err: catalogapplication.ErrGroupDictionaryEntryImmutable,
			status: http.StatusConflict, code: "GROUP_DICTIONARY_ENTRY_IMMUTABLE",
		},
		{
			name: "entry in no layer", err: catalogapplication.ErrGroupDictionaryEntryNotFound,
			status: http.StatusNotFound, code: "GROUP_DICTIONARY_ENTRY_NOT_FOUND",
		},
		{
			name: "invalid request", err: catalogapplication.ErrGroupDictionaryRequestInvalid,
			status: http.StatusBadRequest, code: "SUBGROUP_DICTIONARY_REQUEST_INVALID",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dictionary := &subgroupDictionarySpy{deleteErr: testCase.err}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/config/subgroups/groups/mikan/700", nil)
			newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, request)
			if recorder.Code != testCase.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Join(dictionary.deletions, ",") != "mikan:700" {
				t.Fatalf("the route handed the deletion as %v", dictionary.deletions)
			}
			if testCase.code == "" {
				if strings.TrimSpace(recorder.Body.String()) != "" {
					t.Fatalf("a 204 carried a body: %s", recorder.Body.String())
				}
				return
			}
			if decoded := decodeErrorResponse(t, recorder); decoded.Code != testCase.code {
				t.Fatalf("error code drifted: %+v", decoded)
			}
		})
	}
}

// The anibt key is a slug and travels unchanged: only a Mikan id has a
// spelling to normalize.
func TestDeleteSubgroupGroupDictionaryEntryPassesSlugsUnchanged(t *testing.T) {
	dictionary := &subgroupDictionarySpy{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/config/subgroups/groups/anibt/samplegroup", nil)
	newSubgroupDictionaryTestServer(dictionary).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || strings.Join(dictionary.deletions, ",") != "anibt:samplegroup" {
		t.Fatalf("status=%d deletions=%v", recorder.Code, dictionary.deletions)
	}
}
