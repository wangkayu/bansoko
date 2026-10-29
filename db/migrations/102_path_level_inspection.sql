-- Forward-only migration 102: artifact inspection is path-level and accepts
-- any manifest file count.
--
-- Inspection used to read every file to the end for a sha256 digest and kept
-- the whole file list (twice) in the committed inventory and fingerprint
-- snapshots, so a release with hundreds of files ran past its attempt budget
-- and past the 128-file inspection bound. An inspection now opens each file
-- without following links, checks that it is a regular file of the manifest
-- size and records a metadata fingerprint:
--
--   {"algorithm":"metadata","value":"meta:size=<n>[;dev=<d>;ino=<i>]","size_bytes":<n>}
--
-- No content is read, so inspection time grows with the file count, not with
-- the bytes. The committed snapshots (artifact-inspect/v2 and
-- artifact-fingerprint/v2) carry the primary file reference, the counts and a
-- digest of the bound file set (public.artifact_files_digest, computed the
-- same way by the inspector) instead of the file list; per-file data lives in
-- artifact_files rows only.
--
-- artifact_commit_inspection accepts 1..20000 files (the torrent manifest
-- bound) and checks the v2 summaries and every file's metadata fingerprint;
-- the manifest, downstream-commitment, media and plan steps are unchanged.
-- acquisitions.manifest_item_count follows the same 20000 bound.
--
-- A typed publication's completion records the target fingerprint from the
-- published object (its size and device/inode in the publish_target proof)
-- instead of copying the source fingerprint: a copied file is a new inode,
-- and the placement and companion fingerprints compare inodes.
--
-- Existing rows move to the metadata form once; nothing keeps decoding the
-- digest form for them:
--   * artifact_files.fingerprint becomes the size-only metadata fingerprint of
--     the row's size (no device/inode was ever recorded for them);
--   * file_operations.source_fingerprint takes its artifact file's rewritten
--     fingerprint and target_fingerprint the metadata fingerprint of its
--     publication proof target (size-only when the proof has no identity);
--   * postprocessing_plans.committed_operations[].fingerprint takes its
--     operation's rewritten source fingerprint, so the frozen declaration and
--     the operation row still compare equal;
--   * media_fact_files.source_fingerprint and placement_companions.fingerprint
--     holding a bare sha256 become "meta:size=<size_bytes>";
--   * verified artifacts' v1 snapshots become v2 summaries computed from their
--     artifact_files rows.
-- Canonical plan documents, plan evidence and overwrite snapshots stay as
-- frozen; placements and replacement records keep their stored fingerprint
-- text. Versions are not bumped: this is a representation rewrite of facts
-- no worker holds across the upgrade. The file_operations and
-- postprocessing_plans guards are disabled for the rewrite only (they guard
-- runtime transitions, and a completed plan or operation is otherwise
-- immutable).
--
-- Placement-side fingerprint columns that store this envelope as text widen
-- from 128 to 512 characters: the metadata value with a device/inode pair is
-- up to 79 characters and its envelope text up to about 150.

