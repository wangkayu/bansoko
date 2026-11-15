-- Forward-only migration 137: freeze the acquisition entry in notification facts.
-- This is task identity, not a provider or managed-anime metadata origin.
-- Keep the four SQL projectors aligned with the Go download/file producers.
-- Existing admissions and deliveries are not rewritten.

CREATE FUNCTION public.notification_task_origin(p_entry_kind text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
AS $$
 SELECT CASE p_entry_kind
  WHEN 'automatic' THEN 'subscription'
  WHEN 'subscription_add' THEN 'subscription'
  WHEN 'one_shot' THEN 'one_shot'
  ELSE NULL END
$$;
REVOKE ALL ON FUNCTION public.notification_task_origin(text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 entry_kind text; input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 target_path text; output_directory text; output_name text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input, q.entry_kind INTO STRICT input, entry_kind FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 src:=input->'source';
 IF p_fact->'render_inputs'->'binding'->>'state'='known' AND p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'state'='known' THEN
 title:=p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'value'; END IF;
 IF src->'release'->>'state'='known' THEN release_id:=src->'release'->'value'->>'ref'; END IF;
 name:=regexp_replace(rtrim(f.relative_path,'/'),'^.*/','');
 IF public.file_notification_text(name) IS NOT NULL THEN
 extension:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'^.*\.','') ELSE '' END;
 base:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'\.[^.]*$','') ELSE name END;
 END IF;
 location:=e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'location';
 target_path:=location->>'canonical_path';
 IF target_path IS NOT NULL AND left(target_path,1)='/' AND public.notification_path_text(target_path) IS NOT NULL THEN
  output_name:=regexp_replace(target_path,'^.*/','');
  output_directory:=CASE WHEN target_path ~ '^/[^/]+$' THEN '/' ELSE regexp_replace(target_path,'/[^/]*$','') END;
 END IF;
 RETURN jsonb_build_object(
 'task.origin',public.notification_task_origin(entry_kind),
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.title_override',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.stem',public.file_notification_text(base),'file.ext',public.file_notification_text(lower(extension)),
 'file.size',f.size_bytes,'file.kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'out.dir',public.notification_path_text(output_directory),'out.name',public.notification_name_text(output_name),'out.path',public.notification_path_text(target_path),
 'out.subtitles','[]'::jsonb,
 'op.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'op.conflict',public.file_notification_text(e.collision_policy))
 || public.notification_input_facts(input)
 || public.notification_episode_facts(p_fact->'identity_coverage'->'identity')
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

CREATE OR REPLACE FUNCTION file_failure_notification(p_operation text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; a public.acquisitions; f public.artifact_files;
 input jsonb; src jsonb; title text; release_id text; name text; base text; extension text; reason text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT a FROM public.acquisitions WHERE id=o.acquisition_id;
 SELECT * INTO f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 src:=input->'source';
 IF a.display_title->>'state'='known' AND jsonb_typeof(a.display_title->'value')='string' THEN
  title:=a.display_title->>'value'; END IF;
 IF src->'release'->>'state'='known' THEN release_id:=src->'release'->'value'->>'ref'; END IF;
 name:=regexp_replace(rtrim(f.relative_path,'/'),'^.*/','');
 IF public.file_notification_text(name) IS NOT NULL THEN
  extension:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'^.*\.','') ELSE '' END;
  base:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'\.[^.]*$','') ELSE name END;
 END IF;
 reason:=COALESCE(public.file_notification_text(p_reason),p_code);
 RETURN jsonb_build_object(
 'task.origin',public.notification_task_origin(a.entry_kind),
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.title_override',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_failed','result.outcome','failure','result.reason',reason,
 'file.name',public.file_notification_text(name),'file.stem',public.file_notification_text(base),'file.ext',public.file_notification_text(lower(extension)),
 'file.size',f.size_bytes,'file.kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'out.dir',NULL,'out.name',NULL,'out.path',NULL,
 'op.kind',public.file_notification_text(o.operation),
 'op.conflict',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

CREATE OR REPLACE FUNCTION download_failed_notification(p_task text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE t public.download_tasks; entry_kind text; input jsonb; source_facts jsonb; src_kind text; retryable boolean;
BEGIN
 SELECT * INTO STRICT t FROM public.download_tasks WHERE id=p_task;
 SELECT q.input, q.entry_kind INTO input, entry_kind FROM public.acquisitions q WHERE q.id=t.acquisition_id;
 SELECT rel.source_facts, s.source_kind INTO source_facts, src_kind
   FROM public.releases rel JOIN public.rss_sources s ON s.id=rel.rss_source_id WHERE rel.id=t.release_id;
 -- Retryable from the RSS page once settled: the release still points at this
 -- task, its anime is not retired, and the acquisition is not a one-shot.
 retryable:=EXISTS (
   SELECT 1 FROM public.releases rel
   JOIN public.rss_sources s ON s.id=rel.rss_source_id
   JOIN public.subscriptions sub ON sub.id=s.subscription_id
   LEFT JOIN public.managed_anime l ON l.id=sub.managed_anime_id
   WHERE rel.download_task_id=t.id AND COALESCE(l.lifecycle_state,'active')='active')
  AND NOT EXISTS (SELECT 1 FROM public.acquisitions a WHERE a.id=t.acquisition_id AND a.entry_kind='one_shot');
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),t.release_id)
  || jsonb_build_object(
   'task.origin',public.notification_task_origin(entry_kind),
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(t.acquisition_id)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(t.acquisition_id),
   'release.src',public.file_notification_text(src_kind),
   'result.outcome','failure',
   'result.category',public.download_failure_category(t.failure_code),
   'result.retryable',retryable);
