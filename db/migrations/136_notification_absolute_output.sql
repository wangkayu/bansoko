-- Forward-only migration 136: absolute output facts in file notifications.
--
-- The notification no longer derives a path from a frozen "notification_root".
-- A successful placement reports the exact result of the publication proof:
-- out.path is the final absolute path, out.dir its absolute parent and out.name
-- its basename. The path is kept as placed, including a collision version
-- suffix; it is never reconstructed from the naming template, the original
-- download name or a host-side mount assumption.
--
-- A bound subtitle list now carries the final file names (no directory) of the
-- subtitles that were bound to the main video and succeeded, in manifest order.
-- The video's own payload starts with an empty list and file_subtitle_group_release
-- fills it after every bound operation settles; an empty list hides the default
-- template's subtitle line.
--
-- notification_path_text is the path-only projection used by out.path/out.dir.
-- It accepts an absolute path as the value the owner configured while keeping
-- the unrelated safety bans: relative values, URL schemes and control
-- characters are still rejected. A legitimate placement name may contain "="
-- or a word such as "token", so the credential-shaped text mask is not applied
-- to a path. notification_name_text is the single-component projection used by
-- out.name and the subtitle names: it is not a path, so separators are refused,
-- and it keeps the same "=" / credential-word exemption while still refusing
-- control characters and URL schemes.
--
-- Historical frozen snapshots and deliveries are not backfilled: an old
-- snapshot may still contain notification_root, and this migration and its Go
-- producer simply stop reading it.

