-- 082: overwrite publication busy retry.
--
-- An overwrite import whose target is busy (held open over SMB/CIFS: the
-- replacing rename is refused, or an exclusive holder refuses even the
-- observation of the target) leaves the target untouched. file_busy_requeue
-- resets that operation's publication evidence and attempt budget, closes the
-- current WorkItem and creates the next one, due after p_delay_seconds. Two
-- shapes are admitted:
--   * publish: the executor staged the temp, the rename (or the observation
--     just before it) was refused and the executor removed its own temp in
--     the same publish_target step, recording the step unknown with reason
--     TARGET_BUSY;
--   * prepare: the target observation of the preparation was refused, before
--     any permit, so nothing exists to clean.
--
-- The reset is the import-retry reset (state pending, every publication
-- column cleared) counted by its own column, busy_requeue_count, so a busy
-- chain never spends import_retry_count (CHECK 0..1000) and a manual import
-- retry stays available. The evidence guards admit a reset by exactly one
-- one-step increase of either counter (file_evidence_reset_step).
--
-- The window is carried by the WorkItem input snapshot: busy_since is the
-- first refusal of the chain and busy_wall_seconds the operation's wall
-- duration before it; every successor copies both. Each hop moves the wall to
-- at least now + delay + that duration, so it never compounds. Once p_window
-- has passed since busy_since the command refuses with
-- FILE_BUSY_WINDOW_EXHAUSTED and the caller finishes the operation failed
-- (TARGET_BUSY). An import retry creates a fresh snapshot without either key,
-- so a later manual retry starts a new window.

ALTER TABLE file_operations
    ADD COLUMN busy_requeue_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT file_operations_busy_requeue_count_check CHECK (busy_requeue_count >= 0);
COMMENT ON COLUMN file_operations.busy_requeue_count IS 'How many times a busy target requeued this operation; written only by file_busy_requeue (the guards admit the reset only with a one-step increase).';

-- NULL when neither reset counter moved; TRUE for exactly one one-step
-- increase of one counter with the other unchanged; FALSE otherwise.
CREATE OR REPLACE FUNCTION public.file_evidence_reset_step(p_old_import integer, p_new_import integer,
 p_old_busy integer, p_new_busy integer) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE
  WHEN p_new_import IS NOT DISTINCT FROM p_old_import AND p_new_busy IS NOT DISTINCT FROM p_old_busy THEN NULL
  WHEN (p_new_import=p_old_import+1 AND p_new_busy=p_old_busy) OR (p_new_busy=p_old_busy+1 AND p_new_import=p_old_import) THEN true
  ELSE false END
$$;

