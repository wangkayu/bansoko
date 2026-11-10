-- name: GetBangumiDataSnapshot :one
SELECT upstream_version, payload, last_modified, fetched_at, checked_at
FROM bangumi_data_snapshots
WHERE singleton_id = 'default';

-- name: SaveBangumiDataSnapshot :exec
INSERT INTO bangumi_data_snapshots (
    singleton_id, upstream_version, payload, last_modified, fetched_at, checked_at
) VALUES ('default', $1, $2, $3, $4, $4)
ON CONFLICT (singleton_id) DO UPDATE SET
    upstream_version = EXCLUDED.upstream_version,
    payload = EXCLUDED.payload,
    last_modified = EXCLUDED.last_modified,
    fetched_at = EXCLUDED.fetched_at,
    checked_at = EXCLUDED.checked_at;

-- name: TouchBangumiDataSnapshot :exec
UPDATE bangumi_data_snapshots
SET checked_at = $1
WHERE singleton_id = 'default';
