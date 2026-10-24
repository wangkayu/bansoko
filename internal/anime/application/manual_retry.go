package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// ManualRetryService adapts the owner-specific recovery endpoints to the
// WorkItem runtime. It resolves the concrete owner version inside the manual
// retry transaction and maps runtime failures onto typed HTTP conditions.
type ManualRetryService struct {
	db        *sql.DB
	workItems *workitems.Store
	eventHub  *events.Hub
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (m *ManualRetryService) SetEventHub(hub *events.Hub) {
	if m != nil {
		m.eventHub = hub
	}
}

func NewManualRetryService(db *sql.DB, workItems *workitems.Store) (*ManualRetryService, error) {
	if db == nil || workItems == nil {
		return nil, errors.New("manual retry service requires a database and a work item store")
	}
	return &ManualRetryService{db: db, workItems: workItems}, nil
}

func (m *ManualRetryService) ManualRetry(ctx context.Context, ownerType, ownerID, failedWorkItemID, reason, requestIdempotencyKey string) error {
	return m.manualRetry(ctx, ownerType, ownerID, failedWorkItemID, reason, requestIdempotencyKey)
}

func (m *ManualRetryService) manualRetry(ctx context.Context, ownerType, ownerID, failedWorkItemID, reason, requestIdempotencyKey string) error {
	var managedAnimeID, lifecycleState sql.NullString
	if err := m.db.QueryRowContext(ctx, `
SELECT item.managed_anime_id, lifecycle.lifecycle_state
FROM work_item_history item
LEFT JOIN managed_anime lifecycle ON lifecycle.id = item.managed_anime_id
WHERE item.id = $1 AND item.owner_type = $2 AND item.owner_id = $3`, failedWorkItemID, ownerType, ownerID).Scan(&managedAnimeID, &lifecycleState); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read manual retry owner lifecycle: %w", err)
	}
	if lifecycleState.Valid && lifecycleState.String == "retired" {
		return managedAnimeRetiredError()
	}
	if ownerType == workitems.OwnerDownloadTask || ownerType == workitems.OwnerArtifact {
		var acquisitionID sql.NullString
		err := m.db.QueryRowContext(ctx, `SELECT acquisition_id FROM work_item_history WHERE id=$1 AND owner_type=$2 AND owner_id=$3`, failedWorkItemID, ownerType, ownerID).Scan(&acquisitionID)
		if errors.Is(err, sql.ErrNoRows) {
			return &apperrors.NotFound{Resource: "work_item", ID: failedWorkItemID}
		}
		if err != nil {
			return err
		}
		if acquisitionID.Valid {
			return &apperrors.Business{Status: 409, Code: "MANUAL_RETRY_STAGE_RETIRED", Message: "Start a new acquisition with a new request key."}
		}
	}
	// The retry transaction must take the lifecycle row lock before the failed
	// WorkItem/owner rows so it shares retirement's lock order (no 40P01).
	var preflight workitems.OwnerPreflight
	if managedAnimeID.Valid && managedAnimeID.String != "" {
		lockedAnimeID := managedAnimeID.String
		preflight = func(ctx context.Context, scope txscope.Tx) error {
			return lockActiveManagedAnimeLifecycleTx(ctx, scope.SQLTx(), lockedAnimeID)
		}
	}
	reader, err := m.ownerVersionReader(ownerType, ownerID)
	if err != nil {
		return err
	}
	var resetter workitems.OwnerResetter
	_, err = m.workItems.ManualRetryWithOwnerResetPreflight(ctx, workitems.OwnerRef{Type: ownerType, ID: ownerID}, failedWorkItemID, reason, requestIdempotencyKey, reader, resetter, preflight, nil)
	switch {
	case err == nil:
		if m.eventHub != nil {
			m.eventHub.Publish(events.Event{Target: events.TargetRSS})
		}
		return nil
	case errors.Is(err, workitems.ErrNotFound):
		return &apperrors.NotFound{Resource: "work_item", ID: failedWorkItemID}
	case errors.Is(err, workitems.ErrLiveItemConflict):
		return &apperrors.Business{Status: 409, Code: "WORK_ITEM_LIVE_CONFLICT", Message: "A live recovery work item already exists for this owner."}
	case errors.Is(err, workitems.ErrIdempotencyConflict):
		return &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different request."}
	case errors.Is(err, workitems.ErrManualRetryContention):
		// Entry-specific exhaustion surface: zero commit, zero business
		// write, and never the retirement reason code.
		return &apperrors.Business{Status: 409, Code: "MANUAL_RETRY_BUSY", Message: "The fixed lock set is contended; retry the request."}
	case errors.Is(err, workitems.ErrInvalidSpec):
		return &apperrors.Business{Status: 422, Code: "MANUAL_RETRY_INVALID", Message: "The manual retry request is invalid."}
	default:
		return mapRetirementDBError(err)
	}
}

// ownerVersionReaders maps every owner type the manual-retry entry admits to the
// reader that locks its row and returns its version. It is a package-level map
// rather than a switch so a conformance test can compare it against
// workitems.ManualRetryOwnerTypes: a type admitted by the store but missing here
// would answer OWNER_TYPE_INVALID to every caller, and a type here that the
// store does not admit would only fail inside the retry transaction.
var ownerVersionReaders = map[string]func(*ManualRetryService) func(context.Context, txscope.Tx, string) (int64, error){
	workitems.OwnerRSSSourceRuntime: func(m *ManualRetryService) func(context.Context, txscope.Tx, string) (int64, error) {
		return m.workItems.LockRSSSourceRuntimeVersionTx
	},
	workitems.OwnerDownloadTask: func(m *ManualRetryService) func(context.Context, txscope.Tx, string) (int64, error) {
		return m.workItems.LockDownloadTaskVersionTx
	},
	workitems.OwnerArtifact: func(m *ManualRetryService) func(context.Context, txscope.Tx, string) (int64, error) {
		return m.workItems.LockArtifactVersionTx
	},
}

// A missing owner is a typed not-found, never a silent version.
func (m *ManualRetryService) ownerVersionReader(ownerType, ownerID string) (workitems.OwnerReader, error) {
	resolve, ok := ownerVersionReaders[ownerType]
	if !ok {
		return nil, &apperrors.Business{Status: 422, Code: "OWNER_TYPE_INVALID", Message: "The owner type does not accept manual retry."}
	}
	readVersion := resolve(m)
	if ownerID == "" {
		return nil, &apperrors.BadRequest{Code: "OWNER_ID_REQUIRED", Message: "The owner id is required."}
	}
	return func(ctx context.Context, scope txscope.Tx) (int64, error) {
		version, err := readVersion(ctx, scope, ownerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, &apperrors.NotFound{Resource: ownerType, ID: ownerID}
			}
			return 0, fmt.Errorf("read owner version: %w", err)
		}
		return version, nil
	}, nil
}