-- The 041 guards, with the reset admitted for either counter.
CREATE OR REPLACE FUNCTION public.guard_file_attempt_budget() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry jsonb; n integer:=0; prior jsonb; active jsonb; identities text[]:=ARRAY[]::text[]; identity text;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.budget_clock_at IS NULL THEN NEW.budget_clock_at:=NEW.file_ready_at; END IF;
  IF NEW.file_attempts_started<>0 OR NEW.file_attempt_ticket IS NOT NULL OR NEW.file_attempt_index<>'[]'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INITIAL_SHAPE'; END IF;
 ELSE
  IF public.file_evidence_reset_step(OLD.import_retry_count,NEW.import_retry_count,OLD.busy_requeue_count,NEW.busy_requeue_count) IS NOT NULL THEN
   IF public.file_evidence_reset_step(OLD.import_retry_count,NEW.import_retry_count,OLD.busy_requeue_count,NEW.busy_requeue_count) IS NOT TRUE OR OLD.state='completed' OR NEW.state<>'pending'
    OR ROW(NEW.file_ready_at,NEW.file_max_attempts,NEW.file_attempt_timeout_seconds) IS DISTINCT FROM
       ROW(OLD.file_ready_at,OLD.file_max_attempts,OLD.file_attempt_timeout_seconds)
    OR NEW.file_wall_until<OLD.file_wall_until OR NEW.budget_clock_at<OLD.budget_clock_at
    OR NEW.file_attempts_started<>0 OR NEW.file_attempt_ticket IS NOT NULL OR NEW.file_attempt_index<>'[]'::jsonb THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_IMPORT_RETRY_RESET_SHAPE'; END IF;
   RETURN NEW;
  END IF;
  IF ROW(NEW.file_ready_at,NEW.file_wall_until,NEW.file_max_attempts,NEW.file_attempt_timeout_seconds) IS DISTINCT FROM
     ROW(OLD.file_ready_at,OLD.file_wall_until,OLD.file_max_attempts,OLD.file_attempt_timeout_seconds)
   OR NEW.budget_clock_at<OLD.budget_clock_at OR NEW.file_attempts_started<OLD.file_attempts_started
   OR NEW.file_attempts_started>OLD.file_attempts_started+1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_BUDGET_IMMUTABLE'; END IF;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(NEW.file_attempt_index) LOOP
  n:=n+1;
  IF jsonb_typeof(entry)<>'object' OR (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(entry) key)
   IS DISTINCT FROM ARRAY['ordinal','session_digest','state','token','work_item_id']::text[]
   OR ((entry->>'ordinal')::integer=n AND (entry->>'token')::bigint>=1
    AND entry->>'session_digest' ~ '^sha256:[0-9a-f]{64}$' AND char_length(entry->>'work_item_id') BETWEEN 1 AND 128) IS NOT TRUE
   OR (entry->>'state' IN ('issued','completed','superseded','timed_out')) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INDEX_SHAPE'; END IF;
  identity:=(entry->>'work_item_id')||':'||(entry->>'token');
  IF identity=ANY(identities) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INDEX_DUPLICATE'; END IF;
  identities:=array_append(identities,identity);
  IF entry->>'state'='issued' THEN
   IF active IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_MULTIPLE_ACTIVE'; END IF;
   active:=entry;
  END IF;
  IF TG_OP='UPDATE' AND n<=OLD.file_attempts_started THEN
   prior:=OLD.file_attempt_index->(n-1);
   IF (entry-'state') IS DISTINCT FROM (prior-'state') OR
     (prior->>'state'<>'issued' AND entry IS DISTINCT FROM prior) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_IDENTITY_IMMUTABLE'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='UPDATE' AND OLD.file_attempt_ticket IS NOT NULL AND NEW.file_attempts_started=OLD.file_attempts_started
  AND (NEW.file_attempt_ticket-'state') IS DISTINCT FROM (OLD.file_attempt_ticket-'state') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_IMMUTABLE'; END IF;
 IF NEW.file_attempt_ticket IS NOT NULL THEN
  IF (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(NEW.file_attempt_ticket) key)
   IS DISTINCT FROM ARRAY['attempt_until','executor_session','ordinal','started_at','state','token','work_item_id']::text[]
   OR ((NEW.file_attempt_ticket-'started_at'-'attempt_until'-'executor_session')||jsonb_build_object(
    'session_digest','sha256:'||encode(sha256(convert_to(NEW.file_attempt_ticket->>'executor_session','UTF8')),'hex'))) IS DISTINCT FROM NEW.file_attempt_index->(NEW.file_attempts_started-1)
   OR ((NEW.file_attempt_ticket->>'started_at')::timestamptz>=NEW.file_ready_at AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz>(NEW.file_attempt_ticket->>'started_at')::timestamptz AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz<=NEW.file_wall_until AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz<=(NEW.file_attempt_ticket->>'started_at')::timestamptz+make_interval(secs=>NEW.file_attempt_timeout_seconds)) IS NOT TRUE
   THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_SHAPE'; END IF;
 ELSIF NEW.file_attempts_started<>0 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_MISSING';
 END IF;
 RETURN NEW;
