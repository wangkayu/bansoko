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

// DownloadCancelResult is one accepted download cancel of an acquisition.
type DownloadCancelResult struct {
	AcquisitionID string
	Version       int64
	Replayed      bool
}

// DownloadCancelService cancels the download of one running execution record
// before import. It only records the intent (acquisition_cancel); the download
// worker deletes the qB task with its data and settles the record cancelled.
type DownloadCancelService struct {
	db       *sql.DB
	eventHub *events.Hub
}

func NewDownloadCancelService(db *sql.DB) (*DownloadCancelService, error) {
	if db == nil {
		return nil, errors.New("download cancel service requires a database")
	}
	return &DownloadCancelService{db: db}, nil
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *DownloadCancelService) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

// DownloadCancelRequestHash is the request identity a replayed Idempotency-Key
// is compared with: the addressed acquisition and the version the caller saw.
func DownloadCancelRequestHash(acquisitionID string, expectedVersion int64) string {
	canonical, _ := json.Marshal(struct {
		AcquisitionID   string `json:"acquisition_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{acquisitionID, expectedVersion})
	sum := sha256.Sum256(append([]byte("acquisition-download-cancel:v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type downloadCancelAnswer struct {
	Status   string `json:"status"`
	Code     string `json:"code"`
	Replayed bool   `json:"replayed"`
	Version  int64  `json:"version"`
}

// CancelDownload cancels the download of the acquisition. expectedVersion is
// the record version the caller read; idempotencyKey replays an accepted
// request.
func (s *DownloadCancelService) CancelDownload(ctx context.Context, acquisitionID string, expectedVersion int64, idempotencyKey string) (DownloadCancelResult, error) {
	acquisitionID = strings.TrimSpace(acquisitionID)
	if acquisitionID == "" {
		return DownloadCancelResult{}, &apperrors.BadRequest{Code: "ACQUISITION_ID_REQUIRED", Message: "The acquisition is required."}
	}
	key := strings.TrimSpace(idempotencyKey)
	if len(key) < 8 || len(key) > 128 || key != idempotencyKey {
		return DownloadCancelResult{}, &apperrors.Business{Status: 422, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "A bounded request idempotency key is required."}
	}
	if expectedVersion < 1 {
		return DownloadCancelResult{}, &apperrors.VersionConflict{Owner: "acquisition", CurrentVersion: 0}
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT acquisition_cancel($1, $2, $3, $4)`,
		acquisitionID, expectedVersion, key, DownloadCancelRequestHash(acquisitionID, expectedVersion)).Scan(&raw)
	if err != nil {
		return DownloadCancelResult{}, mapRetirementDBError(fmt.Errorf("cancel acquisition download: %w", err))
	}
	var answer downloadCancelAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return DownloadCancelResult{}, fmt.Errorf("decode acquisition download cancel answer: %w", err)
	}
	switch answer.Status {
	case "accepted":
		if !answer.Replayed && s.eventHub != nil {
			s.eventHub.Publish(events.Event{Target: events.TargetRecords})
			s.eventHub.Publish(events.Event{Target: events.TargetRSS})
		}
		return DownloadCancelResult{AcquisitionID: acquisitionID, Version: answer.Version, Replayed: answer.Replayed}, nil
	case "not_found":
		return DownloadCancelResult{}, &apperrors.NotFound{Resource: "acquisition", ID: acquisitionID}
	case "key_reused":
		return DownloadCancelResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different request."}
	case "version_conflict":
		return DownloadCancelResult{}, &apperrors.VersionConflict{Owner: "acquisition", CurrentVersion: answer.Version}
	case "refused":
		return DownloadCancelResult{}, downloadCancelRefusal(answer.Code)
	default:
		return DownloadCancelResult{}, fmt.Errorf("unexpected acquisition download cancel status %q", answer.Status)
	}
}

func downloadCancelRefusal(code string) error {
	switch code {
	case "DOWNLOAD_CANCEL_ALREADY_REQUESTED":
		return &apperrors.Business{Status: 409, Code: code, Message: "The download was already cancelled."}
	case "MANAGED_ANIME_RETIRED":
		return managedAnimeRetiredError()
	case "DOWNLOAD_CANCEL_NOT_FOUND":
		return &apperrors.NotFound{Resource: "acquisition"}
	default:
		return &apperrors.Business{Status: 409, Code: "DOWNLOAD_CANCEL_NOT_APPLICABLE", Message: "The download has completed or the record has ended."}
	}
}
