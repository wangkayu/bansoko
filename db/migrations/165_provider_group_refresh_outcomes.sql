-- An attempt can be recorded before there is an outcome. Existing rows retain
-- their last completed outcome, even while a new attempt is in progress.
ALTER TABLE public.provider_group_refresh_health
    DROP CONSTRAINT provider_group_refresh_health_state_check,
    DROP CONSTRAINT provider_group_refresh_health_check,
    DROP CONSTRAINT provider_group_refresh_health_check1,
    ALTER COLUMN state DROP NOT NULL,
    ADD COLUMN checked_at timestamptz,
    ADD COLUMN cooldown_until timestamptz;

UPDATE public.provider_group_refresh_health
SET state = CASE WHEN state = 'fresh' THEN 'available' ELSE state END,
    checked_at = last_attempt_at;

ALTER TABLE public.provider_group_refresh_health
    ADD CONSTRAINT provider_group_refresh_health_state_check
        CHECK (state IN ('available', 'empty', 'not_indexed', 'rate_limited', 'cooldown', 'error')),
    ADD CONSTRAINT provider_group_refresh_health_checked_check
        CHECK ((state IS NULL) = (checked_at IS NULL)),
    ADD CONSTRAINT provider_group_refresh_health_reason_check
        CHECK ((COALESCE(state IN ('not_indexed', 'rate_limited', 'cooldown', 'error'), false)) = (error_code IS NOT NULL)),
    ADD CONSTRAINT provider_group_refresh_health_cooldown_check
        CHECK ((state IS DISTINCT FROM 'cooldown' OR cooldown_until IS NOT NULL)
            AND (cooldown_until IS NULL OR COALESCE(state IN ('rate_limited', 'cooldown')
                OR (state = 'error' AND error_code = 'PROVIDER_RETRY_AFTER'), false)));

