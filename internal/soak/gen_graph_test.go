//go:build soakscale

package soak

import "context"

// generateGraph writes the accepted-download graphs. Every release flagged
// `chosen` in the release plan gets one acquisition root with its download
// task and, by outcome class, the frozen input, manifest, requirement and
// target columns, the verified artifact with two files, the post-processing
// plan, completed file operations, delivery results, placements and coverage,
// and the download completion. Direct writes under session_replication_role =
// replica; the real accept path is compared against this shape afterwards.
func (g *generator) generateGraph(ctx context.Context) {
	g.execf("plan: acquisitions", `
CREATE TABLE soak_ext.acq AS
SELECT q.*,
       CASE WHEN q.cls IN ('succeeded', 'failed', 'cancelled') THEN q.accepted_at + make_interval(mins => q.dur_min) END AS terminal_at,
       soak_ext.dg('acquisition-input:v1', q.input_text) AS admission_digest
FROM (
  SELECT p.*,
         CASE WHEN p.cls IN ('running', 'inflight') THEN %[1]s - make_interval(mins => 5 + (soak_ext.rnd('acc', p.i) * 600)::int)
              ELSE LEAST(p.discovered_at + interval '2 minutes', %[1]s - interval '4 hours') END AS accepted_at,
         (20 + (soak_ext.rnd('dur', p.i) * 160)::int) AS dur_min,
         (jsonb_build_object('schema_version', 1, 'source', jsonb_build_object('kind', 'subscription_direct', 'release_ref', p.rel_id),
                             'release_title', p.title, 'delivery', jsonb_build_object('operation', 'hardlink', 'collision', 'block'),
                             'config', jsonb_build_object('sections', soak_ext.pad(p.i, 2200)),
                             -- the frozen stage limits download_task_budget_guard reads on every task update
                             'limits', jsonb_build_object('policy_version', 1, 'download_wait_seconds', 604800, 'stall_seconds', 21600,
                                       'submit', jsonb_build_object('wall_seconds', 86400, 'max_attempts', 5, 'attempt_timeout_seconds', 90),
                                       'sync', jsonb_build_object('poll_interval_seconds', 60, 'attempt_timeout_seconds', 60, 'max_consecutive_errors', 5,
                                                                  'max_total_errors', 100, 'max_polls', 10081),
                                       'reconcile', jsonb_build_object('wall_seconds', 86400, 'max_lookups', 100))))::text AS input_text
  FROM (
    SELECT r.gn AS i, 'acq-' || r.id AS id, r.id AS rel_id, r.owner_n, r.aid, r.sid, r.source_id, r.k, r.src_idx, r.title, r.size_bytes,
           r.oneshot, r.discovered_at,
           CASE WHEN r.oneshot THEN (CASE WHEN soak_ext.rnd('cls', r.gn) < 0.5 THEN 'succeeded' ELSE 'running' END)
                WHEN soak_ext.rnd('cls', r.gn) < 0.90 THEN 'succeeded'
                WHEN soak_ext.rnd('cls', r.gn) < 0.95 THEN 'failed'
                WHEN soak_ext.rnd('cls', r.gn) < 0.97 THEN 'running'
                WHEN soak_ext.rnd('cls', r.gn) < 0.98 THEN 'inflight'
                ELSE 'cancelled' END AS cls,
           CASE WHEN r.oneshot THEN 'one_shot' WHEN soak_ext.rnd('kind', r.gn) < 0.97 THEN 'automatic' ELSE 'subscription_add' END AS entry_kind
    FROM soak_ext.rel r WHERE r.chosen
  ) p
) q`, g.baseSQL)
	g.exec("plan: acq pkey", `ALTER TABLE soak_ext.acq ADD PRIMARY KEY (id)`)
	g.exec("plan: acq paths", `
ALTER TABLE soak_ext.acq ADD COLUMN state text, ADD COLUMN dir text, ADD COLUMN f0 text, ADD COLUMN f1 text, ADD COLUMN p0 text, ADD COLUMN p1 text, ADD COLUMN op text`)
	g.execf("plan: acq paths set", `
UPDATE soak_ext.acq SET
  state = CASE cls WHEN 'succeeded' THEN 'succeeded' WHEN 'failed' THEN 'failed' WHEN 'cancelled' THEN 'cancelled' ELSE 'running' END,
  dir = '%[1]s/Soak Anime ' || (owner_n %% %[2]d) || '/Season 01',
  f0 = 'Soak Anime ' || owner_n || ' - S01E' || soak_ext.ep(k) || '.mkv',
  f1 = 'Soak Anime ' || owner_n || ' - S01E' || soak_ext.ep(k + 1) || '.mkv',
  op = CASE WHEN soak_ext.rnd('op', i) < 0.7 THEN 'copy' WHEN soak_ext.rnd('op', i) < 0.9 THEN 'hardlink' ELSE 'move' END`, g.library, maxInt(g.p.PlacementDirs, 1))
	g.exec("plan: acq paths 2", `UPDATE soak_ext.acq SET p0 = dir || '/' || f0, p1 = dir || '/' || f1`)

	// ---- the root and its immutable inputs --------------------------------------
	g.exec("acquisitions", `
INSERT INTO acquisitions(id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash, canonical_request,
                         admission_kind, admission_http_status, admission_receipt, input_schema_version, admission_digest,
                         origin_managed_anime_ref, source_release_ref, display_title, accepted_at, retention_anchor_at,
                         detail_retention_seconds, receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
                         state, terminal_at, terminal_schema, terminal_receipt, record_state, version,
                         preparation_mode, preparation_digest, facts_revision, checked_revision, settlement_error_count,
                         vars_evidence_version, vars_evidence_digest,
                         input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
SELECT id, 'download', entry_kind, 'deployment_owner_v1',
       CASE entry_kind WHEN 'automatic' THEN 'auto_acquire_v1' ELSE 'manual_acquire_v1' END,
       CASE entry_kind WHEN 'automatic' THEN 'auto:' || rel_id ELSE 'manual:' || rel_id END,
       soak_ext.dg('soak-request', id),
       jsonb_build_object('schema_version', 1, 'kind', entry_kind, 'release_ref', rel_id, 'pad', soak_ext.pad(i, 600)),
       'download_queued', 202,
       jsonb_build_object('schema_version', 1, 'acquisition_id', id, 'state', 'queued'),
       1, admission_digest, aid, rel_id,
       jsonb_build_object('state', 'known', 'value', title, 'reason_code', NULL),
       accepted_at, accepted_at, 2592000, 2592000, 5184000,
       GREATEST(accepted_at + interval '2592000 seconds', COALESCE(terminal_at, accepted_at) + interval '5184000 seconds'),
       CASE WHEN terminal_at IS NOT NULL THEN terminal_at + (2592000::double precision * interval '1 second') END,
       state, terminal_at,
       CASE WHEN terminal_at IS NOT NULL THEN 'native_terminal_v1' END,
       CASE cls
         WHEN 'succeeded' THEN jsonb_build_object('schema_version', 1, 'outcome', 'succeeded', 'requirements_status', 'sealed',
              'counts', jsonb_build_object('total', 2, 'required', 2, 'excluded', 0, 'succeeded', 2, 'failed', 0), 'primary_failure', NULL,
              'effect_summary', jsonb_build_object('complete', true, 'has_confirmed', true, 'has_unknown', false),
              'settled_at', to_char(terminal_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
         WHEN 'failed' THEN jsonb_build_object('schema_version', 1, 'outcome', 'failed', 'requirements_status', 'not_reached',
              'counts', NULL, 'primary_failure', jsonb_build_object('stage', 'download_wait', 'reason_code', 'DOWNLOAD_FAILED', 'file_ref', NULL),
              'effect_summary', jsonb_build_object('complete', true, 'has_confirmed', false, 'has_unknown', false),
              'settled_at', to_char(terminal_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
         WHEN 'cancelled' THEN jsonb_build_object('schema_version', 1, 'outcome', 'cancelled', 'requirements_status', 'not_reached',
              'counts', NULL, 'primary_failure', jsonb_build_object('stage', 'download_wait', 'reason_code', 'CANCELLED', 'file_ref', NULL),
              'effect_summary', jsonb_build_object('complete', true, 'has_confirmed', false, 'has_unknown', false),
              'settled_at', to_char(terminal_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
       END,
       'full', 1, 'none', NULL, 0, 0, 0, 1, soak_ext.dg('soak-vars-evidence', id),
       input_text, soak_ext.dg('soak-payload', id), soak_ext.dg('soak-manifest', id), 2, accepted_at
FROM soak_ext.acq`)

	// The frozen input, its digest and the early-sealed manifest live on the root
	// row (PRUNE-046 S4); only the optional vars-evaluation record is separate.
	g.exec("acquisitions: vars evaluation", `
UPDATE acquisitions a SET vars_evaluation_canonical_utf8=q.t, vars_evidence_digest=soak_ext.dg('acquisition-vars-evaluation:v1', q.t)
FROM (SELECT id, jsonb_build_object('schema_version', 1, 'status', 'ok', 'output', jsonb_build_object('actual', jsonb_build_object('title', title)))::text AS t
      FROM soak_ext.acq) q WHERE a.id=q.id`)
	g.exec("acquisition_files", `
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence)
SELECT a.id, 'file-' || f.i, f.i, CASE f.i WHEN 0 THEN a.f0 ELSE a.f1 END, a.size_bytes, 'video', '{}'::jsonb
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i)`)

	// ---- the download task --------------------------------------------------------
	g.execf("download_tasks", `
INSERT INTO download_tasks(id, release_id, qbittorrent_connection_id, provider_correlation, frozen_input, frozen_policy, state, idempotency_key,
                           failure_code, failure_reason, version, created_at, updated_at, correlation_id, acquisition_id,
                           verified_bytes_highwater, payload_total_bytes, last_progress_at, causation_type, causation_id, stage_policy_version,
                           submit_ready_at, submit_wall_until, submit_max_attempts, submit_attempt_timeout_seconds, budget_clock_at,
                           wait_started_at, wait_until, poll_interval_seconds, sync_attempt_timeout_seconds, max_consecutive_errors,
                           max_total_errors, max_polls, stall_seconds, submit_reconcile_wall_seconds, submit_max_lookups)
SELECT 'dt-' || rel_id, rel_id, NULL,
       CASE cls WHEN 'running' THEN NULL ELSE jsonb_build_object('info_hash', soak_ext.hexn('infohash', i, 40), 'category', 'soak') END,
       jsonb_build_object('release_title', title, 'size', size_bytes, 'pad', soak_ext.pad(i, 900)),
       jsonb_build_object('effective_digest', soak_ext.dg('soak-policy', id), 'sections', soak_ext.pad(i + 7, 2600)),
       CASE cls WHEN 'succeeded' THEN 'completed' WHEN 'inflight' THEN 'completed' WHEN 'failed' THEN 'failed' WHEN 'cancelled' THEN 'failed'
                ELSE (CASE WHEN soak_ext.rnd('dts', i) < 0.7 THEN 'downloading' ELSE 'queued' END) END,
       'dt-key:' || rel_id,
       CASE WHEN cls = 'failed' THEN 'qb_download_failed' WHEN cls = 'cancelled' THEN 'cancelled' END,
       CASE WHEN cls = 'failed' THEN 'the torrent could not be downloaded' END,
       1, accepted_at, COALESCE(terminal_at, accepted_at + interval '1 minute'), 'corr-' || rel_id, id,
       CASE WHEN cls IN ('succeeded', 'inflight') THEN size_bytes * 2 END, size_bytes * 2,
       COALESCE(terminal_at, accepted_at + interval '5 minutes'), 'release', rel_id, 1,
       accepted_at, accepted_at + interval '24 hours', 5, 90, accepted_at,
       accepted_at, accepted_at + interval '7 days', 60, 60, 5, 100, 10081, 21600, 86400, 100
FROM soak_ext.acq`)

	// ---- artifacts, files, plans, operations (outcome classes with a verified artifact) ---
	g.exec("artifacts", `
INSERT INTO artifacts(id, download_task_id, inventory_snapshot, fingerprint_snapshot, state, verified_at, version, created_at, updated_at,
                      correlation_id, acquisition_id, inspect_ready_at, inspect_wall_until, inspect_max_attempts,
                      inspect_attempt_timeout_seconds, inspect_attempts_started, inspect_attempt_index)
SELECT 'af-' || rel_id, 'dt-' || rel_id,
       jsonb_build_object('files', jsonb_build_array(jsonb_build_object('path', f0, 'size', size_bytes), jsonb_build_object('path', f1, 'size', size_bytes)),
                          'pad', soak_ext.pad(i, 500)),
       jsonb_build_object('fingerprints', jsonb_build_array(soak_ext.hexn('fp0', i, 64), soak_ext.hexn('fp1', i, 64))),
       'verified', accepted_at + interval '15 minutes', 1, accepted_at + interval '10 minutes', accepted_at + interval '15 minutes',
       'corr-' || rel_id, id, accepted_at + interval '10 minutes', accepted_at + interval '10 minutes' + interval '24 hours', 5, 300, 0, '[]'::jsonb
FROM soak_ext.acq WHERE cls IN ('succeeded', 'inflight')`)
	g.exec("artifact_files", `
INSERT INTO artifact_files(id, artifact_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, created_at,
                           subtitle_binding_state, acquisition_id)
SELECT 'ff-' || a.rel_id || '-' || f.i, 'af-' || a.rel_id, 'file-' || f.i, CASE f.i WHEN 0 THEN a.f0 ELSE a.f1 END,
       '/downloads/' || CASE f.i WHEN 0 THEN a.f0 ELSE a.f1 END, 'video', a.size_bytes,
       jsonb_build_object('algorithm', 'metadata', 'value', 'meta:size=' || a.size_bytes, 'size_bytes', a.size_bytes),
       '{}'::jsonb, a.accepted_at + interval '15 minutes', 'not_applicable', a.id
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls IN ('succeeded', 'inflight')`)

	g.exec("acquisitions: requirements", `
UPDATE acquisitions a SET
 requirements_manifest_digest=soak_ext.dg('soak-manifest', a.id), requirements_selection_digest=soak_ext.dg('soak-selection', a.id),
 requirements_evidence_digest=soak_ext.dg('soak-evidence', a.id), requirements_digest=soak_ext.dg('soak-requirements', a.id),
 requirements_item_count=2, requirements_required_count=2, requirements_sealed_at=a.accepted_at + interval '17 minutes'
FROM soak_ext.acq q WHERE a.id=q.id AND q.cls IN ('succeeded', 'inflight')`)
	g.exec("acquisition_files: requirements", `
UPDATE acquisition_files f SET requirement='required', requirement_reason_code='selected',
       selection_evidence=jsonb_build_object('path', CASE f.file_ref WHEN 'file-0' THEN a.f0 ELSE a.f1 END)
FROM soak_ext.acq a
WHERE f.acquisition_id=a.id AND a.cls IN ('succeeded', 'inflight') AND f.file_ref IN ('file-0', 'file-1')`)
	g.exec("acquisitions: target set", `
UPDATE acquisitions a SET target_requirements_digest=soak_ext.dg('soak-requirements', a.id), target_digest=soak_ext.dg('soak-targets', a.id),
 target_sealed_at=a.accepted_at + interval '17 minutes'
FROM soak_ext.acq q WHERE a.id=q.id AND q.cls IN ('succeeded', 'inflight')`)
	g.exec("acquisition_files: targets", `
UPDATE acquisition_files f SET spec_digest=soak_ext.dg('soak-spec', a.id || right(f.file_ref,1)), intent='ensure_present', operation=a.op,
       canonical_path=CASE f.file_ref WHEN 'file-0' THEN a.p0 ELSE a.p1 END, collision='block',
       target_source_cleanup='{"mode":"none"}'::jsonb,
       render_snapshot=jsonb_build_object('renderer_version', 'soak-render-v1', 'title', 'Soak Anime ' || a.owner_n)
FROM soak_ext.acq a
WHERE f.acquisition_id=a.id AND a.cls IN ('succeeded', 'inflight') AND f.file_ref IN ('file-0', 'file-1')`)

	g.exec("postprocessing_plans", `
INSERT INTO postprocessing_plans(id, artifact_id, artifact_version, state, outcome_code, outcome_reason, input_digest, version, created_at, updated_at,
                                 acquisition_id, sealed_at, requirements_digest, target_digest, plan_digest, planning_requirements_status,
                                 planning_facts_revision, canonical_requirements, canonical_targets, canonical_plan, committed_operations,
                                 committed_bindings, committed_binding_sources, planning_diagnostic,
                                 plan_ready_at, plan_wall_until, plan_max_attempts, plan_attempt_timeout_seconds, plan_attempts_started,
                                 plan_attempt_index)
SELECT 'pp-' || rel_id, 'af-' || rel_id, 1, 'succeeded', 'planned', 'plan committed', admission_digest, 1, accepted_at + interval '16 minutes',
       accepted_at + interval '17 minutes', id, accepted_at + interval '17 minutes', soak_ext.dg('soak-requirements', id),
       soak_ext.dg('soak-targets', id), soak_ext.dg('soak-plan', id), 'sealed', 1,
       '[]', '[]', '{"dependencies":[],"items":[{"diagnostic":{},"evidence":null,"file_ref":"file-0","kind":"execute","reason_code":null,"target_file_ref":"file-0"},{"diagnostic":{},"evidence":null,"file_ref":"file-1","kind":"execute","reason_code":null,"target_file_ref":"file-1"}]}',
       jsonb_build_array(jsonb_build_object('id', 'fo-' || rel_id || '-0'), jsonb_build_object('id', 'fo-' || rel_id || '-1')),
       '[]'::jsonb, '[]'::jsonb, '{"schema_version": 1, "requirements_status": "sealed"}'::jsonb,
       accepted_at + interval '15 minutes', accepted_at + interval '15 minutes' + interval '24 hours', 5, 120, 0, '[]'::jsonb
FROM soak_ext.acq WHERE cls IN ('succeeded', 'inflight')`)
	// file operations: completed for succeeded roots; pending/running/waiting_retry for the in-flight class.
	g.exec("file_operations", `
INSERT INTO file_operations(id, artifact_id, artifact_version, artifact_file_id, operation, source_path, target_path, source_fingerprint,
                            target_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot, state, observed_at,
                            idempotency_key, version, created_at, updated_at, correlation_id, expected_size_bytes, observed_target_size_bytes,
                            intent, acquisition_id, file_ref, required_tag, postprocessing_plan_id, file_ready_at, file_wall_until,
                            file_max_attempts, file_attempt_timeout_seconds, budget_clock_at, file_attempts_started, file_attempt_index,
                            causation_type, causation_id)
SELECT 'fo-' || a.rel_id || '-' || f.i, 'af-' || a.rel_id, 1, 'ff-' || a.rel_id || '-' || f.i, a.op,
       '/downloads/' || CASE f.i WHEN 0 THEN a.f0 ELSE a.f1 END, CASE f.i WHEN 0 THEN a.p0 ELSE a.p1 END,
       jsonb_build_object('algorithm', 'metadata', 'value', 'meta:size=' || a.size_bytes, 'size_bytes', a.size_bytes),
       CASE WHEN a.cls = 'succeeded' THEN jsonb_build_object('algorithm', 'metadata', 'value', 'meta:size=' || a.size_bytes, 'size_bytes', a.size_bytes) END,
       'block', jsonb_build_object('renderer_version', 'soak-render-v1', 'title', 'Soak Anime ' || a.owner_n),
       jsonb_build_object('operation', a.op, 'pad', soak_ext.pad(a.i + f.i, 900)),
       CASE WHEN a.cls = 'succeeded' THEN 'completed'
            WHEN f.i = 0 THEN 'running' ELSE (CASE WHEN soak_ext.rnd('fos', a.i) < 0.5 THEN 'pending' ELSE 'waiting_retry' END) END,
       CASE WHEN a.cls = 'succeeded' THEN a.terminal_at - interval '2 minutes' END,
       'fo-key:' || a.rel_id || ':' || f.i, 1, a.accepted_at + interval '18 minutes', COALESCE(a.terminal_at, a.accepted_at + interval '19 minutes'),
       'corr-' || a.rel_id, a.size_bytes, CASE WHEN a.cls = 'succeeded' THEN a.size_bytes END, 'ensure_present', a.id, 'file-' || f.i, 'required',
       'pp-' || a.rel_id, a.accepted_at + interval '17 minutes', a.accepted_at + interval '17 minutes' + interval '24 hours', 5, 300,
       a.accepted_at + interval '17 minutes', 0, '[]'::jsonb, 'postprocessing_plan', 'pp-' || a.rel_id
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls IN ('succeeded', 'inflight')`)
	g.exec("acquisition_files: plan items", `
UPDATE acquisition_files f SET plan_kind='execute', plan_target_file_ref=f.file_ref,
       plan_operation_ref='fo-' || a.rel_id || '-' || right(f.file_ref,1), plan_diagnostic='{}'::jsonb
FROM soak_ext.acq a
WHERE f.acquisition_id=a.id AND a.cls IN ('succeeded', 'inflight') AND f.file_ref IN ('file-0', 'file-1')`)
	g.exec("acquisition_files: delivery results", `
UPDATE acquisition_files f SET delivery_schema_version=1, outcome='succeeded', completion_mode='executed', effect_class='confirmed',
       final_object=jsonb_build_object('path', CASE f.file_ref WHEN 'file-0' THEN a.p0 ELSE a.p1 END, 'size', a.size_bytes),
       delivery_source_cleanup='not_required', delivery_reason_code=NULL,
       evidence_digest=soak_ext.dg('soak-delivery', a.id || right(f.file_ref,1)),
       operation_id='fo-' || a.rel_id || '-' || right(f.file_ref,1),
       receipt=jsonb_build_object('schema_version', 1, 'outcome', 'succeeded'),
       delivered_at=a.terminal_at - interval '1 minute'
FROM soak_ext.acq a
WHERE f.acquisition_id=a.id AND a.cls = 'succeeded' AND f.file_ref IN ('file-0', 'file-1')`)
	g.exec("download_completions", `
INSERT INTO download_completions(id, acquisition_id, action, info_hash, provider_ref, state, result_code, version, created_at, updated_at, completed_at)
SELECT 'dc-' || rel_id, id, 'seed', soak_ext.hexn('infohash', i, 40), 'qb:' || soak_ext.hexn('infohash', i, 40), 'succeeded', 'completed', 1,
       terminal_at, terminal_at, terminal_at
FROM soak_ext.acq WHERE cls = 'succeeded'`)

	// ---- placements: one per completed file, with the management handoff that adopted it ----
	g.exec("placements", `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint,
                       facts_version, verification_state, state, version, created_at, updated_at, correlation_id, replacement_eligible)
SELECT 'pl-' || a.rel_id || '-' || f.i, CASE f.i WHEN 0 THEN a.p0 ELSE a.p1 END, a.aid, 'file_operation', 'fo-' || a.rel_id || '-' || f.i,
       soak_ext.hexn('fp' || f.i, a.i, 64), soak_ext.hexn('fp' || f.i, a.i, 64), 'facts-v1',
       CASE WHEN soak_ext.rnd('ver', a.i * 2 + f.i) < 0.92 THEN 'verified' WHEN soak_ext.rnd('ver', a.i * 2 + f.i) < 0.96 THEN 'unverified' ELSE 'excluded' END,
       CASE WHEN soak_ext.rnd('plst', a.i * 2 + f.i) < 0.95 THEN 'active' ELSE 'superseded' END,
       1, a.terminal_at, a.terminal_at, 'corr-' || a.rel_id, true
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls = 'succeeded'`)
	g.exec("placement_coverage", `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
SELECT 'pl-' || a.rel_id || '-' || f.i, 'tv:e' || (a.k + f.i), 0, 'facts-v1', 'fo-' || a.rel_id || '-' || f.i
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls = 'succeeded'`)
	g.exec("placement_obligations", `
INSERT INTO placement_obligations(file_operation_id, state, attempts, max_attempts, created_at, updated_at, settled_at)
SELECT 'fo-' || a.rel_id || '-' || f.i, 'admitted', 1, 3, a.terminal_at, a.terminal_at, a.terminal_at
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls = 'succeeded'`)
	// ---- the work items the accept path and the stages leave behind -------------------------
	// Terminal (retained) for acquisitions accepted in the last 7 days: submit, settle, plan and
	// file execution items as the real path writes them; live file_execute items for the in-flight class.
	g.execf("work_item_outcomes: graph stages (retained)", `
INSERT INTO work_item_outcomes(id, owner_type, owner_id, acquisition_id, kind, state, available_at,
                       fencing_token, idempotency_key, attempt, max_attempts, input_snapshot, expected_owner_version, terminal_at,
                       retention_days, retention_until, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT w.id, w.owner_type, w.owner_id, w.acq_id, w.kind, 'succeeded', w.at0,
       CASE WHEN w.kind IN ('postprocess_plan') THEN 1 ELSE 0 END, w.ikey, CASE WHEN w.kind IN ('postprocess_plan') THEN 1 ELSE 0 END, w.maxa,
       w.snap, 1, w.tat, 7, w.tat + (7::double precision * interval '1 day'), w.at0, w.tat,
       CASE WHEN w.owner_type <> 'acquisition' THEN w.aid END, CASE WHEN w.owner_type <> 'acquisition' THEN 1 END
FROM (
  SELECT 'wi-qsub-' || a.rel_id AS id, 'download_task' AS owner_type, 'dt-' || a.rel_id AS owner_id, a.id AS acq_id, 'qb_submit' AS kind,
         'dt-key:' || a.rel_id AS ikey, 3 AS maxa, '{}'::jsonb AS snap, a.accepted_at AS at0, a.accepted_at + interval '1 minute' AS tat, a.aid AS aid
  FROM soak_ext.acq a WHERE a.accepted_at >= %[1]s - interval '7 days'
  UNION ALL
  SELECT 'wi-st-' || a.rel_id, 'acquisition', a.id, a.id, 'acquisition_settle', 'settle:' || a.id, 3, '{"schema_version": 1}'::jsonb, a.accepted_at,
         COALESCE(a.terminal_at, a.accepted_at) + interval '1 minute', a.aid
  FROM soak_ext.acq a WHERE a.accepted_at >= %[1]s - interval '7 days' AND a.cls IN ('succeeded', 'failed', 'cancelled')
  UNION ALL
  SELECT 'wi-pp-' || a.rel_id, 'postprocessing_plan', 'pp-' || a.rel_id, a.id, 'postprocess_plan', 'plan:pp-' || a.rel_id, 3, '{}'::jsonb,
         a.accepted_at + interval '15 minutes', a.accepted_at + interval '17 minutes', a.aid
  FROM soak_ext.acq a WHERE a.accepted_at >= %[1]s - interval '7 days' AND a.cls = 'succeeded'
  UNION ALL
  SELECT 'wi-fe-' || a.rel_id || '-' || f.i, 'file_operation', 'fo-' || a.rel_id || '-' || f.i, a.id, 'file_execute', 'fo-key:' || a.rel_id || ':' || f.i, 5,
         jsonb_build_object('artifact_id', 'af-' || a.rel_id, 'operation_id', 'fo-' || a.rel_id || '-' || f.i, 'artifact_file_id', 'ff-' || a.rel_id || '-' || f.i),
         a.accepted_at + interval '17 minutes', a.terminal_at - interval '1 minute', a.aid
  FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.accepted_at >= %[1]s - interval '7 days' AND a.cls = 'succeeded'
) w`, g.baseSQL)
	g.execf("work_items: file_execute (live, in-flight)", `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, available_at, fencing_token, idempotency_key, attempt,
                       max_attempts, input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-fe-' || a.rel_id || '-' || f.i, 'file_operation', 'fo-' || a.rel_id || '-' || f.i, a.id, 'file_execute', 'available',
       a.accepted_at + interval '18 minutes', 0, 'fo-key:' || a.rel_id || ':' || f.i, 0, 5,
       jsonb_build_object('artifact_id', 'af-' || a.rel_id, 'operation_id', 'fo-' || a.rel_id || '-' || f.i, 'artifact_file_id', 'ff-' || a.rel_id || '-' || f.i),
       1, 7, a.accepted_at + interval '17 minutes', a.accepted_at + interval '17 minutes', a.aid, 1
FROM soak_ext.acq a CROSS JOIN generate_series(0, 1) f(i) WHERE a.cls = 'inflight'`)

	// ---- lifecycle markers (PRUNE-046) ----------------------------------------------------
	// Triggers do not fire under the replica role, so the markers the real path
	// keeps are set here. compact_pinned: a Release still points at the root's
	// task, or a placement obligation exists. completion_open: every succeeded
	// root already has its completion. Activity markers: facts older than the
	// consumer window were logged long ago; facts inside the window stay unlogged
	// and the drain logs them (one-shot facts included).
	g.exec("acquisitions: compact_pinned", `
UPDATE acquisitions r SET compact_pinned = true
FROM soak_ext.acq a
WHERE r.id = a.id
  AND (EXISTS (SELECT 1 FROM download_tasks t JOIN releases rel ON rel.download_task_id = t.id WHERE t.acquisition_id = r.id)
       OR EXISTS (SELECT 1 FROM file_operations o JOIN placement_obligations po ON po.file_operation_id = o.id WHERE o.acquisition_id = r.id))`)
	g.execf("acquisitions: activity_logged_at", `
UPDATE acquisitions r SET activity_logged_at = r.terminal_at + interval '5 seconds'
FROM soak_ext.acq a
WHERE r.id = a.id AND r.terminal_at < %[1]s - interval '14 days'`, g.baseSQL)
	g.execf("download_tasks: activity markers", `
UPDATE download_tasks t SET
  activity_submit_logged_at = CASE WHEN t.provider_correlation IS NOT NULL THEN t.updated_at + interval '5 seconds' END,
  activity_terminal_logged_at = CASE WHEN t.state IN ('completed', 'failed') THEN t.updated_at + interval '5 seconds' END
FROM soak_ext.acq a
WHERE t.acquisition_id = a.id AND t.updated_at < %[1]s - interval '14 days'`, g.baseSQL)
	g.execf("artifacts: activity_logged_at", `
UPDATE artifacts x SET activity_logged_at = x.verified_at + interval '5 seconds'
FROM soak_ext.acq a
WHERE x.acquisition_id = a.id AND x.verified_at < %[1]s - interval '14 days'`, g.baseSQL)
	g.execf("acquisition_files: activity_logged_at", `
UPDATE acquisition_files d SET activity_logged_at = d.delivered_at + interval '5 seconds'
FROM soak_ext.acq a
WHERE d.acquisition_id = a.id AND d.outcome IS NOT NULL AND d.delivered_at < %[1]s - interval '14 days'`, g.baseSQL)
}
