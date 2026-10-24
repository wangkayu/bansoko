package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapplication "bansoko/internal/accesscontrol/application"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

// parserPolicyDefaultServer builds the real handler stack over a disposable
// schema. wire=false leaves the parser policy owner unwired so the fail-closed
// 404 path is exercised through the same HTTP surface.
func parserPolicyDefaultServer(t *testing.T, wire bool) *Server {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	t.Cleanup(func() { db.Close() })
	service := filemaintenance.NewService(nil, nil, nil, nil, nil)
	if wire {
		service.SetParserPolicy(parserpolicy.NewService(db))
	}
	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	server, err := NewServer(Dependencies{DB: db, Access: access, FileMaintenance: service})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// seedParserPolicyDefault writes the singleton row directly so the read path is
// proven against stored truth rather than against the code default.
func seedParserPolicyDefault(t *testing.T, server *Server, kind string, version int64) {
	t.Helper()
	if _, err := server.deps.DB.Exec(`
INSERT INTO global_config_sections(section_name, schema_version, value, version, updated_at)
VALUES ('parser_policy', 'parser-policy-v1', jsonb_build_object('parser_kind', $1::text), $2, CURRENT_TIMESTAMP)
ON CONFLICT (section_name) DO UPDATE SET value = EXCLUDED.value, version = EXCLUDED.version, updated_at = EXCLUDED.updated_at`,
		kind, version); err != nil {
		t.Fatal(err)
	}
}

func getParserPolicyDefault(server *Server) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-maintenance/parser-policy/default", nil)
	recorder := httptest.NewRecorder()
	server.GetParserPolicyDefault(recorder, req)
	return recorder
}

// parserPolicyDefaultVersion reads the version the shipped migration chain left
// on the singleton template. The baseline writes 1 and migration 012's
// parser-kind rename bumps it, so a test must not assume 1: read the row.
func parserPolicyDefaultVersion(t *testing.T, server *Server) int {
	t.Helper()
	recorder := getParserPolicyDefault(server)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.ParserPolicyDefaultView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view.Version
}

func putParserPolicyDefault(server *Server, expectedVersion int, kind string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(generated.ParserPolicyDefaultInput{ExpectedVersion: expectedVersion, ParserKind: kind})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/file-maintenance/parser-policy/default", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.SaveParserPolicyDefault(recorder, req)
	return recorder
}

// clearParserPolicyDefault removes the chain-seeded row so the absent-row path
// and the create save are reachable.
func clearParserPolicyDefault(t *testing.T, server *Server) {
	t.Helper()
	if _, err := server.deps.DB.Exec(
		`DELETE FROM global_config_sections WHERE section_name = 'parser_policy'`); err != nil {
		t.Fatal(err)
	}
}

// An absent row is a 200 with the versioned code default and version 0, never a
// 404: "never saved" is a valid template state, distinct from "no such
// resource".
func TestGetParserPolicyDefaultReportsTheCodeDefaultWhenAbsent(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)
	clearParserPolicyDefault(t, server)

	recorder := getParserPolicyDefault(server)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.ParserPolicyDefaultView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ParserKind != "reltitle" || view.Version != 0 || view.UpdatedAt != nil {
		t.Fatalf("absent view=%+v, want reltitle/0/omitted-updated_at", view)
	}
}

// A stored row reads back with its own value and CAS version: this is the
// version the client must send back, never the code default's 0.
func TestGetParserPolicyDefaultReadsStoredTruth(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)
	seedParserPolicyDefault(t, server, "regex", 3)

	recorder := getParserPolicyDefault(server)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.ParserPolicyDefaultView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ParserKind != "regex" || view.Version != 3 || view.UpdatedAt == nil {
		t.Fatalf("stored view=%+v, want regex/3/updated_at", view)
	}
}

// The create save (expected_version 0) answers the new row: version 1, so the
// client can render the fresh template without a follow-up read. The baseline
// seeds the row, so the create path is reached by removing it first.
func TestPutParserPolicyDefaultCreatesTheRow(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)
	clearParserPolicyDefault(t, server)

	recorder := putParserPolicyDefault(server, 0, "regex")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.ParserPolicyDefaultView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ParserKind != "regex" || view.Version != 1 || view.UpdatedAt == nil {
		t.Fatalf("created view=%+v, want regex/1/updated_at", view)
	}
	// The save is durable: the read agrees.
	if reread := getParserPolicyDefault(server); reread.Code != http.StatusOK || !strings.Contains(reread.Body.String(), `"version":1`) {
		t.Fatalf("re-read after create: %d body=%s", reread.Code, reread.Body.String())
	}
}

// The seeded row is the real starting state: saving with the matching expected
// version advances it, and the new version is what a follow-up save must send
// back. The shipped chain seeds the row at whatever version the forward
// migrations last wrote (the baseline writes 1, migration 012's parser-kind
// rename bumps it), so the expected version is read from the row.
func TestPutParserPolicyDefaultAdvancesTheSeededRow(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)

	seeded := parserPolicyDefaultVersion(t, server)

	recorder := putParserPolicyDefault(server, seeded, "regex")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.ParserPolicyDefaultView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ParserKind != "regex" || view.Version != seeded+1 || view.UpdatedAt == nil {
		t.Fatalf("saved view=%+v, want regex/%d/updated_at", view, seeded+1)
	}
}

// A stale expected version is a typed 409 carrying the current version, so the
// client can offer a re-read instead of silently overwriting.
func TestPutParserPolicyDefaultRejectsStaleExpectedVersion(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)
	seedParserPolicyDefault(t, server, "regex", 3)

	recorder := putParserPolicyDefault(server, 2, "regex")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "OWNER_VERSION_CONFLICT" {
		t.Fatalf("error code: %s body=%s", response.Code, recorder.Body.String())
	}
	if response.CurrentVersion == nil || *response.CurrentVersion != 3 {
		t.Fatalf("current version: %v body=%s", response.CurrentVersion, recorder.Body.String())
	}
	// A refused save must not have advanced the row.
	if reread := getParserPolicyDefault(server); !strings.Contains(reread.Body.String(), `"version":3`) {
		t.Fatalf("row advanced by a refused save: %s", reread.Body.String())
	}
}

// An unregistered parser kind is a 422 POLICY_INVALID before any write: the
// stored row keeps its previous value and version.
func TestPutParserPolicyDefaultRejectsAnUnregisteredKind(t *testing.T) {
	server := parserPolicyDefaultServer(t, true)
	seedParserPolicyDefault(t, server, "regex", 3)

	recorder := putParserPolicyDefault(server, 3, "not-a-registered-parser")
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "POLICY_INVALID" {
		t.Fatalf("error code: %s body=%s", response.Code, recorder.Body.String())
	}
	if reread := getParserPolicyDefault(server); !strings.Contains(reread.Body.String(), `"parser_kind":"regex"`) || !strings.Contains(reread.Body.String(), `"version":3`) {
		t.Fatalf("row changed by a rejected save: %s", reread.Body.String())
	}
}

// An unwired owner answers a typed 404 on both verbs instead of an empty 200 or
// a silent write.
func TestParserPolicyDefaultFailsClosedWhenUnwired(t *testing.T) {
	server := parserPolicyDefaultServer(t, false)

	if recorder := getParserPolicyDefault(server); recorder.Code != http.StatusNotFound {
		t.Fatalf("unwired read status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder := putParserPolicyDefault(server, 0, "regex")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unwired save status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "NOT_FOUND" {
		t.Fatalf("unwired save code: %s", response.Code)
	}
}
