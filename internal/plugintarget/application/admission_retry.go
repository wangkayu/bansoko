package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	plugintargetport "bansoko/internal/plugintarget/port"
)

// Admission retry policy. A failing admission is retried with exponential
// backoff; after DefaultAdmissionMaxAttempts counted failures it becomes
// admission_failed. Transient database errors back off without counting, so an
// outage never exhausts the budget.
const (
	DefaultAdmissionMaxAttempts = 8
	admissionBackoffBase        = 5 * time.Second
	admissionBackoffCap         = 30 * time.Minute

	codeAdmissionRetry          = "notification_admission_retry"
	codeAdmissionTargetFailed   = "notification_target_failed"
	codeAdmissionTransient      = "notification_admission_transient"
	codeAdmissionRetryExhausted = "notification_admission_retry_exhausted"
	codeTargetConfigInvalid     = "notification_target_config_invalid"
	codePayloadConflict         = "notification_payload_conflict"

	reasonAdmissionTargetFailed = "one or more notification targets failed admission"
)

var errAdmissionNotClaimable = errors.New("admission is locked or already settled")

// DefaultAdmissionBackoff doubles from 5s and caps at 30 minutes.
func DefaultAdmissionBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := admissionBackoffBase
	for i := 1; i < attempt && delay < admissionBackoffCap; i++ {
		delay *= 2
	}
	if delay > admissionBackoffCap {
		delay = admissionBackoffCap
	}
	return delay
}

func (r *AdmissionResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *AdmissionResolver) backoff(attempt int) time.Duration {
	if r.Backoff != nil {
		return r.Backoff(attempt)
	}
	return DefaultAdmissionBackoff(attempt)
}

func (r *AdmissionResolver) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return DefaultAdmissionMaxAttempts
}

