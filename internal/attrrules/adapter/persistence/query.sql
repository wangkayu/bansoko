-- Attribute-rule version persistence is owned by this adapter; the migration chain is authoritative.

-- name: LockAttrRulesPublish :exec
SELECT pg_advisory_xact_lock(hashtext('bansoko:attr-rules')::bigint);

-- name: GetActiveAttrRulesFingerprint :one
SELECT fingerprint
FROM attr_rule_versions
WHERE active
FOR UPDATE;

-- name: DeactivateAttrRules :exec
UPDATE attr_rule_versions SET active = FALSE WHERE active;

-- name: PublishAttrRules :one
INSERT INTO attr_rule_versions(fingerprint, content, active)
VALUES (sqlc.arg(fingerprint), sqlc.arg(content)::jsonb, TRUE)
ON CONFLICT (fingerprint) DO UPDATE SET active = TRUE
WHERE attr_rule_versions.content = EXCLUDED.content
RETURNING fingerprint;

-- name: GetCurrentAttrRules :one
SELECT fingerprint, content
FROM attr_rule_versions
WHERE active
LIMIT 1;
