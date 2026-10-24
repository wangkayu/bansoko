-- Forward-only migration 039: path-level publication verification.
--
-- A library file is identified by its path and size. Publication no longer
-- re-reads content, monitors the exact path or captures cleanup objects into a
-- private namespace; it works alike on local file systems, NFS and SMB.
--   * Object evidence is PathObjectV1 {location, file_type, size,
--     identity, observed_at}; identity is PathIdentityV1 {kind, device,
--     inode}. Device/inode are compared only between observations of one
--     invocation (the version reservation and what the rename displaced, a
--     hardlink and its source).
--   * Frozen publication objects drop source_capture/temp_capture.
--   * version/overwrite publish with rename(2) over the reserved or existing
--     target (atomic_method 'replace'); the displaced object is the one
--     observed at the final location just before the rename.
--   * temp_cleanup always removes the staged temp object (absent = done).
--   * Observed satisfaction is PathSatisfactionV1 (basis path_size).
--   * The library_exact_path_unverified diagnostic is retired.
--
-- In-flight publications carry the retired evidence shape and cannot be
-- finished under the new validators, so the migration refuses to run while
-- any exists. No other data migration: every environment is a test
-- environment.

DO $guard$
DECLARE pending bigint; finishing bigint;
BEGIN
 SELECT count(*) INTO pending FROM public.file_operations WHERE publication_dispatch_state IN ('prepared','dispatching');
 SELECT count(*) INTO finishing FROM public.file_operations WHERE publication_dispatch_state='closed' AND publication_effect_class='confirmed' AND state NOT IN ('completed','failed');
 IF pending>0 OR finishing>0 THEN
  RAISE EXCEPTION USING ERRCODE='55000',MESSAGE='PATH_LEVEL_PUBLICATION_MIGRATION_BLOCKED',
   DETAIL=format('%s publication(s) prepared or dispatching and %s confirmed publication(s) not yet finished; let them finish (or fail) before upgrading',pending,finishing);
 END IF;
END $guard$;

DELETE FROM diagnostic_episodes WHERE error_code='library_exact_path_unverified';

-- PathIdentityV1. Both identity anchors (this and observed_identity_valid)
-- accept the same shape: a real inode is never 0, so zero is refused.
CREATE OR REPLACE FUNCTION public.file_wire_identity_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 RETURN (public.observed_closed(v,ARRAY['kind','device','inode']) IS TRUE
  AND v->>'kind'='local_fs' AND public.file_wire_u64_valid(v->'device') IS TRUE
  AND public.file_wire_u64_valid(v->'inode') IS TRUE AND v->>'inode'<>'0') IS TRUE;
END $$;

