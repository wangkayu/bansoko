-- Cleanup-owner persistence is owned by this adapter; the migration chain is authoritative.

-- name: LockCleanupWorkItem :one
SELECT id::text AS id,owner_type::text AS owner_type,owner_id::text AS owner_id,kind::text AS kind,state::text AS state,expected_owner_version::bigint AS expected_owner_version FROM public.work_item_lock_maintenance_cleanup(sqlc.arg(cleanup_id)::text,sqlc.arg(kind)::text);


-- name: InsertCleanupOperation :exec
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 retention_mode, source_path, source_fingerprint, state, failure_code, reason,
 correlation_id, causation_type, causation_id, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(idempotency_key), sqlc.arg(placement_id), sqlc.arg(managed_anime_id),
        sqlc.arg(coverage), sqlc.arg(replacement_ref), sqlc.arg(retention_mode),
        sqlc.arg(source_path), sqlc.arg(source_fingerprint), sqlc.arg(state),
        sqlc.narg(failure_code), sqlc.narg(reason), sqlc.narg(correlation_id), sqlc.narg(causation_type),
        sqlc.narg(causation_id), 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: LockCleanupOwnerVersion :one
SELECT version
FROM cleanup_operations
WHERE id = sqlc.arg(cleanup_id)
FOR UPDATE;

-- name: LockCleanupOperation :one
SELECT id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
       retention_mode, source_path, source_fingerprint, state, attempt, failure_code, reason,
       correlation_id, version, created_at, updated_at
FROM cleanup_operations
WHERE id = sqlc.arg(cleanup_id)
FOR UPDATE;

-- name: PersistCleanupAttempt :one
UPDATE cleanup_operations
SET attempt = sqlc.arg(attempt), version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(cleanup_id) AND version = sqlc.arg(expected_version)
RETURNING version;

-- name: TransitionCleanup :one
UPDATE cleanup_operations
SET state = sqlc.arg(state), failure_code = NULLIF(sqlc.arg(failure_code), ''),
    reason = NULLIF(sqlc.arg(reason), ''), version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(cleanup_id) AND version = sqlc.arg(expected_version)
RETURNING version;

-- name: GetCleanupOperation :one
SELECT id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
       retention_mode, source_path, source_fingerprint, state, attempt, failure_code, reason,
       correlation_id, version, created_at, updated_at
FROM cleanup_operations
WHERE id = sqlc.arg(cleanup_id);

-- name: ListPendingCleanupOperations :many
SELECT id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
       retention_mode, source_path, source_fingerprint, state, attempt, failure_code, reason,
       correlation_id, version, created_at, updated_at
FROM cleanup_operations
WHERE state = 'admitted' OR (state = 'blocked' AND attempt IS NOT NULL)
ORDER BY id;

-- name: ListCleanupOperations :many
SELECT id, placement_id, managed_anime_id, replacement_ref, retention_mode,
       source_path, source_fingerprint,
       state, COALESCE(failure_code, ''), COALESCE(reason, ''), version, created_at, updated_at
FROM cleanup_operations
WHERE managed_anime_id = sqlc.arg(anime_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: CleanupPathOwned :one
-- A deletion never removes a path something else now owns: an active
-- placement or an active companion recorded at that exact path.
SELECT EXISTS (SELECT 1 FROM placements p WHERE p.physical_path = sqlc.arg(path)::text AND p.state = 'active')
    OR EXISTS (SELECT 1 FROM placement_companions c WHERE c.current_path = sqlc.arg(path)::text AND c.state = 'active');

-- name: SubscriptionIDForAnime :one
SELECT id
FROM subscriptions
WHERE managed_anime_id = sqlc.arg(anime_id);
