-- Forward-only migration 104: a one-shot chain blocked while its work was
-- still live returns to running.
--
-- The drain read a chain as terminal as soon as the download task had settled
-- and every artifact and file operation was settled. Between "artifact
-- verified, plan queued" and "plan committed with file operations" that holds
-- while the acquisition root is still running, so a drain in that window found
-- the plan's admission lease and moved the marker to cleanup_blocked
-- (admission_ledger_not_drained). From then on the one-shot gate refused every
-- claim of the chain: each refusal still used a work-item attempt, the plan
-- item ran out of attempts, and its recovery observation was refused at the
-- same gate on every pass.
--
-- The drain now treats a chain with a running root as not terminal (a running
-- root always holds work, migration 091) and never blocks a marker while chain
-- work is live. The functions here repair a marker the old drain blocked:
--
--   * one_shot_chain_open: the marker has a download chain and its root is
--     running or one of the chain's work items is live. It is the predicate
--     the drain keeps a marker running on and the one a blocked marker is
--     resumed on, so the two cannot disagree.
--   * one_shot_resume_blocked_chain: under the marker lock, a cleanup_blocked
--     marker whose chain is open and whose current execution attempt is still
--     running returns to running with its terminal fields cleared and its
--     admission ledger kept. Each queued plan of the marker gets its wall moved
--     by the time the marker was blocked, and each plan work item gets back
--     the attempts that started no plan attempt (claims the gate refused).
--     plan_attempt_index, not historical admission, is the business-attempt
--     evidence. An attempt_exhausted quarantine is cleared only when no plan
--     attempt ticket is still issued; its fencing token advances so an
--     in-flight recovery observer cannot write through the resumed claim.
--     effect_unknown, cancellation and an issued plan attempt keep their
--     quarantine. With a refused claim (p_work, p_token) that
--     claim also returns to available at once without using an attempt. A
--     marker that cannot resume is left unchanged and the reply names the
--     reason.
--   * guard_plan_attempt_budget admits that wall move: a queued plan of a
--     cleanup_blocked marker may move its wall later by at most the time since
--     the marker was blocked.
--
-- A refused claim never uses an attempt while its marker can resume: the
-- worker resumes the marker and returns the claim in one transaction. A
-- refusal of a marker that cannot resume (reclaim_pending, reclaiming, a stale
-- epoch, a settled chain) keeps the old non-mutating abort.

