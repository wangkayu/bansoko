-- Capture only the selected catalog identity, not the snapshot or cover URL.
-- One-shot mounts can be reclaimed before the notification is delivered.
CREATE OR REPLACE FUNCTION public.freeze_notification_source_evidence() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path TO 'pg_catalog','public'
AS $$
DECLARE root text; evidence jsonb; seed jsonb;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.source_evidence IS DISTINCT FROM OLD.source_evidence THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='NOTIFICATION_SOURCE_IMMUTABLE';
  END IF;
  RETURN NEW;
 END IF;
 IF NEW.trigger_kind IN ('download_submitted','download_failed') AND NEW.source_owner_type='download_task' THEN
  SELECT acquisition_id INTO root FROM public.download_tasks WHERE id=NEW.source_owner_id;
 ELSIF NEW.trigger_kind IN ('file_operation_completed','file_operation_failed') AND NEW.source_owner_type='file_operation' THEN
  SELECT acquisition_id INTO root FROM public.file_operations WHERE id=NEW.source_owner_id;
 ELSIF NEW.trigger_kind='unknown_files_imported' AND NEW.source_owner_type='acquisition' THEN
  root:=NEW.source_owner_id;
 END IF;
 IF root IS NOT NULL THEN
  SELECT a.input->'source'->'provenance', m.input_snapshot->'selected_seed'
  INTO evidence,seed
  FROM public.acquisitions a
  LEFT JOIN public.one_shot_acquisitions m ON a.entry_kind='one_shot'
   AND m.managed_anime_id=a.origin_managed_anime_ref AND m.release_id=a.source_release_ref
  WHERE a.id=root;
 ELSIF NEW.trigger_kind IN ('release_discovered','replacement_applied') THEN
  evidence:=public.release_source_evidence(NEW.payload->>'release.id');
 END IF;
 NEW.source_evidence:=CASE WHEN jsonb_typeof(evidence)='object' THEN evidence ELSE '{}'::jsonb END;
 -- Search and custom previews have scoped refs, never numeric catalog ids.
 IF seed->>'provider' IN ('anibt','mikan')
    AND seed->>'provider'=evidence->>'provider'
    AND seed->>'provider_entry_ref' ~ '^[1-9][0-9]{0,255}$' THEN
  NEW.source_evidence:=NEW.source_evidence || jsonb_build_object('image_catalog_entry',
    jsonb_build_object('provider',seed->>'provider','entry_ref',seed->>'provider_entry_ref'));
 END IF;
 IF NEW.event_catalog_version='plugin-event-catalog-v1'
    AND NEW.event_template_catalog_version='notification-template-catalog-v1' THEN
  NEW.event_catalog_version:='plugin-event-catalog-v2';
  NEW.event_template_catalog_version:='notification-template-catalog-v2';
 END IF;
 RETURN NEW;
END $$;
