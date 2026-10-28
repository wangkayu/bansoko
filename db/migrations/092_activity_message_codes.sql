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
--
-- A replace decision staled by its candidate root's failure or cancellation
-- names that root (replacement_decisions.stale_acquisition_id), so the
-- consumer writes its not_replaced line by that link.

DELETE FROM subscription_log_entries;

ALTER TABLE subscription_log_entries DROP COLUMN title;
ALTER TABLE subscription_log_entries DROP COLUMN summary;

ALTER TABLE subscription_log_entries ADD COLUMN message_code TEXT NOT NULL;
ALTER TABLE subscription_log_entries ADD COLUMN message_params JSONB NOT NULL;

-- The catalog is closed in the writer; the column bounds the code's shape and
-- keeps the params a small object of scalars. The path is strict: lax mode
-- would unwrap an array member before the filter and see only its elements.
-- The CASE keeps the strict member accessor off a non-object value.
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_message_code_check
    CHECK (message_code ~ '^[a-z][a-z0-9_]{0,127}$');
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_message_params_check
    CHECK (CASE WHEN jsonb_typeof(message_params) = 'object'
        THEN pg_column_size(message_params) <= 4096
            AND NOT jsonb_path_exists(message_params, 'strict $.* ? (@.type() == "object" || @.type() == "array")')
        ELSE FALSE END);

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

-- A replace decision staled by its candidate root's failure or cancellation
-- names that root. The activity consumer writes the decision's not_replaced
-- line in the transaction of the root's settlement entry and finds the
-- decisions by this link alone. The other stale paths (settlement
-- revalidation, the stand-alone stale, the scan claim) leave it NULL; they
-- write their superseded line themselves. Acquisition roots are never
-- deleted (they compact in place), so the reference is a plain foreign key;
-- the trigger sets it while updating that same root, whose row it already
-- holds.
ALTER TABLE replacement_decisions ADD COLUMN stale_acquisition_id TEXT;
ALTER TABLE replacement_decisions ADD CONSTRAINT replacement_decisions_stale_acquisition_fk
    FOREIGN KEY (stale_acquisition_id) REFERENCES acquisitions (id);
CREATE INDEX replacement_decisions_stale_acquisition_idx ON replacement_decisions (stale_acquisition_id)
    WHERE stale_acquisition_id IS NOT NULL;

-- Same body as the baseline definition (035 only rebound the trigger to
-- fire on cancelled too), plus the link to the root.
CREATE OR REPLACE FUNCTION stale_failed_candidate_decisions() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NEW.source_release_ref IS NULL OR NEW.origin_managed_anime_ref IS NULL THEN
        RETURN NEW;
    END IF;
    UPDATE public.replacement_decisions d
    SET state = 'stale',
        stale_reason = 'candidate_acquisition_failed',
        stale_acquisition_id = NEW.id,
        version = d.version + 1,
        updated_at = CURRENT_TIMESTAMP
    FROM public.managed_anime lc
    WHERE lc.id = d.managed_anime_id
      AND lc.lifecycle_state <> 'retired'
      AND d.managed_anime_id = NEW.origin_managed_anime_ref
      AND d.candidate_release_id = NEW.source_release_ref
      AND d.candidate_placement_id IS NULL
      AND d.state = 'admitted'
      -- A key the release already placed (verified, active) keeps its
      -- decision: the evaluation binds it to that placement and settles it.
      AND NOT EXISTS (
          SELECT 1
          FROM public.placements p
          JOIN public.placement_coverage pc ON pc.placement_id = p.id
          WHERE p.managed_anime_id = d.managed_anime_id
            AND p.quality_facts_ref = d.candidate_release_id
            AND p.source_evidence_type = 'file_operation'
            AND p.state = 'active'
            AND p.verification_state = 'verified'
            AND pc.observation_key = d.coverage_key
      )
      AND NOT EXISTS (
          SELECT 1 FROM public.acquisitions r
          WHERE r.source_release_ref = NEW.source_release_ref
            AND r.origin_managed_anime_ref = NEW.origin_managed_anime_ref
            AND r.state = 'running'
            AND r.id <> NEW.id
      );
    RETURN NEW;
END;
$$;
