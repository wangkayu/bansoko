package httpapi

import (
	"errors"
	"net/http"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/acquisitionrecord"
	"bansoko/internal/transport/httpapi/generated"
)

// Execution records: the read-only list, detail and file page of accepted
// acquisitions. The reads never lock, settle, retry or enqueue anything, and
// every response is no-store so polling reads a new snapshot.

// ListAcquisitionRecords returns one keyset page of execution records.
func (s *Server) ListAcquisitionRecords(w http.ResponseWriter, r *http.Request, params generated.ListAcquisitionRecordsParams) {
	if s.deps.Readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	query := readmodelapplication.AcquisitionRecordQuery{AcceptedFrom: params.From, AcceptedTo: params.To}
	if params.Limit != nil {
		query.Limit = *params.Limit
	}
	if params.Cursor != nil {
		query.Cursor = *params.Cursor
	}
	if params.ManagedAnimeId != nil {
		query.ManagedAnimeID = string(*params.ManagedAnimeId)
	}
	if params.State != nil {
		query.State = string(*params.State)
	}
	if params.EntryKind != nil {
		query.EntryKind = string(*params.EntryKind)
	}
	if params.Origin != nil {
		query.Origin = string(*params.Origin)
	}
	page, err := s.deps.Readmodel.ListAcquisitionRecords(r.Context(), query)
	if err != nil {
		writeAcquisitionRecordError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, acquisitionrecord.MapPage(page))
}

// GetAcquisitionRecord returns the stable detail of one execution record.
func (s *Server) GetAcquisitionRecord(w http.ResponseWriter, r *http.Request, acquisitionID generated.AcquisitionID) {
	if s.deps.Readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	detail, err := s.deps.Readmodel.GetAcquisitionRecord(r.Context(), string(acquisitionID))
	if err != nil {
		writeAcquisitionRecordError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, acquisitionrecord.MapDetail(detail))
}

// ListAcquisitionRecordFiles returns one manifest-order page of a record's files.
func (s *Server) ListAcquisitionRecordFiles(w http.ResponseWriter, r *http.Request, acquisitionID generated.AcquisitionID, params generated.ListAcquisitionRecordFilesParams) {
	if s.deps.Readmodel == nil {
		writeError(w, r, errors.New("readmodel service is not configured"))
		return
	}
	query := readmodelapplication.AcquisitionFileQuery{}
	if params.Limit != nil {
		query.Limit = *params.Limit
	}
	if params.Cursor != nil {
		query.Cursor = *params.Cursor
	}
	page, err := s.deps.Readmodel.ListAcquisitionRecordFiles(r.Context(), string(acquisitionID), query)
	if err != nil {
		writeAcquisitionRecordError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, acquisitionrecord.MapFilePage(page))
}

// writeAcquisitionRecordError maps the record read errors to their own codes;
// everything else takes the shared error boundary.
func writeAcquisitionRecordError(w http.ResponseWriter, r *http.Request, err error) {
	var notFound *apperrors.NotFound
	switch {
	case errors.As(err, &notFound):
		err = &APIError{Status: http.StatusNotFound, Code: "ACQUISITION_NOT_FOUND", Message: "The execution record was not found."}
	case errors.Is(err, readmodelapplication.ErrAcquisitionFilesNotReady):
		err = &APIError{Status: http.StatusConflict, Code: "ACQUISITION_FILES_NOT_READY", Message: "The record's file set is not sealed yet."}
	case errors.Is(err, readmodelapplication.ErrAcquisitionDetailUnavailable):
		err = &APIError{Status: http.StatusGone, Code: "ACQUISITION_DETAIL_UNAVAILABLE", Message: "The record's detail was reclaimed; only its summary remains."}
	}
	writeError(w, r, err)
}
