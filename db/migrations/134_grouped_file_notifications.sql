-- Keep a bound subtitle's successful file operation out of the delivery stream.
-- Its main video waits for the whole bound group, then carries the final paths.
ALTER TABLE public.notification_admissions DROP CONSTRAINT notification_admissions_state_check;
ALTER TABLE public.notification_admissions ADD CONSTRAINT notification_admissions_state_check
    CHECK (state IN ('pending_target_resolution', 'pending_subtitle_completion', 'admission_failed', 'resolved'));

CREATE OR REPLACE FUNCTION public.file_notification_relative_path(p_root text, p_target text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog','public'
AS $$
DECLARE root_path text := rtrim(p_root,'/'); rel text;
BEGIN
 IF p_target IS NULL OR root_path IS NULL OR root_path='' THEN RETURN NULL; END IF;
 IF left(p_target,length(root_path)+1)<>root_path||'/' THEN RETURN NULL; END IF;
 rel:=substring(p_target FROM length(root_path)+2);
 IF rel='' OR rel='..' OR left(rel,3)='../' THEN RETURN NULL; END IF;
 RETURN public.file_notification_text(rel);
END $$;
REVOKE ALL ON FUNCTION public.file_notification_relative_path(text,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.file_subtitle_group_release(p_video_operation text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog','public'
AS $$
DECLARE v public.file_operations; a public.notification_admissions;
 pending integer; root_path text; paths jsonb; merged jsonb; updated integer;
BEGIN
 SELECT * INTO STRICT v FROM public.file_operations WHERE id=p_video_operation FOR UPDATE;
 IF v.bound_video_operation_id IS NOT NULL THEN RETURN 0; END IF;
 SELECT count(*) INTO pending FROM public.file_operations c
 WHERE c.acquisition_id=v.acquisition_id AND c.bound_video_operation_id=v.id
   AND c.state NOT IN ('completed','failed');
 IF pending>0 THEN RETURN 0; END IF;
 SELECT * INTO a FROM public.notification_admissions
 WHERE source_owner_type='file_operation' AND source_owner_id=v.id
   AND trigger_kind='file_operation_completed' AND state='pending_subtitle_completion' FOR UPDATE;
 IF a.id IS NULL THEN RETURN 0; END IF;
 root_path:=v.frozen_render_snapshot->>'notification_root';
 SELECT COALESCE(jsonb_agg(x.rel ORDER BY x.ordinal,x.file_ref COLLATE "C"),'[]'::jsonb) INTO paths
 FROM (
  SELECT f.ordinal,f.file_ref,
   public.file_notification_relative_path(root_path,f.final_object->'location'->>'canonical_path') AS rel
  FROM public.artifact_files af
  JOIN public.acquisition_files f ON f.acquisition_id=v.acquisition_id AND f.file_ref=af.file_ref
  WHERE af.artifact_id=v.artifact_id AND af.bound_video_file_ref=v.file_ref
    AND af.media_kind='subtitle' AND f.outcome='succeeded'
 ) x WHERE x.rel IS NOT NULL;
 merged:=a.payload||jsonb_build_object('out.subtitles',paths);
 IF octet_length(merged::text) NOT BETWEEN 1 AND 65536 OR public.file_notification_safe(merged) IS NOT TRUE THEN
  UPDATE public.notification_admissions SET state='admission_failed',failure_code='notification_payload_invalid',
   failure_reason='subtitle path list exceeds notification payload bounds'
  WHERE id=a.id AND state='pending_subtitle_completion';
  RETURN 0;
 END IF;
 UPDATE public.notification_admissions SET payload=merged,state='pending_target_resolution'
 WHERE id=a.id AND state='pending_subtitle_completion';
 GET DIAGNOSTICS updated=ROW_COUNT;
 RETURN updated;
END $$;
REVOKE ALL ON FUNCTION public.file_subtitle_group_release(text) FROM PUBLIC;

-- file_success_notification is the byte-parity mirror of the Go payload builder.
CREATE OR REPLACE FUNCTION file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 relative text; directory text; output_name text; root_path text; target_path text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input INTO STRICT input FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 src:=input->'source';
 IF p_fact->'render_inputs'->'binding'->>'state'='known' AND p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'state'='known' THEN
 title:=p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'value'; END IF;
 IF src->'release'->>'state'='known' THEN release_id:=src->'release'->'value'->>'ref'; END IF;
 name:=regexp_replace(rtrim(f.relative_path,'/'),'^.*/','');
 IF public.file_notification_text(name) IS NOT NULL THEN
 extension:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'^.*\.','') ELSE '' END;
 base:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'\.[^.]*$','') ELSE name END;
 END IF;
 location:=e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'location';
 root_path:=o.frozen_render_snapshot->>'notification_root';target_path:=location->>'canonical_path';
 relative:=public.file_notification_relative_path(root_path,target_path);
 IF relative IS NOT NULL THEN
 output_name:=regexp_replace(relative,'^.*/','');
 directory:=CASE WHEN strpos(relative,'/')>0 THEN regexp_replace(relative,'/[^/]*$','') ELSE '' END;
 ELSE output_name:=name; END IF;
 RETURN jsonb_build_object(
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.title_override',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.stem',public.file_notification_text(base),'file.ext',public.file_notification_text(lower(extension)),
 'file.size',f.size_bytes,'file.kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'out.dir',public.file_notification_text(directory),'out.name',public.file_notification_text(output_name),'out.path',public.file_notification_text(relative),
 'out.subtitles','[]'::jsonb,
 'op.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'op.conflict',public.file_notification_text(e.collision_policy))
 || public.notification_input_facts(input)
 || public.notification_episode_facts(p_fact->'identity_coverage'->'identity')
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

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
 IF p_notification IS NULL OR jsonb_typeof(p_notification::jsonb) IS DISTINCT FROM 'object'
 OR octet_length(p_notification) NOT BETWEEN 1 AND 65536
 OR public.file_notification_safe(p_notification::jsonb) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
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
 UPDATE public.file_operations SET state='completed',failure_code=NULL,failure_reason=NULL,
 target_fingerprint=public.file_target_metadata_fingerprint(e.publication_step_checkpoint->'publish_target'->'proof'->'target',
  (e.publication_frozen_objects->'source'->>'size')::bigint),
 publication_checkpoint=e.publication_step_checkpoint->'publish_target'->'proof',
 observed_at=clock_timestamp(),version=version+1 WHERE id=p_operation RETURNING version INTO next_version;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_completed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',p_notification::jsonb,
 CASE WHEN o.bound_video_operation_id IS NOT NULL THEN 'resolved'
  WHEN EXISTS(SELECT 1 FROM public.artifact_files af WHERE af.artifact_id=o.artifact_id AND af.bound_video_file_ref=o.file_ref)
  THEN 'pending_subtitle_completion' ELSE 'pending_target_resolution' END);
 IF public.work_item_finish('succeeded',next_version,p_work)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 PERFORM public.file_subtitle_group_release(COALESCE(o.bound_video_operation_id,o.id));
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','completed','owner_version',next_version,'affected_dependencies',affected);
END $$;

CREATE OR REPLACE FUNCTION file_failure_atomic_body(p_operation text, p_work text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; child public.file_operations; refs text[];
 effect text; cleanup text; receipt_text text; next_version bigint; dependents integer:=0; payload jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>1024 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_SHAPE'; END IF;
 IF EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref AND r.outcome IS NOT NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_RESULT_EXISTS'; END IF;
 effect:=public.file_failure_effect_class(o.id);
 cleanup:=CASE WHEN o.operation='move' AND effect<>'no_external_effect' THEN 'uncertain' ELSE 'not_required' END;
 WITH RECURSIVE descendants(operation_id, file_ref) AS (
  SELECT c.id, c.file_ref FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.bound_video_operation_id=o.id
  UNION
  SELECT c.id, c.file_ref FROM public.file_operations c JOIN descendants p ON c.bound_video_operation_id=p.operation_id
 ) SELECT array_agg(d.file_ref ORDER BY d.file_ref COLLATE "C") INTO refs FROM descendants d
 WHERE NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=d.file_ref AND r.outcome IS NOT NULL);
 IF EXISTS(SELECT 1 FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs)
  AND (c.state NOT IN ('pending','running') OR c.file_attempts_started<>0 OR c.file_attempt_ticket IS NOT NULL
   OR EXISTS(SELECT 1 FROM public.file_operations e WHERE e.publication_permit_id IS NOT NULL AND e.id=c.id)
   OR EXISTS(SELECT 1 FROM public.work_item_history cw WHERE cw.owner_type='file_operation' AND cw.owner_id=c.id AND (cw.state<>'available' OR cw.attempt<>0)))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DEPENDENCY_HANDOFF_EFFECT_CONFLICT'; END IF;
 UPDATE public.file_operations SET state='failed',failure_code=p_code,failure_reason=NULLIF(p_reason,''),
 observed_at=clock_timestamp(),version=version+1 WHERE id=o.id RETURNING version INTO next_version;
 receipt_text:=jsonb_build_object('reason_code',p_code,'schema_version',1,'effect_class',effect)::text;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class=effect,
 delivery_source_cleanup=cleanup,delivery_reason_code=p_code,
 evidence_digest=public.file_proof_digest('acquisition-effect-resolution:v1',receipt_text),operation_id=o.id,
 receipt=receipt_text::jsonb,delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
 payload:=public.file_failure_notification(o.id,p_code,p_reason);
 IF public.file_notification_safe(payload) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_failed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',payload,'pending_target_resolution');
 FOR child IN SELECT c.* FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs) ORDER BY c.id COLLATE "C" LOOP
  receipt_text:='{"reason_code":"DEPENDENCY_FAILED","schema_version":1}';
  UPDATE public.file_operations SET state='failed',failure_code='DEPENDENCY_FAILED',failure_reason='Dependency failed',version=version+1 WHERE id=child.id;
  UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
  delivery_source_cleanup='not_required',delivery_reason_code='DEPENDENCY_FAILED',
  evidence_digest=public.file_proof_digest('acquisition-effect-resolution:v1',receipt_text),operation_id=child.id,
  receipt=receipt_text::jsonb,delivered_at=CURRENT_TIMESTAMP
  WHERE acquisition_id=o.acquisition_id AND file_ref=child.file_ref;
  PERFORM public.work_item_finish('failed',child.version+1,cw.id) FROM public.work_items cw WHERE cw.owner_type='file_operation' AND cw.owner_id=child.id AND cw.state='available';
  dependents:=dependents+1;
 END LOOP;
 IF public.work_item_fail_at_attempt_budget(next_version,p_code,p_reason,p_work,o.version)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 IF o.bound_video_operation_id IS NOT NULL THEN PERFORM public.file_subtitle_group_release(o.bound_video_operation_id); END IF;
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','failed','owner_version',next_version,'effect_class',effect,'failed_dependencies',dependents);
END $$;
