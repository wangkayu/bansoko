-- WorkItem persistence adapter query surface.
-- db/migrations remains the only authoritative schema source.

-- name: LockCleanupWorkItem :one
SELECT id::text AS id,owner_type::text AS owner_type,owner_id::text AS owner_id,kind::text AS kind,state::text AS state,expected_owner_version::bigint AS expected_owner_version FROM public.work_item_lock_cleanup(sqlc.arg(owner_type)::text,sqlc.arg(owner_id)::text,sqlc.arg(kind)::text);

-- name: CompleteCleanupWorkItem :one
SELECT public.work_item_complete_cleanup(sqlc.arg(new_owner_version)::bigint, sqlc.arg(work_item_id)::text, sqlc.arg(owner_type)::text, sqlc.arg(owner_id)::text, sqlc.arg(kind)::text, sqlc.arg(max_expected_version)::bigint)::bigint AS affected;

-- name: LockOneShotWorkItemIdentity :one
SELECT id::text AS id,owner_type::text AS owner_type,owner_id::text AS owner_id,input_snapshot::jsonb AS input_snapshot FROM public.work_item_lock_oneshot_identity(sqlc.arg(id)::text);

-- name: BlockUnrunnableWorkItems :exec
SELECT public.work_item_block_unrunnable(sqlc.arg(kinds)::text[]);

-- name: BlockWorkItemRecovery :one
SELECT public.work_item_block_recovery(sqlc.arg(reason)::text, sqlc.arg(id)::text, sqlc.arg(fencing_token)::bigint)::bigint AS affected;


-- name: ClaimDue :one
SELECT state_before::text AS state_before, id::text AS id, owner_type::text AS owner_type, owner_id::text AS owner_id, kind::text AS kind, expected_owner_version::bigint AS expected_owner_version, fencing_token::bigint AS fencing_token, attempt::integer AS attempt, max_attempts::integer AS max_attempts, idempotency_key::text AS idempotency_key, input_snapshot::jsonb AS input_snapshot, cancel_requested::boolean AS cancel_requested, COALESCE(managed_anime_id, '')::text AS managed_anime_id, COALESCE(owner_lifecycle_epoch, 0)::bigint AS owner_lifecycle_epoch FROM public.work_item_claim_due(sqlc.arg(lease_seconds)::double precision, sqlc.arg(kinds)::text[]);

-- name: AdmitManagedAnimeEffect :one
-- The unreachable table branch supplies sqlc's original nullable column type.
SELECT external_effect_admitted_at FROM work_items WHERE false
UNION ALL
SELECT public.work_item_admit_managed_effect(sqlc.arg(effect_bearing)::boolean, sqlc.arg(work_item_id)::text, sqlc.arg(fencing_token)::bigint) AS external_effect_admitted_at;

-- name: CreateWorkItem :one
SELECT public.work_item_create(sqlc.arg(id)::text, sqlc.arg(owner_type)::text, sqlc.arg(owner_id)::text, sqlc.arg(kind)::text, sqlc.arg(idempotency_key)::text, sqlc.arg(max_attempts)::integer, sqlc.arg(input_snapshot)::jsonb, sqlc.arg(expected_owner_version)::bigint, sqlc.arg(available_after_seconds)::double precision, sqlc.arg(retention_days)::smallint) AS id;

-- name: MergeWorkItem :one
SELECT public.work_item_merge(sqlc.arg(id)::text, sqlc.arg(owner_type)::text, sqlc.arg(owner_id)::text, sqlc.arg(kind)::text, sqlc.arg(idempotency_key)::text, sqlc.arg(max_attempts)::integer, sqlc.arg(input_snapshot)::jsonb, sqlc.arg(expected_owner_version)::bigint, sqlc.arg(available_after_seconds)::double precision, sqlc.arg(retention_days)::smallint) AS id;

-- name: LockRSSSourceRuntimeVersion :one
SELECT version FROM rss_source_runtime WHERE rss_source_id = sqlc.arg(owner_id) FOR UPDATE;

