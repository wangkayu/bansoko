-- Forward-only migration 119: the release-without-metadata switch.
--
-- metadata_hold = {allow_missing_linkage} (schema metadata-hold-v1) is a
-- Global-only setting edited only in the settings page through the generic
-- Global section API, like metadata_refresh. It has no per-anime override, no
-- draft field and never enters the business snapshot: every evaluation and
-- admission reads it live.
--
-- Off (the default) is the historical behavior: a subscription whose BGM or
-- TMDB binding is missing holds its releases as waiting_metadata. On, the
-- missing bindings stay a fact (a reader still names the absent link, and the
-- frozen input carries it) while the subscription is admitted and named from
-- its manual values only; one-shot acquisition bypasses the hold either
-- way. Turning the switch on re-enqueues the releases already waiting in the
-- same save transaction; turning it off never touches a release, only later
-- evaluations hold again.
--
-- Migrations carry no data-preservation obligation before release.

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'replacement', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh', 'metadata_hold', 'subscription_defaults')),
    ADD CONSTRAINT global_config_sections_metadata_hold_check CHECK (
        section_name <> 'metadata_hold' OR (
            schema_version = 'metadata-hold-v1'
            AND public.business_exact_keys(value, ARRAY['allow_missing_linkage'])
            AND jsonb_typeof(value->'allow_missing_linkage') = 'boolean'
        ));
