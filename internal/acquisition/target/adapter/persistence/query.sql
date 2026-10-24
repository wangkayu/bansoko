-- Acquisition-target persistence adapter query surface.
-- db/migrations remains the only authoritative schema source.

-- name: ReadTargets :many
SELECT t.id, t.target_path, t.expected_size_bytes
FROM acquisition_targets AS t
JOIN releases AS rel ON rel.id = t.release_id
JOIN rss_sources AS src ON src.id = rel.rss_source_id
JOIN subscriptions AS sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = sqlc.arg(managed_anime_id)
ORDER BY t.release_id, t.target_ref;

-- name: ManagedAnimeExists :one
SELECT EXISTS (
    SELECT 1
    FROM managed_anime AS ma
    JOIN subscriptions AS sub ON sub.managed_anime_id = ma.id
    WHERE ma.id = sqlc.arg(managed_anime_id)
) AS exists;

-- name: ReadObservationVersions :many
SELECT id AS target_id, observation_version AS version
FROM acquisition_targets
WHERE id = ANY(sqlc.arg(target_ids)::text[]);

-- name: DeleteStaleTargets :one
SELECT public.target_delete_stale(sqlc.arg(release_id)::text,sqlc.arg(target_refs)::text[])::bigint AS deleted;

-- name: UpsertTarget :exec
SELECT public.target_upsert(sqlc.arg(id)::text,sqlc.arg(release_id)::text,sqlc.arg(target_ref)::text,sqlc.arg(source_path)::text,sqlc.arg(media_kind)::text,sqlc.arg(target_path)::text,sqlc.arg(expected_size_bytes)::bigint,sqlc.arg(intent)::text,sqlc.arg(collision_policy)::text,sqlc.arg(render_snapshot)::jsonb,sqlc.arg(config_snapshot)::jsonb,sqlc.arg(spec_digest)::text);

-- name: UpsertObservation :exec
SELECT public.target_observation_upsert(sqlc.arg(target_id)::text,sqlc.arg(state)::text,sqlc.narg(observed_size_bytes)::bigint,sqlc.narg(reason_code)::text,sqlc.arg(observed_at)::timestamptz);
