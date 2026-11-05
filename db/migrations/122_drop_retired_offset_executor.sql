-- Forward-only migration 122: the retired offset migration executor tables go.
--
-- Migration 098 emptied offset_migration_plans and offset_migration_generations,
-- revoked runtime writes and deleted their WorkItem kinds; the tables stayed
-- only because the retirement fence, the maintenance-tree work item deletes
-- and the owner cleanup array still named them. Those references go with the
-- tables, in this migration: nothing reads or writes either table anymore, and
-- a work item of the retired owner kind is rejected by the owner/kind
-- constraint long before the fence runs.
--
-- Latest definitions replaced here: 063_retention_renames.sql
-- (fence_work_item_retirement, work_item_delete_file_maintenance,
-- work_item_delete_local_maintenance_tree) and 093_diagnostic_conditions.sql
-- (cleanup_managed_anime_local_data).

CREATE OR REPLACE FUNCTION fence_work_item_retirement() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_found BOOLEAN := FALSE;
    declares_no_anime BOOLEAN := FALSE;
    pins_key BOOLEAN := FALSE;
    owner_key TEXT;
    resolved_root TEXT;
    resolved_managed_anime_id TEXT;
    lifecycle_state TEXT;
    lifecycle_epoch BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.id, NEW.owner_type, NEW.owner_id, NEW.kind, NEW.acquisition_id)
           IS DISTINCT FROM (OLD.id, OLD.owner_type, OLD.owner_id, OLD.kind, OLD.acquisition_id) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_IMMUTABLE';
        END IF;
        IF NEW.managed_anime_id IS DISTINCT FROM OLD.managed_anime_id
           OR NEW.owner_lifecycle_epoch IS DISTINCT FROM OLD.owner_lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_LIFECYCLE_IMMUTABLE';
        END IF;
        IF OLD.managed_anime_id IS NULL THEN
            RETURN NEW;
        END IF;
        SELECT lifecycle.lifecycle_state, lifecycle.fence_epoch INTO lifecycle_state, lifecycle_epoch
        FROM managed_anime lifecycle
        WHERE lifecycle.id = OLD.managed_anime_id
        FOR KEY SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_MISSING';
        END IF;
        IF lifecycle_state <> 'active' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
        END IF;
        IF OLD.owner_lifecycle_epoch IS DISTINCT FROM lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIREMENT_FENCE_STALE';
        END IF;
        RETURN NEW;
    END IF;

    CASE NEW.owner_type
    WHEN 'rss_source_runtime' THEN
        SELECT s.managed_anime_id INTO resolved_managed_anime_id
        FROM rss_source_runtime rt
        LEFT JOIN rss_sources rs ON rs.id = rt.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE rt.rss_source_id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'release' THEN
        SELECT s.managed_anime_id INTO resolved_managed_anime_id
        FROM releases r
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE r.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'download_task' THEN
        SELECT dt.acquisition_id, dt.idempotency_key, s.managed_anime_id
        INTO resolved_root, owner_key, resolved_managed_anime_id
        FROM download_tasks dt
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE dt.id = NEW.owner_id;
        owner_found := FOUND;
        pins_key := TRUE;
    WHEN 'artifact' THEN
        SELECT a.acquisition_id, s.managed_anime_id INTO resolved_root, resolved_managed_anime_id
        FROM artifacts a
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE a.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'file_operation' THEN
        SELECT fo.acquisition_id, fo.idempotency_key, s.managed_anime_id
        INTO resolved_root, owner_key, resolved_managed_anime_id
        FROM file_operations fo
        LEFT JOIN artifacts a ON a.id = fo.artifact_id
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE fo.id = NEW.owner_id;
        owner_found := FOUND;
        pins_key := TRUE;
    WHEN 'postprocessing_plan' THEN
        SELECT plan.acquisition_id, s.managed_anime_id INTO resolved_root, resolved_managed_anime_id
        FROM postprocessing_plans plan
        LEFT JOIN artifacts a ON a.id = plan.artifact_id
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE plan.id = NEW.owner_id;
        owner_found := FOUND;
        IF owner_found AND resolved_root IS NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ARTIFACT_PLAN_ROOT_MISSING';
        END IF;
    WHEN 'plugin_delivery' THEN
        SELECT delivery.managed_anime_id INTO resolved_managed_anime_id
        FROM plugin_deliveries delivery WHERE delivery.id = NEW.owner_id;
        owner_found := FOUND;
        declares_no_anime := owner_found AND resolved_managed_anime_id IS NULL;
    WHEN 'file_maintenance' THEN
        -- The file-maintenance owner id is the managed anime itself; the
        -- reconciliation kinds also need the anime's reconciliation scan.
        resolved_managed_anime_id := NEW.owner_id;
        owner_found := NEW.kind NOT IN ('reconciliation_scan', 'reconciliation_calibrate',
                'reconciliation_consume_alignment', 'reconciliation_consume_replacement')
            OR EXISTS (SELECT 1 FROM reconciliation_scans scan WHERE scan.managed_anime_id = NEW.owner_id);
    WHEN 'alignment_operation' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM alignment_operations op WHERE op.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'replacement_deletion' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM replacement_deletions op WHERE op.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'acquisition' THEN
        owner_found := EXISTS (SELECT 1 FROM acquisitions root WHERE root.id = NEW.owner_id);
        resolved_root := NEW.owner_id;
        declares_no_anime := TRUE;
    WHEN 'file_manager_operation' THEN
        owner_found := EXISTS (SELECT 1 FROM file_manager_operations op WHERE op.id = NEW.owner_id);
        declares_no_anime := TRUE;
    WHEN 'download_completion' THEN
        owner_found := EXISTS (SELECT 1 FROM download_completions completion WHERE completion.id = NEW.owner_id);
        declares_no_anime := TRUE;
    ELSE
        owner_found := FALSE;
    END CASE;

    IF NOT owner_found THEN
        RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'WORK_ITEM_OWNER_MISSING';
    END IF;
    IF pins_key AND owner_key IS DISTINCT FROM NEW.idempotency_key THEN
        RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'WORK_ITEM_OWNER_KEY_MISMATCH';
    END IF;
    IF NEW.acquisition_id IS NOT NULL AND NEW.acquisition_id IS DISTINCT FROM resolved_root THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_ROOT_MISMATCH';
    END IF;
    NEW.acquisition_id := resolved_root;
    NEW.managed_anime_id := NULL;
    NEW.owner_lifecycle_epoch := NULL;

    IF resolved_managed_anime_id IS NULL THEN
        IF NOT declares_no_anime THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_UNRESOLVED';
        END IF;
        RETURN NEW;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM managed_anime anime WHERE anime.id = resolved_managed_anime_id) THEN
        IF NEW.kind = 'replacement_evaluate' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_UNRESOLVED';
        END IF;
        -- Synthetic owner: no managed_anime identity exists, so no lifecycle
        -- marker can exist and there is nothing to fence.
        RETURN NEW;
    END IF;

    SELECT lifecycle.lifecycle_state, lifecycle.fence_epoch INTO lifecycle_state, lifecycle_epoch
    FROM managed_anime lifecycle
    WHERE lifecycle.id = resolved_managed_anime_id
    FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_MISSING';
    END IF;
    IF lifecycle_state <> 'active' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
    END IF;
    NEW.managed_anime_id := resolved_managed_anime_id;
    NEW.owner_lifecycle_epoch := lifecycle_epoch;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION work_item_delete_file_maintenance(text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items AS wi
WHERE (
    wi.owner_type = 'file_maintenance'
    AND wi.owner_id = $1
) OR (
    wi.owner_type = 'alignment_operation'
    AND wi.kind = 'alignment_execute'
    AND EXISTS (
      SELECT 1
      FROM public.alignment_operations ao
      WHERE ao.id = wi.owner_id
        AND ao.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'replacement_deletion'
    AND wi.kind = 'replacement_delete'
    AND EXISTS (
      SELECT 1
      FROM public.replacement_deletions co
      WHERE co.id = wi.owner_id
        AND co.managed_anime_id = $1
    )
);
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes AS wi
WHERE (
    wi.owner_type = 'file_maintenance'
    AND wi.owner_id = $1
) OR (
    wi.owner_type = 'alignment_operation'
    AND wi.kind = 'alignment_execute'
    AND EXISTS (
      SELECT 1
      FROM public.alignment_operations ao
      WHERE ao.id = wi.owner_id
        AND ao.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'replacement_deletion'
    AND wi.kind = 'replacement_delete'
    AND EXISTS (
      SELECT 1
      FROM public.replacement_deletions co
      WHERE co.id = wi.owner_id
        AND co.managed_anime_id = $1
    )
);
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

CREATE OR REPLACE FUNCTION work_item_delete_local_maintenance_tree(text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items
WHERE (owner_type = 'file_maintenance' AND owner_id = $1)
   OR (owner_type = 'alignment_operation' AND owner_id IN (
       SELECT id FROM public.alignment_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'replacement_deletion' AND owner_id IN (
       SELECT id FROM public.replacement_deletions WHERE managed_anime_id = $1));
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes
WHERE (owner_type = 'file_maintenance' AND owner_id = $1)
   OR (owner_type = 'alignment_operation' AND owner_id IN (
       SELECT id FROM public.alignment_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'replacement_deletion' AND owner_id IN (
       SELECT id FROM public.replacement_deletions WHERE managed_anime_id = $1));
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

CREATE OR REPLACE FUNCTION cleanup_managed_anime_local_data(p_managed_anime_id text, p_idempotency_key text, p_request_hash text, p_expected_lifecycle_epoch bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    existing_hash TEXT;
    existing_epoch BIGINT;
    existing_total BIGINT;
    existing_detail JSONB;
    lifecycle_state TEXT;
    lifecycle_epoch BIGINT;
    affected BIGINT;
    total BIGINT := 0;
    detail JSONB := '{}'::jsonb;

    owner_subscriptions TEXT;
    owner_sources TEXT;
    owner_releases TEXT;
    owner_download_tasks TEXT;
    owner_artifacts TEXT;
    owner_file_operations TEXT;
    owner_snapshots TEXT;

    cleanup_labels TEXT[];
    cleanup_steps TEXT[];
    step_index INT;

    root_row RECORD;
    disposition TEXT;
    compacted JSONB;
    roots_cancelled BIGINT := 0;
    roots_compacted BIGINT := 0;
BEGIN
    IF p_managed_anime_id IS NULL OR btrim(p_managed_anime_id) = '' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_FOUND';
    END IF;

    -- Replay lives in idempotency_records keyed by owner_id (never by the
    -- managed_anime_id FK column) so it survives the owner delete.
    SELECT rec.request_hash, (rec.result_snapshot ->> 'lifecycle_epoch')::bigint,
           (rec.result_snapshot ->> 'deleted_total')::bigint, rec.result_snapshot -> 'deleted_detail'
    INTO existing_hash, existing_epoch, existing_total, existing_detail
    FROM idempotency_records rec
    WHERE rec.kind = 'local_cleanup' AND rec.owner_type = 'managed_anime'
      AND rec.owner_id = p_managed_anime_id AND rec.idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing_hash <> p_request_hash OR existing_epoch <> p_expected_lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'IDEMPOTENCY_KEY_REUSED';
        END IF;
        RETURN jsonb_build_object(
            'status', 'replayed',
            'deleted_total', existing_total,
            'deleted_detail', existing_detail);
    END IF;

    SELECT anime.lifecycle_state, anime.fence_epoch INTO lifecycle_state, lifecycle_epoch
    FROM managed_anime anime
    WHERE anime.id = p_managed_anime_id
    FOR UPDATE;
    IF NOT FOUND THEN
        -- A concurrent cleanup with the same key may have committed while this
        -- transaction waited on the owner row.
        SELECT rec.request_hash, (rec.result_snapshot ->> 'lifecycle_epoch')::bigint,
               (rec.result_snapshot ->> 'deleted_total')::bigint, rec.result_snapshot -> 'deleted_detail'
        INTO existing_hash, existing_epoch, existing_total, existing_detail
        FROM idempotency_records rec
        WHERE rec.kind = 'local_cleanup' AND rec.owner_type = 'managed_anime'
          AND rec.owner_id = p_managed_anime_id AND rec.idempotency_key = p_idempotency_key;
        IF FOUND THEN
            IF existing_hash <> p_request_hash OR existing_epoch <> p_expected_lifecycle_epoch THEN
                RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'IDEMPOTENCY_KEY_REUSED';
            END IF;
            RETURN jsonb_build_object(
                'status', 'replayed',
                'deleted_total', existing_total,
                'deleted_detail', existing_detail);
        END IF;
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_FOUND';
    END IF;
    IF lifecycle_state <> 'retired' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_RETIRED';
    END IF;
    IF lifecycle_epoch <> p_expected_lifecycle_epoch THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_STALE_EPOCH';
    END IF;

    -- Transaction-local authorization read by the guards that only cleanup
    -- may pass (live work delete, owner-cleanup compaction, owner delete).
    PERFORM set_config('bansoko.local_cleanup_owner', p_managed_anime_id, true);

    UPDATE anime_config_candidates
    SET activated_managed_anime_id = NULL,
        status = 'archived',
        version = version + 1,
        updated_at = CURRENT_TIMESTAMP
    WHERE activated_managed_anime_id = p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('candidates_detached', affected);
        total := total + affected;
    END IF;

    owner_subscriptions :=
        'SELECT su.id FROM subscriptions su WHERE su.managed_anime_id = $1';
    owner_sources :=
        'SELECT rs.id FROM rss_sources rs WHERE rs.subscription_id IN (' || owner_subscriptions || ')';
    owner_releases :=
        'SELECT r.id FROM releases r WHERE r.rss_source_id IN (' || owner_sources || ')';
    owner_download_tasks :=
        'SELECT dt.id FROM download_tasks dt WHERE dt.release_id IN (' || owner_releases || ')';
    owner_artifacts :=
        'SELECT a.id FROM artifacts a WHERE a.download_task_id IN (' || owner_download_tasks || ')';
    owner_file_operations :=
        'SELECT fo.id FROM file_operations fo WHERE fo.artifact_id IN (' || owner_artifacts || ')';
    owner_snapshots :=
        'SELECT s.id FROM media_fact_snapshots s WHERE s.release_id IN (' || owner_releases || ')';

    -- The owner's own pins on root rows go first, so compact sees a root that
    -- only its own graph references.
    EXECUTE 'DELETE FROM idempotency_records WHERE kind = ''manual_retry'' AND ('
            || ' (owner_type = ''rss_source_runtime'' AND owner_id IN (' || owner_sources || '))'
            || ' OR (owner_type = ''download_task'' AND owner_id IN (' || owner_download_tasks || '))'
            || ' OR (owner_type = ''artifact'' AND owner_id IN (' || owner_artifacts || '))'
            || ' OR (owner_type = ''file_operation'' AND owner_id IN (' || owner_file_operations || '))'
            || ' OR failed_work_item_id IN (SELECT id FROM work_item_history WHERE managed_anime_id = $1)'
            || ' OR result_work_item_id IN (SELECT id FROM work_item_history WHERE managed_anime_id = $1)'
            || ')' USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('manual_retry_idempotency', affected);
        total := total + affected;
    END IF;
    EXECUTE 'DELETE FROM idempotency_records WHERE managed_anime_id = $1'
            || ' OR release_id IN (' || owner_releases || ')' USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('idempotency_records', affected);
        total := total + affected;
    END IF;
    EXECUTE 'DELETE FROM placement_obligations WHERE file_operation_id IN (' || owner_file_operations || ')'
        USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('placement_obligations', affected);
        total := total + affected;
    END IF;

    -- Every related root settles and compacts before the management graph goes.
    -- A running root of the retired owner settles to cancelled/OWNER_RETIRED;
    -- one whose admitted external effect is still unresolved keeps the cleanup
    -- refused. Nothing here touches qB, a downloaded byte or a library file.
    FOR root_row IN
        SELECT r.id, r.state, r.record_state FROM acquisitions r
        WHERE r.record_state = 'full'
          AND (r.origin_managed_anime_ref = p_managed_anime_id OR EXISTS(
               SELECT 1 FROM download_tasks d JOIN releases rel ON rel.id = d.release_id
               JOIN rss_sources src ON src.id = rel.rss_source_id
               JOIN subscriptions sub ON sub.id = src.subscription_id
               WHERE d.acquisition_id = r.id AND sub.managed_anime_id = p_managed_anime_id))
        ORDER BY r.id
        FOR UPDATE OF r
    LOOP
        IF root_row.state = 'running' THEN
            disposition := acquisition_dispose_retired(root_row.id);
            IF disposition <> 'cancelled' THEN
                RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_ACQUISITION_RETAINED',
                    DETAIL = 'acquisition ' || root_row.id || ': ' || disposition;
            END IF;
            roots_cancelled := roots_cancelled + 1;
        END IF;
        compacted := acquisition_compact(root_row.id, 'owner_cleanup');
        IF compacted ->> 'status' <> 'compacted' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_ACQUISITION_RETAINED',
                DETAIL = 'acquisition ' || root_row.id || ': ' || (compacted ->> 'status');
        END IF;
        roots_compacted := roots_compacted + 1;
        SELECT detail || COALESCE(jsonb_object_agg(e.key,
                   COALESCE((detail ->> e.key)::bigint, 0) + e.value::text::bigint), '{}'::jsonb),
               total + COALESCE(sum(e.value::text::bigint), 0)
        INTO detail, total
        FROM jsonb_each(compacted -> 'deleted') e;
    END LOOP;
    IF roots_cancelled > 0 THEN
        detail := detail || jsonb_build_object('acquisitions_cancelled', roots_cancelled);
    END IF;
    IF roots_compacted > 0 THEN
        detail := detail || jsonb_build_object('acquisitions_compacted', roots_compacted);
    END IF;

    -- Fixed leaf-first order of the management graph (unchanged from 098 apart
    -- from the pins removed above). Cross-owner/global facts, acquisition
    -- roots and their retained evidence, and audit events stay.
    cleanup_labels := ARRAY[
        'work_items',
        'work_item_outcomes',
        'routine_activities',
        'acquisition_targets',
        'media_fact_files',
        'media_fact_snapshots',
        'file_operations',
        'artifact_files',
        'artifacts',
        'release_torrent_inputs',
        'release_candidate_facts',
        'subscription_log_entries',
        'download_tasks',
        'releases',
        'rss_source_runtime',
        'rss_sources',
        'provider_group_selections',
        'placements',
        'replacement_decisions',
        'replacement_deletions',
        'alignment_operations',
        'reconciliation_files',
        'reconciliation_scans',
        'anime_config_overrides',
        'anime_business_configs',
        'anime_metadata',
        'plugin_deliveries',
        'notification_admissions',
        'filemaintenance_policies',
        'subscriptions'
    ];
    cleanup_steps := ARRAY[
        'DELETE FROM work_items WHERE managed_anime_id = $1',
        'DELETE FROM work_item_outcomes WHERE managed_anime_id = $1',
        'DELETE FROM routine_activities WHERE managed_anime_id = $1'
            || ' OR (scope_kind = ''subscription'' AND scope_id IN (' || owner_subscriptions || '))'
            || ' OR (scope_kind = ''rss_source'' AND scope_id IN (' || owner_sources || '))',
        'DELETE FROM acquisition_targets WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM media_fact_files WHERE snapshot_id IN (' || owner_snapshots || ')',
        'DELETE FROM media_fact_snapshots WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM file_operations WHERE artifact_id IN (' || owner_artifacts || ')',
        'DELETE FROM artifact_files WHERE artifact_id IN (' || owner_artifacts || ')',
        'DELETE FROM artifacts WHERE download_task_id IN (' || owner_download_tasks || ')',
        'DELETE FROM release_torrent_inputs WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM release_candidate_facts WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM subscription_log_entries WHERE managed_anime_id = $1'
            || ' OR subscription_id IN (' || owner_subscriptions || ')'
            || ' OR release_id IN (' || owner_releases || ')',
        'DELETE FROM download_tasks WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM releases WHERE rss_source_id IN (' || owner_sources || ')',
        'DELETE FROM rss_source_runtime WHERE rss_source_id IN (' || owner_sources || ')',
        'DELETE FROM rss_sources WHERE subscription_id IN (' || owner_subscriptions || ')',
        'DELETE FROM provider_group_selections WHERE subscription_id IN (' || owner_subscriptions || ')',
        'DELETE FROM placements WHERE managed_anime_id = $1',
        'DELETE FROM replacement_decisions WHERE managed_anime_id = $1',
        'DELETE FROM replacement_deletions WHERE managed_anime_id = $1',
        'DELETE FROM alignment_operations WHERE managed_anime_id = $1',
        'DELETE FROM reconciliation_files WHERE managed_anime_id = $1',
        'DELETE FROM reconciliation_scans WHERE managed_anime_id = $1',
        'DELETE FROM anime_config_overrides WHERE managed_anime_id = $1',
        'DELETE FROM anime_business_configs WHERE managed_anime_id = $1',
        'DELETE FROM anime_metadata WHERE managed_anime_id = $1',
        'DELETE FROM plugin_deliveries WHERE managed_anime_id = $1',
        'DELETE FROM notification_admissions WHERE managed_anime_id = $1',
        'DELETE FROM filemaintenance_policies WHERE scope_key = ''anime:'' || $1',
        'DELETE FROM subscriptions WHERE managed_anime_id = $1'
    ];

    FOR step_index IN 1 .. array_length(cleanup_steps, 1) LOOP
        EXECUTE cleanup_steps[step_index] USING p_managed_anime_id;
        GET DIAGNOSTICS affected = ROW_COUNT;
        IF affected > 0 THEN
            detail := detail || jsonb_build_object(cleanup_labels[step_index], affected);
            total := total + affected;
        END IF;
    END LOOP;

    DELETE FROM managed_anime WHERE id = p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_OWNER_VANISHED';
    END IF;
    detail := detail || jsonb_build_object('managed_anime', affected);
    total := total + affected;

    INSERT INTO idempotency_records(kind, idempotency_key, request_hash, owner_type, owner_id, result_snapshot)
    VALUES ('local_cleanup', p_idempotency_key, p_request_hash, 'managed_anime', p_managed_anime_id,
            jsonb_build_object('lifecycle_epoch', lifecycle_epoch, 'deleted_total', total, 'deleted_detail', detail));
    PERFORM set_config('bansoko.local_cleanup_owner', '', true);

    RETURN jsonb_build_object(
        'status', 'performed',
        'deleted_total', total,
        'deleted_detail', detail);
END;
$_$;


DROP TABLE public.offset_migration_plans;
DROP TABLE public.offset_migration_generations;
