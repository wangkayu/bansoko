-- Forward-only migration 061: subgroup score rules (owner ruling 15).
--
-- A required (always / manual-selected) group is decoupled from max_rss: it is
-- always selected and never consumes a slot, so provider_group_selections
-- loses count_in_limit. Its written and compared score is fixed at 100
-- instead of 0. The two functions that validated the count_in_limit key of a
-- queued acquisition (the preparation witness and the subgroups default-section
-- key whitelist) re-create as their baseline bodies with the key removed; the
-- witness check now also expects an always decision to score 100 and an always
-- decision no longer reduces the remaining ranked slots. Existing data is not
-- preserved: only the stored manual-selected scores are rewritten.
ALTER TABLE provider_group_selections DROP COLUMN count_in_limit;

UPDATE provider_group_selections SET effective_score = 100
WHERE selection_mode = 'manual' AND manual_selected IS TRUE;

CREATE OR REPLACE FUNCTION assert_queued_preparation_witness(p_q jsonb, p_business jsonb, p_request text, p_input text, p_config text, p_mode text, p_canonical text, p_digest text, p_max_bytes integer, p_max_rows integer) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
 w JSONB; policy JSONB; locked JSONB; g JSONB; d JSONB; entry JSONB;
 refs TEXT[]; decision_refs TEXT[]; selected TEXT[]; actual_selected TEXT[]:=ARRAY[]::TEXT[];
 provider TEXT; expected_mode TEXT; observed BOOLEAN;
 expected_score BIGINT; remaining BIGINT; rank BIGINT:=0; reason TEXT; outcome TEXT;
 expected_layer TEXT; expected_version BIGINT; previous JSONB; current_row JSONB;