-- Keep the existing owner-only write boundary and reject incomplete evidence
-- even when a caller invokes the function directly.
CREATE OR REPLACE FUNCTION publish_group_observation(p_canonical text, p_digest text, p_entries jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    o JSONB;
    v_group JSONB;
    v_entry JSONB;
    v_schema TEXT;
    v_evidence TEXT;
    v_previous TEXT := NULL;
    v_version BIGINT;
    v_source TEXT;
    v_time TIMESTAMPTZ;
BEGIN
    o := p_canonical::jsonb;
    -- All canonical observation strings prohibit controls, so the existing
    -- sorted-key SQL canonical encoder has the same string bytes as the Go
    -- acquisition codec here. Equality also rejects duplicate/missing keys,
    -- whitespace, and noncanonical numeric tokens before they can be stored.
    IF NOT public.business_exact_keys(o, ARRAY['schema_version','scope','snapshot_ref',
        'snapshot_version','observed_at','source_version','provider_schema','completeness','groups'])
        OR p_canonical IS DISTINCT FROM public.anime_config_candidate_canonical_json(o)
        OR p_digest IS DISTINCT FROM 'sha256:' || encode(sha256(
            convert_to('acquisition-group-observation:v1','UTF8') || decode('00','hex')
            || convert_to(p_canonical,'UTF8')), 'hex')
        OR o->'schema_version' IS DISTINCT FROM '1'::jsonb
        OR o->>'schema_version' IS DISTINCT FROM '1'
        OR NOT public.business_exact_keys(o->'scope', ARRAY['provider','catalog_entry_ref'])
        OR jsonb_typeof(o->'scope'->'provider') IS DISTINCT FROM 'string'
        OR jsonb_typeof(o->'snapshot_version') IS DISTINCT FROM 'number'
        OR (o->>'snapshot_version') !~ '^[1-9][0-9]*$'
        OR jsonb_typeof(o->'snapshot_ref') IS DISTINCT FROM 'string'
        OR octet_length(o->>'snapshot_ref') NOT BETWEEN 1 AND 128
        OR (o->>'snapshot_ref') ~ '[[:cntrl:]]'
        OR o->>'snapshot_ref' <> btrim(o->>'snapshot_ref')
        OR (o->'scope'->>'catalog_entry_ref') !~ '^[1-9][0-9]*$'
        OR jsonb_typeof(o->'scope'->'catalog_entry_ref') IS DISTINCT FROM 'string'
        OR (o->'scope'->>'catalog_entry_ref')::numeric > 18446744073709551615
        OR jsonb_typeof(o->'provider_schema') IS DISTINCT FROM 'string'
        OR octet_length(o->>'provider_schema') NOT BETWEEN 1 AND 128
        OR (o->>'provider_schema') ~ '[[:cntrl:]]'
        OR jsonb_typeof(o->'groups') IS DISTINCT FROM 'array'
        OR jsonb_typeof(p_entries) IS DISTINCT FROM 'array'
        OR jsonb_array_length(o->'groups') <> jsonb_array_length(p_entries)
        OR NOT public.group_observation_value_ok(o->'source_version', TRUE)
        OR NOT public.group_observation_value_ok(o->'completeness', FALSE)
        OR p_canonical ~ '[[:cntrl:]]' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INVALID';
    END IF;
    IF o->'scope'->>'provider' = 'mikan' THEN
        v_schema := 'mikan-detail-groups-v1'; v_evidence := 'mikan-detail-group-blocks-v1';
    ELSIF o->'scope'->>'provider' = 'anibt' THEN
        v_schema := 'anibt-anime-groups-v1'; v_evidence := 'anibt-entry-total-v1';
    ELSE
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INVALID';
    END IF;
    IF o->'completeness'->>'state' IS DISTINCT FROM 'known'
        OR NOT public.business_exact_keys(o->'completeness'->'value',ARRAY['complete','evidence','adapter_version'])
        OR o->'completeness'->'value'->'complete' IS DISTINCT FROM 'true'::jsonb
        OR o->'completeness'->'value'->>'evidence' IS DISTINCT FROM v_evidence
        OR o->'completeness'->'value'->>'adapter_version' IS DISTINCT FROM v_schema
        OR o->>'provider_schema' IS DISTINCT FROM v_schema THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_EVIDENCE_INVALID';
    END IF;
    v_source := '';
    IF o->'source_version'->>'state' = 'known' THEN
        IF jsonb_typeof(o->'source_version'->'value') IS DISTINCT FROM 'string'
            OR octet_length(o->'source_version'->>'value') NOT BETWEEN 1 AND 256
            OR (o->'source_version'->>'value') ~ '[[:cntrl:]]' THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INVALID';
        END IF;
        v_source := o->'source_version'->>'value';
    END IF;
    IF jsonb_typeof(o->'observed_at') IS DISTINCT FROM 'string' OR o->>'observed_at' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{0,8}[1-9])?Z$' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INVALID';
    END IF;
    v_time := (o->>'observed_at')::timestamptz;
    FOR v_group IN SELECT value FROM jsonb_array_elements(o->'groups') LOOP
        IF NOT public.business_exact_keys(v_group,ARRAY['group_ref','display_name','available','observed_at'])
            OR jsonb_typeof(v_group->'group_ref') IS DISTINCT FROM 'string'
            OR (v_previous IS NOT NULL AND (v_group->>'group_ref') COLLATE "C" <= v_previous COLLATE "C")
            OR (v_schema='mikan-detail-groups-v1' AND (v_group->>'group_ref' !~ '^[1-9][0-9]*$'
                OR (v_group->>'group_ref')::numeric > 18446744073709551615))
            OR (v_schema='anibt-anime-groups-v1' AND v_group->>'group_ref' !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$')
            OR jsonb_typeof(v_group->'display_name') IS DISTINCT FROM 'string'
            OR length(v_group->>'display_name') NOT BETWEEN 1 AND 256
            OR btrim(v_group->>'display_name') = ''
            OR (v_group->>'display_name') ~ '[[:cntrl:]]'
            OR jsonb_typeof(v_group->'observed_at') IS DISTINCT FROM 'string'
            OR v_group->>'observed_at' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{0,8}[1-9])?Z$'
            OR NOT public.group_observation_value_ok(v_group->'available',FALSE)
            OR v_group->'available'->>'state' IS DISTINCT FROM 'known'
            OR jsonb_typeof(v_group->'available'->'value') IS DISTINCT FROM 'boolean' THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INVALID';
        END IF;
        v_previous := v_group->>'group_ref';
    END LOOP;
    -- A complete, unique locator projection accompanies every observed group.
    -- Neither this separate locator nor any availability copy is hashed as a
    -- competing authority; availability is read only from the observation.
    FOR v_entry IN SELECT value FROM jsonb_array_elements(p_entries) LOOP
        IF NOT public.business_exact_keys(v_entry,ARRAY['group_ref','feed_locator'])
            OR NOT public.business_exact_keys(v_entry->'feed_locator',ARRAY['provider','catalog_entry_ref','group_ref'])
            OR NOT public.business_exact_keys(v_entry->'feed_locator'->'group_ref',ARRAY['provider','external_ref'])
            OR jsonb_typeof(v_entry->'group_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(v_entry->'feed_locator'->'provider') IS DISTINCT FROM 'string'
            OR jsonb_typeof(v_entry->'feed_locator'->'catalog_entry_ref') IS DISTINCT FROM 'string'
            OR jsonb_typeof(v_entry->'feed_locator'->'group_ref'->'provider') IS DISTINCT FROM 'string'
            OR jsonb_typeof(v_entry->'feed_locator'->'group_ref'->'external_ref') IS DISTINCT FROM 'string'
            OR v_entry->'feed_locator'->>'provider' IS DISTINCT FROM o->'scope'->>'provider'
            OR v_entry->'feed_locator'->>'catalog_entry_ref' IS DISTINCT FROM o->'scope'->>'catalog_entry_ref'
            OR v_entry->'feed_locator'->'group_ref'->>'provider' IS DISTINCT FROM o->'scope'->>'provider'
            OR v_entry->'feed_locator'->'group_ref'->>'external_ref' IS DISTINCT FROM v_entry->>'group_ref' THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_PROJECTION_INVALID';
        END IF;
    END LOOP;
    PERFORM pg_advisory_xact_lock(hashtext('bansoko:provider-groups:' || (o->'scope'->>'provider') || ':' || (o->'scope'->>'catalog_entry_ref')));
    SELECT COALESCE(max(version),0)+1 INTO v_version FROM public.provider_group_snapshots
    WHERE provider=o->'scope'->>'provider' AND catalog_entry_ref=o->'scope'->>'catalog_entry_ref';
    IF v_version IS DISTINCT FROM (o->>'snapshot_version')::bigint THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_VERSION_CONFLICT';
    END IF;
    INSERT INTO public.provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,
        source_version,schema_version,group_count,version,retention_until,canonical_observation,observation_digest,entries)
    VALUES (o->>'snapshot_ref',o->'scope'->>'provider',o->'scope'->>'catalog_entry_ref',v_time,
        v_source,o->>'provider_schema',jsonb_array_length(o->'groups'),v_version,
        CURRENT_TIMESTAMP + INTERVAL '30 days',p_canonical,p_digest,
        COALESCE((SELECT jsonb_agg(jsonb_build_object('group_ref',g->>'group_ref','display_name',g->>'display_name',
            'feed_locator',e->'feed_locator','observed_at',g->>'observed_at') ORDER BY g->>'group_ref' COLLATE "C")
        FROM jsonb_array_elements(o->'groups') g JOIN jsonb_array_elements(p_entries) e ON e->>'group_ref'=g->>'group_ref'),'[]'::jsonb));
END;
$_$;
