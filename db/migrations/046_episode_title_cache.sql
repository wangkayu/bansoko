-- Forward-only migration 046: the provider episode title is a render-time
-- network fact again, with a persistent cache.
--
-- Owner decree 2026-10-22 (reversing the 025 removal): the rename render and
-- the anime-files alignment render may query TMDB/BGM at render time, and the
-- notification payload reads the queried title again. What stays dead is the
-- 025-era plan-time design: no plan generation freezes a lookup, the query
-- happens at the render point and this table is the freeze point.
--
--   * notification_episode_title_facts is restored: a render snapshot that
--     carries the `episode_titles` block again projects bgm.ep_title and
--     tmdb.ep_title into the notification payload. A notification never
--     queries: it reads the block the rename render froze, and an unknown
--     title renders empty.
--   * episode_title_cache is the persistent lookup cache, keyed by
--     (source, work id, season, episode). `known` rows hold an aired title
--     and never expire; `absent` rows are the definite "the source has no
--     title for that episode" answer the renderer re-checks after the
--     negative TTL (a named constant in the runtime, no tuning env). A
--     transient failure (network error, rate limit, timeout) is not cached:
--     the next render retries the lookup.
--
-- Value shape follows render_episode_title_valid (001): a stored title is
-- 1..512 bytes, trimmed, without control characters. The season column is
-- part of the key: a TMDB show is bound per season and episode 1 of one
-- season is not episode 1 of the next; BGM lookups are per subject and write
-- season 0.

CREATE OR REPLACE FUNCTION notification_episode_title_facts(p_render jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.ep_title',public.file_notification_text(CASE WHEN p_render->'episode_titles'->'bgm'->>'state'='known' THEN p_render->'episode_titles'->'bgm'->>'value' END),
 'tmdb.ep_title',public.file_notification_text(CASE WHEN p_render->'episode_titles'->'tmdb'->>'state'='known' THEN p_render->'episode_titles'->'tmdb'->>'value' END))
$$;

CREATE TABLE episode_title_cache (
    source TEXT NOT NULL,
    work_id TEXT NOT NULL,
    season INTEGER NOT NULL DEFAULT 0,
    episode INTEGER NOT NULL,
    state TEXT NOT NULL,
    title TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT episode_title_cache_pkey PRIMARY KEY (source, work_id, season, episode),
    CONSTRAINT episode_title_cache_source_check CHECK (source IN ('bgm', 'tmdb')),
    CONSTRAINT episode_title_cache_work_id_check CHECK (char_length(btrim(work_id)) BETWEEN 1 AND 128 AND btrim(work_id) = work_id),
    CONSTRAINT episode_title_cache_season_check CHECK (season >= 0),
    CONSTRAINT episode_title_cache_episode_check CHECK (episode >= 1),
    CONSTRAINT episode_title_cache_state_check CHECK (state IN ('known', 'absent')),
    CONSTRAINT episode_title_cache_title_check CHECK (state = 'absent' OR (
        octet_length(title) BETWEEN 1 AND 512 AND btrim(title) = title AND title !~ '[[:cntrl:]]')),
    CONSTRAINT episode_title_cache_version_check CHECK (version >= 1)
);
COMMENT ON TABLE episode_title_cache IS 'Persistent provider episode title cache keyed by (source, work id, season, episode): known rows never expire, absent rows are re-checked after the runtime negative TTL, and a transient lookup failure is never cached.';
COMMENT ON COLUMN episode_title_cache.season IS 'The TMDB season the episode belongs to; BGM lookups are per subject and write 0.';
COMMENT ON COLUMN episode_title_cache.fetched_at IS 'When the state was read from the provider; for a known row also the only time it is ever refreshed.';

-- The rename and alignment renders read one row and upsert the fetched
-- answer. Every other runtime path leaves the table alone.
REVOKE ALL ON TABLE episode_title_cache FROM bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE episode_title_cache TO bansoko_runtime;

-- The restoration checks itself against the same validator the snapshot
-- constraint uses, so a block the projector would read but the schema rejects
-- fails the migration on an empty database too.
DO $$
DECLARE
    block jsonb := jsonb_build_object(
        'renderer_version', 'fileoperation-render-v1',
        'episode_titles', jsonb_build_object(
            'episode', '7',
            'bgm', jsonb_build_object('state', 'known', 'value', '炎の七夕 祭り', 'reason_code', NULL),
            'tmdb', jsonb_build_object('state', 'absent', 'value', NULL, 'reason_code', NULL)));
    facts jsonb;
BEGIN
    IF NOT public.render_episode_titles_valid(block) THEN
        RAISE EXCEPTION 'migration 046 self-check failed: revived episode_titles block is not a valid render snapshot shape';
    END IF;
    facts := public.notification_episode_title_facts(block);
    IF facts ->> 'bgm.ep_title' IS DISTINCT FROM '炎の七夕 祭り'
       OR (facts ->> 'tmdb.ep_title') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 046 self-check failed: notification_episode_title_facts did not project the frozen episode titles: %', facts;
    END IF;
END
$$;
