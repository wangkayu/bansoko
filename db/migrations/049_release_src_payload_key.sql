-- Forward-only migration 049: the notification payload keys rename
-- src.kind -> release.src and swap the release group for the source's
-- configured group name (anime.group), withdrawal of the title-prefix chain.
--
-- Points 2 and 4 of the group-provenance ruling (10-22-fact-catalog-reconcile
-- reconciliation.md): the template face keeps anime.group (the provider or
-- custom display group name the origin RSS source recorded, no fallback of
-- any kind) and release.src (the former src.kind, identical values), and
-- drops release.group / release.group_source / src.group_name entirely. On
-- the payload face this means:
--
--   - notification_input_facts stores "release.src" (the release's provider,
--     anonymity-free alias of the stored "provider.kind" key) and adds
--     "anime.group" (source.group, frozen) beside it; both media and workflow
--     build on it, so every payload carries them.
--   - notification_release_facts drops its "release.group_name" key; the
--     group name reaches the payload only as anime.group now.
--   - download_failed_notification stores "release.src" instead of "src.kind"
--     (the explicit key keeps winning the merge over the input-facts
--     provider value, so a failed download still names its source type
--     provider_group | custom_rss).
--   - file_success_notification (as re-created by 048) and
--     file_failure_notification drop their inline "release.group_name"
--     emission; the group comes from the input-facts merge like everywhere
--     else.
--   - filefact_source_relation_valid (as re-created by 004, the Go-side
--     render-fact validator) drops its "Derived.GroupName" replay key, whose
--     enveloped key the Go writer no longer records.
--
-- The Go builders mirror each function key-for-key
-- (templatecatalog/notification InputFacts, fileoperation/application
-- buildFileOperationNotificationPayload, rss/application
-- releaseDiscoveredFacts, download/application downloadSubmittedFacts) and
-- the parity tests keep them equal. Nothing backfills: every environment is
-- a test environment and the payload keys can stay unrenamed inside an
-- envelope frozen before this migration, where they simply read null under
-- the withdrawn leaves. The baseline stays immutable; this file is applied
-- after it by cmd/bansoko-migrate and is checksummed in schema_migrations.

CREATE OR REPLACE FUNCTION notification_input_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'anime.id',public.notification_known(p_input->'config'->'identity'->'managed_anime_ref','{}'),
 'release.src',public.notification_known(p_input->'source'->'provider','{}'),
 'anime.group',public.notification_known(p_input->'source'->'group','{}'),
 'bangumi.id',public.notification_known(p_input->'media'->'bgm','{ref}'),
 'tmdb.id',public.notification_known(p_input->'media'->'tmdb','{work_ref}'),
 'tmdb.media_kind',public.notification_known(p_input->'media'->'tmdb','{kind}'),
 'tmdb.season',public.notification_known(p_input->'media'->'tmdb','{season}'),
 'torrent.info_hash_v1',public.notification_known(p_input->'source'->'infohash_v1','{}'),
 'torrent.info_hash_v2',public.notification_known(p_input->'source'->'infohash_v2','{}'),
 'torrent.size',public.notification_known(p_input->'source'->'torrent_size','{}'),
 'torrent.file_count',public.notification_known(p_input->'source'->'torrent_file_count','{}'))
$$;

-- notification_release_facts keeps the release identity and title only.
CREATE OR REPLACE FUNCTION notification_release_facts(p_input jsonb, p_release_id text) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
  'release.id',public.file_notification_text(COALESCE(p_release_id,
     CASE WHEN p_input->'source'->'release'->>'state'='known' THEN p_input->'source'->'release'->'value'->>'ref' END)),
  'release.title',public.file_notification_text(CASE WHEN p_input->'source'->'title'->>'state'='known' THEN p_input->'source'->'title'->>'value' END))
$$;

-- download_failed_notification is the twin of the workflow release.src value:
-- the source type of the RSS source the task belongs to.
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

-- file_success_notification is the byte-parity mirror the 039 finish gate
-- compares the Go-built payload against (FILE_NOTIFICATION_PROOF_MISMATCH).
-- It is re-created as the 048 body without the inline group emission: the
-- group reaches the payload through notification_input_facts, as anime.group.
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

