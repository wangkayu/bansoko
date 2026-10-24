-- Forward-only migration 034: download_completion becomes one Global setting
-- with optional seed limits.
--
-- The setting is edited only in the settings page: no anime may hold a
-- download_completion override any more, so stored overrides are deleted and
-- the section leaves the override allowlist. The Global row moves to schema
-- download-completion-v2, whose value is a superset of v1 ({action} stays
-- valid; seed may carry seed_limits).
--
-- A completion row freezes the seed action's limits next to the action. A seed
-- row with limits is pending until the limits are written to qBittorrent; a
-- seed row without limits is still written already succeeded.

DELETE FROM anime_config_overrides WHERE section_name = 'download_completion';

ALTER TABLE anime_config_overrides
    DROP CONSTRAINT anime_config_overrides_section_name_check,
    ADD CONSTRAINT anime_config_overrides_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters'));

UPDATE global_config_sections
SET schema_version = 'download-completion-v2',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'download_completion'
  AND schema_version = 'download-completion-v1';

ALTER TABLE download_completions
    ADD COLUMN seed_limits JSONB;

ALTER TABLE download_completions
    DROP CONSTRAINT download_completions_seed_check,
    ADD CONSTRAINT download_completions_seed_check CHECK (action <> 'seed' OR seed_limits IS NOT NULL OR state = 'succeeded'),
    ADD CONSTRAINT download_completions_seed_limits_check CHECK (
        seed_limits IS NULL OR (
            action = 'seed'
            AND jsonb_typeof(seed_limits) = 'object'
            AND (seed_limits->>'at_limit' IN ('stop', 'remove', 'remove_with_data')) IS TRUE
            AND (seed_limits ? 'seeding_time_minutes' OR seed_limits ? 'ratio')
            AND pg_column_size(seed_limits) <= 1024
        ));

COMMENT ON COLUMN download_completions.seed_limits IS 'Frozen share limits of the seed action ({seeding_time_minutes?, ratio?, at_limit}); NULL seeds without a limit.';

CREATE OR REPLACE FUNCTION download_completions_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF OLD.state = 'succeeded' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_TERMINAL';
    END IF;
    IF (NEW.id, NEW.acquisition_id, NEW.action, NEW.info_hash, NEW.provider_ref, NEW.remote_root,
        NEW.application_root, NEW.seed_limits, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.acquisition_id, OLD.action, OLD.info_hash, OLD.provider_ref, OLD.remote_root,
        OLD.application_root, OLD.seed_limits, OLD.created_at) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_FROZEN';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_VERSION';
    END IF;
    IF OLD.state = 'failed' AND NEW.state <> 'pending' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_TERMINAL';
    END IF;
    RETURN NEW;
END;
$$;