END $_$;
CREATE OR REPLACE FUNCTION public.guard_file_publication_identity() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF TG_OP='UPDATE' AND public.file_evidence_reset_step(OLD.import_retry_count,NEW.import_retry_count,OLD.busy_requeue_count,NEW.busy_requeue_count) IS NOT NULL THEN
  IF public.file_evidence_reset_step(OLD.import_retry_count,NEW.import_retry_count,OLD.busy_requeue_count,NEW.busy_requeue_count) IS NOT TRUE OR NEW.publication_permit_id IS NOT NULL
   OR NEW.publication_dispatch_state IS NOT NULL OR NEW.publication_closed_proof IS NOT NULL
   OR NEW.publication_resolution_proof IS NOT NULL OR NEW.publication_conflict_evidence IS NOT NULL THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_IMPORT_RETRY_RESET_SHAPE'; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='UPDATE' AND OLD.publication_permit_id IS NOT NULL THEN
  IF NEW.publication_permit_id IS DISTINCT FROM OLD.publication_permit_id
   OR NEW.publication_effect_key IS DISTINCT FROM OLD.publication_effect_key
   OR NEW.publication_executor_session IS DISTINCT FROM OLD.publication_executor_session THEN
   -- S6: the column form of the old partial unique index
   -- `file_publication_unresolved_uq (acquisition_id, effect_key) WHERE NOT
   -- retry_safe`. Only a slot whose "unresolved" predicate is FALSE may be
   -- replaced, and the replacement starts a fresh attempt.
   IF OLD.publication_retry_safe IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_UNRESOLVED'; END IF;
   IF NEW.publication_attempt<=OLD.publication_attempt THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_ATTEMPT_STEP'; END IF;
   IF NEW.publication_dispatch_state IS DISTINCT FROM 'prepared'
    OR NEW.publication_effect_class IS DISTINCT FROM 'unknown'
    OR NEW.publication_dispatch_started_at IS NOT NULL THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_REPLACEMENT_SHAPE'; END IF;
   RETURN NEW;
  END IF;
  IF ROW(NEW.publication_permit_id,NEW.acquisition_id,NEW.file_ref,NEW.publication_work_item_id,
   NEW.publication_fencing_token,NEW.publication_owner_version,NEW.publication_attempt,NEW.publication_effect_key,NEW.publication_spec_digest,NEW.publication_plan_digest,NEW.publication_dependency_digest,
   NEW.operation,NEW.intent,NEW.collision_policy,NEW.publication_frozen_objects,NEW.publication_executor_session,
   NEW.publication_prepared_at,NEW.publication_reconcile_wall_seconds,NEW.publication_max_lookups)
  IS DISTINCT FROM ROW(OLD.publication_permit_id,OLD.acquisition_id,OLD.file_ref,OLD.publication_work_item_id,
   OLD.publication_fencing_token,OLD.publication_owner_version,OLD.publication_attempt,OLD.publication_effect_key,OLD.publication_spec_digest,OLD.publication_plan_digest,OLD.publication_dependency_digest,
   OLD.operation,OLD.intent,OLD.collision_policy,OLD.publication_frozen_objects,OLD.publication_executor_session,
   OLD.publication_prepared_at,OLD.publication_reconcile_wall_seconds,OLD.publication_max_lookups)
  THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_IDENTITY_IMMUTABLE'; END IF;
  IF NEW.publication_evidence_version<OLD.publication_evidence_version OR NEW.publication_lookup_count<OLD.publication_lookup_count OR
   (OLD.publication_dispatch_started_at IS NOT NULL AND NEW.publication_dispatch_started_at IS DISTINCT FROM OLD.publication_dispatch_started_at) OR
   (OLD.publication_unknown_since IS NOT NULL AND ROW(NEW.publication_unknown_since,NEW.publication_reconcile_until) IS DISTINCT FROM ROW(OLD.publication_unknown_since,OLD.publication_reconcile_until)) OR
   (OLD.publication_closed_proof IS NOT NULL AND NEW.publication_closed_proof IS DISTINCT FROM OLD.publication_closed_proof) OR
   (OLD.publication_resolution_proof IS NOT NULL AND NEW.publication_resolution_proof IS DISTINCT FROM OLD.publication_resolution_proof) OR
   (OLD.publication_dispatch_state='closed' AND NEW.publication_dispatch_state<>'closed') OR
   (OLD.publication_dispatch_state='dispatching' AND NEW.publication_dispatch_state='prepared')
  THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_FACT_IMMUTABLE'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION public.file_evidence_bytes_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE key text; old_bytes jsonb;
