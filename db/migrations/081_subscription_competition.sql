-- Forward-only migration 081: one competition per subscription instance
-- (owner rulings 2026-10-27).
--
-- Release evaluation no longer admits a download itself. An evaluated release
-- that passes the seed filter, the torrent, the file rules and the batch plan
-- is recorded as awaiting_competition with its plan frozen; one
-- subscription_competition work item per subscription instance then decides,
-- under the subscription row lock, which waiting release takes which episode
-- (against the managed files and the running downloads of that instance
-- only), and runs the admission of every release it decided.
--
-- 1. releases.verdict gains awaiting_competition (never with a download task).
-- 2. The replacement_fallback kind merges into the competition: the file
--    maintenance owner's kind becomes subscription_competition, one live item
--    per managed anime with idempotency key subscription_competition:<anime>.
--    Existing fallback items are dropped (no data-preservation obligation
--    before release); the trigger below re-creates the owner on the next
--    settle, and every other trigger enqueues it from the application.
-- 3. The settle of a running instance acquisition of an active subscription
--    enqueues the competition in the same transaction (the 062 trigger, now
--    naming the merged kind).
-- 4. subscription_competition_merge is the one create-or-merge of the
--    competition item (the Go enqueue and the settle trigger both call it).
--    Unlike work_item_merge it never expires the lease of a running round:
--    the round re-decides under the subscription lock before it commits and
--    starts a fresh item when the inputs moved, so a merge neither fences the
--    round out nor spends its attempt budget.
-- 5. releases.competition_retry_key keeps the explicit retry of a release
--    waiting for the competition (the retry request's idempotency key, also
--    the key of the placement redownload requests it recorded): the decided
--    stage runs it as that retry (no start_baseline gate, the manual retry
--    entry, no discovery notice), and a decision that downloads nothing
--    withdraws those requests.
-- 6. subscriptions.competition_first_round_at records the instance's first
--    committed round: only the first round waits for the sources' first
--    refreshes.
-- 7. A provider group that has left the selection (deselected, unavailable
--    or removed) when the transaction commits ends its sources' waiting
--    releases as filtered (provider_group_not_selected) and drops their
--    discovery notice; the subscription row is locked first (the
--    competition's order). The triggers are deferred and re-read the
--    selection's current row: an authoritative discovery or a selection
--    recompute passes through intermediate states (every group unavailable,
--    then restored) inside one transaction, and only the committed state
--    counts.

ALTER TABLE releases DROP CONSTRAINT releases_verdict_check;
ALTER TABLE releases ADD CONSTRAINT releases_verdict_check CHECK (verdict IN ('discovered', 'waiting_metadata', 'subscription_stopped', 'source_unavailable', 'parse_failed', 'accepted_pending_download', 'filtered', 'adopted_existing', 'acquisition_failed', 'submitted', 'resume_baseline', 'start_baseline', 'awaiting_competition') AND ((download_task_id IS NULL AND verdict <> 'submitted') OR (download_task_id IS NOT NULL AND verdict IN ('accepted_pending_download', 'submitted', 'filtered', 'waiting_metadata', 'parse_failed'))));

-- The retirement delete fence refuses deleting a retired anime's items outside
-- its local cleanup; dropping a retired kind is a schema change, not a
-- cleanup, so the fence is suspended for this one statement.
ALTER TABLE work_items DISABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_items WHERE kind = 'replacement_fallback';
ALTER TABLE work_items ENABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_item_outcomes WHERE kind = 'replacement_fallback';

ALTER TABLE releases ADD COLUMN competition_retry_key TEXT;
ALTER TABLE releases ADD CONSTRAINT releases_competition_retry_key_check CHECK (competition_retry_key IS NULL OR char_length(btrim(competition_retry_key)) BETWEEN 1 AND 128);
ALTER TABLE subscriptions ADD COLUMN competition_first_round_at TIMESTAMPTZ;

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'subscription_competition', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'replacement_deletion' AND kind = 'replacement_delete') OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

DROP TRIGGER acquisitions_replacement_fallback ON acquisitions;
DROP FUNCTION enqueue_replacement_fallback();

CREATE FUNCTION subscription_competition_merge(p_anime text, p_input jsonb, p_version bigint, p_delay double precision) RETURNS text
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
INSERT INTO public.work_items(
    id, owner_type, owner_id, kind, idempotency_key,
    max_attempts, input_snapshot, expected_owner_version, available_at, retention_days)
VALUES (gen_random_uuid()::text, 'file_maintenance', p_anime, 'subscription_competition',
    'subscription_competition:' || p_anime, 3, p_input, GREATEST(p_version, 1),
    CURRENT_TIMESTAMP + make_interval(secs => p_delay), 7)
