-- Forward-only migration 021: a new subscription starts not started.
--
-- 1. subscriptions.state gains 'pending': created by every human entry and by
--    the API unless the caller asks to start at once. A pending subscription
--    does no automatic or manual work (no refresh, evaluation, acquisition or
--    notification); pending -> active is a normal first start (resumed_at stays
--    NULL, so its first refresh discovers and evaluates as usual). There is no
--    pending -> stopped transition. Existing rows keep their state.
--
--    The acquisition qualification function already admits only 'active'
--    (automatic) or 'active'/'stopped' (manual), so a pending subscription
--    fails closed there without a change. The discovery-retention janitor
--    selects only 'stopped', so pending history (there is none) is never
--    treated as stopped.
--
-- 2. subscription_log_entries.kind gains 'subscription_started' for the
--    pending -> active transition.
--

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_state_check;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_state_check CHECK (state IN ('pending', 'active', 'stopped'));

ALTER TABLE subscription_log_entries DROP CONSTRAINT subscription_log_entries_kind_check;
ALTER TABLE subscription_log_entries ADD CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_started', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'rss_refresh_failed', 'release_processed', 'download_changed', 'artifact_verified', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled'));
