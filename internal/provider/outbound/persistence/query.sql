-- name: ListOutboundHealth :many
SELECT bucket_key, cooldown_until, last_limited_at, reason_code, summary, consecutive, version, updated_at
FROM public.provider_outbound_health ORDER BY bucket_key;

-- name: ListGroupRefreshHealth :many
SELECT provider, state, COUNT(*)::bigint AS count,
    MAX(last_attempt_at)::timestamptz AS last_attempt_at,
    COALESCE(MAX(last_success_at), '0001-01-01 UTC')::timestamptz AS last_success_at
FROM public.provider_group_refresh_health
GROUP BY provider, state ORDER BY provider, state;

-- name: ListCatalogRefreshHealth :many
WITH latest AS (
    SELECT DISTINCT ON (provider, season) provider, season, status,
        upstream_state, COALESCE(health_checked_at, observed_at) AS checked_at
    FROM public.provider_catalog_snapshots
    WHERE status <> 'running'
    ORDER BY provider, season, version DESC
), success AS (
    SELECT provider, MAX(observed_at) AS checked_at
    FROM public.provider_catalog_snapshots
    WHERE status IN ('fresh', 'zero_result', 'withdrawn') GROUP BY provider
)
SELECT latest.provider,
    COALESCE(latest.upstream_state, CASE latest.status WHEN 'outage' THEN 'error'
        WHEN 'fresh' THEN 'available' ELSE 'empty' END)::text AS state,
    COUNT(*)::bigint AS count, MAX(latest.checked_at)::timestamptz AS last_attempt_at,
    COALESCE(MAX(success.checked_at), '0001-01-01 UTC')::timestamptz AS last_success_at
FROM latest LEFT JOIN success USING (provider)
GROUP BY 1, 2 ORDER BY 1, 2;

-- name: SaveOutboundHealth :exec
INSERT INTO public.provider_outbound_health
    (bucket_key, cooldown_until, last_limited_at, reason_code, summary, consecutive, version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (bucket_key) DO UPDATE SET
    cooldown_until = GREATEST(provider_outbound_health.cooldown_until, EXCLUDED.cooldown_until),
    last_limited_at = GREATEST(provider_outbound_health.last_limited_at, EXCLUDED.last_limited_at),
    reason_code = CASE WHEN EXCLUDED.version > provider_outbound_health.version THEN EXCLUDED.reason_code ELSE provider_outbound_health.reason_code END,
    summary = CASE WHEN EXCLUDED.version > provider_outbound_health.version THEN EXCLUDED.summary ELSE provider_outbound_health.summary END,
    consecutive = CASE WHEN EXCLUDED.version > provider_outbound_health.version THEN EXCLUDED.consecutive ELSE provider_outbound_health.consecutive END,
    version = GREATEST(provider_outbound_health.version, EXCLUDED.version),
    updated_at = GREATEST(provider_outbound_health.updated_at, EXCLUDED.updated_at);
