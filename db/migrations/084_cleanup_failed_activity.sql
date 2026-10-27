-- Forward-only migration 084: the cleanup_failed Activity kind.
--
-- A replacement that decided to delete an old managed file but could not
-- (the file stayed busy or the change was not permitted, and the automatic
-- retries ran out) records one business Activity entry of this kind with
-- outcome failure. The kind CHECK is redefined with the full list from
-- migration 021 plus cleanup_failed.

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'rss_refresh_failed', 'release_processed', 'download_changed', 'artifact_verified', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'cleanup_failed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled'));
