-- A partial task binds its qB connection identity when it freezes the path.
-- The rest of the admitted input is still immutable.
CREATE OR REPLACE FUNCTION download_freeze_path_snapshot(p_task text, p_work text, p_token bigint, p_expected_version bigint, p_frozen_input jsonb) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; work public.work_item_history%ROWTYPE;
BEGIN
    task := public.download_stage_lock(p_task,p_work,p_token);
    SELECT * INTO STRICT work FROM public.work_item_history WHERE id=p_work;
    IF work.kind IS DISTINCT FROM 'qb_submit' OR task.version IS DISTINCT FROM p_expected_version THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_STAGE_STALE_CLAIM';
    END IF;
    IF task.submit_dispatch_started_at IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_DISPATCH_STARTED';
    END IF;
    IF (jsonb_typeof(p_frozen_input)='object'
        AND jsonb_typeof(p_frozen_input->'path_snapshot')='object'
        AND p_frozen_input->'path_snapshot'<>'{}'::jsonb
        AND (p_frozen_input-'path_snapshot'-'partial_namespace')=(task.frozen_input-'path_snapshot'-'partial_namespace')
        AND CASE WHEN jsonb_typeof(task.frozen_input->'partial_download')='object' THEN
            jsonb_typeof(p_frozen_input->'partial_namespace')='string'
            AND p_frozen_input->>'partial_namespace' ~ '^sha256:[0-9a-f]{64}$'
            AND (task.frozen_input->'partial_namespace' IS NULL
                OR task.frozen_input->'partial_namespace'=p_frozen_input->'partial_namespace')
        ELSE p_frozen_input->'partial_namespace' IS NULL AND task.frozen_input->'partial_namespace' IS NULL END
    ) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_SNAPSHOT_SHAPE';
    END IF;
    IF task.frozen_input->'path_snapshot' IS NOT NULL
        AND task.frozen_input->'path_snapshot'<>'null'::jsonb THEN
        IF task.frozen_input IS DISTINCT FROM p_frozen_input THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_SNAPSHOT_CONFLICT';
        END IF;
        RETURN task.version;
    END IF;
    UPDATE public.download_tasks SET frozen_input=p_frozen_input,
        version=version+1,updated_at=clock_timestamp() WHERE id=task.id
        RETURNING version INTO task.version;
    UPDATE public.work_items SET expected_owner_version=task.version,
        updated_at=clock_timestamp() WHERE id=p_work;
    RETURN task.version;
END;
$$;

-- A partial download may change qB priorities only during its active sync poll.
-- Record the start permission before the request leaves the process, so a later
-- worker observes rather than resending an ambiguous start.
CREATE FUNCTION download_authorize_partial_write(p_task text, p_work text, p_token bigint, p_session text, p_ordinal integer, p_start boolean) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz; deadline timestamptz;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='PARTIAL_DOWNLOAD_REQUIRES_ONE_SHOT';
    END IF;
    SELECT * INTO STRICT marker FROM public.one_shot_acquisitions WHERE id=one_shot->>'marker_id' FOR UPDATE;
    IF (marker.state='running' AND marker.admission_epoch=(one_shot->>'admission_epoch')::bigint
        AND marker.download_task_id=p_task) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    task := public.download_stage_lock(p_task,p_work,p_token);
    ticket := task.active_poll_ticket;
    SELECT least(lease_until,(ticket->>'attempt_until')::timestamptz,task.wait_until) INTO STRICT deadline
        FROM public.work_items WHERE id=p_work AND kind='qb_sync' AND input_snapshot->'one_shot'=one_shot;
    now_at := greatest(clock_timestamp(), task.budget_clock_at);
    IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
        AND (ticket->>'ordinal')::integer=p_ordinal AND ticket->>'executor_session'=p_session
        AND ticket->>'state'='issued' AND now_at<deadline) IS NOT TRUE
        OR task.state NOT IN ('submitting','downloading') OR p_start IS NULL
        OR jsonb_typeof(task.frozen_input->'partial_download') IS DISTINCT FROM 'object'
        OR task.frozen_input->'partial_start_sent' IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='PARTIAL_DOWNLOAD_WRITE_IDENTITY';
    END IF;
    IF task.cancel_cause IS NOT NULL OR EXISTS (
        SELECT 1 FROM public.acquisition_cancel_requests WHERE acquisition_id=task.acquisition_id
    ) THEN
        RETURN jsonb_build_object('granted',false);
    END IF;
    IF p_start THEN
        UPDATE public.download_tasks SET frozen_input=jsonb_set(frozen_input,'{partial_start_sent}','true') WHERE id=p_task;
    END IF;
    RETURN jsonb_build_object('granted',true,'remaining_ms',floor(extract(epoch FROM deadline-now_at)*1000)::bigint);
