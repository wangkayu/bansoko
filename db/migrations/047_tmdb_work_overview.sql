-- Forward-only migration 047: metadata_work_titles records the TMDB overview.
--
-- The TMDB work summary is a network fact like the work titles (naming-module
-- 3.1 dispatch class 1, frozen family): a match or search that observes a
-- bound work records its `overview` on the same row and in the same upsert as
-- the work's native titles, acceptance freezes it beside them
-- (media.tmdb_titles.value.overview), and the notification payload projects it
-- as `tmdb.summary`. The naming scenario never lists it: a multi-paragraph
-- overview in a path is structurally wrong, so the fact is notification-scope
-- only.
--
-- Bangumi has no equivalent overview surface: only tmdb rows accept a
-- non-NULL `overview` (the shape check below enforces that), and the write
-- path stores NULL for a bgm row regardless.
--
-- The column is nullable and never backfilled: every environment is a test
-- environment, and a work the server has not observed through a TMDB search or
-- match since this migration has no overview. Bound-only (metadata-refresh)
-- re-pulls do not exist; the provider re-pull re-records it.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations.

ALTER TABLE metadata_work_titles ADD COLUMN overview TEXT;

ALTER TABLE metadata_work_titles
    ADD CONSTRAINT metadata_work_titles_overview_check
        CHECK (overview IS NULL OR (octet_length(overview) BETWEEN 1 AND 2048 AND overview = btrim(overview))),
    ADD CONSTRAINT metadata_work_titles_overview_provider_check
        CHECK (provider = 'tmdb' OR overview IS NULL);

-- The notification payload projection keeps the Go implementation
-- (templatecatalog/notification TitleFacts) and the SQL twin key-for-key
-- equal: the twin re-created in 007 carried the four title keys, so this one
-- adds tmdb.summary beside them.
CREATE OR REPLACE FUNCTION notification_title_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.title',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title}') #>> '{}'),
 'bgm.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title_cn}') #>> '{}'),
 'tmdb.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{title_cn}') #>> '{}'),
 'tmdb.original_title',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{original_title}') #>> '{}'),
 'tmdb.summary',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{overview}') #>> '{}'))
$$;

-- The re-created shape checks itself against an input the projector would
-- read: a tmdb titles block whose known value carries the overview projects
-- the summary key, a known value without the member projects null beside an
-- unchanged title key, and a bgm row the schema accepts cannot carry an
-- overview the write path did not write. The row probe uses a dedicated work
-- id it clears first and after, so a populated database cannot collide.
DO $$
DECLARE
    input jsonb;
    facts jsonb;
BEGIN
    input := jsonb_build_object(
        'media', jsonb_build_object(
            'tmdb_titles', jsonb_build_object('state', 'known', 'value', jsonb_build_object(
                'title_cn', '弧之谜语', 'original_title', 'Literally Arcana', 'overview', '这个名字是大陆译名。', 'year', 2024))));
    facts := public.notification_title_facts(input);
    IF facts ->> 'tmdb.summary' IS DISTINCT FROM '这个名字是大陆译名。'
       OR (facts ->> 'bgm.title') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 047 self-check failed: notification_title_facts did not project the frozen overview: %', facts;
    END IF;
    input := jsonb_build_object(
        'media', jsonb_build_object(
            'tmdb_titles', jsonb_build_object('state', 'known', 'value', jsonb_build_object(
                'title_cn', '弧之谜语', 'original_title', 'Literally Arcana', 'year', 2024))));
    facts := public.notification_title_facts(input);
    IF (facts ->> 'tmdb.summary') IS NOT NULL
       OR facts ->> 'tmdb.title_cn' IS DISTINCT FROM '弧之谜语' THEN
        RAISE EXCEPTION 'migration 047 self-check failed: tmdb.summary did not read a frozen value as empty: %', facts;
    END IF;
    BEGIN
        DELETE FROM metadata_work_titles
        WHERE provider = 'bgm' AND media_kind = 'subject' AND work_id = '047-selfcheck';
        INSERT INTO metadata_work_titles(provider, media_kind, work_id, title, title_cn, original_title, overview)
        VALUES ('bgm', 'subject', '047-selfcheck', '弧之谜语', '弧之谜语', NULL, 'masthead overview');
        DELETE FROM metadata_work_titles
        WHERE provider = 'bgm' AND media_kind = 'subject' AND work_id = '047-selfcheck';
        RAISE EXCEPTION 'migration 047 self-check failed: a bgm row accepted an overview';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
    DELETE FROM metadata_work_titles
    WHERE provider = 'bgm' AND media_kind = 'subject' AND work_id = '047-selfcheck';
END
$$;
