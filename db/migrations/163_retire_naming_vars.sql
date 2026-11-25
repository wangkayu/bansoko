-- The migration runner performs the AST preflight before applying this file.
-- This lexical backstop checks obvious references when SQL is applied directly.
DO $$
BEGIN
 IF EXISTS (
  WITH documents(body) AS (
   SELECT value FROM public.global_config_sections WHERE section_name='naming_rules'
   UNION ALL SELECT value FROM public.naming_template_library
   UNION ALL SELECT value FROM public.anime_config_overrides WHERE section_name='naming_rules'
   UNION ALL SELECT business_snapshot #> '{sections,naming_rules}' FROM public.anime_business_configs
   UNION ALL SELECT draft #> '{business,sections,naming_rules}' FROM public.anime_config_candidates
    WHERE activated_managed_anime_id IS NULL AND status='working'
   UNION ALL SELECT r.draft #> '{business,sections,naming_rules}' FROM public.anime_config_candidate_revisions r
    JOIN public.anime_config_candidates c ON c.id=r.candidate_id WHERE c.activated_managed_anime_id IS NULL AND c.status='working'
   UNION ALL SELECT r.business_snapshot #> '{sections,naming_rules}' FROM public.anime_config_candidate_revisions r
    JOIN public.anime_config_candidates c ON c.id=r.candidate_id WHERE c.activated_managed_anime_id IS NULL AND c.status='working'
   UNION ALL SELECT jsonb_build_object('title_template', e.value->'title_template', 'body_template', e.value->'body_template')
    FROM public.plugin_target_configs p
    CROSS JOIN LATERAL jsonb_each(NULLIF(p.template_set->'events', 'null'::jsonb)) e(key,value)
    WHERE p.deleted_at IS NULL AND e.value->>'mode'='custom'
   UNION ALL SELECT input #> '{delivery,naming,templates}' FROM public.acquisitions
    WHERE state='running' AND record_state='full' AND target_sealed_at IS NULL
   UNION ALL SELECT s.value->'value' FROM public.acquisitions a
    CROSS JOIN LATERAL jsonb_array_elements(a.input #> '{config,sections}') s(value)
    WHERE a.state='running' AND a.record_state='full' AND a.target_sealed_at IS NULL
      AND s.value->>'section_key'='naming_rules'
  )
  SELECT 1 FROM documents d
  CROSS JOIN LATERAL jsonb_path_query(d.body,
   'lax $.** ? (@.type() == "object").keyvalue() ? (@.key == "directory_template" || @.key == "video_filename_template" || @.key == "subtitle_suffix_template" || @.key == "title_template" || @.key == "body_template").value') t(value)
  WHERE jsonb_typeof(t.value)='string'
    AND (t.value #>> '{}') ~ '(\$\{|\{\{)[[:space:]]*((__ctx|context)[[:space:]]*\.[[:space:]]*)?[vV]ars[[:space:]]*(\.|\[)'
 ) THEN
  RAISE EXCEPTION 'NAMING_VARS_TEMPLATES_PRESENT';
 END IF;
END $$;

CREATE OR REPLACE FUNCTION public.anime_config_candidate_draft_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $_$
DECLARE
 b JSONB;
 sections TEXT[] := ARRAY['subgroups','parser','release_rules','file_rules','naming_rules','file_effect'];
 name TEXT;
BEGIN
 IF NOT anime_config_candidate_identity_shape_ok(draft - 'business' - 'business_digest',origin,origin_entry_ref,instance_label) THEN RETURN FALSE; END IF;
 IF NOT (draft ? 'business') AND NOT (draft ? 'business_digest') THEN RETURN TRUE; END IF;
 b := draft->'business';
 IF NOT business_exact_keys(b,ARRAY['schema_version','sections','section_versions','section_schemas','parser','replacement','replacement_version','unclassified_route','file_effect_version']) THEN RETURN FALSE; END IF;
 IF b->>'schema_version' IS DISTINCT FROM 'business-snapshot-v3'
    OR jsonb_typeof(draft->'business_digest') IS DISTINCT FROM 'string'
    OR draft->>'business_digest' !~ '^sha256:[a-f0-9]{64}$'
    OR draft->>'business_digest' IS DISTINCT FROM 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(b),'UTF8'),'sha256'),'hex') THEN RETURN FALSE; END IF;
 IF NOT business_exact_keys(b->'sections',sections)
    OR NOT business_exact_keys(b->'section_versions',sections)
    OR NOT business_exact_keys(b->'section_schemas',sections) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY sections LOOP
  IF jsonb_typeof(b->'sections'->name) IS DISTINCT FROM 'object'
     OR jsonb_typeof(b->'section_versions'->name) IS DISTINCT FROM 'number'
     OR (b->'section_versions'->>name)::numeric < 0
     OR (b->'section_versions'->>name)::numeric <> floor((b->'section_versions'->>name)::numeric)
     OR jsonb_typeof(b->'section_schemas'->name) IS DISTINCT FROM 'string' THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'parser',ARRAY['kind','policy_version','provenance','profile_version','rules_owner_version','schema_version','source_owner'])
    OR b->'parser'->>'schema_version' IS DISTINCT FROM 'parser-policy-envelope-v1'
    OR NOT business_exact_keys(b->'replacement',ARRAY['admission_mode','quality_profile'])
    OR b->'replacement'->>'admission_mode' NOT IN ('all','per_group','single_copy')
    OR NOT replacement_quality_profile_valid(b->'replacement'->'quality_profile')
    OR (b->'replacement') ? 'cutoff'::text
    OR b->>'unclassified_route' NOT IN ('route','skip') THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['replacement_version','file_effect_version'] LOOP
  IF jsonb_typeof(b->name) IS DISTINCT FROM 'number' OR (b->>name)::numeric < 0 OR (b->>name)::numeric <> floor((b->>name)::numeric) THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'replacement'->'quality_profile',ARRAY['version','items']) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['release_rules','file_rules'] LOOP
  IF NOT business_exact_keys(b->'sections'->name,ARRAY['must_contain','not_in'])
     OR jsonb_typeof(b->'sections'->name->'must_contain') IS DISTINCT FROM 'string'
     OR jsonb_typeof(b->'sections'->name->'not_in') IS DISTINCT FROM 'string' THEN RETURN FALSE; END IF;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN RETURN FALSE;
END;
$_$;

