package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// One-shot execution-attempt reads over HTTP. The handlers own request decoding
// and the response mapping; the finite one-shot application service owns the
// reads. These tests build the real service over a scripted marker port and
// assert exactly the transport's own behaviour: the values it forwards, the
// paging contract it renders and the typed errors it maps.

// errOneShotPortNotExercised marks a marker-port method the transport tests
// never reach, so an unexpected call fails loudly instead of returning a zero
// value.
var errOneShotPortNotExercised = errors.New("one-shot marker port method not exercised by the transport test")

// stubOneShotMarkers is the scripted animeport.OneShotMarkerRepository used by
// the transport tests. It records the read arguments the handlers cause the
// service to issue and returns scripted rows or typed errors.
type stubOneShotMarkers struct {
	marker     *animeport.OneShotMarkerRecord
	markerErr  error
	attempt    *animeport.OneShotAttemptRecord
	attemptErr error
	rows       []animeport.OneShotAttemptRecord
	rowsErr    error

	markerID           string
	attemptAcquisition string
	attemptID          string
	rowsAcquisition    string
	rowsBeforeSequence *int64
	rowsLimit          int
}

func (s *stubOneShotMarkers) Insert(context.Context, txscope.Tx, animeport.OneShotMarkerRecord) error {
	return errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) GetByRequestIdempotencyKey(context.Context, string) (*animeport.OneShotMarkerRecord, error) {
	return nil, errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) LockByRequestIdempotencyKey(context.Context, txscope.Tx, string) (*animeport.OneShotMarkerRecord, error) {
	return nil, errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) GetByID(_ context.Context, id string) (*animeport.OneShotMarkerRecord, error) {
	s.markerID = id
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	return s.marker, nil
}

func (s *stubOneShotMarkers) LockStateByManagedAnimeID(context.Context, txscope.Tx, string) (string, bool, error) {
	return "", false, errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) InsertAttemptTx(context.Context, txscope.Tx, animeport.OneShotAttemptRecord) error {
	return errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) GetAttemptByID(context.Context, string) (*animeport.OneShotAttemptRecord, error) {
	return nil, errOneShotPortNotExercised
}

func (s *stubOneShotMarkers) GetAttemptForAcquisition(_ context.Context, acquisitionID, attemptID string) (*animeport.OneShotAttemptRecord, error) {
	s.attemptAcquisition = acquisitionID
	s.attemptID = attemptID
	if s.attemptErr != nil {
		return nil, s.attemptErr
	}
	return s.attempt, nil
}

func (s *stubOneShotMarkers) ListAttemptsByAcquisition(_ context.Context, acquisitionID string, beforeSequence *int64, limit int) ([]animeport.OneShotAttemptRecord, error) {
	s.rowsAcquisition = acquisitionID
	s.rowsBeforeSequence = beforeSequence
	s.rowsLimit = limit
	if s.rowsErr != nil {
		return nil, s.rowsErr
	}
	return s.rows, nil
}

// newOneShotTransportService builds the real finite one-shot application
// service over the scripted marker port. The database handle is required by the
// constructor but is never dialled: every case here either fails the
// pre-transaction validation or is answered entirely by the marker port, so the
// transport tests stay PostgreSQL-free.
func newOneShotTransportService(t *testing.T, markers *stubOneShotMarkers) *oneshotapplication.Service {
	t.Helper()
	db, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatalf("open unused database handle: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatalf("work item store: %v", err)
	}
	provisioner := animeapplication.NewProvisioner(nil)
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatalf("construct draft service: %v", err)
	}
	service, err := oneshotapplication.NewService(db, oneshotapplication.Dependencies{
		WorkItems: store, OneShotMarkers: markers, Provisioner: &provisioner,
		CandidateRevisions: draftService,
	})
	if err != nil {
		t.Fatalf("construct one-shot service: %v", err)
	}
	return service
}

func oneShotServer(service OneShotService) *Server {
	return &Server{deps: Dependencies{OneShot: service}}
}

