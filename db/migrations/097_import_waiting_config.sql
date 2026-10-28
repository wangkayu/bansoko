-- Forward-only migration 097: an import waits for the anime's configuration.
--
-- A subscription plan whose current configuration cannot render an import
-- path (no BGM or TMDB binding, or a template variable the render does not
-- define) no longer plans and fails. The plan stage checks the configuration
-- before it spends an attempt and parks the plan in waiting_config instead:
--
--   * postprocessing_plans.state gains waiting_config. config_hold lists the
--     missing items with the number of required files each one blocks,
--     config_hold_files the number of files any of them blocks;
--     config_hold_since is when the plan started waiting. A one-shot plan never
--     waits.
--   * postprocessing_plan_wait_config records the hold and returns the work
--     item to the queue with the claim's attempt refunded, so waiting spends
--     neither the work item's attempts nor the plan's attempt budget. Nothing
--     else of the acquisition moves: no file operation exists yet, and the
--     downloaded files stay where they are.
--   * postprocessing_plan_config_resume puts a plan whose configuration is
--     complete again back to queued in the same claim, and extends the plan's
--     wall by the time it waited, so waiting never exhausts the budget the
--     first planning attempt would have had.
--   * postprocessing_plan_wake_config makes waiting plans due now. Every
--     configuration write of an anime calls it for that anime in its own
--     transaction, and a periodic sweep calls it for every anime as the
--     backstop; both are bounded per call.
--   * config_hold_revision moves whenever the hold or the state changes, and
--     the acquisition activity consumer logs each revision once
--     (config_hold_logged_revision, through acquisition_activity_mark_logged).
--     A re-check that finds the same hold writes nothing.
--
-- A waiting plan is live work like a queued one: the root stays running,
-- the retirement of the instance retires its work item and settles the root
-- cancelled (the plan row is left as a queued plan is), and a cancelled work
-- item fails the plan with PLAN_CANCELLED through postprocessing_plan_fail,
-- which clears the hold.

ALTER TABLE postprocessing_plans
    ADD COLUMN config_hold JSONB,
    ADD COLUMN config_hold_files INTEGER,
    ADD COLUMN config_hold_since TIMESTAMPTZ,
    ADD COLUMN config_hold_revision INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN config_hold_logged_revision INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN postprocessing_plans.config_hold IS 'While waiting_config: the configuration items the current render lacks, [{"item","files"}], item binding_missing:bgm|tmdb or template_variable_undefined:<names>.';
COMMENT ON COLUMN postprocessing_plans.config_hold_files IS 'While waiting_config: the number of files at least one hold item blocks.';
COMMENT ON COLUMN postprocessing_plans.config_hold_revision IS 'Moves with every change of the hold or of the waiting state; the activity consumer logs each revision once (config_hold_logged_revision).';

-- The hold shape: 1..16 distinct items, each {"item","files"} with a known
-- item and a positive file count.
CREATE FUNCTION postprocessing_plan_config_hold_valid(p_hold jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN jsonb_typeof(p_hold) IS DISTINCT FROM 'array' THEN false
  WHEN jsonb_array_length(p_hold) NOT BETWEEN 1 AND 16 THEN false
  ELSE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_hold) e(v) WHERE
    jsonb_typeof(v) IS DISTINCT FROM 'object'
    OR (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(v)='object' THEN v ELSE '{}'::jsonb END) k)
       IS DISTINCT FROM ARRAY['files','item']::text[]
    OR jsonb_typeof(v->'item') IS DISTINCT FROM 'string' OR jsonb_typeof(v->'files') IS DISTINCT FROM 'number'
    OR char_length(v->>'item')>256
    OR (v->>'item' ~ '^(binding_missing:(bgm|tmdb)|template_variable_undefined:[^\s,]+(,[^\s,]+)*)$') IS NOT TRUE
    OR (v->>'files' ~ '^[1-9][0-9]{0,5}$') IS NOT TRUE)
   AND (SELECT count(DISTINCT v->>'item') FROM jsonb_array_elements(p_hold) e(v))=jsonb_array_length(p_hold)
 END
$$;

ALTER TABLE postprocessing_plans DROP CONSTRAINT postprocessing_plans_state_check;
ALTER TABLE postprocessing_plans ADD CONSTRAINT postprocessing_plans_state_check
    CHECK (state IN ('queued', 'waiting_config', 'succeeded', 'failed'));
