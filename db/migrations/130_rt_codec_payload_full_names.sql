-- Forward-only migration 130: the rt codec payload keys take their full names.
--
-- reltitle parses both a video codec and an audio codec; the template facts
-- now expose rt.video_codec and rt.audio_codec. This migration carries the
-- same two keys on the notification payload face so the Go projector
-- (templatecatalog/notification ValueFacts) and the SQL twin keep the same
-- key set: rt.codec is renamed to rt.video_codec (same dimension,
-- video_codec) and rt.audio_codec joins it from the audio_codec dimension.
--
-- The body is migration 070's with only those two keys changed. Nothing
-- backfills: every environment is a test environment, and an envelope frozen
-- before this migration keeps its rt.codec key, which now reads null under
-- the renamed leaf. The comparison item in replacement quality profiles keeps
-- its own key (rt.codec) and is untouched.

CREATE OR REPLACE FUNCTION notification_value_facts(p_vars jsonb, p_source_facts jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE facts jsonb; title_tags jsonb:='[]'::jsonb; anibt_tags jsonb:='[]'::jsonb; g record; k record; key text;
BEGIN
 facts:=jsonb_build_object('rt.title',NULL,'rt.subgroup',NULL,'rt.version',NULL,'rt.resolution',NULL,'rt.source',NULL,'rt.video_codec',NULL,
  'rt.audio_codec',NULL,'rt.sub_lang',NULL,'rt.sub_flag',NULL,'rt.uncensored',NULL,'anibt.resolution',NULL,'anibt.subtitle',NULL,
  'anibt.language',NULL,'anibt.format',NULL,'anibt.tag',NULL);
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
  'rt.video_codec',public.file_notification_text(public.notification_tag_values(title_tags,'video_codec')),
  'rt.audio_codec',public.file_notification_text(public.notification_tag_values(title_tags,'audio_codec')),
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
          AND prosrc LIKE '%rt.video_codec%' AND prosrc LIKE '%rt.audio_codec%'
          AND prosrc NOT LIKE '%rt.codec%') THEN
        RAISE EXCEPTION 'migration 130 self-check failed: notification_value_facts does not carry rt.video_codec and rt.audio_codec';
    END IF;
    projected := public.notification_value_facts('{}'::jsonb,
        '{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["video_codec:HEVC","audio_codec:AAC"]}}'::jsonb);
    IF projected->>'rt.video_codec' IS DISTINCT FROM 'HEVC' OR projected->>'rt.audio_codec' IS DISTINCT FROM 'AAC' THEN
        RAISE EXCEPTION 'migration 130 self-check failed: video/audio codec projected % / %', projected->>'rt.video_codec', projected->>'rt.audio_codec';
    END IF;
    IF projected ? 'rt.codec' THEN
        RAISE EXCEPTION 'migration 130 self-check failed: the retired rt.codec key is still projected';
    END IF;
END $$;