-- name: LockDownloadTaskVersion :one
SELECT version FROM download_tasks WHERE id = sqlc.arg(owner_id) FOR UPDATE;

-- name: LockArtifactVersion :one
SELECT version FROM artifacts WHERE id = sqlc.arg(owner_id) FOR UPDATE;

-- name: LockOneShotMarker :one
SELECT state, admission_epoch, version, active_effect_count, active_effect_leases
FROM one_shot_acquisitions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: WriteOneShotAdmissionLedger :execresult
UPDATE one_shot_acquisitions
SET active_effect_leases = sqlc.arg(active_effect_leases),
    active_effect_count = sqlc.arg(active_effect_count),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: FinishOneShotWorkItem :exec
SELECT public.work_item_finish_one_shot(sqlc.arg(state)::text, sqlc.arg(id)::text);

-- name: PatchOneShotWorkItemOwnerVersion :exec
SELECT public.work_item_patch_one_shot_owner_version(sqlc.arg(expected_owner_version)::bigint, sqlc.arg(id)::text);

-- name: FinishWorkItem :exec
SELECT public.work_item_finish(sqlc.arg(state)::text, sqlc.arg(expected_owner_version)::bigint, sqlc.arg(id)::text);

-- name: LockManagedAnimeLifecycleClaim :one
SELECT 1
FROM managed_anime lifecycle
JOIN work_items item ON item.managed_anime_id = lifecycle.id
WHERE item.id = sqlc.arg(id)
  AND item.managed_anime_id = sqlc.arg(managed_anime_id)
  AND lifecycle.lifecycle_state = 'active'
  AND lifecycle.fence_epoch = item.owner_lifecycle_epoch
FOR KEY SHARE OF lifecycle;

-- name: LockWorkItemClaim :one
SELECT locked::integer AS locked FROM public.work_item_lock_claim(sqlc.arg(id)::text,sqlc.arg(fencing_token)::bigint);

-- name: ReadCurrentWorkItemOwnerVersion :one
SELECT expected_owner_version::bigint AS expected_owner_version FROM public.work_item_lock_owner_version(sqlc.arg(id)::text);

-- name: FailWorkItemAtAttemptBudget :one
SELECT public.work_item_fail_at_attempt_budget(sqlc.arg(owner_version)::bigint, sqlc.arg(reason_code)::text, sqlc.arg(reason)::text, sqlc.arg(id)::text, sqlc.arg(current_expected_version)::bigint)::bigint AS affected;

-- name: ReturnWorkItemToAvailable :one
SELECT public.work_item_return_available(sqlc.arg(delay_seconds)::double precision, sqlc.arg(owner_version)::bigint, sqlc.arg(reason_code)::text, sqlc.arg(reason)::text, sqlc.arg(id)::text, sqlc.arg(current_expected_version)::bigint)::bigint AS affected;

-- name: SynchronizeWorkItemOwnerVersion :one
SELECT public.work_item_synchronize_owner_version(sqlc.arg(owner_version)::bigint, sqlc.arg(id)::text, sqlc.arg(fencing_token)::bigint, sqlc.arg(current_expected_owner_version)::bigint)::bigint AS affected;

-- name: PurgeExpiredWorkItems :one
SELECT public.work_item_purge_expired($1::integer)::bigint AS affected;

-- name: RenewWorkItemLease :one
SELECT public.work_item_renew_lease(sqlc.arg(extend_to)::timestamptz, sqlc.arg(id)::text, sqlc.arg(fencing_token)::bigint)::bigint AS affected;

-- name: RequestWorkItemCancel :one
SELECT public.work_item_request_cancel(sqlc.arg(id)::text, sqlc.arg(fencing_token)::bigint)::bigint AS affected;

