-- Forward-only migration 063: the three meanings of "retention" get distinct
-- names (10-26 replacement/retention design §8, lane L5).
--
-- One word carried three unrelated meanings in this schema, so every reader
-- had to infer which one a name meant:
--
--   (A) washing — keeping the better of several files of one episode. The
--       policy, its version columns, the API section and the deletion
--       obligation all called it "retention".
--   (B) record retention — the bounded lifetime of maintenance and history
--       rows (the janitor whose Go package is internal/retention). Its API
--       field `retention_until` already says what it means, so no column
--       changes here; the Go package moves to internal/recordretention.
--   (C) one-shot reclaim — the temporary owner graph of one one-shot
--       acquisition. Its marker state was `retention_pending`.
--
-- This migration renames (A) and (C) in the schema. It changes no behaviour:
-- every renamed object keeps its constraints and rows, and every stored
-- function keeps its body except for the names it mentions.
--
-- (A) washing -> replacement
--   * cleanup_operations -> replacement_deletions, with
--     column retention_mode -> admission_mode and every constraint, index and
--     foreign-key name that carried the old relation name
--   * anime_business_configs.{retention_version,
--     retention_source_default_version} -> *.replacement_*, and the
--     anime_business_configs_retention_check -> *_replacement_check
--   * the frozen business snapshot's `retention` / `retention_version` keys ->
--     `replacement` / `replacement_version`, with every stored digest
--     recomputed and every candidate draft/revision rewritten (the same rule
--     058 followed for cleanup_policy)
--   * global_config_sections section_name 'retention' -> 'replacement'
--   * work_items/work_item_outcomes owner_type 'cleanup_operation' ->
--     'replacement_deletion' and kind 'cleanup_delete' -> 'replacement_delete'
--   * retention_quality_profile_valid() -> replacement_quality_profile_valid()
--
-- (C) one-shot reclaim
--   * one_shot_acquisitions.state 'retention_pending' -> 'reclaim_pending'
--
-- Migrations carry no data-preservation obligation before release, but a
-- rename is cheap and keeps a staged environment readable, so the stored
-- values are translated instead of cleared.

-- ---------------------------------------------------------------------------
-- (A) The deletion obligation: owner/kind values, then the relation
-- ---------------------------------------------------------------------------

UPDATE work_items SET owner_type = 'replacement_deletion' WHERE owner_type = 'cleanup_operation';
UPDATE work_items SET kind = 'replacement_delete' WHERE kind = 'cleanup_delete';
UPDATE work_item_outcomes SET owner_type = 'replacement_deletion' WHERE owner_type = 'cleanup_operation';
UPDATE work_item_outcomes SET kind = 'replacement_delete' WHERE kind = 'cleanup_delete';

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'replacement_fallback', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'replacement_deletion' AND kind = 'replacement_delete') OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

ALTER TABLE cleanup_operations RENAME COLUMN retention_mode TO admission_mode;
ALTER TABLE cleanup_operations RENAME TO replacement_deletions;

-- A RENAME keeps constraint and index names, so each one still says
-- "cleanup_operations". Rename them with the relation.
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_pkey TO replacement_deletions_pkey;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_idempotency_key_key TO replacement_deletions_idempotency_key_key;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_companion_id_fkey TO replacement_deletions_companion_id_fkey;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_attempt_check TO replacement_deletions_attempt_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_causation_id_check TO replacement_deletions_causation_id_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_causation_type_check TO replacement_deletions_causation_type_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_correlation_id_check TO replacement_deletions_correlation_id_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_coverage_keys_check TO replacement_deletions_coverage_keys_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_failure_code_check TO replacement_deletions_failure_code_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_id_check TO replacement_deletions_id_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_idempotency_key_check TO replacement_deletions_idempotency_key_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_managed_anime_id_check TO replacement_deletions_managed_anime_id_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_placement_id_check TO replacement_deletions_placement_id_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_reason_check TO replacement_deletions_reason_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_replacement_ref_check TO replacement_deletions_replacement_ref_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_source_fingerprint_check TO replacement_deletions_source_fingerprint_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_source_path_check TO replacement_deletions_source_path_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_state_check TO replacement_deletions_state_check;
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_version_check TO replacement_deletions_version_check;
-- admission_mode is a narrower name than the column it replaced only in
-- wording; the accepted values are unchanged.
ALTER TABLE replacement_deletions RENAME CONSTRAINT cleanup_operations_retention_mode_check TO replacement_deletions_admission_mode_check;