-- Only owner configs and working drafts/revisions remain admission inputs.
-- Linked rows and sealed acquisition and operation evidence are not rewritten.
CREATE FUNCTION pg_temp.business_snapshot_v3(document jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
 SELECT jsonb_set(document - 'programs', '{schema_version}', '"business-snapshot-v3"'::jsonb)
$$;

CREATE FUNCTION pg_temp.business_snapshot_digest(document jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
 SELECT 'sha256:' || encode(digest(convert_to(public.anime_config_candidate_canonical_json(document),'UTF8'),'sha256'),'hex')
$$;

CREATE FUNCTION pg_temp.business_draft_v3(draft jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
 SELECT CASE WHEN draft->'business'->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2' THEN draft
             WHEN draft ? 'business_digest' THEN
                jsonb_set(jsonb_set(draft, '{business}', upgraded),
                          '{business_digest}', to_jsonb(pg_temp.business_snapshot_digest(upgraded)))
             ELSE jsonb_set(draft, '{business}', upgraded) END
 FROM (SELECT pg_temp.business_snapshot_v3(draft->'business') AS upgraded) s
$$;

WITH upgraded AS (
 SELECT managed_anime_id, pg_temp.business_snapshot_v3(business_snapshot) AS body
 FROM public.anime_business_configs WHERE business_snapshot->>'schema_version'='business-snapshot-v2'
)
UPDATE public.anime_business_configs b
SET business_snapshot=u.body, business_digest=pg_temp.business_snapshot_digest(u.body),
    business_version=b.business_version+1, updated_at=CURRENT_TIMESTAMP
FROM upgraded u WHERE b.managed_anime_id=u.managed_anime_id;

WITH upgraded AS (
 SELECT id, pg_temp.business_draft_v3(draft) AS draft
 FROM public.anime_config_candidates WHERE activated_managed_anime_id IS NULL AND status='working'
  AND draft->'business'->>'schema_version'='business-snapshot-v2'
)
UPDATE public.anime_config_candidates c
SET draft=u.draft, draft_digest=pg_temp.business_snapshot_digest(u.draft)
FROM upgraded u WHERE c.id=u.id;

ALTER TABLE public.anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
WITH upgraded AS (
 SELECT r.candidate_id, r.revision, pg_temp.business_draft_v3(r.draft) AS draft,
        CASE WHEN r.business_snapshot->>'schema_version'='business-snapshot-v2'
             THEN pg_temp.business_snapshot_v3(r.business_snapshot) ELSE r.business_snapshot END AS body
 FROM public.anime_config_candidate_revisions r JOIN public.anime_config_candidates c ON c.id=r.candidate_id
 WHERE c.activated_managed_anime_id IS NULL AND c.status='working' AND (r.business_snapshot->>'schema_version'='business-snapshot-v2'
    OR r.draft->'business'->>'schema_version'='business-snapshot-v2')
)
UPDATE public.anime_config_candidate_revisions r
SET draft=u.draft, draft_digest=pg_temp.business_snapshot_digest(u.draft),
    business_snapshot=u.body, business_digest=pg_temp.business_snapshot_digest(u.body)
FROM upgraded u WHERE r.candidate_id=u.candidate_id AND r.revision=u.revision;
ALTER TABLE public.anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg;

CREATE OR REPLACE FUNCTION public.json_member_bytes(p_bytes text, p_key text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT e.value::text FROM json_each(p_bytes::json) e WHERE e.key=p_key
$$;
REVOKE ALL ON FUNCTION public.json_member_bytes(text, text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.recheck_acquisition_qualification(p_qualification jsonb, p_request_hash text, p_config_digest text, p_payload_digest text) RETURNS timestamp with time zone
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    c JSONB; mg JSONB; src JSONB; obs JSONB; feed JSONB;
    v_row RECORD; v_group RECORD; v_feed RECORD; v_seed RECORD;
    v_now TIMESTAMPTZ; v_kind TEXT;
BEGIN
    IF NOT public.business_exact_keys(p_qualification,ARRAY['config','management','source','observation','feed','placement'])
        OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
        OR p_config_digest IS NULL OR p_config_digest !~ '^sha256:[0-9a-f]{64}$'
        OR p_payload_digest IS NULL OR p_payload_digest !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    c:=p_qualification->'config'; mg:=p_qualification->'management'; src:=p_qualification->'source';
    obs:=p_qualification->'observation'; feed:=p_qualification->'feed';
    -- No ordinary Placement row is a FileFact/OS proof.
    IF p_qualification->'placement' IS DISTINCT FROM '{"kind":"none"}'::jsonb THEN
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
    v_now:=clock_timestamp();
    IF feed<>'null'::jsonb THEN
        IF v_feed.fresh_until<=v_now THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_FEED_EXPIRED';
        END IF;
    END IF;
    RETURN v_now;
END;
$_$;

CREATE OR REPLACE FUNCTION public.accept_acquisition_queued(p_acquisition_id text, p_entry_kind text, p_access_scope text, p_request_namespace text, p_request_key text, p_request_hash text, p_canonical_request jsonb, p_admission_digest text, p_config_identity jsonb, p_display_title jsonb, p_origin_managed_anime_ref text, p_detail_retention_seconds integer, p_receipt_after_accept_seconds integer, p_receipt_after_terminal_seconds integer, p_preparation_mode text, p_preparation_digest text, p_preparation_witness text, p_input_canonical_utf8 text, p_payload bytea, p_validated_infohash_v1 text, p_validated_infohash_v2 text, p_manifest_digest text, p_manifest_files jsonb, p_source_release_id text, p_download_task_id text, p_task_idempotency_key text, p_qbittorrent_connection_id text, p_task_frozen_input jsonb, p_task_frozen_policy jsonb, p_submit_work_item_id text, p_settle_work_item_id text, p_settle_idempotency_key text, p_submit_input_snapshot jsonb, p_max_attempts integer, p_qualification jsonb, p_business_snapshot jsonb, p_witness_max_bytes integer, p_witness_max_rows integer, p_correlation_id text, p_causation_type text, p_causation_id text, p_default_sections jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    v_input_version INTEGER;
    v_now TIMESTAMPTZ;
    v_now_text TEXT;
    v_replay_until_text TEXT;
    v_existing RECORD;
    v_admission_receipt JSONB;
    v_witness_digest TEXT;
    v_manifest_count INTEGER;
    v_config_digest TEXT;
    v_final RECORD;
    v_global RECORD;
BEGIN
    -- One input schema: schema_version 1 (PRUNE-046 S4 flattening).
    IF (p_input_canonical_utf8::jsonb->'schema_version') IS DISTINCT FROM '1'::jsonb THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='ACQ_ACCEPT_INPUT_VERSION_UNSUPPORTED';
    END IF;
    v_input_version := 1;
    -- Parameter shape ------------------------------------------------------
    IF p_correlation_id IS NULL OR char_length(btrim(p_correlation_id)) NOT BETWEEN 1 AND 128
        OR p_causation_type IS NULL OR char_length(btrim(p_causation_type)) NOT BETWEEN 1 AND 128
        OR p_causation_id IS NULL OR char_length(btrim(p_causation_id)) NOT BETWEEN 1 AND 128
        OR p_acquisition_id IS NULL OR btrim(p_acquisition_id) = ''
        OR p_download_task_id IS NULL OR btrim(p_download_task_id) = ''
        OR p_submit_work_item_id IS NULL OR btrim(p_submit_work_item_id) = ''
        OR p_settle_work_item_id IS NULL OR btrim(p_settle_work_item_id) = ''
        OR p_source_release_id IS NULL OR btrim(p_source_release_id) = '' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_BAD_IDENTITY';
    END IF;
    IF jsonb_typeof(p_canonical_request) IS DISTINCT FROM 'object'
        OR jsonb_typeof(p_config_identity) IS DISTINCT FROM 'object'
        OR jsonb_typeof(p_display_title) IS DISTINCT FROM 'object'
        OR jsonb_typeof(p_task_frozen_input) IS DISTINCT FROM 'object'
        OR jsonb_typeof(p_task_frozen_policy) IS DISTINCT FROM 'object'
        OR jsonb_typeof(p_submit_input_snapshot) IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_BAD_IDENTITY';
    END IF;
    -- The approved retention range is 30-90 days for all three windows.
    IF p_detail_retention_seconds IS NULL OR p_detail_retention_seconds NOT BETWEEN 2592000 AND 7776000
        OR p_receipt_after_accept_seconds IS NULL OR p_receipt_after_accept_seconds NOT BETWEEN 2592000 AND 7776000
        OR p_receipt_after_terminal_seconds IS NULL OR p_receipt_after_terminal_seconds NOT BETWEEN 2592000 AND 7776000 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_BAD_RETENTION';
    END IF;
    IF p_preparation_mode IS NULL OR p_preparation_mode NOT IN ('none', 'provider_selection_v1')
        OR (p_preparation_mode = 'none' AND (p_preparation_digest IS NOT NULL OR p_preparation_witness IS NOT NULL))
        OR (p_preparation_mode = 'provider_selection_v1' AND (p_preparation_digest IS NULL OR p_preparation_witness IS NULL)) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_PREPARATION_MISMATCH';
    END IF;
    IF p_max_attempts IS NULL OR p_max_attempts NOT BETWEEN 1 AND 100 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_BAD_IDENTITY';
    END IF;
    -- The early-sealed manifest is the authoritative file list of the verified
    -- payload; a queued accept never starts over an empty one.
    IF jsonb_typeof(p_manifest_files) IS DISTINCT FROM 'array'
        OR jsonb_array_length(p_manifest_files) = 0 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_EMPTY_MANIFEST';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_to_recordset(p_manifest_files)
               AS x(file_ref TEXT, ordinal INTEGER, relative_path TEXT, exact_size BIGINT, media_kind TEXT, identity_evidence JSONB)
               WHERE x.file_ref IS NULL OR x.ordinal IS NULL OR x.relative_path IS NULL
                  OR x.ordinal NOT BETWEEN 0 AND jsonb_array_length(p_manifest_files) - 1
                  OR x.exact_size IS NULL OR x.media_kind IS NULL OR x.identity_evidence IS NULL) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_MALFORMED_SET';
    END IF;

    -- Schema 4 freezes the torrent totals of exactly this manifest.
    IF (
        (p_input_canonical_utf8::jsonb)->'source'->'torrent_file_count' IS DISTINCT FROM
            jsonb_build_object('state','known','value',jsonb_array_length(p_manifest_files),'reason_code',NULL)
        OR (p_input_canonical_utf8::jsonb)->'source'->'torrent_size' IS DISTINCT FROM
            jsonb_build_object('state','known','value',(SELECT sum((f->>'exact_size')::numeric) FROM jsonb_array_elements(p_manifest_files) f),'reason_code',NULL)) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_MALFORMED_SET';
    END IF;

    -- Accept-key serialization, then the idempotent replay read -------------
    PERFORM pg_advisory_xact_lock(hashtextextended(
        p_access_scope || E'\x1f' || p_request_namespace || E'\x1f' || p_request_key, 0));
    SELECT root.request_hash, root.admission_receipt, root.terminal_receipt
    INTO v_existing
    FROM public.acquisitions root
    WHERE root.access_scope = p_access_scope
      AND root.request_namespace = p_request_namespace
      AND root.request_key = p_request_key;
    IF FOUND THEN
        IF v_existing.request_hash IS DISTINCT FROM p_request_hash THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'IDEMPOTENCY_KEY_REUSED';
        END IF;
        RETURN jsonb_build_object(
            'status', 'replayed',
            'admission_receipt', v_existing.admission_receipt,
            'terminal_receipt', v_existing.terminal_receipt);
    END IF;

    -- Replay returns the historical receipt without validating a fresh contract.
    IF (p_input_canonical_utf8::jsonb->'config') ? 'vars'
        OR (p_input_canonical_utf8::jsonb->'delivery'->'naming') ? 'vars_choice' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='ACQ_ACCEPT_BAD_INPUT';
    END IF;
    IF p_qualification->'config'->>'kind'='owner' THEN
        IF p_input_canonical_utf8::jsonb->'config'->'rules_version' IS DISTINCT FROM jsonb_build_object('state','known','value',(p_qualification->'management'->>'expected_rules_owner_version')::bigint,'reason_code',NULL) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_BAD_INPUT';
        END IF;
    ELSIF p_input_canonical_utf8::jsonb->'config'->'rules_version' IS DISTINCT FROM '{"state":"absent","value":null,"reason_code":null}'::jsonb THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_BAD_INPUT';
    END IF;
    IF (p_qualification->'management'->>'kind'='instance' AND p_qualification->'management'->>'managed_anime_ref' IS DISTINCT FROM p_origin_managed_anime_ref)
        OR p_qualification->'source'->>'kind' IS DISTINCT FROM 'release'
        OR p_qualification->'source'->>'release_ref' IS DISTINCT FROM p_source_release_id THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INPUT_MISMATCH';
    END IF;
    v_config_digest := 'sha256:' || encode(sha256(convert_to(
        public.anime_config_candidate_canonical_json(p_business_snapshot),'UTF8')),'hex');
    IF p_qualification->'config'->>'kind'='owner' THEN
        v_config_digest := p_input_canonical_utf8::jsonb->'config'->>'effective_digest';
    END IF;
    IF jsonb_typeof(p_business_snapshot) IS DISTINCT FROM 'object'
        OR (p_input_canonical_utf8::jsonb)->'config'->>'effective_digest' IS DISTINCT FROM v_config_digest THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INPUT_MISMATCH';
    END IF;
    IF p_qualification->'management'->>'kind'='instance' AND NOT (
        ((p_input_canonical_utf8::jsonb)->'source'->>'kind'='automatic_source' AND p_qualification->'management'->>'subscription_policy'='automatic_active')
        OR ((p_input_canonical_utf8::jsonb)->'source'->>'kind'='subscription_direct' AND p_qualification->'management'->>'subscription_policy'='manual_active_or_stopped')
    ) IS TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    v_now := public.recheck_acquisition_qualification(p_qualification,p_request_hash,v_config_digest,
        'sha256:' || encode(sha256(p_payload),'hex'));
    IF p_input_canonical_utf8::jsonb->'source'->'provenance' IS DISTINCT FROM public.release_source_evidence(p_source_release_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_SOURCE_CHANGED';
    END IF;
    -- Compare the exact native blocks under the qualification's release lock.
    -- Fresh inputs cannot carry the retired duplicate media quality authority.
    IF p_input_canonical_utf8::jsonb->'source'->'tag_evidence' IS DISTINCT FROM
       (SELECT jsonb_build_object('title_tags',r.source_facts->'title_tags','anibt',r.source_facts->'anibt')
        FROM public.releases r WHERE r.id=p_source_release_id)
       OR p_input_canonical_utf8::jsonb->'media' ? 'quality' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_SOURCE_CHANGED';
    END IF;
    IF p_qualification->'config'->>'kind'='owner' THEN
        IF jsonb_typeof(p_default_sections) IS DISTINCT FROM 'object' OR
            (SELECT array_agg(key ORDER BY key COLLATE "C") FROM jsonb_object_keys(p_default_sections) key) IS DISTINCT FROM ARRAY['file_effect','file_rules','naming_rules','parser','release_rules','subgroups']::text[] OR
            EXISTS(SELECT 1 FROM jsonb_each(p_default_sections) WHERE jsonb_typeof(value) IS DISTINCT FROM 'object') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        IF (p_default_sections->'file_effect'->>'operation' IN ('copy','move','hardlink') AND p_default_sections->'file_effect'->>'collision_policy' IN ('fail','versioned','overwrite')) IS NOT TRUE OR
            jsonb_typeof(p_default_sections->'parser'->'episode_patterns') IS DISTINCT FROM 'array' OR
            (CASE WHEN jsonb_typeof(p_default_sections->'parser'->'episode_patterns')='array' THEN jsonb_array_length(p_default_sections->'parser'->'episode_patterns')>64 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_default_sections->'parser'->'episode_patterns') p WHERE jsonb_typeof(p) IS DISTINCT FROM 'string' OR octet_length(p #>> '{}')>512) ELSE TRUE END) OR
            EXISTS(SELECT 1 FROM jsonb_object_keys(p_default_sections->'parser') k WHERE k NOT IN ('episode_patterns')) OR
            jsonb_typeof(p_default_sections->'subgroups'->'entries') IS DISTINCT FROM 'array' OR
            jsonb_typeof(p_default_sections->'subgroups'->'max_rss') IS DISTINCT FROM 'number' OR
            (CASE WHEN p_default_sections->'subgroups'->>'max_rss' ~ '^(0|[1-9][0-9]*)$' THEN (p_default_sections->'subgroups'->>'max_rss')::numeric NOT BETWEEN 0 AND 100 ELSE TRUE END) OR
            COALESCE(p_default_sections->'naming_rules'->>'language','') <> 'js-naming-1' OR
            EXISTS(SELECT 1 FROM (VALUES ('file_rules'),('release_rules')) AS s(key) WHERE NOT public.business_exact_keys(p_default_sections->s.key,ARRAY['must_contain','not_in']) OR jsonb_typeof(p_default_sections->s.key->'must_contain') IS DISTINCT FROM 'string' OR jsonb_typeof(p_default_sections->s.key->'not_in') IS DISTINCT FROM 'string' OR octet_length(p_default_sections->s.key->>'must_contain')>4096 OR octet_length(p_default_sections->s.key->>'not_in')>4096) OR
            NOT public.business_exact_keys(p_default_sections->'file_effect',ARRAY['operation','collision_policy']) OR
            jsonb_typeof(p_default_sections->'file_effect'->'operation') IS DISTINCT FROM 'string' OR
            jsonb_typeof(p_default_sections->'file_effect'->'collision_policy') IS DISTINCT FROM 'string' OR
            jsonb_typeof(p_default_sections->'naming_rules'->'language') IS DISTINCT FROM 'string' OR
            EXISTS(SELECT 1 FROM jsonb_object_keys(p_default_sections->'subgroups') k WHERE k NOT IN ('max_rss','entries','name_keywords','provider_defaults')) OR
            (CASE WHEN jsonb_typeof(p_default_sections->'subgroups'->'entries')='array' THEN EXISTS(
                SELECT 1 FROM jsonb_array_elements(p_default_sections->'subgroups'->'entries') e
                WHERE jsonb_typeof(e) IS DISTINCT FROM 'object' OR e->>'provider' IS NULL OR e->>'provider' NOT IN ('anibt','mikan')
                    OR jsonb_typeof(e->'provider') IS DISTINCT FROM 'string' OR jsonb_typeof(e->'selection_mode') IS DISTINCT FROM 'string'
                    OR jsonb_typeof(e->'score') IS DISTINCT FROM 'number'
                    OR (e ? 'effective_selected' AND jsonb_typeof(e->'effective_selected') IS DISTINCT FROM 'boolean')
                    OR (e ? 'manual_selected' AND jsonb_typeof(e->'manual_selected') NOT IN ('null','boolean'))
                    OR (CASE WHEN jsonb_typeof(e)='object' THEN EXISTS(SELECT 1 FROM jsonb_object_keys(e) k WHERE k NOT IN ('provider','group_ref','display_name','selection_mode','score','effective_selected','manual_selected')) ELSE TRUE END)
                    OR jsonb_typeof(e->'group_ref') IS DISTINCT FROM 'string' OR octet_length(e->>'group_ref')>256 OR btrim(e->>'group_ref')=''
                    OR jsonb_typeof(e->'display_name') IS DISTINCT FROM 'string' OR octet_length(e->>'display_name')>256 OR btrim(e->>'display_name')=''
                    OR e->>'selection_mode' IS NULL OR e->>'selection_mode' NOT IN ('auto','manual')
                    OR (CASE WHEN e->>'score' ~ '^-?(0|[1-9][0-9]*)$' THEN e->>'selection_mode'='auto' AND (e->>'score')::numeric NOT BETWEEN 1 AND 100 ELSE TRUE END)
            ) ELSE TRUE END) OR
            EXISTS(SELECT 1 FROM (VALUES ('name_keywords'),('provider_defaults')) s(key) WHERE p_default_sections->'subgroups' ? s.key AND p_default_sections->'subgroups'->s.key<>'null'::jsonb AND jsonb_typeof(p_default_sections->'subgroups'->s.key) IS DISTINCT FROM 'object') OR
            (CASE WHEN jsonb_typeof(p_default_sections->'subgroups'->'name_keywords')='object' THEN EXISTS(
                SELECT 1 FROM jsonb_each(p_default_sections->'subgroups'->'name_keywords') p WHERE p.key NOT IN ('anibt','mikan') OR
                (CASE WHEN p.value='null'::jsonb THEN FALSE WHEN jsonb_typeof(p.value)='array' THEN EXISTS(
                    SELECT 1 FROM jsonb_array_elements(p.value) r WHERE NOT public.business_exact_keys(r,ARRAY['keyword','score']) OR jsonb_typeof(r->'keyword') IS DISTINCT FROM 'string' OR btrim(r->>'keyword')='' OR octet_length(r->>'keyword')>256 OR
                    jsonb_typeof(r->'score') IS DISTINCT FROM 'number' OR (CASE WHEN r->>'score' ~ '^-?(0|[1-9][0-9]*)$' THEN (r->>'score')::numeric NOT BETWEEN -100 AND 100 ELSE TRUE END)
                ) OR (SELECT count(*)<>count(DISTINCT lower(btrim(r->>'keyword'))) FROM jsonb_array_elements(p.value) r) ELSE TRUE END)
            ) ELSE FALSE END) OR
            (CASE WHEN jsonb_typeof(p_default_sections->'subgroups'->'provider_defaults')='object' THEN EXISTS(
                SELECT 1 FROM jsonb_each(p_default_sections->'subgroups'->'provider_defaults') p WHERE p.key NOT IN ('anibt','mikan') OR
                (CASE WHEN p.value='null'::jsonb THEN FALSE WHEN jsonb_typeof(p.value)='object' THEN NOT public.business_exact_keys(p.value,ARRAY['mode','score','groups']) OR (p.value->>'mode' IN ('blacklist','whitelist')) IS NOT TRUE OR
                    jsonb_typeof(p.value->'mode') IS DISTINCT FROM 'string' OR jsonb_typeof(p.value->'score') IS DISTINCT FROM 'number' OR (CASE WHEN p.value->>'score' ~ '^(0|[1-9][0-9]*)$' THEN (p.value->>'score')::numeric NOT BETWEEN 1 AND 100 ELSE TRUE END) OR
                    (CASE WHEN jsonb_typeof(p.value->'groups')='array' THEN jsonb_array_length(p.value->'groups')>512 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p.value->'groups') g WHERE jsonb_typeof(g) IS DISTINCT FROM 'string' OR btrim(g #>> '{}')='' OR octet_length(g #>> '{}')>256) OR (SELECT count(*)<>count(DISTINCT btrim(g #>> '{}')) FROM jsonb_array_elements(p.value->'groups') g) ELSE TRUE END)
                ELSE TRUE END)
            ) ELSE FALSE END) OR
            NOT public.business_exact_keys(p_default_sections->'naming_rules',ARRAY['language','movie','unknown','preset','subtitle_suffix_template','tv']) OR
            jsonb_typeof(p_default_sections->'naming_rules'->'subtitle_suffix_template') IS DISTINCT FROM 'string' OR
            (p_default_sections->'naming_rules'->'preset'<>'null'::jsonb AND jsonb_typeof(p_default_sections->'naming_rules'->'preset') IS DISTINCT FROM 'string') OR
            EXISTS(SELECT 1 FROM (VALUES ('movie'),('unknown'),('tv')) AS s(key) WHERE NOT public.business_exact_keys(p_default_sections->'naming_rules'->s.key,ARRAY['directory_template','video_filename_template']) OR jsonb_typeof(p_default_sections->'naming_rules'->s.key->'directory_template') IS DISTINCT FROM 'string' OR jsonb_typeof(p_default_sections->'naming_rules'->s.key->'video_filename_template') IS DISTINCT FROM 'string') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
        END IF;
        FOR v_final IN SELECT value FROM jsonb_array_elements(p_input_canonical_utf8::jsonb->'config'->'sections') ORDER BY (value->>'section_key') COLLATE "C" LOOP
            IF (v_final.value->>'config_version')::bigint > 0 THEN
                PERFORM pg_advisory_xact_lock(hashtextextended('bansoko:global-config-section:'||(v_final.value->>'section_key'),0));
                SELECT version,value,schema_version INTO v_global FROM public.global_config_sections WHERE section_name=v_final.value->>'section_key' FOR SHARE;
                IF FOUND THEN
                    IF v_final.value->>'section_key'='naming_rules' THEN
                        v_global.value:=jsonb_build_object('preset',NULL)||v_global.value;
                    END IF;
                    IF v_global.version IS DISTINCT FROM (v_final.value->>'config_version')::bigint OR v_global.value IS DISTINCT FROM v_final.value->'value' OR v_global.schema_version IS DISTINCT FROM v_final.value->>'schema_version' THEN
                        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
                    END IF;
                ELSIF (v_final.value->>'config_version')::bigint<>1 OR p_default_sections->(v_final.value->>'section_key') IS DISTINCT FROM v_final.value->'value' THEN
                    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_CONFIG_CHANGED';
                END IF;
            ELSIF (v_final.value->>'config_version')::bigint < 0 THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
            END IF;
        END LOOP;
    END IF;
    PERFORM public.assert_queued_preparation_witness(p_qualification,(CASE WHEN p_qualification->'config'->>'kind'='owner' THEN jsonb_set(p_business_snapshot,'{sections}',(SELECT jsonb_object_agg(value->>'section_key',value->'value') FROM jsonb_array_elements(p_input_canonical_utf8::jsonb->'config'->'sections'))) ELSE p_business_snapshot END),p_request_hash,
        p_admission_digest,v_config_digest,p_preparation_mode,p_preparation_witness,p_preparation_digest,
        p_witness_max_bytes,p_witness_max_rows);
    -- Canonical Time: trim fractional zeros without changing the DB instant.
    v_now_text := rtrim(rtrim(to_char(v_now AT TIME ZONE 'UTC',
        'YYYY-MM-DD"T"HH24:MI:SS.US'),'0'),'.') || 'Z';
    v_replay_until_text := rtrim(rtrim(to_char((v_now + p_receipt_after_accept_seconds * INTERVAL '1 second') AT TIME ZONE 'UTC',
        'YYYY-MM-DD"T"HH24:MI:SS.US'),'0'),'.') || 'Z';

    v_admission_receipt := jsonb_build_object(
        'schema_version', 1,
        'acquisition_id', p_acquisition_id,
        'admission_kind', 'download_queued',
        'download_task_id', p_download_task_id,
        'config_identity', p_config_identity,
        'admission_digest', p_admission_digest,
        'requirements_digest', NULL,
        'target_digest', NULL,
        'accepted_at', v_now_text,
        'replay_guaranteed_until', v_replay_until_text);

    -- The root stays running: the stage entries T02-T06 and the T14 settle
    -- finish it later. The source release reference is the locked release.
    INSERT INTO public.acquisitions (
        id, execution_branch, entry_kind, access_scope, request_namespace, request_key,
        request_hash, canonical_request, admission_kind, admission_http_status, admission_receipt,
        input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
        display_title, accepted_at, retention_anchor_at,
        detail_retention_seconds, receipt_after_accept_seconds, receipt_after_terminal_seconds,
        detail_until, state, terminal_at, terminal_schema, terminal_receipt,
        record_state, version,
        preparation_mode, preparation_digest, facts_revision, checked_revision,
        settlement_error_count,
        input_canonical_utf8, payload_digest, preparation_witness_canonical_utf8,
        manifest_digest, manifest_item_count, manifest_sealed_at)
    VALUES (
        p_acquisition_id, 'download', p_entry_kind, p_access_scope, p_request_namespace, p_request_key,
        p_request_hash, p_canonical_request, 'download_queued', 202, v_admission_receipt,
        v_input_version, p_admission_digest, p_origin_managed_anime_ref, p_source_release_id,
        p_display_title, v_now, v_now,
        p_detail_retention_seconds, p_receipt_after_accept_seconds, p_receipt_after_terminal_seconds,
        NULL, 'running', NULL, NULL, NULL,
        'full', 1,
        p_preparation_mode, p_preparation_digest, 0, 0,
        0,
        p_input_canonical_utf8, 'sha256:' || encode(sha256(p_payload), 'hex'), p_preparation_witness,
        p_manifest_digest, jsonb_array_length(p_manifest_files), v_now);

    -- The frozen input and its raw payload; the digests are recomputed by the
    -- row CHECKs, so the caller cannot bind the root to other bytes.
    IF (p_input_canonical_utf8::jsonb)->'source'->>'payload_digest'
        IS DISTINCT FROM 'sha256:' || encode(sha256(p_payload), 'hex') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_PAYLOAD_DIGEST';
    END IF;

    IF p_preparation_mode = 'provider_selection_v1' THEN
        v_witness_digest := 'sha256:' || encode(sha256(
            convert_to('acquisition-preparation-witness:v1', 'UTF8') || decode('00', 'hex')
            || convert_to(p_preparation_witness, 'UTF8')), 'hex');
        IF p_preparation_digest <> v_witness_digest THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_PREPARATION_MISMATCH';
        END IF;
    END IF;

    -- The early-sealed manifest, whole.
    INSERT INTO public.acquisition_files (
        acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence)
    SELECT p_acquisition_id, x.file_ref, x.ordinal, x.relative_path, x.exact_size, x.media_kind, x.identity_evidence
    FROM jsonb_to_recordset(p_manifest_files)
    AS x(file_ref TEXT, ordinal INTEGER, relative_path TEXT, exact_size BIGINT, media_kind TEXT, identity_evidence JSONB);
    GET DIAGNOSTICS v_manifest_count = ROW_COUNT;
    IF v_manifest_count <> jsonb_array_length(p_manifest_files) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_MANIFEST_ITEM_COUNT';
    END IF;
    -- The unique task, rooted. The mutual Release/Task keys are deferred, so
    -- the release update below lands before the commit-time check.
    INSERT INTO public.download_tasks (
        id, release_id, acquisition_id, qbittorrent_connection_id,
        frozen_input, frozen_policy, state, idempotency_key, correlation_id, causation_type, causation_id)
    VALUES (
        p_download_task_id, p_source_release_id, p_acquisition_id, p_qbittorrent_connection_id,
        p_task_frozen_input, p_task_frozen_policy, 'queued', p_task_idempotency_key,p_correlation_id,p_causation_type,p_causation_id);

    UPDATE public.releases
    SET download_task_id = p_download_task_id
    WHERE id = p_source_release_id;

    -- The two first work items of the branch: the qB submit on the task and
    -- the settle on the root. The settle item is claimable once a settle
    -- worker exists; the database does not second-guess stage completion.
    INSERT INTO public.work_items (
        id, owner_type, owner_id, acquisition_id, kind,
        idempotency_key, max_attempts, input_snapshot, expected_owner_version)
    VALUES (
        p_submit_work_item_id, 'download_task', p_download_task_id, p_acquisition_id, 'qb_submit',
        p_task_idempotency_key, p_max_attempts, p_submit_input_snapshot, 1);

    INSERT INTO public.work_items (
        id, owner_type, owner_id, acquisition_id, kind,
        idempotency_key, max_attempts, input_snapshot, expected_owner_version)
    VALUES (
        p_settle_work_item_id, 'acquisition', p_acquisition_id, p_acquisition_id, 'acquisition_settle',
        p_settle_idempotency_key, p_max_attempts, '{"schema_version":1}'::jsonb, 1);

    -- Final shape, re-read by root identity rather than trusted from the
    -- values written above.
    SELECT root.state, root.version INTO v_final
    FROM public.acquisitions root
    WHERE root.id = p_acquisition_id;
    IF NOT FOUND OR v_final.state <> 'running' OR v_final.version <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ACQ_ACCEPT_FINAL_STATE';
    END IF;

    RETURN jsonb_build_object(
        'status', 'accepted',
        'admission_receipt', v_admission_receipt,
        'terminal_receipt', NULL);
END;
$_$;
REVOKE ALL ON FUNCTION public.accept_acquisition_queued(text, text, text, text, text, text, jsonb, text, jsonb, jsonb, text, integer, integer, integer, text, text, text, text, bytea, text, text, text, jsonb, text, text, text, text, jsonb, jsonb, text, text, text, jsonb, integer, jsonb, jsonb, integer, integer, text, text, text, jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.accept_acquisition_queued(text, text, text, text, text, text, jsonb, text, jsonb, jsonb, text, integer, integer, integer, text, text, text, text, bytea, text, text, text, jsonb, text, text, text, text, jsonb, jsonb, text, text, text, jsonb, integer, jsonb, jsonb, integer, integer, text, text, text, jsonb) TO bansoko_runtime;

CREATE OR REPLACE FUNCTION public.postprocessing_plan_capture_lock(p_plan text, p_work text, p_token bigint, p_capture jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; current_capture jsonb;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 PERFORM f.id FROM public.artifact_files f WHERE artifact_id=p.artifact_id ORDER BY id COLLATE "C" FOR SHARE;
 SELECT jsonb_build_object('input',i.input,'artifact',to_jsonb(a),
 'manifest',jsonb_build_object('acquisition_id',i.id,'format_version',1,'manifest_digest',i.manifest_digest,'item_count',i.manifest_item_count,'sealed_at',i.manifest_sealed_at),
 'preparation_witness',CASE WHEN i.preparation_witness_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.preparation_witness_canonical_utf8,'witness',i.preparation_witness,'witness_digest',i.preparation_digest) END,
 'manifest_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id),'[]'::jsonb),
 'files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM public.artifact_files f WHERE f.artifact_id=a.id),'[]'::jsonb),
 'requirements',CASE WHEN i.requirements_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'manifest_digest',i.requirements_manifest_digest,'selection_digest',i.requirements_selection_digest,'evidence_digest',i.requirements_evidence_digest,'requirements_digest',i.requirements_digest,'item_count',i.requirements_item_count,'required_count',i.requirements_required_count,'sealed_at',i.requirements_sealed_at) END,
 'requirement_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'[]'::jsonb),
 'target_set',CASE WHEN i.target_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'requirements_digest',i.target_requirements_digest,'target_digest',i.target_digest,'sealed_at',i.target_sealed_at) END,
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL),'[]'::jsonb))
 INTO STRICT current_capture FROM public.artifacts a JOIN public.acquisitions i ON i.id=a.acquisition_id
 WHERE a.id=p.artifact_id;
 IF current_capture IS DISTINCT FROM (p_capture-'prepared_bindings'-'prepared_handoffs') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_CAPTURE_CHANGED'; END IF;
END $$;

CREATE OR REPLACE FUNCTION public.postprocessing_plan_commit(p_plan text, p_work text, p_token bigint, p_input_digest text, p_input jsonb, p_requirements text, p_targets text, p_declarations text, p_operations jsonb, p_capture jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE; input jsonb;
 requirements jsonb; targets jsonb; declarations jsonb; rd text; td text; pd text; total integer; required integer;
 entry jsonb; op jsonb; inherited jsonb; new_work text; v bigint; now_at timestamptz; limits jsonb;
 selection_bytes text; evidence_bytes text; sd text; ed text; input_bytes text;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 PERFORM public.postprocessing_plan_capture_lock(p_plan,p_work,p_token,p_capture);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT q.input INTO STRICT input FROM public.acquisitions q WHERE q.id=p.acquisition_id;
 IF w.recovery_blocked OR w.cancel_requested OR p.input_digest IS DISTINCT FROM p_input_digest OR input IS DISTINCT FROM p_input THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF p_requirements IS NULL OR octet_length(p_requirements)>4194304 OR p_targets IS NULL OR octet_length(p_targets)>16777216
 OR p_declarations IS NULL OR octet_length(p_declarations)>16777216 OR p_operations IS NULL OR jsonb_typeof(p_operations)<>'array' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_PREPARED_SHAPE'; END IF;
 IF public.plan_canonical_valid(p_requirements) IS NOT TRUE OR public.plan_canonical_valid(p_targets) IS NOT TRUE
 OR public.plan_canonical_valid(p_declarations) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_CANONICAL_INVALID'; END IF;
 requirements:=p_requirements::jsonb;targets:=p_targets::jsonb;declarations:=p_declarations::jsonb;
 IF jsonb_typeof(requirements)<>'array' OR jsonb_typeof(targets)<>'array' OR jsonb_typeof(declarations)<>'object'
 OR jsonb_typeof(declarations->'items')<>'array' OR jsonb_typeof(declarations->'dependencies')<>'array'
 OR jsonb_array_length(requirements)>10000 OR jsonb_array_length(targets)>10000
 OR jsonb_array_length(declarations->'items')>10000 OR jsonb_array_length(declarations->'dependencies')>20000 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_PREPARED_SHAPE'; END IF;
 IF EXISTS(SELECT 1 FROM jsonb_object_keys(declarations) k WHERE k NOT IN ('items','dependencies'))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'items') i WHERE jsonb_typeof(i)<>'object'
 OR NOT (i ?& ARRAY['file_ref','kind','target_file_ref','evidence','reason_code','diagnostic'])
 OR EXISTS(SELECT 1 FROM jsonb_object_keys(i) k WHERE k NOT IN ('file_ref','kind','target_file_ref','evidence','reason_code','diagnostic')))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'dependencies') d WHERE jsonb_typeof(d)<>'object'
 OR NOT (d ?& ARRAY['file_ref','depends_on_ref','dependency_kind'])
 OR EXISTS(SELECT 1 FROM jsonb_object_keys(d) k WHERE k NOT IN ('file_ref','depends_on_ref','dependency_kind'))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_PREPARED_SHAPE'; END IF;
 IF EXISTS(SELECT 1 FROM (SELECT x->>'file_ref' AS ref,lag(x->>'file_ref') OVER(ORDER BY ordinal) AS previous FROM jsonb_array_elements(requirements) WITH ORDINALITY AS e(x,ordinal)) s WHERE ref IS NULL OR (previous IS NOT NULL AND (ref COLLATE "C")<=(previous COLLATE "C")))
 OR EXISTS(SELECT 1 FROM (SELECT x->>'file_ref' AS ref,lag(x->>'file_ref') OVER(ORDER BY ordinal) AS previous FROM jsonb_array_elements(targets) WITH ORDINALITY AS e(x,ordinal)) s WHERE ref IS NULL OR (previous IS NOT NULL AND (ref COLLATE "C")<=(previous COLLATE "C")))
 OR EXISTS(SELECT 1 FROM (SELECT x->>'file_ref' AS ref,lag(x->>'file_ref') OVER(ORDER BY ordinal) AS previous FROM jsonb_array_elements(declarations->'items') WITH ORDINALITY AS e(x,ordinal)) s WHERE ref IS NULL OR (previous IS NOT NULL AND (ref COLLATE "C")<=(previous COLLATE "C"))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_SET_ORDER_INVALID'; END IF;
 SELECT input_canonical_utf8 INTO STRICT input_bytes FROM public.acquisitions WHERE id=p.acquisition_id;
 selection_bytes:=public.plan_selection_bytes(input_bytes);evidence_bytes:=public.plan_evidence_bytes(p_requirements);
 IF public.plan_canonical_valid(selection_bytes) IS NOT TRUE OR public.plan_canonical_valid(evidence_bytes) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_REQUIREMENTS_PROJECTION'; END IF;
 sd:='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(selection_bytes,'UTF8')),'hex');
 ed:='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(evidence_bytes,'UTF8')),'hex');
 IF EXISTS(SELECT 1 FROM json_array_elements(p_targets::json) t WHERE t::jsonb->>'spec_digest' IS DISTINCT FROM
 'sha256:'||encode(sha256(convert_to('acquisition-targets:v1','UTF8')||decode('00','hex')||convert_to(public.plan_target_spec_bytes(t::text),'UTF8')),'hex')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_TARGET_SPEC_DIGEST'; END IF;
 rd:='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(p_requirements,'UTF8')),'hex');
 td:='sha256:'||encode(sha256(convert_to('acquisition-targets:v1','UTF8')||decode('00','hex')||convert_to(p_targets,'UTF8')),'hex');
 pd:='sha256:'||encode(sha256(convert_to('acquisition-plan:v1','UTF8')||decode('00','hex')||convert_to(p_declarations,'UTF8')),'hex');
 total:=jsonb_array_length(requirements);
 SELECT count(*) INTO required FROM jsonb_array_elements(requirements) x WHERE x->>'requirement'='required';
 IF (SELECT count(DISTINCT x->>'file_ref') FROM jsonb_array_elements(requirements) x)<>total
 OR EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(requirements) x WHERE x->>'file_ref'=m.file_ref))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(requirements) x WHERE NOT EXISTS(SELECT 1 FROM public.acquisition_files m
  WHERE m.acquisition_id=p.acquisition_id AND m.file_ref=x->>'file_ref')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_REQUIREMENTS_SET'; END IF;
 IF EXISTS(SELECT 1 FROM public.acquisitions WHERE id=p.acquisition_id AND requirements_digest IS NOT NULL) THEN
 IF rd IS DISTINCT FROM (SELECT requirements_digest FROM public.acquisitions WHERE id=p.acquisition_id)
 OR EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND (h.requirements_selection_digest IS DISTINCT FROM sd OR h.requirements_evidence_digest IS DISTINCT FROM ed))
 OR NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_manifest_digest=h.manifest_digest AND h.requirements_item_count=total AND h.requirements_required_count=required)
 OR (SELECT count(*) FROM public.acquisition_files WHERE acquisition_id=p.acquisition_id AND requirement IS NOT NULL)<>total
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM jsonb_array_elements(requirements) x WHERE x=jsonb_build_object('file_ref',f.file_ref,'requirement',f.requirement,'reason_code',f.requirement_reason_code,'selection_evidence',f.selection_evidence))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_EARLY_REQUIREMENTS_BINDING'; END IF;
 ELSE
 UPDATE public.acquisition_files f SET requirement=x.requirement,requirement_reason_code=x.reason_code,selection_evidence=x.selection_evidence
 FROM jsonb_to_recordset(requirements) x(file_ref text,requirement text,reason_code text,selection_evidence jsonb)
 WHERE f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref;
 UPDATE public.acquisitions SET requirements_manifest_digest=manifest_digest,requirements_selection_digest=sd,
 requirements_evidence_digest=ed,requirements_digest=rd,requirements_item_count=total,requirements_required_count=required,
 requirements_sealed_at=CURRENT_TIMESTAMP WHERE id=p.acquisition_id;
 END IF;
 IF required=0 THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,'NO_REQUIRED_FILES','no required files');
 RETURN jsonb_build_object('state','failed','reason_code','NO_REQUIRED_FILES'); END IF;
 IF EXISTS(SELECT 1 FROM public.acquisitions WHERE id=p.acquisition_id AND target_digest IS NOT NULL) THEN
 IF td IS DISTINCT FROM (SELECT target_digest FROM public.acquisitions WHERE id=p.acquisition_id)
 OR NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest=rd)
 OR (SELECT count(*) FROM public.acquisition_files t WHERE t.acquisition_id=p.acquisition_id AND t.spec_digest IS NOT NULL)<>jsonb_array_length(targets)
 OR EXISTS(SELECT 1 FROM public.acquisition_files t WHERE t.acquisition_id=p.acquisition_id AND t.spec_digest IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM jsonb_array_elements(targets) x WHERE x->>'file_ref'=t.file_ref AND x->>'spec_digest'=t.spec_digest
 AND x->>'intent'=t.intent AND x->>'operation'=t.operation AND x->>'canonical_path'=t.canonical_path AND x->>'collision'=t.collision
 AND x->'source_cleanup'=t.target_source_cleanup AND x->'render_snapshot'=t.render_snapshot))
 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_EARLY_TARGET_BINDING'; END IF;
 ELSE
 UPDATE public.acquisition_files f SET spec_digest=x.spec_digest,intent=x.intent,operation=x.operation,canonical_path=x.canonical_path,
 collision=x.collision,target_source_cleanup=x.source_cleanup,render_snapshot=x.render_snapshot
 FROM jsonb_to_recordset(targets) x(file_ref text,spec_digest text,intent text,operation text,canonical_path text,collision text,source_cleanup jsonb,render_snapshot jsonb)
 WHERE f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(targets) x WHERE NOT EXISTS(
  SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.file_ref=x->>'file_ref')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_EARLY_TARGET_BINDING'; END IF;
 UPDATE public.acquisitions SET target_requirements_digest=rd,target_digest=td,target_sealed_at=CURRENT_TIMESTAMP WHERE id=p.acquisition_id;
 END IF;
 IF jsonb_array_length(declarations->'items')<>required OR (SELECT count(DISTINCT x->>'file_ref') FROM jsonb_array_elements(declarations->'items') x)<>required
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(requirements) r WHERE r->>'requirement'='required'
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'items') i WHERE i->>'file_ref'=r->>'file_ref')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ITEMS_SET'; END IF;
 IF jsonb_array_length(p_operations)<>(SELECT count(*) FROM jsonb_array_elements(declarations->'items') i WHERE i->>'kind'='execute')
 OR (SELECT count(DISTINCT o->>'id') FROM jsonb_array_elements(p_operations) o)<>jsonb_array_length(p_operations)
 OR (SELECT count(DISTINCT o->>'file_ref') FROM jsonb_array_elements(p_operations) o)<>jsonb_array_length(p_operations)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_operations) o WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'items') i WHERE i->>'kind'='execute' AND i->>'file_ref'=o->>'file_ref')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_OPERATIONS_SET'; END IF;
 -- Prepared decisions cover exactly required subtitles without a captured snapshot.
 IF jsonb_typeof(COALESCE(p_capture->'prepared_bindings','[]'::jsonb)) IS DISTINCT FROM 'array'
 OR jsonb_array_length(COALESCE(p_capture->'prepared_bindings','[]'::jsonb))>10000
 OR (SELECT count(DISTINCT b->>'file') FROM jsonb_array_elements(COALESCE(p_capture->'prepared_bindings','[]'::jsonb)) b)<>jsonb_array_length(COALESCE(p_capture->'prepared_bindings','[]'::jsonb))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_capture->'prepared_bindings') b WHERE jsonb_typeof(b) IS DISTINCT FROM 'object'
 OR NOT EXISTS(SELECT 1 FROM public.artifact_files f JOIN jsonb_array_elements(requirements) q ON q->>'file_ref'=f.file_ref
 WHERE f.id=b->>'file' AND f.artifact_id=p.artifact_id AND f.acquisition_id=p.acquisition_id AND f.media_kind='subtitle' AND q->>'requirement'='required')
 OR EXISTS(SELECT 1 FROM jsonb_object_keys(b) k WHERE k NOT IN ('file','decision','state','bound_ref','locked')))
 OR EXISTS(SELECT 1 FROM public.artifact_files f JOIN jsonb_array_elements(requirements) q ON q->>'file_ref'=f.file_ref
 WHERE f.artifact_id=p.artifact_id AND f.media_kind='subtitle' AND q->>'requirement'='required' AND f.subtitle_binding_snapshot IS NULL
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_capture->'prepared_bindings') b WHERE b->>'file'=f.id AND b ? 'decision'))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_capture->'prepared_bindings') b JOIN public.artifact_files f ON f.id=b->>'file'
 WHERE b ? 'decision' AND (f.subtitle_binding_snapshot IS NOT NULL
 OR jsonb_typeof(b->'decision') IS DISTINCT FROM 'object' OR jsonb_typeof(b->'decision'->'diagnostics') IS DISTINCT FROM 'array'
 OR NOT (b ?& ARRAY['file','decision','state','bound_ref'])
 OR (b->>'state' IN ('bound','missing','ambiguous')) IS NOT TRUE
 OR EXISTS(SELECT 1 FROM jsonb_object_keys(b->'decision') k WHERE k NOT IN ('version','method','confidence','diagnostics'))
 OR (b->>'state'='bound' AND (jsonb_typeof(b->'decision'->'confidence') IS DISTINCT FROM 'number'
 OR (b->'decision'->>'confidence') !~ '^(0|1)([.][0-9]+)?$'
 OR (b->'decision'->>'confidence')::numeric NOT BETWEEN 0 AND 1
 OR (b->'decision'->>'method' IN ('basename_match','episode_match','unique_video')) IS NOT TRUE))
 OR (b->>'state'='bound' AND NOT EXISTS(SELECT 1 FROM public.artifact_files v JOIN jsonb_array_elements(requirements) q ON q->>'file_ref'=v.file_ref
 WHERE v.artifact_id=p.artifact_id AND v.media_kind='video' AND v.file_ref=b->>'bound_ref' AND q->>'requirement'='required'))
 OR (b->>'state'<>'bound' AND b->'bound_ref' IS DISTINCT FROM 'null'::jsonb))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_BINDING_SET'; END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(COALESCE(p_capture->'prepared_bindings','[]'::jsonb)) LOOP
 IF entry ? 'decision' THEN
 IF entry->>'state' NOT IN ('bound','missing','ambiguous') OR entry->'decision'->>'version' IS DISTINCT FROM 'subtitle-binding-1'
 OR NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.id=entry->>'file' AND f.artifact_id=p.artifact_id AND f.media_kind='subtitle') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ARTIFACT_BINDING'; END IF;
 UPDATE public.artifact_files SET subtitle_binding_state=entry->>'state',bound_video_file_ref=entry->>'bound_ref',subtitle_binding_snapshot=entry->'decision'
 WHERE id=entry->>'file' AND subtitle_binding_snapshot IS NULL;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ARTIFACT_BINDING_CHANGED'; END IF;
 END IF;
 END LOOP;
 now_at:=clock_timestamp();limits:=input->'limits'->'file';
 FOR entry IN SELECT i.value FROM jsonb_array_elements(declarations->'items') i
 ORDER BY CASE WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'dependencies') d WHERE d->>'file_ref'=i.value->>'file_ref') THEN 1 ELSE 0 END,
 i.value->>'file_ref' COLLATE "C" LOOP
 IF entry->>'kind'='execute' THEN
 SELECT value INTO STRICT op FROM jsonb_array_elements(p_operations) x WHERE x->>'file_ref'=entry->>'file_ref';
 IF op->>'artifact' IS DISTINCT FROM p.artifact_id OR (op->>'version')::bigint IS DISTINCT FROM p.artifact_version
 OR NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.id=op->>'file' AND f.artifact_id=p.artifact_id
 AND f.acquisition_id=p.acquisition_id AND f.file_ref=entry->>'file_ref' AND f.source_path=op->>'source'
 AND f.fingerprint=op->'fingerprint' AND f.size_bytes=(op->>'size')::bigint)
 OR NOT EXISTS(SELECT 1 FROM public.acquisition_files t WHERE t.acquisition_id=p.acquisition_id AND t.file_ref=entry->>'file_ref'
 AND t.canonical_path=op->>'target' AND t.operation=op->>'operation' AND t.intent=op->>'intent' AND t.collision=op->>'collision'
 AND t.render_snapshot=public.subtitle_business_render(op->'render')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_OPERATION_BINDING'; END IF;
 IF NULLIF(op->>'bound_video','') IS NOT NULL THEN
 IF op->'binding_snapshot'->>'version' IS DISTINCT FROM 'subtitle-binding-1'
 OR op->'binding_snapshot'->>'method' IS DISTINCT FROM op->'render'->'binding'->>'method'
 OR op->'binding_snapshot'->'confidence' IS DISTINCT FROM op->'render'->'binding'->'confidence'
 OR op->'binding_snapshot'->'locked' IS DISTINCT FROM jsonb_build_object(
 'video_file_ref',op->'render'->'binding'->>'bound_video_file_ref','video_operation_id',op->>'bound_video',
 'directory',op->'render'->'binding'->>'locked_directory','basename',op->'render'->'binding'->>'locked_basename','extension',op->'render'->'binding'->>'locked_extension') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ARTIFACT_BINDING'; END IF;
 IF EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.id=op->>'file' AND f.subtitle_binding_snapshot IS NOT NULL AND (
 (f.subtitle_binding_snapshot-'locked') IS DISTINCT FROM ((op->'binding_snapshot')-'locked')
 OR f.bound_video_file_ref IS DISTINCT FROM op->'render'->'binding'->>'bound_video_file_ref'
 OR (f.subtitle_binding_snapshot ? 'locked' AND f.subtitle_binding_snapshot->'locked' IS DISTINCT FROM op->'binding_snapshot'->'locked'))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ARTIFACT_BINDING_CHANGED'; END IF;
 UPDATE public.artifact_files SET subtitle_binding_state='bound',bound_video_file_ref=op->'render'->'binding'->>'bound_video_file_ref',subtitle_binding_snapshot=op->'binding_snapshot' WHERE id=op->>'file';
 END IF;
 INSERT INTO public.file_operations(id,artifact_id,artifact_version,artifact_file_id,acquisition_id,file_ref,postprocessing_plan_id,
 operation,source_path,target_path,source_fingerprint,expected_size_bytes,intent,collision_policy,frozen_render_snapshot,frozen_config_snapshot,
 overwrite_execution_snapshot,state,idempotency_key,correlation_id,causation_type,causation_id,bound_video_operation_id,
 file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds)
 VALUES(op->>'id',p.artifact_id,p.artifact_version,op->>'file',p.acquisition_id,entry->>'file_ref',p.id,
 op->>'operation',op->>'source',op->>'target',op->'fingerprint',(op->>'size')::bigint,op->>'intent',op->>'collision',op->'render',op->'config',
 NULLIF(op->'overwrite','null'::jsonb),'pending',op->>'key',p.acquisition_id,'postprocessing_plan',p.id,NULLIF(op->>'bound_video',''),
 now_at,now_at+make_interval(secs=>(limits->>'wall_seconds')::double precision),(limits->>'max_attempts')::integer,(limits->>'attempt_timeout_seconds')::integer);
 END IF;
 END LOOP;
 UPDATE public.acquisition_files f SET plan_kind=x.kind,plan_target_file_ref=x.target_file_ref,
  plan_operation_ref=CASE WHEN x.kind='execute' THEN (SELECT o->>'id' FROM jsonb_array_elements(p_operations) o WHERE o->>'file_ref'=x.file_ref) ELSE NULL END,
  plan_evidence=x.evidence,plan_reason_code=x.reason_code,plan_diagnostic=x.diagnostic
 FROM jsonb_to_recordset(declarations->'items') x(file_ref text,kind text,target_file_ref text,evidence jsonb,reason_code text,diagnostic jsonb)
 WHERE f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref;
 IF EXISTS(SELECT 1 FROM jsonb_to_recordset(declarations->'items') x(file_ref text)
  WHERE NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ITEMS_SET'; END IF;
 -- The subtitle->video edge is file_operations.bound_video_operation_id; the
 -- declarations must name exactly the edges the operations carry.
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'dependencies') d
  JOIN jsonb_array_elements(declarations->'items') ci ON ci->>'file_ref'=d->>'file_ref' AND ci->>'kind'='execute'
  LEFT JOIN jsonb_array_elements(declarations->'items') pi ON pi->>'file_ref'=d->>'depends_on_ref'
  LEFT JOIN jsonb_array_elements(p_operations) o ON o->>'file_ref'=d->>'file_ref'
  WHERE pi IS NULL OR pi->>'kind' NOT IN ('execute','adopt','retain') OR o IS NULL
   OR (pi->>'kind'='execute' AND o->>'bound_video' IS DISTINCT FROM (SELECT po->>'id' FROM jsonb_array_elements(p_operations) po WHERE po->>'file_ref'=d->>'depends_on_ref'))
   OR (pi->>'kind' IN ('adopt','retain') AND NULLIF(o->>'bound_video','') IS NOT NULL)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_DEPENDENCY_OPERATION_BINDING'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id AND o.bound_video_operation_id IS NOT NULL
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(declarations->'dependencies') d WHERE d->>'file_ref'=o.file_ref)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_DEPENDENCY_OPERATION_BINDING'; END IF;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
 delivery_source_cleanup='not_required',delivery_reason_code=plan_reason_code,evidence_digest=pd,
 receipt=jsonb_build_object('schema_version',1,'reason_code',plan_reason_code,'diagnostic',plan_diagnostic),
 delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=p.acquisition_id AND plan_kind IN ('failed','dependency_failed');
 IF EXISTS(SELECT 1 FROM public.acquisition_files i JOIN public.artifact_files f ON f.artifact_id=p.artifact_id AND f.file_ref=i.file_ref
 WHERE i.acquisition_id=p.acquisition_id AND i.plan_kind IN ('adopt','retain')
 AND (input->'delivery'->'source_cleanup'->>'mode' IS DISTINCT FROM 'none' OR input->'delivery'->>'operation'='move'
 OR (input->'delivery'->>'intent'='replace' AND (i.plan_kind<>'adopt' OR f.media_kind NOT IN ('subtitle','other')
  OR i.canonical_path IN (SELECT r.value->'expected_object'->'location'->>'canonical_path' FROM public.delivery_replacements(input->'delivery') r(value))))
 OR (i.plan_kind='retain') IS DISTINCT FROM (input->'delivery'->>'operation'='noop')
 OR public.observed_satisfaction_valid(i.plan_evidence,p.id,p.acquisition_id,p.artifact_id,p.artifact_version,i.file_ref,p.input_digest,i.spec_digest,i.canonical_path,f.size_bytes,f.fingerprint->>'value') IS NOT TRUE)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_OBSERVED_SATISFACTION_INVALID'; END IF;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='succeeded',
 completion_mode=CASE WHEN plan_kind='adopt' THEN 'adopted' ELSE 'retain' END,effect_class='no_external_effect',
 final_object=plan_evidence->'final_object',delivery_source_cleanup='not_required',
 evidence_digest=plan_evidence->>'digest',receipt=plan_evidence,delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=p.acquisition_id AND plan_kind IN ('adopt','retain');
 -- Adopt/retain proof persistence is filled by the same T05 fixed set; no
 -- execute Operation is fabricated for those branches.
 inherited:=CASE WHEN w.input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',w.input_snapshot->'one_shot') ELSE '{}'::jsonb END;
 FOR entry IN SELECT to_jsonb(f) FROM public.acquisition_files f JOIN public.file_operations o ON o.id=f.plan_operation_ref
 WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind='execute' AND o.bound_video_operation_id IS NULL LOOP
 SELECT to_jsonb(o) INTO STRICT op FROM public.file_operations o WHERE o.id=entry->>'plan_operation_ref';
 SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',entry->>'plan_operation_ref','file_execute',
 op->>'idempotency_key',(limits->>'max_attempts')::integer,inherited||jsonb_build_object('operation_id',op->>'id','artifact_id',p.artifact_id,'artifact_file_id',op->>'artifact_file_id')||CASE WHEN op->>'collision_policy'='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END,1::bigint,0::double precision,7::smallint) INTO new_work;
 IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_WORK_CONFLICT'; END IF;
 END LOOP;
 PERFORM public.download_ensure_settlement(p.acquisition_id);
 UPDATE public.postprocessing_plans SET state='succeeded',outcome_code='plan_completed',outcome_reason='complete planning declarations committed',
 sealed_at=now_at,requirements_digest=rd,target_digest=td,plan_digest=pd,canonical_requirements=p_requirements,canonical_targets=p_targets,canonical_plan=p_declarations,committed_operations=p_operations,committed_bindings=COALESCE(p_capture->'prepared_bindings','[]'::jsonb),
 committed_binding_sources=COALESCE((SELECT jsonb_agg(jsonb_build_object('file',f->>'id','state',f->'subtitle_binding_state','bound_ref',f->'bound_video_file_ref','snapshot',f->'subtitle_binding_snapshot') ORDER BY f->>'id' COLLATE "C") FROM jsonb_array_elements(p_capture->'files') f JOIN jsonb_array_elements(requirements) q ON q->>'file_ref'=f->>'file_ref' WHERE f->>'media_kind'='subtitle' AND q->>'requirement'='required'),'[]'::jsonb),
 planning_requirements_status='sealed',planning_diagnostic=jsonb_build_object('schema_version',1,'requirements_status','sealed'),
 planning_facts_revision=(SELECT facts_revision FROM public.acquisitions WHERE id=p.acquisition_id),version=version+1,updated_at=CURRENT_TIMESTAMP
 WHERE id=p.id RETURNING version INTO v;
 PERFORM public.work_item_finish('succeeded',v,p_work);
 RETURN jsonb_build_object('state','succeeded','plan_version',v);
END $_$;

CREATE OR REPLACE FUNCTION public.postprocessing_plan_commit_state(p_plan text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_outcomes%ROWTYPE;
 r public.acquisitions%ROWTYPE; a public.artifacts%ROWTYPE; items jsonb;
BEGIN
 SELECT * INTO p FROM public.postprocessing_plans WHERE id=p_plan;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO w FROM public.work_item_outcomes WHERE id=p_work;
 IF NOT FOUND OR w.owner_type IS DISTINCT FROM 'postprocessing_plan' OR w.owner_id IS DISTINCT FROM p.id
 OR w.kind IS DISTINCT FROM 'postprocess_plan'
 OR w.acquisition_id IS DISTINCT FROM p.acquisition_id OR w.fencing_token IS DISTINCT FROM p_token
 OR w.expected_owner_version IS DISTINCT FROM p.version OR w.state IS DISTINCT FROM p.state THEN RETURN NULL; END IF;
 IF w.retention_until IS DISTINCT FROM w.terminal_at+(w.retention_days*interval '1 day') THEN RETURN NULL; END IF;
 IF EXISTS(SELECT 1 FROM public.work_items other WHERE other.owner_type='postprocessing_plan' AND other.owner_id=p.id AND other.kind='postprocess_plan' AND other.state IN ('available','leased')) THEN RETURN NULL; END IF;
 IF public.postprocessing_plan_child_diagnostic(p.id) IS NOT NULL THEN RETURN NULL; END IF;
 SELECT * INTO r FROM public.acquisitions WHERE id=p.acquisition_id;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO a FROM public.artifacts WHERE id=p.artifact_id;
 IF NOT FOUND THEN RETURN NULL; END IF;
 IF a.acquisition_id IS DISTINCT FROM p.acquisition_id OR a.version IS DISTINCT FROM p.artifact_version OR a.state IS DISTINCT FROM 'verified'
 OR r.record_state IS DISTINCT FROM 'full' OR p.planning_facts_revision IS NULL OR r.facts_revision<p.planning_facts_revision THEN RETURN NULL; END IF;
 IF NOT (r.checked_revision>=p.planning_facts_revision OR EXISTS(SELECT 1 FROM public.work_items s WHERE s.acquisition_id=p.acquisition_id
 AND s.owner_type='acquisition' AND s.owner_id=p.acquisition_id AND s.kind='acquisition_settle' AND s.input_snapshot='{"schema_version":1}'::jsonb AND s.state IN ('available','leased'))) THEN RETURN NULL; END IF;
 IF p.one_shot_id IS NOT NULL AND (w.input_snapshot->'one_shot'->>'marker_id' IS DISTINCT FROM p.one_shot_id
  OR EXISTS(SELECT 1 FROM public.one_shot_acquisitions m WHERE m.id=p.one_shot_id
   AND (m.admission_epoch IS DISTINCT FROM (w.input_snapshot->'one_shot'->>'admission_epoch')::bigint
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(m.active_effect_leases) e WHERE e->>'work_item_id'=w.id)))
  OR NOT EXISTS(SELECT 1 FROM public.one_shot_acquisitions m WHERE m.id=p.one_shot_id)) THEN RETURN NULL; END IF;
 IF p.state='failed' THEN
  IF p.planning_diagnostic IS NULL OR p.planning_diagnostic->>'reason_code' IS DISTINCT FROM w.last_error_code
  OR p.outcome_code IS DISTINCT FROM (CASE WHEN w.last_error_code='PLAN_CANCELLED' THEN 'PLAN_CANCELLED' ELSE 'plan_failed' END)
  OR p.outcome_reason IS DISTINCT FROM w.last_error_reason OR p.sealed_at IS NOT NULL
  OR p.planning_requirements_status NOT IN ('sealed','not_reached','incomplete')
  OR EXISTS(SELECT 1 FROM public.file_operations o WHERE o.artifact_id=p.artifact_id)
  OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IS NOT NULL) THEN RETURN NULL; END IF;
  IF p.planning_requirements_status='sealed' AND (
   NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest IS NOT NULL)
   OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required'
    AND (f.outcome IS DISTINCT FROM 'failed' OR f.completion_mode IS NOT NULL OR f.final_object IS NOT NULL OR f.operation_id IS NOT NULL
     OR f.effect_class IS DISTINCT FROM 'no_external_effect' OR f.delivery_source_cleanup IS DISTINCT FROM 'not_required'
     OR f.delivery_reason_code IS DISTINCT FROM w.last_error_code
     OR f.receipt IS DISTINCT FROM jsonb_build_object('schema_version',1,'reason_code',w.last_error_code,'stage','planning')
     OR f.evidence_digest IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('plan-failure:v1','UTF8')||decode('00','hex')||convert_to(p.input_digest||':'||f.file_ref||':'||w.last_error_code,'UTF8')),'hex')))) THEN RETURN NULL; END IF;
  IF p.planning_requirements_status<>'sealed' AND EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest IS NOT NULL) THEN RETURN NULL; END IF;
  RETURN jsonb_build_object('state','failed','reason_code',w.last_error_code);
 END IF;
 IF p.state IS DISTINCT FROM 'succeeded' OR p.sealed_at IS NULL OR p.canonical_requirements IS NULL OR p.canonical_targets IS NULL OR p.canonical_plan IS NULL THEN RETURN NULL; END IF;
 IF p.requirements_digest IS DISTINCT FROM public.file_proof_digest('acquisition-requirements:v1',p.canonical_requirements)
 OR p.target_digest IS DISTINCT FROM public.file_proof_digest('acquisition-targets:v1',p.canonical_targets)
 OR p.plan_digest IS DISTINCT FROM public.file_proof_digest('acquisition-plan:v1',p.canonical_plan)
 OR public.plan_canonical_valid(p.canonical_requirements) IS NOT TRUE OR public.plan_canonical_valid(p.canonical_targets) IS NOT TRUE OR public.plan_canonical_valid(p.canonical_plan) IS NOT TRUE THEN RETURN NULL; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisitions i WHERE i.id=p.acquisition_id AND i.admission_digest=p.input_digest
  AND public.plan_canonical_valid(i.input_canonical_utf8) IS TRUE)
 OR EXISTS(SELECT 1 FROM json_array_elements(p.canonical_targets::json) t WHERE t::jsonb->>'spec_digest' IS DISTINCT FROM
  'sha256:'||encode(sha256(convert_to('acquisition-targets:v1','UTF8')||decode('00','hex')||convert_to(public.plan_target_spec_bytes(t::text),'UTF8')),'hex')) THEN RETURN NULL; END IF;
 items:=p.canonical_plan::jsonb->'items';
 IF NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest=p.requirements_digest
  AND h.target_digest=p.target_digest AND h.requirements_required_count=jsonb_array_length(items) AND h.requirements_item_count=h.manifest_item_count
  AND h.requirements_manifest_digest=h.manifest_digest
  AND h.requirements_selection_digest='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(public.plan_selection_bytes((SELECT input_canonical_utf8 FROM public.acquisitions WHERE id=p.acquisition_id)),'UTF8')),'hex')
  AND h.requirements_evidence_digest='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(public.plan_evidence_bytes(p.canonical_requirements),'UTF8')),'hex'))
 OR (SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NULL)<>0
 OR (SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required')<>jsonb_array_length(items)
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.canonical_requirements::jsonb) x WHERE x=jsonb_build_object('file_ref',f.file_ref,'requirement',f.requirement,'reason_code',f.requirement_reason_code,'selection_evidence',f.selection_evidence)))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.canonical_targets::jsonb) x WHERE x->>'file_ref'=f.file_ref AND x->>'spec_digest'=f.spec_digest AND x->>'intent'=f.intent
   AND x->>'operation'=f.operation AND x->>'canonical_path'=f.canonical_path AND x->>'collision'=f.collision
   AND x->'source_cleanup'=f.target_source_cleanup AND x->'render_snapshot'=f.render_snapshot))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(items) x WHERE x=jsonb_build_object('file_ref',f.file_ref,'kind',f.plan_kind,'target_file_ref',f.plan_target_file_ref,'evidence',f.plan_evidence,'reason_code',f.plan_reason_code,'diagnostic',f.plan_diagnostic)))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required' AND f.plan_kind IS NULL)
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IN ('failed','dependency_failed')
  AND (f.outcome<>'failed' OR f.completion_mode IS NOT NULL OR f.final_object IS NOT NULL OR f.operation_id IS NOT NULL
   OR f.delivery_reason_code IS DISTINCT FROM f.plan_reason_code OR f.effect_class<>'no_external_effect' OR f.delivery_source_cleanup<>'not_required'
   OR f.evidence_digest IS DISTINCT FROM p.plan_digest
   OR f.receipt IS DISTINCT FROM jsonb_build_object('schema_version',1,'reason_code',f.plan_reason_code,'diagnostic',f.plan_diagnostic)))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IN ('adopt','retain')
  AND (f.outcome<>'succeeded' OR f.completion_mode IS DISTINCT FROM (CASE WHEN f.plan_kind='adopt' THEN 'adopted' ELSE 'retain' END)
   OR f.final_object IS DISTINCT FROM f.plan_evidence->'final_object' OR f.evidence_digest IS DISTINCT FROM f.plan_evidence->>'digest'
   OR f.receipt IS DISTINCT FROM f.plan_evidence OR f.effect_class<>'no_external_effect' OR f.delivery_source_cleanup<>'not_required' OR f.operation_id IS NOT NULL))
 OR EXISTS(SELECT 1 FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_operation_ref=o.id AND f.plan_kind='execute'))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f JOIN public.file_operations o ON o.id=f.plan_operation_ref
  LEFT JOIN public.file_operations parent ON parent.id=o.bound_video_operation_id
  LEFT JOIN public.artifact_files cf ON cf.artifact_id=p.artifact_id AND cf.file_ref=f.file_ref
  LEFT JOIN public.artifact_files pf ON pf.artifact_id=p.artifact_id AND pf.file_ref=parent.file_ref
  WHERE f.acquisition_id=p.acquisition_id AND o.bound_video_operation_id IS NOT NULL
   AND (parent.id IS NULL OR parent.postprocessing_plan_id IS DISTINCT FROM p.id
    OR cf.media_kind IS DISTINCT FROM 'subtitle' OR pf.media_kind IS DISTINCT FROM 'video'
    OR cf.bound_video_file_ref IS DISTINCT FROM pf.file_ref)) THEN RETURN NULL; END IF;
 IF jsonb_typeof(p.committed_operations) IS DISTINCT FROM 'array'
 OR (SELECT count(*) FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id)<>jsonb_array_length(p.committed_operations)
 OR EXISTS(SELECT 1 FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.committed_operations) x WHERE x->>'id'=o.id AND x->>'file_ref'=o.file_ref
  AND x->>'artifact'=o.artifact_id AND (x->>'version')::bigint=o.artifact_version AND x->>'file'=o.artifact_file_id
  AND x->>'operation'=o.operation AND x->>'source'=o.source_path AND x->>'target'=o.target_path
  AND x->'fingerprint'=o.source_fingerprint AND (x->>'size')::bigint=o.expected_size_bytes
  AND x->>'intent'=o.intent AND x->>'collision'=o.collision_policy AND x->'render'=o.frozen_render_snapshot
  AND x->'config'=o.frozen_config_snapshot AND NULLIF(x->'overwrite','null'::jsonb) IS NOT DISTINCT FROM o.overwrite_execution_snapshot
  AND NULLIF(x->>'bound_video','') IS NOT DISTINCT FROM o.bound_video_operation_id AND x->>'key'=o.idempotency_key
  AND o.correlation_id=p.acquisition_id AND o.causation_type='postprocessing_plan' AND o.causation_id=p.id))
 OR EXISTS(SELECT 1 FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id AND o.bound_video_operation_id IS NULL
  AND NOT EXISTS(SELECT 1 FROM public.work_item_history fw WHERE fw.owner_type='file_operation' AND fw.owner_id=o.id AND fw.kind='file_execute'
  AND fw.acquisition_id=p.acquisition_id AND fw.input_snapshot->>'operation_id'=o.id
  AND fw.input_snapshot=(CASE WHEN w.input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',w.input_snapshot->'one_shot') ELSE '{}'::jsonb END)||jsonb_build_object('operation_id',o.id,'artifact_id',o.artifact_id,'artifact_file_id',o.artifact_file_id)||CASE WHEN o.collision_policy='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END
  AND fw.retention_days=7 AND fw.idempotency_key=o.idempotency_key AND fw.max_attempts=o.file_max_attempts
  AND fw.input_snapshot->'one_shot' IS NOT DISTINCT FROM w.input_snapshot->'one_shot'
  AND fw.input_snapshot->>'artifact_id'=o.artifact_id AND fw.input_snapshot->>'artifact_file_id'=o.artifact_file_id))
 OR EXISTS(SELECT 1 FROM public.file_operations o WHERE o.postprocessing_plan_id=p.id AND o.file_ready_at IS DISTINCT FROM p.sealed_at) THEN RETURN NULL; END IF;
 IF jsonb_typeof(p.committed_binding_sources) IS DISTINCT FROM 'array'
 OR jsonb_array_length(p.committed_binding_sources)<>(SELECT count(*) FROM public.artifact_files f JOIN public.acquisition_files q ON q.acquisition_id=f.acquisition_id AND q.file_ref=f.file_ref WHERE f.artifact_id=p.artifact_id AND f.media_kind='subtitle' AND q.requirement='required')
 OR (SELECT count(DISTINCT s->>'file') FROM jsonb_array_elements(p.committed_binding_sources) s)<>jsonb_array_length(p.committed_binding_sources)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p.committed_binding_sources) s LEFT JOIN public.artifact_files f ON f.id=s->>'file'
  WHERE f.id IS NULL OR f.artifact_id IS DISTINCT FROM p.artifact_id OR f.media_kind IS DISTINCT FROM 'subtitle'
  OR (s->'snapshot'='null'::jsonb AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p.committed_bindings) b WHERE b->>'file'=f.id AND b ? 'decision'))
  OR (s->'snapshot'<>'null'::jsonb AND (f.subtitle_binding_state IS DISTINCT FROM s->>'state' OR f.bound_video_file_ref IS DISTINCT FROM s->>'bound_ref'
  OR (f.subtitle_binding_snapshot-'locked') IS DISTINCT FROM ((s->'snapshot')-'locked')
  OR (s->'snapshot' ? 'locked' AND f.subtitle_binding_snapshot->'locked' IS DISTINCT FROM s->'snapshot'->'locked')))) THEN RETURN NULL; END IF;
 IF jsonb_typeof(p.committed_bindings) IS DISTINCT FROM 'array'
 OR jsonb_array_length(p.committed_bindings)>10000
 OR (SELECT count(DISTINCT b->>'file') FROM jsonb_array_elements(p.committed_bindings) b)<>jsonb_array_length(p.committed_bindings)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p.committed_bindings) b WHERE NOT EXISTS(
  SELECT 1 FROM public.artifact_files f JOIN public.acquisition_files q ON q.acquisition_id=f.acquisition_id AND q.file_ref=f.file_ref
  WHERE f.id=b->>'file' AND f.artifact_id=p.artifact_id AND f.media_kind='subtitle' AND q.requirement='required')) THEN RETURN NULL; END IF;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(p.committed_bindings) b LEFT JOIN public.artifact_files f ON f.id=b->>'file'
  WHERE b ? 'decision' AND (f.id IS NULL OR f.artifact_id IS DISTINCT FROM p.artifact_id
  OR f.subtitle_binding_state IS DISTINCT FROM b->>'state' OR f.bound_video_file_ref IS DISTINCT FROM b->>'bound_ref'
  OR (f.subtitle_binding_snapshot-'locked') IS DISTINCT FROM b->'decision')) THEN RETURN NULL; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations o JOIN public.artifact_files f ON f.id=o.artifact_file_id
  WHERE o.postprocessing_plan_id=p.id AND o.bound_video_operation_id IS NOT NULL AND (
  f.subtitle_binding_state IS DISTINCT FROM 'bound' OR f.bound_video_file_ref IS DISTINCT FROM o.frozen_render_snapshot->'binding'->>'bound_video_file_ref'
  OR f.subtitle_binding_snapshot IS DISTINCT FROM (SELECT x->'binding_snapshot' FROM jsonb_array_elements(p.committed_operations) x WHERE x->>'id'=o.id)
  OR f.subtitle_binding_snapshot->>'version' IS DISTINCT FROM 'subtitle-binding-1'
  OR f.subtitle_binding_snapshot->>'method' IS DISTINCT FROM o.frozen_render_snapshot->'binding'->>'method'
  OR f.subtitle_binding_snapshot->'confidence' IS DISTINCT FROM o.frozen_render_snapshot->'binding'->'confidence'
  OR f.subtitle_binding_snapshot->'locked' IS DISTINCT FROM jsonb_build_object('video_file_ref',f.bound_video_file_ref,
  'video_operation_id',o.bound_video_operation_id,'directory',o.frozen_render_snapshot->'binding'->>'locked_directory',
  'basename',o.frozen_render_snapshot->'binding'->>'locked_basename','extension',o.frozen_render_snapshot->'binding'->>'locked_extension'))) THEN RETURN NULL; END IF;
 RETURN jsonb_build_object('state','succeeded');