CREATE FUNCTION public.notification_path_text(p_path text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
AS $$
BEGIN
 IF p_path IS NULL OR p_path='' OR octet_length(p_path)>4096 OR left(p_path,1)<>'/' THEN RETURN NULL; END IF;
 IF strpos(p_path,'://')>0 OR p_path ~ '[\x01-\x1f\x7f-\x9f]' THEN RETURN NULL; END IF;
 RETURN p_path;
END $$;
REVOKE ALL ON FUNCTION public.notification_path_text(text) FROM PUBLIC;

CREATE FUNCTION public.notification_name_text(p_name text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
AS $$
BEGIN
 IF p_name IS NULL OR p_name='' OR octet_length(p_name)>4096 OR strpos(p_name,'/')>0 THEN RETURN NULL; END IF;
 IF strpos(p_name,'://')>0 OR p_name ~ '[\x01-\x1f\x7f-\x9f]' THEN RETURN NULL; END IF;
 RETURN p_name;
END $$;
REVOKE ALL ON FUNCTION public.notification_name_text(text) FROM PUBLIC;

-- file_success_notification is the 135 body with the absolute output facts.
CREATE OR REPLACE FUNCTION public.file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 target_path text; output_directory text; output_name text;
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
 target_path:=location->>'canonical_path';
 IF target_path IS NOT NULL AND left(target_path,1)='/' AND public.notification_path_text(target_path) IS NOT NULL THEN
  output_name:=regexp_replace(target_path,'^.*/','');
  output_directory:=CASE WHEN target_path ~ '^/[^/]+$' THEN '/' ELSE regexp_replace(target_path,'/[^/]*$','') END;
 END IF;
 RETURN jsonb_build_object(
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

-- file_subtitle_group_release is the 134 body with final file names instead of
-- paths relative to the removed notification root.
CREATE OR REPLACE FUNCTION public.file_subtitle_group_release(p_video_operation text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
AS $$
DECLARE v public.file_operations; a public.notification_admissions;
 pending integer; paths jsonb; merged jsonb; updated integer;
BEGIN
 SELECT * INTO STRICT v FROM public.file_operations WHERE id=p_video_operation FOR UPDATE;
 IF v.bound_video_operation_id IS NOT NULL THEN RETURN 0; END IF;
 SELECT count(*) INTO pending FROM public.file_operations c
 WHERE c.acquisition_id=v.acquisition_id AND c.bound_video_operation_id=v.id
   AND c.state NOT IN ('completed','failed');
 IF pending>0 THEN RETURN 0; END IF;
 SELECT * INTO a FROM public.notification_admissions
 WHERE source_owner_type='file_operation' AND source_owner_id=v.id
   AND trigger_kind='file_operation_completed' AND state='pending_subtitle_completion' FOR UPDATE;
 IF a.id IS NULL THEN RETURN 0; END IF;
 SELECT COALESCE(jsonb_agg(x.name ORDER BY x.ordinal,x.file_ref COLLATE "C"),'[]'::jsonb) INTO paths
 FROM (
  SELECT f.ordinal,f.file_ref,
   public.notification_name_text(regexp_replace(f.final_object->'location'->>'canonical_path','^.*/','')) AS name
  FROM public.artifact_files af
  JOIN public.acquisition_files f ON f.acquisition_id=v.acquisition_id AND f.file_ref=af.file_ref
  WHERE af.artifact_id=v.artifact_id AND af.bound_video_file_ref=v.file_ref
    AND af.media_kind='subtitle' AND f.outcome='succeeded'
    AND left(f.final_object->'location'->>'canonical_path',1)='/'
 ) x WHERE x.name IS NOT NULL;
 merged:=a.payload||jsonb_build_object('out.subtitles',paths);
 IF octet_length(merged::text) NOT BETWEEN 1 AND 65536 OR public.file_notification_safe(merged) IS NOT TRUE THEN
  UPDATE public.notification_admissions SET state='admission_failed',failure_code='notification_payload_invalid',
   failure_reason='subtitle name list exceeds notification payload bounds'
  WHERE id=a.id AND state='pending_subtitle_completion';
  RETURN 0;
 END IF;
 UPDATE public.notification_admissions SET payload=merged,state='pending_target_resolution'
 WHERE id=a.id AND state='pending_subtitle_completion';
 GET DIAGNOSTICS updated=ROW_COUNT;
 RETURN updated;
END $$;
REVOKE ALL ON FUNCTION public.file_subtitle_group_release(text) FROM PUBLIC;

-- The relative-path helper served only the removed root projection. Replace
-- the two callers above first, then confirm no surviving function body still
-- names it (PostgreSQL does not track body references), and only then drop it.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
        WHERE n.nspname='public' AND p.proname <> 'file_notification_relative_path'
          AND p.prosrc LIKE '%file_notification_relative_path%') THEN
        RAISE EXCEPTION 'migration 136 self-check failed: a surviving function still calls file_notification_relative_path';
    END IF;
END $$;
DROP FUNCTION public.file_notification_relative_path(text,text);

DO $$
BEGIN
    IF public.notification_path_text('/media/library/Show/ep.mkv') IS DISTINCT FROM '/media/library/Show/ep.mkv'
       OR public.notification_path_text('/media/library/Show 07/ep.zh-Hans.ass') IS DISTINCT FROM '/media/library/Show 07/ep.zh-Hans.ass'
       OR public.notification_path_text('/media/library/Show/token=final episode.mkv') IS DISTINCT FROM '/media/library/Show/token=final episode.mkv'
       OR public.notification_name_text('Show token=final episode.mkv') IS DISTINCT FROM 'Show token=final episode.mkv' THEN
        RAISE EXCEPTION 'migration 136 self-check failed: an absolute path was not kept verbatim';
    END IF;
    IF public.notification_path_text('Show/ep.mkv') IS NOT NULL
       OR public.notification_path_text('https://example.invalid/ep.mkv') IS NOT NULL
       OR public.notification_path_text('/media/bad'||chr(10)||'name.mkv') IS NOT NULL
       OR public.notification_path_text('/media/'||repeat('a',4090)) IS NOT NULL
       OR public.notification_name_text('Show/ep.mkv') IS NOT NULL
       OR public.notification_name_text('bad'||chr(10)||'name.mkv') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 136 self-check failed: the path projection accepted an unsafe value';
    END IF;
    IF to_regprocedure('public.file_notification_relative_path(text,text)') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 136 self-check failed: the relative-path helper still exists';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_success_notification'
          AND prosrc LIKE '%notification_path_text%' AND prosrc LIKE '%notification_name_text%'
          AND prosrc NOT LIKE '%notification_root%') THEN
        RAISE EXCEPTION 'migration 136 self-check failed: file_success_notification is not on the absolute output projection';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_subtitle_group_release'
          AND prosrc LIKE '%final_object%'
          AND prosrc NOT LIKE '%notification_root%'
          AND prosrc NOT LIKE '%file_notification_relative_path%') THEN
        RAISE EXCEPTION 'migration 136 self-check failed: file_subtitle_group_release is not on the file-name projection';
    END IF;
END $$;
