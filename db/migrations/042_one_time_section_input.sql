-- Forward-only migration 042: every per-anime section is one-time input, and
-- reltitle is the default parser.
--
-- Global sections are only the defaults a new draft copies when it is created;
-- the draft freezes them at subscribe and the instance stores its own complete
-- copy. Nothing reads Global at run time for a per-anime section any more, so
-- a NULL ("inherit") per-anime row and a missing per-anime row are both
-- invalid from now on. Global-only settings (download_completion,
-- download_timeouts) are untouched: they have no per-anime copy and are frozen
-- per acquisition at acceptance.
--
-- Migrations carry no data-preservation obligation before release.

-- 1. reltitle is the default parser.
--
-- The global parser_policy row is the creation template every new instance
-- (and every new draft's business snapshot) copies its parser kind from. The
-- baseline seeded it with the pre-rename regex kind (renamed by 012); this
-- moves the live row to reltitle. regex stays registered and selectable, and
-- the value CHECK from 020 already admits both kinds. Existing instances keep
-- the parser they froze at subscribe; candidate revisions keep their frozen
-- envelope. The version is bumped with the value so a save holding the older
-- expected version loses its race. A database without the row needs nothing:
-- the code default (parserpolicy.DefaultPolicy) is reltitle too.

UPDATE global_config_sections
SET value = jsonb_set(value, '{parser_kind}', '"reltitle"'::jsonb),
    version = version + 1,
    updated_at = NOW()
WHERE section_name = 'parser_policy'
  AND value->>'parser_kind' IS DISTINCT FROM 'reltitle';

-- 2. Drop candidates that were never activated.
--
-- Their drafts may hold the retired inherit envelopes, which no longer decode.
-- A candidate is kept when it is linked to a subscription (dormant) or when a
-- subscription's frozen business still references one of its revisions (an
-- archived candidate of a retired instance): those activated revisions stay
-- exactly as they are. Revisions go with their candidate (ON DELETE CASCADE).
-- The delete guard requires explicit cleanup authority, which this migration
-- is; it is disabled only around this statement.

ALTER TABLE anime_config_candidates DISABLE TRIGGER anime_config_candidate_delete_guard_trg;

DELETE FROM anime_config_candidates AS c
WHERE c.activated_managed_anime_id IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM anime_business_configs AS b WHERE b.candidate_id = c.id);

ALTER TABLE anime_config_candidates ENABLE TRIGGER anime_config_candidate_delete_guard_trg;

-- 3. Materialize every instance's own section values.
--
-- An instance row that inherited (value NULL) or never had a row takes the
-- value it was effectively using: the current Global section when one is
-- saved with the current schema, otherwise the code default
-- (animedomain.DefaultSectionValue). The retired subtitle_rules rows go.

DELETE FROM anime_config_overrides WHERE section_name = 'subtitle_rules';

WITH d (section_name, owner_type, schema_version, value) AS (VALUES
('subgroups', 'subgroups', 'subgroups-v1', '{"entries":[],"max_rss":3}'::jsonb),
('parser', 'rules', 'parser-section-v1', $json${"episode_patterns":["\\s-\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\\s._\\-\\]】])","S[0-9]{1,2}\\s*E0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?","(?:EP?|Episode|第)\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:\\s*[话話集])?","[\\[【(]\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?\\s*[\\]】)]","(?:^|[\\s._-])0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\\s._\\-\\]】)])"]}$json$::jsonb),
('release_rules', 'rules', 'release-rules-v1', '{"must_contain":"","not_in":""}'::jsonb),
('file_rules', 'rules', 'file-rules-v1', '{"must_contain":"","not_in":""}'::jsonb),
('naming_rules', 'rules', 'naming-rules-v1', $json${"language":"js-naming-1","movie":{"directory_template":"`/library/${anime.kind}/${anime.title} ${anime.year}`","video_filename_template":"`${file.stem}.${file.ext}`"},"unknown":{"directory_template":"`/library/${anime.kind}/${anime.title} ${anime.year}/Extras`","video_filename_template":"`${file.stem}.${file.ext}`"},"subtitle_suffix_template":"`${sub.lang_bcp47}`","tv":{"directory_template":"`/library/${anime.kind}/${anime.title} ${anime.year}${anime.season_path}`","video_filename_template":"`${anime.title} ${ep.s00e00}.${file.ext}`"}}$json$::jsonb),
('file_effect', 'rules', 'file-effect-v1', '{"collision_policy":"fail","operation":"copy"}'::jsonb),
('tag_filters', 'rules', 'tag-filters-v2', '{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}'::jsonb))
INSERT INTO anime_config_overrides (managed_anime_id, owner_type, section_name, schema_version, value)
SELECT b.managed_anime_id, d.owner_type, d.section_name, d.schema_version,
       COALESCE(g.value, d.value)
