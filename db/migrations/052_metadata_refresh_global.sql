-- Forward-only migration 052: the metadata_refresh background sweep toggle.
--
-- metadata_refresh = {enabled} (schema metadata-refresh-v1) is a Global-only
-- setting edited only in the settings page through the generic Global section
-- API. It has no per-anime override and never enters the business snapshot.
-- When enabled, the periodic sweep re-pulls the bound Bangumi/TMDB works of
-- active subscriptions every 24 hours; the default is off because a changed
-- upstream TMDB title also changes what templates in use render. The table
-- check repeats the typed shape: exactly one boolean member.
--
-- Migrations carry no data-preservation obligation before release.

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'retention', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh')),
    ADD CONSTRAINT global_config_sections_metadata_refresh_check CHECK (
        section_name <> 'metadata_refresh' OR (
            schema_version = 'metadata-refresh-v1'
            AND public.business_exact_keys(value, ARRAY['enabled'])
            AND jsonb_typeof(value->'enabled') = 'boolean'
        ));
