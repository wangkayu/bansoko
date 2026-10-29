-- Forward-only migration 103: a download cancelled before import.
--
-- Two causes share one mechanism:
--   * provider_group_not_selected: the provider group of the task's source left
--     the subscription's selection (the transition 081 already handles). The
--     deferred drop_unselected_awaiting_releases trigger marks the intent on
--     the subscription's pre-import download tasks of that group at commit.
--   * user_cancelled: an explicit cancel of one execution record
--     (acquisition_cancel), for subscription and one-shot records alike.
--
-- The intent lives on download_tasks (cancel_cause, cancel_requested_at). The
-- trigger skips a task row a running stage holds instead of waiting for it;
-- the worker derives the same selection intent at that task's next step
-- (download_cancel_group_due). The download worker acts on it: qb_submit cancels an attempt that never sent
-- anything (download_cancel_unsent_submission, no qB call); qb_sync takes a
-- cancel ticket (download_cancel_start), deletes the qB task with its data
-- under the download-completion ownership rule, and settles the task
-- (download_finish_cancel). A selection cancel is re-checked under the owner
-- lock first and withdrawn when the group is selected again; once a delete
-- may have run (cancel_dispatched_at) it is never withdrawn. Completion wins
-- over an intent no worker acted on: the guard clears it, so a task that
-- moved into import is never deleted.
--
-- The task ends failed with PROVIDER_GROUP_NOT_ALLOWED or USER_CANCELLED; the
-- root settles cancelled, no download_failed notification is raised, and the
-- coverage claim is held until that settle. After the settle a selection
-- cancel detaches its Release back to filtered/provider_group_not_selected
-- (the state 081 gives the group's waiting releases); a user cancel keeps its
-- Release on the cancelled task until a retry.
--
-- The selection's entry mirrors 081's exit: when a group is available and
-- selected again at commit (or a selected row is inserted), the subscription's
-- releases of that group in filtered/provider_group_not_selected with no
-- download task are evaluated again from their stored facts (no fetch), so a
-- reselect reopens them without depending on a feed refresh.

ALTER TABLE download_tasks
    ADD COLUMN cancel_cause TEXT,
    ADD COLUMN cancel_requested_at TIMESTAMPTZ,
    ADD COLUMN cancel_dispatched_at TIMESTAMPTZ,
    ADD COLUMN cancel_ticket JSONB,
    ADD COLUMN cancel_effect TEXT,
    ADD CONSTRAINT download_tasks_cancel_cause_check CHECK (
        cancel_cause IS NULL OR cancel_cause IN ('provider_group_not_selected', 'user_cancelled')),
    ADD CONSTRAINT download_tasks_cancel_intent_shape_check CHECK ((cancel_cause IS NULL) = (cancel_requested_at IS NULL)),
    ADD CONSTRAINT download_tasks_cancel_dispatch_check CHECK (cancel_dispatched_at IS NULL OR cancel_cause IS NOT NULL),
    ADD CONSTRAINT download_tasks_cancel_ticket_check CHECK (cancel_ticket IS NULL OR (
        cancel_dispatched_at IS NOT NULL AND jsonb_typeof(cancel_ticket) = 'object'
        AND octet_length(cancel_ticket::text) <= 65536)),
    ADD CONSTRAINT download_tasks_cancel_effect_check CHECK (cancel_effect IS NULL OR (
        cancel_effect IN ('not_submitted', 'deleted_with_data', 'absent', 'detached')
        AND cancel_cause IS NOT NULL AND state = 'failed')),
    ADD CONSTRAINT download_tasks_cancelled_terminal_check CHECK (
        failure_code IS NULL OR failure_code NOT IN ('PROVIDER_GROUP_NOT_ALLOWED', 'USER_CANCELLED')
        OR (state = 'failed' AND cancel_effect IS NOT NULL AND cancel_cause =
            CASE failure_code WHEN 'PROVIDER_GROUP_NOT_ALLOWED' THEN 'provider_group_not_selected' ELSE 'user_cancelled' END));
-- The cancel columns are written only by the definer functions below; the
-- runtime role keeps its per-column grant list unchanged.

-- The failure code and the committed text of one cause and effect.
CREATE FUNCTION download_cancel_code(p_cause text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
SELECT CASE p_cause WHEN 'provider_group_not_selected' THEN 'PROVIDER_GROUP_NOT_ALLOWED'
    WHEN 'user_cancelled' THEN 'USER_CANCELLED' END;
$$;

CREATE FUNCTION download_cancel_reason(p_cause text, p_effect text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
SELECT CASE p_cause WHEN 'provider_group_not_selected' THEN '字幕组未选中，已取消下载。'
    ELSE '已按请求取消下载。' END ||
    CASE p_effect
        WHEN 'not_submitted' THEN '下载尚未提交到 qBittorrent，未删除任何文件。'
        WHEN 'deleted_with_data' THEN '已在 qBittorrent 中删除该任务及其已下载的数据。'
        WHEN 'absent' THEN 'qBittorrent 中已没有该任务，未删除任何文件。'
        ELSE '该 qBittorrent 任务不归这次下载独有或无法安全删除，已保留在 qBittorrent 中，未删除任何文件。' END;
$$;

-- True when the task is a subscription download in an active subscription of
-- an active anime whose provider-group source has left the selection (the
-- group is not available and selected). It is both the trigger's marking
-- rule and the worker's re-check under the owner lock: a group selected
-- again, or a subscription stopped, before any delete withdraws the intent.
-- The worker also derives a missing intent from it, so a task the trigger
-- skipped (row locked by a running stage) is still cancelled at its next step.
CREATE FUNCTION download_cancel_group_due(p_task text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
SELECT EXISTS (
    SELECT 1 FROM public.download_tasks t
    JOIN public.acquisitions a ON a.id = t.acquisition_id
    JOIN public.releases r ON r.id = t.release_id
    JOIN public.rss_sources src ON src.id = r.rss_source_id
    JOIN public.subscriptions sub ON sub.id = src.subscription_id
    JOIN public.managed_anime anime ON anime.id = sub.managed_anime_id
    WHERE t.id = p_task AND a.entry_kind <> 'one_shot' AND src.source_kind = 'provider_group'
      AND sub.state = 'active' AND anime.lifecycle_state = 'active'
      AND NOT EXISTS (
          SELECT 1 FROM public.provider_group_selections g
          WHERE g.subscription_id = src.subscription_id AND g.provider = src.provider
            AND g.group_ref = src.provider_group_ref AND g.available AND g.effective_selected));
$$;

-- Completion and an intent no worker acted on resolve to completion: the
-- intent is cleared in the same row write, so a task that moved into import
-- is never cancelled. A dispatched cancel (a delete may have run) can only end
-- through download_finish_cancel. A failure that is not the cancel's own
-- clears an undispatched intent too.
CREATE FUNCTION download_task_cancel_intent_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NEW.state IS NOT DISTINCT FROM OLD.state OR NEW.cancel_cause IS NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.state = 'completed' THEN
        IF NEW.cancel_dispatched_at IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_CANCEL_DISPATCHED';
        END IF;
        NEW.cancel_cause := NULL;
        NEW.cancel_requested_at := NULL;
    ELSIF NEW.state = 'failed' AND NEW.cancel_effect IS NULL AND NEW.cancel_dispatched_at IS NULL THEN
        NEW.cancel_cause := NULL;
        NEW.cancel_requested_at := NULL;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER download_tasks_cancel_intent_guard
    BEFORE UPDATE OF state ON download_tasks
    FOR EACH ROW EXECUTE FUNCTION download_task_cancel_intent_guard();

-- Wakes the task's waiting submit or sync item so the worker acts now. An
-- item a running stage holds is skipped: that stage hands over to a successor
-- that sees the intent.
CREATE FUNCTION download_cancel_wake(p_task text) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
UPDATE public.work_items w SET available_at = LEAST(w.available_at, clock_timestamp()), updated_at = clock_timestamp()
WHERE w.id IN (SELECT i.id FROM public.work_items i
    WHERE i.owner_type = 'download_task' AND i.owner_id = p_task AND i.kind IN ('qb_submit', 'qb_sync')
      AND i.state = 'available'
    FOR UPDATE SKIP LOCKED);
$$;

-- 081 + 103: the group's waiting releases are filtered (unchanged), and its
-- pre-import downloads in an active subscription get the cancel intent. The
-- anime row is taken (key share) before the subscription, the competition's
-- order: the wake below writes work items, whose fence takes it too.
CREATE OR REPLACE FUNCTION drop_unselected_awaiting_releases() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE marked text;
BEGIN
    PERFORM 1 FROM public.managed_anime m JOIN public.subscriptions s ON s.managed_anime_id = m.id
    WHERE s.id = OLD.subscription_id FOR KEY SHARE OF m;
    PERFORM 1 FROM public.subscriptions WHERE id = OLD.subscription_id FOR UPDATE;
    PERFORM 1 FROM public.provider_group_selections g
    WHERE g.subscription_id = OLD.subscription_id AND g.provider = OLD.provider
      AND g.group_ref = OLD.group_ref AND g.available AND g.effective_selected;
    IF FOUND THEN RETURN NULL; END IF;
    WITH target AS (
        SELECT r.id, r.competition_retry_key AS retry_key, sub.managed_anime_id
        FROM public.releases r
        JOIN public.rss_sources src ON src.id = r.rss_source_id
        JOIN public.subscriptions sub ON sub.id = src.subscription_id
        WHERE src.subscription_id = OLD.subscription_id AND src.source_kind = 'provider_group'
          AND src.provider = OLD.provider AND src.provider_group_ref = OLD.group_ref
          AND r.verdict = 'awaiting_competition' AND r.download_task_id IS NULL
        FOR UPDATE OF r
    ), withdrawn AS (
        UPDATE public.placements p
        SET redownload_requested_at = NULL, redownload_request_key = NULL,
            version = p.version + 1, updated_at = CURRENT_TIMESTAMP
        FROM target t
        WHERE t.retry_key IS NOT NULL AND p.managed_anime_id = t.managed_anime_id
          AND p.quality_facts_ref = t.id AND p.state = 'active' AND p.redownload_request_key = t.retry_key
    )
    UPDATE public.releases r
    SET verdict = 'filtered', reason_code = 'provider_group_not_selected',
        reason_detail = 'the provider group is no longer selected',
        discovery_notice_due = FALSE, competition_retry_key = NULL,
        version = r.version + 1, updated_at = CURRENT_TIMESTAMP
    FROM target t
    WHERE r.id = t.id;
    -- The group's pre-import downloads get the cancel intent (the rule of
    -- download_cancel_group_due). A task row a running stage holds is skipped
    -- rather than waited for (a site-policy writer holds the anime row the
    -- stage needs next); the worker derives its intent at the next step.
    FOR marked IN
        SELECT t.id FROM public.download_tasks t
        JOIN public.releases r ON r.id = t.release_id
        JOIN public.rss_sources src ON src.id = r.rss_source_id
        JOIN public.subscriptions sub ON sub.id = src.subscription_id
        JOIN public.managed_anime anime ON anime.id = sub.managed_anime_id
        JOIN public.acquisitions a ON a.id = t.acquisition_id
        WHERE src.subscription_id = OLD.subscription_id AND src.source_kind = 'provider_group'
          AND src.provider = OLD.provider AND src.provider_group_ref = OLD.group_ref
          AND sub.state = 'active' AND anime.lifecycle_state = 'active'
          AND a.state = 'running' AND a.record_state = 'full' AND a.entry_kind <> 'one_shot'
          AND t.state IN ('queued', 'submitting', 'waiting_retry', 'downloading')
          AND t.cancel_cause IS NULL
        ORDER BY t.id COLLATE "C"
        FOR UPDATE OF t SKIP LOCKED
    LOOP
        UPDATE public.download_tasks SET cancel_cause = 'provider_group_not_selected',
            cancel_requested_at = clock_timestamp()
        WHERE id = marked;
        PERFORM public.download_cancel_wake(marked);
    END LOOP;
    RETURN NULL;
END;
$$;

-- qb_submit, before an attempt is granted and again before dispatch: a task
-- whose submission never left the process is cancelled with no qB call. A
-- possibly sent submission (a dispatched permit that is not proven
-- retry-safe) keeps its normal flow -- the typed lookup recovery reconciles
-- it first, and the sync worker cancels the task it finds.
CREATE FUNCTION download_cancel_unsent_submission(p_task text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; now_at timestamptz; code text; reason text;
BEGIN
    task := public.download_stage_lock(p_task, p_work, p_token);
    IF NOT EXISTS (SELECT 1 FROM public.work_items WHERE id = p_work AND kind = 'qb_submit') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'CANCEL_SUBMIT_IDENTITY';
    END IF;
    IF task.state IN ('completed', 'failed') THEN
        RETURN jsonb_build_object('applied', false, 'reason', 'CANCEL_NOT_REQUESTED');
    END IF;
    IF task.submit_permit_id IS NOT NULL AND NOT task.submit_retry_safe
        AND task.submit_dispatch_started_at IS NOT NULL THEN
        RETURN jsonb_build_object('applied', false, 'reason', 'CANCEL_AFTER_SUBMIT');
    END IF;
    IF task.cancel_cause IS NULL OR task.cancel_cause = 'provider_group_not_selected' THEN
        IF NOT public.download_cancel_group_due(p_task) THEN
            IF task.cancel_cause IS NULL THEN
                RETURN jsonb_build_object('applied', false, 'reason', 'CANCEL_NOT_REQUESTED');
            END IF;
            UPDATE public.download_tasks SET cancel_cause = NULL, cancel_requested_at = NULL WHERE id = p_task;
            RETURN jsonb_build_object('applied', false, 'reason', 'CANCEL_WITHDRAWN');
        END IF;
        IF task.cancel_cause IS NULL THEN
            UPDATE public.download_tasks SET cancel_cause = 'provider_group_not_selected',
                cancel_requested_at = clock_timestamp()
            WHERE id = p_task RETURNING * INTO task;
        END IF;
    END IF;
    now_at := greatest(clock_timestamp(), task.budget_clock_at);
    code := public.download_cancel_code(task.cancel_cause);
    reason := public.download_cancel_reason(task.cancel_cause, 'not_submitted');
    UPDATE public.download_tasks SET state = 'failed', failure_code = code, failure_reason = reason,
        cancel_effect = 'not_submitted', budget_clock_at = now_at,
        submit_attempt_ticket = CASE WHEN submit_attempt_ticket->>'state' = 'issued'
            THEN jsonb_set(submit_attempt_ticket, '{state}', '"superseded"') ELSE submit_attempt_ticket END,
        submit_attempt_index = COALESCE((SELECT jsonb_agg(CASE WHEN e->>'state' = 'issued'
            THEN jsonb_set(e, '{state}', '"superseded"') ELSE e END ORDER BY ordinal)
            FROM jsonb_array_elements(submit_attempt_index) WITH ORDINALITY AS entries(e, ordinal)), '[]'::jsonb),
        version = version + 1, updated_at = now_at
    WHERE id = p_task RETURNING * INTO task;
    PERFORM public.work_item_finish('failed', task.version, p_work);
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied', true, 'state', 'failed', 'reason_code', code,
        'reason_text', reason, 'task_version', task.version);
END;
$$;

-- qb_sync, before the poll gate. The cancel ticket is separate from the poll
-- tickets: it spends no poll and ignores the poll interval and wait window,
-- so a woken item can act at once. Any reply without granted=true and
-- reason CANCEL_WITHDRAWN tells the caller to run the normal poll gate in the
-- same transaction.
CREATE FUNCTION download_cancel_start(p_task text, p_work text, p_token bigint, p_ordinal integer, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; now_at timestamptz; ticket jsonb; cancel boolean;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE; inherited jsonb; next_work text;
BEGIN
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id = p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions WHERE id = one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch = (one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_stage_lock(p_task, p_work, p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id = p_work) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_RECLAIMING';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.work_items WHERE id = p_work AND kind = 'qb_sync')
        OR p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'SYNC_POLL_IDENTITY';
    END IF;
    IF task.state IN ('completed', 'failed')
        OR task.active_poll_ticket->>'state' = 'issued'
        OR p_ordinal IS DISTINCT FROM task.next_poll_ordinal
        OR NOT EXISTS (SELECT 1 FROM public.work_items WHERE id = p_work
            AND (input_snapshot->>'poll_ordinal')::integer = p_ordinal) THEN
        RETURN jsonb_build_object('granted', false, 'reason', 'CANCEL_NOT_REQUESTED');
    END IF;
    now_at := greatest(clock_timestamp(), task.budget_clock_at);
    cancel := task.cancel_cause IS NOT DISTINCT FROM 'user_cancelled' OR task.cancel_dispatched_at IS NOT NULL
        OR public.download_cancel_group_due(p_task);
    IF task.cancel_cause IS NULL THEN
        IF NOT cancel THEN
            RETURN jsonb_build_object('granted', false, 'reason', 'CANCEL_NOT_REQUESTED');
        END IF;
        -- A task the deselect trigger skipped (its row was held by a stage).
        UPDATE public.download_tasks SET cancel_cause = 'provider_group_not_selected', cancel_requested_at = now_at
        WHERE id = p_task RETURNING * INTO task;
    END IF;
    IF NOT cancel THEN
        -- The group was selected again before any delete: the download goes on.
        UPDATE public.download_tasks SET cancel_cause = NULL, cancel_requested_at = NULL WHERE id = p_task;
        IF task.active_poll_ticket IS NOT NULL AND now_at <
            (task.active_poll_ticket->>'started_at')::timestamptz + task.poll_interval_seconds * INTERVAL '1 second' THEN
            -- The wake ran this item before its poll was due; a successor takes
            -- the poll at the due time.
            SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot', input_snapshot->'one_shot')
                ELSE '{}'::jsonb END INTO inherited FROM public.work_items WHERE id = p_work;
            PERFORM public.work_item_finish('succeeded', task.version, p_work);
            SELECT public.work_item_create(gen_random_uuid()::text, 'download_task', p_task, 'qb_sync', task.idempotency_key, 5,
                jsonb_build_object('trigger', 'qb_sync_progress', 'poll_ordinal', task.next_poll_ordinal) || inherited,
                task.version, greatest(0, extract(epoch FROM (task.active_poll_ticket->>'started_at')::timestamptz
                    + task.poll_interval_seconds * INTERVAL '1 second' - now_at))::double precision,
                7::smallint) INTO next_work;
            IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'SYNC_SUCCESSOR_CONFLICT'; END IF;
            RETURN jsonb_build_object('granted', false, 'reason', 'CANCEL_WITHDRAWN', 'sync_work_id', next_work);
        END IF;
        RETURN jsonb_build_object('granted', false, 'reason', 'CANCEL_WITHDRAWN_DUE');
    END IF;
    ticket := jsonb_build_object('work_item_id', p_work, 'token', p_token, 'executor_session', p_session,
        'started_at', now_at, 'attempt_until', now_at + task.sync_attempt_timeout_seconds * INTERVAL '1 second',
        'state', 'issued');
    UPDATE public.download_tasks SET cancel_dispatched_at = COALESCE(cancel_dispatched_at, now_at),
        cancel_ticket = ticket, budget_clock_at = now_at WHERE id = p_task;
    UPDATE public.work_items SET input_snapshot = jsonb_set(input_snapshot, '{cancel_ticket}', ticket) WHERE id = p_work;
    RETURN jsonb_build_object('granted', true, 'cancel', true, 'cause', task.cancel_cause, 'ticket', ticket,
        'gate_db_now', now_at);
END;
$$;

-- qb_sync, after the delete attempt. p_effect is what the attempt did:
-- deleted_with_data, absent (qB had no such task), detached (the ownership
-- rule kept the qB task) or retry (a transient failure, spent from the error
-- budget like a failed poll). The cause is read again here, so a user cancel
-- that arrived during the delete decides the code.
CREATE FUNCTION download_finish_cancel(p_task text, p_work text, p_token bigint, p_session text, p_effect text, p_next_id text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz; next_version bigint;
    code text; reason text; inherited jsonb; next_work text; exhausted boolean;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    IF EXISTS (SELECT 1 FROM public.work_item_outcomes WHERE id = p_work AND fencing_token = p_token
        AND owner_type = 'download_task' AND owner_id = p_task AND kind = 'qb_sync') THEN
        SELECT input_snapshot->'cancel_ticket' INTO ticket FROM public.work_item_outcomes WHERE id = p_work;
        IF (ticket->>'work_item_id' = p_work AND (ticket->>'token')::bigint = p_token
            AND ticket->>'executor_session' = p_session) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'CANCEL_FINISH_IDENTITY';
        END IF;
        RETURN jsonb_build_object('applied', false, 'reason', 'CANCEL_ALREADY_FINISHED');
    END IF;
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id = p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions WHERE id = one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch = (one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_stage_lock(p_task, p_work, p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id = p_work) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ONE_SHOT_RECLAIMING';
    END IF;
    ticket := task.cancel_ticket;
    now_at := greatest(clock_timestamp(), task.budget_clock_at);
    IF NOT EXISTS (SELECT 1 FROM public.work_items WHERE id = p_work AND kind = 'qb_sync')
        OR (ticket->>'work_item_id' = p_work AND (ticket->>'token')::bigint = p_token
            AND ticket->>'executor_session' = p_session AND ticket->>'state' = 'issued') IS NOT TRUE
        OR (p_effect IN ('deleted_with_data', 'absent', 'detached', 'retry')) IS NOT TRUE
        OR p_next_id IS NULL OR task.cancel_cause IS NULL OR task.state IN ('completed', 'failed') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'CANCEL_FINISH_IDENTITY';
    END IF;
    IF p_effect = 'retry' THEN
        exhausted := task.consecutive_errors + 1 >= task.max_consecutive_errors
            OR task.total_errors + 1 >= task.max_total_errors;
        UPDATE public.download_tasks SET cancel_ticket = jsonb_set(ticket, '{state}', '"completed"'),
            consecutive_errors = consecutive_errors + 1, total_errors = total_errors + 1,
            state = CASE WHEN exhausted THEN 'failed' ELSE state END,
            failure_code = CASE WHEN exhausted THEN 'SYNC_ERROR_BUDGET_EXHAUSTED' ELSE failure_code END,
            failure_reason = CASE WHEN exhausted
                THEN '多次尝试在 qBittorrent 中删除这个已取消的下载均未成功，已停止重试。' ELSE failure_reason END,
            budget_clock_at = now_at, version = version + 1, updated_at = now_at
        WHERE id = p_task RETURNING version INTO next_version;
        PERFORM public.work_item_finish('failed', next_version, p_work);
        IF NOT exhausted THEN
            SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot', input_snapshot->'one_shot')
                ELSE '{}'::jsonb END INTO inherited FROM public.work_item_outcomes WHERE id = p_work;
            SELECT public.work_item_create(p_next_id, 'download_task', p_task, 'qb_sync', task.idempotency_key, 5,
                jsonb_build_object('trigger', 'qb_sync_cancel_retry', 'poll_ordinal', task.next_poll_ordinal) || inherited,
                next_version, task.poll_interval_seconds::double precision, 7::smallint) INTO next_work;
            IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'SYNC_SUCCESSOR_CONFLICT'; END IF;
        END IF;
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('applied', true, 'task_version', next_version,
            'state', CASE WHEN exhausted THEN 'failed' ELSE task.state END,
            'reason_code', CASE WHEN exhausted THEN 'SYNC_ERROR_BUDGET_EXHAUSTED' ELSE 'qb_cancel_retryable' END,
            'reason_text', CASE WHEN exhausted
                THEN '多次尝试在 qBittorrent 中删除这个已取消的下载均未成功，已停止重试。' ELSE '' END);
    END IF;
    code := public.download_cancel_code(task.cancel_cause);
    reason := public.download_cancel_reason(task.cancel_cause, p_effect);
    UPDATE public.download_tasks SET state = 'failed', failure_code = code, failure_reason = reason,
        cancel_effect = p_effect, cancel_ticket = jsonb_set(ticket, '{state}', '"completed"'),
        budget_clock_at = now_at, version = version + 1, updated_at = now_at
    WHERE id = p_task RETURNING version INTO next_version;
    PERFORM public.work_item_finish('failed', next_version, p_work);
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied', true, 'task_version', next_version, 'state', 'failed',
        'reason_code', code, 'reason_text', reason);
END;
$$;

-- The 001 recovery of an interrupted qb_sync, plus 103: an interrupted cancel
-- attempt. The delete may or may not have run; the successor looks the task
-- up again (an absent task settles as such), so recovery only hands over.
CREATE OR REPLACE FUNCTION download_recover_sync(p_task text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz;
    next_version bigint; next_work text; inherited jsonb; exhausted boolean; cancelled boolean;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_recovery_lock(p_task,p_work,p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id=p_work) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    SELECT cancel_requested INTO cancelled FROM public.work_items WHERE id=p_work AND kind='qb_sync';
    IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_RECOVERY_IDENTITY'; END IF;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF (task.cancel_ticket->>'work_item_id'=p_work AND (task.cancel_ticket->>'token')::bigint<p_token
        AND task.cancel_ticket->>'state'='issued') IS TRUE THEN
        IF task.state IN ('completed','failed') OR cancelled THEN
            UPDATE public.download_tasks SET cancel_ticket=jsonb_set(cancel_ticket,'{state}','"superseded"') WHERE id=p_task;
            PERFORM public.work_item_finish_recovery_observation(p_work,'failed',task.version);
            RETURN jsonb_build_object('applied',true,'reason','SYNC_RECOVERY_CLOSED','state',task.state);
        END IF;
        UPDATE public.download_tasks SET cancel_ticket=jsonb_set(cancel_ticket,'{state}','"superseded"'),
            budget_clock_at=now_at,version=version+1,updated_at=now_at
            WHERE id=p_task RETURNING version INTO next_version;
        PERFORM public.work_item_finish_recovery_observation(p_work,'failed',next_version);
        SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',input_snapshot->'one_shot')
            ELSE '{}'::jsonb END INTO inherited FROM public.work_item_outcomes WHERE id=p_work;
        SELECT public.work_item_create(gen_random_uuid()::text,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_cancel_retry','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,0::double precision,7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('applied',true,'reason','SYNC_CANCEL_INTERRUPTED','task_version',next_version);
    END IF;
    ticket := task.active_poll_ticket;
    IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint<p_token
        AND ticket->>'state'='issued') IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_RECOVERY_IDENTITY';
    END IF;
    IF task.state IN ('completed','failed') OR cancelled THEN
        UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"') WHERE id=p_task;
        PERFORM public.work_item_finish_recovery_observation(p_work,
            CASE WHEN task.state='completed' AND NOT cancelled THEN 'succeeded' ELSE 'failed' END,task.version);
        RETURN jsonb_build_object('applied',true,'reason','SYNC_RECOVERY_CLOSED','state',task.state);
    END IF;
    exhausted := now_at>=task.wait_until OR task.polls_started>=task.max_polls
        OR task.consecutive_errors+1>=task.max_consecutive_errors OR task.total_errors+1>=task.max_total_errors;
    UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"'),
        consecutive_errors=consecutive_errors+1,total_errors=total_errors+1,
        budget_clock_at=now_at,version=version+1,updated_at=now_at,
        state=CASE WHEN exhausted THEN 'failed' ELSE state END,
        failure_code=CASE WHEN exhausted THEN 'SYNC_ERROR_BUDGET_EXHAUSTED' ELSE failure_code END,
        failure_reason=CASE WHEN exhausted THEN 'qB observation budget exhausted after interrupted poll' ELSE failure_reason END
        WHERE id=p_task RETURNING version INTO next_version;
    PERFORM public.work_item_finish_recovery_observation(p_work,'failed',next_version);
    IF NOT exhausted THEN
        SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',input_snapshot->'one_shot')
            ELSE '{}'::jsonb END INTO inherited FROM public.work_item_outcomes WHERE id=p_work;
        SELECT public.work_item_create(gen_random_uuid()::text,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_progress','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,greatest(0,extract(epoch FROM
                (ticket->>'started_at')::timestamptz+task.poll_interval_seconds*INTERVAL '1 second'-now_at))::double precision,
            7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
    END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'reason','SYNC_INTERRUPTED','task_version',next_version);
END;
$$;

-- 035 + 103: the settler cancels a download deleted in the downloader or
-- cancelled before import, and only when the task row really ended that way;
-- every other cancellation is the retirement disposition's.
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
 IF outcome='cancelled' AND (p_receipt->'primary_failure'->>'reason_code' IN
     ('DOWNLOADER_TASK_REMOVED','PROVIDER_GROUP_NOT_ALLOWED','USER_CANCELLED')
   AND p_receipt->'primary_failure'->>'stage' IN ('submit','download_wait')
   AND EXISTS(SELECT 1 FROM public.download_tasks t WHERE t.acquisition_id=p_root
     AND t.state='failed' AND t.failure_code=p_receipt->'primary_failure'->>'reason_code')) IS NOT TRUE THEN
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

-- A cancelled download is not a failure: no download_failed notification.
DROP TRIGGER download_task_failed_notification ON download_tasks;
CREATE TRIGGER download_task_failed_notification
    AFTER UPDATE OF state ON download_tasks
    FOR EACH ROW WHEN (((old.state IS DISTINCT FROM 'failed'::text) AND (new.state = 'failed'::text)
        AND (new.failure_code IS NULL OR new.failure_code NOT IN
            ('DOWNLOADER_TASK_REMOVED'::text, 'PROVIDER_GROUP_NOT_ALLOWED'::text, 'USER_CANCELLED'::text))))
    EXECUTE FUNCTION download_task_failed_admission();

-- The selection's entry, symmetric to 081's exit: the releases of one
-- subscription's provider group that left with the selection
-- (filtered/provider_group_not_selected, no download task: 081's dropped
-- waiters and the reset selection cancels) are evaluated again from their
-- stored facts when the group is available and selected, the subscription
-- active and the anime active. Nothing is fetched; the evaluation is the
-- automatic one (start_baseline gate, filters, then the competition), merged
-- into the release's one release_evaluate item like
-- CreateOrMergeReleaseEvaluation. p_release narrows it to one release. The
-- caller holds the subscription row (anime key share first).
CREATE FUNCTION release_reopen_selected_group(p_subscription text, p_provider text, p_group text, p_release text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE target record; scheduled integer := 0;
BEGIN
    PERFORM 1 FROM public.provider_group_selections g
    JOIN public.subscriptions sub ON sub.id = g.subscription_id
    JOIN public.managed_anime anime ON anime.id = sub.managed_anime_id
    WHERE g.subscription_id = p_subscription AND g.provider = p_provider AND g.group_ref = p_group
      AND g.available AND g.effective_selected AND sub.state = 'active' AND anime.lifecycle_state = 'active';
    IF NOT FOUND THEN RETURN 0; END IF;
    FOR target IN
        SELECT r.id, r.version FROM public.releases r
        JOIN public.rss_sources src ON src.id = r.rss_source_id
        WHERE src.subscription_id = p_subscription AND src.source_kind = 'provider_group'
          AND src.provider = p_provider AND src.provider_group_ref = p_group
          AND r.verdict = 'filtered' AND r.reason_code = 'provider_group_not_selected'
          AND r.download_task_id IS NULL AND (p_release IS NULL OR r.id = p_release)
        ORDER BY r.id COLLATE "C"
        FOR UPDATE OF r
    LOOP
        PERFORM public.work_item_merge(gen_random_uuid()::text, 'release', target.id, 'release_evaluate',
            'release_evaluate:' || target.id, 5, '{"trigger":"group_selected"}'::jsonb, target.version,
            0::double precision, 7::smallint);
        scheduled := scheduled + 1;
    END LOOP;
    RETURN scheduled;
END;
$$;

-- Deferred to commit and re-read like 081's exit: only the committed
-- selection counts, so a group made unavailable and restored inside one
-- transaction schedules nothing new. Lock order: anime (key share),
-- subscription, releases (the competition's).
CREATE FUNCTION reopen_selected_group_releases() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    PERFORM 1 FROM public.managed_anime m JOIN public.subscriptions s ON s.managed_anime_id = m.id
    WHERE s.id = NEW.subscription_id FOR KEY SHARE OF m;
    PERFORM 1 FROM public.subscriptions WHERE id = NEW.subscription_id FOR UPDATE;
    PERFORM public.release_reopen_selected_group(NEW.subscription_id, NEW.provider, NEW.group_ref, NULL);
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER provider_group_selections_reopen_update
    AFTER UPDATE OF available, effective_selected ON provider_group_selections
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NOT (old.available AND old.effective_selected) AND new.available AND new.effective_selected)
    EXECUTE FUNCTION reopen_selected_group_releases();

CREATE CONSTRAINT TRIGGER provider_group_selections_reopen_insert
    AFTER INSERT ON provider_group_selections
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (new.available AND new.effective_selected)
    EXECUTE FUNCTION reopen_selected_group_releases();

-- A subscription that becomes active is an entry for every group available
-- and selected at commit: a group selected again while the subscription was
-- stopped (the reopen above skipped it) has its dropped releases evaluated at
-- the resume. A pending subscription has no releases yet. Deferred and
-- re-read like the selection triggers; same lock order.
CREATE FUNCTION reopen_resumed_subscription_releases() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE g record;
BEGIN
    PERFORM 1 FROM public.managed_anime m JOIN public.subscriptions s ON s.managed_anime_id = m.id
    WHERE s.id = NEW.id FOR KEY SHARE OF m;
    PERFORM 1 FROM public.subscriptions WHERE id = NEW.id FOR UPDATE;
    FOR g IN
        SELECT sel.provider, sel.group_ref FROM public.provider_group_selections sel
        WHERE sel.subscription_id = NEW.id AND sel.available AND sel.effective_selected
        ORDER BY sel.provider COLLATE "C", sel.group_ref COLLATE "C"
    LOOP
        PERFORM public.release_reopen_selected_group(NEW.id, g.provider, g.group_ref, NULL);
    END LOOP;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER subscriptions_resume_reopen
    AFTER UPDATE OF state ON subscriptions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (old.state <> 'active' AND new.state = 'active')
    EXECUTE FUNCTION reopen_resumed_subscription_releases();

-- The releases a group already selected again holds in that state (081's
-- waiters dropped before this migration) are evaluated once now.
DO $$
DECLARE g record;
BEGIN
    FOR g IN
        SELECT DISTINCT sel.subscription_id, sel.provider, sel.group_ref
        FROM public.provider_group_selections sel
        JOIN public.rss_sources src ON src.subscription_id = sel.subscription_id AND src.source_kind = 'provider_group'
            AND src.provider = sel.provider AND src.provider_group_ref = sel.group_ref
        JOIN public.releases r ON r.rss_source_id = src.id
        WHERE sel.available AND sel.effective_selected
          AND r.verdict = 'filtered' AND r.reason_code = 'provider_group_not_selected' AND r.download_task_id IS NULL
        ORDER BY 1, 2, 3
    LOOP
        PERFORM public.release_reopen_selected_group(g.subscription_id, g.provider, g.group_ref, NULL);
    END LOOP;
END;
$$;

-- A selection cancel's cause is the selection: once the root settles, its
-- Release leaves the cancelled task (kept as history) and returns to
-- filtered/provider_group_not_selected, the state 081 gives the group's
-- waiting releases. A group selected again after the delete was dispatched
-- (the entry rule above ran before this reset) reopens it here: the
-- subscription row is taken first, so the reset and a reselect's commit
-- serialize and one of the two sees the other. A user cancel's cause is the
-- Release itself: it keeps its task until a retry.
CREATE FUNCTION acquisition_cancel_reopen_release() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; source public.rss_sources%ROWTYPE;
BEGIN
    SELECT * INTO task FROM public.download_tasks WHERE acquisition_id = NEW.id;
    IF NOT FOUND OR task.state <> 'failed' OR task.failure_code IS DISTINCT FROM 'PROVIDER_GROUP_NOT_ALLOWED' THEN
        RETURN NULL;
    END IF;
    SELECT src.* INTO source FROM public.releases r JOIN public.rss_sources src ON src.id = r.rss_source_id
    WHERE r.id = task.release_id;
    PERFORM 1 FROM public.managed_anime m JOIN public.subscriptions s ON s.managed_anime_id = m.id
    WHERE s.id = source.subscription_id FOR KEY SHARE OF m;
    PERFORM 1 FROM public.subscriptions WHERE id = source.subscription_id FOR UPDATE;
    WITH target AS (
        SELECT r.id, r.competition_retry_key AS retry_key, sub.managed_anime_id
        FROM public.releases r
        JOIN public.rss_sources src ON src.id = r.rss_source_id
        JOIN public.subscriptions sub ON sub.id = src.subscription_id
        WHERE r.id = task.release_id AND r.download_task_id = task.id
        FOR UPDATE OF r
    ), withdrawn AS (
        UPDATE public.placements p
        SET redownload_requested_at = NULL, redownload_request_key = NULL,
            version = p.version + 1, updated_at = CURRENT_TIMESTAMP
        FROM target t
        WHERE t.retry_key IS NOT NULL AND p.managed_anime_id = t.managed_anime_id
          AND p.quality_facts_ref = t.id AND p.state = 'active' AND p.redownload_request_key = t.retry_key
    )
    UPDATE public.releases r
    SET download_task_id = NULL, verdict = 'filtered', reason_code = 'provider_group_not_selected',
        reason_detail = 'the provider group is no longer selected',
        discovery_notice_due = FALSE, competition_retry_key = NULL,
        version = r.version + 1, updated_at = CURRENT_TIMESTAMP
    FROM target t
    WHERE r.id = t.id;
    PERFORM public.release_reopen_selected_group(source.subscription_id, source.provider,
        source.provider_group_ref, task.release_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER acquisitions_cancel_reopen_release
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN (((old.state = 'running'::text) AND (new.state = 'cancelled'::text)))
    EXECUTE FUNCTION acquisition_cancel_reopen_release();

-- The user cancel of one execution record: the request replay identity.
CREATE TABLE acquisition_cancel_requests (
    acquisition_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_version BIGINT NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT acquisition_cancel_requests_pkey PRIMARY KEY (acquisition_id),
    CONSTRAINT acquisition_cancel_requests_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT acquisition_cancel_requests_key_check CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
    CONSTRAINT acquisition_cancel_requests_hash_check CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisition_cancel_requests_version_check CHECK (result_version >= 1)
);
COMMENT ON TABLE acquisition_cancel_requests IS 'The accepted user cancel of an acquisition (at most one) and its request replay identity; written only by acquisition_cancel (definer).';
-- History of the root like its import retries: a root is never deleted, so no
-- foreign key cascades.
REVOKE ALL ON TABLE acquisition_cancel_requests FROM bansoko_runtime;
GRANT SELECT ON TABLE acquisition_cancel_requests TO bansoko_runtime;

-- Why the record cannot be cancelled now, or NULL when it can: a running full
-- download record whose task has not reached import.
CREATE FUNCTION acquisition_cancel_refusal(p_root text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; task public.download_tasks%ROWTYPE; v_owner text; lifecycle text;
BEGIN
    SELECT * INTO r FROM public.acquisitions WHERE id = p_root;
    IF NOT FOUND THEN RETURN 'DOWNLOAD_CANCEL_NOT_FOUND'; END IF;
    IF r.record_state <> 'full' OR r.state <> 'running' OR r.execution_branch <> 'download' THEN
        RETURN 'DOWNLOAD_CANCEL_NOT_APPLICABLE';
    END IF;
    v_owner := public.acquisition_owner(p_root);
    IF v_owner IS NOT NULL THEN
        SELECT l.lifecycle_state INTO lifecycle FROM public.managed_anime l WHERE l.id = v_owner;
        IF lifecycle IS DISTINCT FROM 'active' THEN RETURN 'MANAGED_ANIME_RETIRED'; END IF;
    END IF;
    SELECT * INTO task FROM public.download_tasks WHERE acquisition_id = p_root;
    IF NOT FOUND OR task.state NOT IN ('queued', 'submitting', 'waiting_retry', 'downloading') THEN
        RETURN 'DOWNLOAD_CANCEL_NOT_APPLICABLE';
    END IF;
    IF task.cancel_cause = 'user_cancelled' THEN RETURN 'DOWNLOAD_CANCEL_ALREADY_REQUESTED'; END IF;
    RETURN NULL;
END;
$$;

-- Records the user cancel intent of one record and wakes its download work.
-- The intent outranks a selection cancel not yet settled. The worker decides
-- the effect; a completion that wins the race keeps the record succeeding.
CREATE FUNCTION acquisition_cancel(p_root text, p_expected_version bigint, p_key text, p_hash text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; prior public.acquisition_cancel_requests%ROWTYPE;
    task public.download_tasks%ROWTYPE; refusal text; new_version bigint;
    now_at timestamptz := clock_timestamp();
BEGIN
    IF p_key IS NULL OR char_length(p_key) NOT BETWEEN 8 AND 128 OR p_hash IS NULL
        OR p_hash !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_CANCEL_SHAPE';
    END IF;
    SELECT * INTO r FROM public.acquisitions WHERE id = p_root;
    IF NOT FOUND THEN RETURN jsonb_build_object('status', 'not_found'); END IF;
    IF r.entry_kind = 'one_shot' THEN
        PERFORM 1 FROM public.one_shot_acquisitions WHERE request_idempotency_key = r.request_key FOR UPDATE;
    END IF;
    -- The stage order (marker, root, task); the managed anime is not locked
    -- here because a stage holding the task fences its work items on it.
    SELECT * INTO STRICT r FROM public.acquisitions WHERE id = p_root FOR UPDATE;
    SELECT * INTO task FROM public.download_tasks WHERE acquisition_id = p_root FOR UPDATE;

    SELECT * INTO prior FROM public.acquisition_cancel_requests WHERE acquisition_id = p_root;
    IF FOUND THEN
        IF prior.idempotency_key = p_key THEN
            IF prior.request_hash <> p_hash THEN RETURN jsonb_build_object('status', 'key_reused'); END IF;
            RETURN jsonb_build_object('status', 'accepted', 'replayed', true, 'version', prior.result_version);
        END IF;
    END IF;

    refusal := public.acquisition_cancel_refusal(p_root);
    IF refusal IS NULL AND prior.acquisition_id IS NOT NULL THEN
        -- One accepted cancel per record: a cancel that lost to completion
        -- leaves nothing to cancel again.
        refusal := 'DOWNLOAD_CANCEL_ALREADY_REQUESTED';
    END IF;
    IF refusal IS NOT NULL THEN
        RETURN jsonb_build_object('status', 'refused', 'code', refusal, 'version', r.version);
    END IF;
    IF p_expected_version IS DISTINCT FROM r.version THEN
        RETURN jsonb_build_object('status', 'version_conflict', 'version', r.version);
    END IF;

    UPDATE public.download_tasks SET cancel_cause = 'user_cancelled', cancel_requested_at = now_at
    WHERE id = task.id;
    UPDATE public.acquisitions SET version = version + 1 WHERE id = p_root RETURNING version INTO new_version;
    -- The settle item follows the root version.
    PERFORM public.download_ensure_settlement(p_root);
    PERFORM public.download_cancel_wake(task.id);
    INSERT INTO public.acquisition_cancel_requests(acquisition_id, idempotency_key, request_hash, result_version, accepted_at)
    VALUES (p_root, p_key, p_hash, new_version, now_at);
    RETURN jsonb_build_object('status', 'accepted', 'replayed', false, 'version', new_version);
END;
$$;

REVOKE ALL ON FUNCTION download_cancel_code(text), download_cancel_reason(text, text),
    download_cancel_group_due(text), download_task_cancel_intent_guard(), download_cancel_wake(text),
    download_cancel_unsent_submission(text, text, bigint),
    download_cancel_start(text, text, bigint, integer, text),
    download_finish_cancel(text, text, bigint, text, text, text),
    release_reopen_selected_group(text, text, text, text), reopen_selected_group_releases(),
    reopen_resumed_subscription_releases(),
    acquisition_cancel_reopen_release(), acquisition_cancel_refusal(text),
    acquisition_cancel(text, bigint, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION download_cancel_code(text), download_cancel_reason(text, text),
    download_cancel_group_due(text), download_cancel_unsent_submission(text, text, bigint),
    download_cancel_start(text, text, bigint, integer, text),
    download_finish_cancel(text, text, bigint, text, text, text),
    acquisition_cancel_refusal(text), acquisition_cancel(text, bigint, text, text) TO bansoko_runtime;