-- ---------------------------------------------------------------------------
-- (A) The instance axis and the frozen business snapshot
-- ---------------------------------------------------------------------------

ALTER TABLE anime_business_configs RENAME COLUMN retention_source_default_version TO replacement_source_default_version;
ALTER TABLE anime_business_configs RENAME COLUMN retention_version TO replacement_version;
ALTER TABLE anime_business_configs DROP CONSTRAINT anime_business_configs_retention_check;

-- The quality-profile validator keeps its body (copied verbatim from the
-- baseline); only its name said "retention". It is created under the new name
-- rather than renamed in place, because sqlc's catalog cannot follow a function
-- rename. The old one is dropped at the end, once every constraint and
-- function that called it has been rebuilt on the new name.
CREATE FUNCTION replacement_quality_profile_valid(profile jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
 item jsonb;
 entry jsonb;
 val jsonb;
 keys text[] := ARRAY[]::text[];
BEGIN
 IF profile IS NULL OR jsonb_typeof(profile) IS DISTINCT FROM 'object'
    OR (SELECT count(*) FROM jsonb_object_keys(profile)) <> 2
    OR NOT (profile ?& ARRAY['version','items'])
    OR profile->>'version' IS DISTINCT FROM 'profile-v1'
    OR jsonb_typeof(profile->'items') IS DISTINCT FROM 'array'
    OR jsonb_array_length(profile->'items') > 12 THEN
  RETURN FALSE;
 END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(profile->'items') LOOP
  IF jsonb_typeof(item) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
     OR NOT (item ?& ARRAY['key','ranks'])
     OR jsonb_typeof(item->'key') IS DISTINCT FROM 'string'
     OR item->>'key' NOT IN ('subgroup.score','release.lang','release.resolution','release.revision',
                             'anibt.resolution','anibt.language','anibt.subtitle','anibt.format',
                             'rt.resolution','rt.sub_lang','rt.source','rt.codec')
     OR item->>'key' = ANY(keys)
     OR jsonb_typeof(item->'ranks') IS DISTINCT FROM 'array'
     OR jsonb_array_length(item->'ranks') > 64 THEN
   RETURN FALSE;
  END IF;
  keys := array_append(keys, item->>'key');
  FOR entry IN SELECT value FROM jsonb_array_elements(item->'ranks') LOOP
   IF jsonb_typeof(entry) IS DISTINCT FROM 'array' OR jsonb_array_length(entry) NOT BETWEEN 1 AND 8 THEN
    RETURN FALSE;
   END IF;
   FOR val IN SELECT value FROM jsonb_array_elements(entry) LOOP
    IF jsonb_typeof(val) IS DISTINCT FROM 'string' OR btrim(val #>> '{}') = '' OR octet_length(val #>> '{}') > 128 THEN
     RETURN FALSE;
    END IF;
   END LOOP;
  END LOOP;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN
 RETURN FALSE;
END;
$$;

ALTER TABLE anime_business_configs ADD CONSTRAINT anime_business_configs_replacement_check CHECK (
    (admission_mode IS NULL AND quality_profile IS NULL AND replacement_source_default_version IS NULL AND replacement_version IS NULL)
    OR (admission_mode IN ('all', 'per_group', 'single_copy')
        AND (replacement_quality_profile_valid(quality_profile) AND pg_column_size(quality_profile) <= 65536) IS TRUE
        AND replacement_source_default_version IS NOT NULL AND replacement_source_default_version >= 1
        AND replacement_version IS NOT NULL AND replacement_version >= 1)
);

-- The frozen business snapshot carries the same two names as keys. Every
-- stored digest is the canonical hash of the stored body, so each one is
-- recomputed from the rewritten body.
UPDATE anime_business_configs
SET business_snapshot = jsonb_set(
        jsonb_set(business_snapshot - 'retention', '{replacement}', business_snapshot->'retention'),
        '{replacement_version}',
        COALESCE(business_snapshot->'retention_version', '0'::jsonb))
    - 'retention_version',
    business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
        jsonb_set(
            jsonb_set(business_snapshot - 'retention', '{replacement}', business_snapshot->'retention'),
            '{replacement_version}',
            COALESCE(business_snapshot->'retention_version', '0'::jsonb))
        - 'retention_version'), 'UTF8'), 'sha256'), 'hex')
