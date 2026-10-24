-- Naming Vars version persistence is owned by this adapter; the migration chain is authoritative.

-- name: LockNamingVarsPublish :exec
SELECT pg_advisory_xact_lock(hashtext('bansoko:naming-vars')::bigint);

-- name: GetActiveNamingVarsFingerprint :one
SELECT fingerprint
FROM naming_vars_versions
WHERE active
FOR UPDATE;

-- name: DeactivateNamingVars :exec
UPDATE naming_vars_versions SET active = FALSE WHERE active;

-- name: PublishNamingVars :one
INSERT INTO naming_vars_versions(fingerprint, content, active)
VALUES (sqlc.arg(fingerprint), sqlc.arg(content)::jsonb, TRUE)
ON CONFLICT (fingerprint) DO UPDATE SET active = TRUE, activated_at = CURRENT_TIMESTAMP
WHERE naming_vars_versions.content = EXCLUDED.content
RETURNING fingerprint;

-- name: GetCurrentNamingVars :one
SELECT fingerprint, content
FROM naming_vars_versions
WHERE active
LIMIT 1;

-- name: GetActiveNamingVarsVersionForUpdate :one
SELECT revision, fingerprint, content, origin, note, created_at, activated_at, active
FROM naming_vars_versions
WHERE active
FOR UPDATE;

-- name: GetActiveNamingVarsVersion :one
SELECT revision, fingerprint, content, origin, note, created_at, activated_at, active
FROM naming_vars_versions
WHERE active
LIMIT 1;

-- name: GetNamingVarsVersionByRevision :one
SELECT revision, fingerprint, content, origin, note, created_at, activated_at, active
FROM naming_vars_versions
WHERE revision = sqlc.arg(revision);

-- name: GetNamingVarsVersionByFingerprint :one
SELECT revision, fingerprint, content, origin, note, created_at, activated_at, active
FROM naming_vars_versions
WHERE fingerprint = sqlc.arg(fingerprint)
FOR UPDATE;

-- name: ListNamingVarsVersions :many
SELECT revision, fingerprint, origin, note, created_at, activated_at, active,
       octet_length(content #>> '{}')::int AS source_bytes
FROM naming_vars_versions
ORDER BY revision DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: InsertNamingVarsVersion :one
INSERT INTO naming_vars_versions(fingerprint, content, active, origin, note)
VALUES (sqlc.arg(fingerprint), sqlc.arg(content)::jsonb, TRUE, sqlc.arg(origin), sqlc.arg(note))
RETURNING revision, fingerprint, created_at, activated_at;

-- name: ActivateNamingVarsRevision :exec
UPDATE naming_vars_versions
SET active = TRUE, activated_at = CURRENT_TIMESTAMP
WHERE revision = sqlc.arg(revision);
