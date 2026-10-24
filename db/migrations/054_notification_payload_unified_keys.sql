-- Forward-only migration 054: notification payload keys unify onto the
-- registry fact paths; the legacy spelling layer is gone
-- (10-23-namespace-consistency).
--
-- Until this migration the producers wrote legacy payload spellings and
-- templatefacts.NotificationV1Aliases projected them onto the unified
-- registry paths at render and webhook time. Now the key a producer writes
-- is the path a template reads, with no translation layer in between:
--
--   - notification_input_facts stores bgm.id (was bangumi.id), tmdb.kind
--     (was tmdb.media_kind), torrent.hash_v1 / torrent.hash_v2 (were
--     torrent.info_hash_v1 / torrent.info_hash_v2).
--   - notification_episode_facts stores ep.raw / ep.number / ep.display /
--     ep.offset (were episode.raw / episode.number / episode.display /
--     episode.offset).
--   - file_success_notification and file_failure_notification store
--     anime.title_override (was anime.custom_title), file.stem / file.ext /
--     file.size / file.kind (were file.basename / file.extension /
--     file.size_bytes / file.media_kind), out.dir / out.name / out.path
--     (were output.directory / output.filename / output.relative_path) and
--     op.kind / op.conflict (were file_operation.kind /
--     file_operation.collision_policy).
--   - file_success_atomic_body's FILE_NOTIFICATION_PROOF_MISMATCH pre-check
--     reads the renamed keys, so the proof gate and the projector compare
--     the same payload.
--   - download_failed_notification is re-created byte-identical to 049: its
--     own keys were already unified, and it keeps winning its release.src
--     merge over the re-created input facts.
--
-- The Go builders mirror each function key-for-key
-- (templatecatalog/notification InputFacts and EpisodeFacts,
-- fileoperation/application buildFileOperationNotificationPayload) and the
-- parity tests keep them equal. Nothing backfills: every environment is a
-- test environment, and an envelope frozen before this migration keeps its
-- legacy keys, which now simply read null under the unified leaves (the
-- alias projection that used to rescue them is deleted).

CREATE OR REPLACE FUNCTION notification_input_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'anime.id',public.notification_known(p_input->'config'->'identity'->'managed_anime_ref','{}'),
 'release.src',public.notification_known(p_input->'source'->'provider','{}'),
 'anime.group',public.notification_known(p_input->'source'->'group','{}'),
 'bgm.id',public.notification_known(p_input->'media'->'bgm','{ref}'),
 'tmdb.id',public.notification_known(p_input->'media'->'tmdb','{work_ref}'),
 'tmdb.kind',public.notification_known(p_input->'media'->'tmdb','{kind}'),
 'tmdb.season',public.notification_known(p_input->'media'->'tmdb','{season}'),
 'torrent.hash_v1',public.notification_known(p_input->'source'->'infohash_v1','{}'),
 'torrent.hash_v2',public.notification_known(p_input->'source'->'infohash_v2','{}'),
 'torrent.size',public.notification_known(p_input->'source'->'torrent_size','{}'),
 'torrent.file_count',public.notification_known(p_input->'source'->'torrent_file_count','{}'))
$$;

