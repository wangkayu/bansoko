-- Add file.size as an optional profile-v2 comparison item (ranks: [] when skipped,
-- [["larger"]] to prefer larger files, or [["smaller"]] to prefer smaller files).
CREATE OR REPLACE FUNCTION replacement_quality_profile_valid(profile jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
 item jsonb;
 entry jsonb;
 val jsonb;
 keys text[] := ARRAY[]::text[];
 signatures text[] := ARRAY[]::text[];
 signature text;
 kind text;
BEGIN
 IF profile IS NULL OR jsonb_typeof(profile) IS DISTINCT FROM 'object'
    OR (SELECT count(*) FROM jsonb_object_keys(profile)) <> 2
    OR NOT (profile ?& ARRAY['version','items'])
    OR profile->>'version' IS DISTINCT FROM 'profile-v2'
    OR jsonb_typeof(profile->'items') IS DISTINCT FROM 'array'
    OR jsonb_array_length(profile->'items') > 13 THEN
  RETURN FALSE;
 END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(profile->'items') LOOP
  IF jsonb_typeof(item) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
     OR NOT (item ?& ARRAY['key','ranks'])
     OR jsonb_typeof(item->'key') IS DISTINCT FROM 'string'
     OR item->>'key' NOT IN ('subgroup.score','rt.revision','file.size',
                             'anibt.resolution','anibt.language','anibt.subtitle','anibt.format',
                             'rt.resolution','rt.sub_lang','rt.sub_flag','rt.source','rt.codec',
                             'rt.content_rating')
     OR item->>'key' = ANY(keys)
     OR jsonb_typeof(item->'ranks') IS DISTINCT FROM 'array'
     OR jsonb_array_length(item->'ranks') > 64 THEN
   RETURN FALSE;
  END IF;
  keys := array_append(keys, item->>'key');
  kind := CASE WHEN item->>'key' IN ('subgroup.score','rt.revision') THEN 'unranked'
               WHEN item->>'key' = 'file.size' THEN 'size'
               WHEN item->>'key' IN ('anibt.language','rt.sub_lang','rt.sub_flag') THEN 'multi'
               ELSE 'single' END;
  IF kind='unranked' AND jsonb_array_length(item->'ranks')<>0 THEN
   RETURN FALSE;
  END IF;
  IF kind='size' THEN
   IF jsonb_array_length(item->'ranks') = 0 THEN
    CONTINUE;
   END IF;
   IF jsonb_array_length(item->'ranks') <> 1
      OR jsonb_typeof(item->'ranks'->0) IS DISTINCT FROM 'array'
      OR jsonb_array_length(item->'ranks'->0) <> 1
      OR jsonb_typeof(item->'ranks'->0->0) IS DISTINCT FROM 'string'
      OR (item->'ranks'->0->>0) NOT IN ('larger', 'smaller') THEN
    RETURN FALSE;
   END IF;
   CONTINUE;
  END IF;
  signatures := ARRAY[]::text[];
  FOR entry IN SELECT value FROM jsonb_array_elements(item->'ranks') LOOP
   IF jsonb_typeof(entry) IS DISTINCT FROM 'array'
      OR jsonb_array_length(entry) NOT BETWEEN 1 AND 8
      OR (kind='single' AND jsonb_array_length(entry)<>1) THEN
    RETURN FALSE;
   END IF;
   FOR val IN SELECT value FROM jsonb_array_elements(entry) LOOP
    IF jsonb_typeof(val) IS DISTINCT FROM 'string'
       OR btrim(val #>> '{}', E' \t\n\v\f\r\u0085                 　') = ''
       OR btrim(val #>> '{}', E' \t\n\v\f\r\u0085                 　') IS DISTINCT FROM val #>> '{}'
       OR octet_length(val #>> '{}') > 128 OR strpos(val #>> '{}','+')>0 THEN
     RETURN FALSE;
    END IF;
    IF (SELECT count(*) FROM jsonb_array_elements_text(entry) other
        WHERE lower(other)=lower(val #>> '{}'))>1 THEN
     RETURN FALSE;
    END IF;
   END LOOP;
   SELECT string_agg(lower(v),'+' ORDER BY lower(v)) INTO signature
   FROM jsonb_array_elements_text(entry) v;
   IF signature=ANY(signatures) THEN
    RETURN FALSE;
   END IF;
   signatures:=array_append(signatures,signature);
  END LOOP;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN
 RETURN FALSE;
END;
$$;