-- name: HasLiveFileMaintenanceWorkItems :one
SELECT EXISTS (
    SELECT 1
    FROM work_items wi
    WHERE wi.owner_type = 'file_maintenance'
      AND wi.owner_id = sqlc.arg(managed_anime_id)
      AND wi.state IN ('available', 'leased')
) OR EXISTS (
    SELECT 1
    FROM work_items wi
    JOIN alignment_operations ao ON ao.id = wi.owner_id
    WHERE wi.owner_type = 'alignment_operation'
      AND wi.owner_id = ao.id
      AND wi.kind = 'alignment_execute'
      AND ao.managed_anime_id = sqlc.arg(managed_anime_id)
      AND wi.state IN ('available', 'leased')
) OR EXISTS (
    SELECT 1
    FROM work_items wi
    JOIN replacement_deletions co ON co.id = wi.owner_id
    WHERE wi.owner_type = 'replacement_deletion'
      AND wi.owner_id = co.id
      AND wi.kind = 'replacement_delete'
      AND co.managed_anime_id = sqlc.arg(managed_anime_id)
      AND wi.state IN ('available', 'leased')
);

-- name: DeleteFileMaintenanceWorkItems :one
SELECT public.work_item_delete_file_maintenance(sqlc.arg(managed_anime_id)::text)::bigint AS affected;

-- name: LockManualRetryIdempotency :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key), 0));

-- name: GetManualRetryIdempotency :one
SELECT request_hash, result_work_item_id
FROM idempotency_records
WHERE kind = 'manual_retry'
  AND owner_type = sqlc.arg(owner_type)
  AND owner_id = sqlc.arg(owner_id)
  AND idempotency_key = sqlc.arg(idempotency_key)
FOR UPDATE;

-- name: GetFailedWorkItemSnapshot :one
SELECT input_snapshot
FROM work_item_history
WHERE id = sqlc.arg(id)
  AND owner_type = sqlc.arg(owner_type)
  AND owner_id = sqlc.arg(owner_id);

-- name: LockFailedWorkItemForManualRetry :one
SELECT kind::text AS kind,idempotency_key::text AS idempotency_key,input_snapshot::jsonb AS input_snapshot,max_attempts::integer AS max_attempts,state::text AS state FROM public.work_item_lock_failed_retry(sqlc.arg(id)::text,sqlc.arg(owner_type)::text,sqlc.arg(owner_id)::text);

-- name: CancelWorkItemRequests :one
SELECT public.work_item_cancel_requests(sqlc.arg(kinds)::text[])::bigint AS affected;

-- name: ListRunningOneShotMarkers :many
SELECT id
FROM one_shot_acquisitions
WHERE state = 'running'
ORDER BY retention_until NULLS LAST, id
LIMIT sqlc.arg(batch_size);

-- name: ListDueOneShotMarkers :many
SELECT id
FROM one_shot_acquisitions
WHERE state IN ('reclaim_pending', 'cleanup_blocked')
  AND retention_until <= CURRENT_TIMESTAMP
ORDER BY retention_until NULLS LAST, id
LIMIT sqlc.arg(batch_size);

-- name: ListDueReclaimPendingOneShotMarkers :many
SELECT id
FROM one_shot_acquisitions
WHERE state = 'reclaim_pending'
  AND retention_until <= CURRENT_TIMESTAMP
ORDER BY retention_until NULLS LAST, id
LIMIT sqlc.arg(batch_size);

-- name: ListDueCleanupBlockedOneShotMarkers :many
-- A due marker is rechecked; a marker whose chain is still open (a running
-- root or live chain work) is selected at once, ahead of due ones, so a chain
-- blocked while its work was live resumes on the next pass.
SELECT marker.id
FROM one_shot_acquisitions marker
CROSS JOIN LATERAL (
    SELECT marker.download_task_id IS NOT NULL AND public.one_shot_chain_open(marker.id) AS chain_open
) open_chain
WHERE marker.state = 'cleanup_blocked'
  AND (marker.retention_until <= CURRENT_TIMESTAMP OR open_chain.chain_open)
