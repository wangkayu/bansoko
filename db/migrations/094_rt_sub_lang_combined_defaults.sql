-- Forward-only migration 094: the default rt.sub_lang ranking uses reltitle's
-- combined Chinese+Japanese subtitle values (owner ruling 2026-10-28).
--
-- Migration 064 copied AniBT's decomposed layout (SC+TC+JPN, SC+JPN, ...) into
-- the rt.sub_lang ranking, but reltitle reports Chinese plus Japanese
-- subtitles as one combined value (JPSC, JPTC) and drops SC/TC/JPN when it
-- does, so the most preferred releases (简繁日, 简日) matched no rank. The
-- default now mirrors the AniBT order in reltitle's spelling: JPSC+JPTC,
-- JPSC, SC+TC, SC, JPTC, TC, JPN, ENG.
--
-- Only the Global default is updated, and at item level: the rt.sub_lang item
-- of the stored profile is replaced only while its ranks are still exactly the
-- old default; its position and every other item stay as they are, so an
-- edited rt.sub_lang item, and a Global whose other items were edited, are
-- handled per item. Per-anime copies and drafts are deliberately left
-- unchanged: an upgrade never changes settings stored on existing
-- subscriptions (owner ruling 2026-10-28), because the version priority also
-- decides which existing files retention keeps.

CREATE FUNCTION pg_temp.rt_sub_lang_profile_094(profile jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN jsonb_typeof(profile->'items') = 'array' THEN
        jsonb_set(profile, '{items}', COALESCE((
            SELECT jsonb_agg(
                       CASE WHEN i.item->>'key' = 'rt.sub_lang'
                                 AND i.item->'ranks' = '[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]'::jsonb
                            THEN jsonb_set(i.item, '{ranks}', '[["JPSC","JPTC"],["JPSC"],["SC","TC"],["SC"],["JPTC"],["TC"],["JPN"],["ENG"]]'::jsonb)
                            ELSE i.item END
                       ORDER BY i.ord)
              FROM jsonb_array_elements(profile->'items') WITH ORDINALITY AS i(item, ord)
        ), '[]'::jsonb))
    ELSE profile END
$$;

-- Global default.
UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', pg_temp.rt_sub_lang_profile_094(value->'quality_profile')),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND pg_temp.rt_sub_lang_profile_094(value->'quality_profile') IS DISTINCT FROM value->'quality_profile';

DROP FUNCTION pg_temp.rt_sub_lang_profile_094(jsonb);
