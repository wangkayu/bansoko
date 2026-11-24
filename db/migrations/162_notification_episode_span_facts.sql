-- Forward-only migration 162: unified range projection and media_fact_files
-- as the per-file episode SSOT for completed/failed file notifications.
--
-- Registry identities (download_submitted and other acquisition events) use
-- {state,value:{episode:{state,value}, ...}}. Stored media identities use the
-- Go EpisodeIdentity JSON shape ("State", "Episode", "EpisodeEnd", ...).
-- Keep those adapters separate, then share the same five-key projection.

CREATE OR REPLACE FUNCTION public.notification_episode_facts(p_identity jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
WITH base AS (
    SELECT
        public.notification_known(p_identity->'value'->'raw_episode', '{}') AS raw_ep,
        public.notification_known(p_identity->'value'->'episode', '{}') AS ep,
        public.notification_known(p_identity->'value'->'episode_end', '{}') AS ep_end,
        CASE WHEN jsonb_typeof(p_identity->'value'->'offset') = 'number'
             THEN p_identity->'value'->'offset'
             ELSE 'null'::jsonb END AS ep_offset
    WHERE p_identity->>'state' = 'known'
      AND p_identity->'value'->>'state' = 'resolved'
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM base WHERE ep <> 'null'::jsonb)
THEN (
    SELECT jsonb_build_object(
        'ep.raw', raw_ep,
        'ep.number', ep,
        'ep.end', CASE WHEN ep_end <> 'null'::jsonb AND (ep_end #>> '{}') IS DISTINCT FROM (ep #>> '{}')
                       THEN ep_end ELSE 'null'::jsonb END,
        'ep.display', CASE WHEN ep_end <> 'null'::jsonb AND (ep_end #>> '{}') IS DISTINCT FROM (ep #>> '{}')
                           THEN to_jsonb((ep #>> '{}') || '-' || (ep_end #>> '{}'))
                           ELSE ep END,
        'ep.offset', ep_offset
    )
    FROM base
)
ELSE jsonb_build_object('ep.raw', NULL, 'ep.number', NULL, 'ep.end', NULL, 'ep.display', NULL, 'ep.offset', NULL)
END
$$;

CREATE OR REPLACE FUNCTION public.notification_stored_episode_facts(p_identity jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
WITH base AS (
    SELECT
        public.file_notification_text(CASE WHEN jsonb_typeof(p_identity->'RawEpisode') = 'string'
                                           THEN p_identity->>'RawEpisode' END) AS raw_ep,
        public.file_notification_text(CASE WHEN jsonb_typeof(p_identity->'Episode') = 'string'
                                           THEN p_identity->>'Episode' END) AS ep,
        public.file_notification_text(CASE WHEN jsonb_typeof(p_identity->'EpisodeEnd') = 'string'
                                           THEN p_identity->>'EpisodeEnd' END) AS ep_end,
        CASE WHEN jsonb_typeof(p_identity->'Offset') = 'number'
             THEN p_identity->'Offset' ELSE 'null'::jsonb END AS ep_offset
    WHERE jsonb_typeof(p_identity) = 'object'
      AND p_identity->>'State' = 'resolved'
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM base WHERE ep IS NOT NULL)
THEN (
    SELECT jsonb_build_object(
        'ep.raw', raw_ep,
        'ep.number', ep,
        'ep.end', CASE WHEN ep_end IS NOT NULL AND ep_end IS DISTINCT FROM ep THEN ep_end ELSE NULL END,
        'ep.display', CASE WHEN ep_end IS NOT NULL AND ep_end IS DISTINCT FROM ep
                           THEN ep || '-' || ep_end ELSE ep END,
        'ep.offset', ep_offset
    ) FROM base
)
ELSE jsonb_build_object('ep.raw', NULL, 'ep.number', NULL, 'ep.end', NULL, 'ep.display', NULL, 'ep.offset', NULL)
END
$$;

-- SQL NULL means that the latest snapshot has no row for this file. JSON null,
-- an unresolved identity or malformed effective facts are present file evidence
-- and therefore return null facts instead of borrowing the release identity.
CREATE OR REPLACE FUNCTION public.notification_media_episode_facts(p_file_identity jsonb, p_release_identity jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
SELECT CASE WHEN p_file_identity IS NOT NULL
       THEN public.notification_stored_episode_facts(p_file_identity)
       ELSE public.notification_stored_episode_facts(p_release_identity) END
$$;

CREATE OR REPLACE FUNCTION public.file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 entry_kind text; input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 target_path text; output_directory text; output_name text; file_identity jsonb; release_identity jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input, q.entry_kind INTO STRICT input, entry_kind FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 SELECT CASE WHEN mf.file_ref IS NOT NULL
             THEN COALESCE(mf.effective_facts->'identity', 'null'::jsonb)
        END,
        snap.release_identity INTO file_identity, release_identity
 FROM public.file_operations op
 LEFT JOIN public.artifacts art ON art.id=op.artifact_id
 LEFT JOIN public.download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN public.releases rel ON rel.id=dt.release_id
 LEFT JOIN LATERAL (
     SELECT snap.id, snap.release_identity FROM public.media_fact_snapshots snap
     WHERE snap.release_id=rel.id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1
 ) snap ON TRUE
 LEFT JOIN public.media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=op.file_ref
 WHERE op.id=o.id;
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
 || public.notification_media_episode_facts(file_identity, release_identity)
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   input->'source'->'tag_evidence');
END $_$;

CREATE OR REPLACE FUNCTION public.file_failure_notification(p_operation text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; a public.acquisitions; f public.artifact_files;
 input jsonb; src jsonb; title text; release_id text; name text; base text; extension text; reason text;
 file_identity jsonb; release_identity jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT a FROM public.acquisitions WHERE id=o.acquisition_id;
 SELECT * INTO f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT CASE WHEN mf.file_ref IS NOT NULL
             THEN COALESCE(mf.effective_facts->'identity', 'null'::jsonb)
        END,
        snap.release_identity INTO file_identity, release_identity
 FROM public.file_operations op
 LEFT JOIN public.artifacts art ON art.id=op.artifact_id
 LEFT JOIN public.download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN public.releases rel ON rel.id=dt.release_id
 LEFT JOIN LATERAL (
     SELECT snap.id, snap.release_identity FROM public.media_fact_snapshots snap
     WHERE snap.release_id=rel.id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1
 ) snap ON TRUE
 LEFT JOIN public.media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=op.file_ref
 WHERE op.id=o.id;
 input:=a.input;
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
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'out.dir',NULL,'out.name',NULL,'out.path',NULL,
 'op.kind',public.file_notification_text(o.operation),
 'op.conflict',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_media_episode_facts(file_identity, release_identity)
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   input->'source'->'tag_evidence');
END $_$;

DO $$
DECLARE facts jsonb;
BEGIN
    facts := public.notification_episode_facts(jsonb_build_object(
        'state','known','value',jsonb_build_object(
            'state','resolved',
            'raw_episode',jsonb_build_object('state','known','value','12'),
            'episode',jsonb_build_object('state','known','value','23'),
            'episode_end',jsonb_build_object('state','known','value','24'),
            'offset',0)));
    IF facts->>'ep.display' IS DISTINCT FROM '23-24' OR facts->>'ep.end' IS DISTINCT FROM '24' THEN
        RAISE EXCEPTION 'migration 162 self-check failed: registry range: %', facts;
    END IF;
    facts := public.notification_media_episode_facts(
        '{"State":"resolved","RawEpisode":"12","Episode":"23","EpisodeEnd":"24","Offset":4,"SelectedSource":"file_parser"}'::jsonb,
        '{"State":"resolved","RawEpisode":"1","Episode":"1","Offset":0}'::jsonb);
    IF facts->>'ep.raw' IS DISTINCT FROM '12' OR facts->>'ep.number' IS DISTINCT FROM '23'
       OR facts->>'ep.end' IS DISTINCT FROM '24' OR facts->>'ep.display' IS DISTINCT FROM '23-24'
       OR facts->'ep.offset' IS DISTINCT FROM '4'::jsonb THEN
        RAISE EXCEPTION 'migration 162 self-check failed: stored file range: %', facts;
    END IF;
    facts := public.notification_media_episode_facts(
        '{"State":"unresolved","FailureCode":"episode_missing"}'::jsonb,
        '{"State":"resolved","RawEpisode":"1","Episode":"1","Offset":0}'::jsonb);
    IF facts->'ep.number' IS DISTINCT FROM 'null'::jsonb OR facts->'ep.display' IS DISTINCT FROM 'null'::jsonb THEN
        RAISE EXCEPTION 'migration 162 self-check failed: unresolved file borrowed release identity: %', facts;
    END IF;
    facts := public.notification_media_episode_facts(
        NULL,
        '{"State":"resolved","RawEpisode":"13","Episode":"1","Offset":-12}'::jsonb);
    IF facts->>'ep.raw' IS DISTINCT FROM '13' OR facts->>'ep.display' IS DISTINCT FROM '1'
       OR facts->'ep.offset' IS DISTINCT FROM '-12'::jsonb THEN
        RAISE EXCEPTION 'migration 162 self-check failed: missing-row legacy fallback: %', facts;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname IN ('file_success_notification','file_failure_notification')
                   GROUP BY proname HAVING COUNT(*)=1) THEN
        RAISE EXCEPTION 'migration 162 self-check failed: file notification functions missing';
    END IF;
END $$;
