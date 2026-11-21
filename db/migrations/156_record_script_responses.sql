-- Keep the sanitized response and its source identity with the existing receipt.
-- Legacy claims do not prove a process started; leave the new fields unset.
ALTER TABLE public.event_action_attempts
    ADD COLUMN acquisition_id text,
    ADD COLUMN source_id text,
    ADD COLUMN source_version bigint,
    ADD COLUMN process_started_at timestamptz,
    ADD COLUMN script_summary text,
    ADD COLUMN http_status smallint,
    ADD CONSTRAINT event_action_response_check CHECK (
        (script_summary IS NULL OR (process_started_at IS NOT NULL AND exit_code IS NOT NULL
            AND exit_code >= 0 AND char_length(script_summary) BETWEEN 1 AND 240))
        AND (http_status IS NULL OR (script_summary IS NOT NULL AND http_status BETWEEN 100 AND 599))
    );
CREATE INDEX event_action_attempts_acquisition_idx ON public.event_action_attempts (acquisition_id)
    WHERE acquisition_id IS NOT NULL;
