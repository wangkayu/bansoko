package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

// TestImportRetryEndpointRefusesARunningRecordZeroWrite proves the record's
// import retry is authenticated, needs its If-Match version, and answers a
// typed 409 for a record that still runs, writing nothing. The retired
// per-file retry route is gone.
func TestImportRetryEndpointRefusesARunningRecordZeroWrite(t *testing.T) {
	rig := newTestRig(t, "development", false)
	if recorder, body := rig.do(t, http.MethodPost, "/api/v1/bootstrap/owner", map[string]string{"username": "owner", "password": "correct horse battery", "session_transport": "token"}, nil); recorder.Code != http.StatusCreated {
		t.Fatalf("bootstrap %d %s", recorder.Code, body)
	}
	token := loginSession(t, rig, "owner", "correct horse battery")
	animetest.SeedReleaseOwnerChain(t, rig.db, "source-http-import-retry")
	animetest.SeedCompletedFileOp(t, rig.db, "source-http-import-retry", 1, false, "failed")
	const root = "acq-task-rel-ep-1"
	var version int64
	if err := rig.db.QueryRow(`SELECT version FROM acquisitions WHERE id=$1`, root).Scan(&version); err != nil {
		t.Fatal(err)
	}
	owner := disposabledb.Owner(t)
	defer owner.Close()
	before := acquisitionfixture.Snapshot(t, owner)
	path := "/api/v1/acquisitions/" + root + "/import/retry"
	headers := map[string]string{"Idempotency-Key": "http-import-retry-key", "If-Match": strconv.FormatInt(version, 10)}

	recorder, _ := rig.do(t, http.MethodPost, path, nil, headers)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated retry %d", recorder.Code)
	}
	recorder, body := rig.do(t, http.MethodPost, path, nil, map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "http-import-retry-key"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("retry without If-Match %d %s", recorder.Code, body)
	}
	headers["Authorization"] = "Bearer " + token
	recorder, body = rig.do(t, http.MethodPost, path, nil, headers)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("running record retry %d %s", recorder.Code, body)
	}
	if result := decodeBody[generated.ErrorResponse](t, body); result.Code != "IMPORT_RETRY_IN_FLIGHT" {
		t.Fatalf("running record response %s", body)
	}
	recorder, _ = rig.do(t, http.MethodPost, "/api/v1/file-operations/op-rel-ep-1/retry",
		map[string]string{"failed_work_item_id": "w", "reason": "retired"}, map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "http-retired-file-key"})
	if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired per-file retry route answered %d", recorder.Code)
	}
	if acquisitionfixture.Snapshot(t, owner) != before {
		t.Fatal("refused import retry wrote graph")
	}
}
