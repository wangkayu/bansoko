-- Mark the birth of a finalized business fact, independently of subsequent
-- notification resolution. Existing admissions intentionally are not replayed.
ALTER TABLE public.notification_admissions ADD COLUMN event_action_ready_at timestamptz;

CREATE FUNCTION public.mark_event_action_facts_ready() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF NEW.trigger_kind NOT IN ('file_operation_completed', 'file_operation_failed', 'download_submitted') THEN
        RETURN NEW;
    END IF;
    IF NEW.state = 'pending_target_resolution' THEN
        IF TG_OP = 'INSERT' THEN
            NEW.event_action_ready_at := clock_timestamp();
        ELSIF OLD.state = 'pending_subtitle_completion' THEN
            NEW.event_action_ready_at := clock_timestamp();
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER event_action_facts_ready BEFORE INSERT OR UPDATE OF state
ON public.notification_admissions FOR EACH ROW EXECUTE FUNCTION public.mark_event_action_facts_ready();
CREATE INDEX notification_admissions_event_action_idx
ON public.notification_admissions (trigger_kind, event_action_ready_at, id)
WHERE event_action_ready_at IS NOT NULL;

CREATE TABLE public.event_action_bindings (
    kind text PRIMARY KEY CHECK (kind IN ('file_operation_completed', 'file_operation_failed', 'download_submitted')),
    script text CHECK (script ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    enabled boolean NOT NULL DEFAULT false,
    timeout_seconds integer NOT NULL DEFAULT 30 CHECK (timeout_seconds IN (15, 30, 60)),
    version integer NOT NULL DEFAULT 0 CHECK (version >= 0),
    active_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    missing boolean NOT NULL DEFAULT false,
    CHECK (script IS NOT NULL OR NOT enabled)
);
INSERT INTO public.event_action_bindings (kind) VALUES
    ('file_operation_completed'), ('download_submitted'), ('file_operation_failed');

CREATE TABLE public.event_action_attempts (
    id uuid PRIMARY KEY,
    event_id text NOT NULL UNIQUE,
    kind text NOT NULL REFERENCES public.event_action_bindings(kind),
    binding_version integer NOT NULL,
    script text NOT NULL,
    occurred_at timestamptz NOT NULL,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    state text NOT NULL CHECK (state IN ('running', 'completed', 'missing', 'unavailable', 'failed', 'timeout', 'unconfirmed')),
    exit_code integer,
    warning boolean NOT NULL DEFAULT false
);
CREATE INDEX event_action_attempts_recent_idx ON public.event_action_attempts (started_at DESC, id DESC);
CREATE INDEX event_action_attempts_warning_idx ON public.event_action_attempts (started_at DESC, id DESC) WHERE warning;
REVOKE INSERT, DELETE ON public.event_action_bindings FROM bansoko_runtime;
GRANT SELECT, UPDATE ON public.event_action_bindings TO bansoko_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.event_action_attempts TO bansoko_runtime;
