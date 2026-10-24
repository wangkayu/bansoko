-- Forward-only migration 037: the download timeouts become one Global setting.
--
-- download_timeouts = {stall_seconds, download_wait_seconds} (schema
-- download-timeouts-v1) is edited only in the settings page through the
-- generic Global section API; no anime may hold an override (the
-- anime_config_overrides allowlist does not name it). Acceptance reads it once
-- and freezes both values into the acquisition input limits; the download
-- task budget (download_task_budget_guard) keeps reading only those frozen
-- limits, so a later edit never changes an accepted download. A missing row is
-- the code default (6 hours / 7 days). It replaces the retired
-- BANSOKO_DOWNLOAD_STALL_SECONDS deployment variable and the fixed 7-day wait.
--
-- Bounds mirror internal/anime/domain/download_timeouts.go: stall 10 minutes
-- to 7 days, wait 1 hour to 30 days (LimitsV1 maximum), stall <= wait (the
-- download_tasks budget shape).

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'retention', 'parser_policy', 'file_effect_policy', 'download_timeouts')),
    ADD CONSTRAINT global_config_sections_download_timeouts_check CHECK (
        section_name <> 'download_timeouts' OR (
            schema_version = 'download-timeouts-v1'
            AND public.business_exact_keys(value, ARRAY['stall_seconds', 'download_wait_seconds'])
            AND (CASE
                WHEN jsonb_typeof(value->'stall_seconds') = 'number'
                     AND jsonb_typeof(value->'download_wait_seconds') = 'number'
                     AND value->>'stall_seconds' ~ '^[0-9]{1,9}$'
                     AND value->>'download_wait_seconds' ~ '^[0-9]{1,9}$'
                THEN (value->>'stall_seconds')::bigint BETWEEN 600 AND 604800
                     AND (value->>'download_wait_seconds')::bigint BETWEEN 3600 AND 2592000
                     AND (value->>'stall_seconds')::bigint <= (value->>'download_wait_seconds')::bigint
                ELSE FALSE
            END)
        ));
