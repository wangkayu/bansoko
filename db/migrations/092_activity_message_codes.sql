-- Forward-only migration 092: business journal entries carry a message code.
--
-- An entry no longer stores display text. message_code names its message in
-- the closed catalog the Go writer registers per kind
-- (internal/observability/port/activity_messages.go, published to the
-- frontend as a generated artifact), and message_params holds the scalar
-- values the message interpolates. The entries written before this migration
-- carry only text, so they are deleted: every environment is a test
-- environment before the first release, and the journal restarts here.
--
-- rss_refresh_failed is retired as a kind: a refresh failure is a diagnostic
-- condition, not a journal entry.
--
-- One-shot acquisitions are journalled like subscription ones (their
-- temporary anime and its oneshot-origin subscription are the scope), so the
-- consumer's unlogged-settlement index no longer excludes them.

DELETE FROM subscription_log_entries;

ALTER TABLE subscription_log_entries DROP COLUMN title;
ALTER TABLE subscription_log_entries DROP COLUMN summary;

ALTER TABLE subscription_log_entries ADD COLUMN message_code TEXT NOT NULL;
ALTER TABLE subscription_log_entries ADD COLUMN message_params JSONB NOT NULL;

-- The catalog is closed in the writer; the column bounds the code's shape and
-- keeps the params a small object of scalars.
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_message_code_check
    CHECK (message_code ~ '^[a-z][a-z0-9_]{0,127}$');
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_message_params_check
    CHECK (jsonb_typeof(message_params) = 'object'
        AND pg_column_size(message_params) <= 4096
        AND NOT jsonb_path_exists(message_params, '$.* ? (@.type() == "object" || @.type() == "array")'));

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'release_processed', 'download_changed', 'artifact_verified', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'cleanup_failed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled'));

-- The records feed counts failed settlements for the navigation badge:
-- kind + outcome, newest first.
CREATE INDEX subscription_log_entries_kind_outcome_idx
    ON subscription_log_entries (kind, outcome, occurred_at DESC, id DESC);

-- Run state and search read the other entries of the same Release in time
-- order (the settlement that closes a run, the steps inside it).
CREATE INDEX subscription_log_entries_release_run_idx
    ON subscription_log_entries (release_id, kind, occurred_at)
    WHERE release_id IS NOT NULL;

-- The badge reads failed and unconfirmed deliveries, newest first.
CREATE INDEX plugin_deliveries_failure_record_idx
    ON plugin_deliveries (created_at DESC, id DESC)
    WHERE state IN ('failed', 'unknown');

DROP INDEX acquisitions_activity_unlogged_idx;
CREATE INDEX acquisitions_activity_unlogged_idx ON acquisitions (terminal_at, id)
    WHERE terminal_at IS NOT NULL AND activity_logged_at IS NULL;
