ALTER TABLE public.automation_dispatch_runs ADD COLUMN upstream_outcomes jsonb
    CHECK (upstream_outcomes IS NULL OR jsonb_typeof(upstream_outcomes) = 'object');

-- Keep work_items' column layout unchanged so work_item_history%ROWTYPE
-- assignments stay aligned; encode the bounded headerless-429 backoff streak in
-- last_error_reason instead of adding a column.
CREATE FUNCTION public.work_item_defer_rss_quota(
    p_id text, p_fence bigint, p_version bigint, p_seconds double precision, p_code text
) RETURNS bigint
LANGUAGE sql SECURITY DEFINER SET search_path TO pg_catalog, public
AS $$
    WITH deferred AS (
        UPDATE public.work_items
        SET state = 'available', attempt = attempt - 1, lease_until = NULL,
            available_at = CURRENT_TIMESTAMP + make_interval(secs => CASE WHEN p_seconds = 0 THEN
                LEAST(300, 5 * (1 << CASE
                    WHEN last_error_code = 'provider_rate_limited'
                     AND last_error_reason ~ '^quota_backoff_step:[1-7]$'
                    THEN substring(last_error_reason from 20)::integer
                    ELSE 0 END))
                ELSE p_seconds END),
            last_error_code = p_code,
            last_error_reason = CASE WHEN p_seconds = 0 THEN
                'quota_backoff_step:' || LEAST(7, (CASE
                    WHEN last_error_code = 'provider_rate_limited'
                     AND last_error_reason ~ '^quota_backoff_step:[1-7]$'
                    THEN substring(last_error_reason from 20)::integer
                    ELSE 0 END) + 1)
                ELSE NULL END,
            updated_at = CURRENT_TIMESTAMP
        WHERE id = p_id AND fencing_token = p_fence AND expected_owner_version = p_version
          AND kind = 'rss_refresh' AND owner_type = 'rss_source_runtime'
          AND state = 'leased' AND lease_until > CURRENT_TIMESTAMP AND attempt > 0
          AND NOT recovery_blocked AND NOT cancel_requested
          AND p_seconds >= 0 AND p_seconds <= 86400
          AND (p_seconds > 0 OR p_code = 'provider_rate_limited')
          AND p_code IN ('provider_rate_limited', 'provider_cooldown')
        RETURNING 1
    ) SELECT count(*) FROM deferred;
$$;
REVOKE ALL ON FUNCTION public.work_item_defer_rss_quota(text, bigint, bigint, double precision, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.work_item_defer_rss_quota(text, bigint, bigint, double precision, text) TO bansoko_runtime;
