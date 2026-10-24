-- Forward-only migration 053: the language-tag template fact rename.
--
-- The naming facts `sub.lang_bcp47` and `release.lang_bcp47` were renamed to
-- `sub.lang_tag` and `release.lang_tag`; the registry now registers only the
-- new leaf, so the retired path is an unknown context field. A stored naming
-- template that still interpolates `${sub.lang_bcp47}` (or
-- `${release.lang_bcp47}`) no longer compiles:
-- `TEMPLATE_COMPILE_ERROR: ... unknown context field sub.lang_bcp47`. That
-- matters beyond editing: a maintenance re-render compiles the CURRENT naming
-- rules, never a frozen copy, so one stale stored template breaks target
-- rendering for the whole anime.
--
-- What is rewritten (owner-owned, still editable):
--   * global_config_sections.naming_rules — the default template document;
--   * naming_template_library.value — every non-default template in the
--     library (the templates[] array, each item's rules);
--   * anime_config_overrides.naming_rules — every instance's own copy;
--   * anime_business_configs.business_snapshot — the frozen business of every
--     live instance (its sections.naming_rules plus its digest, recomputed by
--     the same canonical function the trigger checks).
--
-- What is deliberately NOT rewritten: candidate drafts and revisions,
-- one-shot input snapshots, download_tasks.frozen_policy and every
-- file_operations / acquisition snapshot. Those are identity-bearing frozen
-- evidence (their digests are the identity) and the engine replays them under
-- the renderer version they recorded; 025 and 045 state the same rule. A
-- frozen row keeps the bytes it was admitted with.
--
-- The rewrite is a plain substring replacement inside the template strings the
-- naming document owns (the three variants' directory_template and
-- video_filename_template, and subtitle_suffix_template): the exact
-- interpolation token `${sub.lang_bcp47}` becomes `${sub.lang_tag}`, and
-- `${release.lang_bcp47}` becomes `${release.lang_tag}`. A template is
-- arbitrary JavaScript text, so only the literal path token changes: the word
-- `lang_bcp47` appearing as plain output text, without the `${...}`
-- interpolation wrapper, is NOT a fact reference — it renders literally and
-- still compiles — and is deliberately left byte-identical. A document with no
-- retired token is not touched, so a row that never differed keeps its
-- version.
--
-- There is no data-preservation obligation before release; the rewrite exists
-- only so an upgraded instance keeps compiling the templates it stored while
-- the old fact name was live.

CREATE FUNCTION pg_temp.naming_template_text_053(p_text text) RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT replace(replace(p_text, '${sub.lang_bcp47}', '${sub.lang_tag}'),
                '${release.lang_bcp47}', '${release.lang_tag}')
$$;

-- Rewrites every template string inside one naming_rules document. The shape
-- is validated by the application on save, so this only walks the keys a
-- naming document owns: the three variants and the subtitle suffix.
CREATE FUNCTION pg_temp.naming_section_lang_tag_053(p_value jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
    result jsonb := p_value;
    variant text;
    field text;
BEGIN
    IF p_value IS NULL OR jsonb_typeof(p_value) <> 'object' THEN
        RETURN p_value;
    END IF;
    FOREACH variant IN ARRAY ARRAY['tv', 'movie', 'unknown'] LOOP
        IF jsonb_typeof(result -> variant) = 'object' THEN
            FOREACH field IN ARRAY ARRAY['directory_template', 'video_filename_template'] LOOP
                IF jsonb_typeof(result -> variant -> field) = 'string' THEN
                    result := jsonb_set(result, ARRAY[variant, field],
                        to_jsonb(pg_temp.naming_template_text_053(result -> variant ->> field)));
                END IF;
            END LOOP;
        END IF;
    END LOOP;
    IF jsonb_typeof(result -> 'subtitle_suffix_template') = 'string' THEN
        result := jsonb_set(result, ARRAY['subtitle_suffix_template'],
            to_jsonb(pg_temp.naming_template_text_053(result ->> 'subtitle_suffix_template')));
    END IF;
    RETURN result;
END;
$$;

-- The rewrite checks itself before it touches a row, so a pattern that does
-- not do what this header says fails the migration on an empty database too.
-- The second document proves an unrelated template — including one that
-- carries the word lang_bcp47 as plain output text — survives byte-identical.
DO $$
DECLARE
    got jsonb;
    untouched jsonb := jsonb_build_object(
        'language', 'js-naming-1',
        'tv', jsonb_build_object(
            'directory_template', '/library/${anime.kind}/${anime.title}',
            'video_filename_template', '${anime.title} lang_bcp47 cut.${file.ext}'),
        'movie', jsonb_build_object(
            'directory_template', '/library/movie',
            'video_filename_template', '${file.stem}.${file.ext}'),
        'unknown', jsonb_build_object(
            'directory_template', '/library/Extras',
            'video_filename_template', '${fn}'),
        'subtitle_suffix_template', '${sub.lang_source}');
BEGIN
    got := pg_temp.naming_section_lang_tag_053(jsonb_build_object(
        'language', 'js-naming-1',
        'tv', jsonb_build_object(
            'directory_template', '/library/${release.lang_bcp47}/${anime.title}',
            'video_filename_template', '${anime.title}.${file.ext}'),
        'movie', jsonb_build_object(
            'directory_template', '/library/movie',
            'video_filename_template', '${file.stem}.${file.ext}'),
        'unknown', jsonb_build_object(
            'directory_template', '/library/Extras ${sub.lang_bcp47}',
            'video_filename_template', '${fn}'),
        'subtitle_suffix_template', '${sub.lang_bcp47}'));
    IF got IS DISTINCT FROM jsonb_build_object(
        'language', 'js-naming-1',
        'tv', jsonb_build_object(
            'directory_template', '/library/${release.lang_tag}/${anime.title}',
            'video_filename_template', '${anime.title}.${file.ext}'),
        'movie', jsonb_build_object(
            'directory_template', '/library/movie',
            'video_filename_template', '${file.stem}.${file.ext}'),
        'unknown', jsonb_build_object(
            'directory_template', '/library/Extras ${sub.lang_tag}',
            'video_filename_template', '${fn}'),
        'subtitle_suffix_template', '${sub.lang_tag}') THEN
        RAISE EXCEPTION 'migration 053 lang_tag rewrite self-check failed: %', got;
    END IF;
    IF pg_temp.naming_section_lang_tag_053(untouched) IS DISTINCT FROM untouched THEN
        RAISE EXCEPTION 'migration 053 lang_tag rewrite touched an unrelated template: %',
            pg_temp.naming_section_lang_tag_053(untouched);
    END IF;
    IF pg_temp.naming_template_text_053(NULL) IS NOT NULL
       OR pg_temp.naming_template_text_053('plain lang_bcp47 text') IS DISTINCT FROM 'plain lang_bcp47 text'
       OR pg_temp.naming_template_text_053('${sub.lang_bcp47}') IS DISTINCT FROM '${sub.lang_tag}' THEN
        RAISE EXCEPTION 'migration 053 lang_tag text edge cases failed';
    END IF;
END
$$;

UPDATE global_config_sections
   SET value = pg_temp.naming_section_lang_tag_053(value), version = version + 1, updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_section_lang_tag_053(value);

UPDATE anime_config_overrides
   SET value = pg_temp.naming_section_lang_tag_053(value), updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_section_lang_tag_053(value);

-- Every non-default template in the library keeps its body here. The row is
-- rewritten only when a template actually changed, so a library without the
-- retired token keeps its version.
WITH rewritten AS (
    SELECT singleton,
           value AS current_value,
           jsonb_set(value, ARRAY['templates'],
               CASE WHEN jsonb_typeof(value -> 'templates') = 'array' THEN (
                   SELECT COALESCE(
                              jsonb_agg(
                                  CASE WHEN jsonb_typeof(item.template_item -> 'rules') = 'object'
                                       THEN jsonb_set(item.template_item, ARRAY['rules'],
                                            pg_temp.naming_section_lang_tag_053(item.template_item -> 'rules'))
                                       ELSE item.template_item END
                                  ORDER BY item.ordinal),
                              '[]'::jsonb)
                     FROM jsonb_array_elements(value -> 'templates')
                          WITH ORDINALITY AS item(template_item, ordinal))
               ELSE value -> 'templates' END) AS next_value
      FROM naming_template_library
)
UPDATE naming_template_library AS library
   SET value = rewritten.next_value,
       version = library.version + 1,
       updated_at = CURRENT_TIMESTAMP
  FROM rewritten
 WHERE library.singleton = rewritten.singleton
   AND rewritten.next_value IS DISTINCT FROM rewritten.current_value;

-- The frozen business of a live instance is rewritten together with its
-- digest, in one statement: the guard trigger on anime_business_configs checks
-- the new digest against the canonical hash of the new snapshot on every
-- UPDATE. The business version is the CAS token acquisition captures, so it
-- moves with the snapshot exactly as business_sync/business_apply move it. The
-- source subquery is keyed by managed_anime_id: one instance's snapshot is
-- never written onto another one.
UPDATE anime_business_configs AS config
   SET business_snapshot = rewritten.snapshot,
       business_digest = 'sha256:' || encode(digest(convert_to(
           anime_config_candidate_canonical_json(rewritten.snapshot), 'UTF8'), 'sha256'), 'hex'),
       business_version = config.business_version + 1,
       updated_at = CURRENT_TIMESTAMP
  FROM (
        SELECT managed_anime_id,
               jsonb_set(
                   business_snapshot,
                   ARRAY['sections', 'naming_rules'],
                   pg_temp.naming_section_lang_tag_053(business_snapshot -> 'sections' -> 'naming_rules')) AS snapshot
          FROM anime_business_configs
         WHERE business_snapshot IS NOT NULL
           AND jsonb_typeof(business_snapshot -> 'sections' -> 'naming_rules') = 'object'
           AND business_snapshot -> 'sections' -> 'naming_rules'
               IS DISTINCT FROM pg_temp.naming_section_lang_tag_053(business_snapshot -> 'sections' -> 'naming_rules')
       ) AS rewritten
 WHERE config.managed_anime_id = rewritten.managed_anime_id;

DROP FUNCTION pg_temp.naming_section_lang_tag_053(jsonb);
DROP FUNCTION pg_temp.naming_template_text_053(text);
