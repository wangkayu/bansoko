package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

// acquisitionRecordReader fakes only the execution-record reads; any other
// readmodel call panics through the nil embedded interface.
type acquisitionRecordReader struct {
	ReadmodelService
	query     readmodelapplication.AcquisitionRecordQuery
	detailErr error
	filesErr  error
	entryKind string
	oneShotID *string
}

// kind is the entry kind the fake reports; the zero value is a subscription
// record, the case that must never carry a marker handle.
func (f *acquisitionRecordReader) kind() string {
	if f.entryKind == "" {
		return "subscription"
	}
	return f.entryKind
}

func (f *acquisitionRecordReader) ListAcquisitionRecords(_ context.Context, query readmodelapplication.AcquisitionRecordQuery) (readmodelapplication.AcquisitionRecordPage, error) {
	f.query = query
	title := "[Group] Show - 01"
	return readmodelapplication.AcquisitionRecordPage{AsOf: time.Unix(10, 0).UTC(), Items: []readmodelapplication.AcquisitionRecordSummary{{
		AcquisitionID: "root-1", EntryKind: f.kind(), State: "running", RecordDetail: "full", AcceptedAt: time.Unix(5, 0).UTC(),
		OneShotID:   f.oneShotID,
		SourceTitle: readmodelapplication.AcquisitionDisplayValue{State: "visible", Value: &title},
		Stage:       ptrTo("download_wait"), StageState: ptrTo("running"),
	}}}, nil
}

func (f *acquisitionRecordReader) GetAcquisitionRecord(_ context.Context, id string) (readmodelapplication.AcquisitionRecordDetail, error) {
	if f.detailErr != nil {
		return readmodelapplication.AcquisitionRecordDetail{}, f.detailErr
	}
	detail := readmodelapplication.AcquisitionRecordDetail{ExecutionBranch: "download", AsOf: time.Unix(10, 0).UTC()}
	detail.AcquisitionID, detail.EntryKind, detail.State, detail.RecordDetail = id, f.kind(), "running", "full"
	detail.OneShotID = f.oneShotID
	detail.SourceTitle = readmodelapplication.AcquisitionDisplayValue{State: "redacted"}
	detail.Stages = []readmodelapplication.AcquisitionStage{{Stage: "submit", Availability: "present", State: ptrTo("succeeded")}}
	return detail, nil
}

func (f *acquisitionRecordReader) ListAcquisitionRecordFiles(_ context.Context, id string, _ readmodelapplication.AcquisitionFileQuery) (readmodelapplication.AcquisitionFilePage, error) {
	if f.filesErr != nil {
		return readmodelapplication.AcquisitionFilePage{}, f.filesErr
	}
	return readmodelapplication.AcquisitionFilePage{AcquisitionID: id, AsOf: time.Unix(10, 0).UTC()}, nil
}

func ptrTo[T any](value T) *T { return &value }

func decodeAPIErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	return body.Code
}

func TestAcquisitionRecordHandlers(t *testing.T) {
	reader := &acquisitionRecordReader{}
	server := &Server{deps: Dependencies{Readmodel: reader}}

	recorder := httptest.NewRecorder()
	state := generated.AcquisitionRecordState("running")
	anime := generated.ID("anime-1")
	server.ListAcquisitionRecords(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions", nil), generated.ListAcquisitionRecordsParams{State: &state, ManagedAnimeId: &anime})
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list status=%d cache=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	if reader.query.State != "running" || reader.query.ManagedAnimeID != "anime-1" {
		t.Fatalf("list filters not passed through: %+v", reader.query)
	}
	var page generated.AcquisitionRecordPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].Stage == nil || *page.Items[0].Stage != "download_wait" {
		t.Fatalf("list body %s: %v", recorder.Body.String(), err)
	}
	if page.Items[0].OneShotId != nil {
		t.Fatalf("subscription record must not carry one_shot_id: %v", *page.Items[0].OneShotId)
	}

	recorder = httptest.NewRecorder()
	server.GetAcquisitionRecord(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/root-1", nil), "root-1")
	var detail generated.AcquisitionRecordDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil || recorder.Code != http.StatusOK || detail.SourceTitle.Value != nil || len(detail.Stages) != 1 {
		t.Fatalf("detail status=%d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}
	if detail.OneShotId != nil {
		t.Fatalf("subscription detail must not carry one_shot_id: %v", *detail.OneShotId)
	}

	// A one-shot record carries its marker handle on both surfaces.
	oneShot := &acquisitionRecordReader{entryKind: "one_shot", oneShotID: ptrTo("osa-1")}
	oneShotServer := &Server{deps: Dependencies{Readmodel: oneShot}}
	recorder = httptest.NewRecorder()
	oneShotServer.ListAcquisitionRecords(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions", nil), generated.ListAcquisitionRecordsParams{})
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil || page.Items[0].OneShotId == nil || *page.Items[0].OneShotId != "osa-1" {
		t.Fatalf("one-shot list body %s: %v", recorder.Body.String(), err)
	}
	recorder = httptest.NewRecorder()
	oneShotServer.GetAcquisitionRecord(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/root-1", nil), "root-1")
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil || detail.OneShotId == nil || *detail.OneShotId != "osa-1" {
		t.Fatalf("one-shot detail body %s: %v", recorder.Body.String(), err)
	}

	for _, tc := range []struct {
		name   string
		reader *acquisitionRecordReader
		files  bool
		status int
		code   string
	}{
		{"missing record", &acquisitionRecordReader{detailErr: &apperrors.NotFound{Resource: "acquisition", ID: "root-x"}}, false, http.StatusNotFound, "ACQUISITION_NOT_FOUND"},
		{"missing record files", &acquisitionRecordReader{filesErr: &apperrors.NotFound{Resource: "acquisition", ID: "root-x"}}, true, http.StatusNotFound, "ACQUISITION_NOT_FOUND"},
		{"files not sealed", &acquisitionRecordReader{filesErr: readmodelapplication.ErrAcquisitionFilesNotReady}, true, http.StatusConflict, "ACQUISITION_FILES_NOT_READY"},
		{"compact files", &acquisitionRecordReader{filesErr: readmodelapplication.ErrAcquisitionDetailUnavailable}, true, http.StatusGone, "ACQUISITION_DETAIL_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{deps: Dependencies{Readmodel: tc.reader}}
			recorder := httptest.NewRecorder()
			if tc.files {
				server.ListAcquisitionRecordFiles(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/root-x/files", nil), "root-x", generated.ListAcquisitionRecordFilesParams{})
			} else {
				server.GetAcquisitionRecord(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/root-x", nil), "root-x")
			}
			if recorder.Code != tc.status || decodeAPIErrorCode(t, recorder) != tc.code {
				t.Fatalf("status=%d body=%s, want %d %s", recorder.Code, recorder.Body.String(), tc.status, tc.code)
			}
		})
	}
}