ORDER BY open_chain.chain_open DESC, marker.retention_until NULLS LAST, marker.id
LIMIT sqlc.arg(batch_size);

-- name: OneShotChainOpen :one
SELECT public.one_shot_chain_open(sqlc.arg(marker_id)::text)::boolean AS chain_open;

-- name: ResumeBlockedOneShotChain :one
SELECT public.one_shot_resume_blocked_chain(sqlc.arg(marker_id)::text, sqlc.narg(work_item_id)::text, sqlc.narg(fencing_token)::bigint)::jsonb AS reply;

-- name: PurgeExpiredCleanedOneShotMarkers :execresult
-- The immutable attempt summaries are retained exactly until marker
-- purge (never by the generic WorkItem retention), so the purge removes the
-- attempt rows of the markers it deletes in the same statement. Orphaned
-- attempt rows would otherwise outlive their acquisition forever.
WITH due AS (
    SELECT id FROM one_shot_acquisitions
    WHERE state = 'cleaned' AND retention_until <= CURRENT_TIMESTAMP
    ORDER BY retention_until, id
    LIMIT sqlc.arg(batch_size)
),
purged_attempts AS (
    DELETE FROM one_shot_execution_attempts
    WHERE acquisition_id IN (SELECT id FROM due)
)
DELETE FROM one_shot_acquisitions
WHERE id IN (SELECT id FROM due);

-- name: EnterOneShotReclaiming :execresult
UPDATE one_shot_acquisitions
SET state = 'reclaiming',
    admission_epoch = admission_epoch + 1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: MarkOneShotCleaned :execresult
UPDATE one_shot_acquisitions
SET state = 'cleaned',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: ComputeOneShotReclaimDeadline :one
SELECT (CURRENT_TIMESTAMP + (sqlc.arg(retention_days)::int * INTERVAL '1 day'))::timestamptz;

-- name: DrainOneShotMarkerToReclaimPending :execresult
UPDATE one_shot_acquisitions
SET state = 'reclaim_pending',
    terminal_outcome = sqlc.arg(terminal_outcome),
    terminal_reason_code = sqlc.arg(terminal_reason_code),
    terminal_reason = sqlc.arg(terminal_reason),
    terminal_at = CURRENT_TIMESTAMP,
    retention_until = sqlc.arg(retention_until),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: ComputeOneShotCleanupDeadline :one
SELECT LEAST(
    CURRENT_TIMESTAMP + (sqlc.arg(retention_days)::int * INTERVAL '1 day'),
    COALESCE(terminal_at, CURRENT_TIMESTAMP) + (sqlc.arg(max_retention_days)::int * INTERVAL '1 day')
)::timestamptz
FROM one_shot_acquisitions
WHERE id = sqlc.arg(marker_id);

-- name: BlockOneShotMarker :execresult
UPDATE one_shot_acquisitions
SET state = 'cleanup_blocked',
    terminal_outcome = CASE WHEN terminal_outcome IS NOT NULL THEN terminal_outcome ELSE sqlc.arg(terminal_outcome) END,
    terminal_reason_code = CASE WHEN terminal_reason_code IS NOT NULL THEN terminal_reason_code ELSE sqlc.arg(terminal_reason_code) END,
    terminal_reason = CASE WHEN terminal_reason IS NOT NULL THEN terminal_reason ELSE sqlc.arg(terminal_reason) END,
    terminal_at = CASE WHEN terminal_at IS NOT NULL THEN terminal_at ELSE CURRENT_TIMESTAMP END,
    retention_until = sqlc.arg(retention_until),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: GetOneShotReclaimUntil :one
SELECT retention_until
FROM one_shot_acquisitions
WHERE id = sqlc.arg(marker_id);

-- name: UnblockOneShotMarker :execresult
UPDATE one_shot_acquisitions
SET state = 'reclaim_pending',
    retention_until = GREATEST(retention_until, sqlc.arg(retention_until)),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: ExtendOneShotCleanupBlocked :execresult
