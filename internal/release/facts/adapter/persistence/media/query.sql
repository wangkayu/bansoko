-- Media facts persistence is owned by this adapter; the migration chain is authoritative.

-- name: LockReleaseSnapshot :exec
SELECT pg_advisory_xact_lock(hashtext($1));

-- name: CreateReleaseSnapshot :exec
WITH latest AS (
    SELECT schema_version, resolver_version, parser_kind, parser_config_fingerprint,
           parser_config, input_digest, release_identity, release_facts
    FROM media_fact_snapshots
    WHERE release_id = $2
    ORDER BY created_at DESC, id DESC
    LIMIT 1
)
INSERT INTO media_fact_snapshots
(id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint,
 parser_config, input_digest, release_identity, release_facts, created_at)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
WHERE NOT EXISTS (
    SELECT 1
    FROM latest
    WHERE schema_version = $3
      AND resolver_version = $4
      AND parser_kind = $5
      AND parser_config_fingerprint IS NOT DISTINCT FROM $6::text
      AND parser_config IS NOT DISTINCT FROM $7::jsonb
      AND input_digest = $8
      AND release_identity = $9::jsonb
      AND release_facts = $10::jsonb
);

-- name: UpsertFileFacts :exec
INSERT INTO media_fact_files
(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (snapshot_id, file_ref) DO UPDATE
SET media_kind = EXCLUDED.media_kind, size_bytes = EXCLUDED.size_bytes,
    source_fingerprint = EXCLUDED.source_fingerprint, file_evidence = EXCLUDED.file_evidence,
    effective_facts = EXCLUDED.effective_facts, created_at = EXCLUDED.created_at;

-- name: ReadLatestSnapshot :one
SELECT id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint,
       parser_config, input_digest, release_identity, release_facts, created_at
FROM media_fact_snapshots
WHERE release_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListFileRows :many
SELECT snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts, created_at
FROM media_fact_files
WHERE snapshot_id = $1
ORDER BY file_ref;

-- name: ListFileRowsBySnapshots :many
SELECT snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts, created_at
FROM media_fact_files
WHERE snapshot_id = ANY($1::text[])
ORDER BY snapshot_id, file_ref;
