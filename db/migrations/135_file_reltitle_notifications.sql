-- Forward-only migration 135: frozen file-name reltitle facts in the file
-- operation notifications.
--
-- A file operation preparation parses the original download file name once and
-- freezes the complete reltitle tag set into
-- frozen_render_snapshot.file_title_tags (the naming render reads the same
-- parse). The two file operation notifications project that block onto frt.*;
-- nothing parses the renamed library name at admission or delivery time.
-- A snapshot without the block (written before this migration) reads null
-- everywhere: no backfill, and rt.* is never a fallback.
--
-- The Go projector is templatecatalog/notification.FileReltitleFacts; the
-- success payload builder and its file_success_notification twin stay
-- byte-equal, and file_success_atomic_body's FILE_NOTIFICATION_PROOF_MISMATCH
-- gate compares the same shape.
--
-- The frozen block also outlives the operation. acquisition_compact deletes
-- file_operations (detail_expired), and a redownload clears the Release's
-- download task id, so a displaced file operation's snapshot is not readable
-- at displacement time. Each placement therefore freezes its own copy of the
-- source operation's block at admission; a displaced event reads the two
-- surviving placement blocks and never an expired operation. Old placements
-- (and reconciliation/recovery/calibration placements, which have no original
-- download file) keep NULL: no backfill, and nothing is ever reparsed from the
-- renamed library name.

ALTER TABLE public.placements ADD COLUMN file_title_tags jsonb;

ALTER TABLE public.placements ADD CONSTRAINT placements_file_title_tags_check
    CHECK (file_title_tags IS NULL OR jsonb_typeof(file_title_tags) = 'object');

CREATE FUNCTION notification_file_reltitle_facts(p_render jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE block jsonb; tags jsonb:='[]'::jsonb; facts jsonb;
BEGIN
 facts:=jsonb_build_object(
  'frt.title',NULL,'frt.group',NULL,'frt.rev',NULL,'frt.res',NULL,'frt.source',NULL,
  'frt.vc',NULL,'frt.ac',NULL,'frt.lang',NULL,'frt.subflag',NULL,'frt.uncensored',NULL);
 IF p_render IS NULL OR jsonb_typeof(p_render)<>'object' THEN RETURN facts; END IF;
 block:=p_render->'file_title_tags';
 IF block IS NULL OR jsonb_typeof(block)<>'object' OR NOT public.notification_tag_block_valid(block) THEN RETURN facts; END IF;
 IF jsonb_typeof(block->'tags')='array' THEN tags:=block->'tags'; END IF;
 -- ValueTags and the uncensored yes/no answer are read only from the current
 -- schema with a successful parse, exactly like templatefacts.ReltitleValues.
 IF block->>'schema'='reltitle-attrs-v1' THEN
  IF jsonb_typeof(block->'value_tags')='array' THEN tags:=tags||(block->'value_tags'); END IF;
  IF COALESCE(block->>'diagnostic','')='' THEN
   facts:=facts||jsonb_build_object('frt.uncensored',
    CASE WHEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(
      CASE WHEN jsonb_typeof(block->'tags')='array' THEN block->'tags' ELSE '[]'::jsonb END
     ) t(tag) WHERE t.tag='content_rating:UNCENSORED')
     THEN 'true'::jsonb ELSE 'false'::jsonb END);
  END IF;
 END IF;
 RETURN facts||jsonb_build_object(
  'frt.title',public.file_notification_text(public.notification_tag_values(tags,'title')),
  'frt.group',public.file_notification_text(public.notification_tag_values(tags,'group')),
  'frt.rev',public.file_notification_text(public.notification_tag_values(tags,'version')),
  'frt.res',public.file_notification_text(public.notification_tag_values(tags,'resolution')),
  'frt.source',public.file_notification_text(public.notification_tag_values(tags,'source_type')),
  'frt.vc',public.file_notification_text(public.notification_tag_values(tags,'video_codec')),
  'frt.ac',public.file_notification_text(public.notification_tag_values(tags,'audio_codec')),
  'frt.lang',public.file_notification_text(public.notification_tag_values(tags,'sub_lang')),
  'frt.subflag',public.file_notification_text(public.notification_tag_values(tags,'sub_flag')));
