-- Forward-only migration 095: episode identity is the instance and the
-- episode number; season is not part of it.
--
-- One subscription instance has exactly one identity, so an episode is keyed
-- by the instance plus its number. The resolver (episode-identity-v2) now
-- writes coverage keys tv:e<N> and movie:0; season stays a naming-only owner
-- field. Sealed acquisition inputs keep their tv:s<S>:e<N> keys and their
-- episode-identity-v1 version forever, so both shapes stay valid.
--
-- The file-fact source relation check is redefined from its latest
-- definition (049_release_src_payload_key.sql) with exactly one change: the
-- identity coverage schema may be episode-identity-v1 or episode-identity-v2.
-- The Go file-fact writer records the version the frozen identity resolved
-- with.

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
 IF COALESCE(p_fact->'identity_coverage'->>'coverage_schema','') NOT IN ('episode-identity-v1','episode-identity-v2')
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

-- The media kind is an owner field (anime_metadata.media_kind_override, NULL
-- is tv): an identity reads only the owner kind, never the TMDB kind. The
-- owner kind is backfilled once from the TMDB kind when a TMDB tuple is bound
-- while it is NULL; clearing or replacing the tuple later never changes it.
-- Rows bound before this rule receive that one-time backfill here, so every
-- bound row keeps the kind it resolved with.
UPDATE anime_metadata
SET media_kind_override = tmdb_media_kind
WHERE media_kind_override IS NULL
  AND tmdb_media_kind IS NOT NULL;