BEGIN
 IF p_mode='none' THEN
  IF p_q->'observation' IS DISTINCT FROM 'null'::jsonb OR p_q->'feed' IS DISTINCT FROM 'null'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  RETURN;
 END IF;
 IF p_max_bytes IS NULL OR p_max_bytes<=0 OR p_max_rows IS NULL OR p_max_rows<=0
  OR octet_length(p_canonical)>p_max_bytes THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_WITNESS_LIMIT';
 END IF;
 w:=p_canonical::jsonb;
 IF NOT public.business_exact_keys(w,ARRAY['schema_version','kind','evaluator_version','request_hash','input_digest','config_digest','observation','observation_digest','policy','policy_digest','translation','decisions','selected_refs'])
  OR w->'schema_version' IS DISTINCT FROM '1'::jsonb OR w->>'kind' IS DISTINCT FROM 'provider_selection_v1'
  OR w->>'evaluator_version' IS DISTINCT FROM 'evaluator-v1' OR w->'translation' IS DISTINCT FROM 'null'::jsonb
  OR w->>'request_hash' IS DISTINCT FROM p_request OR w->>'input_digest' IS DISTINCT FROM p_input
  OR w->>'config_digest' IS DISTINCT FROM p_config
  OR p_canonical IS DISTINCT FROM public.anime_config_candidate_canonical_json(w)
  OR p_digest IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-preparation-witness:v1','UTF8')||decode('00','hex')||convert_to(p_canonical,'UTF8')),'hex') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
 END IF;
 -- 132 already locked these exact parents. This read must never select latest.
 SELECT observation INTO locked FROM public.provider_group_snapshots WHERE id=p_q->'observation'->>'snapshot_ref';
 IF NOT FOUND OR w->'observation' IS DISTINCT FROM locked
  OR w->>'observation_digest' IS DISTINCT FROM p_q->'observation'->>'observation_digest'
  OR p_q->'feed'->'scope' IS DISTINCT FROM p_q->'observation'->'scope' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
 END IF;
 policy:=p_business->'sections'->'subgroups';
 policy:=policy||jsonb_build_object('name_keywords',COALESCE(policy->'name_keywords','null'::jsonb),'provider_defaults',COALESCE(policy->'provider_defaults','null'::jsonb));
 FOREACH provider IN ARRAY ARRAY['name_keywords','provider_defaults'] LOOP
  IF jsonb_typeof(policy->provider)='object' THEN
   policy:=jsonb_set(policy,ARRAY[provider],jsonb_build_object('anibt',COALESCE(policy->provider->'anibt','null'::jsonb),'mikan',COALESCE(policy->provider->'mikan','null'::jsonb)));
  END IF;
 END LOOP;
 IF w->'policy' IS DISTINCT FROM policy
  OR w->>'policy_digest' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-group-policy:v1','UTF8')||decode('00','hex')||convert_to(public.anime_config_candidate_canonical_json(policy),'UTF8')),'hex')
  OR jsonb_typeof(w->'decisions') IS DISTINCT FROM 'array' OR jsonb_typeof(w->'selected_refs') IS DISTINCT FROM 'array' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
 END IF;
 provider:=locked->'scope'->>'provider';
 SELECT COALESCE(array_agg(ref ORDER BY ref COLLATE "C"),ARRAY[]::TEXT[]) INTO refs FROM (
  SELECT observed_group.value->>'group_ref' ref FROM jsonb_array_elements(locked->'groups') AS observed_group(value)
  UNION SELECT e->>'group_ref' FROM jsonb_array_elements(policy->'entries') e WHERE e->>'provider'=provider
  UNION SELECT x #>> '{}' FROM jsonb_array_elements(COALESCE(NULLIF(policy->'provider_defaults'->provider->'groups','null'::jsonb),'[]'::jsonb)) x
 ) all_refs;
 IF cardinality(refs)>p_max_rows THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_WITNESS_LIMIT'; END IF;
 SELECT COALESCE(array_agg(x->>'group_ref' ORDER BY (x->>'group_ref') COLLATE "C"),ARRAY[]::TEXT[]) INTO decision_refs FROM jsonb_array_elements(w->'decisions') x;
 IF decision_refs IS DISTINCT FROM refs THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH'; END IF;
 remaining:=(policy->>'max_rss')::bigint;
 FOR d IN SELECT value FROM jsonb_array_elements(w->'decisions') LOOP
  IF NOT public.business_exact_keys(d,ARRAY['group_ref','display_name','available','canonical_key','mode','score','policy_origin','rank','outcome','reason_code'])
   OR NOT public.business_exact_keys(d->'policy_origin',ARRAY['layer','config_version'])
   OR NOT public.business_exact_keys(d->'display_name',ARRAY['state','value','reason_code'])
   OR d->'canonical_key' IS DISTINCT FROM '{"state":"absent","value":null,"reason_code":null}'::jsonb
   OR jsonb_typeof(d->'available') IS DISTINCT FROM 'boolean'
   OR jsonb_typeof(d->'score') IS DISTINCT FROM 'number' OR d->>'score' !~ '^-?(0|[1-9][0-9]*)$'
   OR d->>'mode' NOT IN ('always','ranked','never') THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  SELECT value INTO g FROM jsonb_array_elements(locked->'groups') WHERE value->>'group_ref'=d->>'group_ref';
  observed:=FOUND AND g->'available'->'value'='true'::jsonb;
  SELECT value INTO entry FROM jsonb_array_elements(policy->'entries') WHERE value->>'provider'=provider AND value->>'group_ref'=d->>'group_ref';
  IF d->'available' IS DISTINCT FROM to_jsonb(observed)
   OR (g IS NOT NULL AND d->'display_name' IS DISTINCT FROM jsonb_build_object('state','known','value',g->>'display_name','reason_code',NULL))
   OR (g IS NULL AND d->'display_name' IS DISTINCT FROM CASE WHEN entry IS NULL THEN '{"state":"absent","value":null,"reason_code":null}'::jsonb ELSE jsonb_build_object('state','known','value',entry->>'display_name','reason_code',NULL) END) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  expected_layer:='provider_default';expected_version:=1;
  IF entry IS NOT NULL THEN
   expected_layer:=CASE WHEN p_q->'config'->>'kind'='submitted' THEN 'submitted_exact' ELSE 'frozen_exact' END;
   expected_version:=(p_business->'section_versions'->>'subgroups')::bigint;
   expected_mode:=CASE WHEN entry->>'selection_mode'='auto' THEN 'ranked' WHEN entry->'manual_selected'='true'::jsonb THEN 'always' ELSE 'never' END;
   expected_score:=CASE expected_mode WHEN 'ranked' THEN (entry->>'score')::bigint WHEN 'always' THEN 100 ELSE 0 END;
  ELSE
   expected_mode:=CASE WHEN policy->'provider_defaults'->provider->>'mode'='whitelist' THEN 'never' ELSE 'ranked' END;
   expected_score:=CASE WHEN expected_mode='never' THEN 0 ELSE (d->>'score')::bigint END;
   IF jsonb_typeof(policy->'provider_defaults'->provider)='object' THEN
    expected_version:=(p_business->'section_versions'->>'subgroups')::bigint;
    IF (policy->'provider_defaults'->provider->'groups') ? (d->>'group_ref') THEN
     expected_layer:=CASE WHEN p_q->'config'->>'kind'='submitted' THEN 'submitted_exact' ELSE 'frozen_exact' END;
     expected_mode:=CASE WHEN policy->'provider_defaults'->provider->>'mode'='whitelist' THEN 'ranked' ELSE 'never' END;
     expected_score:=CASE WHEN expected_mode='ranked' THEN (policy->'provider_defaults'->provider->>'score')::bigint ELSE 0 END;
    END IF;
   END IF;
  END IF;
  IF d->>'mode' IS DISTINCT FROM expected_mode OR (d->>'score')::bigint IS DISTINCT FROM expected_score
   OR d->'policy_origin' IS DISTINCT FROM jsonb_build_object('layer',expected_layer,'config_version',expected_version) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  current_row:=jsonb_build_object('availability',CASE WHEN observed THEN 0 ELSE 1 END,
   'mode_order',CASE expected_mode WHEN 'always' THEN 0 WHEN 'ranked' THEN 1 ELSE 2 END,
   'score_order',CASE WHEN expected_mode='ranked' THEN -(d->>'score')::numeric ELSE 0 END,'ref',d->>'group_ref');
  IF previous IS NOT NULL AND ((previous->>'availability')::integer,(previous->>'mode_order')::integer,(previous->>'score_order')::numeric,previous->>'ref' COLLATE "C") >= ((current_row->>'availability')::integer,(current_row->>'mode_order')::integer,(current_row->>'score_order')::numeric,current_row->>'ref' COLLATE "C") THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  previous:=current_row;
  IF NOT observed THEN outcome:='unavailable';reason:='provider_group_not_observed';
  ELSIF expected_mode='never' THEN outcome:='not_selected';reason:='policy_never';
  ELSIF expected_mode='ranked' AND expected_score<0 THEN outcome:='not_selected';reason:='policy_score_negative';
  ELSIF expected_mode='always' THEN outcome:='selected';reason:='policy_always';
  ELSE rank:=rank+1;IF remaining>0 THEN outcome:='selected';reason:='ranked_within_max_rss';remaining:=remaining-1;ELSE outcome:='not_selected';reason:='max_rss_exhausted';END IF;
  END IF;
  IF d->>'outcome' IS DISTINCT FROM outcome OR d->>'reason_code' IS DISTINCT FROM reason
   OR d->'rank' IS DISTINCT FROM (CASE WHEN observed AND expected_mode='ranked' AND expected_score>=0 THEN to_jsonb(rank) ELSE 'null'::jsonb END) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
  END IF;
  IF outcome='selected' THEN actual_selected:=array_append(actual_selected,d->>'group_ref');END IF;
 END LOOP;
 SELECT COALESCE(array_agg(x ORDER BY x COLLATE "C"),ARRAY[]::TEXT[]) INTO actual_selected FROM unnest(actual_selected) x;
 SELECT COALESCE(array_agg(value #>> '{}' ORDER BY ordinal),ARRAY[]::TEXT[]) INTO selected FROM jsonb_array_elements(w->'selected_refs') WITH ORDINALITY a(value,ordinal);
 IF selected IS DISTINCT FROM actual_selected OR NOT (p_q->'feed'->>'group_ref'=ANY(selected)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_ACCEPT_PREPARATION_MISMATCH';
 END IF;
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
        receipt_until, detail_until, state, terminal_at, terminal_schema, terminal_receipt,
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
        v_now + p_receipt_after_accept_seconds * INTERVAL '1 second', NULL, 'running', NULL, NULL, NULL,
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