-- notification_episode_facts is the 001 body with the four ep.* keys.
CREATE OR REPLACE FUNCTION notification_episode_facts(p_identity jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN p_identity->>'state'='known' AND p_identity->'value'->>'state'='resolved'
  AND public.notification_known(p_identity->'value'->'episode','{}')<>'null'::jsonb
 THEN jsonb_build_object(
  'ep.raw',public.notification_known(p_identity->'value'->'raw_episode','{}'),
  'ep.number',public.notification_known(p_identity->'value'->'episode','{}'),
  'ep.display',public.notification_known(p_identity->'value'->'episode','{}'),
  'ep.offset',CASE WHEN jsonb_typeof(p_identity->'value'->'offset')='number' THEN p_identity->'value'->'offset' ELSE 'null'::jsonb END)
 ELSE jsonb_build_object('ep.raw',NULL,'ep.number',NULL,'ep.display',NULL,'ep.offset',NULL) END
$$;

-- file_success_notification is the byte-parity mirror the 039 finish gate
-- compares the Go-built payload against (FILE_NOTIFICATION_PROOF_MISMATCH).
-- It is re-created as the 050 body with the unified keys.
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
 'op.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'op.conflict',public.file_notification_text(e.collision_policy))
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
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

-- download_failed_notification is re-created byte-identical to 049: its own
-- keys were already unified, and the re-created notification_input_facts no
-- longer carries the legacy spellings it merged over.
CREATE OR REPLACE FUNCTION download_failed_notification(p_task text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE t public.download_tasks; input jsonb; source_facts jsonb; src_kind text; retryable boolean;
BEGIN
 SELECT * INTO STRICT t FROM public.download_tasks WHERE id=p_task;
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=t.acquisition_id;
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
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(t.acquisition_id)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(t.acquisition_id),
   'release.src',public.file_notification_text(src_kind),
   'result.outcome','failure',
   'result.category',public.download_failure_category(t.failure_code),
   'result.retryable',retryable);
END $$;

-- file_success_atomic_body is the 039 body whose proof pre-check reads the
-- renamed keys: the gate and the projector compare the same payload.
CREATE OR REPLACE FUNCTION public.file_success_atomic_body(p_operation text, p_work text, p_permit text, p_file_fact text, p_notification text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; e public.file_operations;
 next_version bigint; affected integer;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 -- Notification ledger 029 accepts ordinary bounded JSON objects, not Plan
 -- canonical bytes. Frozen construction/sanitization is independently checked
 -- by the fixed caller before admission; this helper remains private.
 IF p_notification IS NULL OR jsonb_typeof(p_notification::jsonb) IS DISTINCT FROM 'object'
 OR octet_length(p_notification) NOT BETWEEN 1 AND 65536
 OR public.file_notification_safe(p_notification::jsonb) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
 -- Bind stable result fields before any business write. Full frozen display
 -- field reconstruction remains required before granting this command.
 IF p_notification::jsonb->>'event.type' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->>'result.outcome' IS DISTINCT FROM 'success'
 OR p_notification::jsonb->>'result.reason' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->'anime.title_override' IS DISTINCT FROM 'null'::jsonb
 OR p_notification::jsonb->>'op.kind' IS DISTINCT FROM e.operation
 OR p_notification::jsonb->>'op.conflict' IS DISTINCT FROM e.collision_policy
 OR p_notification::jsonb->'file.size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 IF p_notification::jsonb IS DISTINCT FROM public.file_success_notification(p_operation,p_permit,p_file_fact::jsonb) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 PERFORM public.file_assert_final_proof(p_permit);
 PERFORM public.file_success_projection(p_operation,p_permit,p_file_fact);
 IF w.cancel_requested THEN
  affected:=public.file_cancelled_dependency_suppression(p_operation,p_work);
 ELSE
  affected:=public.file_success_handoff(p_operation,p_work);
 END IF;
 -- The publish_target proof is the completion record of the typed chain
 -- (atomic_method 'replace' plus the displaced object for version and
 -- overwrite). Path-level verification re-reads no content, so the target
 -- fingerprint carries the frozen source fingerprint (the downloader's
 -- verified digest) with the published size.
 UPDATE public.file_operations SET state='completed',failure_code=NULL,failure_reason=NULL,
 target_fingerprint=jsonb_build_object('algorithm',o.source_fingerprint->>'algorithm','value',o.source_fingerprint->>'value',
  'size_bytes',COALESCE(e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'size',e.publication_frozen_objects->'source'->'size')),
 publication_checkpoint=e.publication_step_checkpoint->'publish_target'->'proof',
 observed_at=clock_timestamp(),version=version+1 WHERE id=p_operation RETURNING version INTO next_version;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_completed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',p_notification::jsonb,'pending_target_resolution');
 IF public.work_item_finish('succeeded',next_version,p_work)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','completed','owner_version',next_version,'affected_dependencies',affected);
END $$;

-- The re-created shapes check themselves: the two sql-language projectors are
-- exercised functionally (unified keys in, legacy spellings out), and the
-- plpgsql bodies are checked from their stored source (prosrc) for the
-- unified keys and against every retired spelling.
DO $$
DECLARE
    input jsonb;
    facts jsonb;
BEGIN
    input := jsonb_build_object(
        'config', jsonb_build_object('identity', jsonb_build_object('managed_anime_ref', jsonb_build_object('state','known','value','ma-054'))),
        'source', jsonb_build_object(
            'provider', jsonb_build_object('state','known','value','anibt'),
            'group', jsonb_build_object('state','known','value','LoliHouse'),
            'infohash_v1', jsonb_build_object('state','known','value','0541'),
            'infohash_v2', jsonb_build_object('state','known','value','0542')),
        'media', jsonb_build_object(
            'bgm', jsonb_build_object('state','known','value', jsonb_build_object('ref','425998')),
            'tmdb', jsonb_build_object('state','known','value', jsonb_build_object('kind','tv','season',1))));
    facts := public.notification_input_facts(input);
    IF facts ->> 'bgm.id' IS DISTINCT FROM '425998'
       OR facts ->> 'tmdb.kind' IS DISTINCT FROM 'tv'
       OR facts ->> 'torrent.hash_v1' IS DISTINCT FROM '0541'
       OR facts ->> 'torrent.hash_v2' IS DISTINCT FROM '0542'
       OR (facts ? 'bangumi.id') OR (facts ? 'tmdb.media_kind')
       OR (facts ? 'torrent.info_hash_v1') OR (facts ? 'torrent.info_hash_v2') THEN
        RAISE EXCEPTION 'migration 054 self-check failed: notification_input_facts did not project the unified keys: %', facts;
    END IF;
    facts := public.notification_episode_facts(jsonb_build_object(
        'state','known',
        'value', jsonb_build_object('state','resolved',
            'raw_episode', jsonb_build_object('state','known','value','07'),
            'episode', jsonb_build_object('state','known','value',7),
            'offset', 0)));
    IF facts ->> 'ep.raw' IS DISTINCT FROM '07'
       OR facts -> 'ep.number' IS DISTINCT FROM '7'::jsonb
       OR facts -> 'ep.display' IS DISTINCT FROM '7'::jsonb
       OR facts -> 'ep.offset' IS DISTINCT FROM '0'::jsonb
       OR (facts ? 'episode.raw') OR (facts ? 'episode.number')
       OR (facts ? 'episode.display') OR (facts ? 'episode.offset') THEN
        RAISE EXCEPTION 'migration 054 self-check failed: notification_episode_facts did not project the ep.* keys: %', facts;
    END IF;
    -- The plpgsql twins are checked from their stored source (prosrc): each
    -- carries the unified keys and none of the retired spellings.
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_success_notification'
          AND prosrc LIKE '%anime.title_override%' AND prosrc LIKE '%file.stem%'
          AND prosrc LIKE '%out.dir%' AND prosrc LIKE '%op.kind%') THEN
        RAISE EXCEPTION 'migration 054 self-check failed: file_success_notification does not emit the unified keys';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_failure_notification'
          AND prosrc LIKE '%anime.title_override%' AND prosrc LIKE '%file.stem%'
          AND prosrc LIKE '%out.dir%' AND prosrc LIKE '%op.conflict%') THEN
        RAISE EXCEPTION 'migration 054 self-check failed: file_failure_notification does not emit the unified keys';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'file_success_atomic_body'
          AND prosrc LIKE '%anime.title_override%' AND prosrc LIKE '%file.size%') THEN
        RAISE EXCEPTION 'migration 054 self-check failed: file_success_atomic_body does not read the unified keys';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname IN ('file_success_notification','file_failure_notification','file_success_atomic_body')
          AND (prosrc LIKE '%anime.custom_title%' OR prosrc LIKE '%file.basename%'
            OR prosrc LIKE '%file.extension%' OR prosrc LIKE '%file.size_bytes%'
            OR prosrc LIKE '%file.media_kind%' OR prosrc LIKE '%output.directory%'
            OR prosrc LIKE '%output.filename%' OR prosrc LIKE '%output.relative_path%'
            OR prosrc LIKE '%file_operation.kind%' OR prosrc LIKE '%file_operation.collision_policy%')) THEN
        RAISE EXCEPTION 'migration 054 self-check failed: a file projector or the proof gate still carries a legacy payload key';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'notification_input_facts'
          AND (prosrc LIKE '%bangumi.id%' OR prosrc LIKE '%tmdb.media_kind%'
            OR prosrc LIKE '%torrent.info_hash_v1%' OR prosrc LIKE '%torrent.info_hash_v2%')) THEN
        RAISE EXCEPTION 'migration 054 self-check failed: notification_input_facts still carries a legacy payload key';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'notification_episode_facts'
          AND (prosrc LIKE '%episode.raw%' OR prosrc LIKE '%episode.number%'
            OR prosrc LIKE '%episode.display%' OR prosrc LIKE '%episode.offset%')) THEN
        RAISE EXCEPTION 'migration 054 self-check failed: notification_episode_facts still carries a legacy payload key';
    END IF;
END $$;