END $$;

CREATE OR REPLACE FUNCTION unknown_files_notification(p_root text, p_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry_kind text; input jsonb; release_id text; source_facts jsonb; link text;
BEGIN
 SELECT q.input, q.entry_kind INTO input, entry_kind FROM public.acquisitions q WHERE q.id=p_root;
 SELECT t.release_id INTO release_id FROM public.download_tasks t WHERE t.acquisition_id=p_root AND t.release_id IS NOT NULL LIMIT 1;
 SELECT rel.source_facts INTO source_facts FROM public.releases rel WHERE rel.id=release_id;
 -- A notice payload is frozen and delivered to external adapters as it stands,
 -- so it carries no absolute container path. The tree the files live under is
 -- not part of that payload either, so the link cannot name one of them: it
 -- opens this acquisition's execution record, which lists the files, and the
 -- owner matches them from there. The anchor is an in-app hash.
 link:='#/?record='||public.notification_query_escape(p_root);
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),release_id)
  || jsonb_build_object(
   'task.origin',public.notification_task_origin(entry_kind),
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(p_root)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(p_root),
   'out.count',jsonb_array_length(p_files),
   'out.files',p_files,
   'event.link',link);
END $_$;

DO $$
BEGIN
 IF public.notification_task_origin('automatic') IS DISTINCT FROM 'subscription'
 OR public.notification_task_origin('subscription_add') IS DISTINCT FROM 'subscription'
 OR public.notification_task_origin('one_shot') IS DISTINCT FROM 'one_shot'
 OR public.notification_task_origin(NULL) IS NOT NULL
 OR public.notification_task_origin('') IS NOT NULL THEN
  RAISE EXCEPTION 'migration 137 self-check failed: task origin projection';
 END IF;
 IF (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
     WHERE n.nspname='public' AND p.proname IN
      ('file_success_notification','file_failure_notification','download_failed_notification','unknown_files_notification')
     AND p.prosrc LIKE '%notification_task_origin%'
     AND p.prosrc LIKE '%''task.origin''%') <> 4 THEN
  RAISE EXCEPTION 'migration 137 self-check failed: notification projectors missing task origin';
 END IF;
END $$;
