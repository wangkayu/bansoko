-- Forward-only migration 138: preserve explicit release pages and original GUIDs.
-- The v2 snapshot identity also covers tracker attribution. New publications,
-- reads and acquisition qualifications use v2 only. Historical v1 rows retain
-- their original labels/hashes; they are not backfilled or replay-compatible.
ALTER TABLE provider_feed_snapshot_seeds
    ADD COLUMN source_guid TEXT NOT NULL DEFAULT '',
    ADD COLUMN details_url TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT provider_feed_snapshot_seeds_provenance_bound CHECK (
        octet_length(source_guid) <= 8192 AND octet_length(details_url) <= 8192);
ALTER TABLE provider_feed_snapshots DROP CONSTRAINT provider_feed_snapshots_hash_schema_check;
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_hash_schema_check
    CHECK (hash_schema IN ('provider-feed-snapshot-v1','provider-feed-snapshot-v2'));
ALTER TABLE provider_feed_snapshots ALTER COLUMN hash_schema SET DEFAULT 'provider-feed-snapshot-v2';
COMMENT ON COLUMN provider_feed_snapshot_seeds.source_site IS
    'Tracker attribution returned by the search source; included in the v2 snapshot content hash.';

CREATE OR REPLACE FUNCTION feed_seed_canonical(seed jsonb, projection_digest text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE result TEXT; key TEXT;
BEGIN
    result := '{"stable_seed_key":' || public.feed_json_string(seed->>'stable_seed_key')
        || ',"locator":' || public.feed_json_string(seed->>'locator')
        || ',"title":' || public.feed_json_string(seed->>'title');
    IF seed->>'published_at' IS NOT NULL THEN
        result := result || ',"published_at":' || public.feed_json_string(public.feed_time_text((seed->>'published_at')::timestamptz));
    END IF;
    IF seed->>'size_bytes' IS NOT NULL THEN result := result || ',"size_bytes":' || (seed->>'size_bytes'); END IF;
    FOREACH key IN ARRAY ARRAY['info_hash','payload_digest','torrent_filename'] LOOP
        IF seed->>key IS NOT NULL THEN
            result := result || ',' || public.feed_json_string(key) || ':' || public.feed_json_string(CASE WHEN key='torrent_filename' THEN public.text_trim_go_ws(seed->>key) ELSE seed->>key END);
        END IF;
    END LOOP;
    result := result || ',"torrent_state":' || public.feed_json_string(seed->>'torrent_state');
    IF projection_digest IS NOT NULL THEN result := result || ',"projection_digest":' || public.feed_json_string(projection_digest); END IF;
    result := result || ',"source_guid":' || public.feed_json_string(seed->>'source_guid')
        || ',"details_url":' || public.feed_json_string(seed->>'details_url') || ',"source_site":';
    IF jsonb_typeof(seed->'source_site') = 'object' THEN
        result := result || '{"id":' || public.feed_json_string(seed->'source_site'->>'id')
            || ',"name":' || public.feed_json_string(seed->'source_site'->>'name')
            || ',"type":' || public.feed_json_string(seed->'source_site'->>'type') || '}';
    ELSE
        result := result || 'null';
    END IF;
    RETURN result || '}';
END;
$$;

CREATE OR REPLACE FUNCTION assert_feed_snapshot_set(p_id text) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE h RECORD; v_seed RECORD; v_count BIGINT; v_refs BIGINT; v_paths BIGINT; v_ords BIGINT;
    v_size NUMERIC; v_min INTEGER; v_max INTEGER; v_canonical TEXT; v_digest TEXT;
BEGIN
    SELECT hash_schema,content_hash,seed_count INTO h FROM public.provider_feed_snapshots WHERE id=p_id;
    IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_FINAL_MISSING'; END IF;
    IF h.hash_schema IS DISTINCT FROM 'provider-feed-snapshot-v2' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_CONTENT_HASH';
    END IF;
    SELECT count(*) INTO v_count FROM public.provider_feed_snapshot_seeds WHERE snapshot_id=p_id;
    IF v_count<>h.seed_count THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_SEED_COUNT'; END IF;
    IF EXISTS (
        SELECT 1 FROM public.provider_feed_snapshot_seeds s
        WHERE s.snapshot_id=p_id AND (
            (s.payload_digest IS NULL) IS DISTINCT FROM (s.payload IS NULL)
            OR (s.payload IS NOT NULL AND (s.payload_digest IS DISTINCT FROM 'sha256:' || encode(sha256(s.payload),'hex')
                OR s.payload_size_bytes IS DISTINCT FROM octet_length(s.payload)))
            OR (s.torrent_state='available') IS DISTINCT FROM (s.manifest_torrent_parser_version IS NOT NULL)
            OR (s.manifest_torrent_parser_version IS NOT NULL AND s.manifest_payload_digest IS DISTINCT FROM s.payload_digest)
        )) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_SEED_MATERIAL'; END IF;
    FOR v_seed IN SELECT * FROM public.provider_feed_snapshot_seeds WHERE snapshot_id=p_id LOOP
        IF v_seed.manifest_torrent_parser_version IS NULL THEN
            IF v_seed.files <> '[]'::jsonb THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_MANIFEST_COUNT'; END IF;
            CONTINUE;
        END IF;
        SELECT count(*),count(DISTINCT f->>'file_ref'),count(DISTINCT f->>'source_path'),
            count(DISTINCT (f->>'ordinal')::integer),COALESCE(sum((f->>'size_bytes')::bigint),0),
            min((f->>'ordinal')::integer),max((f->>'ordinal')::integer)
        INTO v_count,v_refs,v_paths,v_ords,v_size,v_min,v_max
        FROM jsonb_array_elements(v_seed.files) f;
        IF v_count<>v_seed.manifest_file_count OR v_refs<>v_count OR v_paths<>v_count OR v_ords<>v_count
            OR v_size<>v_seed.manifest_total_size_bytes OR v_min<>0 OR v_max<>v_count-1 THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_MANIFEST_COUNT';
        END IF;
        IF EXISTS (SELECT 1 FROM jsonb_array_elements(v_seed.files) f
            WHERE (octet_length(f->>'source_path')>2048 OR public.text_trim_go_ws(f->>'source_path') IS DISTINCT FROM f->>'source_path'
                OR strpos(f->>'source_path',chr(92))>0 OR f->>'source_path' ~ '(^|/)([.]{1,2})?(/|$)'
                OR strpos(f->>'source_path',chr(10))>0 OR strpos(f->>'source_path',chr(13))>0
                OR octet_length(f->>'filename')>512 OR octet_length(COALESCE(f->>'language',''))>64)) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_FILE_PATH';
        END IF;
        IF EXISTS (SELECT 1 FROM jsonb_array_elements(v_seed.files) f
            WHERE f->>'file_ref' IS DISTINCT FROM 'sha256:' || encode(sha256(
                convert_to('torrent-file-v1','UTF8') || decode('00','hex') || convert_to(v_seed.manifest_payload_digest,'UTF8')
                || decode('00','hex') || convert_to(f->>'source_path','UTF8')),'hex')) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_FILE_IDENTITY';
        END IF;
        v_canonical := '{"schema":"torrent-manifest-v1","payload_digest":' || public.feed_json_string(v_seed.manifest_payload_digest)
            || ',"parser_version":' || public.feed_json_string(v_seed.manifest_torrent_parser_version)
            || ',"files":[' || COALESCE((SELECT string_agg(public.feed_file_canonical(f),',' ORDER BY (f->>'ordinal')::integer)
                FROM jsonb_array_elements(v_seed.files) f),'') || ']}';
        v_digest := 'sha256:' || encode(sha256(convert_to(v_canonical,'UTF8')),'hex');
        IF v_digest IS DISTINCT FROM v_seed.manifest_projection_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_PROJECTION_DIGEST'; END IF;
    END LOOP;
    SELECT '{"schema":"provider-feed-snapshot-v2","seeds":['
        || COALESCE(string_agg(canonical,',' ORDER BY canonical COLLATE "C"),'') || ']}' INTO v_canonical
    FROM (SELECT public.feed_seed_canonical(to_jsonb(s),s.manifest_projection_digest) AS canonical
        FROM public.provider_feed_snapshot_seeds s WHERE s.snapshot_id=p_id) ordered;
    IF h.content_hash IS DISTINCT FROM 'sha256:' || encode(sha256(convert_to(v_canonical,'UTF8')),'hex') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_CONTENT_HASH';
    END IF;
END;
$_$;

CREATE OR REPLACE FUNCTION recheck_acquisition_qualification(p_qualification jsonb, p_request_hash text, p_config_digest text, p_payload_digest text) RETURNS timestamp with time zone
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    c JSONB; mg JSONB; src JSONB; obs JSONB; feed JSONB; programs JSONB;
    v_row RECORD; v_group RECORD; v_feed RECORD; v_seed RECORD;
    v_program JSONB; v_canonical TEXT; v_now TIMESTAMPTZ; v_kind TEXT;
BEGIN
    IF NOT public.business_exact_keys(p_qualification,ARRAY['config','management','source','observation','feed','programs','placement'])
        OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
        OR p_config_digest IS NULL OR p_config_digest !~ '^sha256:[0-9a-f]{64}$'
        OR p_payload_digest IS NULL OR p_payload_digest !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    c:=p_qualification->'config'; mg:=p_qualification->'management'; src:=p_qualification->'source';
    obs:=p_qualification->'observation'; feed:=p_qualification->'feed'; programs:=p_qualification->'programs';
    IF NOT public.business_exact_keys(programs,ARRAY['attr_fingerprint','vars_digest','translation'])
        OR jsonb_typeof(programs->'attr_fingerprint') IS DISTINCT FROM 'string'
        OR programs->>'attr_fingerprint' !~ '^sha256:[0-9a-f]{64}$'
        OR (programs->'vars_digest'<>'null'::jsonb AND (jsonb_typeof(programs->'vars_digest') IS DISTINCT FROM 'string'
            OR programs->>'vars_digest' !~ '^sha256:[0-9a-f]{64}$')) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    -- No ordinary Placement row is a FileFact/OS proof; no SQL row represents
    -- the build-embedded translation. Do not silently downgrade either claim.
    IF p_qualification->'placement' IS DISTINCT FROM '{"kind":"none"}'::jsonb
        OR programs->'translation' IS DISTINCT FROM 'null'::jsonb THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROOF_UNSUPPORTED';
    END IF;
    v_kind:=c->>'kind';
    IF (v_kind='submitted' AND NOT public.business_exact_keys(c,ARRAY['kind','request_hash','config_digest']))
        OR (v_kind='saved_revision' AND NOT public.business_exact_keys(c,ARRAY['kind','candidate_ref','candidate_version','revision','revision_digest']))
        OR (v_kind='owner' AND NOT public.business_exact_keys(c,ARRAY['kind','managed_anime_ref','config_version','config_digest']))
        OR v_kind IS NULL OR v_kind NOT IN ('submitted','saved_revision','owner') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    IF mg->>'kind'='instance' THEN
        IF NOT public.business_exact_keys(mg,ARRAY['kind','managed_anime_ref','subscription_ref','expected_subscription_version','expected_lifecycle_version','expected_metadata_version','expected_parser_policy_version','expected_rules_owner_version','expected_subgroups_owner_version','subscription_policy'])
            OR mg->>'subscription_policy' IS NULL OR mg->>'subscription_policy' NOT IN ('automatic_active','manual_active_or_stopped')
            OR jsonb_typeof(mg->'managed_anime_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(mg->'subscription_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(mg->'expected_subscription_version') IS DISTINCT FROM 'number'
            OR jsonb_typeof(mg->'expected_lifecycle_version') IS DISTINCT FROM 'number'
            OR mg->>'expected_subscription_version' !~ '^[1-9][0-9]*$'
            OR mg->>'expected_lifecycle_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT lifecycle_state AS state,lifecycle_version AS version INTO v_row FROM public.managed_anime
        WHERE id=mg->>'managed_anime_ref' FOR SHARE;
        IF NOT FOUND OR v_row.state<>'active' OR v_row.version IS DISTINCT FROM (mg->>'expected_lifecycle_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_MANAGEMENT_CHANGED';
        END IF;
        SELECT state,version,managed_anime_id INTO v_row FROM public.subscriptions
        WHERE id=mg->>'subscription_ref' FOR UPDATE;
        IF NOT FOUND OR (CASE mg->>'subscription_policy' WHEN 'automatic_active' THEN v_row.state<>'active' ELSE v_row.state NOT IN ('active','stopped') END) OR v_row.managed_anime_id IS DISTINCT FROM mg->>'managed_anime_ref'
            OR v_row.version IS DISTINCT FROM (mg->>'expected_subscription_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_MANAGEMENT_CHANGED';
        END IF;
    ELSIF mg IS DISTINCT FROM '{"kind":"none"}'::jsonb THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    IF v_kind<>'owner' AND mg->>'kind'='instance' AND (mg->'expected_metadata_version' IS DISTINCT FROM 'null'::jsonb OR mg->'expected_parser_policy_version' IS DISTINCT FROM 'null'::jsonb OR mg->'expected_rules_owner_version' IS DISTINCT FROM 'null'::jsonb OR mg->'expected_subgroups_owner_version' IS DISTINCT FROM 'null'::jsonb) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    IF v_kind='owner' THEN
        IF jsonb_typeof(mg->'expected_rules_owner_version') IS DISTINCT FROM 'number' OR mg->>'expected_rules_owner_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT rules_version AS version INTO v_row FROM public.anime_business_configs WHERE managed_anime_id=mg->>'managed_anime_ref' FOR SHARE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (mg->>'expected_rules_owner_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
    END IF;
    IF v_kind='owner' THEN
        IF jsonb_typeof(mg->'expected_subgroups_owner_version') IS DISTINCT FROM 'number' OR mg->>'expected_subgroups_owner_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT subgroups_version AS version INTO v_row FROM public.anime_business_configs WHERE managed_anime_id=mg->>'managed_anime_ref' FOR SHARE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (mg->>'expected_subgroups_owner_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
    END IF;
    IF v_kind='submitted' THEN
        IF c->>'request_hash' IS DISTINCT FROM p_request_hash OR c->>'config_digest' IS DISTINCT FROM p_config_digest THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
    ELSIF v_kind='owner' THEN
        IF mg->>'kind'<>'instance' OR c->>'managed_anime_ref' IS DISTINCT FROM mg->>'managed_anime_ref'
            OR jsonb_typeof(c->'config_version') IS DISTINCT FROM 'number' OR c->>'config_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        IF jsonb_typeof(mg->'expected_metadata_version') IS DISTINCT FROM 'number' OR mg->>'expected_metadata_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        IF jsonb_typeof(mg->'expected_parser_policy_version') IS DISTINCT FROM 'number' OR mg->>'expected_parser_policy_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT parser_version AS version INTO v_row FROM public.anime_business_configs WHERE managed_anime_id=mg->>'managed_anime_ref' FOR SHARE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (mg->>'expected_parser_policy_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PARSER_CHANGED';
        END IF;
        SELECT version INTO v_row FROM public.anime_metadata WHERE managed_anime_id=mg->>'managed_anime_ref' FOR SHARE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (mg->>'expected_metadata_version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_METADATA_CHANGED';
        END IF;
        SELECT business_version AS version,business_digest,business_snapshot INTO v_row FROM public.anime_business_configs
        WHERE managed_anime_id=c->>'managed_anime_ref' FOR UPDATE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (c->>'config_version')::bigint
            OR c->>'config_digest' IS DISTINCT FROM p_config_digest THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
        -- Whole-body hash and current owner-override closure remains the
        -- accepting constructor's responsibility; the immutable revision FK
        -- and 117 guard bind the persisted snapshot to its exact saved body.
        IF v_row.business_snapshot->'programs'->'attrrules'->>'digest' IS DISTINCT FROM programs->>'attr_fingerprint'
            OR (programs->'vars_digest'<>'null'::jsonb AND v_row.business_snapshot->'programs'->'vars'->>'digest' IS DISTINCT FROM programs->>'vars_digest') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROGRAM_CHANGED';
        END IF;
    ELSE
        IF jsonb_typeof(c->'candidate_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(c->'candidate_version') IS DISTINCT FROM 'number' OR c->>'candidate_version' !~ '^[1-9][0-9]*$'
            OR jsonb_typeof(c->'revision') IS DISTINCT FROM 'number' OR c->>'revision' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT version,current_revision,current_revision_digest,status,activated_managed_anime_id INTO v_row
        FROM public.anime_config_candidates WHERE id=c->>'candidate_ref' FOR UPDATE;
        IF NOT FOUND OR v_row.version IS DISTINCT FROM (c->>'candidate_version')::bigint
            OR v_row.current_revision IS DISTINCT FROM (c->>'revision')::bigint
            OR v_row.current_revision_digest IS DISTINCT FROM c->>'revision_digest'
            OR v_row.status<>'working' OR v_row.activated_managed_anime_id IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
        SELECT effective_digest,business_digest,business_snapshot INTO v_row FROM public.anime_config_candidate_revisions
        WHERE candidate_id=c->>'candidate_ref' AND revision=(c->>'revision')::bigint;
        IF NOT FOUND OR v_row.effective_digest IS DISTINCT FROM c->>'revision_digest'
            OR (v_row.business_snapshot IS NOT NULL AND v_row.business_digest IS DISTINCT FROM p_config_digest) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
        END IF;
        IF v_row.business_snapshot IS NOT NULL AND (v_row.business_snapshot->'programs'->'attrrules'->>'digest' IS DISTINCT FROM programs->>'attr_fingerprint'
            OR (programs->'vars_digest'<>'null'::jsonb AND v_row.business_snapshot->'programs'->'vars'->>'digest' IS DISTINCT FROM programs->>'vars_digest')) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROGRAM_CHANGED';
        END IF;
    END IF;
    IF src->>'kind'='release' THEN
        IF NOT public.business_exact_keys(src,ARRAY['kind','release_ref','expected_version','source_ref'])
            OR NOT public.business_exact_keys(src->'source_ref',ARRAY['ref','version'])
            OR jsonb_typeof(src->'release_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(src->'expected_version') IS DISTINCT FROM 'number' OR src->>'expected_version' !~ '^[1-9][0-9]*$'
            OR jsonb_typeof(src->'source_ref'->'ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(src->'source_ref'->'version') IS DISTINCT FROM 'number' OR src->'source_ref'->>'version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT version,subscription_id INTO v_row FROM public.rss_sources WHERE id=src->'source_ref'->>'ref' FOR UPDATE;
        IF NOT FOUND OR (mg->>'kind'='instance' AND v_row.subscription_id IS DISTINCT FROM mg->>'subscription_ref')
            OR v_row.version IS DISTINCT FROM (src->'source_ref'->>'version')::bigint THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_SOURCE_CHANGED';
        END IF;
        SELECT version,rss_source_id,download_task_id INTO v_row FROM public.releases WHERE id=src->>'release_ref' FOR UPDATE;
        IF NOT FOUND OR v_row.rss_source_id IS DISTINCT FROM src->'source_ref'->>'ref'
            OR v_row.version IS DISTINCT FROM (src->>'expected_version')::bigint OR v_row.download_task_id IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_SOURCE_CHANGED';
        END IF;
    ELSIF src->>'kind'='seed' AND public.business_exact_keys(src,ARRAY['kind','seed_identity_digest']) THEN
        -- CP has not yet registered seed_identity_digest's encoding domain.
        -- A syntactically plausible digest cannot authorize a seed as a whole.
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROOF_UNSUPPORTED';
    ELSE
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    IF obs <> 'null'::jsonb THEN
        IF NOT public.business_exact_keys(obs,ARRAY['scope','snapshot_ref','snapshot_version','observation_digest'])
            OR NOT public.business_exact_keys(obs->'scope',ARRAY['provider','catalog_entry_ref'])
            OR jsonb_typeof(obs->'snapshot_version') IS DISTINCT FROM 'number' OR obs->>'snapshot_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT provider,catalog_entry_ref,version,observation_digest,observation INTO v_group
        FROM public.provider_group_snapshots WHERE id=obs->>'snapshot_ref' FOR SHARE;
        IF NOT FOUND OR v_group.provider IS DISTINCT FROM obs->'scope'->>'provider'
            OR v_group.catalog_entry_ref IS DISTINCT FROM obs->'scope'->>'catalog_entry_ref'
            OR v_group.version IS DISTINCT FROM (obs->>'snapshot_version')::bigint
            OR v_group.observation_digest IS NULL OR v_group.observation_digest IS DISTINCT FROM obs->>'observation_digest'
            OR v_group.observation->'completeness'->'value'->'complete' IS DISTINCT FROM 'true'::jsonb
            OR EXISTS(SELECT 1 FROM jsonb_array_elements(v_group.observation->'groups') g WHERE g->'available'->>'state' IS DISTINCT FROM 'known') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_OBSERVATION_CHANGED';
        END IF;
    END IF;
    IF feed <> 'null'::jsonb THEN
        IF NOT public.business_exact_keys(feed,ARRAY['snapshot_ref','snapshot_version','scope','group_ref','content_hash','hash_schema','projection_schema','stable_seed_key','payload_digest','manifest_digest'])
            OR NOT public.business_exact_keys(feed->'scope',ARRAY['provider','catalog_entry_ref'])
            OR jsonb_typeof(feed->'snapshot_version') IS DISTINCT FROM 'number' OR feed->>'snapshot_version' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        SELECT provider,provider_entry_ref,provider_group_ref,version,content_hash,hash_schema,projection_schema,fresh_until INTO v_feed
        FROM public.provider_feed_snapshots WHERE id=feed->>'snapshot_ref' FOR SHARE;
        IF NOT FOUND OR v_feed.provider IS DISTINCT FROM feed->'scope'->>'provider'
            OR v_feed.provider_entry_ref IS DISTINCT FROM feed->'scope'->>'catalog_entry_ref'
            OR v_feed.provider_group_ref IS DISTINCT FROM feed->>'group_ref'
            OR v_feed.version IS DISTINCT FROM (feed->>'snapshot_version')::bigint
            OR v_feed.content_hash IS DISTINCT FROM feed->>'content_hash'
            OR v_feed.hash_schema<>'provider-feed-snapshot-v2' OR v_feed.hash_schema IS DISTINCT FROM feed->>'hash_schema'
            OR v_feed.projection_schema<>'torrent-manifest-v1' OR v_feed.projection_schema IS DISTINCT FROM feed->>'projection_schema' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_FEED_CHANGED';
        END IF;
        PERFORM public.assert_feed_snapshot_set(feed->>'snapshot_ref');
        SELECT s.payload_digest,s.manifest_payload_digest AS blob_digest,s.manifest_projection_digest AS projection_digest,
            s.torrent_state INTO v_seed
        FROM public.provider_feed_snapshot_seeds s
        WHERE s.snapshot_id=feed->>'snapshot_ref' AND s.stable_seed_key=feed->>'stable_seed_key';
        IF NOT FOUND OR v_seed.torrent_state<>'available' OR v_seed.payload_digest IS DISTINCT FROM p_payload_digest
            OR v_seed.payload_digest IS DISTINCT FROM feed->>'payload_digest'
            OR v_seed.blob_digest IS DISTINCT FROM feed->>'payload_digest'
            OR v_seed.projection_digest IS DISTINCT FROM feed->>'manifest_digest' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_FEED_CHANGED';
        END IF;
        IF obs<>'null'::jsonb AND (obs->'scope' IS DISTINCT FROM feed->'scope' OR NOT EXISTS(
            SELECT 1 FROM jsonb_array_elements(v_group.observation->'groups') g
            WHERE g->>'group_ref'=feed->>'group_ref' AND g->'available'->'value'='true'::jsonb)) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_OBSERVATION_CHANGED';
        END IF;
    END IF;
    SELECT content INTO v_program FROM public.attr_rule_versions WHERE fingerprint=programs->>'attr_fingerprint' FOR SHARE;
    IF NOT FOUND OR NOT public.business_exact_keys(v_program,ARRAY['version','resolution','quality','language','special_markers','group_brackets']) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROGRAM_CHANGED';
    END IF;
    v_canonical:='{"version":' || (v_program->>'version') || ',"resolution":' || public.anime_config_candidate_canonical_json(v_program->'resolution')
        || ',"quality":' || public.anime_config_candidate_canonical_json(v_program->'quality')
        || ',"language":' || public.anime_config_candidate_canonical_json(v_program->'language')
        || ',"special_markers":' || public.anime_config_candidate_canonical_json(v_program->'special_markers')
        || ',"group_brackets":' || public.anime_config_candidate_canonical_json(v_program->'group_brackets') || '}';
    -- Attr canonical uses Go HTML-escaped strings, unlike acquisition values.
    v_canonical:=replace(replace(replace(replace(replace(v_canonical,'&',chr(92)||'u0026'),'<',chr(92)||'u003c'),'>',chr(92)||'u003e'),chr(8232),chr(92)||'u2028'),chr(8233),chr(92)||'u2029');
    IF 'sha256:' || encode(sha256(convert_to(v_canonical,'UTF8')),'hex') IS DISTINCT FROM programs->>'attr_fingerprint' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROGRAM_CHANGED';
    END IF;
    IF programs->'vars_digest'<>'null'::jsonb THEN
        SELECT content INTO v_program FROM public.naming_vars_versions WHERE fingerprint=programs->>'vars_digest' FOR SHARE;
        IF NOT FOUND OR jsonb_typeof(v_program) IS DISTINCT FROM 'string'
            OR 'sha256:' || encode(sha256(convert_to(v_program #>> '{}','UTF8')),'hex') IS DISTINCT FROM programs->>'vars_digest'
            OR strpos(v_program #>> '{}',chr(13))>0 THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_PROGRAM_CHANGED';
        END IF;
    END IF;
    v_now:=clock_timestamp();
    IF feed<>'null'::jsonb THEN
        IF v_feed.fresh_until<=v_now THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_FEED_EXPIRED';
        END IF;
    END IF;
    RETURN v_now;
END;
$_$;

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
    -- `anibt_tags` is deliberately NOT in this key list: the content hash is
    -- blind to it, and the canonical SQL encoder (feed_seed_canonical) never
    -- reads it. It is listed here only so a caller may declare it.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s WHERE NOT public.business_exact_keys(s,ARRAY[
        'id','stable_seed_key','locator','locator_display','title','published_at','size_bytes','info_hash','payload_digest',
        'torrent_filename','torrent_state','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2',
        'payload_index','manifest','files','anibt_tags','source_site','source_guid','details_url']))
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
    VALUES(p_id,p_provider,p_entry,p_group,p_snapshot_kind,p_search_parser_version,v_version,p_fetched_at,p_content_hash,'provider-feed-snapshot-v2',
        'torrent-manifest-v1',CURRENT_TIMESTAMP+INTERVAL '15 minutes',
        CASE WHEN p_snapshot_kind = 'search' THEN p_fetched_at + v_retention ELSE CURRENT_TIMESTAMP + v_retention END,
        jsonb_array_length(p_seeds));
    INSERT INTO public.provider_feed_snapshot_seeds(id,snapshot_id,stable_seed_key,locator,locator_display,title,
        published_at,size_bytes,info_hash,payload_digest,torrent_filename,torrent_state,torrent_error_code,torrent_error_reason,
        torrent_version,info_hash_v1,info_hash_v2,payload,payload_size_bytes,
        manifest_payload_digest,manifest_torrent_parser_version,manifest_torrent_name,manifest_total_size_bytes,manifest_file_count,
        manifest_projection_digest,files,anibt_tags,source_site,source_guid,details_url)
    SELECT x.id,p_id,x.stable_seed_key,x.locator,x.locator_display,x.title,x.published_at,x.size_bytes,x.info_hash,
        x.payload_digest,x.torrent_filename,x.torrent_state,x.torrent_error_code,x.torrent_error_reason,x.torrent_version,x.info_hash_v1,x.info_hash_v2,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE p_payloads[x.payload_index] END,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE octet_length(p_payloads[x.payload_index]) END,
        m.manifest_payload_digest,m.manifest_torrent_parser_version,m.manifest_torrent_name,m.manifest_total_size_bytes,
        m.manifest_file_count,m.manifest_projection_digest,x.files,
        CASE WHEN jsonb_typeof(x.anibt_tags) = 'object' THEN public.anibt_tag_block(x.anibt_tags) ELSE NULL END,
        x.source_site,x.source_guid,x.details_url
    FROM jsonb_to_recordset(p_seeds) AS x(id TEXT,stable_seed_key TEXT,locator TEXT,locator_display TEXT,title TEXT,
        published_at TIMESTAMPTZ,size_bytes BIGINT,info_hash TEXT,payload_digest TEXT,torrent_filename TEXT,torrent_state TEXT,
        torrent_error_code TEXT,torrent_error_reason TEXT,torrent_version TEXT,info_hash_v1 TEXT,info_hash_v2 TEXT,payload_index INTEGER,
        manifest JSONB,files JSONB,anibt_tags JSONB,source_site JSONB,source_guid TEXT,details_url TEXT)
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