WHERE business_snapshot ? 'retention';

-- Candidate drafts: the candidate guard refuses a linked draft rewrite, so the
-- migration-only fixture switch lets this one shape rewrite through; the
-- defaults trigger recomputes draft_digest from the rewritten draft.
SET LOCAL bansoko.candidate_fixture = 'on';
UPDATE anime_config_candidates
SET draft = jsonb_set(
        jsonb_set(draft, '{business}', jsonb_set(
            jsonb_set((draft->'business') - 'retention', '{replacement}', draft->'business'->'retention'),
            '{replacement_version}',
            COALESCE(draft->'business'->'retention_version', '0'::jsonb)) - 'retention_version'),
        '{business_digest}',
        to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
            jsonb_set(
                jsonb_set((draft->'business') - 'retention', '{replacement}', draft->'business'->'retention'),
                '{replacement_version}',
                COALESCE(draft->'business'->'retention_version', '0'::jsonb))
            - 'retention_version'), 'UTF8'), 'sha256'), 'hex'))),
    draft_digest = ''
WHERE draft ? 'business' AND draft->'business' ? 'retention';
RESET bansoko.candidate_fixture;

-- Candidate revisions are append-only; their guards are lifted for this one
-- shape rewrite only. The business snapshot's effective sections (and so
-- effective_digest and every current_revision_digest mirror) are unchanged.
ALTER TABLE anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
UPDATE anime_config_candidate_revisions
SET business_snapshot = jsonb_set(
        jsonb_set(business_snapshot - 'retention', '{replacement}', business_snapshot->'retention'),
        '{replacement_version}',
        COALESCE(business_snapshot->'retention_version', '0'::jsonb))
    - 'retention_version',
    business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
        jsonb_set(
            jsonb_set(business_snapshot - 'retention', '{replacement}', business_snapshot->'retention'),
            '{replacement_version}',
            COALESCE(business_snapshot->'retention_version', '0'::jsonb))
        - 'retention_version'), 'UTF8'), 'sha256'), 'hex')
WHERE business_snapshot ? 'retention';
WITH rewritten AS (
    SELECT candidate_id, revision, draft_digest AS old_digest,
           'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex') AS old_canonical,
           jsonb_set(
               jsonb_set(draft, '{business}', jsonb_set(
                   jsonb_set((draft->'business') - 'retention', '{replacement}', draft->'business'->'retention'),
                   '{replacement_version}',
                   COALESCE(draft->'business'->'retention_version', '0'::jsonb)) - 'retention_version'),
               '{business_digest}',
               to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
                   jsonb_set(
                       jsonb_set((draft->'business') - 'retention', '{replacement}', draft->'business'->'retention'),
                       '{replacement_version}',
                       COALESCE(draft->'business'->'retention_version', '0'::jsonb))
                   - 'retention_version'), 'UTF8'), 'sha256'), 'hex'))) AS new_draft
    FROM anime_config_candidate_revisions
    WHERE draft ? 'business' AND draft->'business' ? 'retention'
)
UPDATE anime_config_candidate_revisions r
SET draft = rewritten.new_draft,
    -- A revision draft digest that was the canonical draft hash follows the
    -- rewritten draft; any other recorded digest is left as it was.
    draft_digest = CASE WHEN rewritten.old_digest = rewritten.old_canonical
        THEN 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(rewritten.new_draft), 'UTF8'), 'sha256'), 'hex')
        ELSE r.draft_digest END
FROM rewritten
WHERE r.candidate_id = rewritten.candidate_id AND r.revision = rewritten.revision;
ALTER TABLE anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg;

-- Every other frozen document keeps the bytes it was admitted with: one-shot
-- input snapshots and download-task frozen policy are identity-bearing
-- evidence and the engine replays them under the version they recorded.

