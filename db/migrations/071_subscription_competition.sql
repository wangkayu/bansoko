-- Forward-only migration 071: one competition per subscription instance
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

ALTER TABLE releases DROP CONSTRAINT releases_verdict_check;
ALTER TABLE releases ADD CONSTRAINT releases_verdict_check CHECK (verdict IN ('discovered', 'waiting_metadata', 'subscription_stopped', 'source_unavailable', 'parse_failed', 'accepted_pending_download', 'filtered', 'adopted_existing', 'acquisition_failed', 'submitted', 'resume_baseline', 'start_baseline', 'awaiting_competition') AND ((download_task_id IS NULL AND verdict <> 'submitted') OR (download_task_id IS NOT NULL AND verdict IN ('accepted_pending_download', 'submitted', 'filtered', 'waiting_metadata', 'parse_failed'))));

-- The retirement delete fence refuses deleting a retired anime's items outside
-- its local cleanup; dropping a retired kind is a schema change, not a
-- cleanup, so the fence is suspended for this one statement.
ALTER TABLE work_items DISABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_items WHERE kind = 'replacement_fallback';
ALTER TABLE work_items ENABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_item_outcomes WHERE kind = 'replacement_fallback';

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'subscription_competition', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'replacement_deletion' AND kind = 'replacement_delete') OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

DROP TRIGGER acquisitions_replacement_fallback ON acquisitions;
DROP FUNCTION enqueue_replacement_fallback();

-- The Go twin is competition.EnqueueTx (internal/subscription/competition): same owner, kind,
-- idempotency key, attempt budget and subscription-version expectation.
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
    PERFORM public.work_item_merge(gen_random_uuid()::text, 'file_maintenance', NEW.origin_managed_anime_ref,
        'subscription_competition', 'subscription_competition:' || NEW.origin_managed_anime_ref, 3,
        jsonb_build_object('trigger', 'acquisition_settled', 'acquisition_id', NEW.id, 'acquisition_state', NEW.state),
        GREATEST(sub_version, 1), 0::double precision, 7::smallint);
    RETURN NULL;
END;
$$;

CREATE TRIGGER acquisitions_subscription_competition
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN ((old.state = 'running'::text) AND (new.state = ANY (ARRAY['succeeded'::text, 'failed'::text, 'cancelled'::text])))
    EXECUTE FUNCTION enqueue_subscription_competition();
