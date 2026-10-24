-- Forward-only migration 027: the per-file subtitle suffix default.
--
-- The shipped subtitle_suffix_template was `${rt.sub_lang}`, a release-level
-- value: every subtitle of one release rendered the same suffix, so two
-- subtitles of one video in different languages (`.chs&jpn.ass`,
-- `.cht&jpn.ass`) landed on one target. The new default is
-- `${sub.lang_bcp47}`, the subtitle's own one BCP 47 tag (its filename tag
-- first, else the release title).
--
-- No seed row carries naming rules; a stored copy exists only where the owner
-- saved the global or an anime naming section while it still held the old
-- default. Such a copy that is exactly the old default is rewritten; any other
-- template is the owner's own and is left alone. There is no data-preservation
-- obligation.
--
-- Frozen render snapshots, candidate drafts and revisions are not rewritten:
-- their digests are part of their identity, and a frozen snapshot replays
-- under the renderer version it recorded.

CREATE FUNCTION pg_temp.subtitle_suffix_027(p_value jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT CASE WHEN p_value->>'subtitle_suffix_template' = '`${rt.sub_lang}`'
  THEN jsonb_set(p_value, '{subtitle_suffix_template}', to_jsonb('`${sub.lang_bcp47}`'::text))
  ELSE p_value END
$$;

-- The rewrite checks itself before it touches a row, so a rewrite that does
-- not do what this header says fails the migration on an empty database too.
DO $$
BEGIN
    IF pg_temp.subtitle_suffix_027('{"subtitle_suffix_template":"`${rt.sub_lang}`","tv":{}}'::jsonb)
       IS DISTINCT FROM '{"subtitle_suffix_template":"`${sub.lang_bcp47}`","tv":{}}'::jsonb
       OR pg_temp.subtitle_suffix_027('{"subtitle_suffix_template":"`${rt.sub_lang}.forced`"}'::jsonb)
       IS DISTINCT FROM '{"subtitle_suffix_template":"`${rt.sub_lang}.forced`"}'::jsonb THEN
        RAISE EXCEPTION 'migration 027 subtitle suffix rewrite self-check failed';
    END IF;
END
$$;

UPDATE global_config_sections
   SET value = pg_temp.subtitle_suffix_027(value), version = version + 1, updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.subtitle_suffix_027(value);

UPDATE anime_config_overrides
   SET value = pg_temp.subtitle_suffix_027(value), updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.subtitle_suffix_027(value);

DROP FUNCTION pg_temp.subtitle_suffix_027(jsonb);