END;
$$;

REVOKE ALL ON FUNCTION download_authorize_partial_write(text,text,bigint,text,integer,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION download_authorize_partial_write(text,text,bigint,text,integer,boolean) TO bansoko_runtime;

CREATE OR REPLACE FUNCTION public.artifact_commit_inspection(p_artifact text, p_work text, p_token bigint, p_admission_digest text, p_manifest_digest text, p_manifest_files jsonb, p_inventory jsonb, p_fingerprints jsonb, p_files jsonb, p_plan text, p_plan_work text, p_plan_digest text, p_media_snapshot text, p_media_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE; m_manifest_digest text; v bigint; inherited jsonb; plan_work text;
 n_video bigint; n_subtitle bigint; n_other bigint; n_total numeric; files_digest text;
 partial jsonb; expected_count bigint; matched_count bigint; manifest_count integer;
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
 -- Keep the sealed torrent manifest intact. Only the inspection set can be smaller.
 SELECT frozen_input->'partial_download' INTO partial FROM public.download_tasks WHERE id=a.download_task_id;
 IF partial IS NOT NULL THEN
  SELECT manifest_item_count INTO manifest_count FROM public.acquisitions WHERE id=a.acquisition_id;
  IF m_manifest_digest IS NULL OR jsonb_typeof(partial->'paths') IS DISTINCT FROM 'array'
   OR partial->'file_count' IS DISTINCT FROM to_jsonb(manifest_count) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
  IF jsonb_array_length(partial->'paths') NOT BETWEEN 1 AND manifest_count-1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
 END IF;
 -- The earlier distinct-ref check and the manifest primary key make this a one-to-one match.
 WITH expected AS (
  SELECT f.file_ref,f.relative_path,f.exact_size FROM public.acquisition_files f
  WHERE f.acquisition_id=a.acquisition_id AND (partial IS NULL OR (partial->'paths') ? f.relative_path)
 )
 SELECT count(*),count(x.file_ref) INTO expected_count,matched_count
 FROM expected f LEFT JOIN jsonb_to_recordset(p_files) x(file_ref text,relative_path text,size_bytes bigint)
  ON x.file_ref=f.file_ref AND x.relative_path=f.relative_path AND x.size_bytes=f.exact_size;
 IF expected_count<>jsonb_array_length(p_files) OR matched_count<>expected_count
  OR (partial IS NOT NULL AND expected_count<>jsonb_array_length(partial->'paths')) THEN
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

CREATE OR REPLACE FUNCTION postprocessing_plan_clean(p_plan text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT p.state IN ('queued','waiting_config') AND p.sealed_at IS NULL
 AND a.state='verified' AND a.version=p.artifact_version
 AND EXISTS(SELECT 1 FROM public.acquisitions r WHERE r.id=p.acquisition_id AND r.manifest_digest IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.file_operations o WHERE o.artifact_id=p.artifact_id)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.outcome IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=f.acquisition_id AND h.requirements_digest IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=f.acquisition_id AND h.target_digest IS NOT NULL))
 FROM public.postprocessing_plans p JOIN public.artifacts a ON a.id=p.artifact_id AND a.acquisition_id=p.acquisition_id
 LEFT JOIN public.download_tasks d ON d.id=a.download_task_id
 WHERE p.id=p_plan
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest IS NOT NULL AND (
 h.requirements_manifest_digest IS DISTINCT FROM h.manifest_digest
 OR h.requirements_item_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL)
 OR h.requirements_required_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required')
 OR EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.requirement IS NULL)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.target_digest IS NOT NULL AND
 h.target_requirements_digest IS DISTINCT FROM h.requirements_digest)
 AND NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND (f.acquisition_id IS DISTINCT FROM p.acquisition_id
 OR NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.file_ref=f.file_ref
 AND m.relative_path=f.relative_path AND m.exact_size=f.size_bytes AND m.media_kind=f.media_kind)
 OR (d.frozen_input->'partial_download' IS NOT NULL AND
     ((d.frozen_input->'partial_download'->'paths') ? f.relative_path) IS NOT TRUE)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id
 AND (d.frozen_input->'partial_download' IS NULL OR (d.frozen_input->'partial_download'->'paths') ? m.relative_path)
 AND NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND f.file_ref=m.file_ref))
$$;
