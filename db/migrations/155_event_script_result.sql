ALTER TABLE public.event_action_bindings
    ADD COLUMN record_result boolean NOT NULL DEFAULT false;