ALTER TABLE postprocessing_plans DROP CONSTRAINT postprocessing_plans_state_matrix_check;
ALTER TABLE postprocessing_plans ADD CONSTRAINT postprocessing_plans_state_matrix_check
    CHECK ((state IN ('queued', 'waiting_config') AND outcome_code IS NULL AND outcome_reason IS NULL)
        OR (state IN ('succeeded', 'failed') AND outcome_code IS NOT NULL AND outcome_reason IS NOT NULL));
-- A hold exists exactly while the plan waits, never on a one-shot plan.
ALTER TABLE postprocessing_plans ADD CONSTRAINT postprocessing_plans_config_hold_check
    CHECK ((state = 'waiting_config') = (config_hold IS NOT NULL)
       AND (config_hold IS NULL) = (config_hold_since IS NULL)
       AND (config_hold IS NULL) = (config_hold_files IS NULL)
       AND (config_hold_files IS NULL OR config_hold_files BETWEEN 1 AND 999999)
       AND (config_hold IS NULL OR postprocessing_plan_config_hold_valid(config_hold))
       AND (state <> 'waiting_config' OR one_shot_id IS NULL)
       AND config_hold_logged_revision BETWEEN 0 AND config_hold_revision);

DROP INDEX postprocessing_plans_live_uq;
CREATE UNIQUE INDEX postprocessing_plans_live_uq ON postprocessing_plans (artifact_id)
    WHERE state IN ('queued', 'waiting_config');
-- The activity consumer's scan of unlogged hold revisions.
CREATE INDEX postprocessing_plans_config_hold_unlogged_idx ON postprocessing_plans (updated_at, id)
    WHERE config_hold_revision > config_hold_logged_revision;

-- The plan budget guard of the baseline, with one admitted change of the
-- wall: the resume of a waiting plan moves it later by at most the time the
-- plan waited. Every other budget column stays immutable.
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
   OR (NEW.plan_wall_until IS DISTINCT FROM OLD.plan_wall_until AND (OLD.state='waiting_config' AND NEW.state='queued'
     AND NEW.plan_wall_until>OLD.plan_wall_until
     AND NEW.plan_wall_until<=OLD.plan_wall_until+(clock_timestamp()-OLD.config_hold_since)) IS NOT TRUE)
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

-- A terminal plan stays immutable except for the activity consumer's logged
-- revision. A waiting plan resumes to queued before it can commit.
CREATE OR REPLACE FUNCTION postprocessing_plan_immutable() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.acquisition_id IS DISTINCT FROM OLD.acquisition_id
 OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id OR NEW.artifact_version IS DISTINCT FROM OLD.artifact_version
 OR NEW.input_digest IS DISTINCT FROM OLD.input_digest
 OR (OLD.state IN ('succeeded','failed') AND (to_jsonb(NEW)-'config_hold_logged_revision') IS DISTINCT FROM (to_jsonb(OLD)-'config_hold_logged_revision'))
 OR (OLD.state='waiting_config' AND NEW.state='succeeded')
 OR (OLD.sealed_at IS NOT NULL AND (NEW.sealed_at,NEW.requirements_digest,NEW.target_digest,NEW.plan_digest)
 IS DISTINCT FROM (OLD.sealed_at,OLD.requirements_digest,OLD.target_digest,OLD.plan_digest)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_IMMUTABLE'; END IF;
 RETURN NEW;
END $$;

-- The baseline clean-graph check, admitting a waiting plan: it is as
-- untouched as a queued one.
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
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest IS NOT NULL AND (
 h.requirements_manifest_digest IS DISTINCT FROM h.manifest_digest
 OR h.requirements_item_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL)
 OR h.requirements_required_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required')
 OR EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.requirement IS NULL)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.target_digest IS NOT NULL AND
 h.target_requirements_digest IS DISTINCT FROM h.requirements_digest)
 AND NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND (f.acquisition_id IS DISTINCT FROM p.acquisition_id
 OR NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.file_ref=f.file_ref
 AND m.relative_path=f.relative_path AND m.exact_size=f.size_bytes AND m.media_kind=f.media_kind)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND NOT EXISTS(
 SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND f.file_ref=m.file_ref))
 WHERE p.id=p_plan
$$;

