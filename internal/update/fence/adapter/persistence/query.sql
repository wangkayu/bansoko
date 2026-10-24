-- Embedded update fence persistence is owned by this adapter; the migration chain is authoritative.

-- name: ReadSequenceFloor :one
SELECT sequence AS floor
FROM embedded_update_locks
ORDER BY sequence DESC
LIMIT 1;

-- name: InsertActiveFence :exec
INSERT INTO embedded_update_locks (op_id, admitted_by, sequence, manifest_digest, state)
VALUES (sqlc.arg(operation_id), sqlc.arg(admitted_by), sqlc.arg(sequence), sqlc.arg(manifest_digest), 'active');

-- name: ListOpenFencesByOwner :many
SELECT op_id, admitted_by, sequence, manifest_digest, state
FROM embedded_update_locks
WHERE admitted_by = sqlc.arg(admitted_by)
  AND released_at IS NULL
  AND state = 'active'
ORDER BY created_at, op_id;

-- name: ListAllOpenFences :many
SELECT op_id, admitted_by, sequence, manifest_digest, state
FROM embedded_update_locks
WHERE released_at IS NULL
  AND state = 'active'
ORDER BY created_at, op_id;

-- name: CloseFenceExact :execresult
-- The release sequence is part of the compare-and-swap: a row whose sequence
-- floor does not match the journal record it is being closed for is never
-- closed, no matter how well the other identity fields line up.
UPDATE embedded_update_locks
SET state = sqlc.arg(storage_state), released_at = now()
WHERE op_id = sqlc.arg(operation_id)
  AND admitted_by = sqlc.arg(admitted_by)
  AND manifest_digest = sqlc.arg(manifest_digest)
  AND sequence = sqlc.arg(expected_sequence)
  AND released_at IS NULL
  AND state = sqlc.arg(expected_state);

-- name: CloseFenceOrphanExact :execresult
-- The operator-only orphan recovery path: it has no journal record to bind a
-- release sequence to, so its exactness comes from the identity fields and the
-- explicit operator evidence recorded on the closed row with it.
UPDATE embedded_update_locks
SET state = sqlc.arg(storage_state), released_at = now(),
    recovered_at = now(),
    recovery_terminal_state = sqlc.arg(terminal_state),
    recovery_evidence = sqlc.arg(evidence)
WHERE op_id = sqlc.arg(operation_id)
  AND admitted_by = sqlc.arg(admitted_by)
  AND manifest_digest = sqlc.arg(manifest_digest)
  AND released_at IS NULL
  AND state = sqlc.arg(expected_state);

-- name: GetFenceIdentity :one
SELECT admitted_by, sequence, manifest_digest, state, released_at
FROM embedded_update_locks
WHERE op_id = sqlc.arg(operation_id);