END $$;

CREATE OR REPLACE FUNCTION public.filefact_source_relation_valid(p_root text, p_artifact text, p_ref text, p_fact jsonb) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE i jsonb; f public.artifact_files%ROWTYPE; aw public.acquisitions%ROWTYPE; k text; expected jsonb; g record; member record; paths text[]:='{}'; listed text[]; render jsonb; binding jsonb; replay jsonb; offset_group text; video_count jsonb;
BEGIN
 SELECT input INTO STRICT i FROM public.acquisitions WHERE id=p_root;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=p_artifact AND acquisition_id=p_root AND file_ref=p_ref;
 offset_group:=CASE WHEN p_fact->'schema_version'='2'::jsonb THEN 'episode_offset' ELSE 'source_offset' END;
 IF p_fact->'provenance'->'artifact_file_ref' IS DISTINCT FROM jsonb_build_object('state','known','value',f.id,'reason_code',NULL)
 OR p_fact->'content_source'->'file_ref' IS DISTINCT FROM jsonb_build_object('state','known','value',f.file_ref,'reason_code',NULL)
 OR p_fact->'content_source'->'relative_path' IS DISTINCT FROM jsonb_build_object('state','known','value',f.relative_path,'reason_code',NULL)
 OR p_fact->'content_source'->'original_name' IS DISTINCT FROM jsonb_build_object('state','known','value',regexp_replace(f.relative_path,'^.*/',''),'reason_code',NULL)
 OR p_fact->'content_source'->'media_kind' IS DISTINCT FROM jsonb_build_object('state','known','value',f.media_kind,'reason_code',NULL) THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['payload_digest','manifest_digest'] LOOP
 IF p_fact->'content_source'->k IS DISTINCT FROM jsonb_build_object('state','known','value',i->'source'->k,'reason_code',NULL) THEN RETURN false; END IF;
 END LOOP;
 FOREACH k IN ARRAY ARRAY['infohash_v1','infohash_v2','title','group','provider','locator_display','release'] LOOP
 IF p_fact->'content_source'->k IS DISTINCT FROM i->'source'->k THEN RETURN false; END IF;
 END LOOP;
 video_count:=jsonb_build_object('state','known','value',(SELECT count(*) FROM public.artifact_files WHERE artifact_id=p_artifact AND media_kind='video'),'reason_code',NULL);
 IF p_fact->'identity_coverage'->'identity' IS DISTINCT FROM i->'media'->'identity'
 OR p_fact->'quality' IS DISTINCT FROM COALESCE(i->'media'->'quality',jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'))
 OR p_fact->offset_group->'video_count' IS DISTINCT FROM video_count THEN RETURN false; END IF;
 IF offset_group='episode_offset' THEN
  expected:=CASE WHEN i->'media'->'identity'->>'state'='known' THEN jsonb_build_object('state','known','value',i->'media'->'identity'->'value'->'offset','reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
  IF p_fact->'episode_offset'->'offset' IS DISTINCT FROM expected THEN RETURN false; END IF;
 ELSE
  IF p_fact->'source_offset'->'used_offset' IS DISTINCT FROM i->'media'->'source_offset' THEN RETURN false; END IF;
  FOREACH k IN ARRAY ARRAY['source','override'] LOOP
  IF p_fact->'source_offset'->k IS DISTINCT FROM jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') THEN RETURN false; END IF; END LOOP;
 END IF;
 IF COALESCE(p_fact->'identity_coverage'->>'coverage_schema','') NOT IN ('episode-identity-v1','episode-identity-v2')
 OR p_fact->'identity_coverage'->'coverage_keys' IS DISTINCT FROM (CASE WHEN i->'media'->'identity'->>'state'='known' THEN i->'media'->'identity'->'value'->'coverage_keys' ELSE '[]'::jsonb END) THEN RETURN false; END IF;
 SELECT render_snapshot INTO STRICT render FROM public.acquisition_files WHERE acquisition_id=p_root AND file_ref=p_ref;
 binding:=render->'binding_facts'; replay:=render->'render_facts';
 FOREACH k IN ARRAY ARRAY['File.Media.Resolution','File.Media.VideoCodec','File.Media.Container','Parsed.Language.Code','Parsed.Episode.Display','Source.GroupName','Source.Group','Release.OriginalTitle','Release.FrozenEpisode.Display','Anime.BGM.WorkID','Anime.TMDB.WorkID'] LOOP
 expected:=CASE WHEN replay ? k THEN jsonb_build_object('state','known','value',replay->k,'reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'replay'->k IS DISTINCT FROM expected THEN RETURN false; END IF;
 END LOOP;
 IF binding->>'media_kind' IN ('tv','movie') THEN
 expected:=jsonb_build_object('media_kind',binding->'media_kind','season',binding->'season','season_bound',binding->'season_bound');
 FOREACH k IN ARRAY ARRAY['bgm_work_id','tmdb_work_id','effective_title','extension'] LOOP
 expected:=expected||jsonb_build_object(k,CASE WHEN binding ? k AND jsonb_typeof(binding->k)='string' THEN CASE WHEN binding->>k<>'' OR k='extension' THEN jsonb_build_object('state','known','value',binding->k,'reason_code',NULL) ELSE jsonb_build_object('state','absent','value',NULL,'reason_code',NULL) END ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END);
 END LOOP;
 expected:=jsonb_build_object('state','known','value',expected,'reason_code',NULL);
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 IF p_fact->'render_inputs'->'binding' IS DISTINCT FROM expected
 OR p_fact->'render_inputs'->'naming_schema' IS DISTINCT FROM jsonb_build_object('state','known','value','naming-rules-v1','reason_code',NULL) THEN RETURN false; END IF;
 expected:=CASE WHEN COALESCE(render->>'renderer_version','')<>'' THEN jsonb_build_object('state','known','value',render->>'renderer_version','reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'render_program_version' IS DISTINCT FROM expected THEN RETURN false; END IF;
 SELECT public.json_member_bytes(public.json_member_bytes(input_canonical_utf8,'delivery'),'naming') INTO k FROM public.acquisitions WHERE id=p_root;
 expected:=CASE WHEN k IS NOT NULL THEN jsonb_build_object('state','known','value','sha256:'||encode(sha256(convert_to('acquisition-naming:v1','UTF8')||decode('00','hex')||convert_to(k,'UTF8')),'hex'),'reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
 IF p_fact->'render_inputs'->'naming_digest' IS DISTINCT FROM expected THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['media_kind','season','season_bound'] LOOP
 IF i->'media'->'tmdb'->>'state'='known' THEN
 expected:=jsonb_build_object('state','known','value',(CASE k WHEN 'media_kind' THEN i->'media'->'tmdb'->'value'->'kind' WHEN 'season' THEN i->'media'->'tmdb'->'value'->'season' ELSE 'true'::jsonb END),'reason_code',NULL);
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 IF p_fact->offset_group->k IS DISTINCT FROM expected THEN RETURN false; END IF; END LOOP;
 FOR g IN SELECT * FROM jsonb_each(p_fact) LOOP
 IF jsonb_typeof(g.value)<>'object' THEN CONTINUE; END IF;
 -- New inputs retire media.quality and project one unavailable FrozenValue.
 -- Unlike historical QualityV1's nested dimensions, that value itself owns a
 -- missing path; nested groups keep the member scan below unchanged.
 IF public.observed_closed(g.value,ARRAY['state','value','reason_code']) IS TRUE AND g.value->>'state'='unavailable' THEN
  paths:=array_append(paths,'/'||replace(replace(g.key,'~','~0'),'/','~1'));
 END IF;
 FOR member IN SELECT * FROM jsonb_each(g.value) LOOP
 IF public.observed_closed(member.value,ARRAY['state','value','reason_code']) IS TRUE AND member.value->>'state'='unavailable' THEN
 paths:=array_append(paths,'/'||replace(replace(g.key,'~','~0'),'/','~1')||'/'||replace(replace(member.key,'~','~0'),'/','~1'));
 END IF; END LOOP; END LOOP;
 SELECT COALESCE(array_agg(v ORDER BY v COLLATE "C"),'{}'::text[]) INTO paths FROM unnest(paths) v;
 SELECT COALESCE(array_agg(v ORDER BY n),'{}'::text[]) INTO listed FROM jsonb_array_elements_text(p_fact->'provenance'->'missing_paths') WITH ORDINALITY x(v,n);
 IF listed IS DISTINCT FROM paths THEN RETURN false; END IF;
 SELECT * INTO STRICT aw FROM public.acquisitions WHERE id=p_root;
 IF aw.preparation_witness_canonical_utf8 IS NOT NULL THEN
 expected:=jsonb_build_object('state','known','reason_code',NULL,'value',jsonb_build_object('relation','acquisition_preparation_witness_v1','row_ref',p_root,'version',jsonb_build_object('state','known','value',1,'reason_code',NULL),'digest',jsonb_build_object('state','known','value',aw.preparation_digest,'reason_code',NULL),'observed_at',aw.preparation_witness->'observation'->'observed_at','selection','recorded_reference'));
 ELSE expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE'); END IF;
 RETURN p_fact->'provenance'->'exact_selection'=expected;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $$;

CREATE OR REPLACE FUNCTION public.file_success_projection(p_operation text, p_permit text, p_file_fact text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; e public.file_operations;
 r public.acquisitions; fact jsonb; published_object jsonb; digest text; cleanup text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=o.acquisition_id;
 -- No locks are taken here: the fixed caller must already hold the complete
 -- root-first lock set. This component cannot finish a work item or grant I/O.
 IF public.plan_canonical_valid(p_file_fact) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESULT_FACT_CANONICAL_INVALID'; END IF;
 fact:=p_file_fact::jsonb;
 published_object:=e.publication_step_checkpoint->'publish_target'->'proof'->'target';
 digest:=public.file_proof_digest('acquisition-effect-resolution:v1',e.publication_resolution_canonical);
 cleanup:=CASE WHEN e.operation='move' THEN 'completed' ELSE 'not_required' END;
 IF (e.id=o.id AND e.acquisition_id=o.acquisition_id AND e.file_ref=o.file_ref
  AND e.publication_dispatch_state='closed' AND e.publication_effect_class='confirmed' AND e.publication_conflict_evidence IS NULL
  AND e.publication_resolution_canonical IS NOT NULL AND e.publication_closure_canonical IS NOT NULL
  AND e.publication_step_checkpoint->'publish_target'->>'state'='confirmed'
  AND NOT EXISTS(SELECT 1 FROM jsonb_each(e.publication_step_checkpoint) slot WHERE slot.value->>'state' NOT IN ('confirmed','not_required'))
  AND published_object IS NOT NULL AND published_object->'size'=e.publication_frozen_objects->'source'->'size'
  AND public.filefact_source_relation_valid(o.acquisition_id,o.artifact_id,o.file_ref,fact)
  AND fact->'provenance'->'acquisition_ref'->>'value'=o.acquisition_id
  AND fact->'provenance'->'artifact_ref'->>'value'=o.artifact_id
  AND fact->'provenance'->'operation_ref'->>'value'=o.id
  AND fact->'provenance'->'delivery_digest'->>'value'=digest
  AND fact->'locator'->'final_object'->'value'=published_object) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESULT_PROJECTION_INVALID'; END IF;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='succeeded',completion_mode='executed',
 effect_class='confirmed',final_object=published_object,delivery_source_cleanup=cleanup,evidence_digest=digest,
 operation_id=o.id,receipt=e.publication_resolution_proof,delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
END $$;

CREATE OR REPLACE FUNCTION public.notification_value_facts(p_source_facts jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE facts jsonb; title_tags jsonb:='[]'::jsonb; anibt_tags jsonb:='[]'::jsonb;
BEGIN
 facts:=jsonb_build_object('rt.title',NULL,'rt.group',NULL,'rt.rev',NULL,'rt.res',NULL,'rt.source',NULL,'rt.vc',NULL,
  'rt.ac',NULL,'rt.lang',NULL,'rt.subflag',NULL,'rt.uncensored',NULL,'anibt.res',NULL,'anibt.subtitle',NULL,
  'anibt.lang',NULL,'anibt.format',NULL,'anibt.tag',NULL);
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
  'rt.group',public.file_notification_text(public.notification_tag_values(title_tags,'group')),
  'rt.rev',public.file_notification_text(public.notification_tag_values(title_tags,'version')),
  'rt.res',public.file_notification_text(public.notification_tag_values(title_tags,'resolution')),
  'rt.source',public.file_notification_text(public.notification_tag_values(title_tags,'source_type')),
  'rt.vc',public.file_notification_text(public.notification_tag_values(title_tags,'video_codec')),
  'rt.ac',public.file_notification_text(public.notification_tag_values(title_tags,'audio_codec')),
  'rt.lang',public.file_notification_text(public.notification_tag_values(title_tags,'sub_lang')),
  'rt.subflag',public.file_notification_text(public.notification_tag_values(title_tags,'sub_flag')),
  'rt.uncensored',CASE WHEN p_source_facts->'title_tags'->>'schema'='reltitle-attrs-v1'
    AND COALESCE(p_source_facts->'title_tags'->>'diagnostic','')=''
   THEN to_jsonb(COALESCE(jsonb_typeof(p_source_facts->'title_tags'->'tags')='array'
    AND p_source_facts->'title_tags'->'tags' @> jsonb_build_array('content_rating:UNCENSORED'),false)) END,
  'anibt.res',public.file_notification_text(public.notification_tag_values(anibt_tags,'resolution')),
  'anibt.subtitle',public.file_notification_text(public.notification_tag_values(anibt_tags,'subtitle')),
  'anibt.lang',public.file_notification_text(public.notification_tag_values(anibt_tags,'language')),
  'anibt.format',public.file_notification_text(public.notification_tag_values(anibt_tags,'format')),
  'anibt.tag',public.file_notification_text(public.notification_tag_values(public.notification_anibt_custom_tags(anibt_tags),'tag')));
END $$;
REVOKE ALL ON FUNCTION public.notification_value_facts(jsonb) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 entry_kind text; input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 target_path text; output_directory text; output_name text; file_identity jsonb; release_identity jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input, q.entry_kind INTO STRICT input, entry_kind FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 SELECT CASE WHEN mf.file_ref IS NOT NULL
             THEN COALESCE(mf.effective_facts->'identity', 'null'::jsonb)
        END,
        snap.release_identity INTO file_identity, release_identity
 FROM public.file_operations op
 LEFT JOIN public.artifacts art ON art.id=op.artifact_id
 LEFT JOIN public.download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN public.releases rel ON rel.id=dt.release_id
 LEFT JOIN LATERAL (
     SELECT snap.id, snap.release_identity FROM public.media_fact_snapshots snap
     WHERE snap.release_id=rel.id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1
 ) snap ON TRUE
 LEFT JOIN public.media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=op.file_ref
 WHERE op.id=o.id;
 src:=input->'source';
 IF p_fact->'render_inputs'->'binding'->>'state'='known' AND p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'state'='known' THEN
 title:=p_fact->'render_inputs'->'binding'->'value'->'effective_title'->>'value'; END IF;
 IF src->'release'->>'state'='known' THEN release_id:=src->'release'->'value'->>'ref'; END IF;
 name:=regexp_replace(rtrim(f.relative_path,'/'),'^.*/','');
 IF public.file_notification_text(name) IS NOT NULL THEN
 extension:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'^.*\.','') ELSE '' END;
 base:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'\.[^.]*$','') ELSE name END;
 END IF;
 location:=e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'location';
 target_path:=location->>'canonical_path';
 IF target_path IS NOT NULL AND left(target_path,1)='/' AND public.notification_path_text(target_path) IS NOT NULL THEN
  output_name:=regexp_replace(target_path,'^.*/','');
  output_directory:=CASE WHEN target_path ~ '^/[^/]+$' THEN '/' ELSE regexp_replace(target_path,'/[^/]*$','') END;
 END IF;
 RETURN jsonb_build_object(
 'task.origin',public.notification_task_origin(entry_kind),
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.title_override',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.stem',public.file_notification_text(base),'file.ext',public.file_notification_text(lower(extension)),
 'file.size',f.size_bytes,'file.kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'out.dir',public.notification_path_text(output_directory),'out.name',public.notification_name_text(output_name),'out.path',public.notification_path_text(target_path),
 'out.subtitles','[]'::jsonb,
 'op.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'op.conflict',public.file_notification_text(e.collision_policy))
 || public.notification_input_facts(input)
 || public.notification_media_episode_facts(file_identity, release_identity)
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'source'->'tag_evidence');
END $_$;

CREATE OR REPLACE FUNCTION public.file_failure_notification(p_operation text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; a public.acquisitions; f public.artifact_files;
 input jsonb; src jsonb; title text; release_id text; name text; base text; extension text; reason text;
 file_identity jsonb; release_identity jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT a FROM public.acquisitions WHERE id=o.acquisition_id;
 SELECT * INTO f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT CASE WHEN mf.file_ref IS NOT NULL
             THEN COALESCE(mf.effective_facts->'identity', 'null'::jsonb)
        END,
        snap.release_identity INTO file_identity, release_identity
 FROM public.file_operations op
 LEFT JOIN public.artifacts art ON art.id=op.artifact_id
 LEFT JOIN public.download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN public.releases rel ON rel.id=dt.release_id
 LEFT JOIN LATERAL (
     SELECT snap.id, snap.release_identity FROM public.media_fact_snapshots snap
     WHERE snap.release_id=rel.id ORDER BY snap.created_at DESC, snap.id DESC LIMIT 1
 ) snap ON TRUE
 LEFT JOIN public.media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=op.file_ref
 WHERE op.id=o.id;
 input:=a.input;
 src:=input->'source';
 IF a.display_title->>'state'='known' AND jsonb_typeof(a.display_title->'value')='string' THEN
  title:=a.display_title->>'value'; END IF;
 IF src->'release'->>'state'='known' THEN release_id:=src->'release'->'value'->>'ref'; END IF;
 name:=regexp_replace(rtrim(f.relative_path,'/'),'^.*/','');
 IF public.file_notification_text(name) IS NOT NULL THEN
 extension:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'^.*\.','') ELSE '' END;
 base:=CASE WHEN strpos(name,'.')>0 THEN regexp_replace(name,'\.[^.]*$','') ELSE name END;
 END IF;
 reason:=COALESCE(public.file_notification_text(p_reason),p_code);
 RETURN jsonb_build_object(
 'task.origin',public.notification_task_origin(a.entry_kind),
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.title_override',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'event.type','file_operation_failed','result.outcome','failure','result.reason',reason,
 'file.name',public.file_notification_text(name),'file.stem',public.file_notification_text(base),'file.ext',public.file_notification_text(lower(extension)),
 'file.size',f.size_bytes,'file.kind',public.file_notification_text(f.media_kind),
 'media.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'media.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'media.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'media.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'media.hdr',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'hdr')='string' THEN f.facts->>'hdr' END),
 'out.dir',NULL,'out.name',NULL,'out.path',NULL,
 'op.kind',public.file_notification_text(o.operation),
 'op.conflict',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_media_episode_facts(file_identity, release_identity)
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'source'->'tag_evidence');
END $_$;

CREATE OR REPLACE FUNCTION public.unknown_files_notification(p_root text, p_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry_kind text; input jsonb; release_id text; source_facts jsonb; link text;
BEGIN
 SELECT q.input, q.entry_kind INTO input, entry_kind FROM public.acquisitions q WHERE q.id=p_root;
 SELECT t.release_id INTO release_id FROM public.download_tasks t WHERE t.acquisition_id=p_root AND t.release_id IS NOT NULL LIMIT 1;
 source_facts:=input->'source'->'tag_evidence';
 -- A notice payload is frozen and delivered to external adapters as it stands,
 -- so it carries no absolute container path. The tree the files live under is
 -- not part of that payload either, so the link cannot name one of them: it
 -- opens this acquisition's execution record, which lists the files, and the
 -- owner matches them from there. The anchor is an in-app hash.
 link:='#/?record='||public.notification_query_escape(p_root);
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),release_id)
  || jsonb_build_object(
   'task.origin',public.notification_task_origin(entry_kind),
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(p_root)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(p_root),
   'out.count',jsonb_array_length(p_files),
   'out.files',p_files,
   'event.link',link);
END $_$;

CREATE OR REPLACE FUNCTION public.download_failed_notification(p_task text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE t public.download_tasks; entry_kind text; input jsonb; source_facts jsonb; retryable boolean;
BEGIN
 SELECT * INTO STRICT t FROM public.download_tasks WHERE id=p_task;
 SELECT q.input, q.entry_kind INTO input, entry_kind FROM public.acquisitions q WHERE q.id=t.acquisition_id;
 source_facts:=input->'source'->'tag_evidence';
 -- Retryable from the RSS page once settled: the release still points at this
 -- task, its anime is not retired, and the acquisition is not a one-shot.
 retryable:=EXISTS (
   SELECT 1 FROM public.releases rel
   JOIN public.rss_sources s ON s.id=rel.rss_source_id
   JOIN public.subscriptions sub ON sub.id=s.subscription_id
   LEFT JOIN public.managed_anime l ON l.id=sub.managed_anime_id
   WHERE rel.download_task_id=t.id AND COALESCE(l.lifecycle_state,'active')='active')
  AND NOT EXISTS (SELECT 1 FROM public.acquisitions a WHERE a.id=t.acquisition_id AND a.entry_kind='one_shot');
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),t.release_id)
  || jsonb_build_object(
   'task.origin',public.notification_task_origin(entry_kind),
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(t.acquisition_id)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(t.acquisition_id),
   'result.outcome','failure',
   'result.category',public.download_failure_category(t.failure_code),
   'result.retryable',retryable);
