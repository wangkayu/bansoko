-- Global operation history survives removal of the subscription and its log.
ALTER TABLE subscription_log_entries
    ALTER COLUMN subscription_id DROP NOT NULL,
    ALTER COLUMN managed_anime_id DROP NOT NULL,
    ADD COLUMN subject_display TEXT,
    ADD CONSTRAINT subscription_log_entries_scope_check CHECK (
        (kind IN ('subscription_retired', 'local_records_cleaned', 'qb_task_deleted')
            AND subscription_id IS NULL AND managed_anime_id IS NULL
            AND rss_source_id IS NULL AND release_id IS NULL AND batch_key IS NULL
            AND (subject_display IS NULL OR char_length(btrim(subject_display)) BETWEEN 1 AND 512))
        OR
        (kind NOT IN ('subscription_retired', 'local_records_cleaned', 'qb_task_deleted')
            AND subscription_id IS NOT NULL AND managed_anime_id IS NOT NULL AND subject_display IS NULL)
    );

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN (
    'subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed',
    'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'release_processed',
    'release_batch_processed', 'download_changed', 'artifact_verified', 'import_waiting_config',
    'placement_changed', 'replacement_decided', 'cleanup_completed', 'cleanup_failed',
    'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled',
    'subscription_retired', 'local_records_cleaned', 'qb_task_deleted'
));

CREATE UNIQUE INDEX subscription_log_entries_global_dedupe_uq
    ON subscription_log_entries (dedupe_key) WHERE subscription_id IS NULL;
