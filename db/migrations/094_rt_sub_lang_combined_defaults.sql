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
-- The replacement is item-level: in a stored quality profile only the
-- rt.sub_lang item is replaced, and only while its ranks are still exactly the
-- old default; its position and every other item stay as they are, so an
-- edited rt.sub_lang item, and a profile whose other items were edited, are
-- handled per item. By owner ruling 2026-10-28 this includes the per-anime
-- copies (overriding the 067 precedent that left them alone): the Global
-- default, the live per-anime replacement value with its business snapshot
-- copy (digest and business version follow), and the business copy of
-- unlinked drafts. Linked drafts, candidate revisions and other sealed or
-- append-only records are not touched.

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

-- Per-anime live value and its business snapshot copy, in one statement: the
-- guard trigger checks the new digest against the canonical hash of the new
-- snapshot on every UPDATE, and the business version is the CAS token
-- acquisition captures.
UPDATE anime_business_configs AS config
SET quality_profile = rewritten.quality_profile,
    business_snapshot = rewritten.snapshot,
    business_digest = CASE WHEN rewritten.snapshot IS NULL THEN config.business_digest
        ELSE 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(rewritten.snapshot), 'UTF8'), 'sha256'), 'hex') END,
    business_version = config.business_version + 1,
    updated_at = CURRENT_TIMESTAMP
FROM (
    SELECT managed_anime_id,
           pg_temp.rt_sub_lang_profile_094(quality_profile) AS quality_profile,
           CASE WHEN jsonb_typeof(business_snapshot #> '{replacement,quality_profile}') = 'object'
                THEN jsonb_set(business_snapshot, '{replacement,quality_profile}',
                               pg_temp.rt_sub_lang_profile_094(business_snapshot #> '{replacement,quality_profile}'))
                ELSE business_snapshot END AS snapshot
    FROM anime_business_configs
) AS rewritten
WHERE config.managed_anime_id = rewritten.managed_anime_id
  AND (rewritten.quality_profile IS DISTINCT FROM config.quality_profile
       OR rewritten.snapshot IS DISTINCT FROM config.business_snapshot);

-- Unlinked drafts carry a business copy with its own digest; the candidate
-- digest trigger recomputes draft_digest when it is empty, and the version
-- advances by exactly one as the candidate guard requires.
UPDATE anime_config_candidates AS candidate
SET draft = jsonb_set(jsonb_set(candidate.draft, '{business}', rewritten.business),
                      '{business_digest}',
                      to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(rewritten.business), 'UTF8'), 'sha256'), 'hex'))),
    draft_digest = '',
    version = candidate.version + 1,
    updated_at = CURRENT_TIMESTAMP
FROM (
    SELECT id,
           jsonb_set(draft->'business', '{replacement,quality_profile}',
                     pg_temp.rt_sub_lang_profile_094(draft #> '{business,replacement,quality_profile}')) AS business
    FROM anime_config_candidates
    WHERE activated_managed_anime_id IS NULL
      AND jsonb_typeof(draft #> '{business,replacement,quality_profile}') = 'object'
) AS rewritten
WHERE candidate.id = rewritten.id
  AND rewritten.business IS DISTINCT FROM candidate.draft->'business';

DROP FUNCTION pg_temp.rt_sub_lang_profile_094(jsonb);