-- file_failure_notification follows the same shape: the 001 body without its
-- inline group emission.
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
 'file.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'file.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'file.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'file.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'output.directory',NULL,'output.filename',NULL,'output.relative_path',NULL,
 'file_operation.kind',public.file_notification_text(o.operation),
 'file_operation.collision_policy',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

-- The file-fact source relation check (004) names every replay key the Go
-- writer records in the fact; the peer of the withdrawn naming leaf is gone,
-- so it is dropped from the validator's list too (the Go writer no longer
-- enumerates it). The rest is byte-identical to 004.
CREATE OR REPLACE FUNCTION filefact_source_relation_valid(p_root text, p_artifact text, p_ref text, p_fact jsonb) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE i jsonb; f public.artifact_files%ROWTYPE; aw public.acquisitions%ROWTYPE; k text; expected jsonb; g record; member record; paths text[]:='{}'; listed text[]; render jsonb; binding jsonb; replay jsonb; offset_group text; video_count jsonb;
BEGIN
 SELECT input INTO STRICT i FROM public.acquisitions WHERE id=p_root;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=p_artifact AND acquisition_id=p_root AND file_ref=p_ref;
 offset_group:=CASE WHEN p_fact->'schema_version'='2'::jsonb THEN 'episode_offset' ELSE 'source_offset' END;
 IF p_fact->'provenance'->'artifact_file_ref' IS DISTINCT FROM jsonb_build_object('state','known','value',f.id,'reason_code',NULL)
 OR p_fact->'content_source'->'file_ref' IS DISTINCT FROM jsonb_build_object('state','known','value',f.file_ref,'reason_code',NULL)
 OR p_fact->'content_source'->'relative_path' IS DISTINCT FROM jsonb_build_object('state','known','value',f.relative_path,'reason_code',NULL)
 OR p_fact->'content_source'->'original_name' IS DISTINCT FROM jsonb_build_object('state','known','value',regexp_replace(f.relative_path,'^.*/',''),'reason_code',NULL)
 OR p_fact->'content_source'->'media_kind' IS DISTINCT FROM jsonb_build_object('state','known','value',f.media_kind,'reason_code',NULL) THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['payload_digest','manifest_digest'] LOOP
 IF p_fact->'content_source'->k IS DISTINCT FROM jsonb_build_object('state','known','value',i->'source'->k,'reason_code',NULL) THEN RETURN false; END IF;
 END LOOP;
 FOREACH k IN ARRAY ARRAY['infohash_v1','infohash_v2','title','group','provider','locator_display','release'] LOOP
 IF p_fact->'content_source'->k IS DISTINCT FROM i->'source'->k THEN RETURN false; END IF;
 END LOOP;
 video_count:=jsonb_build_object('state','known','value',(SELECT count(*) FROM public.artifact_files WHERE artifact_id=p_artifact AND media_kind='video'),'reason_code',NULL);
 IF p_fact->'identity_coverage'->'identity' IS DISTINCT FROM i->'media'->'identity'
 OR p_fact->'quality' IS DISTINCT FROM i->'media'->'quality'
 OR p_fact->offset_group->'video_count' IS DISTINCT FROM video_count THEN RETURN false; END IF;
 IF offset_group='episode_offset' THEN
  expected:=CASE WHEN i->'media'->'identity'->>'state'='known' THEN jsonb_build_object('state','known','value',i->'media'->'identity'->'value'->'offset','reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
  IF p_fact->'episode_offset'->'offset' IS DISTINCT FROM expected THEN RETURN false; END IF;
 ELSE
  IF p_fact->'source_offset'->'used_offset' IS DISTINCT FROM i->'media'->'source_offset' THEN RETURN false; END IF;
  FOREACH k IN ARRAY ARRAY['source','override'] LOOP
  IF p_fact->'source_offset'->k IS DISTINCT FROM jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') THEN RETURN false; END IF; END LOOP;
 END IF;
 IF p_fact->'identity_coverage'->>'coverage_schema' IS DISTINCT FROM 'episode-identity-v1'
 OR p_fact->'identity_coverage'->'coverage_keys' IS DISTINCT FROM (CASE WHEN i->'media'->'identity'->>'state'='known' THEN i->'media'->'identity'->'value'->'coverage_keys' ELSE '[]'::jsonb END) THEN RETURN false; END IF;
 SELECT render_snapshot INTO STRICT render FROM public.acquisition_files WHERE acquisition_id=p_root AND file_ref=p_ref;
 binding:=render->'binding_facts'; replay:=render->'render_facts';
 FOREACH k IN ARRAY ARRAY['File.Media.Resolution','File.Media.VideoCodec','File.Media.Container','Parsed.Language.Code','Parsed.Episode.Display','Source.GroupName','Source.Group','Release.OriginalTitle','Release.FrozenEpisode.Display','Anime.BGM.WorkID','Anime.TMDB.WorkID'] LOOP
 expected:=CASE WHEN replay ? k THEN jsonb_build_object('state','known','value',replay->k,'reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'replay'->k IS DISTINCT FROM expected THEN RETURN false; END IF;
 END LOOP;
 IF binding->>'media_kind' IN ('tv','movie') THEN
 expected:=jsonb_build_object('media_kind',binding->'media_kind','season',binding->'season','season_bound',binding->'season_bound');
 FOREACH k IN ARRAY ARRAY['bgm_work_id','tmdb_work_id','effective_title','extension'] LOOP
 expected:=expected||jsonb_build_object(k,CASE WHEN binding ? k AND jsonb_typeof(binding->k)='string' THEN CASE WHEN binding->>k<>'' OR k='extension' THEN jsonb_build_object('state','known','value',binding->k,'reason_code',NULL) ELSE jsonb_build_object('state','absent','value',NULL,'reason_code',NULL) END ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END);
 END LOOP;
 expected:=jsonb_build_object('state','known','value',expected,'reason_code',NULL);
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 IF p_fact->'render_inputs'->'binding' IS DISTINCT FROM expected
 OR p_fact->'render_inputs'->'naming_schema' IS DISTINCT FROM jsonb_build_object('state','known','value','naming-rules-v1','reason_code',NULL) THEN RETURN false; END IF;
 expected:=CASE WHEN COALESCE(render->>'renderer_version','')<>'' THEN jsonb_build_object('state','known','value',render->>'renderer_version','reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'render_program_version' IS DISTINCT FROM expected THEN RETURN false; END IF;
 SELECT public.vars_member_bytes(public.vars_member_bytes(input_canonical_utf8,'delivery'),'naming') INTO k FROM public.acquisitions WHERE id=p_root;
 expected:=CASE WHEN k IS NOT NULL THEN jsonb_build_object('state','known','value','sha256:'||encode(sha256(convert_to('acquisition-naming:v1','UTF8')||decode('00','hex')||convert_to(k,'UTF8')),'hex'),'reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'naming_digest' IS DISTINCT FROM expected THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['media_kind','season','season_bound'] LOOP
 IF i->'media'->'tmdb'->>'state'='known' THEN
 expected:=jsonb_build_object('state','known','value',(CASE k WHEN 'media_kind' THEN i->'media'->'tmdb'->'value'->'kind' WHEN 'season' THEN i->'media'->'tmdb'->'value'->'season' ELSE 'true'::jsonb END),'reason_code',NULL);
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 IF p_fact->offset_group->k IS DISTINCT FROM expected THEN RETURN false; END IF; END LOOP;
 FOR g IN SELECT * FROM jsonb_each(p_fact) LOOP
 IF jsonb_typeof(g.value)<>'object' THEN CONTINUE; END IF;
 FOR member IN SELECT * FROM jsonb_each(g.value) LOOP
 IF public.observed_closed(member.value,ARRAY['state','value','reason_code']) IS TRUE AND member.value->>'state'='unavailable' THEN
 paths:=array_append(paths,'/'||replace(replace(g.key,'~','~0'),'/','~1')||'/'||replace(replace(member.key,'~','~0'),'/','~1'));
 END IF; END LOOP; END LOOP;
 SELECT COALESCE(array_agg(v ORDER BY v COLLATE "C"),'{}'::text[]) INTO paths FROM unnest(paths) v;
 SELECT COALESCE(array_agg(v ORDER BY n),'{}'::text[]) INTO listed FROM jsonb_array_elements_text(p_fact->'provenance'->'missing_paths') WITH ORDINALITY x(v,n);
 IF listed IS DISTINCT FROM paths THEN RETURN false; END IF;
 SELECT * INTO STRICT aw FROM public.acquisitions WHERE id=p_root;
 IF aw.preparation_witness_canonical_utf8 IS NOT NULL THEN
 expected:=jsonb_build_object('state','known','reason_code',NULL,'value',jsonb_build_object('relation','acquisition_preparation_witness_v1','row_ref',p_root,'version',jsonb_build_object('state','known','value',1,'reason_code',NULL),'digest',jsonb_build_object('state','known','value',aw.preparation_digest,'reason_code',NULL),'observed_at',aw.preparation_witness->'observation'->'observed_at','selection','recorded_reference'));
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 RETURN p_fact->'provenance'->'exact_selection'=expected;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $$;

-- The re-created shapes check themselves against payloads the projectors
-- would read: an input whose source carries the provider and a configured
-- group projects release.src and anime.group beside the unchanged keys, a
-- source without either projects null (never a stand-in value), the
-- download_failed projection wins the merge with its source type, and no
-- re-created projector emits a withdrawn group key.
DO $$
DECLARE
    input jsonb;
    facts jsonb;
BEGIN
    input := jsonb_build_object(
        'config', jsonb_build_object('identity', jsonb_build_object('managed_anime_ref', jsonb_build_object('state','known','value','ma-049'))),
        'source', jsonb_build_object(
            'provider', jsonb_build_object('state','known','value','anibt'),
            'group', jsonb_build_object('state','known','value','LoliHouse'),
            'title', jsonb_build_object('state','known','value','[Group] 049 - 07'),
            'release', jsonb_build_object('state','known','value', jsonb_build_object('ref','rel-049')),
            'infohash_v1', jsonb_build_object('state','known','value','0491'),
            'infohash_v2', jsonb_build_object('state','known','value','0492'),
            'torrent_size', jsonb_build_object('state','known','value',734003200),
            'torrent_file_count', jsonb_build_object('state','known','value',1)),
        'media', jsonb_build_object('bgm', jsonb_build_object('state','known','value', jsonb_build_object('ref','425998'))));
    facts := public.notification_input_facts(input);
    IF facts ->> 'release.src' IS DISTINCT FROM 'anibt'
       OR facts ->> 'anime.group' IS DISTINCT FROM 'LoliHouse'
       OR facts ->> 'anime.id' IS DISTINCT FROM 'ma-049'
       OR (facts ->> 'provider.kind') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 049 self-check failed: notification_input_facts did not project release.src / anime.group: %', facts;
    END IF;
    input := jsonb_build_object('source', jsonb_build_object(
        'provider', jsonb_build_object('state','absent','value',NULL),
        'group', jsonb_build_object('state','absent','value',NULL)));
    facts := public.notification_input_facts(input);
    IF (facts ->> 'anime.group') IS NOT NULL OR (facts ->> 'release.src') IS NOT NULL THEN
        RAISE EXCEPTION 'migration 049 self-check failed: an absent source read a non-null release.src / anime.group: %', facts;
    END IF;
    IF jsonb_build_object('release.id','rel-049','release.title',NULL) <> public.notification_release_facts(input, 'rel-049') THEN
        RAISE EXCEPTION 'migration 049 self-check failed: notification_release_facts changed beyond the group withdrawal';
    END IF;
    -- The plpgsql twins are checked from their stored source (prosrc): the
    -- failed-download projector wins its merge with release.src, and the two
    -- file projectors carry no withdrawn group key.
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'download_failed_notification'
          AND prosrc LIKE '%release.src%') THEN
        RAISE EXCEPTION 'migration 049 self-check failed: download_failed_notification does not emit release.src';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname IN ('file_success_notification','file_failure_notification')
          AND prosrc LIKE '%release.group_name%') THEN
        RAISE EXCEPTION 'migration 049 self-check failed: the file projections still carry a group key';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'filefact_source_relation_valid'
          AND prosrc LIKE '%Derived.GroupName%') THEN
        RAISE EXCEPTION 'migration 049 self-check failed: the source relation check still names the withdrawn replay key';
    END IF;
END $$;