BEGIN
 IF NEW.publication_frozen_objects_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_frozen_objects_canonical) IS NOT TRUE OR
  NEW.publication_frozen_objects_canonical::jsonb IS DISTINCT FROM NEW.publication_frozen_objects) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_OBJECT_BYTES_INVALID'; END IF;
 IF NEW.publication_closure_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_closure_canonical) IS NOT TRUE OR
  NEW.publication_closure_canonical::jsonb IS DISTINCT FROM NEW.publication_closed_proof) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CLOSURE_BYTES_INVALID'; END IF;
 IF NEW.publication_resolution_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_resolution_canonical) IS NOT TRUE OR
  NEW.publication_resolution_canonical::jsonb IS DISTINCT FROM NEW.publication_resolution_proof) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESOLUTION_BYTES_INVALID'; END IF;
 IF NEW.publication_resolution_checkpoint_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_resolution_checkpoint_canonical) IS NOT TRUE OR
  NEW.publication_resolution_checkpoint_canonical::jsonb IS DISTINCT FROM NEW.publication_step_checkpoint) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESOLUTION_CHECKPOINT_INVALID'; END IF;
 FOR key IN SELECT jsonb_object_keys(NEW.publication_step_proof_bytes) LOOP
  IF key NOT IN ('stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup') OR
   jsonb_typeof(NEW.publication_step_proof_bytes->key) IS DISTINCT FROM 'string' OR
   public.plan_canonical_valid(NEW.publication_step_proof_bytes->>key) IS NOT TRUE OR
   (NEW.publication_step_proof_bytes->>key)::jsonb IS DISTINCT FROM NEW.publication_step_checkpoint->key->'proof' THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_BYTES_INVALID'; END IF;
 END LOOP;
 IF (SELECT count(*) FROM jsonb_object_keys(NEW.publication_conflict_seen))>100 OR EXISTS(SELECT 1 FROM jsonb_each(NEW.publication_conflict_seen) s WHERE s.key !~ '^sha256:[0-9a-f]{64}$' OR s.value IS DISTINCT FROM 'true'::jsonb) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_INDEX_INVALID'; END IF;
 IF TG_OP='UPDATE' AND OLD.publication_permit_id IS NOT DISTINCT FROM NEW.publication_permit_id
  AND public.file_evidence_reset_step(OLD.import_retry_count,NEW.import_retry_count,OLD.busy_requeue_count,NEW.busy_requeue_count) IS NULL THEN
  IF (OLD.publication_conflict_index_exhausted AND NOT NEW.publication_conflict_index_exhausted) OR NOT (NEW.publication_conflict_seen @> OLD.publication_conflict_seen) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_INDEX_IMMUTABLE'; END IF;
  IF (OLD.publication_frozen_objects_canonical IS NOT NULL AND NEW.publication_frozen_objects_canonical IS DISTINCT FROM OLD.publication_frozen_objects_canonical)
   OR (OLD.publication_closure_canonical IS NOT NULL AND NEW.publication_closure_canonical IS DISTINCT FROM OLD.publication_closure_canonical)
   OR (OLD.publication_resolution_canonical IS NOT NULL AND NEW.publication_resolution_canonical IS DISTINCT FROM OLD.publication_resolution_canonical)
   OR (OLD.publication_resolution_checkpoint_canonical IS NOT NULL AND NEW.publication_resolution_checkpoint_canonical IS DISTINCT FROM OLD.publication_resolution_checkpoint_canonical)
   OR NEW.publication_conflict_count<OLD.publication_conflict_count THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PROOF_BYTES_IMMUTABLE'; END IF;
  FOR key IN SELECT jsonb_object_keys(OLD.publication_step_proof_bytes) LOOP
   IF NEW.publication_step_proof_bytes->key IS DISTINCT FROM OLD.publication_step_proof_bytes->key THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_BYTES_IMMUTABLE'; END IF;
  END LOOP;
 END IF;
 RETURN NEW;
