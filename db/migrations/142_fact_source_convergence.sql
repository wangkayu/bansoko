-- Forward-only migration 142: converge accepted release evidence.
-- Historical canonical inputs, receipts and deliveries are never rewritten.
-- Active old work must settle before switching the evidence contract.
COMMENT ON COLUMN release_candidate_facts.compare_values IS 'compare-values-v2: a pure projection of frozen native release tags and stable RSS source identity, with projection source_digest. Legacy rows are explicitly rebuilt from that evidence, never interpreted as empty values.';
-- The historical rule-version table remains as evidence, but the retired
-- production owner no longer needs its runtime write grant.
REVOKE INSERT ON TABLE public.attr_rule_versions FROM bansoko_runtime;
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM public.acquisitions WHERE state='running'
            AND (NOT (input->'source' ? 'tag_evidence')
                 OR preparation_witness::text ~ 'attr_fingerprint|attrrules')) THEN
  RAISE EXCEPTION 'FACT_SOURCE_ACTIVE_INPUTS: settle accepted work before upgrading';
 END IF;
END $$;

-- A retired template reference is never mechanically rewritten. chain accepts
-- arbitrary expressions, ep.kind has no identity-backed replacement, and the
-- old media/release fields changed source semantics. Inspect only fields that
-- really are naming templates; a media.resolution fact in a render snapshot is
-- not itself a template reference. The all-JSONB scan below includes current
-- configuration and frozen replay inputs while leaving their bytes untouched.
CREATE FUNCTION pg_temp.fact_source_retired_template(document jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
 template jsonb;
 body text;
BEGIN
 IF document IS NULL THEN RETURN FALSE; END IF;
 FOR template IN
  SELECT value FROM jsonb_path_query(document, '$.**.directory_template') AS found(value)
  UNION
  SELECT value FROM jsonb_path_query(document, '$.**.video_filename_template') AS found(value)
  UNION
  SELECT value FROM jsonb_path_query(document, '$.**.subtitle_suffix_template') AS found(value)
 LOOP
  IF jsonb_typeof(template) IS DISTINCT FROM 'string' THEN CONTINUE; END IF;
  body := template #>> '{}';
  IF body ~ '(^|[^[:alnum:]_])chain[[:space:]]*\('
     OR body ~ '(^|[^[:alnum:]_])ep\.kind([^[:alnum:]_]|$)'
     OR body ~ '(^|[^[:alnum:]_])media\.resolution([^[:alnum:]_]|$)'
     OR body ~ '(^|[^[:alnum:]_])release\.(lang|langs|lang_tags)([^[:alnum:]_]|$)'
     OR body ~ '(^|[^[:alnum:]_.])res([^[:alnum:]_]|$)' THEN
   RETURN TRUE;
  END IF;
 END LOOP;
 RETURN FALSE;
END;
$$;

DO $$
DECLARE
 old_default constant jsonb := '{"version":"profile-v1","items":[
  {"key":"subgroup.score","ranks":[]},
  {"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},
  {"key":"rt.sub_lang","ranks":[["JPSC","JPTC"],["JPSC"],["SC","TC"],["SC"],["JPTC"],["TC"],["JPN"],["ENG"]]},
  {"key":"anibt.resolution","ranks":[["4K"],["2160p"],["1080p"],["720p"],["480p"],["360p"]]},
  {"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},
  {"key":"release.revision","ranks":[]}
 ]}'::jsonb;
 location record;
 found boolean;
BEGIN
 -- Only the Global exact built-in default changes automatically. It affects
 -- later subscriptions only. Every existing instance, saved draft or custom
 -- Global profile keeps its bytes and stops the upgrade for an explicit review:
 -- even an rt-only v1 profile loses the hidden size tiebreak under v2.
 IF EXISTS (SELECT 1 FROM public.global_config_sections
            WHERE section_name='replacement'
              AND value->'quality_profile'->>'version' IS DISTINCT FROM 'profile-v2'
              AND value->'quality_profile' IS DISTINCT FROM old_default) THEN
  RAISE EXCEPTION 'FACT_SOURCE_GLOBAL_PROFILE: custom or non-equivalent comparison profile requires review';
 END IF;
 IF EXISTS (SELECT 1 FROM public.anime_business_configs
            WHERE quality_profile IS NOT NULL
              AND quality_profile->>'version' IS DISTINCT FROM 'profile-v2') THEN
  RAISE EXCEPTION 'FACT_SOURCE_INSTANCE_PROFILE: existing subscription comparison profiles require review';
 END IF;
 IF EXISTS (SELECT 1 FROM public.anime_config_candidates c
            WHERE c.draft->'business'->'replacement'->'quality_profile'->>'version' IS DISTINCT FROM 'profile-v2'
              AND c.draft->'business'->'replacement'->'quality_profile' IS NOT NULL) THEN
  RAISE EXCEPTION 'FACT_SOURCE_DRAFT_PROFILE: candidate comparison profiles require review';
 END IF;
 IF EXISTS (SELECT 1
            FROM public.anime_config_candidates c
            JOIN public.anime_config_candidate_revisions r
              ON r.candidate_id=c.id AND r.revision=c.current_revision
            WHERE (r.business_snapshot->'replacement'->'quality_profile' IS NOT NULL
                   AND r.business_snapshot->'replacement'->'quality_profile'->>'version' IS DISTINCT FROM 'profile-v2')
               OR (r.draft->'business'->'replacement'->'quality_profile' IS NOT NULL
                   AND r.draft->'business'->'replacement'->'quality_profile'->>'version' IS DISTINCT FROM 'profile-v2')) THEN
  RAISE EXCEPTION 'FACT_SOURCE_CURRENT_REVISION_PROFILE: current saved comparison profiles require review';
 END IF;

 -- Current sections, library templates, candidate bodies and every frozen
 -- replay/notification payload share the same conservative field-level scan.
 -- Historical rows are never edited; any unresolved replay dependency blocks.
 FOR location IN
  SELECT table_name,column_name
  FROM information_schema.columns
  WHERE table_schema='public' AND data_type='jsonb'
  ORDER BY table_name,column_name
 LOOP
  EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I WHERE pg_temp.fact_source_retired_template(%I))',
                 location.table_name,location.column_name)
  INTO found;
  IF found THEN
   RAISE EXCEPTION 'FACT_SOURCE_RETIRED_TEMPLATE: %.% contains chain, ep.kind, media.resolution/${res}, or old release language output',
    location.table_name,location.column_name;
  END IF;
 END LOOP;
