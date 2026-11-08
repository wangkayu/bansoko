-- Forward-only migration 129: the rt.content_rating comparison item.
--
-- reltitle v0.1.0 normalizes the lexicon's content_rating dimension to three
-- values (UNCENSORED, CENSORED, AGE_RESTRICTED) and bansoko now projects all
-- three into the frozen title tags. rt.content_rating is a ranked single-value
-- comparison item (no default order entry); its values come from the same
-- frozen tags as the other rt.* items. It exposes no template field, so the
-- notification payload is unchanged and notification_value_facts stays as it
-- is. The boolean rt.uncensored comparison item is retired with it: the
-- Uncensored template fact remains, but it is no longer a comparison item.
-- The validator body is the one from migration 066 with three changes: the
-- key allow-list drops rt.uncensored and gains rt.content_rating, the flag
-- preference check is gone, and the item-count bound stays 14, the number of
-- registered items.
--
-- Stored profiles are cleaned up because a retired item is a representation
-- change, not a preference change: the Global default, every instance's frozen
-- policy column, and its frozen business snapshot lose the item in place, and
-- the versions that describe those documents move with them. The snapshot is
-- rewritten with its digest in the same statement (anime_business_config_guard
-- enforces the pair), so ruling 3 keeps holding: the row stays the single
-- configuration source. Nothing else in the profile changes.

CREATE OR REPLACE FUNCTION replacement_quality_profile_valid(profile jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
 item jsonb;
 entry jsonb;
 val jsonb;
 keys text[] := ARRAY[]::text[];
BEGIN
 IF profile IS NULL OR jsonb_typeof(profile) IS DISTINCT FROM 'object'
    OR (SELECT count(*) FROM jsonb_object_keys(profile)) <> 2
    OR NOT (profile ?& ARRAY['version','items'])
    OR profile->>'version' IS DISTINCT FROM 'profile-v1'
    OR jsonb_typeof(profile->'items') IS DISTINCT FROM 'array'
    OR jsonb_array_length(profile->'items') > 14 THEN
  RETURN FALSE;
 END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(profile->'items') LOOP
  IF jsonb_typeof(item) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
     OR NOT (item ?& ARRAY['key','ranks'])
     OR jsonb_typeof(item->'key') IS DISTINCT FROM 'string'
     OR item->>'key' NOT IN ('subgroup.score','release.lang','release.resolution','release.revision',
                             'anibt.resolution','anibt.language','anibt.subtitle','anibt.format',
                             'rt.resolution','rt.sub_lang','rt.sub_flag','rt.source','rt.codec',
                             'rt.content_rating')
     OR item->>'key' = ANY(keys)
     OR jsonb_typeof(item->'ranks') IS DISTINCT FROM 'array'
     OR jsonb_array_length(item->'ranks') > 64 THEN
   RETURN FALSE;
  END IF;
  keys := array_append(keys, item->>'key');
  FOR entry IN SELECT value FROM jsonb_array_elements(item->'ranks') LOOP
   IF jsonb_typeof(entry) IS DISTINCT FROM 'array' OR jsonb_array_length(entry) NOT BETWEEN 1 AND 8 THEN
    RETURN FALSE;
   END IF;
   FOR val IN SELECT value FROM jsonb_array_elements(entry) LOOP
    IF jsonb_typeof(val) IS DISTINCT FROM 'string' OR btrim(val #>> '{}') = '' OR octet_length(val #>> '{}') > 128 THEN
     RETURN FALSE;
    END IF;
   END LOOP;
  END LOOP;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN
 RETURN FALSE;
END;
$$;

-- Drops the retired item from one profile, keeping every other item's position.
CREATE FUNCTION pg_temp.rt_content_rating_profile_129(profile jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN jsonb_typeof(profile->'items') = 'array' THEN
        jsonb_set(profile, '{items}', COALESCE((
            SELECT jsonb_agg(i.item ORDER BY i.ord)
            FROM jsonb_array_elements(profile->'items') WITH ORDINALITY AS i(item, ord)
            WHERE i.item->>'key' IS DISTINCT FROM 'rt.uncensored'
        ), '[]'::jsonb))
    ELSE profile END
$$;

UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', pg_temp.rt_content_rating_profile_129(value->'quality_profile')),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND pg_temp.rt_content_rating_profile_129(value->'quality_profile') IS DISTINCT FROM value->'quality_profile';

UPDATE anime_business_configs AS c
SET quality_profile = pg_temp.rt_content_rating_profile_129(c.quality_profile),
    business_snapshot = CASE
        WHEN pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile')
             IS DISTINCT FROM c.business_snapshot->'replacement'->'quality_profile'
        THEN jsonb_set(c.business_snapshot, '{replacement,quality_profile}',
                       pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile'))
        ELSE c.business_snapshot END,
    business_digest = CASE
        WHEN pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile')
             IS DISTINCT FROM c.business_snapshot->'replacement'->'quality_profile'
        THEN 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
                 jsonb_set(c.business_snapshot, '{replacement,quality_profile}',
                           pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile'))), 'UTF8'), 'sha256'), 'hex')
        ELSE c.business_digest END,
    replacement_version = CASE
        WHEN pg_temp.rt_content_rating_profile_129(c.quality_profile) IS DISTINCT FROM c.quality_profile
        THEN c.replacement_version + 1
        ELSE c.replacement_version END,
    business_version = CASE
        WHEN pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile')
             IS DISTINCT FROM c.business_snapshot->'replacement'->'quality_profile'
        THEN c.business_version + 1
        ELSE c.business_version END,
    updated_at = CURRENT_TIMESTAMP
WHERE c.quality_profile IS NOT NULL
  AND (pg_temp.rt_content_rating_profile_129(c.quality_profile) IS DISTINCT FROM c.quality_profile
       OR pg_temp.rt_content_rating_profile_129(c.business_snapshot->'replacement'->'quality_profile')
          IS DISTINCT FROM c.business_snapshot->'replacement'->'quality_profile');

-- Candidate drafts copy the business at creation, so the same item lives in
-- their draft body; the defaults trigger recomputes draft_digest from the
-- rewritten draft. The candidate guard refuses a linked draft rewrite, so the
-- migration-only fixture switch lets this one shape rewrite through, exactly as
-- migration 063 did.
SET LOCAL bansoko.candidate_fixture = 'on';
UPDATE anime_config_candidates
SET draft = jsonb_set(
        jsonb_set(draft, '{business}',
                  jsonb_set(draft->'business', '{replacement,quality_profile}',
                            pg_temp.rt_content_rating_profile_129(draft->'business'->'replacement'->'quality_profile'))),
        '{business_digest}',
        to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
            jsonb_set(draft->'business', '{replacement,quality_profile}',
                      pg_temp.rt_content_rating_profile_129(draft->'business'->'replacement'->'quality_profile'))), 'UTF8'), 'sha256'), 'hex'))),
    draft_digest = ''
