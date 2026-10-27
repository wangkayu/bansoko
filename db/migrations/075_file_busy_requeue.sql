-- 075: overwrite publication busy retry.
--
-- An overwrite import whose rename onto the existing target was refused
-- because the target is busy (held open over SMB/CIFS) or the change is not
-- permitted leaves the target untouched; the executor removed its own staged
-- temp in the same publish_target step and recorded the step unknown with
-- reason TARGET_BUSY. file_busy_requeue resets that operation's publication
-- evidence and attempt budget exactly like the import-retry reset (the one
-- reset the evidence guards admit: a one-step import_retry_count increase,
-- state pending, every publication column cleared), closes the current
-- WorkItem and creates the next one, due after p_delay_seconds.
--
-- The window is carried by the WorkItem input snapshot: busy_since is the
-- first refusal of the chain and every successor copies it. Once p_window
-- has passed since busy_since the command refuses with
-- FILE_BUSY_WINDOW_EXHAUSTED and the caller finishes the operation failed
-- (TARGET_BUSY). An import retry creates a fresh snapshot without busy_since,
-- so a later manual retry starts a new window.

CREATE OR REPLACE FUNCTION public.file_busy_requeue(p_operation text, p_work text, p_token bigint,
 p_reason text, p_delay_seconds double precision, p_window interval) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; checkpoint jsonb; since timestamptz;
 now_at timestamptz:=clock_timestamp(); next_version bigint; snapshot jsonb; new_work text;
BEGIN
 IF p_reason IS NULL OR char_length(p_reason)>1024 OR p_delay_seconds IS NULL OR p_delay_seconds<=0
  OR p_window IS NULL OR p_window<=interval '0' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_REQUEUE_SHAPE'; END IF;
 o:=public.file_finalization_prelock(p_operation,p_work);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 -- The same staleness gate as file_finish_failed.
 IF (o.state IN ('pending','running') AND o.postprocessing_plan_id IS NOT NULL
  AND w.owner_type='file_operation' AND w.owner_id=o.id AND w.kind='file_execute'
  AND w.acquisition_id=o.acquisition_id AND w.state='leased' AND w.fencing_token=p_token
  AND w.expected_owner_version=o.version AND w.lease_until>now_at) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_STALE'; END IF;
 -- Only the busy overwrite shape: the permit of this claim is closed, the
 -- temp was staged, the rename was refused busy and nothing ran after it.
 checkpoint:=o.publication_step_checkpoint;
 IF (o.collision_policy='overwrite' AND o.publication_permit_id='pub-'||o.id
  AND o.publication_work_item_id=p_work AND o.publication_fencing_token=p_token
  AND o.publication_dispatch_state='closed' AND o.publication_conflict_evidence IS NULL
  AND checkpoint->'stage_temp'->>'state'='confirmed'
  AND checkpoint->'reserve_version'->>'state'='not_required'
  AND checkpoint->'publish_target'->>'state'='unknown'
  AND checkpoint->'publish_target'->>'reason_code'='TARGET_BUSY'
  AND checkpoint->'source_cleanup'->>'state' IN ('not_started','not_required')
  AND checkpoint->'temp_cleanup'->>'state'='not_started'
  AND NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref AND r.outcome IS NOT NULL)) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_REQUEUE_EVIDENCE'; END IF;
 since:=now_at;
 IF jsonb_typeof(w.input_snapshot->'busy_since')='string' THEN
  since:=LEAST((w.input_snapshot->>'busy_since')::timestamptz,now_at); END IF;
 IF now_at-since>=p_window THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_WINDOW_EXHAUSTED'; END IF;
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
 WHERE id=o.id RETURNING version INTO next_version;
 IF public.work_item_fail_at_attempt_budget(next_version,'TARGET_BUSY',p_reason,p_work,o.version)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 snapshot:=w.input_snapshot||jsonb_build_object('busy_since',to_char(since AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'));
 SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
  o.file_max_attempts,snapshot,next_version,p_delay_seconds,7::smallint) INTO new_work;
 IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_REQUEUE_WORK_CONFLICT'; END IF;
 RETURN jsonb_build_object('state','pending','owner_version',next_version,'work_item_id',new_work,'busy_since',snapshot->'busy_since');
END $$;

REVOKE ALL ON FUNCTION public.file_busy_requeue(text, text, bigint, text, double precision, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.file_busy_requeue(text, text, bigint, text, double precision, interval) TO bansoko_runtime;
