-- One reusable bangumi-data snapshot backs all local metadata lookups.
-- The payload is replaceable reference data, not per-subscription owner state.

CREATE TABLE bangumi_data_snapshots (
    singleton_id TEXT NOT NULL DEFAULT 'default',
    upstream_version TEXT NOT NULL,
    payload BYTEA NOT NULL,
    last_modified TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT bangumi_data_snapshots_pkey PRIMARY KEY (singleton_id),
    CONSTRAINT bangumi_data_snapshots_singleton CHECK (singleton_id = 'default'),
    CONSTRAINT bangumi_data_snapshots_version_check CHECK (upstream_version ~ '^0\.3\.[0-9]+$'),
    CONSTRAINT bangumi_data_snapshots_payload_size CHECK (octet_length(payload) BETWEEN 2 AND 33554432),
    CONSTRAINT bangumi_data_snapshots_last_modified CHECK (octet_length(last_modified) <= 256),
    CONSTRAINT bangumi_data_snapshots_times CHECK (checked_at >= fetched_at)
);
COMMENT ON TABLE bangumi_data_snapshots IS 'Latest usable bangumi-data CDN payload; stale snapshots remain usable when refresh fails.';

REVOKE ALL ON TABLE bangumi_data_snapshots FROM bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE bangumi_data_snapshots TO bansoko_runtime;

-- Keep the SQL notification projectors key-for-key equal to the simplified
-- Go catalog. This changes only projection names: source tag dimensions and
-- frozen evidence stay unchanged, and no retired key is retained as an alias.
CREATE OR REPLACE FUNCTION notification_title_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.title',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title}') #>> '{}'),
 'bgm.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title_cn}') #>> '{}'),
 'tmdb.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{title_cn}') #>> '{}'),
 'tmdb.title',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{original_title}') #>> '{}'),
 'tmdb.summary',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{overview}') #>> '{}'))
$$;

