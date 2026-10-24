package application

import (
	animeapplication "bansoko/internal/anime/application"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
)

// CleanupAnimeLocalData deletes the complete local business/config/runtime
// owner graph of one RETIRED ManagedAnime. It is the product command behind
// DELETE /anime/{managed_anime_id}/local-data and is deliberately separate
// from retirement: retirement keeps the graph, cleanup removes it later under
// its own explicit authorization.
//
// It never deletes qBittorrent torrents/data or media-library files. It only
// removes local rows, so every external effect is left exactly as it is.
//
// The whole destructive operation lives in the owner-held SECURITY DEFINER
// function `cleanup_managed_anime_local_data`. The runtime can only EXECUTE it:
// the transaction-local cleanup flag, the idempotency replay record and the
// leaf-first graph delete all happen under the schema-owner role, so this
// application role cannot authorize or delete the graph directly, and the
// statement order is fixed by the database rather than by the caller.
func (s *Service) CleanupAnimeLocalData(ctx context.Context, managedAnimeID string, input LocalDataCleanupInput, idempotencyKey string) error {
	if s == nil || s.db == nil {
		return errors.New("anime service database is nil")
	}
	managedAnimeID = strings.TrimSpace(managedAnimeID)
	if managedAnimeID == "" {
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if len(strings.TrimSpace(idempotencyKey)) < 8 || len(idempotencyKey) > 128 {
		return &apperrors.Business{Status: 422, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "A bounded request idempotency key is required."}
	}
	if input.Confirmation != LocalDataCleanupConfirmation {
		return &apperrors.Business{Status: 422, Code: "LOCAL_CLEANUP_CONFIRMATION_REQUIRED", Message: "The request must explicitly confirm local record cleanup."}
	}
	if input.ExpectedLifecycleEpoch < 1 {
		return &apperrors.Business{Status: 422, Code: "LOCAL_CLEANUP_EPOCH_REQUIRED", Message: "The expected retired lifecycle epoch is required."}
	}
	requestHash, err := localCleanupRequestHash(managedAnimeID, input.ExpectedLifecycleEpoch, input.Confirmation)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local cleanup: %w", err)
	}
	defer tx.Rollback()

	// One owner-held call performs the replay check, the retired/epoch CAS, the
	// replay-record write and the complete leaf-first delete atomically.
	var result json.RawMessage
	if err := tx.QueryRowContext(ctx, `
SELECT cleanup_managed_anime_local_data($1, $2, $3, $4)`,
		managedAnimeID, idempotencyKey, requestHash, input.ExpectedLifecycleEpoch).Scan(&result); err != nil {
		return mapLocalCleanupDBError(err, managedAnimeID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit local cleanup: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog, events.TargetRSS)
	return nil
}

func localCleanupIdempotencyConflict() error {
	return &apperrors.Business{
		Status:  409,
		Code:    "IDEMPOTENCY_KEY_REUSED",
		Message: "The idempotency key was already used with a different local cleanup request.",
	}
}

// localCleanupRequestHash is the canonical request identity stored on the
// replay record. Same key + same hash is an idempotent replay; same key and a
// hash fails closed.
func localCleanupRequestHash(managedAnimeID string, epoch int64, confirmation string) (string, error) {
	payload, err := json.Marshal(struct {
		Operation string `json:"operation"`
		OwnerID   string `json:"managed_anime_id"`
		Epoch     int64  `json:"expected_lifecycle_epoch"`
		Confirm   string `json:"confirmation"`
	}{
		Operation: "local_data_cleanup",
		OwnerID:   managedAnimeID,
		Epoch:     epoch,
		Confirm:   confirmation,
	})
	if err != nil {
		return "", fmt.Errorf("encode local cleanup request hash: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// mapLocalCleanupDBError converts the cleanup function's typed rejections into
// the public conditions. A tombstoned id and any fence rejection keep the
// retirement boundary's stable codes; the domain-specific rejections keep
// theirs.
func mapLocalCleanupDBError(err error, managedAnimeID string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return err
	}
	switch pgErr.Message {
	case "IDEMPOTENCY_KEY_REUSED":
		return localCleanupIdempotencyConflict()
	case "LOCAL_CLEANUP_NOT_FOUND", "LOCAL_CLEANUP_OWNER_VANISHED":
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	case "LOCAL_CLEANUP_ACQUISITION_RETAINED":
		return &apperrors.Business{Status: 409, Code: "LOCAL_CLEANUP_ACQUISITION_RETAINED", Message: "A download or file step of this instance is still finishing; local records can be cleaned up once it ends."}
	case "LOCAL_CLEANUP_NOT_RETIRED":
		return &apperrors.Business{
			Status:  409,
			Code:    "LOCAL_CLEANUP_NOT_RETIRED",
			Message: "Only a retired instance has local records that can be cleaned up.",
		}
	case "LOCAL_CLEANUP_STALE_EPOCH":
		return &apperrors.Business{
			Status:  409,
			Code:    "LOCAL_CLEANUP_STALE_EPOCH",
			Message: "The retired lifecycle epoch changed; refresh the instance before cleaning up local records.",
		}
	default:
		return animeapplication.MapRetirementDBError(err)
	}
}
