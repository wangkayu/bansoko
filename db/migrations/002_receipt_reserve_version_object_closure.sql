-- Forward-only migration 002: close the reserve_version object shape.
--
-- file_record_receipt accepted any extra key inside the first receipt's
-- proof.object for the reserve_version step. The generic object closure check
-- after the CASE only runs for stage_temp and publish_target, so injecting an
-- unknown key (e.g. probe_extra) into a reserve_version object was bound
-- field-by-field and then persisted. This migration re-creates the function
-- verbatim from 001_baseline.sql with one added check: the reserve_version
-- WHEN branch now requires proof.object to be closed over exactly the seven
-- keys the producer emits (location, file_type, size, identity, content,
-- observed_at, observation_source) and raises FILE_RECEIPT_OBJECT_INVALID
-- otherwise. Every other line is byte-identical to the baseline definition.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations.

CREATE OR REPLACE FUNCTION file_record_receipt(p_permit text, p_receipt text) RETURNS jsonb
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
     OR public.file_location_valid(proof->'object'->'location') IS NOT TRUE
     OR proof->'object'->'location'->>'root' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'root'
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->'object'->>'file_type' IS DISTINCT FROM 'regular' OR proof->'object'->'size' IS DISTINCT FROM '0'::jsonb
     OR proof->'object'->'identity'->>'state' IS DISTINCT FROM 'known'
     OR public.observed_identity_valid(proof->'object'->'identity'->'value') IS NOT TRUE
     OR proof->'object'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_RESERVATION_BINDING'; END IF;
    IF public.observed_closed(proof->'object',ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
   WHEN 'publish_target' THEN
    IF public.observed_closed(proof,ARRAY['kind','source_evidence_digest','target','parent','atomic_method','displaced','dependency_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'published' OR proof->>'atomic_method' IS DISTINCT FROM (CASE WHEN e.collision_policy IN ('version','overwrite') THEN 'exchange' ELSE 'no_replace' END)
     OR proof->'target'->'location' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN checkpoint->'reserve_version'->'proof'->'object'->'location' ELSE e.publication_frozen_objects->'target' END)
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->>'dependency_digest' IS DISTINCT FROM e.publication_dependency_digest
     OR e.publication_frozen_objects_canonical IS NULL OR proof->>'source_evidence_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
     OR (e.collision_policy NOT IN ('version','overwrite') AND proof->'displaced' IS DISTINCT FROM jsonb_build_object('state','absent','value',NULL,'reason_code',NULL))
     OR (e.collision_policy='overwrite' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
      OR proof->'displaced'->'value'->'identity'->'value'->>'device' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->>'device'
      OR proof->'displaced'->'value'->'identity'->'value'->>'inode' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->>'inode'
      OR proof->'displaced'->'value'->'identity'->'value'->'generation_nanos' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->'generation_nanos'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'size'
      OR proof->'displaced'->'value'->'content'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'content'->'value'->>'sha256' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'content'->'value'->>'sha256'))
     OR (e.collision_policy='version' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
      OR proof->'displaced'->'value'->'identity'->'value'->>'inode' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'identity'->'value'->>'inode'
      OR proof->'displaced'->'value'->'identity'->'value'->>'device' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'identity'->'value'->>'device'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM '0'::jsonb)) THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_PUBLICATION_BINDING'; END IF;
   WHEN 'source_cleanup','temp_cleanup' THEN
    IF public.file_final_object_valid(proof->'before') IS NOT TRUE
     OR public.file_location_valid(proof->'after'->'location') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'after'->'parent') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'parent') IS NOT TRUE
     OR public.file_wire_time_valid(proof->'after'->'observed_at') IS NOT TRUE THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
    IF public.observed_closed(proof,ARRAY['kind','before','parent','after','mutation_receipt_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'removed_object'
     OR public.observed_closed(proof->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE
     OR proof->'after'->>'method' IS DISTINCT FROM 'lstat_enoent'
     OR proof->'before' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source' WHEN e.collision_policy IN ('version','overwrite') THEN checkpoint->'publish_target'->'proof'->'displaced'->'value' ELSE checkpoint->'stage_temp'->'proof'->'object' END)
     OR proof->'parent' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source_parent' ELSE e.publication_frozen_objects->'target_parent' END)
     OR proof->'after'->'parent' IS DISTINCT FROM proof->'parent'
     OR proof->'after'->'location' IS DISTINCT FROM proof->'before'->'location'
     OR proof->>'mutation_receipt_digest' IS DISTINCT FROM public.file_proof_digest('file-mutation-receipt:v1',
      '{"before":'||public.file_json_field(proof_bytes::json,'before')||',"permit_id":'||to_json(e.publication_permit_id)::text||',"removed_at":'||to_json(checkpoint->step->>'observed_at')::text||',"step":'||to_json(step)::text||'}') THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLEANUP_BINDING'; END IF;
   END CASE;
   IF step IN ('stage_temp','publish_target') THEN
    DECLARE object jsonb:=CASE WHEN step='stage_temp' THEN proof->'object' ELSE proof->'target' END;
    BEGIN
     IF public.observed_closed(object,ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
      OR object->>'file_type' IS DISTINCT FROM 'regular'
      OR object->'size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size'
      OR object->'identity'->>'state' IS DISTINCT FROM 'known'
      OR public.observed_identity_valid(object->'identity'->'value') IS NOT TRUE
      OR object->'content'->>'state' IS DISTINCT FROM 'known'
      OR object->'content'->'value'->>'kind' IS DISTINCT FROM 'sha256_file'
      OR object->'content'->'value'->'object_identity' IS DISTINCT FROM object->'identity'->'value'
      OR object->'content'->'value'->'sha256' IS DISTINCT FROM e.publication_frozen_objects->'source'->'content'->'value'->'sha256' THEN
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
