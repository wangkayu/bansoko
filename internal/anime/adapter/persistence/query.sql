-- Anime-owned one-shot marker persistence surface.
-- db/migrations remains the only authoritative schema source.

-- name: InsertOneShotMarker :exec
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, input_snapshot, state,
    admission_epoch, active_effect_count, active_effect_leases, version, current_attempt_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: InsertTerminalOneShotMarker :exec
-- A marker whose chain is already terminal at admission: the exact target set
-- existed, so no DownloadTask/qB work/Artifact/FileOperation was created. The
-- retention deadline is computed from database time exactly like the drain
-- path, so reclamation bounds stay consistent across server instances.
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, input_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until,
    admission_epoch, active_effect_count, active_effect_leases, version, current_attempt_id
)
VALUES (
    sqlc.arg(id), sqlc.arg(managed_anime_id), sqlc.arg(subscription_id), sqlc.arg(rss_source_id),
    sqlc.arg(release_id), sqlc.arg(download_task_id), sqlc.arg(correlation_id),
    sqlc.arg(request_idempotency_key), sqlc.arg(request_hash), sqlc.arg(result_snapshot),
    sqlc.arg(input_snapshot), sqlc.arg(state), sqlc.arg(terminal_outcome),
    sqlc.arg(terminal_reason_code), sqlc.arg(terminal_reason), NOW(),
    NOW() + (sqlc.arg(retention_days)::int * INTERVAL '1 day'),
    sqlc.arg(admission_epoch), sqlc.arg(active_effect_count), sqlc.arg(active_effect_leases),
    sqlc.arg(version), sqlc.arg(current_attempt_id)
);

-- name: GetOneShotMarkerByRequestIdempotencyKey :one
SELECT
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, input_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until,
    admission_epoch, active_effect_count, active_effect_leases, version, created_at, updated_at,
    current_attempt_id
FROM one_shot_acquisitions
WHERE request_idempotency_key = $1;

-- name: LockOneShotMarkerByRequestIdempotencyKey :one
SELECT
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, input_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until,
    admission_epoch, active_effect_count, active_effect_leases, version, created_at, updated_at,
    current_attempt_id
FROM one_shot_acquisitions
WHERE request_idempotency_key = $1
FOR UPDATE;

-- name: GetOneShotMarkerByID :one
SELECT
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, input_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until,
    admission_epoch, active_effect_count, active_effect_leases, version, created_at, updated_at,
    current_attempt_id
FROM one_shot_acquisitions
WHERE id = $1;

-- name: LockOneShotMarkerStateByManagedAnimeID :one
SELECT state
FROM one_shot_acquisitions
WHERE managed_anime_id = $1
FOR UPDATE;

-- name: PurgeEligibleAnimeConfigCandidates :execresult
WITH due AS (
    SELECT candidate.id
    FROM anime_config_candidates AS candidate
    WHERE candidate.cleanup_eligible_at IS NOT NULL
      AND candidate.cleanup_eligible_at <= CURRENT_TIMESTAMP
      AND NOT EXISTS (
          SELECT 1 FROM anime_business_configs AS config
          WHERE config.candidate_id = candidate.id
      )
    ORDER BY candidate.cleanup_eligible_at, candidate.id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
DELETE FROM anime_config_candidates AS candidate
USING due
WHERE candidate.id = due.id
  AND candidate.cleanup_eligible_at IS NOT NULL
  AND candidate.cleanup_eligible_at <= CURRENT_TIMESTAMP;


-- One-shot execution attempts: immutable per-attempt terminal
-- evidence; the marker projection mirrors the current attempt.

-- name: InsertOneShotAttempt :exec
INSERT INTO one_shot_execution_attempts(
    id, acquisition_id, sequence, previous_attempt_id, mode, resume_stage,
    input_snapshot, input_digest, state, failed_stage, outcome_code, outcome_reason, terminal_at,
    request_idempotency_key, request_hash
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: GetOneShotAttemptByID :one
SELECT *
FROM one_shot_execution_attempts
WHERE id = $1;

-- name: TerminalizeOneShotAttempt :one
UPDATE one_shot_execution_attempts
SET state = sqlc.arg(state),
    failed_stage = sqlc.arg(failed_stage),
    outcome_code = sqlc.arg(outcome_code),
    outcome_reason = sqlc.arg(outcome_reason),
    terminal_at = sqlc.arg(terminal_at),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id) AND state = 'running' AND version = sqlc.arg(expected_version)
RETURNING version;

-- Execution attempt reads.

-- name: GetOneShotAttemptForAcquisition :one
SELECT *
FROM one_shot_execution_attempts
WHERE id = $1 AND acquisition_id = $2;

-- name: ListOneShotAttemptsByAcquisition :many
-- sequence DESC keyset page. A cursor sequence of 0 means "from the newest".
SELECT *
FROM one_shot_execution_attempts
WHERE acquisition_id = sqlc.arg(acquisition_id)
  AND (sqlc.arg(before_sequence)::bigint = 0 OR sequence < sqlc.arg(before_sequence)::bigint)
ORDER BY sequence DESC
LIMIT sqlc.arg(page_size);