UPDATE one_shot_acquisitions
SET retention_until = GREATEST(
        retention_until,
        LEAST(
            CURRENT_TIMESTAMP + (sqlc.arg(retention_days)::int * INTERVAL '1 day'),
            COALESCE(terminal_at, CURRENT_TIMESTAMP) + (sqlc.arg(max_retention_days)::int * INTERVAL '1 day')
        )
    ),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(marker_id);

-- name: ResolveOneShotAnchors :one
-- The download task anchor is optional: an exact-target-adopting one-shot
-- acquisition created no DownloadTask. COALESCE(dt.id, '') resolves to '' both
-- for adoption (marker column NULL) and for a dangling anchor id, so the caller
-- must combine it with the release verdict to tell the two apart.
SELECT m.managed_anime_id, m.subscription_id, m.rss_source_id, m.release_id,
       COALESCE(dt.id, '')::text AS download_task_id, r.verdict
FROM one_shot_acquisitions AS m
JOIN managed_anime AS ma ON ma.id = m.managed_anime_id
JOIN subscriptions AS sub ON sub.id = m.subscription_id AND sub.managed_anime_id = ma.id
JOIN rss_sources AS src ON src.id = m.rss_source_id AND src.subscription_id = sub.id
JOIN releases AS r ON r.id = m.release_id AND r.rss_source_id = src.id
LEFT JOIN download_tasks AS dt ON dt.id = m.download_task_id AND dt.release_id = r.id
WHERE m.id = sqlc.arg(marker_id);

-- name: ListAnchoredArtifacts :many
SELECT id
FROM artifacts
WHERE download_task_id = sqlc.arg(download_task_id)
ORDER BY id;

-- name: ListCorrelatedAnchoredFileOperationsJSON :one
SELECT COALESCE(jsonb_agg(fo.id ORDER BY fo.id), '[]'::jsonb)::text
FROM file_operations AS fo
WHERE fo.correlation_id = (SELECT m.correlation_id FROM one_shot_acquisitions AS m WHERE m.id = sqlc.arg(marker_id))
   OR fo.artifact_file_id IN (
       SELECT af.id FROM artifact_files AS af
       WHERE af.artifact_id = ANY(sqlc.arg(artifact_ids)::text[])
   );

-- name: ListArtifactAnchoredFileOperationsJSON :one
SELECT COALESCE(jsonb_agg(fo.id ORDER BY fo.id), '[]'::jsonb)::text
FROM file_operations AS fo
JOIN artifact_files AS af ON af.id = fo.artifact_file_id
WHERE af.artifact_id = ANY(sqlc.arg(artifact_ids)::text[]);

-- name: GetAnchoredDownloadTaskState :one
SELECT state, COALESCE(failure_reason, '') AS failure_reason
FROM download_tasks
WHERE id = sqlc.arg(download_task_id)
FOR SHARE;

-- name: ListAnchoredArtifactStates :many
SELECT id, state
FROM artifacts
WHERE id = ANY(sqlc.arg(artifact_ids)::text[])
ORDER BY id;

-- name: ListAnchoredFileOperationStates :many
SELECT id, state
FROM file_operations
WHERE id = ANY(sqlc.arg(file_operation_ids)::text[])
ORDER BY id;

-- name: ListAnchoredPlanStates :many
SELECT plan.id, plan.state
FROM postprocessing_plans plan
WHERE plan.artifact_id = ANY(sqlc.arg(artifact_ids)::text[])
ORDER BY plan.id;