ON CONFLICT (owner_type, owner_id, kind)
DO UPDATE SET
    input_snapshot = CASE WHEN public.work_items.state = 'available' THEN EXCLUDED.input_snapshot ELSE public.work_items.input_snapshot END,
    expected_owner_version = CASE WHEN public.work_items.state = 'available' THEN EXCLUDED.expected_owner_version ELSE public.work_items.expected_owner_version END,
    available_at = CASE
        WHEN public.work_items.state = 'available' THEN LEAST(public.work_items.available_at, EXCLUDED.available_at)
        ELSE public.work_items.available_at
    END,
    updated_at = CURRENT_TIMESTAMP
WHERE public.work_items.idempotency_key = EXCLUDED.idempotency_key
RETURNING id;
$_$;
REVOKE ALL ON FUNCTION subscription_competition_merge(text, jsonb, bigint, double precision) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION subscription_competition_merge(text, jsonb, bigint, double precision) TO bansoko_runtime;

-- The Go twin is competition.EnqueueTx (internal/subscription/competition):
-- both go through subscription_competition_merge.
CREATE FUNCTION enqueue_subscription_competition() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE sub_version BIGINT;
BEGIN
    IF NEW.origin_managed_anime_ref IS NULL OR NEW.entry_kind = 'one_shot' THEN
        RETURN NULL;
    END IF;
    SELECT s.version INTO sub_version
    FROM public.subscriptions s
    JOIN public.managed_anime m ON m.id = s.managed_anime_id
    WHERE s.managed_anime_id = NEW.origin_managed_anime_ref
      AND s.state = 'active'
      AND m.lifecycle_state = 'active';
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    PERFORM public.subscription_competition_merge(NEW.origin_managed_anime_ref,
        jsonb_build_object('trigger', 'acquisition_settled'), sub_version, 0::double precision);
    RETURN NULL;
END;
$$;

CREATE TRIGGER acquisitions_subscription_competition
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN ((old.state = 'running'::text) AND (new.state = ANY (ARRAY['succeeded'::text, 'failed'::text, 'cancelled'::text])))
    EXECUTE FUNCTION enqueue_subscription_competition();

CREATE FUNCTION drop_unselected_awaiting_releases() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    -- Deferred to commit: only the selection's committed state counts (a
    -- group made unavailable and restored in the same transaction stays).
    PERFORM 1 FROM public.subscriptions WHERE id = OLD.subscription_id FOR UPDATE;
    PERFORM 1 FROM public.provider_group_selections g
    WHERE g.subscription_id = OLD.subscription_id AND g.provider = OLD.provider
      AND g.group_ref = OLD.group_ref AND g.available AND g.effective_selected;
    IF FOUND THEN
        RETURN NULL;
    END IF;
    WITH target AS (
        SELECT r.id, r.competition_retry_key AS retry_key, sub.managed_anime_id
        FROM public.releases r
        JOIN public.rss_sources src ON src.id = r.rss_source_id
        JOIN public.subscriptions sub ON sub.id = src.subscription_id
        WHERE src.subscription_id = OLD.subscription_id AND src.source_kind = 'provider_group'
          AND src.provider = OLD.provider AND src.provider_group_ref = OLD.group_ref
          AND r.verdict = 'awaiting_competition' AND r.download_task_id IS NULL
        FOR UPDATE OF r
    ), withdrawn AS (
        UPDATE public.placements p
        SET redownload_requested_at = NULL, redownload_request_key = NULL,
            version = p.version + 1, updated_at = CURRENT_TIMESTAMP
        FROM target t
        WHERE t.retry_key IS NOT NULL AND p.managed_anime_id = t.managed_anime_id
          AND p.quality_facts_ref = t.id AND p.state = 'active' AND p.redownload_request_key = t.retry_key
    )
    UPDATE public.releases r
    SET verdict = 'filtered', reason_code = 'provider_group_not_selected',
        reason_detail = 'the provider group is no longer selected',
        discovery_notice_due = FALSE, competition_retry_key = NULL,
        version = r.version + 1, updated_at = CURRENT_TIMESTAMP
    FROM target t
    WHERE r.id = t.id;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER provider_group_selections_drop_awaiting_update
    AFTER UPDATE OF available, effective_selected ON provider_group_selections
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (old.available AND old.effective_selected AND NOT (new.available AND new.effective_selected))
    EXECUTE FUNCTION drop_unselected_awaiting_releases();

CREATE CONSTRAINT TRIGGER provider_group_selections_drop_awaiting_delete
    AFTER DELETE ON provider_group_selections
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (old.available AND old.effective_selected)
    EXECUTE FUNCTION drop_unselected_awaiting_releases();
