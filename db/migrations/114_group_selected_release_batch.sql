-- Forward-only migration 114: the group-selected bulk reopen is a release batch.
--
-- release_reopen_selected_group (103) schedules one release_evaluate item per
-- filtered release of a group that re-enters the selection. Reopening a whole
-- backlog is the same shape as evaluating a first feed window: at the refresh
-- path's threshold (3, businesslog.ReleaseBatchThreshold) it writes one
-- release_batch_processed head and stamps every scheduled evaluation with the
-- batch key, so the timeline keeps one row and the read projection aggregates
-- the per-release verdicts behind it.
--
-- The batch is written here rather than by the Go writer because the reopen
-- runs from the provider_group_selections deferred triggers at commit: only
-- SQL sees the final selection state and the count in the same transaction.
-- The head mirrors businesslog.WriteReleaseBatchTx field for field.

CREATE OR REPLACE FUNCTION release_reopen_selected_group(p_subscription text, p_provider text, p_group text, p_release text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    target record; v_source_id text; v_batch_key text;
    v_candidates integer := 0; scheduled integer := 0;
BEGIN
    PERFORM 1 FROM public.provider_group_selections g
    JOIN public.subscriptions sub ON sub.id = g.subscription_id
    JOIN public.managed_anime anime ON anime.id = sub.managed_anime_id
    WHERE g.subscription_id = p_subscription AND g.provider = p_provider AND g.group_ref = p_group
      AND g.available AND g.effective_selected AND sub.state = 'active' AND anime.lifecycle_state = 'active';
    IF NOT FOUND THEN RETURN 0; END IF;

    -- Lock the candidates before counting them: the count alone decides
    -- whether the reopen becomes a batch, so no locked row can leave the set
    -- between the count and the loop below. A row that enters the set later
    -- only makes scheduled larger, which the head then records truthfully;
    -- when the count stays under the threshold nothing is stamped.
    PERFORM 1 FROM public.releases r
    JOIN public.rss_sources src ON src.id = r.rss_source_id
    WHERE src.subscription_id = p_subscription AND src.source_kind = 'provider_group'
      AND src.provider = p_provider AND src.provider_group_ref = p_group
      AND r.verdict = 'filtered' AND r.reason_code = 'provider_group_not_selected'
      AND r.download_task_id IS NULL AND (p_release IS NULL OR r.id = p_release)
    FOR UPDATE OF r;

    SELECT count(*), min(src.id) INTO v_candidates, v_source_id
    FROM public.releases r
    JOIN public.rss_sources src ON src.id = r.rss_source_id
    WHERE src.subscription_id = p_subscription AND src.source_kind = 'provider_group'
      AND src.provider = p_provider AND src.provider_group_ref = p_group
      AND r.verdict = 'filtered' AND r.reason_code = 'provider_group_not_selected'
      AND r.download_task_id IS NULL AND (p_release IS NULL OR r.id = p_release);

    IF v_candidates >= 3 THEN
        v_batch_key := gen_random_uuid()::text;
    END IF;

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
            'release_evaluate:' || target.id, 5,
            CASE WHEN v_batch_key IS NULL THEN '{"trigger":"group_selected"}'::jsonb
                 ELSE jsonb_build_object('trigger', 'group_selected', 'batch_key', v_batch_key) END,
            target.version, 0::double precision, 7::smallint);
        scheduled := scheduled + 1;
    END LOOP;

    IF scheduled >= 3 AND v_batch_key IS NOT NULL THEN
        INSERT INTO public.subscription_log_entries(
            id, subscription_id, managed_anime_id, kind, outcome, message_code, message_params,
            rss_source_id, group_ref, details, dedupe_key, reason_code,
            subject_type, subject_id, subject_version)
        SELECT gen_random_uuid()::text, sub.id, sub.managed_anime_id,
            'release_batch_processed', 'info', 'release_batch_reevaluated',
            jsonb_build_object('count', scheduled), v_source_id, p_group,
            jsonb_build_object('trigger', 'group_selected', 'created', 0, 'changed', scheduled),
            'release_batch_processed:' || v_batch_key, 'release_batch_processed',
            'release_batch', v_batch_key, 1
        FROM public.subscriptions sub
        WHERE sub.id = p_subscription;
    END IF;
    RETURN scheduled;
END;
$$;