END $$;

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
    OR jsonb_array_length(profile->'items') > 12 THEN
  RETURN FALSE;
 END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(profile->'items') LOOP
  IF jsonb_typeof(item) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
     OR NOT (item ?& ARRAY['key','ranks'])
     OR jsonb_typeof(item->'key') IS DISTINCT FROM 'string'
     OR item->>'key' NOT IN ('subgroup.score','rt.revision',
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
               WHEN item->>'key' IN ('anibt.language','rt.sub_lang','rt.sub_flag') THEN 'multi'
               ELSE 'single' END;
  IF kind='unranked' AND jsonb_array_length(item->'ranks')<>0 THEN
   RETURN FALSE;
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

-- The preflight above proved that every remaining v1 Global value is exactly
-- the built-in default. Install the v2 validator before writing that value.
UPDATE public.global_config_sections
SET value=jsonb_set(value,'{quality_profile}','{"version":"profile-v2","items":[
 {"key":"subgroup.score","ranks":[]},
 {"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},
 {"key":"rt.sub_lang","ranks":[["JPSC","JPTC"],["JPSC"],["SC","TC"],["SC"],["JPTC"],["TC"],["JPN"],["ENG"]]},
 {"key":"anibt.resolution","ranks":[["4K"],["2160p"],["1080p"],["720p"],["480p"],["360p"]]},
 {"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},
 {"key":"rt.revision","ranks":[]}
]}'::jsonb), version=version+1, updated_at=CURRENT_TIMESTAMP
WHERE section_name='replacement' AND value->'quality_profile'->>'version'='profile-v1';

CREATE OR REPLACE FUNCTION anime_config_candidate_draft_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $_$
DECLARE
 b JSONB;
 sections TEXT[] := ARRAY['subgroups','parser','release_rules','file_rules','naming_rules','file_effect'];
 name TEXT;
 ref JSONB;
BEGIN
 IF NOT anime_config_candidate_identity_shape_ok(draft - 'business' - 'business_digest',origin,origin_entry_ref,instance_label) THEN RETURN FALSE; END IF;
 IF NOT (draft ? 'business') AND NOT (draft ? 'business_digest') THEN RETURN TRUE; END IF;
 b := draft->'business';
 IF NOT business_exact_keys(b,ARRAY['schema_version','sections','section_versions','section_schemas','parser','replacement','replacement_version','unclassified_route','file_effect_version','programs']) THEN RETURN FALSE; END IF;
 IF b->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2'
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
 -- attrrules no longer has a program identity in new business snapshots. Vars
 -- keeps its strict implementation reference and canonical digest.
 IF NOT business_exact_keys(b->'programs',ARRAY['vars']) THEN RETURN FALSE; END IF;
 ref := b->'programs'->'vars';
 IF NOT business_exact_keys(ref,ARRAY['version','digest'])
    OR jsonb_typeof(ref->'version') IS DISTINCT FROM 'string'
    OR btrim(ref->>'version')=''
    OR jsonb_typeof(ref->'digest') IS DISTINCT FROM 'string'
    OR ref->>'digest' !~ '^sha256:[a-f0-9]{64}$' THEN RETURN FALSE; END IF;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN RETURN FALSE;
END;
$_$;

CREATE OR REPLACE FUNCTION recheck_acquisition_qualification(p_qualification jsonb, p_request_hash text, p_config_digest text, p_payload_digest text) RETURNS timestamp with time zone
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    c JSONB; mg JSONB; src JSONB; obs JSONB; feed JSONB; programs JSONB;
    v_row RECORD; v_group RECORD; v_feed RECORD; v_seed RECORD;
    v_program JSONB; v_now TIMESTAMPTZ; v_kind TEXT;
BEGIN
    IF NOT public.business_exact_keys(p_qualification,ARRAY['config','management','source','observation','feed','programs','placement'])
        OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
        OR p_config_digest IS NULL OR p_config_digest !~ '^sha256:[0-9a-f]{64}$'
        OR p_payload_digest IS NULL OR p_payload_digest !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_QUALIFICATION_INVALID';
    END IF;
    c:=p_qualification->'config'; mg:=p_qualification->'management'; src:=p_qualification->'source';
    obs:=p_qualification->'observation'; feed:=p_qualification->'feed'; programs:=p_qualification->'programs';
    IF NOT public.business_exact_keys(programs,ARRAY['vars_digest','translation'])
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
        IF (programs->'vars_digest'<>'null'::jsonb AND v_row.business_snapshot->'programs'->'vars'->>'digest' IS DISTINCT FROM programs->>'vars_digest') THEN
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
        IF v_row.business_snapshot IS NOT NULL AND ((programs->'vars_digest'<>'null'::jsonb AND v_row.business_snapshot->'programs'->'vars'->>'digest' IS DISTINCT FROM programs->>'vars_digest')) THEN
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

CREATE OR REPLACE FUNCTION accept_acquisition_queued_without_vars(p_acquisition_id text, p_entry_kind text, p_access_scope text, p_request_namespace text, p_request_key text, p_request_hash text, p_canonical_request jsonb, p_admission_digest text, p_config_identity jsonb, p_display_title jsonb, p_origin_managed_anime_ref text, p_detail_retention_seconds integer, p_receipt_after_accept_seconds integer, p_receipt_after_terminal_seconds integer, p_preparation_mode text, p_preparation_digest text, p_preparation_witness text, p_input_canonical_utf8 text, p_payload bytea, p_validated_infohash_v1 text, p_validated_infohash_v2 text, p_manifest_digest text, p_manifest_files jsonb, p_source_release_id text, p_download_task_id text, p_task_idempotency_key text, p_qbittorrent_connection_id text, p_task_frozen_input jsonb, p_task_frozen_policy jsonb, p_submit_work_item_id text, p_settle_work_item_id text, p_settle_idempotency_key text, p_submit_input_snapshot jsonb, p_max_attempts integer, p_qualification jsonb, p_business_snapshot jsonb, p_witness_max_bytes integer, p_witness_max_rows integer, p_correlation_id text, p_causation_type text, p_causation_id text, p_default_sections jsonb) RETURNS jsonb
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

    -- Fresh-only binding, then CP order inside 132. Replay never consumes
    -- today's qualification, business, catalog, program or witness arguments.
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

CREATE OR REPLACE FUNCTION filefact_source_relation_valid(p_root text, p_artifact text, p_ref text, p_fact jsonb) RETURNS boolean
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
 SELECT public.vars_member_bytes(public.vars_member_bytes(input_canonical_utf8,'delivery'),'naming') INTO k FROM public.acquisitions WHERE id=p_root;
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

CREATE OR REPLACE FUNCTION public.file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 entry_kind text; input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 target_path text; output_directory text; output_name text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input, q.entry_kind INTO STRICT input, entry_kind FROM public.acquisitions q WHERE q.id=o.acquisition_id;
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
 || public.notification_episode_facts(p_fact->'identity_coverage'->'identity')
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   input->'source'->'tag_evidence');
END $_$;

CREATE OR REPLACE FUNCTION file_failure_notification(p_operation text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; a public.acquisitions; f public.artifact_files;
 input jsonb; src jsonb; title text; release_id text; name text; base text; extension text; reason text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT a FROM public.acquisitions WHERE id=o.acquisition_id;
 SELECT * INTO f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=o.acquisition_id;
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
 'out.dir',NULL,'out.name',NULL,'out.path',NULL,
 'op.kind',public.file_notification_text(o.operation),
 'op.conflict',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_file_reltitle_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   input->'source'->'tag_evidence');
END $_$;

CREATE OR REPLACE FUNCTION unknown_files_notification(p_root text, p_files jsonb) RETURNS jsonb
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
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
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

CREATE OR REPLACE FUNCTION download_failed_notification(p_task text) RETURNS jsonb
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
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
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
