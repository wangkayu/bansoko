-- Forward-only migration 015: a provider snapshot seed keeps the AniBT
-- structured tag set it was observed with.
--
-- The AniBT feed carries its structured tags as namespaced XML fields
-- (`anibt:resolution`, `anibt:subtitle`, `anibt:language`, `anibt:format`,
-- `anibt:customtag`), not in the item title. `ExtractAniBTTags` reads them
-- once, while the item's transient `Extensions` are still in memory. Mapping
-- the item into a snapshot seed dropped them, so every surface rebuilt from a
-- snapshot — the non-subscribed preview and the one-shot download — saw a nil
-- set: `anibt.*` rendered empty and a non-empty AniBT whitelist failed a valid
-- release as `anibt_tag_missing`, although the same release passed once
-- subscribed (the subscribed path freezes `source_facts.anibt` and reads it
-- back).
--
-- Why a nullable column OUTSIDE the content hash:
--
-- `provider-feed-snapshot-v1` is the hash schema of every persisted snapshot
-- and it is part of the hashed payload. `canonicalFeedSeed` (Go) and
-- `feed_seed_canonical` (SQL) both enumerate the hashed fields explicitly, and
-- the label cannot change: existing rows are append-only and carry no tags, so
-- folding tags into the hash would make `VerifyContentHash` fail closed on
-- every one of them. This column therefore stays absent from both canonical
-- encoders, and the two functions are untouched.
--
-- NULL means "this snapshot was observed before tags were persisted" (every
-- existing row) or "this provider has no tag set" (mikan, custom). A tag-less
-- AniBT item persists the stored block with an empty `tags` array, which is
-- different from NULL and tells the read model the item was observed with no
-- tags rather than never observed with them.
--
-- The publish function is replaced rather than edited in place: 001_baseline is
-- immutable, and its closed key set would reject the extra seed field. The
-- replacement accepts the same call signature and validates the new field with
-- the same closed-shape discipline as the rest of the seed.

ALTER TABLE provider_feed_snapshot_seeds
    ADD COLUMN anibt_tags JSONB;

ALTER TABLE provider_feed_snapshot_seeds
    ADD CONSTRAINT provider_feed_snapshot_seeds_anibt_tags_check CHECK (
        anibt_tags IS NULL
        -- The frozen set is the persisted (public) shape from
        -- acquisition/domain/source_fact_tags.go: `tags` always present, with
        -- `schema` and `diagnostic` optional (they carry omitempty). The shape
        -- is closed so a later field cannot silently ride along in this column,
        -- and the bounds are the ones the extractor already enforces.
        OR (jsonb_typeof(anibt_tags) = 'object'
            AND anibt_tags ? 'tags'
            AND jsonb_typeof(anibt_tags -> 'tags') = 'array'
            AND jsonb_array_length(anibt_tags -> 'tags') <= 64
            AND pg_column_size(anibt_tags) <= 16384
            -- The key removal is spelled with an explicit text[] cast: a bare
            -- string literal is ambiguous between jsonb-text, jsonb-jsonb and
            -- jsonb-text[], and PostgreSQL refuses to guess.
            AND (anibt_tags - ARRAY['tags','schema','diagnostic']::text[]) = '{}'::jsonb
            -- Every element is a string: a nested object would still come back
            -- from jsonb_array_elements_text as text (and may carry a colon) but
            -- cannot be decoded back into the Go []string the read path expects.
            -- A CHECK cannot hold a subquery, so this is a jsonpath test.
            AND NOT jsonb_path_exists(anibt_tags -> 'tags', '$[*] ? (@.type() != "string")')
            AND (NOT anibt_tags ? 'schema' OR jsonb_typeof(anibt_tags -> 'schema') = 'string')
            AND (NOT anibt_tags ? 'diagnostic' OR jsonb_typeof(anibt_tags -> 'diagnostic') = 'string'))
    );

COMMENT ON COLUMN provider_feed_snapshot_seeds.anibt_tags IS
    'Frozen AniBT structured tag set of this seed, in the persisted (public) tag-block shape. NULL for a non-AniBT provider and for a snapshot observed before tags were persisted; an object with an empty `tags` array is a tag-less AniBT item. Deliberately outside the provider-feed-snapshot-v1 content hash.';