END $$;

DROP FUNCTION public.accept_acquisition_queued_with_vars(text, text, text, text, text, text, jsonb, text, jsonb, jsonb, text, integer, integer, integer, text, text, text, text, bytea, text, text, text, jsonb, text, text, text, text, jsonb, jsonb, text, text, text, jsonb, integer, jsonb, jsonb, integer, integer, text, text, text, jsonb, text);
DROP FUNCTION public.accept_acquisition_queued_without_vars(text, text, text, text, text, text, jsonb, text, jsonb, jsonb, text, integer, integer, integer, text, text, text, text, bytea, text, text, text, jsonb, text, text, text, text, jsonb, jsonb, text, text, text, jsonb, integer, jsonb, jsonb, integer, integer, text, text, text, jsonb);

DROP FUNCTION public.vars_filefact_valid(text, jsonb);
DROP FUNCTION public.vars_evidence_consistent(text);
DROP FUNCTION public.vars_evaluation_valid(text, text, text, text);
DROP FUNCTION public.vars_member_bytes(text, text);
DROP FUNCTION public.notification_value_facts(jsonb, jsonb);

ALTER TABLE public.acquisitions
 DROP CONSTRAINT acquisitions_vars_evaluation_check,
 DROP CONSTRAINT acquisitions_vars_evidence_digest_check,
 DROP CONSTRAINT acquisitions_vars_evidence_version_check,
 DROP COLUMN vars_evaluation,
 DROP COLUMN vars_evaluation_canonical_utf8,
 DROP COLUMN vars_evidence_digest,
 DROP COLUMN vars_evidence_version;