FROM anime_business_configs AS b
CROSS JOIN d
LEFT JOIN global_config_sections AS g
  ON g.section_name = d.section_name
 AND g.schema_version = d.schema_version
 AND g.value IS NOT NULL
 AND g.value <> 'null'::jsonb
ON CONFLICT (managed_anime_id, section_name) DO UPDATE
SET schema_version = EXCLUDED.schema_version,
    value = EXCLUDED.value,
    updated_at = NOW()
WHERE anime_config_overrides.value IS NULL;

ALTER TABLE anime_config_overrides
    ALTER COLUMN schema_version SET NOT NULL,
    ALTER COLUMN value SET NOT NULL,
    DROP CONSTRAINT anime_config_overrides_check1,
    ADD CONSTRAINT anime_config_overrides_check1 CHECK (value <> 'null'::jsonb),
    DROP CONSTRAINT anime_config_overrides_schema_version_check,
    ADD CONSTRAINT anime_config_overrides_schema_version_check CHECK (char_length(btrim(schema_version)) BETWEEN 1 AND 128),
    DROP CONSTRAINT anime_config_overrides_section_name_check,
    ADD CONSTRAINT anime_config_overrides_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'naming_rules', 'file_effect', 'tag_filters')),
    DROP CONSTRAINT anime_config_overrides_value_check,
    ADD CONSTRAINT anime_config_overrides_value_check CHECK (pg_column_size(value) <= 1048576);

-- 4. Fill the business axes an instance never froze.
--
-- Same rule: the current Global creation default, else the code default
-- (parser reltitle; retention all + default quality profile + one-week
-- quarantine cleanup; unclassified files skipped). Each filled axis starts at
-- owner version 1 with the Global version it copied as provenance (0 for a
-- file-effect default that was never saved, as the creation port records).

UPDATE anime_business_configs AS b
SET parser_kind = COALESCE(g.value->>'parser_kind', 'reltitle'),
    parser_source_default_version = COALESCE(g.version, 1),
    parser_version = 1,
    updated_at = NOW()
FROM (SELECT 1) AS one
LEFT JOIN global_config_sections AS g ON g.section_name = 'parser_policy'
WHERE b.parser_version IS NULL;

UPDATE anime_business_configs AS b
SET admission_mode = COALESCE(g.value->>'admission_mode', 'all'),
    quality_profile = COALESCE(g.value->'quality_profile',
        '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb),
    cleanup_policy = COALESCE(g.value->'cleanup_policy',
        '{"version":"cleanup-v1","effect":"quarantine","grace_seconds":604800}'::jsonb),
    retention_source_default_version = COALESCE(g.version, 1),
    retention_version = 1,
    updated_at = NOW()
FROM (SELECT 1) AS one
LEFT JOIN global_config_sections AS g ON g.section_name = 'retention'
WHERE b.retention_version IS NULL;

UPDATE anime_business_configs AS b
SET unclassified_route = COALESCE(g.value->>'unclassified_route', 'skip'),
    file_effect_source_default_version = COALESCE(g.version, 0),
    file_effect_version = 1,
    updated_at = NOW()
FROM (SELECT 1) AS one
LEFT JOIN global_config_sections AS g ON g.section_name = 'file_effect_policy'
WHERE b.file_effect_version IS NULL;