-- The baseline terminal planning failure; a waiting plan that fails
-- (cancelled) drops its hold, and the hold's unlogged revision is not logged.
CREATE OR REPLACE FUNCTION postprocessing_plan_fail(p_plan text, p_work text, p_code text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; v bigint; status text;
BEGIN
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=p_plan;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>1024 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_FAILURE_SHAPE'; END IF;
 status:=CASE WHEN (SELECT r.requirements_digest IS NOT NULL FROM public.acquisitions r WHERE r.id=p.acquisition_id) THEN 'sealed' ELSE 'not_reached' END;
 IF status='sealed' THEN
 UPDATE public.acquisition_files f SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
 delivery_source_cleanup='not_required',delivery_reason_code=p_code,
 evidence_digest='sha256:'||encode(sha256(convert_to('plan-failure:v1','UTF8')||decode('00','hex')||convert_to(p.input_digest||':'||f.file_ref||':'||p_code,'UTF8')),'hex'),
 receipt=jsonb_build_object('schema_version',1,'reason_code',p_code,'stage','planning'),
 delivered_at=CURRENT_TIMESTAMP
 WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required';
 END IF;
 PERFORM public.download_ensure_settlement(p.acquisition_id);
 UPDATE public.postprocessing_plans SET state='failed',outcome_code=CASE WHEN p_code='PLAN_CANCELLED' THEN p_code ELSE 'plan_failed' END,
 outcome_reason=p_reason,planning_requirements_status=status,
 planning_diagnostic=jsonb_build_object('schema_version',1,'reason_code',p_code,'requirements_status',status),
 planning_facts_revision=(SELECT facts_revision FROM public.acquisitions WHERE id=p.acquisition_id),
 config_hold=NULL,config_hold_files=NULL,config_hold_since=NULL,config_hold_logged_revision=config_hold_revision,
 version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_plan RETURNING version INTO v;
 IF public.work_item_fail_at_attempt_budget(v,p_code,p_reason,p_work,p.version)<>1 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 RETURN v;
END $$;

-- The plan stage found the current configuration incomplete: record the hold
-- and the files it blocks (the revision moves only when they or the state
-- change) and return the work item, due after p_park_seconds, with this
-- claim's attempt refunded. A cancelled work item fails the plan instead.
CREATE FUNCTION postprocessing_plan_wait_config(p_plan text, p_work text, p_token bigint, p_hold jsonb, p_files integer, p_park_seconds double precision) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE; v bigint; changed boolean;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.recovery_blocked OR p.one_shot_id IS NOT NULL OR p_park_seconds IS NULL OR p_park_seconds<0 OR p_park_seconds>86400
 OR public.postprocessing_plan_config_hold_valid(p_hold) IS NOT TRUE OR p_files IS NULL OR p_files NOT BETWEEN 1 AND 999999 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF w.cancel_requested THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,'PLAN_CANCELLED','read-only planning was cancelled');
 RETURN jsonb_build_object('state','failed','reason_code','PLAN_CANCELLED');
 END IF;
 changed:=p.state IS DISTINCT FROM 'waiting_config' OR p.config_hold IS DISTINCT FROM p_hold OR p.config_hold_files IS DISTINCT FROM p_files;
 UPDATE public.postprocessing_plans SET state='waiting_config',config_hold=p_hold,config_hold_files=p_files,
 config_hold_since=COALESCE(config_hold_since,clock_timestamp()),
 config_hold_revision=config_hold_revision+CASE WHEN changed THEN 1 ELSE 0 END,
 updated_at=CASE WHEN changed THEN CURRENT_TIMESTAMP ELSE updated_at END,
 version=version+1 WHERE id=p_plan RETURNING version INTO v;
 UPDATE public.work_items SET state='available',available_at=clock_timestamp()+make_interval(secs=>p_park_seconds),
 lease_until=NULL,attempt=attempt-1,expected_owner_version=v,last_error_code='PLAN_WAITING_CONFIG',
 last_error_reason='the current configuration cannot render an import path',updated_at=CURRENT_TIMESTAMP
 WHERE id=p_work AND state='leased' AND fencing_token=p_token AND expected_owner_version=p.version AND attempt>=1;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 RETURN jsonb_build_object('state','waiting_config','reason_code','PLAN_WAITING_CONFIG','changed',changed);
END $$;