CREATE OR REPLACE FUNCTION public.observed_identity_valid(p_value jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
 IF public.observed_closed(p_value,ARRAY['kind','device','inode']) IS NOT TRUE
 OR p_value->>'kind' IS DISTINCT FROM 'local_fs' OR jsonb_typeof(p_value->'device') IS DISTINCT FROM 'string' OR jsonb_typeof(p_value->'inode') IS DISTINCT FROM 'string'
 OR (p_value->>'device' ~ '^(0|[1-9][0-9]{0,19})$') IS NOT TRUE OR (p_value->>'inode' ~ '^[1-9][0-9]{0,19}$') IS NOT TRUE
 OR (p_value->>'device')::numeric>18446744073709551615 OR (p_value->>'inode')::numeric>18446744073709551615 THEN RETURN false; END IF;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $_$;

-- PathObjectV1: a regular file at a location with a size.
CREATE OR REPLACE FUNCTION public.file_final_object_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
 IF public.observed_closed(v,ARRAY['location','file_type','size','identity','observed_at']) IS NOT TRUE OR v->>'file_type' IS DISTINCT FROM 'regular'
 OR public.file_location_valid(v->'location') IS NOT TRUE OR public.file_wire_i64_valid(v->'size') IS NOT TRUE OR (v->>'size')::numeric<0
 OR public.file_wire_time_valid(v->'observed_at') IS NOT TRUE OR public.observed_identity_valid(v->'identity') IS NOT TRUE THEN RETURN false; END IF;
 RETURN true;
END $_$;

-- PathSatisfactionV1: the target holds a regular file of the expected
-- size. p_hash is kept in the signature for the plan commit caller and is
-- no longer compared (no content re-read).
CREATE OR REPLACE FUNCTION public.observed_satisfaction_valid(p_e jsonb, p_plan text, p_root text, p_artifact text, p_version bigint, p_ref text, p_input text, p_spec text, p_path text, p_size bigint, p_hash text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o jsonb; x jsonb;
BEGIN
 IF jsonb_typeof(p_e) IS DISTINCT FROM 'object' OR NOT (p_e ?& ARRAY['canonical','digest','observation','final_object'])
 OR (SELECT count(*) FROM jsonb_object_keys(p_e))<>4 OR public.plan_canonical_valid(p_e->>'canonical') IS NOT TRUE THEN RETURN false; END IF;
 x:=(p_e->>'canonical')::jsonb;o:=x->'observed_object';
 IF public.file_final_object_valid(o) IS NOT TRUE
 OR (o->'location'->>'namespace_digest' ~ '^sha256:[0-9a-f]{64}$') IS NOT TRUE
 OR left(o->'location'->>'root',1) IS DISTINCT FROM '/' OR length(o->'location'->>'root')>4096
 OR o->'location'->>'relative_path' ~ '(^/|(^|/)\.\.(/|$))'
 OR o->'location'->>'canonical_path' IS DISTINCT FROM rtrim(o->'location'->>'root','/')||'/'||(o->'location'->>'relative_path')
 OR length(o->'location'->>'relative_path') NOT BETWEEN 1 AND 4096
 OR (o->>'observed_at' ~ '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.[0-9]{1,6})?Z$') IS NOT TRUE THEN RETURN false; END IF;
 IF x IS DISTINCT FROM p_e->'observation' OR o IS DISTINCT FROM p_e->'final_object'
 OR public.observed_closed(x,ARRAY['schema_version','basis','acquisition_id','plan_id','artifact_id','artifact_version','file_ref','input_digest','spec_digest','expected_size','observed_object']) IS NOT TRUE
 OR x->'schema_version' IS DISTINCT FROM '2'::jsonb OR x->>'basis' IS DISTINCT FROM 'path_size'
 OR x->>'acquisition_id' IS DISTINCT FROM p_root OR x->>'plan_id' IS DISTINCT FROM p_plan OR x->>'artifact_id' IS DISTINCT FROM p_artifact
 OR x->'artifact_version' IS DISTINCT FROM to_jsonb(p_version) OR x->>'file_ref' IS DISTINCT FROM p_ref
 OR x->>'input_digest' IS DISTINCT FROM p_input OR x->>'spec_digest' IS DISTINCT FROM p_spec
 OR p_e->>'digest' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-observed-satisfaction:v1','UTF8')||decode('00','hex')||convert_to(p_e->>'canonical','UTF8')),'hex')
 OR o->'size' IS DISTINCT FROM to_jsonb(p_size) OR x->'expected_size' IS DISTINCT FROM to_jsonb(p_size)
 OR o->'location'->>'canonical_path' IS DISTINCT FROM p_path THEN RETURN false; END IF;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $_$;

CREATE OR REPLACE FUNCTION public.file_prepare_permit(p_operation text, p_work text, p_token bigint, p_session text, p_permit text, p_objects text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; p public.postprocessing_plans; t public.acquisition_files;
 objects jsonb; checkpoint jsonb:='{}'::jsonb; limits jsonb; dependency_digest text; step text; state text; now_at timestamptz; existing text;
BEGIN
 o:=public.file_execution_lock(p_operation,p_work,p_token);
 IF p_permit IS NULL OR char_length(p_permit) NOT BETWEEN 1 AND 128 OR p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128
  OR public.plan_canonical_valid(p_objects) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_SHAPE'; END IF;
 objects:=p_objects::jsonb;
 SELECT publication_permit_id INTO existing FROM public.file_operations WHERE publication_work_item_id=p_work AND publication_fencing_token=p_token;
 IF FOUND THEN
  IF (SELECT publication_executor_session FROM public.file_operations WHERE publication_permit_id=existing) IS DISTINCT FROM p_session THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_SESSION_MISMATCH'; END IF;
  RETURN jsonb_build_object('created',false,'permit_id',existing);
 END IF;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at);
 IF (o.file_attempt_ticket->>'work_item_id'=p_work AND (o.file_attempt_ticket->>'token')::bigint=p_token
  AND o.file_attempt_ticket->>'executor_session'=p_session AND o.file_attempt_ticket->>'state'='issued'
  AND now_at<(o.file_attempt_ticket->>'attempt_until')::timestamptz AND now_at<o.file_wall_until) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_TICKET_INVALID'; END IF;
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=o.postprocessing_plan_id;
 SELECT * INTO STRICT t FROM public.acquisition_files WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
 SELECT q.input->'limits'->'reconcile' INTO STRICT limits FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 IF public.observed_closed(objects,CASE WHEN o.collision_policy='overwrite' THEN ARRAY['source','source_parent','target','target_parent','temp','expected_target'] ELSE ARRAY['source','source_parent','target','target_parent','temp'] END) IS NOT TRUE
  OR public.file_final_object_valid(objects->'source') IS NOT TRUE
  OR public.observed_closed(objects->'source'->'location',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'target',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'temp',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_identity_valid(objects->'source_parent') IS NOT TRUE
  OR public.observed_identity_valid(objects->'target_parent') IS NOT TRUE
  OR objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path
  OR objects->'target'->>'canonical_path' IS DISTINCT FROM t.canonical_path
  OR t.canonical_path IS DISTINCT FROM o.target_path OR t.operation IS DISTINCT FROM o.operation
  OR t.intent IS DISTINCT FROM o.intent OR t.collision IS DISTINCT FROM o.collision_policy
  OR (objects->'source'->>'size')::bigint IS DISTINCT FROM o.expected_size_bytes
  OR objects->'temp'->>'canonical_path' IS DISTINCT FROM regexp_replace(o.target_path,'[^/]+$','')||'.bansoko-permit-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')
  OR public.file_location_valid(objects->'target') IS NOT TRUE OR public.file_location_valid(objects->'temp') IS NOT TRUE
  OR ((objects->'source'->>'observed_at')::timestamptz<=now_at) IS NOT TRUE
  OR (o.collision_policy='overwrite' AND (public.file_final_object_valid(objects->'expected_target') IS NOT TRUE
   OR objects->'expected_target'->'location' IS DISTINCT FROM objects->'target'
   OR ((objects->'expected_target'->>'observed_at')::timestamptz<=now_at) IS NOT TRUE))
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_OBJECT_BINDING'; END IF;
 -- Path-level verification: an approved replacement names the
 -- incumbent by location and size; device/inode are not compared across runs.
 IF o.collision_policy='overwrite' AND NOT EXISTS(SELECT 1 FROM public.acquisitions i WHERE i.id=o.acquisition_id
  AND ((i.input->'delivery'->>'intent'='replace'
  AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE
  r.value->'expected_object'->'location' IS NOT DISTINCT FROM objects->'expected_target'->'location'
  AND r.value->'expected_object'->'size' IS NOT DISTINCT FROM objects->'expected_target'->'size'))
  OR public.delivery_direct_overwrite(i.input->'delivery',o.target_path))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_OVERWRITE_PROOF_REQUIRED'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations e WHERE e.acquisition_id=o.acquisition_id AND e.publication_effect_key=o.idempotency_key
  AND (NOT e.publication_retry_safe OR e.publication_conflict_evidence IS NOT NULL)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_UNRESOLVED_EFFECT'; END IF;
 -- Dependency identity derives from the sealed graph and its historical
 -- delivery evidence, never from the current filesystem target observation.
 SELECT 'sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE(string_agg(
  parent.file_ref||':'||r.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C"),''),'UTF8')),'hex') INTO dependency_digest
 FROM public.file_operations parent JOIN public.acquisition_files r
 ON r.acquisition_id=parent.acquisition_id AND r.file_ref=parent.file_ref
 WHERE parent.id=o.bound_video_operation_id;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
  state:='not_started';
  IF (step IN ('stage_temp','temp_cleanup') AND o.operation='hardlink' AND o.collision_policy='block') OR
     (step='reserve_version' AND o.collision_policy<>'version') OR
     (step='source_cleanup' AND o.operation<>'move') THEN state:='not_required'; END IF;
  checkpoint:=checkpoint||jsonb_build_object(step,jsonb_build_object('state',state,'proof',NULL,'observed_at',NULL,'reason_code',NULL));
 END LOOP;
 -- S6: the slot is written onto the operation row. A retry_safe closed slot is
 -- replaced (guard_file_publication_identity admits it and bumps
 -- publication_attempt); every other slot shape is refused by that guard.
 UPDATE public.file_operations SET
  publication_permit_id=p_permit,
  publication_attempt=o.file_attempts_started,
  publication_evidence_version=1,
  publication_effect_key=o.idempotency_key,
  publication_spec_digest=t.spec_digest,
  publication_plan_digest=p.plan_digest,
  publication_dependency_digest=dependency_digest,
  publication_executor_session=p_session,
  publication_work_item_id=p_work,
  publication_fencing_token=p_token,
  publication_owner_version=o.version,
  publication_prepared_at=now_at,
  publication_dispatch_started_at=NULL,
  publication_unknown_since=NULL,
  publication_reconcile_until=NULL,
  publication_lookup_count=0,
  publication_lookup_ticket=NULL,
  publication_closed_proof=NULL,
  publication_resolution_proof=NULL,
  publication_closure_canonical=NULL,
  publication_resolution_canonical=NULL,
  publication_resolution_checkpoint_canonical=NULL,
  publication_closure_receipt_observed_at=NULL,
  publication_retry_safe=false,
  publication_stage_temp_started_at=NULL,
  publication_reserve_version_started_at=NULL,
  publication_publish_target_started_at=NULL,
  publication_source_cleanup_started_at=NULL,
  publication_temp_cleanup_started_at=NULL,
  publication_dispatch_state='prepared',
  publication_effect_class='unknown',
  publication_step_checkpoint=checkpoint,
  publication_step_proof_bytes='{}'::jsonb,
  publication_reconcile_wall_seconds=(limits->>'wall_seconds')::integer,
  publication_max_lookups=(limits->>'max_lookups')::integer,
  publication_frozen_objects=objects,
  publication_frozen_objects_canonical=p_objects,
  budget_clock_at=now_at
  WHERE id=o.id;
 RETURN jsonb_build_object('created',true,'permit_id',p_permit);
END $_$;

CREATE OR REPLACE FUNCTION public.file_record_receipt(p_permit text, p_receipt text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE e public.file_operations; envelope jsonb; identity jsonb; payload jsonb; checkpoint jsonb;
 closure jsonb; step text; old_slot jsonb; new_slot jsonb; proof jsonb; covered jsonb:='[]'::jsonb;
 root_id text; changed boolean:=false; at timestamptz; class text:='unknown'; closed jsonb; resolution jsonb;
 checkpoint_bytes text; closure_bytes text; proof_bytes text; proof_index jsonb; resolution_bytes text; checkpoint_digest text; closure_digest text;
BEGIN
 IF public.plan_canonical_valid(p_receipt) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CANONICAL_INVALID'; END IF;
 envelope:=p_receipt::jsonb;identity:=envelope->'identity';payload:=envelope->'payload';checkpoint:=payload->'checkpoint';closure:=payload->'closure';
 SELECT acquisition_id INTO STRICT root_id FROM public.file_operations WHERE publication_permit_id=p_permit;
 PERFORM id FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit FOR UPDATE;
 IF public.observed_closed(envelope,ARRAY['identity','emitter','observed_at','payload']) IS NOT TRUE
 OR identity IS DISTINCT FROM jsonb_build_object('schema_version',1,'permit_id',e.publication_permit_id,'root_id',e.acquisition_id,
  'original_work_id',e.publication_work_item_id,'fencing_token',e.publication_fencing_token,'executor_session',e.publication_executor_session,'spec_digest',e.publication_spec_digest)
 OR envelope->'emitter' IS DISTINCT FROM jsonb_build_object('mode','dispatch_session','session',e.publication_executor_session)
 OR public.observed_closed(payload,ARRAY['checkpoint','closure']) IS NOT TRUE
 OR public.observed_closed(checkpoint,ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup']) IS NOT TRUE
 OR public.observed_closed(closure,ARRAY['state','value','reason_code']) IS NOT TRUE
 OR e.publication_dispatch_state='prepared' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_IDENTITY_INVALID'; END IF;
 IF public.file_wire_time_valid(envelope->'observed_at') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
 at:=(envelope->>'observed_at')::timestamptz;
 IF at IS NULL OR at<e.publication_dispatch_started_at THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
 checkpoint_bytes:=public.file_json_field(public.file_json_field(p_receipt::json,'payload')::json,'checkpoint');
 closure_bytes:=public.file_json_field(public.file_json_field(public.file_json_field(p_receipt::json,'payload')::json,'closure')::json,'value');
 proof_index:=e.publication_step_proof_bytes;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
  old_slot:=e.publication_step_checkpoint->step;new_slot:=checkpoint->step;proof:=new_slot->'proof';
  IF public.observed_closed(new_slot,ARRAY['state','proof','observed_at','reason_code']) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_SHAPE'; END IF;
  IF old_slot->>'state' IN ('started','unknown','confirmed','no_effect') THEN covered:=covered||jsonb_build_array(step); END IF;
  IF new_slot IS NOT DISTINCT FROM old_slot THEN CONTINUE; END IF;
  proof_bytes:=public.file_json_field(public.file_json_field(checkpoint_bytes::json,step)::json,'proof');
  IF new_slot->>'state' IN ('unknown','confirmed','no_effect') THEN
   IF public.file_step_authorized_at(e,step) IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_NOT_GRANTED'; END IF;
   IF public.file_wire_time_valid(new_slot->'observed_at') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
   IF ((new_slot->>'observed_at')::timestamptz BETWEEN public.file_step_authorized_at(e,step) AND at) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
  END IF;
  IF old_slot->>'state' IN ('confirmed','no_effect') AND new_slot->>'state' IN ('confirmed','no_effect') THEN
   IF new_slot->>'state'=old_slot->>'state' AND proof IS NOT DISTINCT FROM old_slot->'proof' THEN checkpoint:=jsonb_set(checkpoint,ARRAY[step],old_slot);CONTINUE; END IF;
   IF proof_index->>step IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_ORIGINAL_BYTES_MISSING'; END IF;
   RETURN public.file_record_conflict(e.publication_permit_id,step,public.file_proof_digest('file-step-proof:v1',proof_index->>step),public.file_proof_digest('file-step-proof:v1',proof_bytes),at);
  END IF;
  IF (old_slot->>'state' IN ('started','unknown') AND new_slot->>'state' IN ('confirmed','unknown')
   AND (new_slot->>'observed_at')::timestamptz>=(old_slot->>'observed_at')::timestamptz
   AND (new_slot->>'observed_at')::timestamptz<=at) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_NOT_GRANTED'; END IF;
  IF new_slot->>'state'='unknown' THEN
   IF proof IS DISTINCT FROM 'null'::jsonb OR (new_slot->>'reason_code' ~ '^[A-Z][A-Z0-9_]{0,127}$') IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_UNKNOWN_SHAPE'; END IF;
  ELSE
   IF new_slot->'reason_code' IS DISTINCT FROM 'null'::jsonb THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CONFIRMED_SHAPE'; END IF;
   -- Object closure first: every nested object a step proof carries must be
   -- a closed PathObjectV1 / PathIdentityV1 before any binding.
   IF (step IN ('stage_temp','reserve_version') AND (public.file_final_object_valid(proof->'object') IS NOT TRUE OR public.observed_identity_valid(proof->'parent') IS NOT TRUE))
    OR (step='publish_target' AND (public.file_final_object_valid(proof->'target') IS NOT TRUE OR public.observed_identity_valid(proof->'parent') IS NOT TRUE
     OR public.observed_closed(proof->'displaced',ARRAY['state','value','reason_code']) IS NOT TRUE
     OR (proof->'displaced'->>'state'='known' AND public.file_final_object_valid(proof->'displaced'->'value') IS NOT TRUE)))
    OR (step IN ('source_cleanup','temp_cleanup') AND (public.file_final_object_valid(proof->'before') IS NOT TRUE
     OR public.file_location_valid(proof->'after'->'location') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'after'->'parent') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'parent') IS NOT TRUE
     OR public.file_wire_time_valid(proof->'after'->'observed_at') IS NOT TRUE)) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
   CASE step
   WHEN 'stage_temp' THEN
    IF public.observed_closed(proof,ARRAY['kind','object','parent','verified_source_digest','flushed']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'created_temp' OR proof->'flushed' IS DISTINCT FROM 'true'::jsonb
     OR proof->'object'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR e.publication_frozen_objects_canonical IS NULL OR proof->>'verified_source_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source')) THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TEMP_BINDING'; END IF;
   WHEN 'reserve_version' THEN
    IF public.observed_closed(proof,ARRAY['kind','object','parent','base_path','candidate_ordinal','reservation_method']) IS NOT TRUE
     OR e.collision_policy<>'version' OR proof->>'kind' IS DISTINCT FROM 'reserved_path' OR proof->>'reservation_method' IS DISTINCT FROM 'exclusive_create'
     OR proof->>'base_path' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'canonical_path'
     OR (proof->>'candidate_ordinal')::bigint NOT BETWEEN 1 AND 1001
     OR proof->'object'->'location'->>'canonical_path' IS DISTINCT FROM public.file_version_candidate(e.publication_frozen_objects->'target'->>'canonical_path',(proof->>'candidate_ordinal')::bigint)
     OR proof->'object'->'location'->>'root' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'root'
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->'object'->'size' IS DISTINCT FROM '0'::jsonb THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_RESERVATION_BINDING'; END IF;
   WHEN 'publish_target' THEN
    -- block publishes without replacing (renameat2 NOREPLACE, link, or an
    -- absent check plus rename); version/overwrite rename over the reserved
    -- or existing target. The displaced object is the one observed at the
    -- final location just before the rename: for version the reservation of
    -- this invocation (same device/inode, empty), for overwrite a regular
    -- file of the frozen expected size.
    IF public.observed_closed(proof,ARRAY['kind','source_evidence_digest','target','parent','atomic_method','displaced','dependency_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'published' OR proof->>'atomic_method' IS DISTINCT FROM (CASE WHEN e.collision_policy IN ('version','overwrite') THEN 'replace' ELSE 'no_replace' END)
     OR proof->'target'->'location' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN checkpoint->'reserve_version'->'proof'->'object'->'location' ELSE e.publication_frozen_objects->'target' END)
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->>'dependency_digest' IS DISTINCT FROM e.publication_dependency_digest
     OR e.publication_frozen_objects_canonical IS NULL OR proof->>'source_evidence_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
     OR (e.collision_policy NOT IN ('version','overwrite') AND proof->'displaced' IS DISTINCT FROM jsonb_build_object('state','absent','value',NULL,'reason_code',NULL))
     OR (e.collision_policy='overwrite' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'target'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'size'))
     OR (e.collision_policy='version' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'location'
      OR proof->'displaced'->'value'->'identity' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'identity'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM '0'::jsonb)) THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_PUBLICATION_BINDING'; END IF;
   WHEN 'source_cleanup','temp_cleanup' THEN
    IF public.observed_closed(proof,ARRAY['kind','before','parent','after','mutation_receipt_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'removed_object'
     OR public.observed_closed(proof->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE
     OR proof->'after'->>'method' IS DISTINCT FROM 'lstat_enoent'
     OR proof->'before' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source' ELSE checkpoint->'stage_temp'->'proof'->'object' END)
     OR proof->'parent' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source_parent' ELSE e.publication_frozen_objects->'target_parent' END)
     OR proof->'after'->'parent' IS DISTINCT FROM proof->'parent'
     OR proof->'after'->'location' IS DISTINCT FROM proof->'before'->'location'
     OR proof->>'mutation_receipt_digest' IS DISTINCT FROM public.file_proof_digest('file-mutation-receipt:v1',
      '{"before":'||public.file_json_field(proof_bytes::json,'before')||',"permit_id":'||to_json(e.publication_permit_id)::text||',"removed_at":'||to_json(checkpoint->step->>'observed_at')::text||',"step":'||to_json(step)::text||'}') THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLEANUP_BINDING'; END IF;
   END CASE;
   -- The staged and published object is a regular file of the frozen source
   -- size; a hardlink is the source object itself (same invocation, so
   -- device/inode are comparable).
   IF step IN ('stage_temp','publish_target') THEN
    DECLARE object jsonb:=CASE WHEN step='stage_temp' THEN proof->'object' ELSE proof->'target' END;
    BEGIN
     IF object->'size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size'
      OR (e.operation='hardlink' AND object->'identity' IS DISTINCT FROM e.publication_frozen_objects->'source'->'identity') THEN
      RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
    END;
   END IF;
  END IF;
  IF new_slot->>'state'='confirmed' THEN proof_index:=proof_index||jsonb_build_object(step,proof_bytes); END IF;
  changed:=true;
 END LOOP;
 closed:=e.publication_closed_proof;
 IF closure->>'state'='known' THEN
  IF public.file_wire_time_valid(closure->'value'->'joined_at') IS NOT TRUE OR public.file_wire_id_valid(closure->'value'->'boot_nonce') IS NOT TRUE OR public.file_wire_id_valid(closure->'value'->'call_nonce') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF EXISTS(SELECT 1 FROM jsonb_each(checkpoint) s WHERE s.value->>'state' IN ('confirmed','unknown','no_effect') AND ((s.value->>'observed_at')::timestamptz<=(closure->'value'->>'joined_at')::timestamptz) IS NOT TRUE) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF public.observed_closed(closure->'value',ARRAY['kind','executor_session','boot_nonce','call_nonce','covered_steps','joined_at','observer_version']) IS NOT TRUE
   OR closure->'reason_code' IS DISTINCT FROM 'null'::jsonb OR closure->'value'->>'kind' IS DISTINCT FROM 'local_join'
   OR closure->'value'->>'executor_session' IS DISTINCT FROM e.publication_executor_session
   OR e.publication_executor_session IS DISTINCT FROM (closure->'value'->>'boot_nonce')||':'||(closure->'value'->>'call_nonce')
   OR closure->'value'->'covered_steps' IS DISTINCT FROM covered
   OR char_length(closure->'value'->>'boot_nonce') NOT BETWEEN 1 AND 128
   OR char_length(closure->'value'->>'call_nonce') NOT BETWEEN 1 AND 128
   OR ((closure->'value'->>'joined_at')::timestamptz BETWEEN e.publication_dispatch_started_at AND at) IS NOT TRUE
  OR COALESCE(jsonb_typeof(closure->'value'->'observer_version'),'null') IS DISTINCT FROM 'string' OR COALESCE(char_length(closure->'value'->>'observer_version'),0) NOT BETWEEN 1 AND 128 THEN -- §13.16.35 Q11: null/type-confusion-safe (three-valued OR chain)
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF closed IS NOT NULL AND closed IS DISTINCT FROM closure->'value' THEN
   RETURN public.file_record_conflict(e.publication_permit_id,'closure',public.file_proof_digest('file-closure:v1',e.publication_closure_canonical),public.file_proof_digest('file-closure:v1',closure_bytes),at); END IF;
  IF closed IS NULL THEN changed:=true;closed:=closure->'value'; END IF;
 ELSIF (closure->>'state'='unavailable' AND closure->'value'='null'::jsonb AND closure->>'reason_code' ~ '^[A-Z][A-Z0-9_]{0,127}$') IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_SHAPE'; END IF;
 IF NOT changed THEN RETURN jsonb_build_object('recorded',false,'evidence_version',e.publication_evidence_version); END IF;
 -- Only complete facts derive a confirmed disposition. Real channel closure
 -- with remaining unknown slots stays unknown; joining never implies success.
 IF closed IS NOT NULL AND NOT EXISTS(SELECT 1 FROM jsonb_each(checkpoint) s WHERE s.value->>'state' NOT IN ('not_required','confirmed')) THEN class:='confirmed'; END IF;
 resolution_bytes:=e.publication_resolution_canonical;resolution:=e.publication_resolution_proof;
 IF class='confirmed' AND e.publication_conflict_evidence IS NULL AND resolution IS NULL THEN
  checkpoint_digest:=public.file_proof_digest('file-checkpoint:v1',checkpoint_bytes);
  closure_digest:=public.file_proof_digest('file-closure:v1',COALESCE(e.publication_closure_canonical,closure_bytes));
  resolution_bytes:='{"checkpoint_digest":"'||checkpoint_digest||'","closure_digests":["'||closure_digest||'"],"effect_class":"confirmed","schema_version":1}';
  resolution:=resolution_bytes::jsonb;
 END IF;
 at:=clock_timestamp(); -- first durable unknown uses DB time, not caller time
 UPDATE public.file_operations SET publication_step_checkpoint=checkpoint,publication_closed_proof=closed,
  publication_closure_receipt_observed_at=CASE WHEN closed->>'kind'='local_join' THEN COALESCE(e.publication_closure_receipt_observed_at,(envelope->>'observed_at')::timestamptz) ELSE e.publication_closure_receipt_observed_at END,
  publication_step_proof_bytes=proof_index,publication_closure_canonical=CASE WHEN closed IS NOT NULL THEN COALESCE(e.publication_closure_canonical,closure_bytes) ELSE NULL END,
  publication_resolution_proof=resolution,publication_resolution_canonical=resolution_bytes,
  publication_resolution_checkpoint_canonical=CASE WHEN resolution IS NOT NULL THEN COALESCE(e.publication_resolution_checkpoint_canonical,checkpoint_bytes) ELSE NULL END,
  publication_dispatch_state=CASE WHEN closed IS NOT NULL THEN 'closed' ELSE publication_dispatch_state END,publication_effect_class=class,
  publication_unknown_since=CASE WHEN class='unknown' THEN COALESCE(publication_unknown_since,at) ELSE publication_unknown_since END,
  publication_reconcile_until=CASE WHEN class='unknown' THEN COALESCE(publication_reconcile_until,COALESCE(publication_unknown_since,at)+make_interval(secs=>publication_reconcile_wall_seconds)) ELSE publication_reconcile_until END,
  publication_evidence_version=publication_evidence_version+1,updated_at=clock_timestamp() WHERE publication_permit_id=e.publication_permit_id;
 UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=root_id AND state='running' AND record_state='full';
 RETURN jsonb_build_object('recorded',true,'evidence_version',e.publication_evidence_version+1);
END $_$;

CREATE OR REPLACE FUNCTION public.file_validate_final_proof_before181(e file_operations) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE cp jsonb; cl jsonb; resolution jsonb;
 step text; slot jsonb; required boolean; covered jsonb:='[]'::jsonb;
BEGIN
 IF e.publication_dispatch_state IS DISTINCT FROM 'closed' OR e.publication_effect_class IS DISTINCT FROM 'confirmed'
 OR e.publication_conflict_evidence IS NOT NULL OR e.publication_conflict_index_exhausted
 OR public.plan_canonical_valid(e.publication_frozen_objects_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_resolution_checkpoint_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_closure_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_resolution_canonical) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_UNAVAILABLE'; END IF;
 cp:=e.publication_resolution_checkpoint_canonical::jsonb;cl:=e.publication_closure_canonical::jsonb;resolution:=e.publication_resolution_canonical::jsonb;
 IF cp IS DISTINCT FROM e.publication_step_checkpoint OR cl IS DISTINCT FROM e.publication_closed_proof
 OR resolution IS DISTINCT FROM e.publication_resolution_proof OR e.publication_frozen_objects_canonical::jsonb IS DISTINCT FROM e.publication_frozen_objects
 OR public.observed_closed(resolution,ARRAY['schema_version','effect_class','checkpoint_digest','closure_digests']) IS NOT TRUE
 OR resolution->'schema_version' IS DISTINCT FROM '1'::jsonb OR resolution->>'effect_class' IS DISTINCT FROM 'confirmed'
 OR resolution->>'checkpoint_digest' IS DISTINCT FROM public.file_proof_digest('file-checkpoint:v1',e.publication_resolution_checkpoint_canonical)
 OR resolution->'closure_digests' IS DISTINCT FROM jsonb_build_array(public.file_proof_digest('file-closure:v1',e.publication_closure_canonical)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_DIGEST_INVALID'; END IF;
 IF public.file_wire_id_valid(cl->'boot_nonce') IS NOT TRUE OR public.file_wire_id_valid(cl->'call_nonce') IS NOT TRUE
 OR public.file_wire_time_valid(cl->'joined_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
 slot:=cp->step;
 required:=NOT ((step='source_cleanup' AND e.operation<>'move') OR (step='reserve_version' AND e.collision_policy<>'version') OR (step IN ('stage_temp','temp_cleanup') AND e.operation='hardlink' AND e.collision_policy='block'));
 IF public.observed_closed(slot,ARRAY['state','proof','observed_at','reason_code']) IS NOT TRUE
 OR (required AND slot->>'state' IS DISTINCT FROM 'confirmed')
 OR (NOT required AND slot IS DISTINCT FROM jsonb_build_object('state','not_required','proof',NULL,'observed_at',NULL,'reason_code',NULL)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_STEP_INVALID'; END IF;
 IF required THEN
 IF public.plan_canonical_valid(e.publication_step_proof_bytes->>step) IS NOT TRUE OR (e.publication_step_proof_bytes->>step)::jsonb IS DISTINCT FROM slot->'proof' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_STEP_BYTES_INVALID'; END IF;
 IF public.file_wire_time_valid(slot->'observed_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF slot->'reason_code' IS DISTINCT FROM 'null'::jsonb
 OR ((slot->>'observed_at')::timestamptz>=e.publication_dispatch_started_at AND (slot->>'observed_at')::timestamptz<=(cl->>'joined_at')::timestamptz) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF (step='stage_temp' AND public.observed_closed(slot->'proof',ARRAY['kind','object','parent','verified_source_digest','flushed']) IS NOT TRUE)
 OR (step='publish_target' AND public.observed_closed(slot->'proof',ARRAY['kind','source_evidence_digest','target','parent','atomic_method','displaced','dependency_digest']) IS NOT TRUE)
 OR (step IN ('source_cleanup','temp_cleanup') AND (public.observed_closed(slot->'proof',ARRAY['kind','before','parent','after','mutation_receipt_digest']) IS NOT TRUE OR public.observed_closed(slot->'proof'->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_SHAPE_INVALID'; END IF;
 IF step='stage_temp' AND (slot->'proof'->>'kind' IS DISTINCT FROM 'created_temp'
 OR slot->'proof'->>'verified_source_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
 OR slot->'proof'->'flushed' IS DISTINCT FROM 'true'::jsonb
 OR slot->'proof'->'object'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TEMP_INVALID'; END IF;
 IF step='publish_target' AND (slot->'proof'->>'kind' IS DISTINCT FROM 'published'
 OR slot->'proof'->>'source_evidence_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
 OR slot->'proof'->>'dependency_digest' IS DISTINCT FROM e.publication_dependency_digest
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 IF step IN ('source_cleanup','temp_cleanup') AND (slot->'proof'->>'kind' IS DISTINCT FROM 'removed_object'
 OR slot->'proof'->'after'->>'method' IS DISTINCT FROM 'lstat_enoent'
 OR slot->'proof'->'parent' IS DISTINCT FROM CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source_parent' ELSE e.publication_frozen_objects->'target_parent' END
 OR slot->'proof'->'before' IS DISTINCT FROM CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source' ELSE cp->'stage_temp'->'proof'->'object' END
 OR slot->'proof'->'after'->'parent' IS DISTINCT FROM slot->'proof'->'parent'
 OR slot->'proof'->'after'->'location' IS DISTINCT FROM slot->'proof'->'before'->'location'
 OR slot->'proof'->>'mutation_receipt_digest' IS DISTINCT FROM public.file_proof_digest('file-mutation-receipt:v1',
 '{"before":'||public.file_json_field((e.publication_step_proof_bytes->>step)::json,'before')||',"permit_id":'||to_json(e.publication_permit_id)::text||',"removed_at":'||to_json(slot->>'observed_at')::text||',"step":'||to_json(step)::text||'}')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLEANUP_INVALID'; END IF;
 IF step='reserve_version' AND (public.observed_closed(slot->'proof',ARRAY['kind','object','parent','base_path','candidate_ordinal','reservation_method']) IS NOT TRUE
 OR e.collision_policy IS DISTINCT FROM 'version' OR slot->'proof'->>'kind' IS DISTINCT FROM 'reserved_path'
 OR slot->'proof'->>'reservation_method' IS DISTINCT FROM 'exclusive_create'
 OR slot->'proof'->>'base_path' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'canonical_path'
 OR ((slot->'proof'->>'candidate_ordinal')::bigint BETWEEN 1 AND 1001) IS NOT TRUE
 OR slot->'proof'->'object'->'location'->>'canonical_path' IS DISTINCT FROM public.file_version_candidate(e.publication_frozen_objects->'target'->>'canonical_path',(slot->'proof'->>'candidate_ordinal')::bigint)
 OR public.file_location_valid(slot->'proof'->'object'->'location') IS NOT TRUE
 OR slot->'proof'->'object'->'location'->>'root' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'root'
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
 OR slot->'proof'->'object'->'size' IS DISTINCT FROM '0'::jsonb) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_RESERVATION_INVALID'; END IF;
 IF step='reserve_version' AND public.file_final_object_valid(slot->'proof'->'object') IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF step='publish_target' AND (slot->'proof'->>'atomic_method' IS DISTINCT FROM CASE WHEN e.collision_policy IN ('version','overwrite') THEN 'replace' ELSE 'no_replace' END
 OR slot->'proof'->'target'->'location' IS DISTINCT FROM CASE WHEN e.collision_policy='version' THEN cp->'reserve_version'->'proof'->'object'->'location' ELSE e.publication_frozen_objects->'target' END
 OR (e.collision_policy='block' AND slot->'proof'->'displaced' IS DISTINCT FROM jsonb_build_object('state','absent','value',NULL,'reason_code',NULL))
 OR (e.collision_policy='version' AND (slot->'proof'->'displaced'->>'state' IS DISTINCT FROM 'known'
 OR slot->'proof'->'displaced'->'value'->'location' IS DISTINCT FROM cp->'reserve_version'->'proof'->'object'->'location'
 OR slot->'proof'->'displaced'->'value'->'identity' IS DISTINCT FROM cp->'reserve_version'->'proof'->'object'->'identity'
 OR slot->'proof'->'displaced'->'value'->'size' IS DISTINCT FROM '0'::jsonb))
 OR (e.collision_policy='overwrite' AND (slot->'proof'->'displaced'->>'state' IS DISTINCT FROM 'known'
 OR slot->'proof'->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'target'
 OR slot->'proof'->'displaced'->'value'->'size' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'size'))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 IF step IN ('stage_temp','publish_target') THEN
 DECLARE object jsonb:=CASE WHEN step='stage_temp' THEN slot->'proof'->'object' ELSE slot->'proof'->'target' END;
 BEGIN
 IF public.file_final_object_valid(object) IS NOT TRUE
 OR object->'size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size'
 OR ((object->>'observed_at')::timestamptz>=e.publication_dispatch_started_at AND (object->>'observed_at')::timestamptz<=(slot->>'observed_at')::timestamptz) IS NOT TRUE
 OR (e.operation='hardlink' AND object->'identity' IS DISTINCT FROM e.publication_frozen_objects->'source'->'identity') THEN
 -- Path-level qualification: a regular file of the frozen source size,
 -- observed inside the step; a hardlink is the source object itself.
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 END;
 END IF;
 covered:=covered||jsonb_build_array(step);
 END IF;
 END LOOP;
 IF public.observed_closed(cl,ARRAY['kind','executor_session','boot_nonce','call_nonce','covered_steps','joined_at','observer_version']) IS NOT TRUE
 OR cl->>'kind' IS DISTINCT FROM 'local_join' OR cl->>'executor_session' IS DISTINCT FROM e.publication_executor_session
 OR e.publication_executor_session IS DISTINCT FROM (cl->>'boot_nonce')||':'||(cl->>'call_nonce')
 OR cl->'covered_steps' IS DISTINCT FROM covered OR length(cl->>'boot_nonce') NOT BETWEEN 1 AND 128
 OR COALESCE(jsonb_typeof(cl->'observer_version'),'null') IS DISTINCT FROM 'string' OR COALESCE(length(cl->>'observer_version'),0) NOT BETWEEN 1 AND 128 -- §13.16.35 Q11: jsonb null/type confusion must not slip the three-valued OR chain (no upstream wire check covers observer_version)
 OR ((cl->>'joined_at')::timestamptz>=e.publication_dispatch_started_at) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
END $$;

CREATE OR REPLACE FUNCTION public.file_validate_final_proof(e file_operations) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE s text; a timestamptz; o timestamptz; j timestamptz; proof jsonb; object jsonb;
BEGIN
 PERFORM public.file_validate_final_proof_before181(e);
 IF public.file_final_object_valid(e.publication_frozen_objects->'source') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF e.collision_policy='overwrite' AND public.file_final_object_valid(e.publication_frozen_objects->'expected_target') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 j:=(e.publication_closed_proof->>'joined_at')::timestamptz;
 IF e.publication_closure_receipt_observed_at IS NULL OR NOT isfinite(e.publication_closure_receipt_observed_at) OR (j<=e.publication_closure_receipt_observed_at) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
 FOREACH s IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
 a:=public.file_step_authorized_at(e,s);
 IF e.publication_step_checkpoint->s->>'state'='not_required' THEN
 IF a IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 CONTINUE; END IF;
 IF a IS NULL OR NOT isfinite(a) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 o:=(e.publication_step_checkpoint->s->>'observed_at')::timestamptz;
 IF (e.publication_dispatch_started_at<=a AND a<=o AND o<=j) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 proof:=e.publication_step_checkpoint->s->'proof';
 IF s IN ('stage_temp','reserve_version','publish_target') THEN
 object:=CASE WHEN s='publish_target' THEN proof->'target' ELSE proof->'object' END;
 IF public.file_final_object_valid(object) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF s='publish_target' AND proof->'displaced'->>'state'='known' THEN
 IF public.file_wire_fv_valid(proof->'displaced') IS NOT TRUE OR public.file_final_object_valid(proof->'displaced'->'value') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF proof->'displaced'->'value'->'size' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN e.publication_step_checkpoint->'reserve_version'->'proof'->'object'->'size' ELSE e.publication_frozen_objects->'expected_target'->'size' END) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 END IF;
 IF public.file_wire_time_valid(object->'observed_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF (a<=(object->>'observed_at')::timestamptz AND (object->>'observed_at')::timestamptz<=o) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 ELSE
 IF public.file_final_object_valid(proof->'before') IS NOT TRUE OR public.observed_closed(proof->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE OR public.file_location_valid(proof->'after'->'location') IS NOT TRUE OR public.file_wire_identity_valid(proof->'after'->'parent') IS NOT TRUE OR proof->'after'->>'method' IS DISTINCT FROM 'lstat_enoent' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF public.file_wire_time_valid(proof->'after'->'observed_at') IS NOT TRUE OR ((proof->'after'->>'observed_at')::timestamptz BETWEEN a AND o) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 END IF;
 END LOOP;
END $$;

CREATE OR REPLACE FUNCTION public.file_assert_final_proof(p_permit text) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; o public.file_operations; dep text;
BEGIN
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=e.id;
 IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(o.file_attempt_index) x WHERE x->>'ordinal'=e.publication_attempt::text AND x->>'work_item_id'=e.publication_work_item_id AND x->>'token'=e.publication_fencing_token::text AND x->>'session_digest'='sha256:'||encode(sha256(convert_to(e.publication_executor_session,'UTF8')),'hex')) OR e.publication_effect_key IS DISTINCT FROM o.idempotency_key OR e.publication_frozen_objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path OR e.publication_frozen_objects->'source'->'size' IS DISTINCT FROM to_jsonb(o.expected_size_bytes) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 SELECT 'sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE(string_agg(parent.file_ref||':'||dep.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C"),''),'UTF8')),'hex') INTO dep FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id;
 IF dep IS DISTINCT FROM e.publication_dependency_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id AND dep.outcome IS DISTINCT FROM 'succeeded') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisition_files t JOIN public.acquisitions i ON i.id=t.acquisition_id WHERE t.acquisition_id=e.acquisition_id AND t.file_ref=e.file_ref AND t.canonical_path=o.target_path AND e.publication_frozen_objects->'target'->>'canonical_path'=o.target_path AND o.operation=t.operation AND o.intent=t.intent AND o.collision_policy=t.collision AND (e.collision_policy<>'overwrite' OR (i.input->'delivery'->>'intent'='replace' AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE r.value->'expected_object'->'location'=e.publication_frozen_objects->'expected_target'->'location' AND r.value->'expected_object'->'size'=e.publication_frozen_objects->'expected_target'->'size')) OR public.delivery_direct_overwrite(i.input->'delivery',o.target_path))) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 PERFORM public.file_validate_final_proof(e);
END $$;

CREATE OR REPLACE FUNCTION public.file_success_projection(p_operation text, p_permit text, p_file_fact text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; e public.file_operations;
 r public.acquisitions; fact jsonb; published_object jsonb; digest text; cleanup text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=o.acquisition_id;
 -- No locks are taken here: the fixed caller must already hold the complete
 -- root-first lock set. This component cannot finish a work item or grant I/O.
 IF public.plan_canonical_valid(p_file_fact) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESULT_FACT_CANONICAL_INVALID'; END IF;
 fact:=p_file_fact::jsonb;
 published_object:=e.publication_step_checkpoint->'publish_target'->'proof'->'target';
 digest:=public.file_proof_digest('acquisition-effect-resolution:v1',e.publication_resolution_canonical);
 cleanup:=CASE WHEN e.operation='move' THEN 'completed' ELSE 'not_required' END;
 IF (e.id=o.id AND e.acquisition_id=o.acquisition_id AND e.file_ref=o.file_ref
  AND e.publication_dispatch_state='closed' AND e.publication_effect_class='confirmed' AND e.publication_conflict_evidence IS NULL
  AND e.publication_resolution_canonical IS NOT NULL AND e.publication_closure_canonical IS NOT NULL
  AND e.publication_step_checkpoint->'publish_target'->>'state'='confirmed'
  AND NOT EXISTS(SELECT 1 FROM jsonb_each(e.publication_step_checkpoint) slot WHERE slot.value->>'state' NOT IN ('confirmed','not_required'))
  AND published_object IS NOT NULL AND published_object->'size'=e.publication_frozen_objects->'source'->'size'
  AND public.vars_filefact_valid(o.acquisition_id,fact)
  AND public.filefact_source_relation_valid(o.acquisition_id,o.artifact_id,o.file_ref,fact)
  AND fact->'provenance'->'acquisition_ref'->>'value'=o.acquisition_id
  AND fact->'provenance'->'artifact_ref'->>'value'=o.artifact_id
  AND fact->'provenance'->'operation_ref'->>'value'=o.id
  AND fact->'provenance'->'delivery_digest'->>'value'=digest
  AND fact->'locator'->'final_object'->'value'=published_object) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESULT_PROJECTION_INVALID'; END IF;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='succeeded',completion_mode='executed',
 effect_class='confirmed',final_object=published_object,delivery_source_cleanup=cleanup,evidence_digest=digest,
 operation_id=o.id,receipt=e.publication_resolution_proof,delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
END $$;

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
 OR p_notification::jsonb->'anime.custom_title' IS DISTINCT FROM 'null'::jsonb
 OR p_notification::jsonb->>'file_operation.kind' IS DISTINCT FROM e.operation
 OR p_notification::jsonb->>'file_operation.collision_policy' IS DISTINCT FROM e.collision_policy
 OR p_notification::jsonb->'file.size_bytes' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size' THEN
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
 -- fingerprint carries the frozen source fingerprint (the downloader's
 -- verified digest) with the published size.
 UPDATE public.file_operations SET state='completed',failure_code=NULL,failure_reason=NULL,
 target_fingerprint=jsonb_build_object('algorithm',o.source_fingerprint->>'algorithm','value',o.source_fingerprint->>'value',
  'size_bytes',COALESCE(e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'size',e.publication_frozen_objects->'source'->'size')),
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
