-- Forward-only migration 035: a download deleted in the downloader cancels its
-- execution record.
--
-- The qB sync reports a successful lookup that no longer lists the task as the
-- observation code qb_task_absent. download_finish_sync counts it in
-- download_tasks.consecutive_absent, never in the error budget; a present
-- observation resets it and an error poll leaves it alone. The second
-- consecutive absence commits the task failed with DOWNLOADER_TASK_REMOVED.
-- The DownloadTask state set is unchanged: the acquisition root maps that code
-- to the terminal state cancelled (acquisition_settle admits cancelled for it
-- alone), the download_failed notification skips it, and a cancelled candidate
-- stales its replacement decision like a failed one.

ALTER TABLE download_tasks
    ADD COLUMN consecutive_absent INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT download_tasks_consecutive_absent_check CHECK (consecutive_absent BETWEEN 0 AND 2),
    ADD CONSTRAINT download_tasks_removed_terminal_check CHECK (
        failure_code IS DISTINCT FROM 'DOWNLOADER_TASK_REMOVED' OR (state = 'failed' AND consecutive_absent = 2));

-- The runtime role writes download_tasks per column (baseline grant list);
-- the absence counter joins consecutive_errors there.
GRANT INSERT (consecutive_absent), UPDATE (consecutive_absent) ON TABLE download_tasks TO bansoko_runtime;

