-- Discovery release retention: keep inactive subscription history for 30
-- days, active history indefinitely.
--
-- The discovery janitor deletes only `releases` rows that never reached a
-- download task, whose owner is inactive: a retired managed anime
-- (`retired_at`) or a stopped subscription (`updated_at`). The two clocks are
-- ordinary owner columns, so the eligibility scan starts from the inactive
-- owner rows and follows `managed_anime -> subscriptions -> rss_sources ->
-- releases`. Both existing indexes cover the owner id only; these two partial
-- indexes let the scan find the inactive clock range without reading every
-- active subscription or anime.
--
-- Forward-only, index-only. No column, row, or grant changes: the runtime
-- already holds DELETE on the release graph (001_baseline.sql).

CREATE INDEX managed_anime_retired_retention_idx
    ON managed_anime (retired_at, id)
    WHERE lifecycle_state = 'retired';

CREATE INDEX subscriptions_stopped_retention_idx
    ON subscriptions (updated_at, id)
    WHERE state = 'stopped';

-- A release is skipped while a run-log row still links it: the log has its own
-- (never longer) 30-day bound, so the release follows on a later pass instead
-- of shortening evidence. The FK side of that probe has no index today.
CREATE INDEX subscription_log_entries_release_idx
    ON subscription_log_entries (release_id)
    WHERE release_id IS NOT NULL;
