-- Forward-only migration 123: artifacts.inspection_requirements_status goes.
--
-- The staging marker was written on inspection failure ('incomplete' when the
-- acquisition carried a requirements digest, else 'not_reached') and cleared on
-- success, but no production reader consumed it: recovery proves requirements
-- state from the acquisition's requirement digests, not from this column. It
-- was a resettable stage marker, not a frozen audit fact.
--
-- Latest definitions replaced here: 102_path_level_inspection.sql
-- (artifact_commit_inspection) and 001_baseline.sql (artifact_inspect_fail).

CREATE OR REPLACE FUNCTION public.artifact_commit_inspection(p_artifact text, p_work text, p_token bigint, p_admission_digest text, p_manifest_digest text, p_manifest_files jsonb, p_inventory jsonb, p_fingerprints jsonb, p_files jsonb, p_plan text, p_plan_work text, p_plan_digest text, p_media_snapshot text, p_media_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE; m_manifest_digest text; v bigint; inherited jsonb; plan_work text;
 n_video bigint; n_subtitle bigint; n_other bigint; n_total numeric; files_digest text;
BEGIN
 a:=public.artifact_recovery_lock(p_artifact,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.cancel_requested OR w.recovery_blocked OR a.state NOT IN ('pending','waiting_retry')
 OR (SELECT state FROM public.download_tasks WHERE id=a.download_task_id) IS DISTINCT FROM 'completed'
 OR p_admission_digest IS DISTINCT FROM (SELECT admission_digest FROM public.acquisitions WHERE id=a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 IF jsonb_typeof(p_files) IS DISTINCT FROM 'array' OR jsonb_array_length(p_files) NOT BETWEEN 1 AND 20000
 OR jsonb_typeof(p_manifest_files) IS DISTINCT FROM 'array' OR octet_length(p_manifest_files::text)>16777216
 OR jsonb_typeof(p_inventory) IS DISTINCT FROM 'object' OR jsonb_typeof(p_fingerprints) IS DISTINCT FROM 'object'
 OR p_plan IS NULL OR p_plan_work IS NULL OR p_plan_digest !~ '^sha256:[0-9a-f]{64}$' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_INSPECTION_SHAPE'; END IF;
 -- The committed summaries carry counts and the file-set digest, never the
 -- file list; per-file data lives only in the submitted artifact_files rows.
 IF p_inventory->>'schema_version' IS DISTINCT FROM 'artifact-inspect/v2'
 OR p_fingerprints->>'schema_version' IS DISTINCT FROM 'artifact-fingerprint/v2'
 OR p_inventory ?| ARRAY['files','inspected_files','inspected_primary_file']
 OR (SELECT count(*) FROM jsonb_object_keys(p_fingerprints))<>4
 OR NOT (p_fingerprints ?& ARRAY['schema_version','primary_file_ref','file_count','files_digest']) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_SNAPSHOT_BINDING'; END IF;
 -- Every file carries its path-level metadata fingerprint: the observed size
 -- (equal to the manifest size) and, when the platform reports one, the
 -- device/inode pair. No content digest is part of an inspection.
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(p_files) f WHERE jsonb_typeof(f) IS DISTINCT FROM 'object'
  OR jsonb_typeof(f->'file_ref') IS DISTINCT FROM 'string' OR jsonb_typeof(f->'size_bytes') IS DISTINCT FROM 'number'
  OR f->'fingerprint'->>'algorithm' IS DISTINCT FROM 'metadata'
  OR (COALESCE(f->'fingerprint'->>'value','') ~ '^meta:size=(0|[1-9][0-9]*)(;dev=[1-9][0-9]*;ino=[1-9][0-9]*)?$') IS NOT TRUE
  OR f->'fingerprint'->'size_bytes' IS DISTINCT FROM f->'size_bytes'
  OR substring(f->'fingerprint'->>'value' FROM '^meta:size=([0-9]+)') IS DISTINCT FROM f->>'size_bytes') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_INSPECTION_SHAPE'; END IF;
 SELECT count(*) FILTER (WHERE f->>'media_kind'='video'),count(*) FILTER (WHERE f->>'media_kind'='subtitle'),
  count(*) FILTER (WHERE COALESCE(f->>'media_kind','') NOT IN ('video','subtitle')),COALESCE(sum((f->>'size_bytes')::numeric),0)
 INTO n_video,n_subtitle,n_other,n_total FROM jsonb_array_elements(p_files) f;
 files_digest:=public.artifact_files_digest(p_files);
 IF (SELECT count(DISTINCT f->>'file_ref') FROM jsonb_array_elements(p_files) f)<>jsonb_array_length(p_files)
 OR jsonb_typeof(p_inventory->'primary_file_ref') IS DISTINCT FROM 'string'
 OR p_inventory->'primary_file_ref' IS DISTINCT FROM p_fingerprints->'primary_file_ref'
 OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_files) f WHERE f->'file_ref'=p_inventory->'primary_file_ref')
 OR p_inventory->'file_count' IS DISTINCT FROM to_jsonb(jsonb_array_length(p_files))
 OR p_fingerprints->'file_count' IS DISTINCT FROM to_jsonb(jsonb_array_length(p_files))
 OR p_inventory->'video_count' IS DISTINCT FROM to_jsonb(n_video)
 OR p_inventory->'subtitle_count' IS DISTINCT FROM to_jsonb(n_subtitle)
 OR p_inventory->'other_count' IS DISTINCT FROM to_jsonb(n_other)
 OR p_inventory->'total_size_bytes' IS DISTINCT FROM to_jsonb(n_total)
 OR files_digest IS NULL OR p_inventory->>'files_digest' IS DISTINCT FROM files_digest
 OR p_fingerprints->>'files_digest' IS DISTINCT FROM files_digest THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_SNAPSHOT_BINDING'; END IF;
 SELECT manifest_digest INTO m_manifest_digest FROM public.acquisitions WHERE id=a.acquisition_id;
 IF m_manifest_digest IS NOT NULL THEN
 IF m_manifest_digest IS DISTINCT FROM p_manifest_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
 ELSE
