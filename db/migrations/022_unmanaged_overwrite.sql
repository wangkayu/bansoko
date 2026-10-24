-- Forward-only migration 022: the overwrite collision setting replaces an
-- unmanaged file.
--
-- The global/instance collision setting is authoritative. Under `overwrite`
-- a present target that no owner manages (a detached or foreign file) is
-- replaced by the existing atomic overwrite protocol; the replaced file is
-- deleted after commit. A managed target is overwritten only through an
-- approved quality replacement (DeliveryV3.replacements, unchanged); any
-- other managed target fails closed.
--
-- overwrite_target_managed is the one definition of "managed" that
-- acceptance, the file plan and the prepare proof read: an active or
-- superseded placement (a superseded file may still await cleanup), or an
-- active companion, names the path. Released placements are detached.
--
-- file_prepare_permit and file_assert_final_proof keep their approved
-- replacement branch byte for byte and add the unmanaged branch: an
-- overwrite delivery whose approvals do not name the target path. Prepare,
-- immediately before the effect, also requires the path to be unmanaged.
--

CREATE FUNCTION overwrite_target_managed(p_path text) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT EXISTS(SELECT 1 FROM public.placements WHERE state IN ('active','superseded') AND physical_path COLLATE "C"=p_path)
  OR EXISTS(SELECT 1 FROM public.placement_companions WHERE state='active' AND current_path COLLATE "C"=p_path)
$$;

CREATE FUNCTION delivery_unmanaged_overwrite(p_delivery jsonb, p_path text, p_check_managed boolean) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT p_delivery->>'collision'='overwrite' AND p_path IS NOT NULL
  AND NOT EXISTS(SELECT 1 FROM public.delivery_replacements(p_delivery) r(value)
   WHERE r.value->'expected_object'->'location'->>'canonical_path'=p_path)
  AND (NOT p_check_managed OR public.overwrite_target_managed(p_path) IS NOT TRUE)
$$;

REVOKE ALL ON FUNCTION overwrite_target_managed, delivery_unmanaged_overwrite FROM PUBLIC;
GRANT EXECUTE ON FUNCTION overwrite_target_managed, delivery_unmanaged_overwrite TO bansoko_runtime;

CREATE OR REPLACE FUNCTION file_assert_final_proof(p_permit text) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; o public.file_operations; dep text;
BEGIN
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=e.id;
 IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(o.file_attempt_index) x WHERE x->>'ordinal'=e.publication_attempt::text AND x->>'work_item_id'=e.publication_work_item_id AND x->>'token'=e.publication_fencing_token::text AND x->>'session_digest'='sha256:'||encode(sha256(convert_to(e.publication_executor_session,'UTF8')),'hex')) OR e.publication_effect_key IS DISTINCT FROM o.idempotency_key OR e.publication_frozen_objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path OR e.publication_frozen_objects->'source'->'size' IS DISTINCT FROM to_jsonb(o.expected_size_bytes) OR e.publication_frozen_objects->'source'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:'||(o.source_fingerprint->>'value') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 SELECT 'sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE(string_agg(parent.file_ref||':'||dep.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C"),''),'UTF8')),'hex') INTO dep FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id;
 IF dep IS DISTINCT FROM e.publication_dependency_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id AND dep.outcome IS DISTINCT FROM 'succeeded') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisition_files t JOIN public.acquisitions i ON i.id=t.acquisition_id WHERE t.acquisition_id=e.acquisition_id AND t.file_ref=e.file_ref AND t.canonical_path=o.target_path AND e.publication_frozen_objects->'target'->>'canonical_path'=o.target_path AND o.operation=t.operation AND o.intent=t.intent AND o.collision_policy=t.collision AND o.source_fingerprint->>'algorithm'='sha256' AND (e.collision_policy<>'overwrite' OR (i.input->'delivery'->>'intent'='replace' AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE r.value->'expected_object'->'location'=e.publication_frozen_objects->'expected_target'->'location' AND r.value->'expected_object'->'identity'=e.publication_frozen_objects->'expected_target'->'identity' AND r.value->'expected_object'->'size'=e.publication_frozen_objects->'expected_target'->'size' AND r.value->'expected_object'->'content'->'value'->>'sha256'=e.publication_frozen_objects->'expected_target'->'content'->'value'->>'sha256')) OR public.delivery_unmanaged_overwrite(i.input->'delivery',o.target_path,false))) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 PERFORM public.file_validate_final_proof(e);
END $$;

