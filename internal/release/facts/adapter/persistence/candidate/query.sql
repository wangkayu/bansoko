-- Candidate facts persistence is owned by this adapter; the migration chain is authoritative.

-- name: UpsertCandidateFacts :execresult
INSERT INTO release_candidate_facts
(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys,
 resolution, revision, source, language, "group", size_bytes, compare_values, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, CURRENT_TIMESTAMP)
ON CONFLICT (release_id, release_version) DO UPDATE SET
  created_at = release_candidate_facts.created_at
WHERE release_candidate_facts.input_fingerprint = EXCLUDED.input_fingerprint;

-- name: GetLatestCandidateFactsVersion :one
SELECT release_version
FROM release_candidate_facts
WHERE release_id = $1
ORDER BY release_version DESC
LIMIT 1;

-- name: GetCandidateFacts :one
SELECT
    release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts,
    coverage_keys, resolution, revision, source, language, "group", size_bytes, created_at, compare_values
FROM release_candidate_facts
WHERE release_id = $1 AND release_version = $2;

-- name: LockReleaseRawFacts :one
SELECT raw_facts
FROM releases
WHERE id = $1
FOR UPDATE;

-- name: LockLatestCandidateFacts :one
SELECT
    release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts,
    coverage_keys, resolution, revision, source, language, "group", size_bytes, created_at, compare_values
FROM release_candidate_facts
WHERE release_id = $1
ORDER BY release_version DESC
LIMIT 1
FOR UPDATE;
