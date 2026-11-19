-- Only new deliveries freeze complete presentation. Older rows never stored
-- their inline links or image instructions; leave them on the legacy reader.
ALTER TABLE public.plugin_deliveries ADD COLUMN message_snapshot jsonb;

ALTER TABLE public.plugin_deliveries ADD CONSTRAINT plugin_deliveries_message_snapshot_check CHECK (
  message_snapshot IS NULL OR (
    jsonb_typeof(message_snapshot) = 'object' AND pg_column_size(message_snapshot) <= 1048576
    AND message_snapshot ?& ARRAY['version', 'title', 'parts']
    AND message_snapshot->'version' = '1'::jsonb
    AND jsonb_typeof(message_snapshot->'title') = 'string'
    AND jsonb_typeof(message_snapshot->'parts') = 'array'
    AND rendered_title IS NOT NULL AND rendered_body IS NOT NULL
    AND rendered_title = message_snapshot->>'title'
  ) IS TRUE
);

CREATE FUNCTION public.keep_notification_message_snapshot() RETURNS trigger
 LANGUAGE plpgsql SET search_path TO 'pg_catalog', 'public'
AS $$
BEGIN
 IF NEW.message_snapshot IS DISTINCT FROM OLD.message_snapshot THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='NOTIFICATION_MESSAGE_IMMUTABLE';
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER keep_notification_message_snapshot
BEFORE UPDATE OF message_snapshot ON public.plugin_deliveries
FOR EACH ROW EXECUTE FUNCTION public.keep_notification_message_snapshot();