DROP TABLE public.naming_vars_versions;
DROP FUNCTION public.guard_naming_vars_version();
DROP FUNCTION public.naming_vars_assign_revision();

DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM public.anime_business_configs
     WHERE business_snapshot IS NOT NULL AND business_snapshot->>'schema_version' IS DISTINCT FROM 'business-snapshot-v3')
 OR EXISTS (SELECT 1 FROM public.anime_config_candidates WHERE activated_managed_anime_id IS NULL AND status='working'
     AND draft ? 'business' AND draft->'business'->>'schema_version' IS DISTINCT FROM 'business-snapshot-v3')
 OR EXISTS (SELECT 1 FROM public.anime_config_candidate_revisions r
     JOIN public.anime_config_candidates c ON c.id=r.candidate_id WHERE c.activated_managed_anime_id IS NULL AND c.status='working'
     AND ((r.business_snapshot IS NOT NULL AND r.business_snapshot->>'schema_version' IS DISTINCT FROM 'business-snapshot-v3')
          OR (r.draft ? 'business' AND r.draft->'business'->>'schema_version' IS DISTINCT FROM 'business-snapshot-v3'))) THEN
  RAISE EXCEPTION 'BUSINESS_SNAPSHOT_V3_INCOMPLETE';
 END IF;
 IF to_regclass('public.naming_vars_versions') IS NOT NULL
 OR EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='public.acquisitions'::regclass AND NOT attisdropped
     AND attname IN ('vars_evaluation', 'vars_evaluation_canonical_utf8', 'vars_evidence_digest', 'vars_evidence_version'))
 OR EXISTS (SELECT 1 FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN (
     'accept_acquisition_queued_with_vars','accept_acquisition_queued_without_vars','vars_filefact_valid',
     'vars_evidence_consistent','vars_evaluation_valid','vars_member_bytes','guard_naming_vars_version','naming_vars_assign_revision'))
 OR (SELECT count(*)=1 AND bool_and(pronargs=42 AND has_function_privilege('bansoko_runtime',oid,'EXECUTE'))
     FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname='accept_acquisition_queued') IS NOT TRUE
 OR (SELECT count(*)=1 AND bool_and(pronargs=1)
     FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname='notification_value_facts') IS NOT TRUE THEN
  RAISE EXCEPTION 'NAMING_VARS_RETIREMENT_INCOMPLETE';
 END IF;
END $$;
