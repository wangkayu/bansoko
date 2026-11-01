-- Forward-only migration 110: the replacement and displacement notifications
-- (owner ruling 2026-11-01).
--
-- Two workflow triggers join the closed registry:
--   * replacement_applied — one settled replace decision took over the versions
--     it named; its source owner is the decision;
--   * file_displaced — one managed file lost its place to another file; its
--     source owner is the victim placement.
-- The delivery owner CHECK gains both owner types, every trigger-kind mirror
-- gains both triggers, and is_valid_plugin_trigger_kinds accepts them in a
-- target's list. CREATE OR REPLACE keeps the function's privileges.

ALTER TABLE notification_admissions DROP CONSTRAINT notification_admissions_trigger_kind_check;
ALTER TABLE notification_admissions ADD CONSTRAINT notification_admissions_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed', 'replacement_applied', 'file_displaced'));

ALTER TABLE plugin_deliveries DROP CONSTRAINT plugin_deliveries_trigger_kind_check;
ALTER TABLE plugin_deliveries ADD CONSTRAINT plugin_deliveries_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed', 'replacement_applied', 'file_displaced'));

ALTER TABLE plugin_deliveries DROP CONSTRAINT plugin_deliveries_source_owner_type_check;
ALTER TABLE plugin_deliveries ADD CONSTRAINT plugin_deliveries_source_owner_type_check CHECK (source_owner_type IN ('download_task', 'file_operation', 'plugin_target', 'release', 'subscription', 'acquisition', 'replacement_deletion', 'replacement_decision', 'placement'));

ALTER TABLE idempotency_records DROP CONSTRAINT idempotency_records_trigger_kind_check;
ALTER TABLE idempotency_records ADD CONSTRAINT idempotency_records_trigger_kind_check CHECK (trigger_kind IS NULL OR trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed', 'replacement_applied', 'file_displaced'));

CREATE OR REPLACE FUNCTION is_valid_plugin_trigger_kinds(value jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE
        WHEN jsonb_typeof(value) <> 'array' OR pg_column_size(value) > 16384 THEN FALSE
        ELSE NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements(value) AS item
            WHERE jsonb_typeof(item) <> 'string'
        )
        AND NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements_text(value) AS item(value)
            WHERE item.value NOT IN (
                'download_submitted', 'file_operation_completed', 'file_operation_failed',
                'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported',
                'replacement_deletion_failed', 'replacement_applied', 'file_displaced')
        )
        AND jsonb_array_length(value) = (
            SELECT COUNT(DISTINCT item.value)
            FROM jsonb_array_elements_text(value) AS item(value)
        )
    END;
$$;
