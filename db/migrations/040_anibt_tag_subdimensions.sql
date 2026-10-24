-- Forward-only migration 040: AniBT customTag splits into sub-dimensions.
--
-- The AniBT tag row's `tag` dimension (AniBT customTag) is split by a
-- server-owned classification table into codec, bitdepth, audio, source,
-- platform and subformat; `tag` keeps only unclassified values
-- (internal/acquisition/domain/anibt_tags.go). Saved AniBT rows are reset to
-- the empty row instead of migrated; the title row is
-- left as it is. Frozen release tag sets are not rewritten: the reader
-- reclassifies `tag:<value>` through the same table, as it does for saved rules.
--
-- Covered: the Global default, every per-anime replace override, and every
-- unlinked candidate draft that carries a tag_filters replace override. A
-- linked (dormant) candidate draft and the saved candidate revisions are
-- immutable by trigger and stay as they are; their AniBT row is normalized
-- when read.

UPDATE global_config_sections
SET value = jsonb_set(value, '{anibt}', '{"require":[],"exclude":[]}'::jsonb),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'tag_filters'
  AND jsonb_typeof(value) = 'object'
  AND value ? 'anibt'
  AND value->'anibt' IS DISTINCT FROM '{"require":[],"exclude":[]}'::jsonb;

UPDATE anime_config_overrides
SET value = jsonb_set(value, '{anibt}', '{"require":[],"exclude":[]}'::jsonb),
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'tag_filters'
  AND jsonb_typeof(value) = 'object'
  AND value ? 'anibt'
  AND value->'anibt' IS DISTINCT FROM '{"require":[],"exclude":[]}'::jsonb;

-- The candidate digest trigger recomputes draft_digest from the new draft when
-- the supplied digest is empty; the version advances by exactly one as the
-- candidate guard requires.
UPDATE anime_config_candidates
SET draft = jsonb_set(draft, '{rules,sections,tag_filters,value,anibt}', '{"require":[],"exclude":[]}'::jsonb),
    draft_digest = '',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE activated_managed_anime_id IS NULL
  AND draft->'rules'->'sections'->'tag_filters'->>'mode' = 'replace'
  AND jsonb_typeof(draft->'rules'->'sections'->'tag_filters'->'value') = 'object'
  AND draft->'rules'->'sections'->'tag_filters'->'value' ? 'anibt'
  AND draft->'rules'->'sections'->'tag_filters'->'value'->'anibt' IS DISTINCT FROM '{"require":[],"exclude":[]}'::jsonb;


-- Notification facts mirror templatefacts.AniBTTagValues: `anibt.tag` keeps
-- rendering every customTag value, so the sub-dimensions fold back into `tag`
-- (a stored legacy `tag:<value>` already is). The function is otherwise the
-- 001 body unchanged.
CREATE FUNCTION notification_anibt_custom_tags(p_tags jsonb) RETURNS jsonb
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
 facts:=jsonb_build_object('rt.title',NULL,'rt.subgroup',NULL,'rt.resolution',NULL,'rt.source',NULL,'rt.codec',NULL,'rt.sub_lang',NULL,
  'anibt.resolution',NULL,'anibt.subtitle',NULL,'anibt.language',NULL,'anibt.format',NULL,'anibt.tag',NULL);
 IF jsonb_typeof(p_vars)='object' THEN
  FOR g IN SELECT * FROM jsonb_each(p_vars) LOOP
   IF jsonb_typeof(g.value)='object' THEN
    FOR k IN SELECT * FROM jsonb_each(g.value) LOOP
     key:='vars.'||g.key||'.'||k.key;
     IF jsonb_typeof(k.value)='string' AND lower(key) !~ '(secret|token|password|credential|url)' THEN
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
  'rt.resolution',public.file_notification_text(public.notification_tag_values(title_tags,'resolution')),
  'rt.source',public.file_notification_text(public.notification_tag_values(title_tags,'source_type')),
  'rt.codec',public.file_notification_text(public.notification_tag_values(title_tags,'video_codec')),
  'rt.sub_lang',public.file_notification_text(public.notification_tag_values(title_tags,'sub_lang')),
  'anibt.resolution',public.file_notification_text(public.notification_tag_values(anibt_tags,'resolution')),
  'anibt.subtitle',public.file_notification_text(public.notification_tag_values(anibt_tags,'subtitle')),
  'anibt.language',public.file_notification_text(public.notification_tag_values(anibt_tags,'language')),
  'anibt.format',public.file_notification_text(public.notification_tag_values(anibt_tags,'format')),
  'anibt.tag',public.file_notification_text(public.notification_tag_values(public.notification_anibt_custom_tags(anibt_tags),'tag')));
END $$;
