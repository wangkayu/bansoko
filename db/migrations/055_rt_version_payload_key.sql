-- Forward-only migration 055: the rt.version payload key.
--
-- templatefacts.ReltitleFields adds Version, so the Go notification builders
-- emit rt.version with every new envelope. Re-create the shared SQL projector
-- with the same key in its seed object and return projection. Envelopes frozen
-- before this migration keep their original payload; old source_facts blocks
-- without version:... project null and are never re-parsed or backfilled.
CREATE OR REPLACE FUNCTION notification_value_facts(p_vars jsonb, p_source_facts jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE facts jsonb; title_tags jsonb:='[]'::jsonb; anibt_tags jsonb:='[]'::jsonb; g record; k record; key text;
BEGIN
 facts:=jsonb_build_object('rt.title',NULL,'rt.subgroup',NULL,'rt.version',NULL,'rt.resolution',NULL,'rt.source',NULL,'rt.codec',NULL,'rt.sub_lang',NULL,
  'rt.sub_flag',NULL,'anibt.resolution',NULL,'anibt.subtitle',NULL,'anibt.language',NULL,'anibt.format',NULL,'anibt.tag',NULL);
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
  'anibt.resolution',public.file_notification_text(public.notification_tag_values(anibt_tags,'resolution')),
  'anibt.subtitle',public.file_notification_text(public.notification_tag_values(anibt_tags,'subtitle')),
  'anibt.language',public.file_notification_text(public.notification_tag_values(anibt_tags,'language')),
  'anibt.format',public.file_notification_text(public.notification_tag_values(anibt_tags,'format')),
  'anibt.tag',public.file_notification_text(public.notification_tag_values(anibt_tags,'tag')));
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'notification_value_facts'
          AND prosrc LIKE '%''rt.version'',NULL%' AND prosrc LIKE '%''rt.version'',public.file_notification_text%') THEN
        RAISE EXCEPTION 'migration 055 self-check failed: notification_value_facts does not emit rt.version';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc WHERE proname IN ('file_success_notification','file_failure_notification','download_failed_notification')
          AND prosrc LIKE '%notification_value_facts%') IS FALSE THEN
        RAISE EXCEPTION 'migration 055 self-check failed: a notification projector no longer composes the shared value facts';
    END IF;
END $$;
