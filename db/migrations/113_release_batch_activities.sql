-- Release evaluation batches: one timeline row per bulk enqueue trigger.
-- A refresh that enqueues several release evaluations (a subscription's first
-- evaluation of the feed window, a sweep of re-evaluations) writes one
-- release_batch_processed head entry and stamps every release_processed
-- detail entry of that batch with batch_key. The primary event view hides the
-- detail rows; the batch head is their one timeline row.

ALTER TABLE subscription_log_entries
    ADD COLUMN batch_key TEXT;

ALTER TABLE subscription_log_entries
    ADD CONSTRAINT subscription_log_entries_batch_key_check
        CHECK (batch_key IS NULL OR char_length(btrim(batch_key)) BETWEEN 1 AND 128);

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'release_processed', 'release_batch_processed', 'download_changed', 'artifact_verified', 'import_waiting_config', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'cleanup_failed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled'));

CREATE INDEX subscription_log_entries_batch_idx
    ON subscription_log_entries (batch_key, occurred_at DESC, id)
    WHERE batch_key IS NOT NULL;

COMMENT ON COLUMN subscription_log_entries.batch_key IS 'The release_batch_processed head this detail row belongs to; NULL is a standalone timeline entry.';
