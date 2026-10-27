-- Forward-only migration 083: the replacement_deletion_failed notification
-- trigger (owner ruling 2026-10-27).
--
-- An old managed file a replacement decided to delete that stayed busy or not
-- permitted until the automatic retries ran out admits one notice per deletion
-- obligation. The trigger joins the closed registry and its source owner type
-- replacement_deletion joins the delivery owner types:
--   * notification_admissions, plugin_deliveries and idempotency_records gain
--     the trigger in their trigger_kind CHECK;
--   * plugin_deliveries gains replacement_deletion in its source owner CHECK;
--   * is_valid_plugin_trigger_kinds accepts the trigger in a target's list.
-- CREATE OR REPLACE keeps the function's privileges.

ALTER TABLE notification_admissions DROP CONSTRAINT notification_admissions_trigger_kind_check;
ALTER TABLE notification_admissions ADD CONSTRAINT notification_admissions_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed'));

ALTER TABLE plugin_deliveries DROP CONSTRAINT plugin_deliveries_trigger_kind_check;
ALTER TABLE plugin_deliveries ADD CONSTRAINT plugin_deliveries_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed'));

ALTER TABLE plugin_deliveries DROP CONSTRAINT plugin_deliveries_source_owner_type_check;
ALTER TABLE plugin_deliveries ADD CONSTRAINT plugin_deliveries_source_owner_type_check CHECK (source_owner_type IN ('download_task', 'file_operation', 'plugin_target', 'release', 'subscription', 'acquisition', 'replacement_deletion'));

ALTER TABLE idempotency_records DROP CONSTRAINT idempotency_records_trigger_kind_check;
ALTER TABLE idempotency_records ADD CONSTRAINT idempotency_records_trigger_kind_check CHECK (trigger_kind IS NULL OR trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported', 'replacement_deletion_failed'));

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
                'replacement_deletion_failed')
        )
        AND jsonb_array_length(value) = (
            SELECT COUNT(DISTINCT item.value)
            FROM jsonb_array_elements_text(value) AS item(value)
        )
    END;
$$;
