-- Forward-only migration 041: retry the import stage from execution records.
--
-- The import (file execution) stage of a settled
-- acquisition is the only retryable stage. One retry re-runs every failed file
-- effect of the acquisition (succeeded files are untouched, nothing earlier is
-- redone, nothing is downloaded again) and re-opens every failed placement
-- handoff of its completed files. It applies to subscription and one-shot
-- acquisitions alike.
--
-- The retry is addressed by the acquisition and rebuilt from durable owner
-- state (acquisitions, acquisition_files, file_operations,
-- placement_obligations), never from a failed WorkItem: retention may have
-- pruned those. acquisition_import_retry is the only writer:
--   * each failed file operation is reset in place to pending with a fresh
--     attempt budget and an empty publication slot (the next attempt runs the
--     path + size verification of 039 again), its acquisition_files delivery
--     result is cleared, and a fresh file_execute WorkItem is created (a
--     subtitle whose video is retried too waits for the video's handoff);
--   * each failed placement obligation is re-opened with a fresh typed
--     continuation WorkItem;
--   * a failed root returns to running and gets a settlement item; a one-shot
--     marker returns to running with a new 'retry' execution attempt;
--   * acquisition_import_retries records the attempt (the record shows it) and
--     owns request idempotency.
-- The reset is the only transition that may empty a publication slot or
-- restart the attempt budget; the guards admit it only together with a
-- one-step increase of file_operations.import_retry_count, a column the
-- runtime role cannot write.

ALTER TABLE file_operations
    ADD COLUMN import_retry_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT file_operations_import_retry_count_check CHECK (import_retry_count BETWEEN 0 AND 1000);
COMMENT ON COLUMN file_operations.import_retry_count IS 'How many times an import retry reset this operation; written only by acquisition_import_retry (the guards admit the reset only with a one-step increase).';

CREATE TABLE acquisition_import_retries (
    acquisition_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    retried_files INTEGER NOT NULL,
    reopened_placements INTEGER NOT NULL,
    result_version BIGINT NOT NULL,
    previous_state TEXT NOT NULL,
    previous_terminal_at TIMESTAMPTZ NOT NULL,
    previous_terminal_receipt JSONB NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT acquisition_import_retries_pkey PRIMARY KEY (acquisition_id, ordinal),
    CONSTRAINT acquisition_import_retries_key_uq UNIQUE (acquisition_id, idempotency_key),
    CONSTRAINT acquisition_import_retries_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT acquisition_import_retries_ordinal_check CHECK (ordinal BETWEEN 1 AND 1000),
    CONSTRAINT acquisition_import_retries_key_check CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
    CONSTRAINT acquisition_import_retries_hash_check CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisition_import_retries_counts_check CHECK (retried_files >= 0 AND reopened_placements >= 0 AND retried_files + reopened_placements >= 1),
    CONSTRAINT acquisition_import_retries_version_check CHECK (result_version >= 1),
    CONSTRAINT acquisition_import_retries_previous_check CHECK (previous_state IN ('succeeded', 'failed') AND jsonb_typeof(previous_terminal_receipt) = 'object')
);
COMMENT ON TABLE acquisition_import_retries IS 'One row per accepted import-stage retry of an acquisition: the attempt the record shows, the settled outcome it reopened (kept, never rewritten) and the request replay identity.';
-- History of the root, like its receipts: a root is never deleted and
-- compaction keeps it, so no foreign key cascades. Rows are written only by
-- acquisition_import_retry (definer); the schema default privileges would
-- also grant INSERT, UPDATE and DELETE, which no runtime path uses.
REVOKE ALL ON TABLE acquisition_import_retries FROM bansoko_runtime;
GRANT SELECT ON TABLE acquisition_import_retries TO bansoko_runtime;

-- The attempt budget stays immutable except for the import-retry reset: the
-- one-step import_retry_count increase restarts the budget (no attempt, no
-- ticket, an empty index) with the same ready time, attempt count and attempt
-- timeout and a wall that only moves later.
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
  IF NEW.import_retry_count IS DISTINCT FROM OLD.import_retry_count THEN
   IF NEW.import_retry_count IS DISTINCT FROM OLD.import_retry_count+1 OR OLD.state='completed' OR NEW.state<>'pending'
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

-- The publication slot stays immutable except for the import-retry reset,
-- which empties it entirely (the next attempt prepares a new permit).
CREATE OR REPLACE FUNCTION public.guard_file_publication_identity() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.import_retry_count IS DISTINCT FROM OLD.import_retry_count THEN
  IF NEW.import_retry_count IS DISTINCT FROM OLD.import_retry_count+1 OR NEW.publication_permit_id IS NOT NULL
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

-- Proof bytes stay immutable within one slot; the import-retry reset (one
-- step of import_retry_count) is the only write that may drop them with it.
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
  AND NEW.import_retry_count IS NOT DISTINCT FROM OLD.import_retry_count THEN
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

-- Why the import stage of a root cannot be retried now, or NULL when it can.
-- The records read and the retry share this one rule.
--   IMPORT_RETRY_NOT_FOUND       no such root
--   IMPORT_RETRY_IN_FLIGHT       the root still runs or still has live work
--                                (an accepted retry is in flight)
--   MANAGED_ANIME_RETIRED        the owning anime is retired
--   IMPORT_RETRY_NOT_APPLICABLE  compact or cancelled root, nothing failed in
--                                the import stage, or a one-shot whose
--                                temporary graph is being reclaimed
CREATE FUNCTION public.acquisition_import_retry_refusal(p_root text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; v_owner text; lifecycle text; marker_state text;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root;
 IF NOT FOUND THEN RETURN 'IMPORT_RETRY_NOT_FOUND'; END IF;
 IF r.record_state<>'full' OR r.state='cancelled' THEN RETURN 'IMPORT_RETRY_NOT_APPLICABLE'; END IF;
 IF r.state='running' OR public.acquisition_live_work(p_root) THEN RETURN 'IMPORT_RETRY_IN_FLIGHT'; END IF;
 v_owner:=public.acquisition_owner(p_root);
 IF v_owner IS NOT NULL THEN
  SELECT l.lifecycle_state INTO lifecycle FROM public.managed_anime l WHERE l.id=v_owner;
  IF lifecycle IS DISTINCT FROM 'active' THEN RETURN 'MANAGED_ANIME_RETIRED'; END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisition_files f JOIN public.file_operations o
    ON o.id=f.plan_operation_ref AND o.acquisition_id=f.acquisition_id
   WHERE r.state='failed' AND f.acquisition_id=p_root AND f.plan_kind='execute' AND f.outcome='failed' AND o.state<>'completed')
  AND NOT EXISTS(SELECT 1 FROM public.file_operations o JOIN public.placement_obligations po ON po.file_operation_id=o.id
   WHERE o.acquisition_id=p_root AND o.state='completed' AND po.state='failed') THEN
  RETURN 'IMPORT_RETRY_NOT_APPLICABLE';
 END IF;
 IF r.entry_kind='one_shot' THEN
  SELECT m.state INTO marker_state FROM public.one_shot_acquisitions m WHERE m.request_idempotency_key=r.request_key;
  IF marker_state IS NULL OR marker_state NOT IN ('running','retention_pending') THEN RETURN 'IMPORT_RETRY_NOT_APPLICABLE'; END IF;
 END IF;
 RETURN NULL;
END $$;

-- Retry the import stage of one settled root. Lock order: the one-shot
-- marker, the owning anime lifecycle row, the root (marker -> owner -> child,
-- and lifecycle -> owner rows, as reclamation and retirement take them).
-- Answers {status: accepted|not_found|key_reused|version_conflict|refused,
-- ...}; only 'accepted' writes.
CREATE FUNCTION public.acquisition_import_retry(p_root text, p_expected_version bigint, p_key text, p_hash text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; prior public.acquisition_import_retries%ROWTYPE;
 marker public.one_shot_acquisitions%ROWTYPE; attempt public.one_shot_execution_attempts%ROWTYPE;
 v_owner text; refusal text; now_at timestamptz:=clock_timestamp(); one_shot jsonb:=NULL;
 o public.file_operations%ROWTYPE; retried text[]:=ARRAY[]::text[]; files integer:=0; placements integer:=0;
 snapshot jsonb; new_work text; next_ordinal integer; next_attempt text; new_version bigint;
BEGIN
 IF p_key IS NULL OR char_length(p_key) NOT BETWEEN 8 AND 128 OR p_hash IS NULL OR p_hash !~ '^sha256:[0-9a-f]{64}$' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_SHAPE'; END IF;
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root;
 IF NOT FOUND THEN RETURN jsonb_build_object('status','not_found'); END IF;
 IF r.entry_kind='one_shot' THEN
  SELECT * INTO marker FROM public.one_shot_acquisitions WHERE request_idempotency_key=r.request_key FOR UPDATE;
 END IF;
 v_owner:=public.acquisition_owner(p_root);
 IF v_owner IS NOT NULL THEN
  PERFORM 1 FROM public.managed_anime WHERE id=v_owner FOR UPDATE;
 END IF;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=p_root FOR UPDATE;

 SELECT * INTO prior FROM public.acquisition_import_retries WHERE acquisition_id=p_root AND idempotency_key=p_key;
 IF FOUND THEN
  IF prior.request_hash<>p_hash THEN RETURN jsonb_build_object('status','key_reused'); END IF;
  RETURN jsonb_build_object('status','accepted','replayed',true,'attempt',prior.ordinal+1,
   'retried_files',prior.retried_files,'reopened_placements',prior.reopened_placements,'version',prior.result_version);
 END IF;

 refusal:=public.acquisition_import_retry_refusal(p_root);
 IF refusal IS NOT NULL THEN RETURN jsonb_build_object('status','refused','code',refusal,'version',r.version); END IF;
 IF p_expected_version IS DISTINCT FROM r.version THEN
  RETURN jsonb_build_object('status','version_conflict','version',r.version); END IF;

 IF r.entry_kind='one_shot' THEN
  -- The marker's current attempt; a marker whose pointer predates the
  -- attempts backfill falls back to its newest attempt, and one with no
  -- attempt at all reopens without a new attempt row.
  SELECT * INTO attempt FROM public.one_shot_execution_attempts WHERE id=marker.current_attempt_id;
  IF NOT FOUND THEN
   SELECT * INTO attempt FROM public.one_shot_execution_attempts WHERE acquisition_id=marker.id
   ORDER BY sequence DESC LIMIT 1;
  END IF;
  IF attempt.id IS NOT NULL AND attempt.state='running' THEN
   -- The marker was not drained yet: the attempt closes with the root's
   -- settled outcome before the retry attempt opens.
   UPDATE public.one_shot_execution_attempts SET state=r.state,
    failed_stage=CASE WHEN r.state='failed' THEN 'file_execution' END,
    outcome_code=CASE WHEN r.state='failed' THEN 'one_shot_failed' ELSE 'one_shot_completed' END,
    outcome_reason=CASE WHEN r.state='failed' THEN 'one-shot acquisition failed in file_execution' ELSE 'one-shot acquisition completed' END,
    terminal_at=now_at,version=version+1,updated_at=now_at
   WHERE id=attempt.id;
  END IF;
  IF attempt.id IS NOT NULL THEN
   next_attempt:=gen_random_uuid()::text;
   INSERT INTO public.one_shot_execution_attempts(id,acquisition_id,sequence,previous_attempt_id,mode,resume_stage,
    input_snapshot,input_digest,state,request_idempotency_key,request_hash)
   SELECT next_attempt,marker.id,COALESCE(max(a.sequence),0)+1,attempt.id,'retry','file_execution',
    attempt.input_snapshot,attempt.input_digest,'running',p_key,p_hash
   FROM public.one_shot_execution_attempts a WHERE a.acquisition_id=marker.id;
  END IF;
  -- retention_pending -> running: the terminal evidence and the retention
  -- deadline clear together (the marker field matrix), so the retention pass
  -- neither reclaims it nor drains it while the retried work is live; it
  -- drains again with a fresh deadline once the root settles.
  UPDATE public.one_shot_acquisitions SET state='running',terminal_outcome=NULL,terminal_reason_code=NULL,
   terminal_reason=NULL,terminal_at=NULL,retention_until=NULL,current_attempt_id=COALESCE(next_attempt,current_attempt_id),
   version=version+1,updated_at=now_at
  WHERE id=marker.id;
  one_shot:=jsonb_build_object('marker_id',marker.id,'correlation_id',marker.correlation_id,'admission_epoch',marker.admission_epoch);
 END IF;

 -- Failed file effects: reset in place, clear the delivery result.
 IF r.state='failed' THEN
  FOR o IN SELECT op.* FROM public.file_operations op JOIN public.acquisition_files f
    ON f.plan_operation_ref=op.id AND f.acquisition_id=op.acquisition_id
   WHERE f.acquisition_id=p_root AND f.plan_kind='execute' AND f.outcome='failed' AND op.state<>'completed'
   ORDER BY op.id COLLATE "C" FOR UPDATE OF op LOOP
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
   WHERE id=o.id;
   UPDATE public.acquisition_files SET delivery_schema_version=NULL,outcome=NULL,completion_mode=NULL,
    effect_class=NULL,final_object=NULL,delivery_source_cleanup=NULL,delivery_reason_code=NULL,
    evidence_digest=NULL,operation_id=NULL,receipt=NULL,delivered_at=NULL,activity_logged_at=NULL
   WHERE acquisition_id=p_root AND file_ref=o.file_ref;
   retried:=array_append(retried,o.id);
   files:=files+1;
  END LOOP;
  -- A subtitle whose video is retried too gets its work from the video's
  -- success handoff; every other retried operation is queued now.
  FOR o IN SELECT op.* FROM public.file_operations op WHERE op.id=ANY(retried)
   AND (op.bound_video_operation_id IS NULL OR NOT (op.bound_video_operation_id=ANY(retried)))
   ORDER BY op.id COLLATE "C" LOOP
   snapshot:=jsonb_build_object('operation_id',o.id,'artifact_id',o.artifact_id,'artifact_file_id',o.artifact_file_id)
    ||CASE WHEN o.collision_policy='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END
    ||CASE WHEN one_shot IS NOT NULL THEN jsonb_build_object('one_shot',one_shot) ELSE '{}'::jsonb END;
   SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
    o.file_max_attempts,snapshot,o.version,0::double precision,7::smallint) INTO new_work;
   IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_WORK_CONFLICT'; END IF;
  END LOOP;
 END IF;

 -- Failed placement handoffs of completed files: re-open with a fresh typed
 -- continuation (the completed file effect is never re-run).
 FOR o IN SELECT op.* FROM public.file_operations op JOIN public.placement_obligations po ON po.file_operation_id=op.id
  WHERE op.acquisition_id=p_root AND op.state='completed' AND po.state='failed'
  ORDER BY op.id COLLATE "C" FOR UPDATE OF po LOOP
  UPDATE public.placement_obligations SET state='pending',attempts=0,reason_code=NULL,reason=NULL,settled_at=NULL,updated_at=now_at
  WHERE file_operation_id=o.id AND state='failed';
  snapshot:=jsonb_build_object('schema_version','file-execute-continuation/v1','continuation','placement',
   'placement_retry',true,'file_operation_id',o.id)
   ||CASE WHEN one_shot IS NOT NULL THEN jsonb_build_object('one_shot',one_shot) ELSE '{}'::jsonb END;
  SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',o.id,'file_execute',o.idempotency_key,
   3,snapshot,o.version,0::double precision,7::smallint) INTO new_work;
  IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_WORK_CONFLICT'; END IF;
  placements:=placements+1;
 END LOOP;
 IF files+placements=0 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMPORT_RETRY_NOTHING_TO_RETRY'; END IF;

 IF files>0 THEN
  UPDATE public.acquisitions SET state='running',terminal_at=NULL,terminal_schema=NULL,terminal_receipt=NULL,
   detail_until=NULL,completion_open=false,activity_logged_at=NULL,settlement_blocked_code=NULL,
   settlement_error_count=0,version=version+1
  WHERE id=p_root RETURNING version INTO new_version;
  PERFORM public.download_ensure_settlement(p_root);
 ELSE
  UPDATE public.acquisitions SET version=version+1 WHERE id=p_root RETURNING version INTO new_version;
 END IF;

 SELECT COALESCE(max(x.ordinal),0)+1 INTO next_ordinal FROM public.acquisition_import_retries x WHERE x.acquisition_id=p_root;
 INSERT INTO public.acquisition_import_retries(acquisition_id,ordinal,idempotency_key,request_hash,retried_files,
  reopened_placements,result_version,previous_state,previous_terminal_at,previous_terminal_receipt,accepted_at)
 VALUES(p_root,next_ordinal,p_key,p_hash,files,placements,new_version,r.state,r.terminal_at,r.terminal_receipt,now_at);
 RETURN jsonb_build_object('status','accepted','replayed',false,'attempt',next_ordinal+1,
  'retried_files',files,'reopened_placements',placements,'version',new_version);
END $$;

REVOKE ALL ON FUNCTION public.acquisition_import_retry_refusal(text), public.acquisition_import_retry(text, bigint, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.acquisition_import_retry_refusal(text), public.acquisition_import_retry(text, bigint, text, text) TO bansoko_runtime;