END $_$;

CREATE OR REPLACE FUNCTION public.file_busy_requeue(p_operation text, p_work text, p_token bigint,
 p_reason text, p_delay_seconds double precision, p_window interval) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; checkpoint jsonb; since timestamptz;
 now_at timestamptz:=clock_timestamp(); next_version bigint; snapshot jsonb; new_work text; wall_seconds double precision;
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
 -- Only the two busy overwrite shapes, with no file outcome recorded yet.
 checkpoint:=o.publication_step_checkpoint;
 IF (o.collision_policy='overwrite'
  AND NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref AND r.outcome IS NOT NULL)
  AND (
   -- prepare: refused before any permit.
   (o.publication_permit_id IS NULL AND o.publication_dispatch_state IS NULL AND checkpoint IS NULL)
   OR
   -- publish: the permit of this claim is closed, the temp was staged, the
   -- publish step was refused busy and nothing ran after it.
   (o.publication_permit_id='pub-'||o.id
    AND o.publication_work_item_id=p_work AND o.publication_fencing_token=p_token
    AND o.publication_dispatch_state='closed' AND o.publication_conflict_evidence IS NULL
    AND checkpoint->'stage_temp'->>'state'='confirmed'
    AND checkpoint->'reserve_version'->>'state'='not_required'
    AND checkpoint->'publish_target'->>'state'='unknown'
    AND checkpoint->'publish_target'->>'reason_code'='TARGET_BUSY'
    AND checkpoint->'source_cleanup'->>'state' IN ('not_started','not_required')
    AND checkpoint->'temp_cleanup'->>'state'='not_started'))) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_REQUEUE_EVIDENCE'; END IF;
 since:=now_at;
 IF jsonb_typeof(w.input_snapshot->'busy_since')='string' THEN
  since:=LEAST((w.input_snapshot->>'busy_since')::timestamptz,now_at); END IF;
 IF now_at-since>=p_window THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_WINDOW_EXHAUSTED'; END IF;
 wall_seconds:=EXTRACT(EPOCH FROM o.file_wall_until-o.file_ready_at);
 IF jsonb_typeof(w.input_snapshot->'busy_wall_seconds')='number' THEN
  wall_seconds:=(w.input_snapshot->>'busy_wall_seconds')::double precision; END IF;
 UPDATE public.file_operations SET state='pending',failure_code=NULL,failure_reason=NULL,observed_at=NULL,
  target_fingerprint=NULL,observed_target_size_bytes=NULL,publication_checkpoint=NULL,
  busy_requeue_count=busy_requeue_count+1,version=version+1,updated_at=now_at,
  file_wall_until=GREATEST(file_wall_until,now_at+make_interval(secs=>p_delay_seconds+wall_seconds)),
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
 snapshot:=w.input_snapshot||jsonb_build_object('busy_since',to_char(since AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'busy_wall_seconds',wall_seconds);
 SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
  o.file_max_attempts,snapshot,next_version,p_delay_seconds,7::smallint) INTO new_work;
 IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUSY_REQUEUE_WORK_CONFLICT'; END IF;
 RETURN jsonb_build_object('state','pending','owner_version',next_version,'work_item_id',new_work,'busy_since',snapshot->'busy_since');
END $$;

REVOKE ALL ON FUNCTION public.file_busy_requeue(text, text, bigint, text, double precision, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.file_busy_requeue(text, text, bigint, text, double precision, interval) TO bansoko_runtime;