-- The singleton policy default: the section name is the identity and its value
-- is an object without the section name inside it.
ALTER TABLE global_config_sections DROP CONSTRAINT global_config_sections_section_name_check;
ALTER TABLE global_config_sections DROP CONSTRAINT global_config_sections_policy_shape_check;
UPDATE global_config_sections SET section_name = 'replacement' WHERE section_name = 'retention';
-- The stored schema_version follows the section identity: the baseline seeded
-- 'retention-policy-v1' and nothing validates that string, so it is normalized
-- here rather than left pointing at the old meaning.
UPDATE global_config_sections SET schema_version = 'replacement-policy-v1'
WHERE section_name = 'replacement' AND schema_version = 'retention-policy-v1';
ALTER TABLE global_config_sections ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'replacement', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh'));
ALTER TABLE global_config_sections ADD CONSTRAINT global_config_sections_policy_shape_check CHECK (
    (section_name <> 'replacement' OR (
        jsonb_typeof(value) = 'object'
        AND (value->>'admission_mode' IN ('all', 'per_group', 'single_copy')) IS TRUE
        AND NOT (value ? 'cleanup_policy')
        AND (replacement_quality_profile_valid(value->'quality_profile') AND pg_column_size(value->'quality_profile') <= 65536) IS TRUE
    ))
    AND (section_name <> 'parser_policy' OR (
        jsonb_typeof(value) = 'object'
        AND (char_length(btrim(value->>'parser_kind')) BETWEEN 1 AND 64) IS TRUE
    ))
    AND (section_name <> 'file_effect_policy' OR (
        jsonb_typeof(value) = 'object'
        AND (value->>'unclassified_route' IN ('route', 'skip')) IS TRUE
    ))
);

-- ---------------------------------------------------------------------------
-- (A) Stored functions that named the old column, section, table, kind or owner
-- ---------------------------------------------------------------------------

-- The draft guard names the snapshot's business keys.
CREATE OR REPLACE FUNCTION anime_config_candidate_draft_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $_$
DECLARE
 b JSONB;
 sections TEXT[] := ARRAY['subgroups','parser','release_rules','file_rules','naming_rules','file_effect'];
 name TEXT;
 ref JSONB;
BEGIN
 IF NOT anime_config_candidate_identity_shape_ok(draft - 'business' - 'business_digest',origin,origin_entry_ref,instance_label) THEN RETURN FALSE; END IF;
 IF NOT (draft ? 'business') AND NOT (draft ? 'business_digest') THEN RETURN TRUE; END IF;
 b := draft->'business';
 IF NOT business_exact_keys(b,ARRAY['schema_version','sections','section_versions','section_schemas','parser','replacement','replacement_version','unclassified_route','file_effect_version','programs']) THEN RETURN FALSE; END IF;
 IF b->>'schema_version' IS DISTINCT FROM 'business-snapshot-v1'
    OR jsonb_typeof(draft->'business_digest') IS DISTINCT FROM 'string'
    OR draft->>'business_digest' !~ '^sha256:[a-f0-9]{64}$'
    OR draft->>'business_digest' IS DISTINCT FROM 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(b),'UTF8'),'sha256'),'hex') THEN RETURN FALSE; END IF;
 IF NOT business_exact_keys(b->'sections',sections)
    OR NOT business_exact_keys(b->'section_versions',sections)
    OR NOT business_exact_keys(b->'section_schemas',sections) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY sections LOOP
  IF jsonb_typeof(b->'sections'->name) IS DISTINCT FROM 'object'
     OR jsonb_typeof(b->'section_versions'->name) IS DISTINCT FROM 'number'
     OR (b->'section_versions'->>name)::numeric < 0
     OR (b->'section_versions'->>name)::numeric <> floor((b->'section_versions'->>name)::numeric)
     OR jsonb_typeof(b->'section_schemas'->name) IS DISTINCT FROM 'string' THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'parser',ARRAY['kind','policy_version','provenance','profile_version','rules_owner_version','schema_version','source_owner'])
    OR b->'parser'->>'schema_version' IS DISTINCT FROM 'parser-policy-envelope-v1'
    OR NOT business_exact_keys(b->'replacement',ARRAY['admission_mode','quality_profile'])
    OR b->'replacement'->>'admission_mode' NOT IN ('all','per_group','single_copy')
    OR NOT replacement_quality_profile_valid(b->'replacement'->'quality_profile')
    OR (b->'replacement') ? 'cutoff'::text
    OR b->>'unclassified_route' NOT IN ('route','skip') THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['replacement_version','file_effect_version'] LOOP
  IF jsonb_typeof(b->name) IS DISTINCT FROM 'number' OR (b->>name)::numeric < 0 OR (b->>name)::numeric <> floor((b->>name)::numeric) THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'replacement'->'quality_profile',ARRAY['version','items']) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['release_rules','file_rules'] LOOP
  IF NOT business_exact_keys(b->'sections'->name,ARRAY['must_contain','not_in'])
     OR jsonb_typeof(b->'sections'->name->'must_contain') IS DISTINCT FROM 'string'
     OR jsonb_typeof(b->'sections'->name->'not_in') IS DISTINCT FROM 'string' THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'programs',ARRAY['attrrules','vars']) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['attrrules','vars'] LOOP
  ref := b->'programs'->name;
  IF NOT business_exact_keys(ref,ARRAY['version','digest'])
     OR jsonb_typeof(ref->'version') IS DISTINCT FROM 'string'
     OR jsonb_typeof(ref->'digest') IS DISTINCT FROM 'string'
     OR ref->>'digest' !~ '^sha256:[a-f0-9]{64}$' THEN RETURN FALSE; END IF;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN RETURN FALSE;
