-- Forward-only migration 062: replacement fallback (owner rulings 3, 5, 6).
--
-- A fallback is one highest-score query, not a state machine. When an
-- instance acquisition of a subscription leaves `running` (succeeded, failed
-- or cancelled — a timeout fails it, a deletion in the downloader cancels it)
-- the coverage it claimed is free again. The releases that were skipped as
-- covered_in_flight for that coverage are compared once and the best of each
-- pool is evaluated again through normal acceptance. A failed or cancelled
-- release keeps its download task and so stays out of the query until a user
-- retries it.
--
-- 1. The file-maintenance owner gets the replacement_fallback kind (one live
--    item per anime, idempotency key replacement_fallback:<anime>).
-- 2. The settle of a running non-one-shot acquisition of an active
--    subscription enqueues it in the same transaction, so a crash between
--    the settle and the query loses nothing. A stopped subscription is not
--    triggered here; resume enqueues it instead.

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'replacement_fallback', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'cleanup_operation' AND kind = 'cleanup_delete') OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

CREATE FUNCTION enqueue_replacement_fallback() RETURNS trigger
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
        'replacement_fallback', 'replacement_fallback:' || NEW.origin_managed_anime_ref, 3,
        jsonb_build_object('trigger', 'candidate_settled', 'acquisition_id', NEW.id, 'acquisition_state', NEW.state),
        GREATEST(sub_version, 1), 0::double precision, 7::smallint);
    RETURN NULL;
END;
$$;

CREATE TRIGGER acquisitions_replacement_fallback
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN ((old.state = 'running'::text) AND (new.state = ANY (ARRAY['succeeded'::text, 'failed'::text, 'cancelled'::text])))
    EXECUTE FUNCTION enqueue_replacement_fallback();