CREATE FUNCTION one_shot_chain_open(p_marker text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH m AS (
  SELECT id, managed_anime_id, release_id, download_task_id FROM public.one_shot_acquisitions WHERE id=p_marker
 ), roots AS (
  SELECT r.id, r.state FROM public.acquisitions r, m
  WHERE r.origin_managed_anime_ref=m.managed_anime_id
     OR r.id=(SELECT d.acquisition_id FROM public.download_tasks d WHERE d.id=m.download_task_id)
 )
 SELECT COALESCE((SELECT m.download_task_id IS NOT NULL FROM m),false)
  AND (EXISTS(SELECT 1 FROM roots WHERE roots.state='running')
   OR EXISTS(SELECT 1 FROM public.work_items w WHERE w.acquisition_id IN (SELECT roots.id FROM roots))
   OR EXISTS(SELECT 1 FROM public.work_items w, m WHERE w.owner_type='release' AND w.owner_id=m.release_id));
$$;

CREATE FUNCTION one_shot_resume_blocked_chain(p_marker text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE m public.one_shot_acquisitions%ROWTYPE; attempt_state text; w public.work_items%ROWTYPE;
 p public.postprocessing_plans%ROWTYPE; held interval; started integer; target integer;
 plans integer:=0; refunded integer:=0; revived integer:=0; current_started boolean;
BEGIN
 SELECT * INTO m FROM public.one_shot_acquisitions WHERE id=p_marker FOR UPDATE;
 IF NOT FOUND THEN RETURN jsonb_build_object('resumed',false,'reason','marker_missing'); END IF;
 IF m.state<>'cleanup_blocked' OR m.download_task_id IS NULL OR m.terminal_at IS NULL THEN
  RETURN jsonb_build_object('resumed',false,'reason','not_blocked'); END IF;
 -- A marker drained to reclaim_pending closed its attempt; only a marker the
 -- drain blocked straight from running still has a running attempt. A marker
 -- whose pointer predates the attempts backfill falls back to its newest one.
 SELECT a.state INTO attempt_state FROM public.one_shot_execution_attempts a WHERE a.id=m.current_attempt_id;
 IF NOT FOUND THEN
  SELECT a.state INTO attempt_state FROM public.one_shot_execution_attempts a WHERE a.acquisition_id=m.id
  ORDER BY a.sequence DESC LIMIT 1;
 END IF;
 IF attempt_state IS NOT NULL AND attempt_state<>'running' THEN
  RETURN jsonb_build_object('resumed',false,'reason','attempt_terminal'); END IF;
 IF public.one_shot_chain_open(m.id) IS NOT TRUE THEN
  RETURN jsonb_build_object('resumed',false,'reason','chain_settled'); END IF;
 -- Marker, then plans, then their work items and the refused claim in id order.
 PERFORM 1 FROM public.postprocessing_plans WHERE one_shot_id=m.id AND state='queued' ORDER BY id COLLATE "C" FOR UPDATE;
 PERFORM 1 FROM public.work_items
 WHERE id=p_work OR (owner_type='postprocessing_plan' AND kind='postprocess_plan'
  AND owner_id IN (SELECT id FROM public.postprocessing_plans WHERE one_shot_id=m.id AND state='queued'))
 ORDER BY id COLLATE "C" FOR UPDATE;
 IF p_work IS NOT NULL THEN
  SELECT * INTO w FROM public.work_items WHERE id=p_work;
  IF NOT FOUND OR w.state<>'leased' OR w.fencing_token IS DISTINCT FROM p_token
  OR (w.lease_until>clock_timestamp()) IS NOT TRUE OR w.recovery_blocked OR w.cancel_requested
  OR w.attempt<1
  OR w.input_snapshot->'one_shot'->>'marker_id' IS DISTINCT FROM m.id
  OR w.input_snapshot->'one_shot'->>'admission_epoch' IS DISTINCT FROM m.admission_epoch::text
  OR (w.owner_type='postprocessing_plan' AND EXISTS(SELECT 1 FROM public.postprocessing_plans owner_plan
   CROSS JOIN LATERAL jsonb_array_elements(owner_plan.plan_attempt_index) e
   WHERE owner_plan.id=w.owner_id AND e->>'work_item_id'=w.id AND (e->>'token')::bigint=w.fencing_token)) THEN
   RETURN jsonb_build_object('resumed',false,'reason','claim_stale'); END IF;
 END IF;
 held:=GREATEST(clock_timestamp()-m.terminal_at,INTERVAL '0');
 FOR p IN SELECT * FROM public.postprocessing_plans WHERE one_shot_id=m.id AND state='queued' ORDER BY id COLLATE "C" LOOP
  plans:=plans+1;
  IF held>INTERVAL '0' THEN
   UPDATE public.postprocessing_plans SET plan_wall_until=plan_wall_until+held,updated_at=CURRENT_TIMESTAMP WHERE id=p.id;
  END IF;
  FOR w IN SELECT * FROM public.work_items WHERE owner_type='postprocessing_plan' AND owner_id=p.id AND kind='postprocess_plan' LOOP
   -- The immutable index records actual plan business attempts. Admission is
   -- deliberately not used here: a claim may have been admitted and crashed
   -- before plan_start_attempt without producing any business attempt.
   SELECT count(*) INTO started FROM jsonb_array_elements(p.plan_attempt_index) e WHERE e->>'work_item_id'=w.id;
   current_started:=EXISTS(SELECT 1 FROM jsonb_array_elements(p.plan_attempt_index) e
    WHERE e->>'work_item_id'=w.id AND (e->>'token')::bigint=w.fencing_token);
   IF w.attempt<started THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RESUME_ATTEMPT_INCONSISTENT';
   END IF;
   target:=started+CASE WHEN w.state='leased' AND NOT w.recovery_blocked AND w.lease_until>clock_timestamp()
    AND NOT current_started THEN 1 ELSE 0 END;
   IF w.attempt>target THEN
    UPDATE public.work_items SET attempt=target,updated_at=CURRENT_TIMESTAMP WHERE id=w.id;
    refunded:=refunded+(w.attempt-target);
   END IF;
   -- A recovery observer cannot pass the blocked marker gate, so maintenance
   -- must release exactly the false attempt_exhausted quarantine left by that
   -- refusal. BlockRecovery advances the fencing token, so token comparison
   -- alone cannot distinguish this from an interrupted business attempt. An
   -- issued plan ticket is the authoritative evidence of the latter and stays
   -- with the ordinary interrupted-plan recovery protocol.
   IF w.recovery_blocked AND w.recovery_reason='attempt_exhausted'
    AND NOT w.cancel_requested
    AND p.plan_attempt_ticket->>'state' IS DISTINCT FROM 'issued' THEN
    UPDATE public.work_items SET state='available',available_at=clock_timestamp(),lease_until=NULL,
     recovery_blocked=false,recovery_reason=NULL,fencing_token=fencing_token+1,
     last_error_code='ONE_SHOT_RESUMED',
     last_error_reason='the one-shot acquisition was blocked before this claim started a plan attempt; attempt exhaustion was repaired',
     updated_at=CURRENT_TIMESTAMP
    WHERE id=w.id AND fencing_token=w.fencing_token;
    IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RESUME_RECOVERY_STALE'; END IF;
    revived:=revived+1;
   END IF;
  END LOOP;
 END LOOP;
 IF p_work IS NOT NULL THEN
  UPDATE public.work_items SET state='available',available_at=clock_timestamp(),lease_until=NULL,attempt=attempt-1,
   fencing_token=fencing_token+1,last_error_code='ONE_SHOT_RESUMED',
   last_error_reason='the one-shot acquisition was blocked while its work was live; the claim was returned without using an attempt',
   updated_at=CURRENT_TIMESTAMP
  WHERE id=p_work AND state='leased' AND fencing_token=p_token AND attempt>=1;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RESUME_CLAIM_STALE'; END IF;
 END IF;
 UPDATE public.one_shot_acquisitions SET state='running',terminal_outcome=NULL,terminal_reason_code=NULL,
  terminal_reason=NULL,terminal_at=NULL,retention_until=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP
 WHERE id=m.id;
 RETURN jsonb_build_object('resumed',true,'plans',plans,'refunded',refunded,'recovery_revived',revived,
  'held_seconds',floor(extract(epoch FROM held))::bigint,'claim_returned',p_work IS NOT NULL);
END $$;

-- The plan budget guard of 097, with one more admitted change of the wall: a
-- queued plan of a cleanup_blocked marker moves it later by at most the time
-- the marker has been blocked (one_shot_resume_blocked_chain). Every other
-- budget column stays immutable.
CREATE OR REPLACE FUNCTION guard_plan_attempt_budget() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry jsonb; n integer:=0; prior jsonb; active jsonb; identities text[]:=ARRAY[]::text[]; identity text;
BEGIN
 IF TG_OP='INSERT' THEN
  -- Complete every budget column the writer left NULL (plan:
  -- 5 attempts / 120s / 86400s wall).
  IF NEW.plan_ready_at IS NULL THEN NEW.plan_ready_at:=CURRENT_TIMESTAMP; END IF;
  IF NEW.plan_wall_until IS NULL THEN NEW.plan_wall_until:=CURRENT_TIMESTAMP + INTERVAL '86400 seconds'; END IF;
  IF NEW.plan_max_attempts IS NULL THEN NEW.plan_max_attempts:=5; END IF;
  IF NEW.plan_attempt_timeout_seconds IS NULL THEN NEW.plan_attempt_timeout_seconds:=120; END IF;
  IF NEW.plan_attempts_started<>0 OR NEW.plan_attempt_ticket IS NOT NULL OR NEW.plan_attempt_index<>'[]'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INITIAL_SHAPE'; END IF;
 ELSE
  IF ROW(NEW.plan_ready_at,NEW.plan_max_attempts,NEW.plan_attempt_timeout_seconds) IS DISTINCT FROM
     ROW(OLD.plan_ready_at,OLD.plan_max_attempts,OLD.plan_attempt_timeout_seconds)
   OR (NEW.plan_wall_until IS DISTINCT FROM OLD.plan_wall_until AND ((OLD.state='waiting_config' AND NEW.state='queued'
     AND NEW.plan_wall_until>OLD.plan_wall_until
     AND NEW.plan_wall_until<=OLD.plan_wall_until+(clock_timestamp()-OLD.config_hold_since))
    OR (OLD.state='queued' AND NEW.state='queued' AND NEW.one_shot_id IS NOT NULL
     AND NEW.plan_wall_until>OLD.plan_wall_until
     AND NEW.plan_wall_until<=OLD.plan_wall_until+(clock_timestamp()-(SELECT m.terminal_at FROM public.one_shot_acquisitions m
      WHERE m.id=NEW.one_shot_id AND m.state='cleanup_blocked')))) IS NOT TRUE)
   OR NEW.plan_attempts_started<OLD.plan_attempts_started
   OR NEW.plan_attempts_started>OLD.plan_attempts_started+1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_BUDGET_IMMUTABLE'; END IF;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(NEW.plan_attempt_index) LOOP
  n:=n+1;
  IF jsonb_typeof(entry)<>'object' OR (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(entry) key)
   IS DISTINCT FROM ARRAY['ordinal','session_digest','state','token','work_item_id']::text[]
   OR ((entry->>'ordinal')::integer=n AND (entry->>'token')::bigint>=1
    AND entry->>'session_digest' ~ '^sha256:[0-9a-f]{64}$' AND char_length(entry->>'work_item_id') BETWEEN 1 AND 128) IS NOT TRUE
   OR (entry->>'state' IN ('issued','completed','superseded','timed_out')) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INDEX_SHAPE'; END IF;
  identity:=(entry->>'work_item_id')||':'||(entry->>'token');
  IF identity=ANY(identities) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INDEX_DUPLICATE'; END IF;
  identities:=array_append(identities,identity);
  IF entry->>'state'='issued' THEN
   IF active IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_MULTIPLE_ACTIVE'; END IF;
   active:=entry;
  END IF;
  IF TG_OP='UPDATE' AND n<=OLD.plan_attempts_started THEN
   prior:=OLD.plan_attempt_index->(n-1);
   IF (entry-'state') IS DISTINCT FROM (prior-'state') OR
     (prior->>'state'<>'issued' AND entry IS DISTINCT FROM prior) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_IDENTITY_IMMUTABLE'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='UPDATE' AND OLD.plan_attempt_ticket IS NOT NULL AND NEW.plan_attempts_started=OLD.plan_attempts_started
  AND (NEW.plan_attempt_ticket-'state') IS DISTINCT FROM (OLD.plan_attempt_ticket-'state') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_IMMUTABLE'; END IF;
 IF NEW.plan_attempt_ticket IS NOT NULL THEN
  IF (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(NEW.plan_attempt_ticket) key)
   IS DISTINCT FROM ARRAY['attempt_until','executor_session','ordinal','started_at','state','token','work_item_id']::text[]
   OR ((NEW.plan_attempt_ticket-'started_at'-'attempt_until'-'executor_session')||jsonb_build_object(
    'session_digest','sha256:'||encode(sha256(convert_to(NEW.plan_attempt_ticket->>'executor_session','UTF8')),'hex'))) IS DISTINCT FROM NEW.plan_attempt_index->(NEW.plan_attempts_started-1)
   OR ((NEW.plan_attempt_ticket->>'started_at')::timestamptz>=NEW.plan_ready_at AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz>(NEW.plan_attempt_ticket->>'started_at')::timestamptz AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz<=NEW.plan_wall_until AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz<=(NEW.plan_attempt_ticket->>'started_at')::timestamptz+make_interval(secs=>NEW.plan_attempt_timeout_seconds)) IS NOT TRUE
   THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_SHAPE'; END IF;
 ELSIF NEW.plan_attempts_started<>0 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_MISSING';
 END IF;
 RETURN NEW;
END $_$;

REVOKE ALL ON FUNCTION one_shot_chain_open(text), one_shot_resume_blocked_chain(text, text, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION one_shot_chain_open(text), one_shot_resume_blocked_chain(text, text, bigint) TO bansoko_runtime;
