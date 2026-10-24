-- Media facts persistence is owned by this adapter; the migration chain is authoritative.

-- name: CreateReleaseSnapshot :exec
INSERT INTO media_fact_snapshots
(id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint,
 parser_config, input_digest, release_identity, release_facts, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

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
