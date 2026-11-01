-- Forward-only migration 110: the ignore-multi-video option gains a Global
-- default.
--
-- subscription_defaults = {ignore_multi_video_releases} (schema
-- subscription-defaults-v1) is edited only in the settings page through the
-- generic Global section API. It is a one-time-input default exactly like
-- tag_filters: a new draft copies it once when the draft is created, the
-- subscription freezes the copied value at subscribe, and a later edit never
-- reaches an existing draft, subscription or one-shot acquisition (the
-- evaluation gate reads only the subscription's own column). A missing row is
-- the code default (false).

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'replacement', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh', 'subscription_defaults')),
    ADD CONSTRAINT global_config_sections_subscription_defaults_check CHECK (
        section_name <> 'subscription_defaults' OR (
            schema_version = 'subscription-defaults-v1'
            AND public.business_exact_keys(value, ARRAY['ignore_multi_video_releases'])
            AND jsonb_typeof(value->'ignore_multi_video_releases') = 'boolean'
        ));
