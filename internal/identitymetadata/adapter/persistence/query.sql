-- Poster cache persistence is static and bounded.

-- name: GetMediaPosterCache :one
UPDATE media_poster_cache
SET last_used = CURRENT_TIMESTAMP
WHERE bgm_id = $1
RETURNING bgm_id, image_bytes, content_type, image_url, fetched_at, expires_at, last_used;

-- name: UpsertMediaPosterCache :exec
INSERT INTO media_poster_cache(
    bgm_id, image_bytes, content_type, image_url, fetched_at, expires_at, last_used
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (bgm_id) DO UPDATE
SET image_bytes = EXCLUDED.image_bytes,
    content_type = EXCLUDED.content_type,
    image_url = EXCLUDED.image_url,
    fetched_at = EXCLUDED.fetched_at,
    expires_at = EXCLUDED.expires_at,
    last_used = EXCLUDED.last_used;

-- name: PurgeExpiredMediaPosterCache :execresult
DELETE FROM media_poster_cache AS poster
WHERE poster.bgm_id IN (
    SELECT candidate.bgm_id
    FROM media_poster_cache AS candidate
    WHERE candidate.expires_at <= CURRENT_TIMESTAMP
    ORDER BY candidate.expires_at, candidate.bgm_id
    LIMIT $1
);

-- Provider cover images behind the authenticated same-origin proxy. A row is
-- the server's registry entry: the proxy resolves the upstream URL only from it.

-- name: RegisterProviderImages :exec
INSERT INTO provider_image_cache(image_key, provider, source_url)
SELECT unnest(sqlc.arg(image_keys)::text[]), sqlc.arg(provider)::text, unnest(sqlc.arg(source_urls)::text[])
ON CONFLICT (image_key) DO UPDATE
SET last_used = CURRENT_TIMESTAMP
WHERE provider_image_cache.last_used < CURRENT_TIMESTAMP - INTERVAL '1 day';

-- name: GetProviderImage :one
SELECT image_key, provider, source_url, image_bytes, content_type, fetched_at, expires_at, last_used
FROM provider_image_cache
WHERE image_key = $1;

-- name: TouchProviderImage :exec
UPDATE provider_image_cache
SET last_used = CURRENT_TIMESTAMP
WHERE image_key = $1
  AND last_used < CURRENT_TIMESTAMP - INTERVAL '1 hour';

-- name: StoreProviderImage :execresult
UPDATE provider_image_cache
SET image_bytes = $2,
    content_type = $3,
    fetched_at = $4,
    expires_at = $5,
    last_used = CURRENT_TIMESTAMP
WHERE image_key = $1;

-- name: PurgeIdleProviderImages :execresult
DELETE FROM provider_image_cache AS image
WHERE image.image_key IN (
    SELECT candidate.image_key
    FROM provider_image_cache AS candidate
    WHERE candidate.last_used <= CURRENT_TIMESTAMP - INTERVAL '30 days'
    ORDER BY candidate.last_used, candidate.image_key
    LIMIT $1
);