WHERE jsonb_path_exists(draft, '$.business.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")');
RESET bansoko.candidate_fixture;

-- Candidate revisions are append-only; their guards are lifted for this one
-- shape rewrite only. The effective sections are untouched, so effective_digest
-- and every current_revision_digest mirror stay valid.
ALTER TABLE anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
UPDATE anime_config_candidate_revisions
SET business_snapshot = jsonb_set(business_snapshot, '{replacement,quality_profile}',
        pg_temp.rt_content_rating_profile_129(business_snapshot->'replacement'->'quality_profile')),
    business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
        jsonb_set(business_snapshot, '{replacement,quality_profile}',
                  pg_temp.rt_content_rating_profile_129(business_snapshot->'replacement'->'quality_profile'))), 'UTF8'), 'sha256'), 'hex')
WHERE jsonb_path_exists(business_snapshot, '$.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")');
WITH rewritten AS (
    SELECT r.candidate_id, r.revision, r.draft,
           jsonb_set(
               jsonb_set(r.draft, '{business}',
                         jsonb_set(r.draft->'business', '{replacement,quality_profile}',
                                   pg_temp.rt_content_rating_profile_129(r.draft->'business'->'replacement'->'quality_profile'))),
               '{business_digest}',
               to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
                   jsonb_set(r.draft->'business', '{replacement,quality_profile}',
                             pg_temp.rt_content_rating_profile_129(r.draft->'business'->'replacement'->'quality_profile'))), 'UTF8'), 'sha256'), 'hex'))) AS new_draft
    FROM anime_config_candidate_revisions r
    WHERE jsonb_path_exists(r.draft, '$.business.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")')
)
UPDATE anime_config_candidate_revisions r
SET draft = w.new_draft,
    -- A draft digest that was the canonical draft hash follows the rewritten
    -- draft; any other recorded digest is left as it was (migration 063's rule).
    draft_digest = CASE
        WHEN r.draft_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(r.draft), 'UTF8'), 'sha256'), 'hex')
        THEN 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(w.new_draft), 'UTF8'), 'sha256'), 'hex')
        ELSE r.draft_digest END
FROM rewritten w
WHERE r.candidate_id = w.candidate_id AND r.revision = w.revision;
ALTER TABLE anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg;

DROP FUNCTION pg_temp.rt_content_rating_profile_129(jsonb);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'replacement_quality_profile_valid'
          AND prosrc LIKE '%rt.content_rating%' AND prosrc LIKE '%> 14 %'
          AND prosrc NOT LIKE '%rt.uncensored%') THEN
        RAISE EXCEPTION 'migration 129 self-check failed: the validator does not accept rt.content_rating alone';
    END IF;
    IF EXISTS (
        SELECT 1 FROM global_config_sections
         WHERE section_name = 'replacement'
           AND jsonb_path_exists(value, '$.quality_profile.items[*] ? (@.key == "rt.uncensored")'))
       OR EXISTS (
        SELECT 1 FROM anime_business_configs
         WHERE jsonb_path_exists(quality_profile, '$.items[*] ? (@.key == "rt.uncensored")')
            OR jsonb_path_exists(business_snapshot, '$.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")'))
       OR EXISTS (
        SELECT 1 FROM anime_config_candidates
         WHERE jsonb_path_exists(draft, '$.business.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")'))
       OR EXISTS (
        SELECT 1 FROM anime_config_candidate_revisions
         WHERE jsonb_path_exists(business_snapshot, '$.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")')
            OR jsonb_path_exists(draft, '$.business.replacement.quality_profile.items[*] ? (@.key == "rt.uncensored")')) THEN
        RAISE EXCEPTION 'migration 129 self-check failed: a stored profile still lists rt.uncensored';
    END IF;
END $$;
