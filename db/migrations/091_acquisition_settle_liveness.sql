-- Forward-only migration 091: a running acquisition root can no longer be left
-- with nothing that will settle it.
--
-- T1 settlement decides from the root's current facts (domain.Decide), but it
-- runs only while the root has a due acquisition_settle work item. That item
-- was armed by an explicit download_ensure_settlement at each stage transition
-- and consumed by every defer, so a stage whose last live work ended without
-- the explicit call left the root running with no live work and no settle
-- item, and nothing ever looked at it again (a placement continuation that
-- finished after the file completion's settle had been deferred is one such
-- path). The re-arm now sits where every piece of root work leaves:
--
--   * work_items_settle_liveness: when a non-settle work item of a running root
--     leaves work_items (work_item_close or any other delete), its transaction
--     wakes the root's settle item at commit (acquisition_settle_wake). No
--     stage has to remember the call; the explicit calls stay as harmless
--     early wakes.
--   * The same trigger on a settle item, and acquisitions_settle_liveness on a
--     root that is (again) running: a running root that holds no work item at
--     commit, neither stage work nor its settle item, gets a due settle item
--     (acquisition_settle_keep_live). The guarantee does not depend on the
--     order the deferred triggers of one transaction run in.
--   * acquisition_settle_defer still consumes the settle item when stage work
--     is live (that work's exit wakes it again). A root whose execution graph
--     is incomplete has nothing live to wake it, so its settle item is kept and
--     re-checked with a growing delay (1, 4, 16, 64, 256 minutes); the root
--     stays visibly blocked (settlement_blocked_code) until a decision lands,
--     and any work exit wakes it at once. A defer on live work clears the
--     block.
--   * Every running root without a settle item (left by the old
--     consume-on-defer) gets one now and is re-decided on the next pass.

-- The wake shared by every arming path: bump the facts revision (a decision
-- made on the old facts becomes stale) and make the root's settle item due,
-- creating it when the root has none. A root that is not running is left alone.
CREATE FUNCTION acquisition_settle_wake(p_root text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE root public.acquisitions%ROWTYPE;
BEGIN
    UPDATE public.acquisitions SET facts_revision=facts_revision+1
        WHERE id=p_root AND state='running' AND record_state='full' RETURNING * INTO root;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE public.work_items SET available_at=LEAST(available_at,clock_timestamp()),
        expected_owner_version=root.version,updated_at=clock_timestamp()
        WHERE acquisition_id=p_root AND owner_type='acquisition' AND kind='acquisition_settle'
        AND state='available';
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE acquisition_id=p_root
        AND owner_type='acquisition' AND kind='acquisition_settle' AND state IN ('available','leased')) THEN
        INSERT INTO public.work_items(id,owner_type,owner_id,acquisition_id,kind,idempotency_key,
            max_attempts,input_snapshot,expected_owner_version)
        VALUES(gen_random_uuid()::text,'acquisition',p_root,p_root,'acquisition_settle',
            'acquisition_settle:'||p_root||':'||root.facts_revision::text,5,
            '{"schema_version":1}'::jsonb,root.version);
    END IF;
    RETURN true;
END;
$$;

CREATE OR REPLACE FUNCTION download_ensure_settlement(p_root text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NOT public.acquisition_settle_wake(p_root) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_SETTLEMENT_ROOT';
    END IF;
END;
$$;

-- A running root holds at least one work item: stage work whose exit wakes
-- settlement, or the settle item itself. One left with neither gets a due
-- settle item.
CREATE FUNCTION acquisition_settle_keep_live(p_root text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM public.acquisitions WHERE id=p_root AND state='running' AND record_state='full')
        AND NOT EXISTS(SELECT 1 FROM public.work_items WHERE acquisition_id=p_root) THEN
        PERFORM public.acquisition_settle_wake(p_root);
    END IF;
END;
$$;

CREATE FUNCTION acquisition_settle_on_work_exit() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF OLD.kind='acquisition_settle' THEN
        PERFORM public.acquisition_settle_keep_live(OLD.acquisition_id);
    ELSE
        PERFORM public.acquisition_settle_wake(OLD.acquisition_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION acquisition_settle_on_root_running() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    PERFORM public.acquisition_settle_keep_live(NEW.id);
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER work_items_settle_liveness
    AFTER DELETE ON work_items
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (old.acquisition_id IS NOT NULL)
    EXECUTE FUNCTION acquisition_settle_on_work_exit();

-- Acceptance inserts a root together with its submit and settle items in one
-- function; a root that becomes running again (an import-stage retry) is the
-- transition to check.
CREATE CONSTRAINT TRIGGER acquisitions_settle_liveness
    AFTER UPDATE OF state, record_state ON acquisitions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (new.state = 'running' AND new.record_state = 'full')
    EXECUTE FUNCTION acquisition_settle_on_root_running();

CREATE OR REPLACE FUNCTION acquisition_settle_defer(p_root text, p_facts_revision bigint, p_blocked_code text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; errors integer;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND OR r.state<>'running' OR r.record_state<>'full' THEN RETURN jsonb_build_object('status','not_running'); END IF;
 IF r.facts_revision<>p_facts_revision THEN RETURN jsonb_build_object('status','stale'); END IF;
 IF p_blocked_code IS NOT NULL AND p_blocked_code<>'EXECUTION_GRAPH_INCOMPLETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_DEFER_CODE'; END IF;
 IF p_blocked_code IS NULL THEN
  -- Live stage work: its exit wakes the root again (work_items_settle_liveness).
  IF r.settlement_blocked_code IS NOT NULL OR r.settlement_error_count<>0 THEN
   UPDATE public.acquisitions SET settlement_blocked_code=NULL,settlement_error_count=0 WHERE id=p_root;
  END IF;
  PERFORM public.acquisition_consume_settle_work(p_root);
  RETURN jsonb_build_object('status','deferred');
 END IF;
 -- Nothing live will wake an incomplete graph: keep the settle item and look
 -- again later.
 UPDATE public.acquisitions SET settlement_blocked_code=p_blocked_code,
   settlement_error_count=LEAST(settlement_error_count+1,5) WHERE id=p_root
   RETURNING settlement_error_count INTO errors;
 PERFORM public.acquisition_settle_wake(p_root);
 UPDATE public.work_items SET available_at=clock_timestamp()+INTERVAL '1 minute'*power(4,errors-1),
   updated_at=clock_timestamp()
  WHERE acquisition_id=p_root AND owner_type='acquisition' AND kind='acquisition_settle' AND state='available';
 RETURN jsonb_build_object('status','deferred');
END $$;

REVOKE ALL ON FUNCTION acquisition_settle_wake(text), acquisition_settle_keep_live(text),
    acquisition_settle_on_work_exit(), acquisition_settle_on_root_running() FROM PUBLIC;

SELECT public.acquisition_settle_wake(r.id)
FROM public.acquisitions r
WHERE r.state='running' AND r.record_state='full'
  AND NOT EXISTS(SELECT 1 FROM public.work_items w WHERE w.acquisition_id=r.id
      AND w.owner_type='acquisition' AND w.kind='acquisition_settle')
ORDER BY r.id;
