-- Image identity survives retirement and compaction without creating a lifecycle owner.
CREATE OR REPLACE FUNCTION public.freeze_notification_source_evidence() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path TO 'pg_catalog','public'
AS $$
DECLARE root text; evidence jsonb; seed jsonb; image_owner text;
 entry_kind text; catalog_provider text; catalog_ref text; binding jsonb;
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
 image_owner:=NEW.managed_anime_id;
 IF root IS NOT NULL THEN
  SELECT a.input->'source'->'provenance', m.input_snapshot->'selected_seed', a.entry_kind,
         a.origin_managed_anime_ref, jsonb_strip_nulls(jsonb_build_object(
           'bgm_id',public.notification_known(a.input->'media'->'bgm','{ref}'),
           'tmdb_id',public.notification_known(a.input->'media'->'tmdb','{work_ref}'),
           'tmdb_kind',public.notification_known(a.input->'media'->'tmdb','{kind}')))
  INTO evidence,seed,entry_kind,image_owner,binding
  FROM public.acquisitions a
  LEFT JOIN public.one_shot_acquisitions m ON a.entry_kind='one_shot'
   AND m.managed_anime_id=a.origin_managed_anime_ref AND m.release_id=a.source_release_ref
  WHERE a.id=root;
 ELSIF NEW.trigger_kind IN ('release_discovered','replacement_applied') THEN
  evidence:=public.release_source_evidence(NEW.payload->>'release.id');
 ELSIF NEW.trigger_kind='subscription_changed' AND NEW.source_owner_type='subscription' THEN
  SELECT s.managed_anime_id INTO image_owner FROM public.subscriptions s WHERE s.id=NEW.source_owner_id;
 END IF;
 NEW.source_evidence:=CASE WHEN jsonb_typeof(evidence)='object' THEN evidence ELSE '{}'::jsonb END;
 IF entry_kind='one_shot' THEN
  IF seed->>'provider'=evidence->>'provider' THEN
   catalog_provider:=seed->>'provider';
   catalog_ref:=seed->>'provider_entry_ref';
  END IF;
 ELSE
  SELECT s.origin,s.origin_entry_ref INTO catalog_provider,catalog_ref
  FROM public.subscriptions s WHERE s.managed_anime_id=image_owner;
 END IF;
 IF catalog_provider IN ('anibt','mikan') AND catalog_ref ~ '^[1-9][0-9]{0,255}$' THEN
  NEW.source_evidence:=NEW.source_evidence || jsonb_build_object('image_catalog_entry',
    jsonb_build_object('provider',catalog_provider,'entry_ref',catalog_ref));
 END IF;
 -- Acquisition images use the binding frozen at acceptance. Other events
 -- freeze the binding they observe here, without making it a template fact.
 IF root IS NULL AND image_owner IS NOT NULL THEN
  SELECT jsonb_strip_nulls(jsonb_build_object('bgm_id',m.bgm_work_id,
      'tmdb_id',m.tmdb_work_id,'tmdb_kind',m.tmdb_media_kind)) INTO binding
  FROM public.anime_metadata m WHERE m.managed_anime_id=image_owner;
 END IF;
 IF binding IS NOT NULL AND binding <> '{}'::jsonb THEN
  NEW.source_evidence:=NEW.source_evidence || jsonb_build_object('image_binding',binding);
 END IF;
 IF NEW.event_catalog_version='plugin-event-catalog-v1'
    AND NEW.event_template_catalog_version='notification-template-catalog-v1' THEN
  NEW.event_catalog_version:='plugin-event-catalog-v2';
  NEW.event_template_catalog_version:='notification-template-catalog-v2';
 END IF;
 RETURN NEW;
END $$;