-- The stored tag block is re-emitted through one function so the write path and
-- the read path share the canonical key order and omit the empty optional
-- fields exactly as acquisition/domain.StoredAniBTTags marshals them (Go
-- `omitempty` on schema and diagnostic). The shape was validated above; this
-- only normalizes it.
CREATE FUNCTION anibt_tag_block(p_block jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT jsonb_build_object('tags', COALESCE(p_block -> 'tags', '[]'::jsonb))
        || CASE WHEN COALESCE(p_block ->> 'schema', '') <> '' THEN jsonb_build_object('schema', p_block ->> 'schema') ELSE '{}'::jsonb END
        || CASE WHEN COALESCE(p_block ->> 'diagnostic', '') <> '' THEN jsonb_build_object('diagnostic', p_block ->> 'diagnostic') ELSE '{}'::jsonb END
$$;

CREATE OR REPLACE FUNCTION publish_feed_snapshot(p_id text, p_provider text, p_entry text, p_group text, p_expected_version bigint, p_fetched_at timestamp with time zone, p_content_hash text, p_seeds jsonb, p_payloads bytea[]) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE v_version BIGINT; v_payload_count INTEGER; value JSONB; leaf JSONB; key TEXT;
BEGIN
    IF jsonb_typeof(p_seeds) IS DISTINCT FROM 'array' OR p_payloads IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_INVALID_SET';
    END IF;
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
        'payload_index','manifest','files','anibt_tags']))
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
    INSERT INTO public.provider_feed_snapshots(id,provider,provider_entry_ref,provider_group_ref,version,fetched_at,
        content_hash,hash_schema,projection_schema,fresh_until,retention_until,seed_count)
    VALUES(p_id,p_provider,p_entry,p_group,v_version,p_fetched_at,p_content_hash,'provider-feed-snapshot-v1',
        'torrent-manifest-v1',CURRENT_TIMESTAMP+INTERVAL '15 minutes',CURRENT_TIMESTAMP+INTERVAL '30 days',jsonb_array_length(p_seeds));
    INSERT INTO public.provider_feed_snapshot_seeds(id,snapshot_id,stable_seed_key,locator,locator_display,title,
        published_at,size_bytes,info_hash,payload_digest,torrent_filename,torrent_state,torrent_error_code,torrent_error_reason,
        torrent_version,info_hash_v1,info_hash_v2,payload,payload_size_bytes,
        manifest_payload_digest,manifest_torrent_parser_version,manifest_torrent_name,manifest_total_size_bytes,manifest_file_count,
        manifest_projection_digest,files,anibt_tags)
    SELECT x.id,p_id,x.stable_seed_key,x.locator,x.locator_display,x.title,x.published_at,x.size_bytes,x.info_hash,
        x.payload_digest,x.torrent_filename,x.torrent_state,x.torrent_error_code,x.torrent_error_reason,x.torrent_version,x.info_hash_v1,x.info_hash_v2,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE p_payloads[x.payload_index] END,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE octet_length(p_payloads[x.payload_index]) END,
        m.manifest_payload_digest,m.manifest_torrent_parser_version,m.manifest_torrent_name,m.manifest_total_size_bytes,
        m.manifest_file_count,m.manifest_projection_digest,x.files,
        CASE WHEN jsonb_typeof(x.anibt_tags) = 'object' THEN public.anibt_tag_block(x.anibt_tags) ELSE NULL END
    FROM jsonb_to_recordset(p_seeds) AS x(id TEXT,stable_seed_key TEXT,locator TEXT,locator_display TEXT,title TEXT,
        published_at TIMESTAMPTZ,size_bytes BIGINT,info_hash TEXT,payload_digest TEXT,torrent_filename TEXT,torrent_state TEXT,
        torrent_error_code TEXT,torrent_error_reason TEXT,torrent_version TEXT,info_hash_v1 TEXT,info_hash_v2 TEXT,payload_index INTEGER,
        manifest JSONB,files JSONB,anibt_tags JSONB)
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

REVOKE ALL ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[]) TO bansoko_runtime;
-- The normalizer is only ever called from inside the SECURITY DEFINER owner, so
-- it gets no caller-facing grant at all. PostgreSQL makes every new function
-- PUBLIC-executable unless revoked, and a grant no code path uses is a
-- privilege the runtime should not hold (the same reasoning as 014). The owner
-- executes it through its own ownership, not through a grant.
REVOKE ALL ON FUNCTION anibt_tag_block(jsonb) FROM PUBLIC;
