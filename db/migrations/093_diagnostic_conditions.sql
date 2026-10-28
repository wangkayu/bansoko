-- Forward-only migration 093: diagnostic episodes are conditions on a target.
--
-- An episode was keyed by the internal identity of the failing operation
-- (component, operation, error_code, scope). It is now keyed by what the
-- owner recognises: a condition kind on a target (TMDB, the update source, a
-- site, a downloader connection, a library root, a notification target), so
-- failures of different operations on one target merge into one row.
--
-- Every row written before this migration has the old shape and no target, so
-- the table is emptied: every environment is a test environment before the
-- first release and the incident store restarts here.
--
-- The row holds no free text: the condition is a closed kind/variant, the
-- detail of the latest failure is a closed error class with an optional HTTP
-- status and route, and the last five occurrences live in a bounded array.
-- SQL keeps the invariants (closed sets, one open episode per key, bounds);
-- the merge of a new failure into an open episode is derived in Go.
--
-- Internal errors (anything outside the condition catalog) keep one episode
-- per internal source (target_id) and carry the opaque request id.
--
-- managed_anime_id is dropped: no writer ever set it, and "who is affected" is
-- now the counted affected_ids set. The episode therefore no longer belongs to
-- a ManagedAnime graph, so cleanup_managed_anime_local_data below is redefined
-- from its latest definition (063_retention_renames.sql) with exactly one
-- change: the diagnostic_episodes label and its delete step are removed.

DELETE FROM diagnostic_episodes;

DROP INDEX diagnostic_episodes_open_identity_uq;
ALTER TABLE diagnostic_episodes DROP COLUMN component;
ALTER TABLE diagnostic_episodes DROP COLUMN operation;
ALTER TABLE diagnostic_episodes DROP COLUMN error_code;
ALTER TABLE diagnostic_episodes DROP COLUMN scope_kind;
ALTER TABLE diagnostic_episodes DROP COLUMN scope_id;
ALTER TABLE diagnostic_episodes DROP COLUMN managed_anime_id;
ALTER TABLE diagnostic_episodes DROP COLUMN last_error;
ALTER TABLE diagnostic_episodes DROP COLUMN details;

ALTER TABLE diagnostic_episodes ADD COLUMN kind TEXT NOT NULL;
ALTER TABLE diagnostic_episodes ADD COLUMN variant TEXT;
ALTER TABLE diagnostic_episodes ADD COLUMN target_id TEXT NOT NULL;
ALTER TABLE diagnostic_episodes ADD COLUMN target_name TEXT;
ALTER TABLE diagnostic_episodes ADD COLUMN target_host TEXT;
ALTER TABLE diagnostic_episodes ADD COLUMN error_class TEXT;
ALTER TABLE diagnostic_episodes ADD COLUMN http_status INTEGER;
ALTER TABLE diagnostic_episodes ADD COLUMN via TEXT;
ALTER TABLE diagnostic_episodes ADD COLUMN affected_ids TEXT[] NOT NULL;
ALTER TABLE diagnostic_episodes ADD COLUMN recent JSONB NOT NULL;
ALTER TABLE diagnostic_episodes ADD COLUMN last_request_id TEXT;

ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_kind_check
    CHECK (kind IN ('metadata', 'update', 'site', 'downloader', 'library', 'notify', 'internal'));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_variant_check
    CHECK (variant IN ('unreachable', 'auth_failed', 'paused', 'unreadable', 'refresh_failed', 'not_writable', 'no_space', 'secret', 'template', 'request', 'address_rejected'));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_internal_variant_check
    CHECK ((kind = 'internal') = (variant IS NULL));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_target_id_check
    CHECK (char_length(btrim(target_id)) BETWEEN 1 AND 128);
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_target_text_check
    CHECK ((target_name IS NULL OR char_length(btrim(target_name)) BETWEEN 1 AND 256)
       AND (target_host IS NULL OR char_length(btrim(target_host)) BETWEEN 1 AND 253));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_error_class_check
    CHECK (error_class IN ('timeout', 'connection_refused', 'connection_reset', 'dns', 'tls', 'http_status', 'unknown'));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_http_status_check
    CHECK (http_status IS NULL OR (error_class = 'http_status' AND http_status BETWEEN 100 AND 599));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_via_check
    CHECK (via IN ('direct', 'proxy'));
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_affected_ids_check
    CHECK (cardinality(affected_ids) <= 1000);
-- At most five occurrences; a newer one pushes the oldest out in the writer.
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_recent_check
    CHECK (CASE WHEN jsonb_typeof(recent) = 'array'
        THEN jsonb_array_length(recent) <= 5 AND pg_column_size(recent) <= 4096
        ELSE FALSE END);
ALTER TABLE diagnostic_episodes ADD CONSTRAINT diagnostic_episodes_request_id_check
    CHECK (last_request_id IS NULL OR (kind = 'internal' AND char_length(last_request_id) BETWEEN 1 AND 128));

CREATE UNIQUE INDEX diagnostic_episodes_open_identity_uq ON diagnostic_episodes (kind, target_id)
    WHERE succeeded_after_at IS NULL;

COMMENT ON TABLE diagnostic_episodes IS 'Bounded structured fault episodes keyed by condition kind on a target; no free text; purge must never cascade to business owners.';

-- Physical local cleanup of one retired ManagedAnime. Latest definition:
-- 063_retention_renames.sql. Change: diagnostic_episodes is no longer part of
-- the management graph (the column and scopes it was deleted by are gone).
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
        'offset_migration_plans',
        'offset_migration_generations',
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
        'DELETE FROM offset_migration_plans WHERE managed_anime_id = $1',
        'DELETE FROM offset_migration_generations WHERE managed_anime_id = $1',
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