CREATE OR REPLACE FUNCTION notification_value_facts(p_vars jsonb, p_source_facts jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE facts jsonb; title_tags jsonb:='[]'::jsonb; anibt_tags jsonb:='[]'::jsonb; g record; k record; key text;
BEGIN
 facts:=jsonb_build_object('rt.title',NULL,'rt.group',NULL,'rt.rev',NULL,'rt.res',NULL,'rt.source',NULL,'rt.vc',NULL,
  'rt.ac',NULL,'rt.lang',NULL,'rt.subflag',NULL,'rt.uncensored',NULL,'anibt.res',NULL,'anibt.subtitle',NULL,
  'anibt.lang',NULL,'anibt.format',NULL,'anibt.tag',NULL);
 IF jsonb_typeof(p_vars)='object' THEN
  FOR g IN SELECT * FROM jsonb_each(p_vars) LOOP
   IF jsonb_typeof(g.value)='object' THEN
    FOR k IN SELECT * FROM jsonb_each(g.value) LOOP
     key:='vars.'||g.key||'.'||k.key;
     IF jsonb_typeof(k.value)='string' AND lower(key) !~ '(secret|token|pass(word)|credential|url)' THEN
      facts:=facts||jsonb_build_object(key,public.file_notification_text(k.value #>> '{}'));
     END IF;
    END LOOP;
   END IF;
  END LOOP;
 END IF;
 IF p_source_facts IS NULL OR jsonb_typeof(p_source_facts)='null' THEN RETURN facts; END IF;
 IF jsonb_typeof(p_source_facts)<>'object'
  OR NOT public.notification_tag_block_valid(p_source_facts->'title_tags')
  OR NOT public.notification_tag_block_valid(p_source_facts->'anibt') THEN RETURN facts; END IF;
 IF jsonb_typeof(p_source_facts->'title_tags')='object' THEN
  IF jsonb_typeof(p_source_facts->'title_tags'->'tags')='array' THEN title_tags:=p_source_facts->'title_tags'->'tags'; END IF;
  IF p_source_facts->'title_tags'->>'schema'='reltitle-attrs-v1' AND jsonb_typeof(p_source_facts->'title_tags'->'value_tags')='array' THEN
   title_tags:=title_tags||(p_source_facts->'title_tags'->'value_tags'); END IF;
 END IF;
 IF jsonb_typeof(p_source_facts->'anibt')='object' AND jsonb_typeof(p_source_facts->'anibt'->'tags')='array' THEN
  anibt_tags:=p_source_facts->'anibt'->'tags'; END IF;
 RETURN facts||jsonb_build_object(
  'rt.title',public.file_notification_text(public.notification_tag_values(title_tags,'title')),
  'rt.group',public.file_notification_text(public.notification_tag_values(title_tags,'group')),
  'rt.rev',public.file_notification_text(public.notification_tag_values(title_tags,'version')),
  'rt.res',public.file_notification_text(public.notification_tag_values(title_tags,'resolution')),
  'rt.source',public.file_notification_text(public.notification_tag_values(title_tags,'source_type')),
  'rt.vc',public.file_notification_text(public.notification_tag_values(title_tags,'video_codec')),
  'rt.ac',public.file_notification_text(public.notification_tag_values(title_tags,'audio_codec')),
  'rt.lang',public.file_notification_text(public.notification_tag_values(title_tags,'sub_lang')),
  'rt.subflag',public.file_notification_text(public.notification_tag_values(title_tags,'sub_flag')),
  'rt.uncensored',CASE WHEN p_source_facts->'title_tags'->>'schema'='reltitle-attrs-v1'
    AND COALESCE(p_source_facts->'title_tags'->>'diagnostic','')=''
   THEN to_jsonb(COALESCE(jsonb_typeof(p_source_facts->'title_tags'->'tags')='array'
    AND p_source_facts->'title_tags'->'tags' @> jsonb_build_array('content_rating:UNCENSORED'),false)) END,
  'anibt.res',public.file_notification_text(public.notification_tag_values(anibt_tags,'resolution')),
  'anibt.subtitle',public.file_notification_text(public.notification_tag_values(anibt_tags,'subtitle')),
  'anibt.lang',public.file_notification_text(public.notification_tag_values(anibt_tags,'language')),
  'anibt.format',public.file_notification_text(public.notification_tag_values(anibt_tags,'format')),
  'anibt.tag',public.file_notification_text(public.notification_tag_values(public.notification_anibt_custom_tags(anibt_tags),'tag')));
END $$;

DO $$
DECLARE projected jsonb;
BEGIN
    projected := public.notification_value_facts('{}'::jsonb,
        '{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["resolution:1080p","video_codec:HEVC","audio_codec:AAC","sub_lang:SC"],"value_tags":["group:SampleGroup","version:v2"]},"anibt":{"schema":"anibt-rss-1.0","tags":["resolution:1080p","language:CHS"]}}'::jsonb);
    IF projected->>'rt.group' IS DISTINCT FROM 'SampleGroup'
       OR projected->>'rt.rev' IS DISTINCT FROM 'v2'
       OR projected->>'rt.res' IS DISTINCT FROM '1080p'
       OR projected->>'rt.vc' IS DISTINCT FROM 'HEVC'
       OR projected->>'rt.ac' IS DISTINCT FROM 'AAC'
       OR projected->>'rt.lang' IS DISTINCT FROM 'SC'
       OR projected->>'anibt.res' IS DISTINCT FROM '1080p'
       OR projected->>'anibt.lang' IS DISTINCT FROM 'CHS'
       OR projected ? 'rt.subgroup' OR projected ? 'rt.version'
       OR projected ? 'rt.resolution' OR projected ? 'rt.video_codec'
       OR projected ? 'rt.audio_codec' OR projected ? 'rt.sub_lang'
       OR projected ? 'anibt.resolution' OR projected ? 'anibt.language' THEN
        RAISE EXCEPTION 'migration 132 self-check failed: simplified notification keys projected %', projected;
    END IF;
    projected := public.notification_title_facts('{"media":{"tmdb_titles":{"state":"known","value":{"original_title":"Original Title"}}}}'::jsonb);
    IF projected->>'tmdb.title' IS DISTINCT FROM 'Original Title' OR projected ? 'tmdb.original_title' THEN
        RAISE EXCEPTION 'migration 132 self-check failed: simplified TMDB title keys projected %', projected;
    END IF;
END $$;