-- artifact_files_digest is the inspected file-set digest:
--   "sha256:" || hex(sha256("artifact-files:v1" || 0x00 || fields))
-- where fields concatenates, for the files ordered by file_ref (bytewise),
-- "<byte length>:<value>" of file_ref, relative_path, media_kind, size_bytes
-- (decimal) and the fingerprint value. A malformed file set yields NULL.
CREATE FUNCTION public.artifact_files_digest(p_files jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN jsonb_typeof(p_files) IS DISTINCT FROM 'array' OR EXISTS(
   SELECT 1 FROM jsonb_array_elements(p_files) f WHERE jsonb_typeof(f) IS DISTINCT FROM 'object'
    OR jsonb_typeof(f->'file_ref') IS DISTINCT FROM 'string' OR jsonb_typeof(f->'relative_path') IS DISTINCT FROM 'string'
    OR jsonb_typeof(f->'media_kind') IS DISTINCT FROM 'string' OR jsonb_typeof(f->'size_bytes') IS DISTINCT FROM 'number'
    OR ((f->>'size_bytes') ~ '^(0|[1-9][0-9]{0,18})$') IS NOT TRUE
    OR jsonb_typeof(f->'fingerprint'->'value') IS DISTINCT FROM 'string' OR f->'fingerprint'->>'value'='')
 THEN NULL
 ELSE 'sha256:'||encode(sha256(convert_to('artifact-files:v1','UTF8')||decode('00','hex')||convert_to(COALESCE((
  SELECT string_agg(
    octet_length(f->>'file_ref')::text||':'||(f->>'file_ref')
    ||octet_length(f->>'relative_path')::text||':'||(f->>'relative_path')
    ||octet_length(f->>'media_kind')::text||':'||(f->>'media_kind')
    ||octet_length(f->>'size_bytes')::text||':'||(f->>'size_bytes')
    ||octet_length(f->'fingerprint'->>'value')::text||':'||(f->'fingerprint'->>'value'),
    '' ORDER BY f->>'file_ref' COLLATE "C")
  FROM jsonb_array_elements(p_files) f),''),'UTF8')),'hex') END
$$;
REVOKE ALL ON FUNCTION public.artifact_files_digest(jsonb) FROM PUBLIC;

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
 failure_code=NULL,failure_reason=NULL,inspection_requirements_status=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
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
-- target without a size (no proof object) falls back to p_fallback_size and
-- no identity.
CREATE FUNCTION public.file_target_metadata_fingerprint(p_target jsonb, p_fallback_size bigint) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
 SELECT jsonb_build_object('algorithm','metadata','value',
   'meta:size='||s.size::text||CASE WHEN s.dev ~ '^[1-9][0-9]{0,19}$' AND s.ino ~ '^[1-9][0-9]{0,19}$' THEN ';dev='||s.dev||';ino='||s.ino ELSE '' END,
   'size_bytes',s.size)
 FROM (SELECT CASE WHEN known THEN (p_target->>'size')::bigint ELSE p_fallback_size END size,
   CASE WHEN known AND jsonb_typeof(p_target->'identity'->'device')='string' THEN p_target->'identity'->>'device' END dev,
   CASE WHEN known AND jsonb_typeof(p_target->'identity'->'inode')='string' THEN p_target->'identity'->>'inode' END ino
  FROM (SELECT jsonb_typeof(p_target->'size')='number' AND (p_target->>'size') ~ '^(0|[1-9][0-9]{0,18})$' AS known) k) s
 WHERE s.size IS NOT NULL
$_$;
REVOKE ALL ON FUNCTION public.file_target_metadata_fingerprint(jsonb, bigint) FROM PUBLIC;

-- file_success_atomic_body is the 054 body; the completion's target
-- fingerprint is the published object's metadata fingerprint.

CREATE OR REPLACE FUNCTION public.file_success_atomic_body(p_operation text, p_work text, p_permit text, p_file_fact text, p_notification text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; e public.file_operations;
 next_version bigint; affected integer;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 -- Notification ledger 029 accepts ordinary bounded JSON objects, not Plan
 -- canonical bytes. Frozen construction/sanitization is independently checked
 -- by the fixed caller before admission; this helper remains private.
 IF p_notification IS NULL OR jsonb_typeof(p_notification::jsonb) IS DISTINCT FROM 'object'
 OR octet_length(p_notification) NOT BETWEEN 1 AND 65536
 OR public.file_notification_safe(p_notification::jsonb) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
 -- Bind stable result fields before any business write. Full frozen display
 -- field reconstruction remains required before granting this command.
 IF p_notification::jsonb->>'event.type' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->>'result.outcome' IS DISTINCT FROM 'success'
 OR p_notification::jsonb->>'result.reason' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->'anime.title_override' IS DISTINCT FROM 'null'::jsonb
 OR p_notification::jsonb->>'op.kind' IS DISTINCT FROM e.operation
 OR p_notification::jsonb->>'op.conflict' IS DISTINCT FROM e.collision_policy
 OR p_notification::jsonb->'file.size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 IF p_notification::jsonb IS DISTINCT FROM public.file_success_notification(p_operation,p_permit,p_file_fact::jsonb) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 PERFORM public.file_assert_final_proof(p_permit);
 PERFORM public.file_success_projection(p_operation,p_permit,p_file_fact);
 IF w.cancel_requested THEN
  affected:=public.file_cancelled_dependency_suppression(p_operation,p_work);
 ELSE
  affected:=public.file_success_handoff(p_operation,p_work);
 END IF;
 -- The publish_target proof is the completion record of the typed chain
 -- (atomic_method 'replace' plus the displaced object for version and
 -- overwrite). Path-level verification re-reads no content, so the target
 -- fingerprint is the metadata fingerprint of the published object: its
 -- observed size and device/inode (a copy is a new inode, a move keeps it).
 UPDATE public.file_operations SET state='completed',failure_code=NULL,failure_reason=NULL,
 target_fingerprint=public.file_target_metadata_fingerprint(e.publication_step_checkpoint->'publish_target'->'proof'->'target',
  (e.publication_frozen_objects->'source'->>'size')::bigint),
 publication_checkpoint=e.publication_step_checkpoint->'publish_target'->'proof',
 observed_at=clock_timestamp(),version=version+1 WHERE id=p_operation RETURNING version INTO next_version;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_completed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',p_notification::jsonb,'pending_target_resolution');
 IF public.work_item_finish('succeeded',next_version,p_work)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','completed','owner_version',next_version,'affected_dependencies',affected);
END $$;

-- The companion digest check would refuse the rewritten values; it is
-- replaced after the rewrite. artifact_files keeps its object check: its one
-- writer, artifact_commit_inspection, refuses any file whose fingerprint is
-- not the metadata envelope of its size.
ALTER TABLE public.placement_companions DROP CONSTRAINT placement_companions_fingerprint_check;

UPDATE public.artifact_files
SET fingerprint=jsonb_build_object('algorithm','metadata','value','meta:size='||size_bytes::text,'size_bytes',size_bytes)
WHERE fingerprint->>'algorithm' IS DISTINCT FROM 'metadata';

ALTER TABLE public.file_operations DISABLE TRIGGER USER;
UPDATE public.file_operations fo SET source_fingerprint=af.fingerprint
FROM public.artifact_files af
WHERE af.id=fo.artifact_file_id AND fo.source_fingerprint IS DISTINCT FROM af.fingerprint;
UPDATE public.file_operations
SET target_fingerprint=public.file_target_metadata_fingerprint(publication_checkpoint->'target',COALESCE(observed_target_size_bytes,expected_size_bytes))
WHERE target_fingerprint IS NOT NULL AND target_fingerprint->>'algorithm' IS DISTINCT FROM 'metadata'
  AND COALESCE(observed_target_size_bytes,expected_size_bytes) IS NOT NULL;
ALTER TABLE public.file_operations ENABLE TRIGGER USER;

ALTER TABLE public.postprocessing_plans DISABLE TRIGGER USER;
UPDATE public.postprocessing_plans p SET committed_operations=(
  SELECT jsonb_agg(CASE
    WHEN jsonb_typeof(x)<>'object' OR NOT (x ? 'fingerprint') OR x->'fingerprint'->>'algorithm'='metadata' THEN x
    WHEN fo.id IS NOT NULL THEN jsonb_set(x,'{fingerprint}',fo.source_fingerprint)
    WHEN (x->>'size') ~ '^(0|[1-9][0-9]{0,18})$' THEN jsonb_set(x,'{fingerprint}',
      jsonb_build_object('algorithm','metadata','value','meta:size='||(x->>'size'),'size_bytes',(x->>'size')::bigint))
    ELSE x END ORDER BY e.ord)
  FROM jsonb_array_elements(p.committed_operations) WITH ORDINALITY e(x,ord)
  LEFT JOIN public.file_operations fo ON fo.id=e.x->>'id')
WHERE jsonb_typeof(p.committed_operations)='array' AND EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.committed_operations) x
  WHERE jsonb_typeof(x)='object' AND x ? 'fingerprint' AND x->'fingerprint'->>'algorithm' IS DISTINCT FROM 'metadata');