END $$;

-- file_success_notification is the 134 body plus the frozen frt projection.
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
 root_path:=o.frozen_render_snapshot->>'notification_root';target_path:=location->>'canonical_path';
 relative:=public.file_notification_relative_path(root_path,target_path);
 IF relative IS NOT NULL THEN
 output_name:=regexp_replace(relative,'^.*/','');
 directory:=CASE WHEN strpos(relative,'/')>0 THEN regexp_replace(relative,'/[^/]*$','') ELSE '' END;
 ELSE output_name:=name; END IF;
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
 'out.dir',public.file_notification_text(directory),'out.name',public.file_notification_text(output_name),'out.path',public.file_notification_text(relative),
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

-- The re-created projector checks itself functionally: a full frozen block,
-- an old snapshot without one, and a diagnostic set.
DO $$
DECLARE
    full_block jsonb;
    partial_block jsonb;
    facts jsonb;
BEGIN
    full_block := jsonb_build_object(
        'schema','reltitle-attrs-v1','implementation','reltitle@v0.1.0','config_digest','lexicon:sha256:135',
        'tags', jsonb_build_array('resolution:2160p','content_rating:UNCENSORED'),
        'value_tags', jsonb_build_array('title:示例作品','group:新组','version:v2'));
    facts := public.notification_file_reltitle_facts(jsonb_build_object('file_title_tags', full_block));
    IF facts->>'frt.title' IS DISTINCT FROM '示例作品'
       OR facts->>'frt.group' IS DISTINCT FROM '新组'
       OR facts->>'frt.rev' IS DISTINCT FROM 'v2'
       OR facts->>'frt.res' IS DISTINCT FROM '2160p'
       OR facts->'frt.uncensored' IS DISTINCT FROM 'true'::jsonb THEN
        RAISE EXCEPTION 'migration 135 self-check failed: full block projected wrong: %', facts;
    END IF;
    IF facts->>'frt.vc' IS NOT NULL OR facts->>'frt.ac' IS NOT NULL THEN
        RAISE EXCEPTION 'migration 135 self-check failed: absent fields are not null: %', facts;
    END IF;
    facts := public.notification_file_reltitle_facts('{"renderer_version":"fileoperation-render-v1"}'::jsonb);
    -- -> returns the JSON null for a key whose value is null, which is not
    -- SQL NULL; compare against the JSON null explicitly.
    IF facts->>'frt.title' IS NOT NULL OR facts->'frt.uncensored' IS DISTINCT FROM 'null'::jsonb THEN
        RAISE EXCEPTION 'migration 135 self-check failed: an old snapshot without the block yielded values: %', facts;
    END IF;
    partial_block := jsonb_build_object('schema','reltitle-attrs-v1','tags',jsonb_build_array('resolution:1080p'),'diagnostic','reltitle_parse_failed');
    facts := public.notification_file_reltitle_facts(jsonb_build_object('file_title_tags', partial_block));
    IF facts->>'frt.res' IS DISTINCT FROM '1080p' OR facts->'frt.uncensored' IS DISTINCT FROM 'null'::jsonb THEN
        RAISE EXCEPTION 'migration 135 self-check failed: a diagnostic block must keep tags and withhold the yes/no answer: %', facts;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'placements' AND column_name = 'file_title_tags'
          AND data_type = 'jsonb' AND is_nullable = 'YES') THEN
        RAISE EXCEPTION 'migration 135 self-check failed: placements.file_title_tags is missing or not nullable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_success_notification'
          AND prosrc LIKE '%notification_file_reltitle_facts%') THEN
        RAISE EXCEPTION 'migration 135 self-check failed: file_success_notification does not project frt';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_failure_notification'
          AND prosrc LIKE '%notification_file_reltitle_facts%') THEN
        RAISE EXCEPTION 'migration 135 self-check failed: file_failure_notification does not project frt';
    END IF;
END $$;
