-- Existing failed runs cannot be reliably classified as failures or interruptions.
ALTER TABLE public.automation_dispatch_runs
    DROP CONSTRAINT automation_dispatch_runs_state_check,
    DROP CONSTRAINT automation_dispatch_runs_check,
    ADD CONSTRAINT automation_dispatch_runs_state_check CHECK (
        state IN ('queued', 'running', 'completed', 'partial', 'failed', 'unconfirmed')
    ),
    ADD CONSTRAINT automation_dispatch_runs_timestamps_check CHECK (
        (state = 'queued' AND started_at IS NULL AND finished_at IS NULL) OR
        (state = 'running' AND started_at IS NOT NULL AND finished_at IS NULL) OR
        (state IN ('completed', 'partial', 'failed', 'unconfirmed') AND
            started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at)
    ),
    ADD CONSTRAINT automation_dispatch_runs_unconfirmed_counts_check CHECK (
        state <> 'unconfirmed' OR (completed_count IS NULL AND target_count = 0 AND
            enqueued_count = 0 AND merged_count = 0 AND skipped_count = 0 AND dispatch_error_count = 0)
    );
