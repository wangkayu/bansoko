package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
)

// ImportRetryResult is one accepted import-stage retry of an acquisition.
type ImportRetryResult struct {
	AcquisitionID      string
	Attempt            int64
	RetriedFiles       int64
	ReopenedPlacements int64
	Version            int64
	Replayed           bool
}

// ImportRetryService retries the import (file execution) stage of one settled
// acquisition from its execution record. The retry is addressed by the
// acquisition and rebuilt from durable owner state in one database command
// (acquisition_import_retry): every failed file effect is reset and queued
// again, every failed placement handoff of a completed file is reopened, and
// nothing earlier is redone. It never reads a failed WorkItem, so it works
// after retention pruned them.
type ImportRetryService struct {
	db       *sql.DB
	eventHub *events.Hub
}

func NewImportRetryService(db *sql.DB) (*ImportRetryService, error) {
	if db == nil {
		return nil, errors.New("import retry service requires a database")
	}
	return &ImportRetryService{db: db}, nil
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *ImportRetryService) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

// ImportRetryRequestHash is the request identity a replayed Idempotency-Key is
// compared with: the addressed acquisition and the version the caller saw.
func ImportRetryRequestHash(acquisitionID string, expectedVersion int64) string {
	canonical, _ := json.Marshal(struct {
		AcquisitionID   string `json:"acquisition_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{acquisitionID, expectedVersion})
	sum := sha256.Sum256(append([]byte("acquisition-import-retry:v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type importRetryAnswer struct {
	Status             string `json:"status"`
	Code               string `json:"code"`
	Replayed           bool   `json:"replayed"`
	Attempt            int64  `json:"attempt"`
	RetriedFiles       int64  `json:"retried_files"`
	ReopenedPlacements int64  `json:"reopened_placements"`
	Version            int64  `json:"version"`
}

// RetryImport runs the import stage of the acquisition again. expectedVersion
// is the record version the caller read; idempotencyKey replays an accepted
// request.
func (s *ImportRetryService) RetryImport(ctx context.Context, acquisitionID string, expectedVersion int64, idempotencyKey string) (ImportRetryResult, error) {
	acquisitionID = strings.TrimSpace(acquisitionID)
	if acquisitionID == "" {
		return ImportRetryResult{}, &apperrors.BadRequest{Code: "ACQUISITION_ID_REQUIRED", Message: "The acquisition is required."}
	}
	key := strings.TrimSpace(idempotencyKey)
	if len(key) < 8 || len(key) > 128 || key != idempotencyKey {
		return ImportRetryResult{}, &apperrors.Business{Status: 422, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "A bounded request idempotency key is required."}
	}
	if expectedVersion < 1 {
		return ImportRetryResult{}, &apperrors.VersionConflict{Owner: "acquisition", CurrentVersion: 0}
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT acquisition_import_retry($1, $2, $3, $4)`,
		acquisitionID, expectedVersion, key, ImportRetryRequestHash(acquisitionID, expectedVersion)).Scan(&raw)
	if err != nil {
		return ImportRetryResult{}, mapRetirementDBError(fmt.Errorf("retry acquisition import: %w", err))
	}
	var answer importRetryAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return ImportRetryResult{}, fmt.Errorf("decode acquisition import retry answer: %w", err)
	}
	switch answer.Status {
	case "accepted":
		if !answer.Replayed && s.eventHub != nil {
			s.eventHub.Publish(events.Event{Target: events.TargetRecords})
			s.eventHub.Publish(events.Event{Target: events.TargetRSS})
		}
		return ImportRetryResult{
			AcquisitionID:      acquisitionID,
			Attempt:            answer.Attempt,
			RetriedFiles:       answer.RetriedFiles,
			ReopenedPlacements: answer.ReopenedPlacements,
			Version:            answer.Version,
			Replayed:           answer.Replayed,
		}, nil
	case "not_found":
		return ImportRetryResult{}, &apperrors.NotFound{Resource: "acquisition", ID: acquisitionID}
	case "key_reused":
		return ImportRetryResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different request."}
	case "version_conflict":
		return ImportRetryResult{}, &apperrors.VersionConflict{Owner: "acquisition", CurrentVersion: answer.Version}
	case "refused":
		return ImportRetryResult{}, importRetryRefusal(answer.Code)
	default:
		return ImportRetryResult{}, fmt.Errorf("unexpected acquisition import retry status %q", answer.Status)
	}
}

func importRetryRefusal(code string) error {
	switch code {
	case "IMPORT_RETRY_IN_FLIGHT":
		return &apperrors.Business{Status: 409, Code: code, Message: "The record is still running."}
	case "MANAGED_ANIME_RETIRED":
		return managedAnimeRetiredError()
	case "IMPORT_RETRY_NOT_FOUND":
		return &apperrors.NotFound{Resource: "acquisition"}
	default:
		return &apperrors.Business{Status: 409, Code: "IMPORT_RETRY_NOT_APPLICABLE", Message: "The import stage has nothing failed to retry."}
	}
}