ALTER TABLE public.postprocessing_plans ENABLE TRIGGER USER;

UPDATE public.media_fact_files SET source_fingerprint='meta:size='||size_bytes::text
WHERE source_fingerprint ~ '^[0-9a-f]{64}$';

UPDATE public.placement_companions SET fingerprint='meta:size='||size_bytes::text
WHERE fingerprint !~ '^meta:size=(0|[1-9][0-9]*)(;dev=[1-9][0-9]*;ino=[1-9][0-9]*)?$';

-- Verified artifacts' v1 snapshots become the v2 summaries of their rows.
WITH summary AS (
  SELECT af.artifact_id,count(*) AS n,
    count(*) FILTER (WHERE af.media_kind='video') AS n_video,
    count(*) FILTER (WHERE af.media_kind='subtitle') AS n_subtitle,
    count(*) FILTER (WHERE af.media_kind NOT IN ('video','subtitle')) AS n_other,
    COALESCE(sum(af.size_bytes),0) AS total,
    public.artifact_files_digest(jsonb_agg(jsonb_build_object('file_ref',af.file_ref,'relative_path',af.relative_path,
      'media_kind',af.media_kind,'size_bytes',af.size_bytes,'fingerprint',af.fingerprint))) AS digest
  FROM public.artifact_files af GROUP BY af.artifact_id)
UPDATE public.artifacts a SET
  inventory_snapshot=(a.inventory_snapshot-ARRAY['files','inspected_files','inspected_primary_file'])||jsonb_build_object(
    'schema_version','artifact-inspect/v2','primary_file_ref',a.inventory_snapshot->'primary_file_ref','file_count',s.n,
    'video_count',s.n_video,'subtitle_count',s.n_subtitle,'other_count',s.n_other,'total_size_bytes',s.total,'files_digest',s.digest),
  fingerprint_snapshot=jsonb_build_object('schema_version','artifact-fingerprint/v2',
    'primary_file_ref',a.inventory_snapshot->'primary_file_ref','file_count',s.n,'files_digest',s.digest)