CREATE OR REPLACE FUNCTION file_prepare_permit(p_operation text, p_work text, p_token bigint, p_session text, p_permit text, p_objects text) RETURNS jsonb
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
 IF public.observed_closed(objects,CASE WHEN o.collision_policy='overwrite' THEN ARRAY['source','source_parent','target','target_parent','temp','source_capture','temp_capture','expected_target'] WHEN o.operation IN ('copy','move') OR o.collision_policy='version' THEN ARRAY['source','source_parent','target','target_parent','temp','source_capture','temp_capture'] ELSE ARRAY['source','source_parent','target','target_parent','temp'] END) IS NOT TRUE
  OR public.observed_closed(objects->'source',ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'identity',ARRAY['state','value','reason_code']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'content',ARRAY['state','value','reason_code']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'content'->'value',ARRAY['kind','sha256','size','object_identity','read_started_at','read_finished_at','verifier_version']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'location',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'target',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'temp',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR objects->'source'->'identity'->'reason_code' IS DISTINCT FROM 'null'::jsonb
  OR objects->'source'->'content'->'reason_code' IS DISTINCT FROM 'null'::jsonb
  OR public.observed_identity_valid(objects->'source_parent') IS NOT TRUE
  OR public.observed_identity_valid(objects->'target_parent') IS NOT TRUE
  OR objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path
  OR objects->'target'->>'canonical_path' IS DISTINCT FROM t.canonical_path
  OR t.canonical_path IS DISTINCT FROM o.target_path OR t.operation IS DISTINCT FROM o.operation
  OR t.intent IS DISTINCT FROM o.intent OR t.collision IS DISTINCT FROM o.collision_policy
  OR objects->'source'->>'file_type' IS DISTINCT FROM 'regular'
  OR (objects->'source'->>'size')::bigint IS DISTINCT FROM o.expected_size_bytes
  OR objects->'source'->'identity'->>'state' IS DISTINCT FROM 'known'
  OR public.observed_identity_valid(objects->'source'->'identity'->'value') IS NOT TRUE
  OR objects->'source'->'content'->>'state' IS DISTINCT FROM 'known'
  OR objects->'source'->'content'->'value'->>'kind' IS DISTINCT FROM 'sha256_file'
  OR o.source_fingerprint->>'algorithm' IS DISTINCT FROM 'sha256'
  OR objects->'source'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:'||(o.source_fingerprint->>'value')
  OR (objects->'source'->'content'->'value'->>'size')::bigint IS DISTINCT FROM o.expected_size_bytes
  OR objects->'source'->'content'->'value'->'object_identity' IS DISTINCT FROM objects->'source'->'identity'->'value'
  OR objects->'temp'->>'canonical_path' IS DISTINCT FROM regexp_replace(o.target_path,'[^/]+$','')||'.bansoko-permit-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')
  OR public.file_location_valid(objects->'source'->'location') IS NOT TRUE
  OR public.file_location_valid(objects->'target') IS NOT TRUE OR public.file_location_valid(objects->'temp') IS NOT TRUE
  OR objects->'source'->>'observed_at' IS DISTINCT FROM objects->'source'->'content'->'value'->>'read_finished_at'
  OR objects->'source'->>'observation_source' IS DISTINCT FROM objects->'source'->'content'->'value'->>'verifier_version'
  OR (objects->'source'->'content'->'value'->>'verifier_version' IN ('local-stable-read-v1','local-exact-path-read-v1')) IS NOT TRUE
  OR ((objects->'source'->'content'->'value'->>'read_started_at')::timestamptz <= (objects->'source'->'content'->'value'->>'read_finished_at')::timestamptz
   AND (objects->'source'->'content'->'value'->>'read_finished_at')::timestamptz<=now_at) IS NOT TRUE
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_OBJECT_BINDING'; END IF;
 -- Overwrite's admitted backup/exchange protocol is not represented by this
 -- initial prepare producer. Refuse it until its own complete proof path lands.
 IF (o.operation IN ('copy','move') OR o.collision_policy IN ('version','overwrite')) AND (
  public.file_location_valid(objects->'source_capture') IS NOT TRUE OR public.file_location_valid(objects->'temp_capture') IS NOT TRUE
  OR objects->'source_capture'->>'canonical_path' IS DISTINCT FROM rtrim(objects->'source'->'location'->>'root','/')||'/.bansoko-cleanup-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')||'-source/object'
  OR objects->'temp_capture'->>'canonical_path' IS DISTINCT FROM rtrim(objects->'target'->>'root','/')||'/.bansoko-cleanup-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')||'-temp/object'
 ) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CLEANUP_CAPTURE_BINDING'; END IF;
 IF o.collision_policy='overwrite' AND NOT EXISTS(SELECT 1 FROM public.acquisitions i WHERE i.id=o.acquisition_id
  AND ((i.input->'delivery'->>'intent'='replace'
  AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE
  r.value->'expected_object'->'location' IS NOT DISTINCT FROM objects->'expected_target'->'location'
  AND r.value->'expected_object'->'size' IS NOT DISTINCT FROM objects->'expected_target'->'size'
  AND r.value->'expected_object'->'identity' IS NOT DISTINCT FROM objects->'expected_target'->'identity'
  AND r.value->'expected_object'->'content'->'value'->'sha256' IS NOT DISTINCT FROM objects->'expected_target'->'content'->'value'->'sha256'))
  OR public.delivery_unmanaged_overwrite(i.input->'delivery',o.target_path,true))) THEN
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

-- Alignment follows the same collision setting:
-- under `overwrite` an alignment move replaces an unmanaged occupant through
-- the same atomic exchange. overwrite_recovery is the durable exchange record
-- the executor checkpoints before each stage, so a restart finishes or
-- restores an interrupted exchange instead of guessing.
ALTER TABLE alignment_operations
    ADD COLUMN overwrite_recovery JSONB,
    ADD CONSTRAINT alignment_operations_overwrite_recovery_check
        CHECK (overwrite_recovery IS NULL OR (jsonb_typeof(overwrite_recovery) = 'object' AND pg_column_size(overwrite_recovery) <= 65536));
