-- Forward-only migration 058: replacement losers are deleted directly.
--
-- A managed replacement loser no longer goes through quarantine, a grace
-- period and a separate purge. Settlement records one deletion obligation per
-- loser in the same transaction that supersedes it, and one `cleanup_delete`
-- work item verifies the loser's path-level identity (path + recorded size,
-- plus the inode when both sides recorded one) and removes it: the file is
-- captured into a private namespace next to it and purged inside the same
-- claim. That namespace is not a quarantine area: there is no grace period and
-- nothing can be restored.
--
-- What leaves the schema:
--   * cleanup_operations.{quarantine_path, grace_deadline, effect,
--     policy_version} and the states quarantined / grace_waiting / purged /
--     completed. The terminal states are deleted | skipped | blocked | failed.
--   * the cleanup_quarantine / cleanup_purge work-item kinds (one
--     cleanup_delete kind replaces them).
--   * the cleanup policy (effect, grace_seconds) everywhere it was stored:
--     anime_business_configs.cleanup_policy, the global retention section, the
--     frozen business snapshots and their digests, and the
--     retention_cleanup_policy_valid() validator.
--
-- Migrations carry no data-preservation obligation before release: pending
-- cleanup operations and replacement decisions are cleared instead of being
-- translated into the new shape.

-- Cleanup owners and their work items.
DELETE FROM work_items WHERE owner_type = 'cleanup_operation';
DELETE FROM work_item_outcomes WHERE owner_type = 'cleanup_operation';
DELETE FROM cleanup_operations;
DELETE FROM replacement_decisions;

ALTER TABLE cleanup_operations
    DROP CONSTRAINT cleanup_operations_effect_check,
    DROP CONSTRAINT cleanup_operations_policy_version_check,
    DROP CONSTRAINT cleanup_operations_quarantine_path_check,
    DROP CONSTRAINT cleanup_operations_state_check,
    DROP COLUMN quarantine_path,
    DROP COLUMN grace_deadline,
    DROP COLUMN effect,
    DROP COLUMN policy_version,
    ADD CONSTRAINT cleanup_operations_state_check
        CHECK (state IN ('admitted', 'deleted', 'skipped', 'blocked', 'failed'));

ALTER TABLE work_items DROP CONSTRAINT work_items_owner_kind_check;
ALTER TABLE work_items ADD CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'cleanup_operation' AND kind = 'cleanup_delete') OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute'));

