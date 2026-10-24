-- Forward-only migration 045: naming template values become the template body.
--
-- A naming template value is the body of one JavaScript template literal: the
-- text as it reads between the backticks, `${...}` interpolations included. The
-- engine supplies the enclosing backticks, so an owner never types them and a
-- folder name may carry parentheses and quotes as plain text (a literal
-- backslash is still a JavaScript escape: `\\`). Before this migration the
-- stored value WAS the full literal, backticks included.
--
-- Rows written in the old form keep rendering: the engine recognises a value
-- that is one complete template literal and compiles it as written. This
-- migration only removes the redundant wrapper from the values an owner edits,
-- so the editor, the settings summary and the HTTP/MCP surfaces show the body
-- and the two forms do not coexist in editable configuration.
--
-- What is rewritten (owner-owned, still editable):
--   * global_config_sections.naming_rules — the default template body;
--   * naming_template_library.value — every non-default template body;
--   * anime_config_overrides.naming_rules — every instance's own copy;
--   * anime_business_configs.business_snapshot — the frozen business of every
--     live instance (its sections.naming_rules plus its digest, recomputed by
--     the same canonical function the trigger checks).
--
-- What is deliberately NOT rewritten: candidate drafts and revisions, one-shot
-- input snapshots, download_tasks.frozen_policy and every file_operations /
-- acquisition snapshot. Those are identity-bearing frozen evidence (their
-- digests are the identity), the engine renders both forms, and 025 already
-- states the rule. A frozen row keeps the bytes it was admitted with.
--
-- The rewrite is value-exact: only a string that starts and ends with a
-- backtick and contains no unescaped backtick of its own is unwrapped. Text
-- that merely starts with a backtick is left alone, and so is a body that
-- somehow already is one.
--
-- There is no data-preservation obligation before release; the rewrite exists
-- only so an upgraded instance shows the same form a fresh one does.

CREATE FUNCTION pg_temp.naming_body_045(p_text text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
    body text;
BEGIN
    IF p_text IS NULL OR length(p_text) < 2 THEN
        RETURN p_text;
    END IF;
    IF left(p_text, 1) <> '`' OR right(p_text, 1) <> '`' THEN
        RETURN p_text;
    END IF;
    body := substr(p_text, 2, length(p_text) - 2);
    -- A backtick inside the body means the value was not one simple literal
    -- (it was escaped or the value merely reads like Markdown code). Leave it.
    IF strpos(body, '`') > 0 THEN
        RETURN p_text;
    END IF;
    RETURN body;
END;
$$;

-- Rewrites every template string inside one naming_rules document. The shape is
-- validated by the application on save, so this only walks the keys a naming
-- document owns: the three variants and the subtitle suffix.
CREATE FUNCTION pg_temp.naming_section_body_045(p_value jsonb) RETURNS jsonb
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
                        to_jsonb(pg_temp.naming_body_045(result -> variant ->> field)));
                END IF;
            END LOOP;
        END IF;
    END LOOP;
    IF jsonb_typeof(result -> 'subtitle_suffix_template') = 'string' THEN
        result := jsonb_set(result, ARRAY['subtitle_suffix_template'],
            to_jsonb(pg_temp.naming_body_045(result ->> 'subtitle_suffix_template')));
    END IF;
    RETURN result;
END;
$$;

-- The rewrite checks itself before it touches a row, so a pattern that does not
-- do what this header says fails the migration on an empty database too.
DO $$
DECLARE
    got jsonb;
BEGIN
    got := pg_temp.naming_section_body_045(jsonb_build_object(
        'language', 'js-naming-1',
        'tv', jsonb_build_object(
            'directory_template', '`/library/${anime.kind}/${anime.title}`',
            'video_filename_template', '`${anime.title}.${file.ext}`'),
        'movie', jsonb_build_object(
            'directory_template', '/library/${anime.kind}',
            'video_filename_template', '`${file.stem}.${file.ext}`'),
        'unknown', jsonb_build_object(
            'directory_template', '`/library/Show (2024)`',
            'video_filename_template', '`a`b`'),
        'subtitle_suffix_template', '`${sub.lang_bcp47}`'));
    IF got IS DISTINCT FROM jsonb_build_object(
        'language', 'js-naming-1',
        'tv', jsonb_build_object(
            'directory_template', '/library/${anime.kind}/${anime.title}',
            'video_filename_template', '${anime.title}.${file.ext}'),
        'movie', jsonb_build_object(
            'directory_template', '/library/${anime.kind}',
            'video_filename_template', '${file.stem}.${file.ext}'),
        'unknown', jsonb_build_object(
            'directory_template', '/library/Show (2024)',
            'video_filename_template', '`a`b`'),
        'subtitle_suffix_template', '${sub.lang_bcp47}') THEN
        RAISE EXCEPTION 'migration 045 naming body rewrite self-check failed: %', got;
    END IF;
    IF pg_temp.naming_body_045(NULL) IS NOT NULL
       OR pg_temp.naming_body_045('`') IS DISTINCT FROM '`'
       OR pg_temp.naming_body_045('plain') IS DISTINCT FROM 'plain'
       OR pg_temp.naming_body_045('`unclosed') IS DISTINCT FROM '`unclosed' THEN
        RAISE EXCEPTION 'migration 045 naming body edge cases failed';
    END IF;
END
$$;

UPDATE global_config_sections
   SET value = pg_temp.naming_section_body_045(value), version = version + 1, updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_section_body_045(value);

UPDATE anime_config_overrides
   SET value = pg_temp.naming_section_body_045(value), updated_at = CURRENT_TIMESTAMP
 WHERE section_name = 'naming_rules'
   AND value IS NOT NULL
   AND value IS DISTINCT FROM pg_temp.naming_section_body_045(value);

-- Every non-default template in the library keeps its body here. The row is
-- rewritten only when a template actually changed, so an already-body library
-- keeps its version.
WITH rewritten AS (
    SELECT singleton,
           value AS current_value,
           jsonb_set(value, ARRAY['templates'],
               CASE WHEN jsonb_typeof(value -> 'templates') = 'array' THEN (
                   SELECT COALESCE(
                              jsonb_agg(
                                  CASE WHEN jsonb_typeof(item.template_item -> 'rules') = 'object'
                                       THEN jsonb_set(item.template_item, ARRAY['rules'],
                                            pg_temp.naming_section_body_045(item.template_item -> 'rules'))
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

-- The frozen business of a live instance is rewritten together with its digest,
-- in one statement: the guard trigger on anime_business_configs checks the new
-- digest against the canonical hash of the new snapshot on every UPDATE. The
-- business version is the CAS token acquisition captures, so it moves with the
-- snapshot exactly as business_sync/business_apply move it. The source subquery
-- is keyed by managed_anime_id: one instance's snapshot is never written onto
-- another one.
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
                   pg_temp.naming_section_body_045(business_snapshot -> 'sections' -> 'naming_rules')) AS snapshot
          FROM anime_business_configs
         WHERE business_snapshot IS NOT NULL
           AND jsonb_typeof(business_snapshot -> 'sections' -> 'naming_rules') = 'object'
           AND business_snapshot -> 'sections' -> 'naming_rules'
               IS DISTINCT FROM pg_temp.naming_section_body_045(business_snapshot -> 'sections' -> 'naming_rules')
       ) AS rewritten
 WHERE config.managed_anime_id = rewritten.managed_anime_id;

DROP FUNCTION pg_temp.naming_section_body_045(jsonb);
DROP FUNCTION pg_temp.naming_body_045(text);
