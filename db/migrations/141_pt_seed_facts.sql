-- Forward-only migration 141: persist the tracker-reported swarm, billing and
-- category facts of a Jackett search seed. The search response carries
-- seeders/peers, download/upload volume factors and category ids; the peers
-- count is folded into the derived leechers at parse time and the category ids
-- are resolved to id+name pairs through the indexer caps before publication.
--
-- All five columns stay OUTSIDE the provider-feed-snapshot-v2 content hash:
-- every snapshot published before this migration carries none of them, and the
-- hash is verified on read, so folding them into either canonical encoder
-- (feed_seed_canonical here, canonicalFeedSeed in Go) would make
-- VerifyContentHash fail closed on every existing snapshot. They follow the
-- anibt_tags precedent: declared in the publish function's closed key set,
-- type-checked, stored, and never read by the canonical encoders.
--
-- Absent stays absent: an unreported billing factor is NULL, never 1, and the
-- (1,1) neutral pair is only hidden at render time.
ALTER TABLE provider_feed_snapshot_seeds
    ADD COLUMN seeders BIGINT,
    ADD COLUMN leechers BIGINT,
    ADD COLUMN download_factor DOUBLE PRECISION,
    ADD COLUMN upload_factor DOUBLE PRECISION,
    ADD COLUMN categories JSONB,
    ADD CONSTRAINT provider_feed_snapshot_seeds_pt_facts_check CHECK (
        (seeders IS NULL OR (seeders >= 0 AND seeders <= 1099511627776))
        AND (leechers IS NULL OR (leechers >= 0 AND leechers <= 1099511627776))
        AND (download_factor IS NULL OR (download_factor >= 0 AND download_factor <= 1000000))
        AND (upload_factor IS NULL OR (upload_factor >= 0 AND upload_factor <= 1000000))
        AND (categories IS NULL OR (jsonb_typeof(categories) = 'array' AND jsonb_array_length(categories) <= 32
            AND NOT jsonb_path_exists(categories, '$[*] ? (@.type() != "object")'))));
COMMENT ON COLUMN provider_feed_snapshot_seeds.seeders IS
    'Tracker-reported seeder count of a Jackett search result; display evidence, outside the content hash.';
COMMENT ON COLUMN provider_feed_snapshot_seeds.leechers IS
    'Derived leecher count max(peers-seeders, 0), stored only when the tracker published both counts; outside the content hash.';
COMMENT ON COLUMN provider_feed_snapshot_seeds.download_factor IS
    'Tracker-reported download volume factor; NULL means unreported, never normalized to 1; outside the content hash.';
COMMENT ON COLUMN provider_feed_snapshot_seeds.upload_factor IS
    'Tracker-reported upload volume factor; NULL means unreported, never normalized to 1; outside the content hash.';
COMMENT ON COLUMN provider_feed_snapshot_seeds.categories IS
    'Caps-resolved tracker categories as [{"id","name"}]; empty when the tracker published none or caps were unreadable; outside the content hash.';

