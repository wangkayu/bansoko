package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// AdmissionResolver consumes the NotificationAdmission ledger exclusively. It
// never scans DownloadTask/Artifact/FileOperation to guess eligibility, and it
// resolves every admission against the target configuration that is current at
// resolution time: a target deleted before the sweep delivers nothing, a target
// edited before the sweep delivers its new configuration. Duplicate sweeps,
// concurrent resolvers, restarts and reclaims converge idempotently because the
// delivery identity and its idempotency key are derived from the admission and
// the target, never from wall-clock state.
type AdmissionResolver struct {
	Service *Service
	Logger  observabilityport.RuntimeLogger
	// SweepInterval is the ledger polling period.
	SweepInterval time.Duration
	// Now, Backoff and MaxAttempts bound retries of a failing admission; zero
	// values select the production defaults (see admission_retry.go).
	Now         func() time.Time
	Backoff     func(attempt int) time.Duration
	MaxAttempts int
}

// Run blocks until ctx is cancelled, sweeping the ledger periodically.
func (r *AdmissionResolver) Run(ctx context.Context) {
	interval := r.SweepInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.SweepOnce(ctx); err != nil && ctx.Err() == nil {
				r.logError(ctx, observabilityport.RuntimeEvent{
					Message:    "admission ledger sweep failed",
					Operation:  "sweep",
					ReasonCode: "sweep_failed",
					ErrorClass: "admission",
				})
			}
		}
	}
}

// logError emits a structured failure event. Raw error text never enters the
// message; the stable failure category lives in ReasonCode/ErrorClass.
func (r *AdmissionResolver) logError(ctx context.Context, event observabilityport.RuntimeEvent) {
	if r == nil || r.Logger == nil {
		return
	}
	event.Component = "plugin_admission"
	event.Outcome = "failure"
	r.Logger.Error(ctx, event)
}

// logWarn emits a structured warning event for recoverable per-item failures.
func (r *AdmissionResolver) logWarn(ctx context.Context, event observabilityport.RuntimeEvent) {
	if r == nil || r.Logger == nil {
		return
	}
	event.Component = "plugin_admission"
	event.Outcome = "failure"
	r.Logger.Warn(ctx, event)
}

// SweepOnce resolves a batch of pending admissions into deliveries and returns
// the number of deliveries created. Rows that failed wait for next_attempt_at;
// never-tried rows sort first so a backlog of failing admissions cannot starve
// new ones.
func (r *AdmissionResolver) SweepOnce(ctx context.Context) (int, error) {
	created := 0

	rows, err := r.Service.db.QueryContext(ctx, `
SELECT id, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
       result_key, managed_anime_id, occurred_at, event_catalog_version,
       event_template_catalog_version, payload, attempt_count
FROM notification_admissions
WHERE state = 'pending_target_resolution'
  AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
ORDER BY next_attempt_at NULLS FIRST, occurred_at, id
LIMIT 20`, r.now())
	if err != nil {
		return 0, fmt.Errorf("query pending admissions: %w", err)
	}
	type pendingAdmission struct {
		id       string
		entry    LedgerEntry
		attempts int
	}
	var pending []pendingAdmission
	for rows.Next() {
		var pa pendingAdmission
		var managedAnimeID sql.NullString
		var occurredAt time.Time
		var payload []byte
		if err := rows.Scan(&pa.id, &pa.entry.TriggerKind, &pa.entry.SourceOwnerType, &pa.entry.SourceOwnerID,
			&pa.entry.SourceOwnerVersion, &pa.entry.ResultKey, &managedAnimeID, &occurredAt,
			&pa.entry.EventCatalogVersion, &pa.entry.EventTemplateCatalogVersion, &payload, &pa.attempts); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan pending admission: %w", err)
		}
		pa.entry.ManagedAnimeID = managedAnimeID.String
		pa.entry.OccurredAt = occurredAt
		pa.entry.Payload = json.RawMessage(payload)
		pending = append(pending, pa)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate pending admissions: %w", err)
	}
	rows.Close()

	for _, pa := range pending {
		if err := r.claimAdmission(ctx, pa.id, pa.entry); err != nil {
			if errors.Is(err, errAdmissionNotClaimable) {
				continue
			}
			r.logWarn(ctx, observabilityport.RuntimeEvent{
				Message: "admission claim failed", Operation: "claim",
				ReasonCode: "admission_claim_failed", OwnerType: "notification_admission",
				OwnerID: pa.id, ErrorClass: "admission",
			})
			r.recordAttemptFailure(ctx, pa.id, pa.attempts, err)
			continue
		}
		n, err := r.createCurrentDeliveries(ctx, pa.id, pa.entry)
		created += n
		if err != nil {
			r.logWarn(ctx, observabilityport.RuntimeEvent{
				Message: "admission delivery creation failed", Operation: "create_delivery",
				ReasonCode: "delivery_create_failed", OwnerType: "notification_admission",
				OwnerID: pa.id, ErrorClass: "admission",
			})
			r.recordAttemptFailure(ctx, pa.id, pa.attempts, err)
			continue
		}
	}
	return created, nil
}