-- name: HasLiveOneShotWorkItems :one
SELECT EXISTS (
    SELECT 1
    FROM work_items
    WHERE state IN ('available', 'leased')
      AND (
          (owner_type = 'download_task' AND owner_id = sqlc.arg(download_task_id))
          OR (owner_type = 'release' AND owner_id = sqlc.arg(release_id))
          OR (owner_type = 'artifact' AND owner_id = ANY(sqlc.arg(artifact_ids)::text[]))
          OR (owner_type = 'file_operation' AND owner_id = ANY(sqlc.arg(file_operation_ids)::text[]))
          OR (
              owner_type = 'postprocessing_plan'
              AND owner_id IN (
                  SELECT plan.id FROM postprocessing_plans plan
                  WHERE plan.artifact_id = ANY(sqlc.arg(artifact_ids)::text[])
              )
          )
      )
);

-- name: DeleteOneShotWorkItems :exec
SELECT public.work_item_delete_one_shot(sqlc.arg(download_task_id)::text, sqlc.arg(release_id)::text, sqlc.arg(artifact_ids)::text[], sqlc.arg(file_operation_ids)::text[]);

-- name: DeleteOneShotFileOperations :exec
DELETE FROM file_operations
WHERE id = ANY(sqlc.arg(file_operation_ids)::text[]);

-- name: DeleteOneShotArtifactFiles :exec
DELETE FROM artifact_files
WHERE artifact_id = ANY(sqlc.arg(artifact_ids)::text[]);

-- name: DeleteOneShotArtifacts :exec
DELETE FROM artifacts
WHERE download_task_id = sqlc.arg(download_task_id);

-- name: DeleteOneShotIdempotencyRecords :exec
DELETE FROM idempotency_records
WHERE release_id = sqlc.arg(release_id);

-- name: DeleteOneShotReleaseTorrentInputs :exec
DELETE FROM release_torrent_inputs
WHERE release_id = sqlc.arg(release_id);

-- name: DeleteOneShotReleaseCandidateFacts :exec
DELETE FROM release_candidate_facts
WHERE release_id = sqlc.arg(release_id);

-- name: DeleteOneShotDownloadTasks :exec
DELETE FROM download_tasks
WHERE id = sqlc.arg(download_task_id);

-- name: DeleteOneShotReleases :exec
DELETE FROM releases
WHERE id = sqlc.arg(release_id);

-- name: DeleteOneShotRSSSourceRuntime :exec
DELETE FROM rss_source_runtime
WHERE rss_source_id = sqlc.arg(rss_source_id);

-- name: DeleteOneShotRSSSources :exec
DELETE FROM rss_sources
WHERE id = sqlc.arg(rss_source_id);

-- name: DeleteOneShotSubscriptionLogEntries :exec
DELETE FROM subscription_log_entries
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: DeleteOneShotProviderGroupSelections :exec
DELETE FROM provider_group_selections
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: DeleteOneShotAnimeConfigOverrides :exec
DELETE FROM anime_config_overrides
WHERE managed_anime_id = sqlc.arg(managed_anime_id);

-- name: DeleteOneShotAnimeBusinessConfigs :exec
DELETE FROM anime_business_configs
WHERE managed_anime_id = sqlc.arg(managed_anime_id);

-- name: DeleteOneShotAnimeMetadata :exec
DELETE FROM anime_metadata
WHERE managed_anime_id = sqlc.arg(managed_anime_id);

-- name: DeleteOneShotSubscriptions :exec
DELETE FROM subscriptions
WHERE id = sqlc.arg(subscription_id);

-- name: DeleteOneShotManagedAnime :exec
DELETE FROM managed_anime
WHERE id = sqlc.arg(managed_anime_id);

-- name: RecordManualRetryIdempotency :exec
INSERT INTO idempotency_records(
    kind, idempotency_key, request_hash, owner_type, owner_id,
    failed_work_item_id, result_work_item_id, result_snapshot
)
VALUES (
    'manual_retry', sqlc.arg(idempotency_key), sqlc.arg(request_hash),
    sqlc.arg(owner_type), sqlc.arg(owner_id), sqlc.arg(failed_work_item_id),
    sqlc.arg(result_work_item_id), sqlc.arg(result_snapshot)::jsonb
);
