-- name: ListOutboundHealth :many
SELECT bucket_key, cooldown_until, last_limited_at, reason_code, summary, consecutive, version, updated_at
FROM public.provider_outbound_health ORDER BY bucket_key;

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
