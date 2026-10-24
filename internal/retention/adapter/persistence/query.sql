-- Discovery-history retention is static and bounded. It keeps discovery history for 30 days of inactivity: a
-- subscription stopped or a managed anime retired for at least 30 days. A
-- release that reached a download task is acquisition evidence and is never
-- selected, even after a failed retry detached the release's own pointer.

-- name: PurgeExpiredDiscoveryReleases :one
WITH due AS (
    SELECT rel.id
    FROM releases AS rel
    JOIN rss_sources AS src ON src.id = rel.rss_source_id
    JOIN subscriptions AS sub ON sub.id = src.subscription_id
    JOIN managed_anime AS anime ON anime.id = sub.managed_anime_id
    WHERE rel.download_task_id IS NULL
      AND (
          (anime.lifecycle_state = 'retired'
           AND anime.retired_at <= CURRENT_TIMESTAMP - INTERVAL '30 days')
          OR (sub.state = 'stopped'
              AND sub.updated_at <= CURRENT_TIMESTAMP - INTERVAL '30 days')
      )
      -- Acquisition evidence keeps its release: a detached failed task still
      -- names the release, and an execution record still links it.
      AND NOT EXISTS (
          SELECT 1 FROM download_tasks AS task WHERE task.release_id = rel.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM acquisitions AS root WHERE root.source_release_ref = rel.id
      )
      -- A one-shot marker keeps its release until its own reclamation runs.
      AND NOT EXISTS (
          SELECT 1 FROM one_shot_acquisitions AS marker
          WHERE marker.release_id = rel.id AND marker.state <> 'cleaned'
      )
      -- A placement still names the release as its quality facts: it is
      -- library evidence, not discovery history.
      AND NOT EXISTS (
          SELECT 1 FROM placements AS placement WHERE placement.quality_facts_ref = rel.id
      )
      -- A run-log row has its own 30-day bound; wait for it rather than
      -- shortening that evidence.
      AND NOT EXISTS (
          SELECT 1 FROM subscription_log_entries AS entry WHERE entry.release_id = rel.id
      )
    ORDER BY rel.discovered_at, rel.id
    -- Fail closed against a concurrent writer: the evaluation and retry paths
    -- lock the release row before they write, and an insert that names the
    -- release through its foreign key waits for this lock. Holding it until
    -- commit means no new child row can appear between the probes above and
    -- the delete below.
    LIMIT $1
    FOR UPDATE OF rel
),
deleted_retries AS (
    DELETE FROM idempotency_records AS record
    WHERE record.kind = 'release_retry'
      AND record.release_id IN (SELECT id FROM due)
    RETURNING record.release_id
),
deleted_torrent_inputs AS (
    DELETE FROM release_torrent_inputs AS input
    WHERE input.release_id IN (SELECT id FROM due)
    RETURNING input.release_id
),
deleted_candidate_facts AS (
    DELETE FROM release_candidate_facts AS fact
    WHERE fact.release_id IN (SELECT id FROM due)
    RETURNING fact.release_id
),
deleted_media_files AS (
    DELETE FROM media_fact_files AS file
    WHERE file.snapshot_id IN (
        SELECT snapshot.id
        FROM media_fact_snapshots AS snapshot
        WHERE snapshot.release_id IN (SELECT id FROM due)
    )
    RETURNING file.snapshot_id
),
deleted_media_snapshots AS (
    DELETE FROM media_fact_snapshots AS snapshot
    WHERE snapshot.release_id IN (SELECT id FROM due)
    RETURNING snapshot.release_id
),
-- acquisition_targets is not deleted here: its FK to releases is the one
-- cascade in the Release graph, and the runtime role holds no DELETE on it
-- (owner functions are its only writers). The release delete cascades them.
deleted_releases AS (
    DELETE FROM releases AS rel
    WHERE rel.id IN (SELECT id FROM due)
    RETURNING rel.id
)
SELECT count(*)::bigint AS deleted FROM deleted_releases;

-- Maintenance-record retention (library content is permanent, maintenance
-- records expire after 30 days). Each cleaner deletes
-- only rows that are terminal and that no live row or in-flight work still
-- reads; ordering is the terminal clock so a backlog drains oldest first.

-- name: PurgeExpiredIdempotencyRecords :one
-- HTTP replay records of every kind. Nothing references a record (no FK points
-- at the table) and every kind's retried request after the window is decided
-- by its owner's own state/version checks, so the record only shortens how
-- long an identical retry replays the first answer.
-- The table has no primary key; idempotency_records_identity_uq (NULLS NOT
-- DISTINCT) makes the identity tuple unique, so the delete matches on it.
WITH due AS (
    SELECT record.kind, record.release_id, record.plugin_target_config_id,
           record.owner_type, record.owner_id, record.idempotency_key
    FROM idempotency_records AS record
    WHERE record.created_at <= CURRENT_TIMESTAMP - INTERVAL '30 days'
    ORDER BY record.created_at, record.kind, record.idempotency_key
    LIMIT $1
    FOR UPDATE OF record
),
deleted AS (
    DELETE FROM idempotency_records AS record
    USING due
    WHERE record.kind = due.kind
      AND record.idempotency_key = due.idempotency_key
      AND record.release_id IS NOT DISTINCT FROM due.release_id
      AND record.plugin_target_config_id IS NOT DISTINCT FROM due.plugin_target_config_id
      AND record.owner_type IS NOT DISTINCT FROM due.owner_type
      AND record.owner_id IS NOT DISTINCT FROM due.owner_id
    RETURNING 1
)
SELECT count(*)::bigint AS deleted FROM deleted;

