-- Forward-only migration 007: TMDB titled facts carry the request language
-- value in title_cn.
--
-- Every TMDB request this product makes sends `language=zh-CN`
-- (metadata/matcher search + details, identitymetadata search/multi + tv
-- details), so the returned `name`/`title` IS the localized title. The
-- baseline stored it in `metadata_work_titles.title` and forced `title_cn`
-- to NULL for tmdb rows, which is the opposite of the fact's meaning: the
-- native name is `original_title` and the localized one is `title_cn`.
--
-- This migration is both a data fix and a constraint replacement:
--   1. the installed `metadata_work_titles_check` is dropped first, because it
--      still requires `title_cn IS NULL` for every tmdb row: moving a value
--      into `title_cn` while it is in force aborts on any populated database,
--      while a fresh database has no tmdb row and never sees the order,
--   2. every existing tmdb row then moves its value from `title` to `title_cn`
--      and clears `title`,
--   3. the replacement check is `tmdb -> title IS NULL` instead of
--      `tmdb -> title_cn IS NULL`, so the invariant now describes the real
--      shape (bgm unchanged: subject rows may carry both `title` and
--      `title_cn` and never an `original_title`),
--   4. `notification_title_facts` is re-created so the notification key is
--      `tmdb.title_cn`, reading `media.tmdb_titles.value.title_cn`, keeping
--      the Go and SQL implementations key-for-key equal.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations. The columns
-- themselves do not change, so the runner's baseline column inventory is
-- unaffected.

ALTER TABLE metadata_work_titles
    DROP CONSTRAINT metadata_work_titles_check;

UPDATE metadata_work_titles
SET title_cn = title, title = NULL
WHERE provider = 'tmdb' AND title IS NOT NULL;

ALTER TABLE metadata_work_titles
    ADD CONSTRAINT metadata_work_titles_check
        CHECK ((provider = 'bgm' AND media_kind = 'subject' AND original_title IS NULL) OR (provider = 'tmdb' AND media_kind IN ('tv', 'movie') AND title IS NULL));

CREATE OR REPLACE FUNCTION notification_title_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.title',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title}') #>> '{}'),
 'bgm.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title_cn}') #>> '{}'),
 'tmdb.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{title_cn}') #>> '{}'),
 'tmdb.original_title',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{original_title}') #>> '{}'))
$$;
