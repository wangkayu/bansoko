-- Forward-only migration 050: the five media-family payload keys rename to
-- the media.* registry paths (10-22-media-rename).
--
-- Points 1-2 of the media-rename lane (10-22-media-rename/prd.md): the
-- template facts file.resolution / file.video_codec / file.audio_codec /
-- file.container / info.hdr become media.resolution / media.video_codec /
-- media.audio_codec / media.container / media.hdr, so the Go builder
-- (fileoperation/application buildFileOperationNotificationPayload) writes the
-- unified keys and the parity functions re-create as their current bodies with
-- the same keys. The payload keys of envelopes frozen before this migration
-- stay unrenamed inside their rows; templatefacts.NotificationV1Aliases keeps
-- projecting them onto the media.* registry paths, so nothing backfills and no
-- stored notification changes. The Go builders mirror each function key-for-key
-- and the parity tests keep them equal.
CREATE OR REPLACE FUNCTION file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 relative text; directory text; output_name text; root_path text; target_path text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input INTO STRICT input FROM public.acquisitions q WHERE q.id=o.acquisition_id;
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
 root_path:=rtrim(location->>'root','/');target_path:=location->>'canonical_path';
 -- Registered publication locations are canonical absolute paths. Their exact
 -- relationship is revalidated by proof before this projector is admitted.
 IF left(target_path,length(root_path)+1)=root_path||'/' THEN
 relative:=substring(target_path FROM length(root_path)+2);
 IF relative='' OR relative='..' OR left(relative,3)='../' OR public.file_notification_text(relative) IS NULL THEN relative:=NULL; END IF;
 END IF;
 IF relative IS NOT NULL THEN
 output_name:=regexp_replace(relative,'^.*/','');
 directory:=CASE WHEN strpos(relative,'/')>0 THEN regexp_replace(relative,'/[^/]*$','') ELSE '' END;
 ELSE output_name:=name; END IF;
 RETURN jsonb_build_object(
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.custom_title',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.basename',public.file_notification_text(base),'file.extension',public.file_notification_text(lower(extension)),
 'file.size_bytes',f.size_bytes,'file.media_kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'output.directory',public.file_notification_text(directory),'output.filename',public.file_notification_text(output_name),'output.relative_path',public.file_notification_text(relative),
 'file_operation.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'file_operation.collision_policy',public.file_notification_text(e.collision_policy))
 || public.notification_input_facts(input)
 || public.notification_episode_facts(p_fact->'identity_coverage'->'identity')
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

-- file_failure_notification follows the same shape as its success twin.
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
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.custom_title',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_failed','result.outcome','failure','result.reason',reason,
 'file.name',public.file_notification_text(name),'file.basename',public.file_notification_text(base),'file.extension',public.file_notification_text(lower(extension)),
 'file.size_bytes',f.size_bytes,'file.media_kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'output.directory',NULL,'output.filename',NULL,'output.relative_path',NULL,
 'file_operation.kind',public.file_notification_text(o.operation),
 'file_operation.collision_policy',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

-- The re-created shapes check themselves against the stored projectors: the
-- payload keys carry the media.* spellings and the old file.*-media and
-- info.hdr keys are gone from both bodies (an envelope frozen before this
-- migration keeps its own keys and reads them through the alias table).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_success_notification'
          AND prosrc LIKE '%media.resolution%' AND prosrc LIKE '%media.hdr%') THEN
        RAISE EXCEPTION 'migration 050 self-check failed: file_success_notification does not emit the media.* keys';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_failure_notification'
          AND prosrc LIKE '%media.resolution%') THEN
        RAISE EXCEPTION 'migration 050 self-check failed: file_failure_notification does not emit the media.* keys';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname IN ('file_success_notification','file_failure_notification')
          AND (prosrc LIKE '%''file.resolution%' OR prosrc LIKE '%''file.container%'
            OR prosrc LIKE '%''file.video_codec%' OR prosrc LIKE '%''file.audio_codec%'
            OR prosrc LIKE '%''info.hdr%')) THEN
        RAISE EXCEPTION 'migration 050 self-check failed: the file projections still carry a pre-rename media key';
    END IF;
END $$;
