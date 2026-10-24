-- Forward-only migration 025: naming without execution-time metadata lookups.
--
-- bgm.ep_title and tmdb.ep_title were the only facts that needed a metadata
-- query while the library plan was made. They leave both catalogs, together
-- with the lookup:
--
--   * a render snapshot no longer carries `episode_titles`.
--     render_episode_titles_valid accepts a snapshot without the key under
--     every renderer version. A snapshot that still carries the key keeps the
--     old fileoperation-render-v1 shape check, so frozen rows stay valid.
--   * notification_episode_title_facts contributes no keys, so the
--     notification payloads no longer contain bgm.ep_title / tmdb.ep_title.
--   * episode_title_refreshes (the manual re-lookup cache) is dropped.
--
-- Stored templates are rewritten in place. There is no data-preservation
-- obligation; the rewrite keeps owner templates working where the meaning is
-- clear:
--
--   * naming: `/${anime.season_dir}` becomes `${anime.season_path}` (the
--     season folder composite, empty when no season is bound), `${s00e00}`
--     becomes `${ep.s00e00}` (the season/episode composite),
--     `defaultValue('x', path)` becomes `defaultIfEmpty(path, 'x')` (one
--     fallback helper, value first), an interpolation of an episode title is
--     removed and any other episode-title reference becomes ''.
--   * notification: a `{{ ... }}` tag that reads an episode title is removed.
--
-- Frozen render snapshots, candidate drafts and revisions are not rewritten:
-- their digests are part of their identity, and a frozen snapshot replays
-- under the renderer version it recorded.
--

CREATE OR REPLACE FUNCTION render_episode_titles_valid(p_render jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN NOT (p_render ? 'episode_titles') THEN true
 WHEN p_render->>'renderer_version'='fileoperation-render-v1' THEN COALESCE(
  jsonb_typeof(p_render->'episode_titles')='object'
  AND (SELECT array_agg(k COLLATE "C" ORDER BY k COLLATE "C") FROM jsonb_object_keys(p_render->'episode_titles') k)=ARRAY['bgm','episode','tmdb']::text[]
  AND (p_render->'episode_titles'->'episode'='null'::jsonb OR (jsonb_typeof(p_render->'episode_titles'->'episode')='string'
   AND octet_length(p_render->'episode_titles'->>'episode') BETWEEN 1 AND 64 AND p_render->'episode_titles'->>'episode' !~ '[[:cntrl:]]'))
  AND public.render_episode_title_valid(p_render->'episode_titles'->'bgm')
  AND public.render_episode_title_valid(p_render->'episode_titles'->'tmdb'),false)
 ELSE false END
$$;

CREATE OR REPLACE FUNCTION notification_episode_title_facts(p_render jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT '{}'::jsonb
$$;

DROP TABLE episode_title_refreshes;

CREATE FUNCTION pg_temp.naming_template_025(p_value jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT regexp_replace(regexp_replace(regexp_replace(regexp_replace(regexp_replace(
  p_value::text,
  '/\$\{anime\.season_dir\}', '${anime.season_path}', 'g'),
  '\$\{s00e00\}', '${ep.s00e00}', 'g'),
  'defaultValue\(\s*(''[^''\\]*'')\s*,\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\)', 'defaultIfEmpty(\2, \1)', 'g'),
  '\$\{\s*(bgm|tmdb)\.ep_title\s*\}', '', 'g'),
  '\m(bgm|tmdb)\.ep_title\M', '''''', 'g')::jsonb
$$;

-- The rewrite checks itself before it touches a row, so a pattern that does
-- not do what this header says fails the migration on an empty database too.
DO $$
DECLARE
    v_got jsonb := pg_temp.naming_template_025(jsonb_build_object(
        'dir', '`/library/${anime.title}/${anime.season_dir}`',
        'file', '`${anime.title} ${s00e00} ${bgm.ep_title}.${file.ext}`',
        'lang', '${defaultValue(''und'', release.lang)}',
        'expr', '${sanitize(tmdb.ep_title)}'));
BEGIN
    IF v_got IS DISTINCT FROM jsonb_build_object(
        'dir', '`/library/${anime.title}${anime.season_path}`',
        'file', '`${anime.title} ${ep.s00e00} .${file.ext}`',
        'lang', '${defaultIfEmpty(release.lang, ''und'')}',
        'expr', '${sanitize('''')}') THEN
        RAISE EXCEPTION 'migration 025 template rewrite self-check failed: %', v_got;
    END IF;
END
$$;

UPDATE global_config_sections
   SET value = pg_temp.naming_template_025(value), version = version + 1, updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_template_025(value);

UPDATE anime_config_overrides
   SET value = pg_temp.naming_template_025(value), updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_template_025(value);

UPDATE plugin_target_configs
   SET template_set = regexp_replace(template_set::text, '\{\{[^{}]*\m(bgm|tmdb)\.ep_title\M[^{}]*\}\}', '', 'g')::jsonb,
       version = version + 1, updated_at = CURRENT_TIMESTAMP
 WHERE template_set::text ~ '\m(bgm|tmdb)\.ep_title\M';

DROP FUNCTION pg_temp.naming_template_025(jsonb);
