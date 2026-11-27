ALTER TABLE public.automation_dispatch_runs ADD COLUMN upstream_outcomes jsonb
    CHECK (upstream_outcomes IS NULL OR jsonb_typeof(upstream_outcomes) = 'object');

-- Persist a bounded streak on the work item so reclaimed leases and worker
-- restarts do not reset the wait for a 429 without a usable deadline.
ALTER TABLE public.work_items ADD COLUMN rss_quota_backoff_step integer NOT NULL DEFAULT 0
    CHECK (rss_quota_backoff_step BETWEEN 0 AND 7);

CREATE FUNCTION public.work_item_defer_rss_quota(
    p_id text, p_fence bigint, p_version bigint, p_seconds double precision, p_code text
) RETURNS bigint
LANGUAGE sql SECURITY DEFINER SET search_path TO pg_catalog, public
AS $$
    WITH deferred AS (
        UPDATE public.work_items
        SET state = 'available', attempt = attempt - 1, lease_until = NULL,
            available_at = CURRENT_TIMESTAMP + make_interval(secs => CASE WHEN p_seconds = 0 THEN
                LEAST(300, 5 * (1 << CASE WHEN last_error_code = 'provider_rate_limited' THEN rss_quota_backoff_step ELSE 0 END))
                ELSE p_seconds END),
            rss_quota_backoff_step = CASE WHEN p_seconds = 0 THEN
                CASE WHEN last_error_code = 'provider_rate_limited' THEN LEAST(rss_quota_backoff_step + 1, 7) ELSE 1 END
                ELSE 0 END,
            last_error_code = p_code, last_error_reason = NULL, updated_at = CURRENT_TIMESTAMP
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