CREATE OR REPLACE FUNCTION download_finish_sync(p_task text, p_work text, p_token bigint, p_session text, p_ordinal integer, p_state text, p_correlation jsonb, p_code text, p_reason text, p_observation_error boolean, p_observed_complete_bytes bigint, p_observed_total_bytes bigint, p_observed_provider_state text, p_next_id text, p_artifact_id text, p_inventory jsonb, p_fingerprints jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz;
    next_version bigint; next_work text; final_state text; code text; reason text; terminal_state text;
    inherited jsonb; artifact text; artifact_version bigint;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
    observed_bytes bigint; observed_total bigint; provider_state text;
    next_highwater bigint; next_last_progress timestamptz; stall_expired boolean;
    absent boolean;
BEGIN
    IF EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=p_work AND fencing_token=p_token
        AND owner_type='download_task' AND owner_id=p_task AND kind='qb_sync') THEN
        SELECT input_snapshot->'sync_ticket' INTO ticket FROM public.work_item_outcomes WHERE id=p_work;
        IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
            AND (ticket->>'ordinal')::integer=p_ordinal AND ticket->>'executor_session'=p_session) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
        END IF;
        RETURN jsonb_build_object('applied',false,'reason','SYNC_ALREADY_FINISHED');
    END IF;
    -- Preserve the existing optional marker-first continuation gate before
    -- entering the root -> task -> work -> evidence lock hierarchy.
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions
            WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING';
        END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_stage_lock(p_task,p_work,p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id=p_work) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    ticket := task.active_poll_ticket; now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_sync')
        OR (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
            AND (ticket->>'ordinal')::integer=p_ordinal AND ticket->>'executor_session'=p_session
            AND ticket->>'state'='issued') IS NOT TRUE
        OR (p_state IN ('submitting','downloading','completed','failed','waiting_retry')) IS NOT TRUE
        OR p_observation_error IS NULL OR p_next_id IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
    END IF;
    -- Typed observation shape is checked after identity/locks and before writes.
    -- The state is already ASCII-normalized by Go; SQL must not normalize it.
    IF (p_observation_error AND (p_observed_complete_bytes IS NOT NULL
            OR p_observed_total_bytes IS NOT NULL OR p_observed_provider_state IS NOT NULL))
        OR (NOT p_observation_error AND (p_observed_provider_state IS NULL
            OR octet_length(p_observed_provider_state)>128
            OR p_observed_provider_state COLLATE "C" !~ '^[a-z0-9]*$'))
        OR p_observed_complete_bytes<0 OR p_observed_total_bytes<0
        OR p_observed_complete_bytes>p_observed_total_bytes THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_OBSERVATION_SHAPE';
    END IF;
    IF task.state IN ('completed','failed') THEN
        UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"')
            WHERE id=p_task;
        PERFORM public.work_item_finish(
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version,p_work);
        RETURN jsonb_build_object('applied',false,'reason','SYNC_TASK_TERMINAL','state',task.state);
    END IF;
    IF (now_at<(ticket->>'attempt_until')::timestamptz AND now_at<task.wait_until) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
    END IF;
    final_state := p_state; code := p_code; reason := p_reason;
    -- 035: a successful qB lookup that no longer lists the task. It is an
    -- observation without provider facts, but it is definitive, so it never
    -- spends the error budget; an error poll neither confirms nor resets it.
    absent := p_observation_error AND p_code='qb_task_absent';
    observed_bytes := p_observed_complete_bytes;
    observed_total := p_observed_total_bytes;
    provider_state := p_observed_provider_state;
    -- The mark only rises (BC 4.1). A first reliable zero is recorded as a mark
    -- but does not restart the clock the submit confirmation already started.
    IF observed_bytes IS NULL THEN
        next_highwater := task.verified_bytes_highwater;
        next_last_progress := task.last_progress_at;
    ELSE
        next_highwater := CASE WHEN task.verified_bytes_highwater IS NULL THEN observed_bytes
            ELSE greatest(task.verified_bytes_highwater,observed_bytes) END;
        IF (task.verified_bytes_highwater IS NULL AND observed_bytes>0)
            OR (task.verified_bytes_highwater IS NOT NULL AND observed_bytes>task.verified_bytes_highwater) THEN
            next_last_progress := now_at;
        ELSE
            next_last_progress := task.last_progress_at;
        END IF;
    END IF;
    -- A row whose clock was never started -- written before the monitoring
    -- window existed, or confirmed before this column -- starts it at this
    -- observation instead of being backdated into an immediate stall.
    IF next_last_progress IS NULL THEN next_last_progress := now_at; END IF;
    stall_expired := task.stall_seconds IS NOT NULL
        AND (now_at-next_last_progress)>=(task.stall_seconds * INTERVAL '1 second');
    IF absent THEN
        task.consecutive_absent := task.consecutive_absent+1;
        IF task.consecutive_absent>=2 THEN
            -- Two consecutive definitive absences are a
            -- deletion in the downloader. The root settles this code cancelled
            -- and no download_failed notification is raised.
            final_state := 'failed'; code := 'DOWNLOADER_TASK_REMOVED';
            reason := '连续两次查询 qBittorrent 均未找到该任务，视为已在下载器中删除。未删除任何文件。';
        ELSE
            final_state := CASE
                WHEN task.state IN ('submitting','downloading') THEN task.state
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state'='downloading' THEN 'downloading'
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state' IN ('queued','submitted') THEN 'submitting'
                ELSE task.state END;
        END IF;
    ELSIF p_observation_error THEN
        task.consecutive_errors := task.consecutive_errors+1; task.total_errors := task.total_errors+1;
        IF task.consecutive_errors>=task.max_consecutive_errors OR task.total_errors>=task.max_total_errors THEN
            final_state := 'failed'; code := 'SYNC_ERROR_BUDGET_EXHAUSTED';
        ELSE
            final_state := CASE
                WHEN task.state IN ('submitting','downloading') THEN task.state
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state'='downloading' THEN 'downloading'
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state' IN ('queued','submitted') THEN 'submitting'
                ELSE task.state END;
        END IF;
    ELSE task.consecutive_errors := 0; task.consecutive_absent := 0; END IF;
    IF final_state NOT IN ('completed','failed') AND (now_at>=task.wait_until OR task.polls_started>=task.max_polls) THEN
        final_state := 'failed'; code := 'SYNC_BUDGET_EXHAUSTED';
    END IF;
    -- BC 4.2 / 13.16.12: reached only when the frozen 146 branches above left the
    -- task unsettled, so an S1 budget verdict keeps priority in every race.
    IF final_state NOT IN ('completed','failed') THEN
        IF absent THEN
            -- One absence is not yet a verdict: the next poll confirms it or the
            -- task reappears. The stall judgment waits for that poll.
            NULL;
        ELSIF p_observation_error THEN
            -- No trustworthy current observation. A retained high-water mark, a
            -- cached pause tag or the previous correlation cannot stand in for
            -- one, and being unable to observe is not proof of a stall.
            IF stall_expired THEN
                final_state := 'failed'; code := 'DOWNLOAD_OBSERVATION_UNAVAILABLE';
                reason := format('本轮未能读取到可信的 qBittorrent 状态，且已超过 stall 时限 %s 没有可核实进展。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'));
            END IF;
        ELSIF provider_state IN ('pauseddl','stoppeddl','checkingdl','checkingup','checkingresumedata') THEN
            -- Only this round's judgment is skipped: the clock is untouched, so
            -- the pause is not credited and a resume can stall immediately.
            NULL;
        ELSIF stall_expired THEN
            final_state := 'failed';
            IF next_highwater IS NULL THEN
                code := 'DOWNLOAD_PROGRESS_UNVERIFIABLE';
                reason := format('下载进展已 %s 无法核实：qBittorrent 尚未给出可信的完成字节数，因此不宣称“没有进展”。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'));
            ELSE
                code := 'DOWNLOAD_STALLED';
                reason := format('下载已连续 %s 没有任何进展（已完成字节停留在 %s），已停止自动同步。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'),next_highwater);
            END IF;
        END IF;
    END IF;
    SELECT COALESCE(jsonb_build_object('one_shot',input_snapshot->'one_shot'),'{}') INTO inherited
        FROM public.work_items WHERE id=p_work AND input_snapshot ? 'one_shot';
    inherited := COALESCE(inherited,'{}');
    UPDATE public.download_tasks SET state=final_state,
        provider_correlation=COALESCE(p_correlation,provider_correlation),
        failure_code=CASE WHEN final_state IN ('failed','waiting_retry') THEN NULLIF(code,'') ELSE NULL END,
        failure_reason=CASE WHEN final_state IN ('failed','waiting_retry') THEN NULLIF(reason,'') ELSE NULL END,
        consecutive_errors=task.consecutive_errors,total_errors=task.total_errors,
        consecutive_absent=task.consecutive_absent,
        stall_since=next_last_progress,active_poll_ticket=jsonb_set(ticket,'{state}','"completed"'),
        verified_bytes_highwater=next_highwater,
        payload_total_bytes=COALESCE(observed_total,payload_total_bytes),
        last_progress_at=next_last_progress,
        budget_clock_at=now_at,version=version+1,updated_at=now_at WHERE id=p_task RETURNING version INTO next_version;
    terminal_state := CASE WHEN final_state='failed' THEN 'failed' ELSE 'succeeded' END;
    PERFORM public.work_item_finish(terminal_state,next_version,p_work);
    IF final_state IN ('submitting','downloading','waiting_retry') THEN
        SELECT public.work_item_create(p_next_id,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_progress','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,task.poll_interval_seconds::double precision,7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
    ELSIF final_state='completed' THEN
        IF p_artifact_id IS NULL OR (jsonb_typeof(p_inventory)='object' AND jsonb_typeof(p_fingerprints)='object') IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_ARTIFACT_SHAPE';
        END IF;
        SELECT id,version INTO artifact,artifact_version FROM public.artifacts WHERE download_task_id=p_task;
        IF NOT FOUND THEN
            INSERT INTO public.artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,
                state,correlation_id,causation_type,causation_id)
            VALUES(p_artifact_id,p_task,task.acquisition_id,p_inventory,p_fingerprints,
                'pending',task.correlation_id,'download_task',p_task) RETURNING id,version INTO artifact,artifact_version;
        END IF;
        SELECT public.work_item_create(p_next_id,'artifact',artifact,'artifact_inspect',
            'artifact_inspect:'||artifact,5,jsonb_build_object('trigger','download_completed')||inherited,
            artifact_version,0::double precision,7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_INSPECT_CONFLICT'; END IF;
    END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'task_version',next_version,'state',final_state,
        'reason_code',code,'reason_text',COALESCE(reason,''));
