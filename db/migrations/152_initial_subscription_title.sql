-- Existing subscriptions (including those not started yet) keep their titles.
ALTER TABLE managed_anime ADD COLUMN initial_title_pending boolean NOT NULL DEFAULT false;

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'replacement', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh', 'metadata_hold', 'subscription_defaults', 'subscription_title')),
    ADD CONSTRAINT global_config_sections_subscription_title_check CHECK (
        section_name <> 'subscription_title' OR (
            schema_version = 'subscription-title-v1'
            AND public.business_exact_keys(value, ARRAY['source'])
            AND jsonb_typeof(value->'source') = 'string'
            AND value->>'source' IN ('keep', 'tmdb_cn', 'tmdb_original', 'bgm_cn', 'bgm')
        ));
