-- Forward-only migration 098: offset alignment competes; post-import
-- correction (owner rulings 2026-10-28 / 2026-10-29).
--
-- 1. An offset change is an identity change: the transaction that changes a
--    link's offset rewrites the coverage key of the link's single-episode
--    placements and touches no file. The keys then go through retention
--    first (losers are deleted by the existing retention path) and the
--    alignment routing moves the keepers under the global collision policy.
--    offset_alignment_changes is one offset change of one link: the
--    re-keyed count, the files retention deleted, and the state of its one
--    activity record (comparing -> aligning -> recorded).
-- 2. alignment_requests are the placements a scan must align whatever the
--    instance's alignment setting: a newly imported file (post-import
--    correction, written with its placement) and a re-keyed file (with the
--    offset change it belongs to). The alignment consumer resolves a request
--    with the scan that started after it; outcome is what the routing did
--    with an offset request.
-- 3. The old offset executor is retired: an offset plan that never applied
--    (open, or blocked on an occupied or conflicting target) of a generation
--    that is still current carries forward as a re-key plus a request, then
--    every offset_migration_execute item and every plan and generation row is
--    removed and the work item kind is dropped. The two tables stay (the local
--    cleanup function and the work item purge functions name them) without
--    runtime write privileges.

CREATE TABLE offset_alignment_changes (
    id TEXT NOT NULL,
    rss_source_id TEXT NOT NULL,
    source_version BIGINT NOT NULL,
    old_offset INTEGER NOT NULL,
    new_offset INTEGER NOT NULL,
    rekeyed_count INTEGER NOT NULL DEFAULT 0,
    deleted_count INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'comparing',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT offset_alignment_changes_pkey PRIMARY KEY (id),
    CONSTRAINT offset_alignment_changes_source_version_key UNIQUE (rss_source_id, source_version),
    CONSTRAINT offset_alignment_changes_rss_source_id_fkey FOREIGN KEY (rss_source_id) REFERENCES rss_sources (id) ON DELETE CASCADE,
    CONSTRAINT offset_alignment_changes_counts_check CHECK (rekeyed_count >= 0 AND deleted_count >= 0),
    CONSTRAINT offset_alignment_changes_state_check CHECK (state IN ('comparing', 'aligning', 'recorded'))
);
COMMENT ON TABLE offset_alignment_changes IS 'One offset change of one RSS link: the placements it re-keyed, the files retention deleted after comparing them, and the state of its one activity record.';

CREATE TABLE alignment_requests (
    placement_id TEXT NOT NULL,
    offset_change_id TEXT,
    outcome TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT alignment_requests_pkey PRIMARY KEY (placement_id),
    CONSTRAINT alignment_requests_placement_id_fkey FOREIGN KEY (placement_id) REFERENCES placements (id) ON DELETE CASCADE,
    CONSTRAINT alignment_requests_offset_change_id_fkey FOREIGN KEY (offset_change_id) REFERENCES offset_alignment_changes (id) ON DELETE CASCADE,
    CONSTRAINT alignment_requests_outcome_check CHECK (outcome IS NULL OR (offset_change_id IS NOT NULL AND outcome IN ('moved', 'deleted', 'versioned', 'overwritten', 'skipped', 'unchanged')))
);
CREATE INDEX alignment_requests_offset_change_idx ON alignment_requests (offset_change_id) WHERE offset_change_id IS NOT NULL;
COMMENT ON TABLE alignment_requests IS 'Placements the next scan aligns regardless of the alignment setting: a new import (post-import correction) or a file an offset change re-keyed.';

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE offset_alignment_changes, alignment_requests TO bansoko_runtime;

-- Carry the unapplied offset plans forward as re-keys plus requests.
WITH open_plans AS (
    SELECT DISTINCT ON (op.placement_id) op.placement_id, op.new_coverage_key
    FROM offset_migration_plans op
    JOIN offset_migration_generations g ON g.id = op.generation_id
    WHERE g.state <> 'superseded'
      AND op.new_coverage_key IS NOT NULL
      AND (op.state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')
           OR (op.state = 'blocked' AND op.reason_code IN ('target_occupied', 'target_conflict')))
    ORDER BY op.placement_id, g.created_at DESC, g.id DESC
),
rekeyed AS (
    UPDATE placement_coverage c
    SET observation_key = o.new_coverage_key
    FROM open_plans o
    JOIN placements p ON p.id = o.placement_id
    WHERE c.placement_id = o.placement_id
      AND c.observation_key <> o.new_coverage_key
      AND p.state = 'active' AND p.verification_state = 'verified' AND p.replacement_eligible
      AND (SELECT count(*) FROM placement_coverage x WHERE x.placement_id = o.placement_id) = 1
    RETURNING c.placement_id
),
bumped AS (
    UPDATE placements p
    SET version = p.version + 1, updated_at = CURRENT_TIMESTAMP
    FROM rekeyed r
    WHERE p.id = r.placement_id
    RETURNING p.id
)
INSERT INTO alignment_requests (placement_id)
SELECT id FROM bumped
ON CONFLICT (placement_id) DO NOTHING;

-- Dropping a retired kind is a schema change, not a cleanup: the retirement
-- delete fence is suspended for this one statement (as in 081).
ALTER TABLE work_items DISABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_items WHERE kind = 'offset_migration_execute';
ALTER TABLE work_items ENABLE TRIGGER work_items_retirement_delete_fence;
DELETE FROM work_item_outcomes WHERE kind = 'offset_migration_execute';
DELETE FROM offset_migration_plans;
DELETE FROM offset_migration_generations;

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'subscription_competition', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'replacement_deletion' AND kind = 'replacement_delete') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

REVOKE INSERT, UPDATE, DELETE ON TABLE offset_migration_plans, offset_migration_generations FROM bansoko_runtime;
