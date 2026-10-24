-- Forward-only migration 019: maintenance-record retention and the resume
-- baseline.
--
-- 1. Resume baseline (see 3 below: the first refresh after a resume only
--    records the baseline). `subscriptions.resumed_at` is written
--    by the stopped -> active transition; `rss_source_runtime.
--    resume_baseline_at` copies that value when the source's first
--    successful refresh after the resume recorded the feed window. A source
--    whose runtime existed before the resume and whose copy differs is due a
--    baseline refresh: no discovery notice, no evaluation, no acquisition.
--    The copy lives on the runtime row the refresh already owns, so the resume
--    transaction never has to lock runtime rows (the refresh locks runtime
--    before subscription; the resume locks subscription first).
--
-- 2. Maintenance retention (library content is permanent,
--    maintenance records expire). The retention janitor's new
--    cleaners scan by their terminal clock; these partial indexes keep each
--    scan on the expired range. No grant changes: the runtime already holds
--    DELETE on idempotency_records, notification_admissions,
--    alignment_operations and reconciliation_files (001_baseline.sql).

ALTER TABLE subscriptions ADD COLUMN resumed_at TIMESTAMPTZ;
ALTER TABLE rss_source_runtime ADD COLUMN resume_baseline_at TIMESTAMPTZ;

COMMENT ON COLUMN subscriptions.resumed_at IS 'Time of the last stopped -> active transition; NULL when the subscription was never resumed.';
COMMENT ON COLUMN rss_source_runtime.resume_baseline_at IS 'Copy of subscriptions.resumed_at taken by the first successful refresh after that resume; a differing value means the next refresh only records the baseline.';

CREATE INDEX idempotency_records_created_retention_idx
    ON idempotency_records (created_at);

CREATE INDEX notification_admissions_settled_retention_idx
    ON notification_admissions (created_at, id)
    WHERE state IN ('resolved', 'admission_failed');

CREATE INDEX alignment_operations_terminal_retention_idx
    ON alignment_operations (completed_at, id)
    WHERE state IN ('completed', 'blocked', 'failed', 'canceled');

-- 3. A Release first stored by a resume-baseline refresh carries the durable
--    verdict 'resume_baseline': it was in the feed when the subscription
--    resumed, so every evaluation path treats it as terminal history.
ALTER TABLE releases DROP CONSTRAINT releases_verdict_check;
ALTER TABLE releases ADD CONSTRAINT releases_verdict_check CHECK (verdict IN ('discovered', 'waiting_metadata', 'subscription_stopped', 'source_unavailable', 'parse_failed', 'accepted_pending_download', 'filtered', 'adopted_existing', 'acquisition_failed', 'submitted', 'resume_baseline') AND ((download_task_id IS NULL AND verdict <> 'submitted') OR (download_task_id IS NOT NULL AND verdict IN ('accepted_pending_download', 'submitted', 'filtered', 'waiting_metadata', 'parse_failed'))));