INSERT INTO public.acquisition_files(acquisition_id,file_ref,ordinal,relative_path,exact_size,media_kind,identity_evidence)
SELECT a.acquisition_id,x.file_ref,x.ordinal,x.relative_path,x.exact_size,x.media_kind,x.identity_evidence
FROM jsonb_to_recordset(p_manifest_files) AS x(file_ref text,ordinal integer,relative_path text,exact_size bigint,media_kind text,identity_evidence jsonb);
 UPDATE public.acquisitions SET manifest_digest=p_manifest_digest,manifest_item_count=jsonb_array_length(p_manifest_files),manifest_sealed_at=CURRENT_TIMESTAMP WHERE id=a.acquisition_id;
 END IF;
 -- Compare the complete observed set to the immutable manifest in both directions.
IF EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=a.acquisition_id AND NOT EXISTS(
SELECT 1 FROM jsonb_to_recordset(p_files) x(file_ref text,relative_path text,size_bytes bigint) WHERE x.file_ref=f.file_ref AND x.relative_path=f.relative_path AND x.size_bytes=f.exact_size))
OR EXISTS(SELECT 1 FROM jsonb_to_recordset(p_files) x(file_ref text,relative_path text,size_bytes bigint) WHERE NOT EXISTS(
SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=a.acquisition_id AND f.file_ref=x.file_ref AND f.relative_path=x.relative_path AND f.exact_size=x.size_bytes))
OR jsonb_array_length(p_files)<>(SELECT count(*) FROM public.acquisition_files WHERE acquisition_id=a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
 -- Old observations may be replaced only before any downstream commitment.
 IF EXISTS(SELECT 1 FROM public.artifact_files WHERE artifact_id=p_artifact AND acquisition_id IS DISTINCT FROM a.acquisition_id)
 OR EXISTS(SELECT 1 FROM public.postprocessing_plans WHERE artifact_id=p_artifact)
 OR EXISTS(SELECT 1 FROM public.file_operations WHERE artifact_id=p_artifact)
 OR EXISTS(SELECT 1 FROM public.artifact_files f JOIN public.artifact_files consumer ON consumer.artifact_id=f.artifact_id AND consumer.bound_video_file_ref=f.file_ref WHERE f.artifact_id=p_artifact AND consumer.acquisition_id IS DISTINCT FROM a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_DOWNSTREAM_COMMITMENT'; END IF;
 DELETE FROM public.artifact_files WHERE artifact_id=p_artifact;
 INSERT INTO public.artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
 SELECT x.id,p_artifact,a.acquisition_id,x.file_ref,x.relative_path,x.source_path,x.media_kind,x.size_bytes,x.fingerprint,x.facts,
 CASE WHEN x.media_kind='subtitle' THEN 'missing' ELSE 'not_applicable' END
 FROM jsonb_to_recordset(p_files) x(id text,file_ref text,relative_path text,source_path text,media_kind text,size_bytes bigint,fingerprint jsonb,facts jsonb);
 IF jsonb_typeof(p_media_files) IS DISTINCT FROM 'array' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_SHAPE'; END IF;
 IF p_media_snapshot<>'' THEN
 IF p_media_snapshot IS DISTINCT FROM (SELECT snap.id FROM public.media_fact_snapshots snap JOIN public.download_tasks t ON t.release_id=snap.release_id WHERE t.id=a.download_task_id ORDER BY snap.created_at DESC,snap.id DESC LIMIT 1)
 OR jsonb_array_length(p_media_files)<>jsonb_array_length(p_files) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 PERFORM id FROM public.media_fact_snapshots WHERE id=p_media_snapshot FOR SHARE;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(p_media_files) prepared WHERE prepared->'snapshot_binding' IS DISTINCT FROM (
 SELECT jsonb_build_object('release_facts',snap.release_facts,'release_identity',snap.release_identity,'parser_kind',snap.parser_kind,'parser_config',snap.parser_config,'parser_config_fingerprint',snap.parser_config_fingerprint) FROM public.media_fact_snapshots snap WHERE snap.id=p_media_snapshot))
 OR (SELECT count(DISTINCT prepared->>'file_ref') FROM jsonb_array_elements(p_media_files) prepared)<>jsonb_array_length(p_media_files)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_media_files) prepared WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_files) f WHERE f->>'file_ref'=prepared->>'file_ref' AND f->'size_bytes'=prepared->'size_bytes' AND f->'media_kind'=prepared->'media_kind' AND f->'fingerprint'->>'value'=prepared->>'source_fingerprint')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 INSERT INTO public.media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
 SELECT p_media_snapshot,x.file_ref,x.media_kind,x.size_bytes,x.source_fingerprint,x.file_evidence,x.effective_facts
 FROM jsonb_to_recordset(p_media_files) x(file_ref text,media_kind text,size_bytes bigint,source_fingerprint text,file_evidence jsonb,effective_facts jsonb)
 ON CONFLICT(snapshot_id,file_ref) DO UPDATE SET media_kind=EXCLUDED.media_kind,size_bytes=EXCLUDED.size_bytes,source_fingerprint=EXCLUDED.source_fingerprint,file_evidence=EXCLUDED.file_evidence,effective_facts=EXCLUDED.effective_facts;
 ELSIF jsonb_array_length(p_media_files)<>0 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 UPDATE public.artifacts SET state='verified',inventory_snapshot=p_inventory,fingerprint_snapshot=p_fingerprints,verified_at=CURRENT_TIMESTAMP,
 failure_code=NULL,failure_reason=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 inherited:=CASE WHEN w.input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',w.input_snapshot->'one_shot') ELSE '{}'::jsonb END;
 INSERT INTO public.postprocessing_plans(id,acquisition_id,artifact_id,artifact_version,one_shot_id,input_digest,state)
 VALUES(p_plan,a.acquisition_id,p_artifact,v,w.input_snapshot->'one_shot'->>'marker_id',p_plan_digest,'queued');
 SELECT public.work_item_create(p_plan_work,'postprocessing_plan',p_plan,'postprocess_plan','plan:'||p_plan,5,inherited,1::bigint,0::double precision,7::smallint) INTO plan_work;
 IF plan_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_PLAN_WORK_CONFLICT'; END IF;
 PERFORM public.work_item_finish('succeeded',v,p_work);
 PERFORM public.download_ensure_settlement(a.acquisition_id);
 RETURN jsonb_build_object('state','verified','reason_code','artifact_verified','artifact_version',v);
END $_$;

-- file_target_metadata_fingerprint is the metadata fingerprint of a published
-- PathObjectV1: its size and, when both are nonzero, its device/inode. A

CREATE OR REPLACE FUNCTION artifact_inspect_fail(p_artifact text, p_work text, p_code text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; v bigint;
BEGIN
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=p_artifact;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>2048 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_FAILURE_SHAPE'; END IF;
 UPDATE public.artifacts SET state='failed',failure_code=p_code,failure_reason=NULLIF(p_reason,''),verified_at=NULL,
 version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 PERFORM public.work_item_fail_at_attempt_budget(v,p_code,p_reason,p_work,a.version);
 PERFORM public.download_ensure_settlement(a.acquisition_id);
 RETURN v;
END $$;

ALTER TABLE public.artifacts DROP COLUMN inspection_requirements_status;