func decodeOneShotError(t *testing.T, recorder *httptest.ResponseRecorder) generated.ErrorResponse {
	t.Helper()
	var body generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v (%s)", err, recorder.Body.String())
	}
	return body
}

// TestListOneShotExecutionAttemptsHandlerRendersKeysetPage drives the real
// service's keyset read through the handler: the HTTP limit and the opaque
// cursor must reach the marker port, and the page/next-cursor projection must
// round-trip back into the next request.
func TestListOneShotExecutionAttemptsHandlerRendersKeysetPage(t *testing.T) {
	terminal := time.Unix(30, 0).UTC()
	failedStage := animeport.OneShotStageDownload
	reason := "one_shot_failed"
	markers := &stubOneShotMarkers{
		marker: &animeport.OneShotMarkerRecord{ID: "one-shot-1"},
		rows: []animeport.OneShotAttemptRecord{
			{
				ID: "attempt-3", AcquisitionID: "one-shot-1", Sequence: 3, Mode: animeport.OneShotAttemptModeRetry,
				State: "failed", InputDigest: "sha256:" + strings.Repeat("a", 64), CreatedAt: time.Unix(10, 0).UTC(),
				TerminalAt: &terminal, FailedStage: &failedStage, OutcomeCode: &reason,
			},
			{
				ID: "attempt-2", AcquisitionID: "one-shot-1", Sequence: 2, Mode: animeport.OneShotAttemptModeInitial,
				State: "failed", InputDigest: "sha256:" + strings.Repeat("b", 64), CreatedAt: time.Unix(5, 0).UTC(),
			},
		},
	}
	service := newOneShotTransportService(t, markers)
	limit := 1

	firstRecorder := httptest.NewRecorder()
	oneShotServer(service).ListOneShotExecutionAttempts(firstRecorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts?limit=1", nil), "one-shot-1", generated.ListOneShotExecutionAttemptsParams{Limit: &limit})
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", firstRecorder.Code, firstRecorder.Body.String())
	}
	if markers.markerID != "one-shot-1" || markers.rowsAcquisition != "one-shot-1" {
		t.Fatalf("marker read = %q rows read = %q, want one-shot-1", markers.markerID, markers.rowsAcquisition)
	}
	if markers.rowsLimit != 2 || markers.rowsBeforeSequence != nil {
		t.Fatalf("first page port args = limit %d before %v, want 2/nil", markers.rowsLimit, markers.rowsBeforeSequence)
	}
	var firstPage generated.OneShotExecutionAttemptPage
	if err := json.Unmarshal(firstRecorder.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].Sequence != 3 || firstPage.Items[0].State != generated.OneShotExecutionAttemptSummaryState("failed") {
		t.Fatalf("page = %+v", firstPage)
	}
	if firstPage.Items[0].FailedStage == nil || string(*firstPage.Items[0].FailedStage) != "download" {
		t.Fatalf("failed stage was not projected: %+v", firstPage.Items[0])
	}
	if firstPage.NextCursor == nil {
		t.Fatal("a page with one row of headroom must return a next cursor")
	}
	if want := base64.RawURLEncoding.EncodeToString([]byte("1|3")); *firstPage.NextCursor != want {
		t.Fatalf("next cursor = %q, want %q", *firstPage.NextCursor, want)
	}
	// The first page's opaque cursor must be the second page's keyset position.
	cursor := generated.Cursor(*firstPage.NextCursor)
	secondRecorder := httptest.NewRecorder()
	oneShotServer(service).ListOneShotExecutionAttempts(secondRecorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts?limit=1&cursor=next", nil), "one-shot-1", generated.ListOneShotExecutionAttemptsParams{Limit: &limit, Cursor: &cursor})
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second page status = %d, want 200 (%s)", secondRecorder.Code, secondRecorder.Body.String())
	}
	if markers.rowsBeforeSequence == nil || *markers.rowsBeforeSequence != 3 {
		t.Fatalf("second page keyset = %v, want sequence 3", markers.rowsBeforeSequence)
	}

	// A malformed cursor is a typed 400 from the service, never an empty page.
	badCursor := generated.Cursor("not-a-cursor")
	badRecorder := httptest.NewRecorder()
	oneShotServer(service).ListOneShotExecutionAttempts(badRecorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts?limit=1&cursor=bad", nil), "one-shot-1", generated.ListOneShotExecutionAttemptsParams{Limit: &limit, Cursor: &badCursor})
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor status = %d, want 400 (%s)", badRecorder.Code, badRecorder.Body.String())
	}
	if body := decodeOneShotError(t, badRecorder); body.Code != "INVALID_CURSOR" {
		t.Fatalf("bad cursor code = %q, want INVALID_CURSOR", body.Code)
	}

	// An out-of-range page size is refused by the service before any owner read.
	outOfRange := 1000
	rangeRecorder := httptest.NewRecorder()
	oneShotServer(service).ListOneShotExecutionAttempts(rangeRecorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts?limit=1000", nil), "one-shot-1", generated.ListOneShotExecutionAttemptsParams{Limit: &outOfRange})
	if rangeRecorder.Code != http.StatusBadRequest {
		t.Fatalf("limit status = %d, want 400 (%s)", rangeRecorder.Code, rangeRecorder.Body.String())
	}
	if body := decodeOneShotError(t, rangeRecorder); body.Code != "LIMIT_INVALID" {
		t.Fatalf("limit code = %q, want LIMIT_INVALID", body.Code)
	}
}

// TestGetOneShotExecutionAttemptHandlerScopesToAcquisition proves the attempt
// detail is read through the acquisition-scoped port read, and that a missing
// attempt or a purged acquisition stays a typed 404.
func TestGetOneShotExecutionAttemptHandlerScopesToAcquisition(t *testing.T) {
	markers := &stubOneShotMarkers{
		marker: &animeport.OneShotMarkerRecord{ID: "one-shot-1"},
		attempt: &animeport.OneShotAttemptRecord{
			ID: "attempt-1", AcquisitionID: "one-shot-1", Sequence: 1, Mode: animeport.OneShotAttemptModeInitial,
			State: "failed", InputDigest: "sha256:" + strings.Repeat("b", 64),
			CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(20, 0).UTC(),
		},
	}
	service := newOneShotTransportService(t, markers)
	recorder := httptest.NewRecorder()
	oneShotServer(service).GetOneShotExecutionAttempt(recorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts/attempt-1", nil), "one-shot-1", "attempt-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	if markers.attemptAcquisition != "one-shot-1" || markers.attemptID != "attempt-1" {
		t.Fatalf("scoped read = %q/%q, want one-shot-1/attempt-1", markers.attemptAcquisition, markers.attemptID)
	}
	var detail generated.OneShotExecutionAttempt
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if string(detail.AcquisitionId) != "one-shot-1" || detail.Sequence != 1 || detail.State != generated.OneShotExecutionAttemptState("failed") {
		t.Fatalf("detail = %+v", detail)
	}

	// A cross-acquisition or missing attempt is a typed 404.
	markers.attemptErr = sql.ErrNoRows
	recorder = httptest.NewRecorder()
	oneShotServer(service).GetOneShotExecutionAttempt(recorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-1/attempts/attempt-other", nil), "one-shot-1", "attempt-other")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing attempt status = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}

	// A purged acquisition is a typed 404 before any attempt read.
	markers.attemptErr = nil
	markers.markerErr = sql.ErrNoRows
	recorder = httptest.NewRecorder()
	oneShotServer(service).GetOneShotExecutionAttempt(recorder, httptest.NewRequest(http.MethodGet, "http://test/api/v1/one-shot-acquisitions/one-shot-purged/attempts/attempt-1", nil), "one-shot-purged", "attempt-1")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("purged acquisition status = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}
}