CREATE OR REPLACE FUNCTION publish_feed_snapshot(
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
    -- `anibt_tags` and the five PT display facts are deliberately NOT read by
    -- the content hash: the canonical SQL encoder (feed_seed_canonical) never
    -- reads them. They are listed here only so a caller may declare them.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s WHERE NOT public.business_exact_keys(s,ARRAY[
        'id','stable_seed_key','locator','locator_display','title','published_at','size_bytes','info_hash','payload_digest',
        'torrent_filename','torrent_state','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2',
        'payload_index','manifest','files','anibt_tags','source_site','source_guid','details_url',
        'seeders','leechers','download_factor','upload_factor','categories']))
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
    -- The category list is a closed array of at most 32 {"id","name"} pairs,
    -- the shape the caps resolution produces. The same split-out type checks
    -- apply: an element that is not an object is a declared rejection, never a
    -- driver error.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE s ? 'categories' AND jsonb_typeof(s->'categories') NOT IN ('null','array')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
        WHERE jsonb_array_length(NULLIF(s->'categories','null'::jsonb)) > 32) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- NULLIF folds a JSON null to SQL NULL before the set-returning call:
    -- jsonb_array_elements(jsonb 'null') errors instead of yielding zero rows,
    -- so the scalar guard must run on the input of the lateral walk itself.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s, jsonb_array_elements(NULLIF(s->'categories','null'::jsonb)) c
        WHERE jsonb_typeof(c) IS DISTINCT FROM 'object'
           OR NOT c ?& '{id,name}'::text[]
           OR (c - '{id,name}'::text[]) <> '{}'::jsonb
           OR jsonb_typeof(c->'id') IS DISTINCT FROM 'string'
           OR jsonb_typeof(c->'name') IS DISTINCT FROM 'string'
           OR octet_length(btrim(c->>'id')) NOT BETWEEN 1 AND 64
           OR octet_length(btrim(c->>'name')) NOT BETWEEN 1 AND 256) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
    -- Recordset casts are not a closed decoder: reject kind coercions first.
    FOR value IN SELECT item FROM jsonb_array_elements(p_seeds) item LOOP
        FOREACH key IN ARRAY ARRAY['id','stable_seed_key','locator','locator_display','title','torrent_state','source_guid','details_url'] LOOP
            IF jsonb_typeof(value->key) IS DISTINCT FROM 'string' THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        FOREACH key IN ARRAY ARRAY['published_at','info_hash','payload_digest','torrent_filename','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2'] LOOP
            IF jsonb_typeof(value->key) NOT IN ('string','null') THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        FOREACH key IN ARRAY ARRAY['size_bytes','payload_index','seeders','leechers'] LOOP
            IF jsonb_typeof(value->key) <> 'null' AND (jsonb_typeof(value->key) IS DISTINCT FROM 'number' OR value->>key !~ '^[0-9]+$') THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
            END IF;
        END LOOP;
        -- Billing factors are JSON numbers, not integers: 0.5 and 2 are both
        -- valid, a negative is not.
        FOREACH key IN ARRAY ARRAY['download_factor','upload_factor'] LOOP
            IF jsonb_typeof(value->key) <> 'null' AND (jsonb_typeof(value->key) IS DISTINCT FROM 'number' OR (value->key)::float8 < 0) THEN
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
    VALUES(p_id,p_provider,p_entry,p_group,p_snapshot_kind,p_search_parser_version,v_version,p_fetched_at,p_content_hash,'provider-feed-snapshot-v2',
        'torrent-manifest-v1',CURRENT_TIMESTAMP+INTERVAL '15 minutes',
        CASE WHEN p_snapshot_kind = 'search' THEN p_fetched_at + v_retention ELSE CURRENT_TIMESTAMP + v_retention END,
        jsonb_array_length(p_seeds));
    INSERT INTO public.provider_feed_snapshot_seeds(id,snapshot_id,stable_seed_key,locator,locator_display,title,
        published_at,size_bytes,info_hash,payload_digest,torrent_filename,torrent_state,torrent_error_code,torrent_error_reason,
        torrent_version,info_hash_v1,info_hash_v2,payload,payload_size_bytes,
        manifest_payload_digest,manifest_torrent_parser_version,manifest_torrent_name,manifest_total_size_bytes,manifest_file_count,
        manifest_projection_digest,files,anibt_tags,source_site,source_guid,details_url,
        seeders,leechers,download_factor,upload_factor,categories)
    SELECT x.id,p_id,x.stable_seed_key,x.locator,x.locator_display,x.title,x.published_at,x.size_bytes,x.info_hash,
        x.payload_digest,x.torrent_filename,x.torrent_state,x.torrent_error_code,x.torrent_error_reason,x.torrent_version,x.info_hash_v1,x.info_hash_v2,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE p_payloads[x.payload_index] END,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE octet_length(p_payloads[x.payload_index]) END,
        m.manifest_payload_digest,m.manifest_torrent_parser_version,m.manifest_torrent_name,m.manifest_total_size_bytes,
        m.manifest_file_count,m.manifest_projection_digest,x.files,
        CASE WHEN jsonb_typeof(x.anibt_tags) = 'object' THEN public.anibt_tag_block(x.anibt_tags) ELSE NULL END,
        x.source_site,x.source_guid,x.details_url,
        x.seeders,x.leechers,x.download_factor,x.upload_factor,
        CASE WHEN jsonb_typeof(x.categories) = 'array' THEN x.categories ELSE NULL END
    FROM jsonb_to_recordset(p_seeds) AS x(id TEXT,stable_seed_key TEXT,locator TEXT,locator_display TEXT,title TEXT,
        published_at TIMESTAMPTZ,size_bytes BIGINT,info_hash TEXT,payload_digest TEXT,torrent_filename TEXT,torrent_state TEXT,
        torrent_error_code TEXT,torrent_error_reason TEXT,torrent_version TEXT,info_hash_v1 TEXT,info_hash_v2 TEXT,payload_index INTEGER,
        manifest JSONB,files JSONB,anibt_tags JSONB,source_site JSONB,source_guid TEXT,details_url TEXT,
        seeders BIGINT,leechers BIGINT,download_factor DOUBLE PRECISION,upload_factor DOUBLE PRECISION,categories JSONB)
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