// isTransientError reports connection loss, serialization/deadlock, resource
// exhaustion and operator intervention: failures a later attempt can clear.
func isTransientError(err error) bool {
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && len(pg.Code) == 5 {
		switch pg.Code[:2] {
		case "08", "40", "53", "57":
			return true
		}
		return pg.Code == "55P03"
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

type targetFailure struct {
	code, reason string
	// delivery records a failed delivery so the outcome shows in the history.
	delivery bool
}

// deterministicTargetFailure classifies errors that retrying cannot change.
func deterministicTargetFailure(err error) (targetFailure, bool) {
	var validation *apperrors.Validation
	if errors.As(err, &validation) && validation.Code == "PLUGIN_TARGET_CONFIG_INVALID" {
		return targetFailure{code: codeTargetConfigInvalid, reason: "the target adapter config is not canonical; re-save the target", delivery: true}, true
	}
	var business *apperrors.Business
	if errors.As(err, &business) && business.Status == http.StatusConflict && business.Code == "DELIVERY_PAYLOAD_CONFLICT" {
		return targetFailure{code: codePayloadConflict, reason: "the delivery identity was already admitted with a different payload"}, true
	}
	return targetFailure{}, false
}

// admissionWriter is the write surface the admission settlement helpers need, so
// the same UPDATE serves the sweep (on the pool) and a caller's transaction.
type admissionWriter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// settleAdmissionFailed closes an admission that cannot be resolved. The state
// guard makes it idempotent: a concurrent sweep that already settled the row
// changes nothing.
func settleAdmissionFailed(ctx context.Context, writer admissionWriter, admissionID, code, reason string) error {
	_, err := writer.ExecContext(ctx, `
UPDATE notification_admissions
SET state = 'admission_failed', failure_code = $2, failure_reason = $3
WHERE id = $1 AND state = $4`, admissionID, code, reason, AdmissionPendingTargetResolution)
	return err
}

// failTargetWithEvidence records one target's deterministic failure as a failed
// delivery, so the outcome is visible in the delivery history. The insert is
// best effort: it runs under a savepoint so a bad snapshot cannot block it.
func (r *AdmissionResolver) failTargetWithEvidence(ctx context.Context, admissionID string, target targetRecord, entry LedgerEntry, failure targetFailure) error {
	tx, err := r.Service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if failure.delivery {
		r.Service.recordFailedDeliveryBestEffortTx(ctx, tx, target, StableKey(target.ID, target.Version, entry), entry, failure.code, failure.reason)
	}
	if err := settleAdmissionFailed(ctx, tx, admissionID, codeAdmissionTargetFailed, reasonAdmissionTargetFailed); err != nil {
		return err
	}
	return tx.Commit()
}

// recordAttemptFailure counts a failed resolution attempt and schedules the
// next one, or settles the admission once the budget is spent.
func (r *AdmissionResolver) recordAttemptFailure(ctx context.Context, admissionID string, attempts int, cause error) {
	now := r.now()
	if isTransientError(cause) {
		r.updateRetry(ctx, admissionID, attempts, now.Add(r.backoff(attempts+1)), codeAdmissionTransient)
		return
	}
	attempts++
	if attempts >= r.maxAttempts() {
		if err := r.exhaustAdmission(ctx, admissionID, attempts); err != nil {
			r.logError(ctx, observabilityport.RuntimeEvent{
				Message: "admission retry exhaustion could not be recorded", Operation: "exhaust",
				ReasonCode: codeAdmissionRetryExhausted, OwnerType: "notification_admission",
				OwnerID: admissionID, ErrorClass: "admission",
			})
		}
		return
	}
	r.updateRetry(ctx, admissionID, attempts, now.Add(r.backoff(attempts)), codeAdmissionRetry)
}

func (r *AdmissionResolver) updateRetry(ctx context.Context, admissionID string, attempts int, next time.Time, code string) {
	if _, err := r.Service.db.ExecContext(ctx, `
UPDATE notification_admissions
SET attempt_count = $3, next_attempt_at = $4, last_error_code = $5
WHERE id = $1 AND state = $2`, admissionID, AdmissionPendingTargetResolution, attempts, next, code); err != nil {
		r.logWarn(ctx, observabilityport.RuntimeEvent{
			Message: "admission retry could not be scheduled", Operation: "schedule_retry",
			ReasonCode: code, OwnerType: "notification_admission", OwnerID: admissionID, ErrorClass: "admission",
		})
	}
}

// exhaustAdmission moves an admission whose retries are spent to
// admission_failed. Every target that is still eligible gets a failed delivery,
// so the abandoned outcome stays visible in the delivery history.
func (r *AdmissionResolver) exhaustAdmission(ctx context.Context, admissionID string, attempts int) error {
	tx, err := r.Service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var entry LedgerEntry
	var anime sql.NullString
	var payload []byte
	err = tx.QueryRowContext(ctx, `
SELECT trigger_kind, source_owner_type, source_owner_id, source_owner_version, result_key, managed_anime_id, payload
FROM notification_admissions
WHERE id = $1 AND state = $2
FOR UPDATE SKIP LOCKED`, admissionID, AdmissionPendingTargetResolution).Scan(&entry.TriggerKind, &entry.SourceOwnerType, &entry.SourceOwnerID, &entry.SourceOwnerVersion, &entry.ResultKey, &anime, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	entry.ManagedAnimeID = anime.String
	entry.Payload = json.RawMessage(payload)
	const reason = "notification admission failed repeatedly and was abandoned"
	targets, err := enumerateEligibleTargets(ctx, tx, entry.TriggerKind)
	if err != nil {
		return err
	}
	for _, targetID := range targets {
		target, err := r.Service.lockTargetTx(ctx, tx, targetID)
		if err != nil {
			// A target deleted or unreadable right now carries no evidence.
			continue
		}
		r.Service.recordFailedDeliveryBestEffortTx(ctx, tx, target, StableKey(target.ID, target.Version, entry), entry, codeAdmissionRetryExhausted, reason)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE notification_admissions
SET state = 'admission_failed', failure_code = $2, failure_reason = $3,
    attempt_count = $4, last_error_code = $2, next_attempt_at = NULL
WHERE id = $1`, admissionID, codeAdmissionRetryExhausted, reason, attempts); err != nil {
		return err
	}
	return tx.Commit()
}

// recordFailedDeliveryBestEffortTx inserts a failed delivery for one target
// under a savepoint; any failure (a payload that cannot be frozen) rolls back to
// the savepoint and leaves the transaction usable, because the admission-level
// failure evidence is still written.
func (s *Service) recordFailedDeliveryBestEffortTx(ctx context.Context, tx *sql.Tx, target targetRecord, stableKey string, entry LedgerEntry, code, reason string) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT failed_delivery`); err != nil {
		return
	}
	insert := func() error {
		request := plugintargetport.BusinessDeliveryRequest{SourceOwnerType: entry.SourceOwnerType, SourceOwnerID: entry.SourceOwnerID, SourceOwnerVersion: entry.SourceOwnerVersion, ManagedAnimeID: entry.ManagedAnimeID, TriggerKind: entry.TriggerKind, ResultKey: entry.ResultKey, Payload: entry.Payload}
		return s.insertFailedDeliveryTx(ctx, tx, target, request, stableKey, code, reason)
	}
	if err := insert(); err != nil {
		_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT failed_delivery`)
		return
	}
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT failed_delivery`)
}