// claimAdmission takes the ledger row for this sweep. A row locked by a
// concurrent resolver is not a failure; an admission written under a retired
// catalog boundary fails closed instead of being resolved with semantics this
// build no longer implements.
func (r *AdmissionResolver) claimAdmission(ctx context.Context, admissionID string, entry LedgerEntry) error {
	tx, err := r.Service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var claimed string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM notification_admissions WHERE id = $1 AND state = $2 FOR UPDATE SKIP LOCKED`, admissionID, AdmissionPendingTargetResolution).Scan(&claimed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Locked by a concurrent resolver or already settled: not a failure.
			return errAdmissionNotClaimable
		}
		return err
	}
	if entry.EventCatalogVersion != EventCatalogVersion || entry.EventTemplateCatalogVersion != TemplateCatalogVersion {
		return r.failAdmissionTx(ctx, tx, admissionID, "notification_catalog_invalid", "notification catalog or template version is unsupported")
	}
	return tx.Commit()
}

// failAdmissionTx settles one admission inside the caller's transaction.
func (r *AdmissionResolver) failAdmissionTx(ctx context.Context, tx *sql.Tx, admissionID, code, reason string) error {
	if err := settleAdmissionFailed(ctx, tx, admissionID, code, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// failAdmission settles one admission outside any caller transaction.
func (r *AdmissionResolver) failAdmission(ctx context.Context, admissionID, code, reason string) error {
	return settleAdmissionFailed(ctx, r.Service.db, admissionID, code, reason)
}

// enumerateCurrentTargets lists the targets that are eligible for one trigger
// right now: enabled, not deleted, and subscribed to the trigger.
func (r *AdmissionResolver) enumerateCurrentTargets(ctx context.Context, triggerKind string) ([]string, error) {
	return enumerateEligibleTargets(ctx, r.Service.db, triggerKind)
}

// targetLister is the read surface enumerateEligibleTargets needs, so the same
// eligibility query serves the sweep and the retry-exhaustion path.
type targetLister interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// enumerateEligibleTargets lists the live targets subscribed to one trigger. The
// list is the resolution-time snapshot; each target is re-read under a row lock
// when its delivery is created, so a concurrent edit is either fully seen or not
// at all.
func enumerateEligibleTargets(ctx context.Context, querier targetLister, triggerKind string) ([]string, error) {
	rows, err := querier.QueryContext(ctx, `
SELECT id FROM plugin_target_configs
WHERE enabled = TRUE AND deleted_at IS NULL
  AND trigger_kinds @> jsonb_build_array($1::text)
ORDER BY id`, triggerKind)
	if err != nil {
		return nil, fmt.Errorf("enumerate current targets: %w", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var targetID string
		if err := rows.Scan(&targetID); err != nil {
			return nil, err
		}
		targets = append(targets, targetID)
	}
	return targets, rows.Err()
}

// createCurrentDeliveries resolves one claimed admission against the current
// target configuration. Every target gets its own transaction, so one target's
// deterministic failure never rolls back its siblings; the admission is settled
// once every target has been attempted.
func (r *AdmissionResolver) createCurrentDeliveries(ctx context.Context, admissionID string, entry LedgerEntry) (int, error) {
	targetIDs, err := r.enumerateCurrentTargets(ctx, entry.TriggerKind)
	if err != nil {
		return 0, err
	}
	if len(targetIDs) == 0 {
		return 0, r.failAdmission(ctx, admissionID, "zero_eligible_targets", "no notification target is currently eligible for this trigger")
	}
	request := plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType:    entry.SourceOwnerType,
		SourceOwnerID:      entry.SourceOwnerID,
		SourceOwnerVersion: entry.SourceOwnerVersion,
		ManagedAnimeID:     entry.ManagedAnimeID,
		TriggerKind:        entry.TriggerKind,
		ResultKey:          entry.ResultKey,
		Payload:            withEventFacts(entry.Payload, entry.TriggerKind, entry.OccurredAt),
	}
	created, failed := 0, 0
	for _, targetID := range targetIDs {
		tx, err := r.Service.db.BeginTx(ctx, nil)
		if err != nil {
			return created, fmt.Errorf("begin delivery creation: %w", err)
		}
		target, err := r.Service.lockTargetTx(ctx, tx, targetID)
		if err != nil {
			_ = tx.Rollback()
			var missing *apperrors.NotFound
			if errors.As(err, &missing) {
				// Deleted between enumeration and resolution: it delivers
				// nothing, and its siblings still deliver.
				continue
			}
			return created, fmt.Errorf("read notification target: %w", err)
		}
		err = r.Service.enqueueForTargetWithKey(ctx, tx, target, request, StableKey(target.ID, target.Version, entry))
		if err != nil {
			_ = tx.Rollback()
			if failure, ok := deterministicTargetFailure(err); ok {
				// Retrying cannot change the outcome: settle this target now
				// with its own evidence and keep going with its siblings.
				if failErr := r.failTargetWithEvidence(ctx, admissionID, target, entry, failure); failErr != nil {
					return created, failErr
				}
				failed++
				continue
			}
			return created, fmt.Errorf("create current delivery: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return created, err
		}
		created++
	}
	if err := r.settleAdmission(ctx, admissionID, failed); err != nil {
		return created, fmt.Errorf("settle notification admission: %w", err)
	}
	return created, nil
}

// settleAdmission closes a fully attempted admission: resolved when every
// target produced a delivery, admission_failed when at least one settled with
// its own evidence.
func (r *AdmissionResolver) settleAdmission(ctx context.Context, admissionID string, failed int) error {
	state, code, reason := AdmissionResolved, "", ""
	if failed > 0 {
		state = AdmissionFailed
		code, reason = codeAdmissionTargetFailed, reasonAdmissionTargetFailed
	}
	_, err := r.Service.db.ExecContext(ctx, `
UPDATE notification_admissions
SET state = $2, failure_code = NULLIF($3, ''), failure_reason = NULLIF($4, '')
WHERE id = $1 AND state = $5`, admissionID, state, code, reason, AdmissionPendingTargetResolution)
	return err
}

// withEventFacts fills event.type and event.at from the admission
// itself: every trigger carries them, and the ledger row is their single
// source, so producers need not repeat them.
func withEventFacts(payload json.RawMessage, trigger string, occurredAt time.Time) json.RawMessage {
	facts, ok := notification.DecodeFacts(payload)
	if !ok {
		return payload
	}
	if facts["event.type"] == nil {
		facts["event.type"] = trigger
	}
	if facts["event.at"] == nil && !occurredAt.IsZero() {
		facts["event.at"] = occurredAt.UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		return payload
	}
	return encoded
}
