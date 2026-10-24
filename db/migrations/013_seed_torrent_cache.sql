-- Forward-only migration 013: lazy seed expansion gets a long-lived torrent cache.
--
-- The baseline is immutable, so this file carries the new relation instead of
-- editing 001. It is a new table in every environment and stores no historical
-- row, so it is created empty with no backfill.
--
-- Why a separate table instead of the snapshot's own seed row:
-- provider_feed_snapshots and provider_feed_snapshot_seeds are append-only
-- evidence (their append-only guard rejects UPDATE), and a snapshot's
-- content_hash is computed over the seed's torrent_state plus its manifest
-- projection digest. A snapshot now records only the RSS-native facts, so the
-- payload cannot be filled in after the snapshot was sealed. The expansion
-- cache is a content fact keyed by the seed locator: the same bytes serve every
-- snapshot that published the locator, and the read model recomputes targets,
-- filters and verdicts from the current configuration on every read.
--
-- The cache key is the locator alone. A locator embeds its provider host, so
-- the two providers cannot collide, and a redirect to different content is
-- rejected by the downloader's strong-identity check before any write; a
-- changed locator content is then re-fetched after the row is removed.

CREATE TABLE seed_torrent_cache (
    locator            TEXT PRIMARY KEY,
    payload            BYTEA NOT NULL,
    payload_digest     TEXT NOT NULL,
    payload_size_bytes BIGINT NOT NULL,
    torrent_version    TEXT NOT NULL,
    info_hash_v1       TEXT,
    info_hash_v2       TEXT,
    torrent_name       TEXT NOT NULL,
    total_size_bytes   BIGINT NOT NULL,
    file_count         INTEGER NOT NULL,
    projection_digest  TEXT NOT NULL,
    parser_version     TEXT NOT NULL,
    files              JSONB NOT NULL,
    fetched_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- The payload digest and size are recomputed from the bytes, exactly as the
    -- snapshot seed row does, so a corrupted cache row is refused by the
    -- database rather than handed to a downloader.
    CONSTRAINT seed_torrent_cache_payload_check CHECK (
        octet_length(payload) BETWEEN 1 AND 20971520
        AND payload_size_bytes = octet_length(payload)
        AND payload_digest = 'sha256:' || encode(sha256(payload), 'hex')
    ),
    CONSTRAINT seed_torrent_cache_payload_digest_check CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT seed_torrent_cache_locator_check CHECK (char_length(btrim(locator)) BETWEEN 1 AND 2048),
    CONSTRAINT seed_torrent_cache_torrent_version_check CHECK (torrent_version IN ('v1', 'v2', 'hybrid')),
    CONSTRAINT seed_torrent_cache_info_hash_v1_check CHECK (info_hash_v1 IS NULL OR info_hash_v1 ~ '^[0-9a-f]{40}$'),
    CONSTRAINT seed_torrent_cache_info_hash_v2_check CHECK (info_hash_v2 IS NULL OR info_hash_v2 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT seed_torrent_cache_info_hash_check CHECK (info_hash_v1 IS NOT NULL OR info_hash_v2 IS NOT NULL),
    CONSTRAINT seed_torrent_cache_torrent_name_check CHECK (char_length(btrim(torrent_name)) BETWEEN 1 AND 512),
    CONSTRAINT seed_torrent_cache_total_size_bytes_check CHECK (total_size_bytes BETWEEN 0 AND '1125899906842624'::bigint),
    CONSTRAINT seed_torrent_cache_file_count_check CHECK (file_count BETWEEN 1 AND 20000 AND file_count = jsonb_array_length(files)),
    CONSTRAINT seed_torrent_cache_projection_digest_check CHECK (projection_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT seed_torrent_cache_parser_version_check CHECK (char_length(btrim(parser_version)) BETWEEN 1 AND 64),
    CONSTRAINT seed_torrent_cache_files_check CHECK (jsonb_typeof(files) = 'array' AND pg_column_size(files) <= 16777216)
);

GRANT SELECT, INSERT, UPDATE ON TABLE seed_torrent_cache TO bansoko_runtime;