END;
$_$;

CREATE OR REPLACE FUNCTION acquisition_settle(p_root text, p_facts_revision bigint, p_receipt jsonb, p_synthesized jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; now_at timestamptz:=clock_timestamp(); s record; receipt_text text; outcome text;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_ROOT_MISSING'; END IF;
 IF r.state<>'running' OR r.record_state<>'full' THEN RETURN jsonb_build_object('status','already_terminal','state',r.state); END IF;
 IF r.execution_branch<>'download' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_BRANCH'; END IF;
 IF r.facts_revision<>p_facts_revision THEN RETURN jsonb_build_object('status','stale'); END IF;
 IF public.acquisition_live_work(p_root) THEN RETURN jsonb_build_object('status','live_work'); END IF;
 outcome:=p_receipt->>'outcome';
 IF jsonb_typeof(p_receipt)<>'object' OR (p_receipt->>'schema_version')::int IS DISTINCT FROM 1
   OR outcome NOT IN ('succeeded','failed','cancelled') OR jsonb_typeof(p_synthesized)<>'array' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_DECISION_SHAPE'; END IF;
 -- 035: the settler cancels only a download deleted in the downloader, and only
 -- when the task row really ended that way; every other cancellation is the
 -- retirement disposition's.
 IF outcome='cancelled' AND (p_receipt->'primary_failure'->>'reason_code'='DOWNLOADER_TASK_REMOVED'
   AND p_receipt->'primary_failure'->>'stage' IN ('submit','download_wait')
   AND EXISTS(SELECT 1 FROM public.download_tasks t WHERE t.acquisition_id=p_root
     AND t.state='failed' AND t.failure_code='DOWNLOADER_TASK_REMOVED')) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_CANCELLED_REASON'; END IF;
 FOR s IN SELECT x.file_ref, x.reason_code FROM jsonb_to_recordset(p_synthesized) AS x(file_ref text, reason_code text) LOOP
  IF NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p_root AND f.file_ref=s.file_ref AND f.requirement='required')
    OR EXISTS(SELECT 1 FROM public.acquisition_files d WHERE d.acquisition_id=p_root AND d.file_ref=s.file_ref AND d.outcome IS NOT NULL) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_SYNTHESIZED_FILE'; END IF;
  receipt_text:=jsonb_build_object('reason_code',s.reason_code,'schema_version',1,'synthesized',true)::text;
  UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
    delivery_source_cleanup='not_required',delivery_reason_code=s.reason_code,
    evidence_digest=public.file_proof_digest('acquisition-settlement-synthesized:v1',receipt_text),receipt=receipt_text::jsonb,
    delivered_at=CURRENT_TIMESTAMP WHERE acquisition_id=p_root AND file_ref=s.file_ref;
 END LOOP;
 IF outcome='succeeded' AND EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p_root AND f.requirement='required'
   AND f.outcome IS DISTINCT FROM 'succeeded') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_SUCCESS_UNPROVEN'; END IF;
 PERFORM public.acquisition_write_terminal(p_root,outcome,
   p_receipt || jsonb_build_object('settled_at',public.acquisition_terminal_time_text(now_at)),now_at);
 RETURN jsonb_build_object('status','settled','state',outcome);
END $$;

DROP TRIGGER download_task_failed_notification ON download_tasks;
CREATE TRIGGER download_task_failed_notification
    AFTER UPDATE OF state ON download_tasks
    FOR EACH ROW WHEN (((old.state IS DISTINCT FROM 'failed'::text) AND (new.state = 'failed'::text)
        AND (new.failure_code IS DISTINCT FROM 'DOWNLOADER_TASK_REMOVED'::text)))
    EXECUTE FUNCTION download_task_failed_admission();

DROP TRIGGER acquisitions_candidate_failed ON acquisitions;
CREATE TRIGGER acquisitions_candidate_failed
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN (((old.state IS DISTINCT FROM new.state) AND (new.state = ANY (ARRAY['failed'::text, 'cancelled'::text]))))
    EXECUTE FUNCTION stale_failed_candidate_decisions();