END;
$_$;

-- Physical local cleanup of one retired ManagedAnime: the leaf-first step list
-- names the deletion obligation table.
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
        'diagnostic_episodes',
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
        'DELETE FROM diagnostic_episodes WHERE managed_anime_id = $1'
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

-- The retirement fence resolves a deletion obligation's ManagedAnime.
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
    WHEN 'offset_migration_operation' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM offset_migration_plans op WHERE op.id = NEW.owner_id;
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

-- Deleting one anime's file-maintenance owners lists the single deletion kind.
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
) OR (
    wi.owner_type = 'offset_migration_operation'
    AND wi.kind = 'offset_migration_execute'
    AND EXISTS (
      SELECT 1
      FROM public.offset_migration_plans omp
      WHERE omp.id = wi.owner_id
        AND omp.managed_anime_id = $1
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
) OR (
    wi.owner_type = 'offset_migration_operation'
    AND wi.kind = 'offset_migration_execute'
    AND EXISTS (
      SELECT 1
      FROM public.offset_migration_plans omp
      WHERE omp.id = wi.owner_id
        AND omp.managed_anime_id = $1
    )
);
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

-- The local-maintenance tree delete walks the same owner set.
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
       SELECT id FROM public.replacement_deletions WHERE managed_anime_id = $1))
   OR (owner_type = 'offset_migration_operation' AND owner_id IN (
       SELECT id FROM public.offset_migration_plans WHERE managed_anime_id = $1));
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes
WHERE (owner_type = 'file_maintenance' AND owner_id = $1)
   OR (owner_type = 'alignment_operation' AND owner_id IN (
       SELECT id FROM public.alignment_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'replacement_deletion' AND owner_id IN (
       SELECT id FROM public.replacement_deletions WHERE managed_anime_id = $1))
   OR (owner_type = 'offset_migration_operation' AND owner_id IN (
       SELECT id FROM public.offset_migration_plans WHERE managed_anime_id = $1));
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

-- The maintenance work-item lock reads the deletion obligation's anime.
CREATE OR REPLACE FUNCTION work_item_lock_maintenance_cleanup(text, text) RETURNS TABLE(id text, owner_type text, owner_id text, kind text, state text, expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT id,owner_type,owner_id,kind,state,expected_owner_version FROM public.work_items WHERE owner_type='replacement_deletion' AND owner_id=$1 AND kind=$2 AND state IN ('available','leased') ORDER BY id LIMIT 1 FOR UPDATE;
$_$;

-- ---------------------------------------------------------------------------
-- (C) one-shot reclaim state value
-- ---------------------------------------------------------------------------

-- Two stored function bodies name the marker state. They change in the same
-- transaction as the CHECK so no window accepts one value and rejects the
-- other.
CREATE OR REPLACE FUNCTION public.acquisition_import_retry_refusal(p_root text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; v_owner text; lifecycle text; marker_state text;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root;
 IF NOT FOUND THEN RETURN 'IMPORT_RETRY_NOT_FOUND'; END IF;
 IF r.record_state<>'full' OR r.state='cancelled' THEN RETURN 'IMPORT_RETRY_NOT_APPLICABLE'; END IF;
 IF r.state='running' OR public.acquisition_live_work(p_root) THEN RETURN 'IMPORT_RETRY_IN_FLIGHT'; END IF;
 v_owner:=public.acquisition_owner(p_root);
 IF v_owner IS NOT NULL THEN
  SELECT l.lifecycle_state INTO lifecycle FROM public.managed_anime l WHERE l.id=v_owner;
  IF lifecycle IS DISTINCT FROM 'active' THEN RETURN 'MANAGED_ANIME_RETIRED'; END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisition_files f JOIN public.file_operations o
    ON o.id=f.plan_operation_ref AND o.acquisition_id=f.acquisition_id
   WHERE r.state='failed' AND f.acquisition_id=p_root AND f.plan_kind='execute' AND f.outcome='failed' AND o.state<>'completed')
  AND NOT EXISTS(SELECT 1 FROM public.file_operations o JOIN public.placement_obligations po ON po.file_operation_id=o.id
   WHERE o.acquisition_id=p_root AND o.state='completed' AND po.state='failed') THEN
  RETURN 'IMPORT_RETRY_NOT_APPLICABLE';
 END IF;
 IF r.entry_kind='one_shot' THEN
  SELECT m.state INTO marker_state FROM public.one_shot_acquisitions m WHERE m.request_idempotency_key=r.request_key;
  IF marker_state IS NULL OR marker_state NOT IN ('running','reclaim_pending') THEN RETURN 'IMPORT_RETRY_NOT_APPLICABLE'; END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.acquisition_import_retry(p_root text, p_expected_version bigint, p_key text, p_hash text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; prior public.acquisition_import_retries%ROWTYPE;
 marker public.one_shot_acquisitions%ROWTYPE; attempt public.one_shot_execution_attempts%ROWTYPE;
 v_owner text; refusal text; now_at timestamptz:=clock_timestamp(); one_shot jsonb:=NULL;
 o public.file_operations%ROWTYPE; retried text[]:=ARRAY[]::text[]; files integer:=0; placements integer:=0;
 snapshot jsonb; new_work text; next_ordinal integer; next_attempt text; new_version bigint;
BEGIN
 IF p_key IS NULL OR char_length(p_key) NOT BETWEEN 8 AND 128 OR p_hash IS NULL OR p_hash !~ '^sha256:[0-9a-f]{64}$' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_SHAPE'; END IF;
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root;
 IF NOT FOUND THEN RETURN jsonb_build_object('status','not_found'); END IF;
 IF r.entry_kind='one_shot' THEN
  SELECT * INTO marker FROM public.one_shot_acquisitions WHERE request_idempotency_key=r.request_key FOR UPDATE;
 END IF;
 v_owner:=public.acquisition_owner(p_root);
 IF v_owner IS NOT NULL THEN
  PERFORM 1 FROM public.managed_anime WHERE id=v_owner FOR UPDATE;
 END IF;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=p_root FOR UPDATE;

 SELECT * INTO prior FROM public.acquisition_import_retries WHERE acquisition_id=p_root AND idempotency_key=p_key;
 IF FOUND THEN
  IF prior.request_hash<>p_hash THEN RETURN jsonb_build_object('status','key_reused'); END IF;
  RETURN jsonb_build_object('status','accepted','replayed',true,'attempt',prior.ordinal+1,
   'retried_files',prior.retried_files,'reopened_placements',prior.reopened_placements,'version',prior.result_version);
 END IF;

 refusal:=public.acquisition_import_retry_refusal(p_root);
 IF refusal IS NOT NULL THEN RETURN jsonb_build_object('status','refused','code',refusal,'version',r.version); END IF;
 IF p_expected_version IS DISTINCT FROM r.version THEN
  RETURN jsonb_build_object('status','version_conflict','version',r.version); END IF;

 IF r.entry_kind='one_shot' THEN
  -- The marker's current attempt; a marker whose pointer predates the
  -- attempts backfill falls back to its newest attempt, and one with no
  -- attempt at all reopens without a new attempt row.
  SELECT * INTO attempt FROM public.one_shot_execution_attempts WHERE id=marker.current_attempt_id;
  IF NOT FOUND THEN
   SELECT * INTO attempt FROM public.one_shot_execution_attempts WHERE acquisition_id=marker.id
   ORDER BY sequence DESC LIMIT 1;
  END IF;
  IF attempt.id IS NOT NULL AND attempt.state='running' THEN
   -- The marker was not drained yet: the attempt closes with the root's
   -- settled outcome before the retry attempt opens.
   UPDATE public.one_shot_execution_attempts SET state=r.state,
    failed_stage=CASE WHEN r.state='failed' THEN 'file_execution' END,
    outcome_code=CASE WHEN r.state='failed' THEN 'one_shot_failed' ELSE 'one_shot_completed' END,
    outcome_reason=CASE WHEN r.state='failed' THEN 'one-shot acquisition failed in file_execution' ELSE 'one-shot acquisition completed' END,
    terminal_at=now_at,version=version+1,updated_at=now_at
   WHERE id=attempt.id;
  END IF;
  IF attempt.id IS NOT NULL THEN
   next_attempt:=gen_random_uuid()::text;
   INSERT INTO public.one_shot_execution_attempts(id,acquisition_id,sequence,previous_attempt_id,mode,resume_stage,
    input_snapshot,input_digest,state,request_idempotency_key,request_hash)
   SELECT next_attempt,marker.id,COALESCE(max(a.sequence),0)+1,attempt.id,'retry','file_execution',
    attempt.input_snapshot,attempt.input_digest,'running',p_key,p_hash
   FROM public.one_shot_execution_attempts a WHERE a.acquisition_id=marker.id;
  END IF;
  -- reclaim_pending -> running: the terminal evidence and the retention
  -- deadline clear together (the marker field matrix), so the retention pass
  -- neither reclaims it nor drains it while the retried work is live; it
  -- drains again with a fresh deadline once the root settles.
  UPDATE public.one_shot_acquisitions SET state='running',terminal_outcome=NULL,terminal_reason_code=NULL,
   terminal_reason=NULL,terminal_at=NULL,retention_until=NULL,current_attempt_id=COALESCE(next_attempt,current_attempt_id),
   version=version+1,updated_at=now_at
  WHERE id=marker.id;
  one_shot:=jsonb_build_object('marker_id',marker.id,'correlation_id',marker.correlation_id,'admission_epoch',marker.admission_epoch);
 END IF;

 -- Failed file effects: reset in place, clear the delivery result.
 IF r.state='failed' THEN
  FOR o IN SELECT op.* FROM public.file_operations op JOIN public.acquisition_files f
    ON f.plan_operation_ref=op.id AND f.acquisition_id=op.acquisition_id
   WHERE f.acquisition_id=p_root AND f.plan_kind='execute' AND f.outcome='failed' AND op.state<>'completed'
   ORDER BY op.id COLLATE "C" FOR UPDATE OF op LOOP
   UPDATE public.file_operations SET state='pending',failure_code=NULL,failure_reason=NULL,observed_at=NULL,
    target_fingerprint=NULL,observed_target_size_bytes=NULL,publication_checkpoint=NULL,
    import_retry_count=import_retry_count+1,version=version+1,updated_at=now_at,
    file_wall_until=GREATEST(file_wall_until,now_at+(file_wall_until-file_ready_at)),
    budget_clock_at=GREATEST(budget_clock_at,now_at),
    file_attempts_started=0,file_attempt_ticket=NULL,file_attempt_index='[]'::jsonb,
    publication_permit_id=NULL,publication_dispatch_state=NULL,publication_effect_class=NULL,
    publication_effect_key=NULL,publication_spec_digest=NULL,publication_plan_digest=NULL,
    publication_dependency_digest=NULL,publication_executor_session=NULL,publication_work_item_id=NULL,
    publication_fencing_token=NULL,publication_owner_version=NULL,publication_attempt=0,
    publication_evidence_version=1,publication_prepared_at=NULL,publication_dispatch_started_at=NULL,
    publication_frozen_objects=NULL,publication_frozen_objects_canonical=NULL,
    publication_step_checkpoint=NULL,publication_step_proof_bytes=NULL,
    publication_stage_temp_started_at=NULL,publication_reserve_version_started_at=NULL,
    publication_publish_target_started_at=NULL,publication_source_cleanup_started_at=NULL,
    publication_temp_cleanup_started_at=NULL,publication_closed_proof=NULL,publication_resolution_proof=NULL,
    publication_closure_canonical=NULL,publication_resolution_canonical=NULL,
    publication_resolution_checkpoint_canonical=NULL,publication_conflict_evidence=NULL,
    publication_conflict_count=0,publication_conflict_seen='{}'::jsonb,publication_conflict_index_exhausted=false,
    publication_conflict_digest=NULL,publication_retry_safe=false,publication_unknown_since=NULL,
    publication_reconcile_until=NULL,publication_reconcile_wall_seconds=NULL,publication_max_lookups=NULL,
    publication_lookup_count=0,publication_lookup_ticket=NULL,publication_closure_receipt_observed_at=NULL
   WHERE id=o.id;
   UPDATE public.acquisition_files SET delivery_schema_version=NULL,outcome=NULL,completion_mode=NULL,
    effect_class=NULL,final_object=NULL,delivery_source_cleanup=NULL,delivery_reason_code=NULL,
    evidence_digest=NULL,operation_id=NULL,receipt=NULL,delivered_at=NULL,activity_logged_at=NULL
   WHERE acquisition_id=p_root AND file_ref=o.file_ref;
   retried:=array_append(retried,o.id);
   files:=files+1;
  END LOOP;
  -- A subtitle whose video is retried too gets its work from the video's
  -- success handoff; every other retried operation is queued now.
  FOR o IN SELECT op.* FROM public.file_operations op WHERE op.id=ANY(retried)
   AND (op.bound_video_operation_id IS NULL OR NOT (op.bound_video_operation_id=ANY(retried)))
   ORDER BY op.id COLLATE "C" LOOP
   snapshot:=jsonb_build_object('operation_id',o.id,'artifact_id',o.artifact_id,'artifact_file_id',o.artifact_file_id)
    ||CASE WHEN o.collision_policy='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END
    ||CASE WHEN one_shot IS NOT NULL THEN jsonb_build_object('one_shot',one_shot) ELSE '{}'::jsonb END;
   SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
    o.file_max_attempts,snapshot,o.version,0::double precision,7::smallint) INTO new_work;
   IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_WORK_CONFLICT'; END IF;
  END LOOP;
 END IF;

 -- Failed placement handoffs of completed files: re-open with a fresh typed
 -- continuation (the completed file effect is never re-run).
 FOR o IN SELECT op.* FROM public.file_operations op JOIN public.placement_obligations po ON po.file_operation_id=op.id
  WHERE op.acquisition_id=p_root AND op.state='completed' AND po.state='failed'
  ORDER BY op.id COLLATE "C" FOR UPDATE OF po LOOP
  UPDATE public.placement_obligations SET state='pending',attempts=0,reason_code=NULL,reason=NULL,settled_at=NULL,updated_at=now_at
  WHERE file_operation_id=o.id AND state='failed';
  snapshot:=jsonb_build_object('schema_version','file-execute-continuation/v1','continuation','placement',
   'placement_retry',true,'file_operation_id',o.id)
   ||CASE WHEN one_shot IS NOT NULL THEN jsonb_build_object('one_shot',one_shot) ELSE '{}'::jsonb END;
  SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
   3,snapshot,o.version,0::double precision,7::smallint) INTO new_work;
  IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_WORK_CONFLICT'; END IF;
  placements:=placements+1;
 END LOOP;
 IF files+placements=0 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_NOTHING_TO_RETRY'; END IF;

 IF files>0 THEN
  UPDATE public.acquisitions SET state='running',terminal_at=NULL,terminal_schema=NULL,terminal_receipt=NULL,
   detail_until=NULL,completion_open=false,activity_logged_at=NULL,settlement_blocked_code=NULL,
   settlement_error_count=0,version=version+1
  WHERE id=p_root RETURNING version INTO new_version;
  PERFORM public.download_ensure_settlement(p_root);
 ELSE
  UPDATE public.acquisitions SET version=version+1 WHERE id=p_root RETURNING version INTO new_version;
 END IF;

 SELECT COALESCE(max(x.ordinal),0)+1 INTO next_ordinal FROM public.acquisition_import_retries x WHERE x.acquisition_id=p_root;
 INSERT INTO public.acquisition_import_retries(acquisition_id,ordinal,idempotency_key,request_hash,retried_files,
  reopened_placements,result_version,previous_state,previous_terminal_at,previous_terminal_receipt,accepted_at)
 VALUES(p_root,next_ordinal,p_key,p_hash,files,placements,new_version,r.state,r.terminal_at,r.terminal_receipt,now_at);
 RETURN jsonb_build_object('status','accepted','replayed',false,'attempt',next_ordinal+1,
  'retried_files',files,'reopened_placements',placements,'version',new_version);
END $$;

ALTER TABLE one_shot_acquisitions DROP CONSTRAINT one_shot_acquisitions_state_check;
UPDATE one_shot_acquisitions SET state = 'reclaim_pending' WHERE state = 'retention_pending';
ALTER TABLE one_shot_acquisitions ADD CONSTRAINT one_shot_acquisitions_state_check
    CHECK (state IN ('running', 'reclaim_pending', 'cleanup_blocked', 'reclaiming', 'cleaned'));

DROP FUNCTION retention_quality_profile_valid(jsonb);
