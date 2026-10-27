-- Forward-only migration 070: restore the AniBT customTag fold in the shared
-- SQL notification projector.
--
-- Migration 040 split an AniBT customTag into the codec, bitdepth, audio,
-- source, platform and subformat sub-dimensions and folded them back into
-- `anibt.tag` with notification_anibt_custom_tags before reading the tag
-- dimension. Migration 051 re-created notification_value_facts from the 001
-- body and dropped that fold; 055 and 066 kept the fold-less call, so a
-- release whose customTag values are all classified (the persisted
-- source_facts shape) projected `anibt.tag` = null while
-- templatefacts.AniBTTagValues renders "10bit+AAC+HEVC+WEBRip". The finish
-- gate compares the Go payload with this function
-- (FILE_NOTIFICATION_PROOF_MISMATCH), so the merge is restored here.
--
-- The body is the one from 066 with only `anibt.tag` changed. The fold is the
-- one from 040, re-stated so this migration is self-contained: a tag whose
-- dimension is a customTag sub-dimension becomes "tag:<value>" (a `tag:` entry
-- is already one) and keeps its position, so notification_tag_values orders
-- and de-duplicates (case-insensitively, first spelling wins) exactly as
-- templatefacts.JoinValues does. `tag` remains the only dimension read for
-- `anibt.tag`; an unrecognised dimension is never merged.
--
-- No other SQL projector emits `anibt.tag`: file_success_notification,
-- file_failure_notification and download_failed_notification all compose
-- notification_value_facts, and notification_tag_block/ anibt_tag_block only
-- normalize the stored block. Envelopes frozen before this migration keep
-- their payload; source_facts are never re-parsed or backfilled.

CREATE OR REPLACE FUNCTION notification_anibt_custom_tags(p_tags jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT COALESCE(jsonb_agg(CASE
   WHEN strpos(t.tag,':')>1 AND left(t.tag,strpos(t.tag,':')-1) IN ('codec','bitdepth','audio','source','platform','subformat')
   THEN 'tag:'||substr(t.tag,strpos(t.tag,':')+1) ELSE t.tag END ORDER BY t.ord),'[]'::jsonb)
 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(p_tags)='array' THEN p_tags ELSE '[]'::jsonb END) WITH ORDINALITY AS t(tag,ord)
$$;

CREATE OR REPLACE FUNCTION notification_value_facts(p_vars jsonb, p_source_facts jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE facts jsonb; title_tags jsonb:='[]'::jsonb; anibt_tags jsonb:='[]'::jsonb; g record; k record; key text;
BEGIN
 facts:=jsonb_build_object('rt.title',NULL,'rt.subgroup',NULL,'rt.version',NULL,'rt.resolution',NULL,'rt.source',NULL,'rt.codec',NULL,'rt.sub_lang',NULL,
  'rt.sub_flag',NULL,'rt.uncensored',NULL,'anibt.resolution',NULL,'anibt.subtitle',NULL,'anibt.language',NULL,'anibt.format',NULL,'anibt.tag',NULL);
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
  'rt.subgroup',public.file_notification_text(public.notification_tag_values(title_tags,'group')),
  'rt.version',public.file_notification_text(public.notification_tag_values(title_tags,'version')),
  'rt.resolution',public.file_notification_text(public.notification_tag_values(title_tags,'resolution')),
  'rt.source',public.file_notification_text(public.notification_tag_values(title_tags,'source_type')),
  'rt.codec',public.file_notification_text(public.notification_tag_values(title_tags,'video_codec')),
  'rt.sub_lang',public.file_notification_text(public.notification_tag_values(title_tags,'sub_lang')),
  'rt.sub_flag',public.file_notification_text(public.notification_tag_values(title_tags,'sub_flag')),
  'rt.uncensored',CASE WHEN p_source_facts->'title_tags'->>'schema'='reltitle-attrs-v1'
    AND COALESCE(p_source_facts->'title_tags'->>'diagnostic','')=''
   THEN to_jsonb(COALESCE(jsonb_typeof(p_source_facts->'title_tags'->'tags')='array'
    AND p_source_facts->'title_tags'->'tags' @> jsonb_build_array('content_rating:UNCENSORED'),false)) END,
  'anibt.resolution',public.file_notification_text(public.notification_tag_values(anibt_tags,'resolution')),
  'anibt.subtitle',public.file_notification_text(public.notification_tag_values(anibt_tags,'subtitle')),
  'anibt.language',public.file_notification_text(public.notification_tag_values(anibt_tags,'language')),
  'anibt.format',public.file_notification_text(public.notification_tag_values(anibt_tags,'format')),
  'anibt.tag',public.file_notification_text(public.notification_tag_values(public.notification_anibt_custom_tags(anibt_tags),'tag')));
END $$;

DO $$
DECLARE projected jsonb;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'notification_value_facts'
          AND prosrc LIKE '%notification_anibt_custom_tags(anibt_tags)%') THEN
        RAISE EXCEPTION 'migration 070 self-check failed: notification_value_facts does not fold the AniBT sub-dimensions into anibt.tag';
    END IF;
    -- The fold is the templatefacts.AniBTTagValues merge: a classified
    -- customTag reads as `tag:<value>` in place, and the tag dimension joins
    -- trimmed, case-insensitively de-duplicated values in case-folded order.
    projected := public.notification_value_facts('{}'::jsonb,
        '{"anibt":{"schema":"anibt-rss-1.0","tags":["codec:HEVC","bitdepth:10bit","audio:AAC","source:WEBRip"]}}'::jsonb);
    IF projected->>'anibt.tag' IS DISTINCT FROM '10bit+AAC+HEVC+WEBRip' THEN
        RAISE EXCEPTION 'migration 070 self-check failed: sub-dimension tags projected anibt.tag = %', projected->>'anibt.tag';
    END IF;
    projected := public.notification_value_facts('{}'::jsonb,
        '{"anibt":{"schema":"anibt-rss-1.0","tags":["tag:xxx","codec:HEVC","tag:10bit","bitdepth:10bit","audio:AAC","audio:aac","source:WEBRip","mystery:drop"]}}'::jsonb);
    IF projected->>'anibt.tag' IS DISTINCT FROM '10bit+AAC+HEVC+WEBRip+xxx' THEN
        RAISE EXCEPTION 'migration 070 self-check failed: mixed legacy and classified tags projected anibt.tag = %', projected->>'anibt.tag';
    END IF;
    projected := public.notification_value_facts('{}'::jsonb,
        '{"anibt":{"schema":"anibt-rss-1.0","tags":["codec:","audio:","format:MKV"]}}'::jsonb);
    IF projected->>'anibt.tag' IS NOT NULL OR projected->>'anibt.format' IS DISTINCT FROM 'MKV' THEN
        RAISE EXCEPTION 'migration 070 self-check failed: an empty sub-dimension value reached anibt.tag = %', projected->>'anibt.tag';
    END IF;
END $$;