-- Deleting one anime's file-maintenance owners lists the single cleanup kind.
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
    wi.owner_type = 'cleanup_operation'
    AND wi.kind = 'cleanup_delete'
    AND EXISTS (
      SELECT 1
      FROM public.cleanup_operations co
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
    wi.owner_type = 'cleanup_operation'
    AND wi.kind = 'cleanup_delete'
    AND EXISTS (
      SELECT 1
      FROM public.cleanup_operations co
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

-- A business snapshot's retention value is exactly admission_mode + quality_profile.
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
 IF NOT business_exact_keys(b,ARRAY['schema_version','sections','section_versions','section_schemas','parser','retention','retention_version','unclassified_route','file_effect_version','programs']) THEN RETURN FALSE; END IF;
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
    OR NOT business_exact_keys(b->'retention',ARRAY['admission_mode','quality_profile'])
    OR b->'retention'->>'admission_mode' NOT IN ('all','per_group','single_copy')
    OR NOT retention_quality_profile_valid(b->'retention'->'quality_profile')
    OR (b->'retention') ? 'cutoff'::text
    OR b->>'unclassified_route' NOT IN ('route','skip') THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['retention_version','file_effect_version'] LOOP
  IF jsonb_typeof(b->name) IS DISTINCT FROM 'number' OR (b->>name)::numeric < 0 OR (b->>name)::numeric <> floor((b->>name)::numeric) THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'retention'->'quality_profile',ARRAY['version','items']) THEN RETURN FALSE; END IF;
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

-- The global retention default loses its cleanup policy.
ALTER TABLE global_config_sections DROP CONSTRAINT global_config_sections_policy_shape_check;
UPDATE global_config_sections SET value = value - 'cleanup_policy' WHERE section_name = 'retention' AND value ? 'cleanup_policy';
ALTER TABLE global_config_sections ADD CONSTRAINT global_config_sections_policy_shape_check CHECK (
    (section_name <> 'retention' OR (
        jsonb_typeof(value) = 'object'
        AND (value->>'admission_mode' IN ('all', 'per_group', 'single_copy')) IS TRUE
        AND NOT (value ? 'cleanup_policy')
        AND (retention_quality_profile_valid(value->'quality_profile') AND pg_column_size(value->'quality_profile') <= 65536) IS TRUE
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

-- The per-instance retention axis keeps admission mode and quality profile.
ALTER TABLE anime_business_configs DROP CONSTRAINT anime_business_configs_retention_check;
ALTER TABLE anime_business_configs DROP COLUMN cleanup_policy;
ALTER TABLE anime_business_configs ADD CONSTRAINT anime_business_configs_retention_check CHECK (
    (admission_mode IS NULL AND quality_profile IS NULL AND retention_source_default_version IS NULL AND retention_version IS NULL)
    OR (admission_mode IN ('all', 'per_group', 'single_copy')
        AND (retention_quality_profile_valid(quality_profile) AND pg_column_size(quality_profile) <= 65536) IS TRUE
        AND retention_source_default_version IS NOT NULL AND retention_source_default_version >= 1
        AND retention_version IS NOT NULL AND retention_version >= 1)
);

-- Frozen business snapshots drop the cleanup policy; every stored digest is
-- the canonical hash of the stored body, so each one is recomputed with it.
UPDATE anime_business_configs
SET business_snapshot = business_snapshot #- '{retention,cleanup_policy}',
    business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot #- '{retention,cleanup_policy}'), 'UTF8'), 'sha256'), 'hex')
WHERE business_snapshot IS NOT NULL AND business_snapshot->'retention' ? 'cleanup_policy';

-- Candidate drafts. The candidate guard refuses a linked draft rewrite; the
-- migration-only fixture switch lets this one shape rewrite through, and the
-- defaults trigger recomputes draft_digest from the rewritten draft.
SET LOCAL bansoko.candidate_fixture = 'on';
UPDATE anime_config_candidates
SET draft = jsonb_set(draft #- '{business,retention,cleanup_policy}', '{business_digest}',
        to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft->'business' #- '{retention,cleanup_policy}'), 'UTF8'), 'sha256'), 'hex'))),
    draft_digest = ''
WHERE draft ? 'business' AND draft->'business'->'retention' ? 'cleanup_policy';
RESET bansoko.candidate_fixture;

-- Candidate revisions are append-only; their guards are lifted for this one
-- shape rewrite only. The effective sections (and so effective_digest and
-- every current_revision_digest mirror) are unchanged.
ALTER TABLE anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
UPDATE anime_config_candidate_revisions
SET business_snapshot = business_snapshot #- '{retention,cleanup_policy}',
    business_digest = 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(business_snapshot #- '{retention,cleanup_policy}'), 'UTF8'), 'sha256'), 'hex')
WHERE business_snapshot IS NOT NULL AND business_snapshot->'retention' ? 'cleanup_policy';
WITH rewritten AS (
    SELECT candidate_id, revision, draft_digest AS old_digest,
           'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex') AS old_canonical,
           jsonb_set(draft #- '{business,retention,cleanup_policy}', '{business_digest}',
               to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft->'business' #- '{retention,cleanup_policy}'), 'UTF8'), 'sha256'), 'hex'))) AS new_draft
    FROM anime_config_candidate_revisions
    WHERE draft ? 'business' AND draft->'business'->'retention' ? 'cleanup_policy'
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

DROP FUNCTION retention_cleanup_policy_valid(jsonb);