-- The plan stage found the configuration of a waiting plan complete: back to
-- queued in the same claim, the wall moved by the time it waited. A plan that
-- is not waiting is left alone.
CREATE FUNCTION postprocessing_plan_config_resume(p_plan text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 IF p.state IS DISTINCT FROM 'waiting_config' THEN RETURN jsonb_build_object('resumed',false); END IF;
 UPDATE public.postprocessing_plans SET state='queued',config_hold=NULL,config_hold_files=NULL,config_hold_since=NULL,
 config_hold_revision=config_hold_revision+1,
 plan_wall_until=plan_wall_until+(clock_timestamp()-config_hold_since),updated_at=CURRENT_TIMESTAMP
 WHERE id=p_plan;
 RETURN jsonb_build_object('resumed',true);
END $$;

-- Make waiting plans due now: one anime's (p_managed_anime) or every anime's
-- (NULL), at most p_limit per call, the longest-parked first. A plan another
-- transaction holds is skipped; the sweep reaches it later.
CREATE FUNCTION postprocessing_plan_wake_config(p_managed_anime text, p_limit integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE woken integer;
BEGIN
 IF p_limit IS NULL OR p_limit NOT BETWEEN 1 AND 1000 THEN
 RAISE EXCEPTION USING ERRCODE='22023',MESSAGE='PLAN_CONFIG_WAKE_LIMIT'; END IF;
 WITH due AS (
  SELECT w.id FROM public.work_items w JOIN public.postprocessing_plans p ON p.id=w.owner_id
  WHERE w.owner_type='postprocessing_plan' AND w.kind='postprocess_plan' AND w.state='available'
   AND w.available_at>clock_timestamp() AND p.state='waiting_config'
   AND (p_managed_anime IS NULL OR w.managed_anime_id=p_managed_anime)
  ORDER BY w.available_at, w.id LIMIT p_limit
  FOR UPDATE OF w SKIP LOCKED)
 UPDATE public.work_items w SET available_at=clock_timestamp(),updated_at=CURRENT_TIMESTAMP
 FROM due WHERE w.id=due.id;
 GET DIAGNOSTICS woken=ROW_COUNT;
 RETURN woken;
END $$;

-- The consumer's marker, with the hold revision as a fact: p_file_ref carries
-- the revision the entry describes.
CREATE OR REPLACE FUNCTION acquisition_activity_mark_logged(p_fact text, p_id text, p_file_ref text DEFAULT NULL) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 CASE p_fact
 WHEN 'download_submitted' THEN
  PERFORM 1 FROM public.download_tasks WHERE id=p_id AND activity_submit_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.download_tasks SET activity_submit_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'download_terminal' THEN
  PERFORM 1 FROM public.download_tasks WHERE id=p_id AND activity_terminal_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.download_tasks SET activity_terminal_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'artifact_verified' THEN
  PERFORM 1 FROM public.artifacts WHERE id=p_id AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.artifacts SET activity_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'file_result' THEN
  PERFORM 1 FROM public.acquisition_files WHERE acquisition_id=p_id AND file_ref=p_file_ref AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.acquisition_files SET activity_logged_at=CURRENT_TIMESTAMP WHERE acquisition_id=p_id AND file_ref=p_file_ref;
 WHEN 'acquisition_settled' THEN
  PERFORM 1 FROM public.acquisitions WHERE id=p_id AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.acquisitions SET activity_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'plan_config_hold' THEN
  IF p_file_ref IS NULL OR p_file_ref !~ '^[1-9][0-9]{0,8}$' THEN
   RAISE EXCEPTION USING ERRCODE='22023',MESSAGE='ACTIVITY_FACT_KIND'; END IF;
  PERFORM 1 FROM public.postprocessing_plans WHERE id=p_id AND config_hold_revision=p_file_ref::integer
   AND config_hold_logged_revision<p_file_ref::integer FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.postprocessing_plans SET config_hold_logged_revision=p_file_ref::integer WHERE id=p_id;
 ELSE
  RAISE EXCEPTION USING ERRCODE='22023',MESSAGE='ACTIVITY_FACT_KIND';
 END CASE;
 RETURN true;
END $$;

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'release_processed', 'download_changed', 'artifact_verified', 'import_waiting_config', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'cleanup_failed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled'));

REVOKE ALL ON FUNCTION postprocessing_plan_config_hold_valid(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION postprocessing_plan_wait_config(text, text, bigint, jsonb, integer, double precision) FROM PUBLIC;
REVOKE ALL ON FUNCTION postprocessing_plan_config_resume(text, text, bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION postprocessing_plan_wake_config(text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION postprocessing_plan_wait_config(text, text, bigint, jsonb, integer, double precision) TO bansoko_runtime;
GRANT EXECUTE ON FUNCTION postprocessing_plan_config_resume(text, text, bigint) TO bansoko_runtime;
GRANT EXECUTE ON FUNCTION postprocessing_plan_wake_config(text, integer) TO bansoko_runtime;
