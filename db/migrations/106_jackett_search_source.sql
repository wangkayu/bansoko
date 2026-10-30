-- Forward-only migration 106: Jackett is a credential-bearing, search-only
-- connection. Its `all` aggregate supplies tracker attribution for each result;
-- that attribution is independent of the subtitle group parsed from the title.

ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('qbittorrent', 'rss_http', 'anibt', 'mikan', 'nyaa', 'jackett', 'tmdb', 'bgm'));

ALTER TABLE connections DROP CONSTRAINT connections_config_shape_check;
ALTER TABLE connections ADD CONSTRAINT connections_config_shape_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384 AND ((kind = 'qbittorrent' AND config ?& '{username,category,save_root,downloader_root,user_tags}'::text[] AND (config - '{username,category,save_root,downloader_root,user_tags}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'username')) = 'string' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256) AND jsonb_typeof((config -> 'category')) = 'string' AND char_length((config ->> 'category')) <= 256 AND jsonb_typeof((config -> 'save_root')) = 'string' AND char_length((config ->> 'save_root')) <= 4096 AND jsonb_typeof((config -> 'downloader_root')) = 'string' AND char_length((config ->> 'downloader_root')) <= 4096 AND is_valid_qbittorrent_user_tags((config -> 'user_tags'))) OR (kind = 'rss_http' AND config ?& '{auth_mode,username}'::text[] AND (config - '{auth_mode,username}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('none', 'basic', 'bearer', 'cookie') AND jsonb_typeof((config -> 'username')) = 'string' AND char_length((config ->> 'username')) <= 256 AND (((config ->> 'auth_mode') = 'basic' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') IN ('none', 'bearer', 'cookie') AND (config ->> 'username') = '')) AND ((config ->> 'auth_mode') <> 'none') = (secret_ref IS NOT NULL)) OR (kind = 'jackett' AND config = '{}'::jsonb AND secret_ref IS NOT NULL) OR (kind IN ('anibt', 'mikan', 'nyaa', 'tmdb', 'bgm') AND config = '{}'::jsonb)));

ALTER TABLE provider_feed_snapshots DROP CONSTRAINT provider_feed_snapshots_provider_check;
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_provider_check
    CHECK (provider IN ('anibt', 'mikan', 'nyaa', 'jackett', 'custom'));

ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_jackett_search_only_check
    CHECK (provider <> 'jackett' OR snapshot_kind = 'search');

ALTER TABLE provider_feed_snapshots DROP CONSTRAINT provider_feed_snapshots_search_scope_check;
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_search_scope_check CHECK (
    snapshot_kind <> 'search'
    OR (provider IN ('anibt', 'mikan', 'nyaa', 'jackett')
        AND provider_entry_ref ~ '^search-scope:[0-9a-f]{64}$'
        AND provider_group_ref ~ '^search-query:[0-9a-f]{64}$')
);

ALTER TABLE provider_feed_snapshot_seeds ADD COLUMN source_site JSONB;
ALTER TABLE provider_feed_snapshot_seeds ADD CONSTRAINT provider_feed_snapshot_seeds_source_site_check CHECK (
    source_site IS NULL OR (
        jsonb_typeof(source_site) = 'object'
        AND source_site ?& '{id,name,type}'::text[]
        AND (source_site - '{id,name,type}'::text[]) = '{}'::jsonb
        AND jsonb_typeof(source_site -> 'id') = 'string'
        AND jsonb_typeof(source_site -> 'name') = 'string'
        AND jsonb_typeof(source_site -> 'type') = 'string'
        AND octet_length(btrim(source_site ->> 'id')) BETWEEN 1 AND 256
        AND octet_length(btrim(source_site ->> 'name')) BETWEEN 1 AND 256
        AND source_site ->> 'type' IN ('public', 'private', 'semi-private')
    )
);
COMMENT ON COLUMN provider_feed_snapshot_seeds.source_site IS
    'Tracker attribution returned by an aggregated search source. Independent of the subtitle group parsed from the release title and outside the legacy content hash.';

-- Replace the publisher as one closed decoder so the Go and SQL write paths
-- accept exactly the same optional source_site fact.

DROP FUNCTION IF EXISTS publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[], text, text);

CREATE FUNCTION publish_feed_snapshot(
    p_id text,
    p_provider text,
    p_entry text,
    p_group text,
    p_expected_version bigint,
    p_fetched_at timestamp with time zone,
    p_content_hash text,
    p_seeds jsonb,
    p_payloads bytea[],
    p_snapshot_kind text DEFAULT 'provider_feed',
    p_search_parser_version text DEFAULT NULL
) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE v_version BIGINT; v_payload_count INTEGER; value JSONB; leaf JSONB; key TEXT;
    v_retention INTERVAL;
BEGIN
    IF jsonb_typeof(p_seeds) IS DISTINCT FROM 'array' OR p_payloads IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    CASE p_snapshot_kind
        WHEN 'provider_feed' THEN v_retention := INTERVAL '30 days';
        WHEN 'custom' THEN v_retention := INTERVAL '30 days';
        WHEN 'search' THEN v_retention := INTERVAL '24 hours';
        ELSE RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END CASE;
    -- The seed shape must be closed before any set-returning function can walk it,
    -- so a non-array projection is a declared rejection instead of a driver error.
    -- A JSON null is a value, not SQL NULL: presence of the manifest is tested with
    -- jsonb_typeof, never with IS NOT NULL.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_typeof(s->'files') IS DISTINCT FROM 'array'
           OR jsonb_typeof(s->'manifest') NOT IN ('object','null')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    v_payload_count:=cardinality(p_payloads);
    IF COALESCE(array_ndims(p_payloads),1)<>1 OR (v_payload_count>0 AND array_lower(p_payloads,1)<>1) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_PAYLOAD_INDEX';
    END IF;
    -- `anibt_tags` is deliberately NOT in this key list: the content hash is
    -- blind to it, and the canonical SQL encoder (feed_seed_canonical) never
    -- reads it. It is listed here only so a caller may declare it.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s WHERE NOT public.business_exact_keys(s,ARRAY[
        'id','stable_seed_key','locator','locator_display','title','published_at','size_bytes','info_hash','payload_digest',
        'torrent_filename','torrent_state','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2',
        'payload_index','manifest','files','anibt_tags','source_site']))
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
            WHERE jsonb_typeof(s->'manifest') = 'object' AND NOT public.business_exact_keys(s->'manifest',ARRAY[
                'payload_digest','torrent_parser_version','torrent_name',
                'total_size_bytes','file_count','projection_digest']))
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s, jsonb_array_elements(s->'files') f
            WHERE NOT public.business_exact_keys(f,ARRAY['file_ref','ordinal','source_path','filename','size_bytes','media_kind','language'])) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- The tag block is a closed object of at most 64 "dimension:value" strings
    -- (the extractor's bounds). The extractor's own schema labels are structural
    -- constants, not free text, and a tag it keeps never contains whitespace
    -- control characters or an empty side of its colon.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE s ? 'anibt_tags' AND jsonb_typeof(s->'anibt_tags') NOT IN ('null','object')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- `schema` and `diagnostic` carry omitempty in the Go encoder, so the block
    -- is a closed SET rather than a fixed key list: `tags` is always present and
    -- nothing outside the three known keys may appear.
    --
    -- The type checks are split out of one OR chain on purpose: PostgreSQL does
    -- not promise short-circuit evaluation inside a boolean expression, so a
    -- single chain could still evaluate jsonb_array_length / a set-returning
    -- call on a non-array and raise a driver error instead of this typed one.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_typeof(s->'anibt_tags') = 'object'
          AND (NOT (s->'anibt_tags') ? 'tags'
               OR ((s->'anibt_tags') - ARRAY['tags','schema','diagnostic']::text[]) <> '{}'::jsonb
               OR jsonb_typeof(s->'anibt_tags'->'schema') NOT IN ('string','null')
               OR jsonb_typeof(s->'anibt_tags'->'diagnostic') NOT IN ('string','null'))) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_typeof(s->'anibt_tags') = 'object'
          AND jsonb_typeof(s->'anibt_tags'->'tags') IS DISTINCT FROM 'array') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_typeof(s->'anibt_tags'->'tags') = 'array'
          AND jsonb_array_length(s->'anibt_tags'->'tags') > 64) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- Every element is a string: a nested object also yields text (with colons)
    -- from jsonb_array_elements_text but cannot be decoded back into the Go
    -- []string the read path expects.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s, jsonb_array_elements(s->'anibt_tags'->'tags') element
        WHERE jsonb_typeof(s->'anibt_tags'->'tags') = 'array'
          AND jsonb_typeof(element) IS DISTINCT FROM 'string') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- The bound is the ONE the extractor enforces: at most 128 bytes of value,
    -- not of the whole "dimension:value" string (a 128-byte customtag is a
    -- valid tag, and comparing the tag length would refuse it at publish after
    -- the domain accepted it). Whitespace controls are the same three the
    -- extractor drops; XML text cannot carry any other control character.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s, jsonb_array_elements_text(s->'anibt_tags'->'tags') tag
        WHERE jsonb_typeof(s->'anibt_tags'->'tags') = 'array'
          AND (strpos(tag, ':') < 2 OR strpos(tag, ':') = length(tag)
               OR octet_length(substr(tag, strpos(tag, ':') + 1)) > 128
               OR tag ~ '[\r\n\t]')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE s ? 'source_site' AND jsonb_typeof(s->'source_site') NOT IN ('null','object')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_typeof(s->'source_site') = 'object'
          AND (NOT (s->'source_site') ?& '{id,name,type}'::text[]
               OR ((s->'source_site') - '{id,name,type}'::text[]) <> '{}'::jsonb
               OR jsonb_typeof(s->'source_site'->'id') IS DISTINCT FROM 'string'
               OR jsonb_typeof(s->'source_site'->'name') IS DISTINCT FROM 'string'
               OR jsonb_typeof(s->'source_site'->'type') IS DISTINCT FROM 'string'
               OR octet_length(btrim(s->'source_site'->>'id')) NOT BETWEEN 1 AND 256
               OR octet_length(btrim(s->'source_site'->>'name')) NOT BETWEEN 1 AND 256
               OR s->'source_site'->>'type' NOT IN ('public','private','semi-private'))) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- Recordset casts are not a closed decoder: reject kind coercions first.
    FOR value IN SELECT item FROM jsonb_array_elements(p_seeds) item LOOP
        FOREACH key IN ARRAY ARRAY['id','stable_seed_key','locator','locator_display','title','torrent_state'] LOOP
            IF jsonb_typeof(value->key) IS DISTINCT FROM 'string' THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        FOREACH key IN ARRAY ARRAY['published_at','info_hash','payload_digest','torrent_filename','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2'] LOOP
            IF jsonb_typeof(value->key) NOT IN ('string','null') THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        FOREACH key IN ARRAY ARRAY['size_bytes','payload_index'] LOOP
            IF jsonb_typeof(value->key) <> 'null' AND (jsonb_typeof(value->key) IS DISTINCT FROM 'number' OR value->>key !~ '^[0-9]+$') THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        IF jsonb_typeof(value->'manifest') = 'object' THEN
            FOREACH key IN ARRAY ARRAY['payload_digest','torrent_parser_version','torrent_name','projection_digest'] LOOP
                IF jsonb_typeof(value->'manifest'->key) IS DISTINCT FROM 'string' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET'; END IF;
            END LOOP;
            FOREACH key IN ARRAY ARRAY['total_size_bytes','file_count'] LOOP
                IF jsonb_typeof(value->'manifest'->key) IS DISTINCT FROM 'number' OR value->'manifest'->>key !~ '^[0-9]+$' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET'; END IF;
            END LOOP;
        END IF;
        FOR leaf IN SELECT item FROM jsonb_array_elements(value->'files') item LOOP
            FOREACH key IN ARRAY ARRAY['file_ref','source_path','filename','media_kind'] LOOP
                IF jsonb_typeof(leaf->key) IS DISTINCT FROM 'string' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET'; END IF;
            END LOOP;
            IF jsonb_typeof(leaf->'language') NOT IN ('string','null') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET'; END IF;
            FOREACH key IN ARRAY ARRAY['ordinal','size_bytes'] LOOP
                IF jsonb_typeof(leaf->key) IS DISTINCT FROM 'number' OR leaf->>key !~ '^[0-9]+$' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET'; END IF;
            END LOOP;
        END LOOP;
    END LOOP;
    IF (SELECT COALESCE(array_agg((s->>'payload_index')::integer ORDER BY (s->>'payload_index')::integer),ARRAY[]::integer[])
        FROM jsonb_array_elements(p_seeds) s WHERE s->>'payload_index' IS NOT NULL)
        IS DISTINCT FROM ARRAY(SELECT generate_series(1,v_payload_count)) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_PAYLOAD_INDEX';
    END IF;
    -- A manifest is a leaf of its own seed: it may not name another seed, must
    -- cover exactly the files it declares, and a seed without a manifest projects
    -- none. The payload digest is declared twice and must agree.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
            WHERE (jsonb_typeof(s->'manifest') IS DISTINCT FROM 'object' AND jsonb_array_length(s->'files')<>0)
               OR (jsonb_typeof(s->'manifest') = 'object' AND (jsonb_array_length(s->'files')<>(s->'manifest'->>'file_count')::integer
                   OR s->'manifest'->>'payload_digest' IS DISTINCT FROM s->>'payload_digest'))) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_CROSS_ROOT';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('bansoko:provider-feed:' || p_provider || ':' || p_entry || ':' || p_group));
    SELECT COALESCE(max(version),0)+1 INTO v_version FROM public.provider_feed_snapshots
    WHERE provider=p_provider AND provider_entry_ref=p_entry AND provider_group_ref=p_group;
    IF v_version IS DISTINCT FROM p_expected_version THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_VERSION_CONFLICT'; END IF;
    INSERT INTO public.provider_feed_snapshots(id,provider,provider_entry_ref,provider_group_ref,snapshot_kind,search_parser_version,version,fetched_at,
        content_hash,hash_schema,projection_schema,fresh_until,retention_until,seed_count)
    VALUES(p_id,p_provider,p_entry,p_group,p_snapshot_kind,p_search_parser_version,v_version,p_fetched_at,p_content_hash,'provider-feed-snapshot-v1',
        'torrent-manifest-v1',CURRENT_TIMESTAMP+INTERVAL '15 minutes',
        CASE WHEN p_snapshot_kind = 'search' THEN p_fetched_at + v_retention ELSE CURRENT_TIMESTAMP + v_retention END,
        jsonb_array_length(p_seeds));
    INSERT INTO public.provider_feed_snapshot_seeds(id,snapshot_id,stable_seed_key,locator,locator_display,title,
        published_at,size_bytes,info_hash,payload_digest,torrent_filename,torrent_state,torrent_error_code,torrent_error_reason,
        torrent_version,info_hash_v1,info_hash_v2,payload,payload_size_bytes,
        manifest_payload_digest,manifest_torrent_parser_version,manifest_torrent_name,manifest_total_size_bytes,manifest_file_count,
        manifest_projection_digest,files,anibt_tags,source_site)
    SELECT x.id,p_id,x.stable_seed_key,x.locator,x.locator_display,x.title,x.published_at,x.size_bytes,x.info_hash,
        x.payload_digest,x.torrent_filename,x.torrent_state,x.torrent_error_code,x.torrent_error_reason,x.torrent_version,x.info_hash_v1,x.info_hash_v2,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE p_payloads[x.payload_index] END,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE octet_length(p_payloads[x.payload_index]) END,
        m.manifest_payload_digest,m.manifest_torrent_parser_version,m.manifest_torrent_name,m.manifest_total_size_bytes,
        m.manifest_file_count,m.manifest_projection_digest,x.files,
        CASE WHEN jsonb_typeof(x.anibt_tags) = 'object' THEN public.anibt_tag_block(x.anibt_tags) ELSE NULL END,
        x.source_site
    FROM jsonb_to_recordset(p_seeds) AS x(id TEXT,stable_seed_key TEXT,locator TEXT,locator_display TEXT,title TEXT,
        published_at TIMESTAMPTZ,size_bytes BIGINT,info_hash TEXT,payload_digest TEXT,torrent_filename TEXT,torrent_state TEXT,
        torrent_error_code TEXT,torrent_error_reason TEXT,torrent_version TEXT,info_hash_v1 TEXT,info_hash_v2 TEXT,payload_index INTEGER,
        manifest JSONB,files JSONB,anibt_tags JSONB,source_site JSONB)
    CROSS JOIN LATERAL (SELECT (x.manifest->>'payload_digest')::TEXT AS manifest_payload_digest,
        (x.manifest->>'torrent_parser_version')::TEXT AS manifest_torrent_parser_version,
        (x.manifest->>'torrent_name')::TEXT AS manifest_torrent_name,
        (x.manifest->>'total_size_bytes')::BIGINT AS manifest_total_size_bytes,
        (x.manifest->>'file_count')::INTEGER AS manifest_file_count,
        (x.manifest->>'projection_digest')::TEXT AS manifest_projection_digest) m;
    -- The deferred seal rechecks final rows at COMMIT, including any subsequent
    -- owner-only writes in the outer transaction. Runtime cannot append leaves.
    RETURN p_id;
END;
$_$;

REVOKE ALL ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[], text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[], text, text) TO bansoko_runtime;
