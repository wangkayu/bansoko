-- Forward-only migration 048: the completion-notification payload carries
-- info.hdr again.
--
-- The 001 projection file_success_notification is the byte-parity mirror the
-- 039 finish gate compares the Go-built payload against
-- (FILE_NOTIFICATION_PROOF_MISMATCH). Commit 4436ebaf9 added the info.hdr
-- key to the Go builder (buildFileOperationNotificationPayload) but never
-- mirrored it here, so every successful file operation fails the proof gate
-- on a real deployment AND the linux chain parity test
-- (file_publication_chain_linux_test.go, "SQL frozen notification parity")
-- fails on this head; the macOS suites stay green because that test is
-- //go:build linux.
--
-- The fix mirrors the Go side exactly: read the HDR vocabulary the file
-- inspection persisted into artifact_files.facts under key `hdr`
-- (hdr10 / hlg / dovi / a comma-joined list, empty for SDR or unread),
-- sanitized like every other file media fact. Nothing else in the
-- projection changes; unknown payload keys stay rejected.
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
 'release.group_name',public.file_notification_text(CASE WHEN src->'group'->>'state'='known' THEN src->'group'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.basename',public.file_notification_text(base),'file.extension',public.file_notification_text(lower(extension)),
 'file.size_bytes',f.size_bytes,'file.media_kind',public.file_notification_text(f.media_kind),
 'file.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'file.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'file.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'file.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'info.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
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
