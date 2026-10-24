-- Forward-only migration 018: tag_filters rows become additive.
--
-- A tag_filters row was {mode: whitelist|blacklist, tags}. It is now
-- {require, exclude}: every require tag must be present, any exclude tag
-- present filters the release. Stored sections (the Global default and anime
-- overrides) are converted in place, whitelist -> require and blacklist ->
-- exclude, and move to schema tag-filters-v2. A value that is not the old
-- shape is left untouched.

CREATE FUNCTION pg_temp.tag_filter_row_v2(row_value JSONB) RETURNS JSONB
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN row_value->>'mode' = 'blacklist' THEN
            jsonb_build_object('require', '[]'::jsonb, 'exclude', COALESCE(row_value->'tags', '[]'::jsonb))
        ELSE
            jsonb_build_object('require', COALESCE(row_value->'tags', '[]'::jsonb), 'exclude', '[]'::jsonb)
    END
$$;

UPDATE global_config_sections
SET value = jsonb_build_object(
        'anibt', pg_temp.tag_filter_row_v2(value->'anibt'),
        'title', pg_temp.tag_filter_row_v2(value->'title')),
    schema_version = 'tag-filters-v2',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'tag_filters'
  AND value IS NOT NULL
  AND value->'anibt' ? 'mode';

UPDATE anime_config_overrides
SET value = jsonb_build_object(
        'anibt', pg_temp.tag_filter_row_v2(value->'anibt'),
        'title', pg_temp.tag_filter_row_v2(value->'title')),
    schema_version = 'tag-filters-v2',
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'tag_filters'
  AND value IS NOT NULL
  AND value->'anibt' ? 'mode';

DROP FUNCTION pg_temp.tag_filter_row_v2(JSONB);