-- name: PurgeExpiredNotificationAdmissions :one
-- The notification dedupe ledger outlives the discovery history it dedupes
-- (30 days) by a 60-day margin. Only settled admissions are eligible; a
-- pending admission is live resolver work.
WITH due AS (
    SELECT admission.id
    FROM notification_admissions AS admission
    WHERE admission.state IN ('resolved', 'admission_failed')
      AND admission.created_at <= CURRENT_TIMESTAMP - INTERVAL '90 days'
    ORDER BY admission.created_at, admission.id
    LIMIT $1
    FOR UPDATE OF admission
),
deleted AS (
    DELETE FROM notification_admissions AS admission
    WHERE admission.id IN (SELECT id FROM due)
    RETURNING admission.id
)
SELECT count(*)::bigint AS deleted FROM deleted;

-- name: PurgeExpiredAlignmentOperations :one
-- A settled alignment of a superseded scan generation. The idempotency key
-- embeds the generation, and a newer generation already exists for the anime,
-- so no planner can admit the same key again; the companion probe reads only
-- the current generation. A row with any WorkItem (live or a retained
-- outcome) waits for that item to expire first.
WITH due AS (
    SELECT op.id
    FROM alignment_operations AS op
    JOIN reconciliation_scans AS scan ON scan.managed_anime_id = op.managed_anime_id
    WHERE op.state IN ('completed', 'blocked', 'failed', 'canceled')
      AND op.completed_at <= CURRENT_TIMESTAMP - INTERVAL '30 days'
      AND op.scan_generation < scan.generation
      AND NOT EXISTS (
          SELECT 1 FROM work_items AS item
          WHERE item.owner_type = 'alignment_operation' AND item.owner_id = op.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM work_item_outcomes AS outcome
          WHERE outcome.owner_type = 'alignment_operation' AND outcome.owner_id = op.id
      )
    ORDER BY op.completed_at, op.id
    LIMIT $1
    FOR UPDATE OF op
),
deleted AS (
    DELETE FROM alignment_operations AS op
    WHERE op.id IN (SELECT id FROM due)
    RETURNING op.id
)
SELECT count(*)::bigint AS deleted FROM deleted;

-- name: PurgeSupersededReconciliationFiles :one
-- Observation rows of scan generations nothing reads any more. Per anime the
-- current generation, the newest earlier generation with observations (the
-- next calibration's missing-file input) and the newest calibrated generation
-- (the path-diff default) are kept; older generations go once they are 30 days
-- old and no file-maintenance work or open alignment of the anime is in
-- flight (a late calibration of a historical generation reads its rows).
WITH floors AS (
    SELECT scan.managed_anime_id,
           LEAST(
               scan.generation,
               COALESCE((
                   SELECT max(prev.generation) FROM reconciliation_files AS prev
                   WHERE prev.managed_anime_id = scan.managed_anime_id
                     AND prev.generation < scan.generation AND NOT prev.derived_missing
               ), scan.generation),
               COALESCE((
                   SELECT max(cal.generation) FROM reconciliation_files AS cal
                   WHERE cal.managed_anime_id = scan.managed_anime_id
                     AND cal.disposition IS NOT NULL
               ), scan.generation)
           ) AS keep_from
    FROM reconciliation_scans AS scan
    WHERE NOT EXISTS (
            SELECT 1 FROM work_items AS item
            WHERE item.owner_type = 'file_maintenance' AND item.owner_id = scan.managed_anime_id
        )
      AND NOT EXISTS (
            SELECT 1 FROM alignment_operations AS op
            WHERE op.managed_anime_id = scan.managed_anime_id
              AND op.state IN ('planned', 'ready', 'running')
        )
),
due AS (
    SELECT file.managed_anime_id, file.generation, file.file_path
    FROM reconciliation_files AS file
    JOIN floors ON floors.managed_anime_id = file.managed_anime_id
    WHERE file.generation < floors.keep_from
      AND file.created_at <= CURRENT_TIMESTAMP - INTERVAL '30 days'
    ORDER BY file.created_at, file.managed_anime_id, file.generation, file.file_path
    LIMIT $1
    FOR UPDATE OF file
),
deleted AS (
    DELETE FROM reconciliation_files AS file
    USING due
    WHERE file.managed_anime_id = due.managed_anime_id
      AND file.generation = due.generation
      AND file.file_path = due.file_path
    RETURNING 1
)
SELECT count(*)::bigint AS deleted FROM deleted;