FROM summary s
WHERE s.artifact_id=a.id AND a.inventory_snapshot->>'schema_version'='artifact-inspect/v1'
  AND jsonb_typeof(a.inventory_snapshot->'primary_file_ref')='string' AND s.digest IS NOT NULL;

ALTER TABLE public.placement_companions ADD CONSTRAINT placement_companions_fingerprint_check
  CHECK (fingerprint ~ '^meta:size=(0|[1-9][0-9]*)(;dev=[1-9][0-9]*;ino=[1-9][0-9]*)?$');

ALTER TABLE public.acquisitions DROP CONSTRAINT acquisitions_manifest_check;
ALTER TABLE public.acquisitions ADD CONSTRAINT acquisitions_manifest_check
  CHECK ((manifest_digest ~ '^sha256:[0-9a-f]{64}$') AND manifest_item_count BETWEEN 0 AND 20000);

ALTER TABLE public.placements DROP CONSTRAINT placements_source_fingerprint_check;
ALTER TABLE public.placements ADD CONSTRAINT placements_source_fingerprint_check
  CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 512);
ALTER TABLE public.placements DROP CONSTRAINT placements_target_fingerprint_check;
ALTER TABLE public.placements ADD CONSTRAINT placements_target_fingerprint_check
  CHECK (char_length(btrim(target_fingerprint)) BETWEEN 1 AND 512);
ALTER TABLE public.replacement_deletions DROP CONSTRAINT replacement_deletions_source_fingerprint_check;
ALTER TABLE public.replacement_deletions ADD CONSTRAINT replacement_deletions_source_fingerprint_check
  CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 512);
ALTER TABLE public.alignment_operations DROP CONSTRAINT alignment_operations_source_fingerprint_check;
ALTER TABLE public.alignment_operations ADD CONSTRAINT alignment_operations_source_fingerprint_check
  CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 512);
ALTER TABLE public.offset_migration_plans DROP CONSTRAINT offset_migration_plans_source_fingerprint_check;
ALTER TABLE public.offset_migration_plans ADD CONSTRAINT offset_migration_plans_source_fingerprint_check
  CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 512);

-- The replaced shapes check themselves: the digest helper's domain and field
-- framing, the target helper's identity rule, and the stored bodies.
DO $$
BEGIN
 IF public.artifact_files_digest('[{"file_ref":"b","relative_path":"x","media_kind":"video","size_bytes":1,"fingerprint":{"value":"meta:size=1"}},
   {"file_ref":"a","relative_path":"y","media_kind":"subtitle","size_bytes":22,"fingerprint":{"value":"meta:size=22"}}]'::jsonb)
  IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('artifact-files:v1','UTF8')||decode('00','hex')
   ||convert_to('1:a1:y8:subtitle2:2212:meta:size=221:b1:x5:video1:111:meta:size=1','UTF8')),'hex')
 OR public.artifact_files_digest('[{"file_ref":"a","relative_path":"y","media_kind":"video","size_bytes":1.5,"fingerprint":{"value":"v"}}]'::jsonb) IS NOT NULL
 OR public.file_target_metadata_fingerprint('{"size":7,"identity":{"kind":"local_fs","device":"3","inode":"44"}}'::jsonb,9)
  IS DISTINCT FROM '{"algorithm":"metadata","value":"meta:size=7;dev=3;ino=44","size_bytes":7}'::jsonb
 OR public.file_target_metadata_fingerprint('{"size":7,"identity":{"kind":"local_fs","device":"0","inode":"44"}}'::jsonb,9)
  IS DISTINCT FROM '{"algorithm":"metadata","value":"meta:size=7","size_bytes":7}'::jsonb
 OR public.file_target_metadata_fingerprint(NULL,9)
  IS DISTINCT FROM '{"algorithm":"metadata","value":"meta:size=9","size_bytes":9}'::jsonb THEN
  RAISE EXCEPTION 'migration 102 self-check failed: fingerprint helpers';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_proc WHERE proname='artifact_commit_inspection'
   AND prosrc LIKE '%artifact-inspect/v2%' AND prosrc LIKE '%artifact_files_digest%' AND prosrc NOT LIKE '%inspected_files'')%')
 OR NOT EXISTS(SELECT 1 FROM pg_proc WHERE proname='file_success_atomic_body' AND prosrc LIKE '%file_target_metadata_fingerprint%')
 OR EXISTS(SELECT 1 FROM public.artifact_files WHERE fingerprint->>'algorithm' IS DISTINCT FROM 'metadata')
 OR EXISTS(SELECT 1 FROM public.file_operations WHERE source_fingerprint->>'algorithm' IS DISTINCT FROM 'metadata') THEN
  RAISE EXCEPTION 'migration 102 self-check failed: inspection bodies or rewritten rows';
 END IF;
END $$;
