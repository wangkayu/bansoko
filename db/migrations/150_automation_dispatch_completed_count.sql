-- Older dispatches did not record inline completions. Leave them unknown,
-- including historical catalog checks whose counts were recorded as enqueued.
ALTER TABLE public.automation_dispatch_runs
    ADD COLUMN completed_count integer,
    DROP CONSTRAINT automation_dispatch_runs_check2,
    ADD CONSTRAINT automation_dispatch_runs_completed_count_check CHECK (
        completed_count IS NULL OR (completed_count >= 0 AND state IN ('completed', 'partial', 'failed'))
    ),
    ADD CONSTRAINT automation_dispatch_runs_partial_check CHECK (
        state <> 'partial' OR (dispatch_error_count > 0 AND
            (enqueued_count > 0 OR merged_count > 0 OR COALESCE(completed_count, 0) > 0))
    ),
    ADD CONSTRAINT automation_dispatch_runs_confirmed_failure_check CHECK (
        state <> 'failed' OR completed_count IS NULL OR
        (dispatch_error_count > 0 AND completed_count = 0 AND enqueued_count = 0 AND merged_count = 0)
    );
