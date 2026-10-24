-- Bansoko schema baseline.
--
-- Builds the complete schema from an empty database. The migration runner
-- applies this file first, records its checksum in schema_migrations and then
-- applies later forward migrations (002_*.sql, ...) in file-name order. This
-- file is immutable once released: schema changes are new forward migrations.
--
-- Layout: one section per domain. Each section declares its tables (with their
-- constraints, indexes and comments), seeds, functions, triggers and the
-- runtime role's privileges. Foreign keys to a table declared in a later
-- section are added right after that table.
--
-- Privileges: the schema owner (the migration role) owns every object. The
-- runtime connects as a login role that is only a member of the NOLOGIN group
-- role bansoko_runtime, so it cannot run DDL, alter triggers or write
-- owner-authored evidence. Functions marked owner-only have EXECUTE revoked
-- from PUBLIC and are reachable only through owner-held SECURITY DEFINER
-- entry points or triggers.

-- ============================================================================
-- Schema identity
-- The runner creates schema_migrations (the checksum ledger) before this file
-- runs; schema_identity pins the database to this baseline so a database built
-- by any other migration history is refused instead of upgraded.
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bansoko_runtime') THEN
        CREATE ROLE bansoko_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END
$$;

ALTER ROLE bansoko_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;

-- Only the schema owner may create objects in public, so the fixed search_path
-- of the SECURITY DEFINER functions cannot be shadowed.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO bansoko_runtime;

-- Migration bookkeeping is owner-authored; the runtime only verifies it.
GRANT SELECT ON TABLE schema_migrations TO bansoko_runtime;

CREATE TABLE schema_identity (
    singleton_id SMALLINT NOT NULL,
    identity TEXT NOT NULL,
    baseline_version TEXT NOT NULL,
    installed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT schema_identity_pkey PRIMARY KEY (singleton_id),
    CONSTRAINT schema_identity_baseline_version_check CHECK (baseline_version = '001_baseline.sql'),
    CONSTRAINT schema_identity_identity_check CHECK (identity = 'bansoko:baseline'),
    CONSTRAINT schema_identity_singleton_id_check CHECK (singleton_id = 1)
);

COMMENT ON TABLE schema_identity IS 'Pins the database to the bansoko:baseline schema; a database built by any other migration history is refused, not upgraded.';

INSERT INTO schema_identity (singleton_id, identity, baseline_version)
VALUES (1, 'bansoko:baseline', '001_baseline.sql');

GRANT SELECT ON TABLE schema_identity TO bansoko_runtime;

-- ============================================================================
-- Shared helper functions
-- Pure helpers (validators, canonical digests, JSON shape checks) that the
-- constraints and functions of every later section call.
-- ============================================================================

CREATE FUNCTION acquisition_terminal_time_text(p_at timestamp with time zone) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT to_char(p_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
$$;

CREATE FUNCTION anime_config_candidate_canonical_json(input_json jsonb) RETURNS text
    LANGUAGE plpgsql IMMUTABLE STRICT
    AS $$
DECLARE
    item RECORD;
    result TEXT;
    first_item BOOLEAN := TRUE;
BEGIN
    CASE jsonb_typeof(input_json)
        WHEN 'object' THEN
            result := '{';
            FOR item IN
                SELECT object_item.key AS item_key, object_item.value AS item_value
                FROM jsonb_each(input_json) AS object_item(key, value)
                ORDER BY object_item.key COLLATE "C"
            LOOP
                IF NOT first_item THEN
                    result := result || ',';
                END IF;
                result := result || to_json(item.item_key)::TEXT || ':'
                    || anime_config_candidate_canonical_json(item.item_value);
                first_item := FALSE;
            END LOOP;
            RETURN result || '}';
        WHEN 'array' THEN
            result := '[';
            FOR item IN
                SELECT array_item.value AS item_value
                FROM jsonb_array_elements(input_json) WITH ORDINALITY AS array_item(value, ordinal)
                ORDER BY array_item.ordinal
            LOOP
                IF NOT first_item THEN
                    result := result || ',';
                END IF;
                result := result || anime_config_candidate_canonical_json(item.item_value);
                first_item := FALSE;
            END LOOP;
            RETURN result || ']';
        WHEN 'string' THEN
            RETURN to_json(input_json #>> '{}')::TEXT;
        ELSE
            RETURN input_json::TEXT;
    END CASE;
END;
$$;

CREATE FUNCTION business_exact_keys(value jsonb, keys text[]) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT COALESCE(jsonb_typeof(value) = 'object' AND value ?& keys
   AND (SELECT count(*) FROM jsonb_object_keys(value)) = cardinality(keys), FALSE)
$$;

CREATE FUNCTION anime_config_candidate_draft_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
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
 IF NOT business_exact_keys(b,ARRAY['schema_version','sections','section_versions','section_schemas','parser','retention','retention_version','unclassified_route','file_effect_version','programs']) THEN RETURN FALSE; END IF;
 IF b->>'schema_version' IS DISTINCT FROM 'business-snapshot-v1'
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
    OR NOT business_exact_keys(b->'retention',ARRAY['admission_mode','quality_profile','cleanup_policy'])
    OR b->'retention'->>'admission_mode' NOT IN ('all','per_group','single_copy')
    OR NOT retention_quality_profile_valid(b->'retention'->'quality_profile')
    OR (b->'retention') ? 'cutoff'::text
    OR NOT retention_cleanup_policy_valid(b->'retention'->'cleanup_policy')
    OR b->>'unclassified_route' NOT IN ('route','skip') THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['retention_version','file_effect_version'] LOOP
  IF jsonb_typeof(b->name) IS DISTINCT FROM 'number' OR (b->>name)::numeric < 0 OR (b->>name)::numeric <> floor((b->>name)::numeric) THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'retention'->'quality_profile',ARRAY['version','items'])
    OR NOT business_exact_keys(b->'retention'->'cleanup_policy',ARRAY['version','effect','grace_seconds']) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['release_rules','file_rules'] LOOP
  IF NOT business_exact_keys(b->'sections'->name,ARRAY['must_contain','not_in'])
     OR jsonb_typeof(b->'sections'->name->'must_contain') IS DISTINCT FROM 'string'
     OR jsonb_typeof(b->'sections'->name->'not_in') IS DISTINCT FROM 'string' THEN RETURN FALSE; END IF;
 END LOOP;
 IF NOT business_exact_keys(b->'programs',ARRAY['attrrules','vars']) THEN RETURN FALSE; END IF;
 FOREACH name IN ARRAY ARRAY['attrrules','vars'] LOOP
  ref := b->'programs'->name;
  IF NOT business_exact_keys(ref,ARRAY['version','digest'])
     OR jsonb_typeof(ref->'version') IS DISTINCT FROM 'string'
     OR jsonb_typeof(ref->'digest') IS DISTINCT FROM 'string'
     OR ref->>'digest' !~ '^sha256:[a-f0-9]{64}$' THEN RETURN FALSE; END IF;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN RETURN FALSE;
END;
$_$;

CREATE FUNCTION delivery_replacements(p_delivery jsonb) RETURNS SETOF jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT r.value FROM jsonb_array_elements(CASE
   WHEN jsonb_typeof(p_delivery->'replacements') = 'array' THEN p_delivery->'replacements'
   WHEN p_delivery->'replacement'->>'state' = 'known' THEN jsonb_build_array(p_delivery->'replacement'->'value')
   ELSE '[]'::jsonb END) AS r(value)
$$;

CREATE FUNCTION download_failure_category(p_code text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE
  WHEN p_code IN ('DOWNLOAD_STALLED','DOWNLOAD_PROGRESS_UNVERIFIABLE') THEN 'stalled'
  WHEN p_code IN ('SYNC_BUDGET_EXHAUSTED','SUBMIT_BUDGET_EXHAUSTED') THEN 'timeout'
  WHEN p_code IN ('qb_provider_failed','qb_sync_failed') THEN 'client'
  WHEN p_code IN ('SYNC_ERROR_BUDGET_EXHAUSTED','DOWNLOAD_OBSERVATION_UNAVAILABLE') THEN 'unobservable'
  ELSE 'submit' END
$$;

CREATE FUNCTION download_merge_work_input(p_old jsonb, p_new jsonb) RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF p_old ? 'sync_ticket' THEN
        IF (p_new ? 'sync_ticket' AND p_new->'sync_ticket' IS DISTINCT FROM p_old->'sync_ticket')
            OR (p_new ? 'poll_ordinal' AND p_new->'poll_ordinal' IS DISTINCT FROM p_old->'poll_ordinal') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_WORK_MERGE_TICKET_CONFLICT';
        END IF;
        RETURN p_new || jsonb_build_object('sync_ticket',p_old->'sync_ticket','poll_ordinal',p_old->'poll_ordinal');
    END IF;
    RETURN p_new;
END;
$$;

CREATE FUNCTION feed_json_string(value text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT replace(replace(replace(replace(replace(to_json(value)::text,
        '&', chr(92)||'u0026'), '<', chr(92)||'u003c'), '>', chr(92)||'u003e'), chr(8232), chr(92)||'u2028'), chr(8233), chr(92)||'u2029');
$$;

CREATE FUNCTION feed_file_canonical(file jsonb) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE result TEXT;
BEGIN
    result := '{"file_ref":' || public.feed_json_string(file->>'file_ref')
        || ',"source_path":' || public.feed_json_string(file->>'source_path')
        || ',"filename":' || public.feed_json_string(file->>'filename')
        || ',"size_bytes":' || (file->>'size_bytes')
        || ',"media_kind":' || public.feed_json_string(file->>'media_kind');
    IF COALESCE(file->>'language','') <> '' THEN result := result || ',"language":' || public.feed_json_string(file->>'language'); END IF;
    RETURN result || '}';
END;
$$;

CREATE FUNCTION feed_time_text(value timestamp with time zone) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT to_char(value AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS')
        || CASE WHEN rtrim(to_char(value AT TIME ZONE 'UTC','US'),'0')='' THEN ''
            ELSE '.' || rtrim(to_char(value AT TIME ZONE 'UTC','US'),'0') END || 'Z';
$$;

CREATE FUNCTION go_whitespace_class() RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT '\t\n\v\f\r ' || chr(133) || chr(160) || chr(5760)
        || chr(8192) || '-' || chr(8202)
        || chr(8232) || chr(8233) || chr(8239) || chr(8287) || chr(12288)
$$;

CREATE FUNCTION text_trim_go_ws(input_text text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    AS $_$
    SELECT regexp_replace(
        input_text,
        '^[' || go_whitespace_class() || ']+|[' || go_whitespace_class() || ']+$',
        '',
        'g'
    )
$_$;

CREATE FUNCTION feed_seed_canonical(seed jsonb, projection_digest text) RETURNS text
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
    RETURN result || '}';
END;
$$;

CREATE FUNCTION observed_closed(p_value jsonb, p_keys text[]) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT (jsonb_typeof(p_value)='object' AND p_value ?& p_keys AND (SELECT count(*) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(p_value)='object' THEN p_value ELSE '{}'::jsonb END))=cardinality(p_keys)) IS TRUE
$$;

CREATE FUNCTION group_observation_entries_ok(p_entries jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 -- A CHECK constraint cannot hold a subquery, so the per-element shape of the
 -- group snapshot's entries projection lives here. It reads nothing but its
 -- own argument.
 SELECT jsonb_typeof(p_entries)='array' AND pg_column_size(p_entries)<=16777216
   AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(p_entries) e
     WHERE jsonb_typeof(e) IS DISTINCT FROM 'object'
        OR jsonb_typeof(e->'group_ref') IS DISTINCT FROM 'string'
        OR octet_length(e->>'group_ref') NOT BETWEEN 1 AND 256
        OR jsonb_typeof(e->'display_name') IS DISTINCT FROM 'string'
        OR octet_length(e->>'display_name') NOT BETWEEN 1 AND 256
        OR jsonb_typeof(e->'feed_locator') IS DISTINCT FROM 'object'
        OR pg_column_size(e->'feed_locator')>16384
        OR jsonb_typeof(e->'observed_at') IS DISTINCT FROM 'string');
$$;
CREATE FUNCTION file_location_valid(loc jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE root text; rel text; ns text;
BEGIN
 IF public.observed_closed(loc,ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE THEN RETURN false; END IF;
 root:=loc->>'root';rel:=loc->>'relative_path';
 IF (root ~ '^/' AND rel<>'' AND rel !~ '(^/|(^|/)\.\.?(/|$)|//|[[:cntrl:]])'
  AND root !~ '(^|/)\.\.?(/|$)|//|[[:cntrl:]]' AND (root='/' OR right(root,1)<>'/')
  AND loc->>'canonical_path'=(CASE WHEN root='/' THEN root||rel ELSE root||'/'||rel END)) IS NOT TRUE THEN RETURN false; END IF;
 -- JSON encoding of the namespace contains only these two ordered keys.
 ns:='{"adapter_version":"local-stable-read-v1","root":'||to_json(root)::text||'}';
 RETURN loc->>'namespace_digest'='sha256:'||encode(sha256(convert_to('local-filesystem-namespace:v1','UTF8')||decode('00','hex')||convert_to(ns,'UTF8')),'hex');
END $_$;

CREATE FUNCTION file_wire_fv_valid(v jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
 SELECT public.observed_closed(v,ARRAY['state','value','reason_code']) AND CASE v->>'state'
 WHEN 'known' THEN v->'value'<>'null'::jsonb AND v->'reason_code'='null'::jsonb
 WHEN 'absent' THEN v->'value'='null'::jsonb AND v->'reason_code'='null'::jsonb
 WHEN 'unavailable' THEN v->'value'='null'::jsonb AND jsonb_typeof(v->'reason_code')='string' AND (v->>'reason_code') ~ '^[A-Z][A-Z0-9_]{0,127}$'
 ELSE false END IS TRUE
$_$;

CREATE FUNCTION file_wire_i64_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
 IF jsonb_typeof(v) IS DISTINCT FROM 'number' OR (v#>>'{}') !~ '^-?(0|[1-9][0-9]*)$' THEN RETURN false; END IF;
 RETURN (v#>>'{}')::numeric BETWEEN -9223372036854775808 AND 9223372036854775807;
END $_$;

CREATE FUNCTION file_wire_handle_valid(v jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT public.observed_closed(v,ARRAY['mount_id','handle_type','opaque','encoding'])
 AND public.file_wire_i64_valid(v->'mount_id') AND public.file_wire_i64_valid(v->'handle_type')
 AND jsonb_typeof(v->'opaque')='string' AND octet_length(v->>'opaque')<=8192
 AND v->>'encoding'='base64'
$$;

CREATE FUNCTION file_wire_u64_valid(v jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
 SELECT COALESCE(jsonb_typeof(v)='string' AND (v#>>'{}') ~ '^(0|[1-9][0-9]{0,19})$'
 AND (length(v#>>'{}')<20 OR (v#>>'{}') COLLATE "C" <= '18446744073709551615' COLLATE "C"),false)
$_$;

CREATE FUNCTION file_wire_identity_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE k text; fv jsonb;
BEGIN
 IF public.observed_closed(v,ARRAY['kind','mount_ref','device','inode','generation_nanos','change_nanos','handle']) IS NOT TRUE
 OR v->>'kind' IS DISTINCT FROM 'local_fs' OR public.file_wire_u64_valid(v->'device') IS NOT TRUE
 OR public.file_wire_u64_valid(v->'inode') IS NOT TRUE THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['mount_ref','generation_nanos','change_nanos','handle'] LOOP
 fv:=v->k;
 IF public.file_wire_fv_valid(fv) IS NOT TRUE THEN RETURN false; END IF;
 IF fv->>'state'='known' THEN
 IF k='mount_ref' AND (jsonb_typeof(fv->'value') IS DISTINCT FROM 'string' OR (octet_length(fv->>'value') BETWEEN 1 AND 256) IS NOT TRUE) THEN RETURN false; END IF;
 IF k IN ('generation_nanos','change_nanos') AND public.file_wire_i64_valid(fv->'value') IS NOT TRUE THEN RETURN false; END IF;
 IF k='handle' AND public.file_wire_handle_valid(fv->'value') IS NOT TRUE THEN RETURN false; END IF;
 END IF;
 END LOOP;
 RETURN true;
END $$;

CREATE FUNCTION file_wire_time_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE s text; t timestamptz;
BEGIN
 IF jsonb_typeof(v) IS DISTINCT FROM 'string' THEN RETURN false; END IF;
 s:=v#>>'{}';
 IF s !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{0,5}[1-9])?Z$' THEN RETURN false; END IF;
 IF substring(s,12,2)::int>23 OR substring(s,15,2)::int>59 OR substring(s,18,2)::int>59 THEN RETURN false; END IF;
 BEGIN t:=s::timestamptz; EXCEPTION WHEN invalid_datetime_format OR datetime_field_overflow THEN RETURN false; END;
 RETURN to_char(t AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS')=left(s,19);
END $_$;

CREATE FUNCTION file_final_object_valid(v jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE c jsonb; k text;
BEGIN
 IF public.observed_closed(v,ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE OR v->>'file_type' IS DISTINCT FROM 'regular' OR public.file_location_valid(v->'location') IS NOT TRUE OR public.file_wire_i64_valid(v->'size') IS NOT TRUE OR (v->>'size')::numeric<0 OR public.file_wire_time_valid(v->'observed_at') IS NOT TRUE THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['identity','content'] LOOP
 IF public.file_wire_fv_valid(v->k) IS NOT TRUE OR v->k->>'state' IS DISTINCT FROM 'known' THEN RETURN false; END IF;
 END LOOP;
 IF public.file_wire_identity_valid(v->'identity'->'value') IS NOT TRUE THEN RETURN false; END IF;
 c:=v->'content'->'value';
 IF public.observed_closed(c,ARRAY['kind','sha256','size','object_identity','read_started_at','read_finished_at','verifier_version']) IS NOT TRUE OR c->>'kind' IS DISTINCT FROM 'sha256_file' OR jsonb_typeof(c->'sha256') IS DISTINCT FROM 'string' OR (c->>'sha256') !~ '^sha256:[0-9a-f]{64}$' OR c->'size' IS DISTINCT FROM v->'size' OR c->'object_identity' IS DISTINCT FROM v->'identity'->'value' OR (c->>'verifier_version' IN ('local-stable-read-v1','local-exact-path-read-v1')) IS NOT TRUE OR v->>'observation_source' IS DISTINCT FROM c->>'verifier_version' OR public.file_wire_time_valid(c->'read_started_at') IS NOT TRUE OR public.file_wire_time_valid(c->'read_finished_at') IS NOT TRUE THEN RETURN false; END IF;
 RETURN ((c->>'read_started_at')::timestamptz<=(c->>'read_finished_at')::timestamptz AND c->>'read_finished_at'=v->>'observed_at') IS TRUE;
END $_$;

CREATE FUNCTION file_finish_cancelled_success(p_operation text, p_work text, p_token bigint, p_permit text, p_file_fact text, p_notification text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 PERFORM public.file_finalization_cancelled_gate(p_operation,p_work,p_token,p_permit);
 RETURN public.file_success_atomic_body(p_operation,p_work,p_permit,p_file_fact,p_notification);
END $$;

CREATE FUNCTION file_finish_success(p_operation text, p_work text, p_token bigint, p_permit text, p_file_fact text, p_notification text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 PERFORM public.file_finalization_normal_gate(p_operation,p_work,p_token,p_permit);
 RETURN public.file_success_atomic_body(p_operation,p_work,p_permit,p_file_fact,p_notification);
END $$;

CREATE FUNCTION file_finish_runtime_entry(p_operation text, p_work text, p_token bigint, p_permit text, p_file_fact text, p_notification text, p_cancelled boolean) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF p_cancelled THEN
  RETURN public.file_finish_cancelled_success(p_operation,p_work,p_token,p_permit,p_file_fact,p_notification);
 END IF;
 RETURN public.file_finish_success(p_operation,p_work,p_token,p_permit,p_file_fact,p_notification);
END $$;

CREATE FUNCTION file_json_field(p_json json, p_key text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT value::text FROM json_each(p_json) WHERE key=p_key
$$;

CREATE FUNCTION file_notification_safe(p_value jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE member record; child jsonb;
BEGIN
 CASE jsonb_typeof(p_value)
 WHEN 'object' THEN
  FOR member IN SELECT key,value FROM jsonb_each(p_value) LOOP
   IF lower(member.key) ~ '(secret|token|password|credential|url)' OR NOT public.file_notification_safe(member.value) THEN RETURN false; END IF;
  END LOOP;
 WHEN 'array' THEN
  FOR child IN SELECT value FROM jsonb_array_elements(p_value) LOOP
   IF NOT public.file_notification_safe(child) THEN RETURN false; END IF;
  END LOOP;
 WHEN 'string' THEN IF strpos(lower(p_value #>> '{}'),'://')>0 THEN RETURN false; END IF;
 ELSE NULL;
 END CASE;
 RETURN true;
END $$;

CREATE FUNCTION file_notification_text(p_text text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE value text;
BEGIN
 IF p_text IS NULL THEN RETURN NULL; END IF;
 value:=btrim(p_text, E' \t\n\r\f\v'||chr(133)||chr(160)||chr(5760)||chr(8192)||chr(8193)||chr(8194)||chr(8195)||chr(8196)||chr(8197)||chr(8198)||chr(8199)||chr(8200)||chr(8201)||chr(8202)||chr(8232)||chr(8233)||chr(8239)||chr(8287)||chr(12288));
 IF value='' OR octet_length(value)>512 THEN RETURN NULL; END IF;
 value:=btrim(regexp_replace(value,'[\x01-\x1f\x7f-\x9f]',' ','g'));
 IF value ~* '(^|[^a-z0-9])(api[_-]?key|apikey|passkey|cookie|credentials?|private[_-]?key|password|token|secret|authorization|access[_-]?token|client[_-]?secret|access[_-]?key|secret[_-]?key|dsn)["'']?[[:space:]]*[:=]'
 OR value ~* '\m(bearer|token|secret|password|authorization|dsn)\M'
 OR value ~* '[a-z][a-z0-9+.-]*://' OR value ~* 'magnet:'
 OR value ~ '(^|[ 　"''`=:,;|<>(\[{（：「『，；])/((home|Users|root|tmp|var|opt|etc|mnt|media|srv|data|downloads|library|Library|private|Volumes|volume[0-9]*|usr|proc|dev|run|nas|share|app|config|workspace)([^0-9A-Za-z_]|$)|[^ /]+/)'
 OR value ~* '[a-z]:[\\/]' OR value ~ E'\\\\\\\\[[:graph:]]'
 OR value ~ '(^|[^a-zA-Z0-9_])(~|\.{1,2})/[[:graph:]]'
 OR lower(value) LIKE '%payload%' OR lower(value) LIKE '%panic:%'
 OR lower(value) LIKE '%/users/%' OR lower(value) LIKE '%/home/%' THEN value:='<redacted>'; END IF;
 IF value='' OR octet_length(value)>512 OR left(value,1)='/' OR strpos(value,'://')>0
 OR value ~* '(password|passwd|token|secret|credential|authorization|cookie)='
 OR value ~ '[\x01-\x1f\x7f-\x9f]' THEN RETURN NULL; END IF;
 RETURN value;
END $_$;

CREATE FUNCTION file_proof_digest(p_domain text, p_bytes text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT 'sha256:'||encode(sha256(convert_to(p_domain,'UTF8')||decode('00','hex')||convert_to(p_bytes,'UTF8')),'hex')
$$;

CREATE FUNCTION file_version_candidate(p_base text, p_ordinal bigint) RETURNS text
    LANGUAGE plpgsql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE leaf text; extension text; base text;
BEGIN
 IF p_ordinal NOT BETWEEN 1 AND 1001 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_VERSION_ORDINAL_INVALID'; END IF;
 IF p_ordinal=1 THEN RETURN p_base; END IF;
 leaf:=regexp_replace(p_base,'^.*/','');
 extension:=COALESCE(substring(leaf from '(\.[^.]*)$'),'');
 base:=left(p_base,char_length(p_base)-char_length(extension));
 RETURN base||' ('||p_ordinal::text||')'||extension;
END $_$;

CREATE FUNCTION file_wire_id_valid(v jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
 SELECT COALESCE(jsonb_typeof(v)='string' AND octet_length(v#>>'{}') BETWEEN 1 AND 128
 AND (v#>>'{}') !~ '[[:cntrl:]]' AND (v#>>'{}') !~ '^[[:space:]]|[[:space:]]$',false)
$_$;

CREATE FUNCTION group_observation_value_ok(v jsonb, allow_absent boolean) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
    SELECT COALESCE(public.business_exact_keys(v, ARRAY['state','value','reason_code']) AND (
        (v->>'state' = 'known' AND v->'value' <> 'null'::jsonb AND v->'reason_code' = 'null'::jsonb)
        OR (allow_absent AND v->>'state' = 'absent' AND v->'value' = 'null'::jsonb AND v->'reason_code' = 'null'::jsonb)
        OR (v->>'state' = 'unavailable' AND v->'value' = 'null'::jsonb
            AND v->>'reason_code' = 'OBSERVATION_EVIDENCE_UNAVAILABLE')), FALSE);
$$;

CREATE FUNCTION notification_known(p_fv jsonb, p_path text[]) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN p_fv->>'state'='known'
  AND ((jsonb_typeof(p_fv->'value' #> p_path)='string' AND p_fv->'value' #>> p_path<>'')
   OR jsonb_typeof(p_fv->'value' #> p_path)='number')
 THEN p_fv->'value' #> p_path ELSE 'null'::jsonb END
$$;

CREATE FUNCTION notification_episode_facts(p_identity jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN p_identity->>'state'='known' AND p_identity->'value'->>'state'='resolved'
  AND public.notification_known(p_identity->'value'->'episode','{}')<>'null'::jsonb
 THEN jsonb_build_object(
  'episode.raw',public.notification_known(p_identity->'value'->'raw_episode','{}'),
  'episode.number',public.notification_known(p_identity->'value'->'episode','{}'),
  'episode.display',public.notification_known(p_identity->'value'->'episode','{}'),
  'episode.offset',CASE WHEN jsonb_typeof(p_identity->'value'->'offset')='number' THEN p_identity->'value'->'offset' ELSE 'null'::jsonb END)
 ELSE jsonb_build_object('episode.raw',NULL,'episode.number',NULL,'episode.display',NULL,'episode.offset',NULL) END
$$;

CREATE FUNCTION notification_episode_title_facts(p_render jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.ep_title',public.file_notification_text(CASE WHEN p_render->'episode_titles'->'bgm'->>'state'='known' THEN p_render->'episode_titles'->'bgm'->>'value' END),
 'tmdb.ep_title',public.file_notification_text(CASE WHEN p_render->'episode_titles'->'tmdb'->>'state'='known' THEN p_render->'episode_titles'->'tmdb'->>'value' END))
$$;

CREATE FUNCTION notification_input_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'anime.id',public.notification_known(p_input->'config'->'identity'->'managed_anime_ref','{}'),
 'provider.kind',public.notification_known(p_input->'source'->'provider','{}'),
 'bangumi.id',public.notification_known(p_input->'media'->'bgm','{ref}'),
 'tmdb.id',public.notification_known(p_input->'media'->'tmdb','{work_ref}'),
 'tmdb.media_kind',public.notification_known(p_input->'media'->'tmdb','{kind}'),
 'tmdb.season',public.notification_known(p_input->'media'->'tmdb','{season}'),
 'torrent.info_hash_v1',public.notification_known(p_input->'source'->'infohash_v1','{}'),
 'torrent.info_hash_v2',public.notification_known(p_input->'source'->'infohash_v2','{}'),
 'torrent.size',public.notification_known(p_input->'source'->'torrent_size','{}'),
 'torrent.file_count',public.notification_known(p_input->'source'->'torrent_file_count','{}'))
$$;

CREATE FUNCTION notification_join_values(p_values text[]) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT NULLIF(string_agg(d.v,'+' ORDER BY lower(d.v) COLLATE "C"),'')
 FROM (SELECT DISTINCT ON (lower(s.v)) s.v FROM (
  SELECT btrim(u.x, E' \t\n\r\f\v'||chr(133)||chr(160)||chr(5760)||chr(8192)||chr(8193)||chr(8194)||chr(8195)||chr(8196)||chr(8197)||chr(8198)||chr(8199)||chr(8200)||chr(8201)||chr(8202)||chr(8232)||chr(8233)||chr(8239)||chr(8287)||chr(12288)) AS v, u.ord
  FROM unnest(p_values) WITH ORDINALITY AS u(x,ord)) s
  WHERE s.v<>'' ORDER BY lower(s.v), s.ord) d
$$;

CREATE FUNCTION notification_query_escape(p_value text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
 SELECT COALESCE(string_agg(
   CASE WHEN c ~ '^[A-Za-z0-9_.~/-]$' THEN c
        ELSE regexp_replace(upper(encode(convert_to(c,'UTF8'),'hex')),'(..)','%\1','g') END,
   '' ORDER BY i),'')
 FROM regexp_split_to_table(p_value,'') WITH ORDINALITY AS t(c,i)
$_$;

CREATE FUNCTION notification_release_facts(p_input jsonb, p_release_id text) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
  'release.id',public.file_notification_text(COALESCE(p_release_id,
     CASE WHEN p_input->'source'->'release'->>'state'='known' THEN p_input->'source'->'release'->'value'->>'ref' END)),
  'release.title',public.file_notification_text(CASE WHEN p_input->'source'->'title'->>'state'='known' THEN p_input->'source'->'title'->>'value' END),
  'release.group_name',public.file_notification_text(CASE WHEN p_input->'source'->'group'->>'state'='known' THEN p_input->'source'->'group'->>'value' END))
$$;

CREATE FUNCTION notification_tag_block_valid(p_block jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT p_block IS NULL OR jsonb_typeof(p_block)='null' OR (jsonb_typeof(p_block)='object'
  AND NOT EXISTS (SELECT 1 FROM unnest(ARRAY['schema','implementation','config_digest','diagnostic']) k
   WHERE COALESCE(jsonb_typeof(p_block->k),'null') NOT IN ('null','string'))
  AND NOT EXISTS (SELECT 1 FROM unnest(ARRAY['tags','value_tags']) k
   WHERE COALESCE(jsonb_typeof(p_block->k),'null') NOT IN ('null','array')
    OR (jsonb_typeof(p_block->k)='array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(p_block->k) e WHERE jsonb_typeof(e) NOT IN ('null','string')))))
$$;

CREATE FUNCTION notification_tag_values(p_tags jsonb, p_dimension text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT public.notification_join_values(ARRAY(
  SELECT substr(t.tag,strpos(t.tag,':')+1) FROM jsonb_array_elements_text(p_tags) WITH ORDINALITY AS t(tag,ord)
  WHERE strpos(t.tag,':')>1 AND strpos(t.tag,':')<length(t.tag) AND left(t.tag,strpos(t.tag,':')-1)=p_dimension
  ORDER BY t.ord))
$$;

CREATE FUNCTION notification_title_facts(p_input jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT jsonb_build_object(
 'bgm.title',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title}') #>> '{}'),
 'bgm.title_cn',public.file_notification_text(public.notification_known(p_input->'media'->'bgm_titles','{title_cn}') #>> '{}'),
 'tmdb.title',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{title}') #>> '{}'),
 'tmdb.original_title',public.file_notification_text(public.notification_known(p_input->'media'->'tmdb_titles','{original_title}') #>> '{}'))
$$;

CREATE FUNCTION notification_value_facts(p_vars jsonb, p_source_facts jsonb) RETURNS jsonb
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
  'anibt.tag',public.file_notification_text(public.notification_tag_values(anibt_tags,'tag')));
END $$;

CREATE FUNCTION observed_identity_valid(p_value jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE k text; fv jsonb;
BEGIN
 IF public.observed_closed(p_value,ARRAY['kind','mount_ref','device','inode','generation_nanos','change_nanos','handle']) IS NOT TRUE
 OR p_value->>'kind' IS DISTINCT FROM 'local_fs' OR jsonb_typeof(p_value->'device') IS DISTINCT FROM 'string' OR jsonb_typeof(p_value->'inode') IS DISTINCT FROM 'string'
 OR (p_value->>'device' ~ '^(0|[1-9][0-9]{0,19})$') IS NOT TRUE OR (p_value->>'inode' ~ '^[1-9][0-9]{0,19}$') IS NOT TRUE
 OR (p_value->>'device')::numeric>18446744073709551615 OR (p_value->>'inode')::numeric>18446744073709551615 THEN RETURN false; END IF;
 FOREACH k IN ARRAY ARRAY['mount_ref','generation_nanos','change_nanos','handle'] LOOP
 fv:=p_value->k;
 IF public.observed_closed(fv,ARRAY['state','value','reason_code']) IS NOT TRUE THEN RETURN false; END IF;
 IF fv->>'state'='known' THEN
 IF fv->'reason_code' IS DISTINCT FROM 'null'::jsonb OR fv->'value'='null'::jsonb THEN RETURN false; END IF;
 IF k IN ('generation_nanos','change_nanos') AND (jsonb_typeof(fv->'value') IS DISTINCT FROM 'number' OR (fv->>'value' ~ '^-?(0|[1-9][0-9]*)$') IS NOT TRUE) THEN RETURN false; END IF;
 IF k IN ('generation_nanos','change_nanos') THEN PERFORM (fv->>'value')::bigint; END IF;
 IF k='mount_ref' AND (jsonb_typeof(fv->'value') IS DISTINCT FROM 'string' OR octet_length(fv->>'value') NOT BETWEEN 1 AND 256) THEN RETURN false; END IF;
 IF k='handle' THEN RETURN false; END IF; -- this local verifier emits no opaque handle
 ELSIF fv->>'state'='unavailable' THEN
 IF fv->'value' IS DISTINCT FROM 'null'::jsonb OR (fv->>'reason_code' ~ '^[A-Z][A-Z0-9_]{0,127}$') IS NOT TRUE THEN RETURN false; END IF;
 ELSIF fv->>'state'='absent' THEN
 IF fv->'value' IS DISTINCT FROM 'null'::jsonb OR fv->'reason_code' IS DISTINCT FROM 'null'::jsonb THEN RETURN false; END IF;
 ELSE RETURN false; END IF;
 END LOOP;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $_$;

CREATE FUNCTION plan_canonical_valid(p_text text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE value json; kind text; row record; previous_key text; stripped text;
BEGIN
 value:=p_text::json; kind:=json_typeof(value);
 -- Strip complete string tokens, then reject whitespace outside strings.
 stripped:=regexp_replace(p_text,'"([^"\\]|\\.)*"','""','g');
 IF stripped ~ '[[:space:]]' THEN RETURN false; END IF;
 FOR row IN SELECT token[1] AS token FROM regexp_matches(p_text,'("([^"\\]|\\.)*")','g') AS matches(token) LOOP
 IF row.token !~ '^"([^"\\[:cntrl:]]|\\["\\]|\\u00(0[1-9a-f]|1[0-9a-f]))*"$' THEN RETURN false; END IF;
 END LOOP;
 IF kind='object' THEN
 FOR row IN SELECT e.key,e.value AS child FROM json_each(value) e LOOP
 IF previous_key IS NOT NULL AND (row.key COLLATE "C") <= (previous_key COLLATE "C") THEN RETURN false; END IF;
 IF NOT public.plan_canonical_valid(row.child::text) THEN RETURN false; END IF;
 previous_key:=row.key;
 END LOOP;
 ELSIF kind='array' THEN
 FOR row IN SELECT child FROM json_array_elements(value) AS elements(child) LOOP
 IF NOT public.plan_canonical_valid(row.child::text) THEN RETURN false; END IF;
 END LOOP;
 ELSIF kind='string' THEN
 -- Only quotes/backslashes and lowercase control-byte escapes are canonical.
 -- Validate the original token; ordinary Unicode is literal, never \u escaped.
 IF p_text !~ '^"([^"\\[:cntrl:]]|\\["\\]|\\u00(0[1-9a-f]|1[0-9a-f]))*"$' THEN RETURN false; END IF;
 ELSIF kind='number' THEN
 IF p_text !~ '^(0|-[1-9][0-9]*|[1-9][0-9]*)$' THEN RETURN false; END IF;
 ELSIF kind='boolean' THEN
 IF p_text NOT IN ('true','false') THEN RETURN false; END IF;
 ELSIF kind='null' THEN
 IF p_text<>'null' THEN RETURN false; END IF;
 ELSE RETURN false;
 END IF;
 RETURN true;
EXCEPTION WHEN invalid_text_representation THEN RETURN false;
END $_$;

CREATE FUNCTION observed_satisfaction_valid(p_e jsonb, p_plan text, p_root text, p_artifact text, p_version bigint, p_ref text, p_input text, p_spec text, p_path text, p_size bigint, p_hash text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o jsonb; c jsonb; x jsonb; expected jsonb;
BEGIN
 IF jsonb_typeof(p_e) IS DISTINCT FROM 'object' OR NOT (p_e ?& ARRAY['canonical','digest','observation','final_object'])
 OR (SELECT count(*) FROM jsonb_object_keys(p_e))<>4 OR public.plan_canonical_valid(p_e->>'canonical') IS NOT TRUE THEN RETURN false; END IF;
 x:=(p_e->>'canonical')::jsonb;o:=x->'observed_object';c:=o->'content'->'value';expected:=x->'expected_content';
 IF public.observed_closed(o,ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
 OR public.observed_closed(o->'location',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
 OR public.observed_closed(o->'identity',ARRAY['state','value','reason_code']) IS NOT TRUE
 OR public.observed_closed(o->'content',ARRAY['state','value','reason_code']) IS NOT TRUE
 OR o->'identity'->'reason_code' IS DISTINCT FROM 'null'::jsonb OR o->'content'->'reason_code' IS DISTINCT FROM 'null'::jsonb
 OR public.observed_closed(c,ARRAY['kind','sha256','size','object_identity','read_started_at','read_finished_at','verifier_version']) IS NOT TRUE
 OR public.observed_closed(expected,ARRAY['kind','sha256','size','object_identity','read_started_at','read_finished_at','verifier_version']) IS NOT TRUE
 OR public.observed_identity_valid(c->'object_identity') IS NOT TRUE OR public.observed_identity_valid(expected->'object_identity') IS NOT TRUE
 OR (o->'location'->>'namespace_digest' ~ '^sha256:[0-9a-f]{64}$') IS NOT TRUE
 OR left(o->'location'->>'root',1) IS DISTINCT FROM '/' OR length(o->'location'->>'root')>4096
 OR o->'location'->>'relative_path' ~ '(^/|(^|/)\.\.(/|$))'
 OR o->'location'->>'canonical_path' IS DISTINCT FROM rtrim(o->'location'->>'root','/')||'/'||(o->'location'->>'relative_path')
 OR length(o->'location'->>'relative_path') NOT BETWEEN 1 AND 4096
 OR (expected->>'read_started_at')::timestamptz>(expected->>'read_finished_at')::timestamptz
 OR (c->>'read_started_at' ~ '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.[0-9]{1,6})?Z$') IS NOT TRUE
 OR (c->>'read_finished_at' ~ '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.[0-9]{1,6})?Z$') IS NOT TRUE THEN RETURN false; END IF;
 IF x IS DISTINCT FROM p_e->'observation' OR o IS DISTINCT FROM p_e->'final_object'
 OR (SELECT count(*) FROM jsonb_object_keys(x))<>14
 OR NOT (x ?& ARRAY['schema_version','basis','acquisition_id','plan_id','artifact_id','artifact_version','file_ref','input_digest','spec_digest','expected_content','observed_object','read_started_at','read_finished_at','verifier_version'])
 OR x->'schema_version' IS DISTINCT FROM '1'::jsonb OR x->>'basis' IS DISTINCT FROM 'stable_read'
 OR x->>'acquisition_id' IS DISTINCT FROM p_root OR x->>'plan_id' IS DISTINCT FROM p_plan OR x->>'artifact_id' IS DISTINCT FROM p_artifact
 OR x->'artifact_version' IS DISTINCT FROM to_jsonb(p_version) OR x->>'file_ref' IS DISTINCT FROM p_ref
 OR x->>'input_digest' IS DISTINCT FROM p_input OR x->>'spec_digest' IS DISTINCT FROM p_spec
 OR p_e->>'digest' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-observed-satisfaction:v1','UTF8')||decode('00','hex')||convert_to(p_e->>'canonical','UTF8')),'hex')
 OR o->>'file_type' IS DISTINCT FROM 'regular' OR o->'size' IS DISTINCT FROM to_jsonb(p_size)
 OR o->'location'->>'canonical_path' IS DISTINCT FROM p_path
 OR o->'identity'->>'state' IS DISTINCT FROM 'known' OR o->'content'->>'state' IS DISTINCT FROM 'known'
 OR c->>'kind' IS DISTINCT FROM 'sha256_file' OR expected->>'kind' IS DISTINCT FROM 'sha256_file'
 OR c->>'sha256' IS DISTINCT FROM 'sha256:'||p_hash OR expected->>'sha256' IS DISTINCT FROM 'sha256:'||p_hash
 OR c->'size' IS DISTINCT FROM to_jsonb(p_size) OR expected->'size' IS DISTINCT FROM to_jsonb(p_size)
 OR c->'object_identity' IS DISTINCT FROM o->'identity'->'value'
 OR c->>'read_started_at' IS DISTINCT FROM x->>'read_started_at' OR c->>'read_finished_at' IS DISTINCT FROM x->>'read_finished_at'
 OR o->>'observed_at' IS DISTINCT FROM x->>'read_finished_at'
 OR (c->>'verifier_version' IN ('local-stable-read-v1','local-exact-path-read-v1')) IS NOT TRUE
 OR (expected->>'verifier_version' IN ('local-stable-read-v1','local-exact-path-read-v1')) IS NOT TRUE
 OR x->>'verifier_version' IS DISTINCT FROM c->>'verifier_version'
 OR o->>'observation_source' IS DISTINCT FROM c->>'verifier_version'
 OR (x->>'read_started_at')::timestamptz>(x->>'read_finished_at')::timestamptz
 OR c->'object_identity'->>'kind' IS DISTINCT FROM 'local_fs'
 OR c->'object_identity'->'change_nanos'->>'state' IS DISTINCT FROM 'known'
 OR (c->'object_identity'->'change_nanos'->>'value')::bigint<=0 THEN RETURN false; END IF;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $_$;

CREATE FUNCTION plan_evidence_bytes(p_requirements text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT '['||COALESCE(string_agg('{"file_ref":'||(SELECT e.value::text FROM json_each(x) e WHERE e.key='file_ref')||',"selection_evidence":'||(SELECT e.value::text FROM json_each(x) e WHERE e.key='selection_evidence')||'}',',' ORDER BY ordinal),'')||']'
 FROM json_array_elements(p_requirements::json) WITH ORDINALITY AS a(x,ordinal)
$$;

CREATE FUNCTION plan_selection_bytes(p_input text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT e.value::text FROM json_each(p_input::json) e WHERE e.key='selection'
$$;

CREATE FUNCTION plan_target_spec_bytes(p_target text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT '{'||string_agg('"'||e.key||'":'||e.value::text,',' ORDER BY e.key COLLATE "C")||'}' FROM json_each(p_target::json) e WHERE e.key<>'spec_digest'
$$;

CREATE FUNCTION render_episode_title_valid(p jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT COALESCE(jsonb_typeof(p)='object'
 AND (SELECT array_agg(k COLLATE "C" ORDER BY k COLLATE "C") FROM jsonb_object_keys(p) k)=ARRAY['reason_code','state','value']::text[]
 AND CASE p->>'state'
  WHEN 'known' THEN jsonb_typeof(p->'value')='string' AND p->'reason_code'='null'::jsonb
   AND octet_length(p->>'value') BETWEEN 1 AND 512 AND btrim(p->>'value')=p->>'value' AND p->>'value' !~ '[[:cntrl:]]'
  WHEN 'absent' THEN p->'value'='null'::jsonb AND p->'reason_code'='null'::jsonb
  WHEN 'unavailable' THEN p->'value'='null'::jsonb AND p->>'reason_code' IN ('not_requested','no_episode','not_bound','title_invalid')
  ELSE false END,false)
$$;

CREATE FUNCTION subtitle_business_render(p_render jsonb) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE b jsonb; c text;
BEGIN
 IF NOT p_render ? 'binding' THEN RETURN p_render; END IF;
 b:=p_render->'binding';
 IF jsonb_typeof(b) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(b))<>8
 OR NOT b ?& ARRAY['state','bound_video_file_ref','bound_video_operation_id','locked_directory','locked_basename','locked_extension','method','confidence']
 OR b->>'state' IS DISTINCT FROM 'bound' OR b->>'method' NOT IN ('basename_match','episode_match','unique_video') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='BINDING_RENDER_SOURCE_UNSUPPORTED'; END IF;
 IF jsonb_typeof(b->'confidence') IS DISTINCT FROM 'number' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='BINDING_CONFIDENCE_INVALID'; END IF;
 c:=b->>'confidence';
 IF length(c)>128 OR c !~ '^(0|1)([.][0-9]+)?$' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='BINDING_CONFIDENCE_INVALID'; END IF;
 IF position('.' in c)>0 THEN c:=rtrim(rtrim(c,'0'),'.'); END IF;
 IF c !~ '^(0|1|0[.][0-9]{0,63}[1-9])$' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='BINDING_CONFIDENCE_INVALID'; END IF;
 RETURN jsonb_set(p_render,'{binding}',(b-'bound_video_operation_id')||jsonb_build_object('schema_version',1,'confidence',c));
END $_$;

CREATE FUNCTION vars_member_bytes(p_bytes text, p_key text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT e.value::text FROM json_each(p_bytes::json) e WHERE e.key=p_key
$$;

CREATE FUNCTION vars_evaluation_valid(p_record text, p_input text, p_root text, p_admission text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE r jsonb; v jsonb; program jsonb; vb text; inputs text; output text; k record; child record; count_keys integer:=0;
BEGIN
 IF p_record IS NULL OR octet_length(p_record)>65536 OR public.plan_canonical_valid(p_record) IS NOT TRUE THEN RETURN false; END IF;
 r:=p_record::jsonb;
 vb:=public.vars_member_bytes(public.vars_member_bytes(p_input,'config'),'vars');
 v:=vb::jsonb;program:=v->'program';
 inputs:=public.vars_member_bytes(vb,'known_inputs');output:=public.vars_member_bytes(vb,'output');
 IF public.observed_closed(r,ARRAY['schema_version','acquisition_id','admission_digest','evaluation_id','program_digest','program_version','program_runtime_version','runtime_version','input_digest','output','output_digest','evaluated_at']) IS NOT TRUE
 OR r->'schema_version' IS DISTINCT FROM '1'::jsonb
 OR r->>'acquisition_id' IS DISTINCT FROM p_root OR r->>'admission_digest' IS DISTINCT FROM p_admission
 OR jsonb_typeof(r->'evaluation_id') IS DISTINCT FROM 'string' OR octet_length(r->>'evaluation_id') NOT BETWEEN 1 AND 128 OR r->>'evaluation_id'<>btrim(r->>'evaluation_id')
 OR v->>'mode' IS DISTINCT FROM 'frozen' OR v->>'status' IS DISTINCT FROM 'ok' OR v->'reason_code' IS DISTINCT FROM 'null'::jsonb
 OR r->'program_digest' IS DISTINCT FROM program->'digest' OR r->'program_version' IS DISTINCT FROM program->'version'
 OR COALESCE(program->>'runtime_version','') <> 'naming-vars-v1'
 OR program->>'version' IS DISTINCT FROM (program->>'runtime_version')||'@'||(program->>'digest')
 OR r->'program_runtime_version' IS DISTINCT FROM program->'runtime_version'
 OR r->'runtime_version' IS DISTINCT FROM program->'runtime_version'
 OR octet_length(r->>'program_version') NOT BETWEEN 1 AND 128
 OR r->>'input_digest' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-vars-input:v1','UTF8')||decode('00','hex')||convert_to(inputs,'UTF8')),'hex')
 OR r->'output' IS DISTINCT FROM v->'output'
 OR r->>'output_digest' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to('acquisition-vars-output:v1','UTF8')||decode('00','hex')||convert_to(output,'UTF8')),'hex')
 OR jsonb_typeof(r->'output') IS DISTINCT FROM 'object' OR octet_length(output)>8192
 OR jsonb_typeof(r->'evaluated_at') IS DISTINCT FROM 'string' OR (r->>'evaluated_at' ~ '^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.[0-9]{1,6})?Z$') IS NOT TRUE THEN RETURN false; END IF;
 PERFORM (r->>'evaluated_at')::timestamptz;
 IF r->>'evaluated_at' ~ '\.[0-9]*0Z$' THEN RETURN false; END IF;
 FOR k IN SELECT * FROM jsonb_each(r->'output') LOOP
 IF (k.key ~ '^[a-z][a-z0-9_]*$') IS NOT TRUE OR k.key='input' OR octet_length(k.key)>128 OR jsonb_typeof(k.value)<>'object' OR (SELECT count(*) FROM jsonb_object_keys(k.value))>256 THEN RETURN false; END IF;
 FOR child IN SELECT * FROM jsonb_each(k.value) LOOP
 count_keys:=count_keys+1;
 IF (child.key ~ '^[a-z][a-z0-9_]*$') IS NOT TRUE OR octet_length(child.key)>128 OR jsonb_typeof(child.value)<>'string' OR octet_length(child.value #>> '{}')>8192 THEN RETURN false; END IF;
 END LOOP;
 END LOOP;
 IF (SELECT count(*) FROM jsonb_object_keys(r->'output'))>256 THEN RETURN false; END IF;
 RETURN true;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $_$;

REVOKE ALL ON FUNCTION acquisition_terminal_time_text, delivery_replacements,
    download_failure_category, download_merge_work_input, observed_closed, file_location_valid,
    file_wire_fv_valid, file_wire_i64_valid, file_wire_handle_valid, file_wire_u64_valid,
    file_wire_identity_valid, file_wire_time_valid, file_final_object_valid,
    file_finish_cancelled_success, file_finish_success, file_finish_runtime_entry,
    file_json_field, file_notification_safe, file_notification_text, file_proof_digest,
    file_version_candidate, file_wire_id_valid, notification_known, notification_episode_facts,
    notification_episode_title_facts, notification_input_facts, notification_join_values,
    notification_query_escape, notification_release_facts, notification_tag_block_valid,
    notification_tag_values, notification_title_facts, notification_value_facts,
    observed_identity_valid, plan_canonical_valid, observed_satisfaction_valid,
    plan_evidence_bytes, plan_selection_bytes, plan_target_spec_bytes,
    render_episode_title_valid, subtitle_business_render, vars_member_bytes,
    vars_evaluation_valid FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delivery_replacements, file_finish_runtime_entry,
    render_episode_title_valid TO bansoko_runtime;

-- ============================================================================
-- Instance bootstrap, accounts, sessions, API tokens, secrets
-- Single-owner instance: first-run bootstrap state, the local account, browser
-- sessions, scoped API tokens, and encrypted secret values.
-- ============================================================================

CREATE TABLE instance_bootstrap (
    singleton_id SMALLINT NOT NULL,
    state TEXT NOT NULL,
    install_id TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT instance_bootstrap_pkey PRIMARY KEY (singleton_id),
    CONSTRAINT instance_bootstrap_check CHECK ((state = 'ready') = (completed_at IS NOT NULL)),
    CONSTRAINT instance_bootstrap_install_id_check CHECK (char_length(btrim(install_id)) BETWEEN 1 AND 128),
    CONSTRAINT instance_bootstrap_singleton_id_check CHECK (singleton_id = 1),
    CONSTRAINT instance_bootstrap_state_check CHECK (state IN ('uninitialized', 'ready')),
    CONSTRAINT instance_bootstrap_version_check CHECK (version >= 1)
);

CREATE TABLE local_accounts (
    id TEXT NOT NULL,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'owner',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT local_accounts_pkey PRIMARY KEY (id),
    CONSTRAINT local_accounts_username_key UNIQUE (username),
    CONSTRAINT local_accounts_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT local_accounts_password_hash_check CHECK (char_length(btrim(password_hash)) BETWEEN 32 AND 512),
    CONSTRAINT local_accounts_role_check CHECK (role = 'owner'),
    CONSTRAINT local_accounts_username_check CHECK (char_length(btrim(username)) BETWEEN 1 AND 128),
    CONSTRAINT local_accounts_version_check CHECK (version >= 1)
);

CREATE TABLE auth_sessions (
    id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT auth_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT auth_sessions_account_id_fkey FOREIGN KEY (account_id) REFERENCES local_accounts (id),
    CONSTRAINT auth_sessions_check CHECK (expires_at > created_at),
    CONSTRAINT auth_sessions_revoked_at_check CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT auth_sessions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT auth_sessions_token_hash_check CHECK (char_length(btrim(token_hash)) BETWEEN 32 AND 256)
);

CREATE INDEX auth_sessions_account_idx ON auth_sessions (account_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE api_tokens (
    id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    secret_ref TEXT,
    scopes TEXT NOT NULL DEFAULT 'read,write',
    CONSTRAINT api_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT api_tokens_token_hash_key UNIQUE (token_hash),
    CONSTRAINT api_tokens_account_id_fkey FOREIGN KEY (account_id) REFERENCES local_accounts (id),
    CONSTRAINT api_tokens_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT api_tokens_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 128),
    CONSTRAINT api_tokens_scopes_check CHECK (scopes IN ('read', 'read,write', 'read,secrets', 'read,write,secrets')),
    CONSTRAINT api_tokens_secret_ref_check CHECK (secret_ref IS NULL OR char_length(btrim(secret_ref)) BETWEEN 1 AND 512),
    CONSTRAINT api_tokens_token_hash_check CHECK (char_length(btrim(token_hash)) BETWEEN 32 AND 256),
    CONSTRAINT api_tokens_version_check CHECK (version >= 1)
);

COMMENT ON TABLE api_tokens IS 'Long-lived bearer credentials for external agents. The raw token is shown once at create time; only its HMAC hash is stored. Revocation deletes the row (hard delete, no tombstone).';
COMMENT ON COLUMN api_tokens.secret_ref IS 'Reference into secret_values holding the raw token encrypted at rest, so the UI can display and copy the key for its lifetime. NULL means raw display is unavailable.';
COMMENT ON COLUMN api_tokens.scopes IS 'Canonical comma-joined scope set (read always included). Immutable after creation; rotation keeps it.';

CREATE TABLE secret_values (
    ref TEXT NOT NULL,
    purpose TEXT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT secret_values_pkey PRIMARY KEY (ref),
    CONSTRAINT secret_values_ciphertext_check CHECK (octet_length(ciphertext) >= 16),
    CONSTRAINT secret_values_nonce_check CHECK (octet_length(nonce) BETWEEN 12 AND 32),
    CONSTRAINT secret_values_purpose_check CHECK (char_length(btrim(purpose)) BETWEEN 1 AND 128),
    CONSTRAINT secret_values_ref_check CHECK (char_length(btrim(ref)) BETWEEN 1 AND 256),
    CONSTRAINT secret_values_version_check CHECK (version >= 1)
);

INSERT INTO instance_bootstrap (singleton_id, state, install_id)
VALUES (1, 'uninitialized', 'pending-install-id');

GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE instance_bootstrap, local_accounts, auth_sessions,
    api_tokens, secret_values TO bansoko_runtime;

-- ============================================================================
-- Connections, network routes, global configuration and policy defaults
-- External connections (qBittorrent, HTTP feeds, metadata providers) and their
-- network routes; versioned global configuration sections; attribute-rule and
-- naming-vars documents; instance-wide defaults for retention, parsing and file
-- effects; file maintenance policy; automation schedules.
-- ============================================================================

CREATE FUNCTION is_valid_qbittorrent_user_tags(value jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE
        WHEN jsonb_typeof(value) <> 'array' THEN FALSE
        ELSE NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements_text(value) AS item(tag)
            WHERE char_length(btrim(item.tag)) NOT BETWEEN 1 AND 256
        )
        AND jsonb_array_length(value) = (
            SELECT COUNT(DISTINCT item.tag)
            FROM jsonb_array_elements_text(value) AS item(tag)
        )
    END;
$$;

CREATE FUNCTION retention_cleanup_policy_valid(policy jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT policy IS NOT NULL
        AND jsonb_typeof(policy) = 'object'
        AND jsonb_typeof(policy->'version') = 'string'
        AND btrim(policy->>'version') <> ''
        AND (NOT (policy ? 'effect') OR policy->>'effect' IN ('quarantine', 'unlink', 'purge'))
        AND (NOT (policy ? 'grace_seconds')
             OR (jsonb_typeof(policy->'grace_seconds') = 'number'
                 AND (policy->>'grace_seconds')::numeric >= 0
                 AND (policy->>'grace_seconds')::numeric = floor((policy->>'grace_seconds')::numeric)))
$$;

CREATE FUNCTION retention_quality_profile_valid(profile jsonb) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
 item jsonb;
 entry jsonb;
 val jsonb;
 keys text[] := ARRAY[]::text[];
BEGIN
 IF profile IS NULL OR jsonb_typeof(profile) IS DISTINCT FROM 'object'
    OR (SELECT count(*) FROM jsonb_object_keys(profile)) <> 2
    OR NOT (profile ?& ARRAY['version','items'])
    OR profile->>'version' IS DISTINCT FROM 'profile-v1'
    OR jsonb_typeof(profile->'items') IS DISTINCT FROM 'array'
    OR jsonb_array_length(profile->'items') > 12 THEN
  RETURN FALSE;
 END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(profile->'items') LOOP
  IF jsonb_typeof(item) IS DISTINCT FROM 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
     OR NOT (item ?& ARRAY['key','ranks'])
     OR jsonb_typeof(item->'key') IS DISTINCT FROM 'string'
     OR item->>'key' NOT IN ('subgroup.score','release.lang','release.resolution','release.revision',
                             'anibt.resolution','anibt.language','anibt.subtitle','anibt.format',
                             'rt.resolution','rt.sub_lang','rt.source','rt.codec')
     OR item->>'key' = ANY(keys)
     OR jsonb_typeof(item->'ranks') IS DISTINCT FROM 'array'
     OR jsonb_array_length(item->'ranks') > 64 THEN
   RETURN FALSE;
  END IF;
  keys := array_append(keys, item->>'key');
  FOR entry IN SELECT value FROM jsonb_array_elements(item->'ranks') LOOP
   IF jsonb_typeof(entry) IS DISTINCT FROM 'array' OR jsonb_array_length(entry) NOT BETWEEN 1 AND 8 THEN
    RETURN FALSE;
   END IF;
   FOR val IN SELECT value FROM jsonb_array_elements(entry) LOOP
    IF jsonb_typeof(val) IS DISTINCT FROM 'string' OR btrim(val #>> '{}') = '' OR octet_length(val #>> '{}') > 128 THEN
     RETURN FALSE;
    END IF;
   END LOOP;
  END LOOP;
 END LOOP;
 RETURN TRUE;
EXCEPTION WHEN OTHERS THEN
 RETURN FALSE;
END;
$$;

CREATE TABLE network_routes (
    id TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    endpoint TEXT,
    secret_ref TEXT,
    state TEXT NOT NULL,
    reason_code TEXT,
    reason_detail TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT network_routes_pkey PRIMARY KEY (id),
    CONSTRAINT network_routes_name_key UNIQUE (name),
    CONSTRAINT network_routes_check CHECK ((kind = 'direct' AND endpoint IS NULL AND secret_ref IS NULL) OR (kind IN ('http_proxy', 'socks5') AND endpoint IS NOT NULL)),
    CONSTRAINT network_routes_endpoint_check CHECK (endpoint IS NULL OR char_length(endpoint) BETWEEN 1 AND 2048),
    CONSTRAINT network_routes_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT network_routes_kind_check CHECK (kind IN ('direct', 'http_proxy', 'socks5')),
    CONSTRAINT network_routes_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 256),
    CONSTRAINT network_routes_reason_code_check CHECK (reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT network_routes_reason_detail_check CHECK (reason_detail IS NULL OR char_length(reason_detail) <= 1024),
    CONSTRAINT network_routes_secret_ref_check CHECK (secret_ref IS NULL OR char_length(btrim(secret_ref)) BETWEEN 1 AND 256),
    CONSTRAINT network_routes_state_check CHECK (state IN ('configured', 'degraded', 'disabled')),
    CONSTRAINT network_routes_version_check CHECK (version >= 1)
);

CREATE TABLE connections (
    id TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    secret_ref TEXT,
    secret_present BOOLEAN GENERATED ALWAYS AS (secret_ref IS NOT NULL) STORED,
    network_route_id TEXT,
    state TEXT NOT NULL,
    health_version BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT connections_pkey PRIMARY KEY (id),
    CONSTRAINT connections_id_kind_key UNIQUE (id, kind),
    CONSTRAINT connections_id_kind_secret_present_key UNIQUE (id, kind, secret_present),
    CONSTRAINT connections_kind_name_key UNIQUE (kind, name),
    CONSTRAINT connections_network_route_id_fkey FOREIGN KEY (network_route_id) REFERENCES network_routes (id),
    CONSTRAINT connections_config_shape_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384 AND ((kind = 'qbittorrent' AND config ?& '{username,category,save_root,downloader_root,user_tags}'::text[] AND (config - '{username,category,save_root,downloader_root,user_tags}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'username')) = 'string' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256) AND jsonb_typeof((config -> 'category')) = 'string' AND char_length((config ->> 'category')) <= 256 AND jsonb_typeof((config -> 'save_root')) = 'string' AND char_length((config ->> 'save_root')) <= 4096 AND jsonb_typeof((config -> 'downloader_root')) = 'string' AND char_length((config ->> 'downloader_root')) <= 4096 AND is_valid_qbittorrent_user_tags((config -> 'user_tags'))) OR (kind = 'rss_http' AND config ?& '{auth_mode,username}'::text[] AND (config - '{auth_mode,username}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('none', 'basic', 'bearer', 'cookie') AND jsonb_typeof((config -> 'username')) = 'string' AND char_length((config ->> 'username')) <= 256 AND (((config ->> 'auth_mode') = 'basic' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') IN ('none', 'bearer', 'cookie') AND (config ->> 'username') = '')) AND ((config ->> 'auth_mode') <> 'none') = (secret_ref IS NOT NULL)) OR (kind IN ('anibt', 'mikan', 'tmdb', 'bgm') AND config = '{}'::jsonb))),
    CONSTRAINT connections_endpoint_check CHECK (char_length(btrim(endpoint)) BETWEEN 1 AND 4096),
    CONSTRAINT connections_health_version_check CHECK (health_version >= 0),
    CONSTRAINT connections_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT connections_kind_check CHECK (kind IN ('qbittorrent', 'rss_http', 'anibt', 'mikan', 'tmdb', 'bgm')),
    CONSTRAINT connections_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 256),
    CONSTRAINT connections_secret_ref_check CHECK (secret_ref IS NULL OR char_length(btrim(secret_ref)) BETWEEN 1 AND 256),
    CONSTRAINT connections_state_check CHECK (state IN ('configured', 'degraded', 'disabled')),
    CONSTRAINT connections_version_check CHECK (version >= 1)
);

CREATE INDEX connections_network_route_idx ON connections (network_route_id)
    WHERE network_route_id IS NOT NULL;

CREATE TABLE global_config_sections (
    section_name TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    value JSONB,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT global_config_sections_pkey PRIMARY KEY (section_name),
    CONSTRAINT global_config_sections_schema_version_check CHECK (char_length(btrim(schema_version)) BETWEEN 1 AND 128),
    CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'library_root', 'tag_filters', 'download_completion', 'retention', 'parser_policy', 'file_effect_policy')),
    CONSTRAINT global_config_sections_value_check CHECK (value <> 'null'::jsonb AND pg_column_size(value) <= 1048576),
    CONSTRAINT global_config_sections_value_presence_check CHECK (section_name = 'library_root' OR (value IS NOT NULL AND value <> 'null'::jsonb)),
    -- The three policy-default sections are owned by the retention/parser/file-effect
    -- policy services, not by the generic section API. Their value shape is checked
    -- here because a generic JSONB column carries no per-section type.
    CONSTRAINT global_config_sections_policy_shape_check CHECK (
        (section_name <> 'retention' OR (
            jsonb_typeof(value) = 'object'
            AND (value->>'admission_mode' IN ('all', 'per_group', 'single_copy')) IS TRUE
            AND (retention_cleanup_policy_valid(value->'cleanup_policy') AND pg_column_size(value->'cleanup_policy') <= 65536) IS TRUE
            AND (retention_quality_profile_valid(value->'quality_profile') AND pg_column_size(value->'quality_profile') <= 65536) IS TRUE
        ))
        AND (section_name <> 'parser_policy' OR (
            jsonb_typeof(value) = 'object'
            AND (char_length(btrim(value->>'parser_kind')) BETWEEN 1 AND 64) IS TRUE
        ))
        AND (section_name <> 'file_effect_policy' OR (
            jsonb_typeof(value) = 'object'
            AND (value->>'unclassified_route' IN ('route', 'skip')) IS TRUE
        ))
    ),
    CONSTRAINT global_config_sections_version_check CHECK (version >= 1)
);

CREATE TABLE attr_rule_versions (
    id BIGSERIAL,
    fingerprint TEXT NOT NULL,
    content JSONB NOT NULL,
    active BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT attr_rule_versions_pkey PRIMARY KEY (id),
    CONSTRAINT attr_rule_versions_fingerprint_key UNIQUE (fingerprint)
);

CREATE UNIQUE INDEX attr_rule_versions_active_uq ON attr_rule_versions (active)
    WHERE active;

CREATE TABLE naming_vars_versions (
    id BIGSERIAL,
    fingerprint TEXT NOT NULL,
    content JSONB NOT NULL,
    active BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revision BIGINT NOT NULL,
    origin TEXT NOT NULL DEFAULT 'file',
    note TEXT NOT NULL DEFAULT '',
    activated_at TIMESTAMPTZ,
    CONSTRAINT naming_vars_versions_pkey PRIMARY KEY (id),
    CONSTRAINT naming_vars_versions_fingerprint_key UNIQUE (fingerprint),
    CONSTRAINT naming_vars_versions_revision_uq UNIQUE (revision),
    CONSTRAINT naming_vars_versions_note_bounded CHECK (char_length(note) <= 200),
    CONSTRAINT naming_vars_versions_origin_valid CHECK (origin IN ('file', 'editor', 'import')),
    CONSTRAINT naming_vars_versions_revision_positive CHECK (revision > 0)
);

CREATE UNIQUE INDEX naming_vars_versions_active_uq ON naming_vars_versions (active)
    WHERE active;


CREATE TABLE filemaintenance_policies (
    scope_key TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT false,
    scan_interval_seconds INTEGER,
    alignment_enabled BOOLEAN,
    safety JSONB NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT filemaintenance_policies_pkey PRIMARY KEY (scope_key),
    CONSTRAINT filemaintenance_policies_safety_check CHECK (jsonb_typeof(safety) = 'object' AND pg_column_size(safety) <= 65536),
    CONSTRAINT filemaintenance_policies_scan_interval_seconds_check CHECK (scan_interval_seconds IS NULL OR scan_interval_seconds BETWEEN 60 AND 604800),
    CONSTRAINT filemaintenance_policies_scope_key_check CHECK (char_length(btrim(scope_key)) BETWEEN 1 AND 128),
    CONSTRAINT filemaintenance_policies_version_check CHECK (version >= 1)
);

CREATE TABLE automation_schedules (
    kind TEXT NOT NULL,
    enabled BOOLEAN NOT NULL,
    interval_value INTEGER NOT NULL,
    interval_unit TEXT NOT NULL,
    next_run_at TIMESTAMPTZ,
    last_dispatch_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT automation_schedules_pkey PRIMARY KEY (kind),
    CONSTRAINT automation_schedules_check CHECK ((kind = 'rss_refresh' AND (interval_unit = 'minute' OR interval_unit = 'hour' OR interval_unit = 'day') AND ((interval_value::double precision *
CASE interval_unit
    WHEN 'minute' THEN '00:01:00'::interval
    WHEN 'hour' THEN '01:00:00'::interval
    ELSE '1 day'::interval
END) >= '00:05:00'::interval AND (interval_value::double precision *
CASE interval_unit
    WHEN 'minute' THEN '00:01:00'::interval
    WHEN 'hour' THEN '01:00:00'::interval
    ELSE '1 day'::interval
END) <= '7 days'::interval)) OR (kind IN ('group_discovery', 'metadata_match', 'catalog_refresh') AND ((interval_value::double precision *
CASE interval_unit
    WHEN 'minute' THEN '00:01:00'::interval
    WHEN 'hour' THEN '01:00:00'::interval
    ELSE '1 day'::interval
END) >= '01:00:00'::interval AND (interval_value::double precision *
CASE interval_unit
    WHEN 'minute' THEN '00:01:00'::interval
    WHEN 'hour' THEN '01:00:00'::interval
    ELSE '1 day'::interval
END) <= '7 days'::interval))),
    CONSTRAINT automation_schedules_check1 CHECK (enabled OR next_run_at IS NULL),
    CONSTRAINT automation_schedules_interval_unit_check CHECK (interval_unit IN ('minute', 'hour', 'day')),
    CONSTRAINT automation_schedules_interval_value_check CHECK (interval_value >= 1),
    CONSTRAINT automation_schedules_kind_check CHECK (kind IN ('group_discovery', 'rss_refresh', 'metadata_match', 'catalog_refresh')),
    CONSTRAINT automation_schedules_version_check CHECK (version >= 1)
);

CREATE INDEX automation_schedules_due_idx ON automation_schedules (next_run_at, kind)
    WHERE enabled AND next_run_at IS NOT NULL;

INSERT INTO global_config_sections (section_name, schema_version, value, version)
VALUES ('library_root', 'library-root-v1', NULL, 1);

INSERT INTO global_config_sections (section_name, schema_version, value, version)
VALUES ('retention', 'retention-policy-v1',
        '{"admission_mode": "all", "quality_profile": {"version": "profile-v1", "items": [
            {"key": "subgroup.score", "ranks": []},
            {"key": "release.lang", "ranks": [["chs"], ["sc"], ["cht"], ["tc"], ["jpn"], ["eng"]]},
            {"key": "release.resolution", "ranks": [["2160p"], ["1440p"], ["1080p"], ["720p"], ["480p"]]},
            {"key": "release.revision", "ranks": []}]},
         "cleanup_policy": {"version": "cleanup-v1", "effect": "quarantine", "grace_seconds": 604800}}'::jsonb,
        1),
       ('parser_policy', 'parser-policy-v1', '{"parser_kind": "traditional"}'::jsonb, 1);

INSERT INTO automation_schedules (kind, enabled, interval_value, interval_unit, next_run_at)
VALUES ('rss_refresh', TRUE, 15, 'minute', CURRENT_TIMESTAMP + INTERVAL '15 minutes'),
       ('metadata_match', TRUE, 24, 'hour', CURRENT_TIMESTAMP + INTERVAL '24 hours'),
       ('catalog_refresh', TRUE, 12, 'hour', CURRENT_TIMESTAMP + INTERVAL '12 hours'),
       ('group_discovery', TRUE, 6, 'hour', CURRENT_TIMESTAMP + INTERVAL '6 hours');

CREATE FUNCTION guard_exact_program_content() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'EXACT_PROGRAM_DELETE_FORBIDDEN';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
        OR NEW.content IS DISTINCT FROM OLD.content
        OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'EXACT_PROGRAM_CONTENT_IMMUTABLE';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION guard_naming_vars_version() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'EXACT_PROGRAM_DELETE_FORBIDDEN';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
        OR NEW.content IS DISTINCT FROM OLD.content
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
        OR NEW.revision IS DISTINCT FROM OLD.revision
        OR NEW.origin IS DISTINCT FROM OLD.origin
        OR NEW.note IS DISTINCT FROM OLD.note THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'EXACT_PROGRAM_CONTENT_IMMUTABLE';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION naming_vars_assign_revision() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    -- Same lock the repository takes to publish: numbering and the single
    -- active row are decided under one serialization point.
    PERFORM pg_advisory_xact_lock(hashtext('bansoko:naming-vars')::bigint);
    IF NEW.revision IS NULL THEN
        SELECT COALESCE(MAX(revision), 0) + 1 INTO NEW.revision FROM public.naming_vars_versions;
    END IF;
    IF NEW.active AND NEW.activated_at IS NULL THEN
        NEW.activated_at := CURRENT_TIMESTAMP;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER attr_rule_versions_content_guard
    BEFORE DELETE OR UPDATE ON attr_rule_versions
    FOR EACH ROW EXECUTE FUNCTION guard_exact_program_content();

CREATE TRIGGER naming_vars_versions_assign_revision
    BEFORE INSERT ON naming_vars_versions
    FOR EACH ROW EXECUTE FUNCTION naming_vars_assign_revision();
CREATE TRIGGER naming_vars_versions_content_guard
    BEFORE DELETE OR UPDATE ON naming_vars_versions
    FOR EACH ROW EXECUTE FUNCTION guard_naming_vars_version();

GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE network_routes, connections,
    global_config_sections, filemaintenance_policies,
    automation_schedules TO bansoko_runtime;
GRANT SELECT, INSERT ON TABLE attr_rule_versions, naming_vars_versions TO bansoko_runtime;
GRANT UPDATE (active) ON TABLE attr_rule_versions TO bansoko_runtime;
GRANT SELECT, USAGE ON SEQUENCE attr_rule_versions_id_seq TO bansoko_runtime;
GRANT UPDATE (active) ON TABLE naming_vars_versions TO bansoko_runtime;
GRANT UPDATE (activated_at) ON TABLE naming_vars_versions TO bansoko_runtime;
GRANT SELECT, USAGE ON SEQUENCE naming_vars_versions_id_seq TO bansoko_runtime;

-- ============================================================================
-- Provider catalog, group and feed snapshots, metadata caches
-- Season catalog snapshots from the providers, user suppressions and the
-- metadata/poster caches the catalog and workspace read.
-- ============================================================================

CREATE TABLE provider_catalog_preferences (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    season TEXT NOT NULL,
    refresh_enabled BOOLEAN NOT NULL DEFAULT false,
    schedule TEXT,
    network_route_id TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT provider_catalog_preferences_pkey PRIMARY KEY (id),
    CONSTRAINT provider_catalog_preferences_provider_season_key UNIQUE (provider, season),
    CONSTRAINT provider_catalog_preferences_network_route_id_fkey FOREIGN KEY (network_route_id) REFERENCES network_routes (id),
    CONSTRAINT provider_catalog_preferences_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_catalog_preferences_season_check CHECK (char_length(season) BETWEEN 1 AND 32),
    CONSTRAINT provider_catalog_preferences_version_check CHECK (version >= 1)
);

CREATE TABLE provider_catalog_snapshots (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    season TEXT NOT NULL,
    status TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    source_version TEXT NOT NULL DEFAULT '',
    cursor TEXT,
    etag TEXT,
    last_modified TEXT,
    contract_version TEXT NOT NULL,
    health_state TEXT NOT NULL DEFAULT 'unknown',
    health_code TEXT,
    health_message TEXT,
    health_checked_at TIMESTAMPTZ NOT NULL,
    error_code TEXT,
    error_message TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    trace_id TEXT,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    CONSTRAINT provider_catalog_snapshots_pkey PRIMARY KEY (id),
    CONSTRAINT provider_catalog_snapshots_provider_season_version_key UNIQUE (provider, season, version),
    CONSTRAINT provider_catalog_snapshots_health_state_check CHECK (health_state IN ('unknown', 'ready', 'degraded', 'failed')),
    CONSTRAINT provider_catalog_snapshots_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_catalog_snapshots_season_check CHECK (char_length(season) BETWEEN 1 AND 32),
    CONSTRAINT provider_catalog_snapshots_status_check CHECK (status IN ('running', 'fresh', 'stale', 'withdrawn', 'outage', 'zero_result')),
    CONSTRAINT provider_catalog_snapshots_version_check CHECK (version >= 1)
);

CREATE INDEX provider_catalog_snapshots_latest_idx ON provider_catalog_snapshots (provider, season, version DESC);
CREATE INDEX provider_catalog_snapshots_retention_idx ON provider_catalog_snapshots (retention_until, id);
CREATE UNIQUE INDEX provider_catalog_snapshots_running_idx ON provider_catalog_snapshots (provider, season)
    WHERE status = 'running';

CREATE TABLE provider_catalog_entries (
    id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_entry_ref TEXT NOT NULL,
    bgm_id TEXT,
    title TEXT NOT NULL,
    original_title TEXT,
    source_hint JSONB NOT NULL DEFAULT '{}'::jsonb,
    state TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT provider_catalog_entries_pkey PRIMARY KEY (id),
    CONSTRAINT provider_catalog_entries_snapshot_id_provider_provider_e_key UNIQUE (snapshot_id, provider, provider_entry_ref),
    CONSTRAINT provider_catalog_entries_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES provider_catalog_snapshots (id) ON DELETE CASCADE,
    CONSTRAINT provider_catalog_entries_bgm_id_check CHECK (bgm_id IS NULL OR bgm_id ~ '^[1-9][0-9]*$'),
    CONSTRAINT provider_catalog_entries_check CHECK ((provider = 'mikan' AND provider_entry_ref ~ '^[1-9][0-9]*$') OR (provider = 'anibt' AND provider_entry_ref ~ '^[1-9][0-9]*$')),
    CONSTRAINT provider_catalog_entries_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_catalog_entries_state_check CHECK (state IN ('present', 'withdrawn'))
);

CREATE INDEX provider_catalog_entries_identity_idx ON provider_catalog_entries (provider, provider_entry_ref, observed_at DESC);

CREATE TABLE catalog_suppressions (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_entry_ref TEXT NOT NULL,
    title_snapshot TEXT NOT NULL,
    suppressed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT catalog_suppressions_pkey PRIMARY KEY (id),
    CONSTRAINT catalog_suppressions_provider_provider_entry_ref_key UNIQUE (provider, provider_entry_ref),
    CONSTRAINT catalog_suppressions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT catalog_suppressions_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT catalog_suppressions_provider_entry_ref_check CHECK (char_length(btrim(provider_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT catalog_suppressions_title_snapshot_check CHECK (char_length(btrim(title_snapshot)) BETWEEN 1 AND 512),
    CONSTRAINT catalog_suppressions_version_check CHECK (version >= 1)
);

CREATE TABLE metadata_work_titles (
    provider TEXT NOT NULL,
    media_kind TEXT NOT NULL,
    work_id TEXT NOT NULL,
    title TEXT,
    title_cn TEXT,
    original_title TEXT,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT metadata_work_titles_pkey PRIMARY KEY (provider, media_kind, work_id),
    CONSTRAINT metadata_work_titles_check CHECK ((provider = 'bgm' AND media_kind = 'subject' AND original_title IS NULL) OR (provider = 'tmdb' AND media_kind IN ('tv', 'movie') AND title_cn IS NULL)),
    CONSTRAINT metadata_work_titles_media_kind_check CHECK (media_kind IN ('subject', 'tv', 'movie')),
    CONSTRAINT metadata_work_titles_original_title_check CHECK (original_title IS NULL OR (octet_length(original_title) BETWEEN 1 AND 512 AND original_title = btrim(original_title))),
    CONSTRAINT metadata_work_titles_provider_check CHECK (provider IN ('bgm', 'tmdb')),
    CONSTRAINT metadata_work_titles_title_check CHECK (title IS NULL OR (octet_length(title) BETWEEN 1 AND 512 AND title = btrim(title))),
    CONSTRAINT metadata_work_titles_title_cn_check CHECK (title_cn IS NULL OR (octet_length(title_cn) BETWEEN 1 AND 512 AND title_cn = btrim(title_cn))),
    CONSTRAINT metadata_work_titles_work_id_check CHECK (char_length(btrim(work_id)) BETWEEN 1 AND 128)
);

CREATE TABLE media_poster_cache (
    bgm_id TEXT NOT NULL,
    image_bytes BYTEA NOT NULL,
    content_type TEXT NOT NULL,
    image_url TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,
    last_used TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT media_poster_cache_pkey PRIMARY KEY (bgm_id),
    CONSTRAINT media_poster_cache_bgm_id_check CHECK (char_length(btrim(bgm_id)) BETWEEN 1 AND 128),
    CONSTRAINT media_poster_cache_check CHECK (expires_at >= fetched_at),
    CONSTRAINT media_poster_cache_check1 CHECK (last_used >= fetched_at),
    CONSTRAINT media_poster_cache_content_type_check CHECK (char_length(btrim(content_type)) BETWEEN 1 AND 256),
    CONSTRAINT media_poster_cache_image_url_check CHECK (char_length(btrim(image_url)) BETWEEN 1 AND 2048)
);

CREATE INDEX media_poster_cache_expires_idx ON media_poster_cache (expires_at, bgm_id);

-- Provider cover and candidate-poster images behind the authenticated
-- same-origin proxy (/images/cover/{image_key}). A row is the server's own
-- registry entry: image_key is the SHA-256 of provider and source_url, and the
-- proxy resolves the upstream URL only from this row, never from the request.
-- Bytes stay NULL until the first successful fetch.
CREATE TABLE provider_image_cache (
    image_key TEXT NOT NULL,
    provider TEXT NOT NULL,
    source_url TEXT NOT NULL,
    image_bytes BYTEA,
    content_type TEXT,
    fetched_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    last_used TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT provider_image_cache_pkey PRIMARY KEY (image_key),
    CONSTRAINT provider_image_cache_image_key_check CHECK (image_key ~ '^[0-9a-f]{64}$'),
    CONSTRAINT provider_image_cache_provider_check CHECK (provider IN ('mikan', 'anibt', 'tmdb')),
    CONSTRAINT provider_image_cache_source_url_check CHECK (char_length(source_url) BETWEEN 1 AND 2048),
    CONSTRAINT provider_image_cache_content_type_check CHECK (content_type IS NULL OR char_length(btrim(content_type)) BETWEEN 1 AND 256),
    CONSTRAINT provider_image_cache_bytes_check CHECK (
        (image_bytes IS NULL AND content_type IS NULL AND fetched_at IS NULL AND expires_at IS NULL)
        OR (image_bytes IS NOT NULL AND content_type IS NOT NULL AND fetched_at IS NOT NULL AND expires_at IS NOT NULL AND expires_at >= fetched_at)
    )
);

CREATE INDEX provider_image_cache_last_used_idx ON provider_image_cache (last_used, image_key);

GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE provider_catalog_preferences,
    provider_catalog_snapshots, provider_catalog_entries, catalog_suppressions,
    metadata_work_titles, media_poster_cache, provider_image_cache TO bansoko_runtime;

-- ============================================================================
-- Managed anime, configuration owners, subscriptions, RSS sources and candidates
-- A ManagedAnime is the owner of everything below it. Lifecycle fences keep
-- late writers out of a retired owner. Subscriptions select provider groups;
-- RSS sources carry the feed locator and episode offset; feed snapshots freeze
-- what a feed returned. Candidates are drafts that become a ManagedAnime when
-- activated; per-instance policies and the business snapshot freeze its
-- configuration.
-- ============================================================================

CREATE TABLE managed_anime (
    id TEXT NOT NULL,
    title TEXT NOT NULL,
    title_override TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    fence_epoch BIGINT NOT NULL DEFAULT 1,
    retired_at TIMESTAMPTZ,
    retired_subscription_version BIGINT,
    lifecycle_version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT managed_anime_pkey PRIMARY KEY (id),
    CONSTRAINT managed_anime_lifecycle_check CHECK ((lifecycle_state = 'active' AND retired_at IS NULL AND retired_subscription_version IS NULL) OR (lifecycle_state = 'retired' AND retired_at IS NOT NULL AND retired_subscription_version IS NOT NULL)),
    CONSTRAINT managed_anime_fence_epoch_check CHECK (fence_epoch >= 1),
    CONSTRAINT managed_anime_retired_subscription_version_check CHECK (retired_subscription_version IS NULL OR retired_subscription_version >= 1),
    CONSTRAINT managed_anime_lifecycle_state_check CHECK (lifecycle_state IN ('active', 'retired')),
    CONSTRAINT managed_anime_lifecycle_version_check CHECK (lifecycle_version >= 1),
    CONSTRAINT managed_anime_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT managed_anime_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 512),
    CONSTRAINT managed_anime_title_override_check CHECK (title_override IS NULL OR char_length(btrim(title_override)) BETWEEN 1 AND 512),
    CONSTRAINT managed_anime_version_check CHECK (version >= 1)
);

CREATE TABLE anime_metadata (
    managed_anime_id TEXT NOT NULL,
    bgm_work_id TEXT,
    tmdb_work_id TEXT,
    tmdb_media_kind TEXT,
    tmdb_season INTEGER,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_episodes INTEGER,
    CONSTRAINT anime_metadata_pkey PRIMARY KEY (managed_anime_id),
    CONSTRAINT anime_metadata_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT anime_metadata_bgm_work_id_check CHECK (bgm_work_id IS NULL OR char_length(btrim(bgm_work_id)) BETWEEN 1 AND 128),
    CONSTRAINT anime_metadata_tmdb_atomic_check CHECK ((tmdb_work_id IS NULL AND tmdb_media_kind IS NULL AND tmdb_season IS NULL) OR (tmdb_work_id IS NOT NULL AND tmdb_media_kind IS NOT NULL AND tmdb_season IS NOT NULL)),
    CONSTRAINT anime_metadata_tmdb_media_kind_check CHECK (tmdb_media_kind IS NULL OR tmdb_media_kind IN ('tv', 'movie')),
    CONSTRAINT anime_metadata_tmdb_season_check CHECK (tmdb_season IS NULL OR tmdb_season >= 0),
    CONSTRAINT anime_metadata_tmdb_work_id_check CHECK (tmdb_work_id IS NULL OR char_length(btrim(tmdb_work_id)) BETWEEN 1 AND 128),
    CONSTRAINT anime_metadata_total_episodes_check CHECK (total_episodes IS NULL OR total_episodes >= 0),
    CONSTRAINT anime_metadata_version_check CHECK (version >= 1)
);


CREATE TABLE anime_config_overrides (
    managed_anime_id TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    section_name TEXT NOT NULL,
    schema_version TEXT,
    value JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT anime_config_overrides_pkey PRIMARY KEY (managed_anime_id, section_name),
    CONSTRAINT anime_config_overrides_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT anime_config_overrides_check CHECK ((owner_type = 'subgroups' AND section_name = 'subgroups') OR (owner_type = 'rules' AND section_name <> 'subgroups')),
    CONSTRAINT anime_config_overrides_check1 CHECK ((value IS NULL AND schema_version IS NULL) OR (value IS NOT NULL AND value <> 'null'::jsonb AND schema_version IS NOT NULL)),
    CONSTRAINT anime_config_overrides_owner_type_check CHECK (owner_type IN ('subgroups', 'rules')),
    CONSTRAINT anime_config_overrides_schema_version_check CHECK (schema_version IS NULL OR char_length(btrim(schema_version)) BETWEEN 1 AND 128),
    CONSTRAINT anime_config_overrides_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion')),
    CONSTRAINT anime_config_overrides_value_check CHECK (value IS NULL OR pg_column_size(value) <= 1048576)
);

CREATE TABLE subscriptions (
    id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    origin TEXT NOT NULL,
    origin_entry_ref TEXT,
    instance_label TEXT,
    state TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT subscriptions_pkey PRIMARY KEY (id),
    CONSTRAINT subscriptions_id_managed_anime_id_key UNIQUE (id, managed_anime_id),
    CONSTRAINT subscriptions_managed_anime_id_key UNIQUE (managed_anime_id),
    CONSTRAINT subscriptions_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT subscriptions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT subscriptions_instance_label_check CHECK (instance_label IS NULL OR char_length(btrim(instance_label)) BETWEEN 1 AND 256),
    CONSTRAINT subscriptions_origin_check CHECK (origin IN ('anibt', 'mikan', 'custom', 'oneshot')),
    CONSTRAINT subscriptions_origin_entry_ref_check CHECK (origin_entry_ref IS NULL OR char_length(btrim(origin_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT subscriptions_origin_label_check CHECK (origin NOT IN ('custom', 'oneshot') OR instance_label IS NOT NULL),
    CONSTRAINT subscriptions_origin_ref_null_check CHECK (origin NOT IN ('custom', 'oneshot') OR origin_entry_ref IS NULL),
    CONSTRAINT subscriptions_state_check CHECK (state IN ('active', 'stopped')),
    CONSTRAINT subscriptions_version_check CHECK (version >= 1)
);

CREATE TABLE provider_group_scope_generations (
    subscription_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT provider_group_scope_generations_pkey PRIMARY KEY (subscription_id, provider),
    CONSTRAINT provider_group_scope_generations_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions (id),
    CONSTRAINT provider_group_scope_generations_generation_check CHECK (generation >= 0),
    CONSTRAINT provider_group_scope_generations_provider_check CHECK (provider IN ('anibt', 'mikan'))
);

CREATE TABLE provider_group_selections (
    id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    group_ref TEXT NOT NULL,
    display_name TEXT NOT NULL,
    selection_mode TEXT NOT NULL,
    manual_selected BOOLEAN,
    effective_selected BOOLEAN NOT NULL,
    effective_score INTEGER NOT NULL,
    count_in_limit BOOLEAN NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    available BOOLEAN NOT NULL DEFAULT true,
    availability_generation BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT provider_group_selections_pkey PRIMARY KEY (id),
    CONSTRAINT provider_group_selections_subscription_id_provider_group_key UNIQUE (subscription_id, provider, group_ref),
    CONSTRAINT provider_group_selections_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions (id),
    CONSTRAINT provider_group_selections_availability_generation_check CHECK (availability_generation >= 0),
    CONSTRAINT provider_group_selections_check CHECK ((selection_mode = 'auto' AND manual_selected IS NULL) OR (selection_mode = 'manual' AND manual_selected IS NOT NULL)),
    CONSTRAINT provider_group_selections_display_name_check CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 256),
    CONSTRAINT provider_group_selections_group_ref_check CHECK (char_length(btrim(group_ref)) BETWEEN 1 AND 256),
    CONSTRAINT provider_group_selections_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_selections_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_group_selections_selection_mode_check CHECK (selection_mode IN ('auto', 'manual')),
    CONSTRAINT provider_group_selections_version_check CHECK (version >= 1)
);

CREATE INDEX provider_group_selections_subscription_idx ON provider_group_selections (subscription_id, effective_selected, effective_score DESC, group_ref);

CREATE TABLE provider_group_snapshots (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    catalog_entry_ref TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    source_version TEXT NOT NULL DEFAULT '',
    schema_version TEXT NOT NULL,
    group_count INTEGER NOT NULL,
    version BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    canonical_observation TEXT,
    observation JSONB GENERATED ALWAYS AS (canonical_observation::jsonb) STORED,
    observation_digest TEXT,
    entries JSONB NOT NULL DEFAULT '[]'::jsonb,
    CONSTRAINT provider_group_snapshots_pkey PRIMARY KEY (id),
    CONSTRAINT provider_group_snapshots_provider_catalog_entry_ref_vers_key UNIQUE (provider, catalog_entry_ref, version),
    CONSTRAINT group_observation_pair CHECK ((canonical_observation IS NULL AND observation_digest IS NULL) OR (canonical_observation IS NOT NULL AND observation_digest IS NOT NULL AND observation_digest = ('sha256:' || encode(sha256(((convert_to('acquisition-group-observation:v1', 'UTF8'::name) || decode('00', 'hex')) || convert_to(canonical_observation, 'UTF8'::name))), 'hex')))),
    CONSTRAINT provider_group_snapshots_catalog_entry_ref_check CHECK (char_length(btrim(catalog_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT provider_group_snapshots_entries_check CHECK (group_observation_entries_ok(entries)),
    CONSTRAINT provider_group_snapshots_group_count_check CHECK (group_count >= 0),
    CONSTRAINT provider_group_snapshots_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_snapshots_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_group_snapshots_schema_version_check CHECK (char_length(btrim(schema_version)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_snapshots_source_version_check CHECK (char_length(source_version) <= 256),
    CONSTRAINT provider_group_snapshots_version_check CHECK (version >= 1)
);

CREATE INDEX provider_group_snapshots_latest_idx ON provider_group_snapshots (provider, catalog_entry_ref, version DESC);
CREATE INDEX provider_group_snapshots_retention_idx ON provider_group_snapshots (retention_until, id);

CREATE TABLE provider_group_refresh_health (
    provider TEXT NOT NULL,
    catalog_entry_ref TEXT NOT NULL,
    state TEXT NOT NULL,
    last_attempt_at TIMESTAMPTZ NOT NULL,
    last_success_at TIMESTAMPTZ,
    error_code TEXT,
    error_message TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT provider_group_refresh_health_pkey PRIMARY KEY (provider, catalog_entry_ref),
    CONSTRAINT provider_group_refresh_health_catalog_entry_ref_check CHECK (char_length(btrim(catalog_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT provider_group_refresh_health_check CHECK (last_success_at IS NULL OR last_success_at <= last_attempt_at),
    CONSTRAINT provider_group_refresh_health_check1 CHECK ((state = 'error' AND error_code IS NOT NULL) OR (state <> 'error' AND error_code IS NULL AND error_message IS NULL)),
    CONSTRAINT provider_group_refresh_health_error_code_check CHECK (error_code IS NULL OR char_length(btrim(error_code)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_refresh_health_error_message_check CHECK (error_message IS NULL OR char_length(error_message) <= 512),
    CONSTRAINT provider_group_refresh_health_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_group_refresh_health_state_check CHECK (state IN ('fresh', 'empty', 'error')),
    CONSTRAINT provider_group_refresh_health_version_check CHECK (version >= 1)
);

CREATE TABLE rss_sources (
    id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    locator TEXT NOT NULL,
    locator_display TEXT NOT NULL,
    group_name TEXT NOT NULL,
    title TEXT NOT NULL,
    episode_offset INTEGER NOT NULL DEFAULT 0,
    provenance JSONB,
    provider TEXT,
    provider_entry_ref TEXT,
    provider_group_ref TEXT,
    discovery_snapshot_version TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    http_feed_connection_ref TEXT,
    network_route_ref TEXT,
    anibt_aux_mode TEXT,
    CONSTRAINT rss_sources_pkey PRIMARY KEY (id),
    CONSTRAINT rss_sources_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions (id),
    CONSTRAINT rss_sources_anibt_aux_mode_check CHECK (anibt_aux_mode IN ('disabled', 'fill_missing', 'override')),
    CONSTRAINT rss_sources_check CHECK ((source_kind = 'custom_rss' AND provenance IS NULL AND provider IS NULL AND provider_entry_ref IS NULL AND provider_group_ref IS NULL AND discovery_snapshot_version IS NULL) OR (source_kind = 'provider_group' AND provenance IS NOT NULL AND provider IS NOT NULL AND provider_entry_ref IS NOT NULL AND provider_group_ref IS NOT NULL AND discovery_snapshot_version IS NOT NULL AND (provenance ->> 'provider') = provider AND (provenance ->> 'provider_entry_ref') = provider_entry_ref AND (provenance ->> 'provider_group_ref') = provider_group_ref AND (provenance ->> 'discovery_snapshot_version') = discovery_snapshot_version)),
    CONSTRAINT rss_sources_discovery_snapshot_version_check CHECK (discovery_snapshot_version IS NULL OR char_length(btrim(discovery_snapshot_version)) BETWEEN 1 AND 128),
    CONSTRAINT rss_sources_group_name_check CHECK (char_length(btrim(group_name)) BETWEEN 1 AND 256),
    CONSTRAINT rss_sources_http_feed_connection_ref_check CHECK (http_feed_connection_ref IS NULL OR char_length(btrim(http_feed_connection_ref)) BETWEEN 1 AND 256),
    CONSTRAINT rss_sources_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT rss_sources_locator_check CHECK (char_length(locator) BETWEEN 1 AND 4096 AND locator !~ '[[:cntrl:]]'),
    CONSTRAINT rss_sources_locator_display_check CHECK (char_length(locator_display) BETWEEN 1 AND 4096 AND locator_display !~ '[[:cntrl:]]'),
    CONSTRAINT rss_sources_network_route_ref_check CHECK (network_route_ref IS NULL OR char_length(btrim(network_route_ref)) BETWEEN 1 AND 256),
    CONSTRAINT rss_sources_offset_override_check CHECK (episode_offset >= '-1000'::integer AND episode_offset <= 1000),
    CONSTRAINT rss_sources_provenance_check CHECK (provenance IS NULL OR (jsonb_typeof(provenance) = 'object' AND pg_column_size(provenance) <= 16384 AND provenance ?& ARRAY['provider', 'provider_entry_ref', 'provider_group_ref', 'discovery_snapshot_version'] AND jsonb_typeof((provenance -> 'provider')) = 'string' AND jsonb_typeof((provenance -> 'provider_entry_ref')) = 'string' AND jsonb_typeof((provenance -> 'provider_group_ref')) = 'string' AND jsonb_typeof((provenance -> 'discovery_snapshot_version')) = 'string' AND (char_length(btrim((provenance ->> 'provider'))) >= 1 AND char_length(btrim((provenance ->> 'provider'))) <= 16) AND (provenance ->> 'provider') IN ('anibt', 'mikan') AND (char_length(btrim((provenance ->> 'provider_entry_ref'))) >= 1 AND char_length(btrim((provenance ->> 'provider_entry_ref'))) <= 256) AND (char_length(btrim((provenance ->> 'provider_group_ref'))) >= 1 AND char_length(btrim((provenance ->> 'provider_group_ref'))) <= 256) AND (char_length(btrim((provenance ->> 'discovery_snapshot_version'))) >= 1 AND char_length(btrim((provenance ->> 'discovery_snapshot_version'))) <= 128))),
    CONSTRAINT rss_sources_provider_check CHECK (provider IS NULL OR provider IN ('anibt', 'mikan')),
    CONSTRAINT rss_sources_provider_entry_ref_check CHECK (provider_entry_ref IS NULL OR char_length(btrim(provider_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT rss_sources_provider_group_ref_check CHECK (provider_group_ref IS NULL OR char_length(btrim(provider_group_ref)) BETWEEN 1 AND 256),
    CONSTRAINT rss_sources_source_kind_check CHECK (source_kind IN ('provider_group', 'custom_rss')),
    CONSTRAINT rss_sources_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 512),
    CONSTRAINT rss_sources_transport_kind_check CHECK (source_kind = 'custom_rss' OR (http_feed_connection_ref IS NULL AND network_route_ref IS NULL)),
    CONSTRAINT rss_sources_version_check CHECK (version >= 1)
);

CREATE UNIQUE INDEX rss_sources_custom_locator_uq ON rss_sources (subscription_id, locator)
    WHERE source_kind = 'custom_rss';
CREATE INDEX rss_sources_provider_group_idx ON rss_sources (subscription_id, provider, provider_group_ref)
    WHERE source_kind = 'provider_group';
CREATE UNIQUE INDEX rss_sources_provider_identity_uq ON rss_sources (subscription_id, provider, provider_entry_ref, provider_group_ref)
    WHERE source_kind = 'provider_group';
CREATE INDEX rss_sources_subscription_idx ON rss_sources (subscription_id, source_kind);

COMMENT ON COLUMN rss_sources.episode_offset IS 'The episode offset of this RSS link, -1000..1000; 0 means no offset. The only offset owner: nothing inherits it.';

CREATE TABLE rss_source_runtime (
    rss_source_id TEXT NOT NULL,
    cursor JSONB,
    last_good_facts JSONB,
    last_refresh_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    health TEXT NOT NULL DEFAULT 'unknown',
    reason_code TEXT,
    reason_detail TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    etag TEXT,
    last_modified TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT rss_source_runtime_pkey PRIMARY KEY (rss_source_id),
    CONSTRAINT rss_source_runtime_rss_source_id_fkey FOREIGN KEY (rss_source_id) REFERENCES rss_sources (id),
    CONSTRAINT rss_source_runtime_check CHECK (last_success_at IS NULL OR (last_refresh_at IS NOT NULL AND last_success_at <= last_refresh_at)),
    CONSTRAINT rss_source_runtime_consecutive_failures_check CHECK (consecutive_failures >= 0),
    CONSTRAINT rss_source_runtime_cursor_check CHECK (cursor IS NULL OR (jsonb_typeof(cursor) = 'object' AND pg_column_size(cursor) <= 262144)),
    CONSTRAINT rss_source_runtime_etag_check CHECK (etag IS NULL OR char_length(etag) BETWEEN 1 AND 512),
    CONSTRAINT rss_source_runtime_health_check CHECK (health IN ('unknown', 'ready', 'degraded', 'failed')),
    CONSTRAINT rss_source_runtime_last_good_facts_check CHECK (last_good_facts IS NULL OR (jsonb_typeof(last_good_facts) = 'object' AND pg_column_size(last_good_facts) <= 1048576)),
    CONSTRAINT rss_source_runtime_last_modified_check CHECK (last_modified IS NULL OR char_length(last_modified) BETWEEN 1 AND 128),
    CONSTRAINT rss_source_runtime_reason_code_check CHECK (reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT rss_source_runtime_reason_detail_check CHECK (reason_detail IS NULL OR char_length(reason_detail) <= 2048),
    CONSTRAINT rss_source_runtime_version_check CHECK (version >= 1)
);

CREATE TABLE rss_provider_circuits (
    provider TEXT NOT NULL,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    paused_until TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT rss_provider_circuits_pkey PRIMARY KEY (provider),
    CONSTRAINT rss_provider_circuits_check CHECK (paused_until IS NULL OR last_failure_at IS NOT NULL),
    CONSTRAINT rss_provider_circuits_consecutive_failures_check CHECK (consecutive_failures >= 0),
    CONSTRAINT rss_provider_circuits_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT rss_provider_circuits_version_check CHECK (version >= 1)
);

CREATE TABLE provider_feed_snapshots (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_entry_ref TEXT NOT NULL,
    provider_group_ref TEXT NOT NULL,
    version BIGINT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    content_hash TEXT NOT NULL,
    fresh_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '00:15:00'::interval),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    seed_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hash_schema TEXT NOT NULL DEFAULT 'provider-feed-snapshot-v1',
    projection_schema TEXT,
    CONSTRAINT provider_feed_snapshots_pkey PRIMARY KEY (id),
    CONSTRAINT provider_feed_snapshots_provider_provider_entry_ref_prov_key UNIQUE (provider, provider_entry_ref, provider_group_ref, version),
    CONSTRAINT provider_feed_snapshots_check CHECK (fresh_until >= fetched_at),
    CONSTRAINT provider_feed_snapshots_check1 CHECK (retention_until >= created_at),
    CONSTRAINT provider_feed_snapshots_content_hash_check CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT provider_feed_snapshots_custom_scope_check CHECK (provider <> 'custom' OR (provider_entry_ref ~ '^custom-source:[0-9a-f]{64}$' AND provider_group_ref ~ '^custom-group:[0-9a-f]{64}$')),
    CONSTRAINT provider_feed_snapshots_hash_schema_check CHECK (hash_schema = 'provider-feed-snapshot-v1'),
    CONSTRAINT provider_feed_snapshots_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT provider_feed_snapshots_projection_schema_check CHECK (projection_schema IS NOT NULL),
    CONSTRAINT provider_feed_snapshots_projection_schema_value_check CHECK (projection_schema IS NULL OR projection_schema = 'torrent-manifest-v1'),
    CONSTRAINT provider_feed_snapshots_provider_check CHECK (provider IN ('anibt', 'mikan', 'custom')),
    CONSTRAINT provider_feed_snapshots_provider_entry_ref_check CHECK (char_length(btrim(provider_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT provider_feed_snapshots_provider_group_ref_check CHECK (char_length(btrim(provider_group_ref)) BETWEEN 1 AND 256),
    CONSTRAINT provider_feed_snapshots_seed_count_check CHECK (seed_count BETWEEN 0 AND 10000),
    CONSTRAINT provider_feed_snapshots_version_check CHECK (version >= 1)
);

CREATE INDEX provider_feed_snapshots_hash_schema_idx ON provider_feed_snapshots (hash_schema, retention_until);
CREATE INDEX provider_feed_snapshots_latest_idx ON provider_feed_snapshots (provider, provider_entry_ref, provider_group_ref, version DESC);
CREATE INDEX provider_feed_snapshots_retention_idx ON provider_feed_snapshots (retention_until, id);

COMMENT ON COLUMN provider_feed_snapshots.hash_schema IS 'Canonical content-hash schema the stored content_hash used. It covers each seed payload digest plus its manifest projection digest.';

CREATE TABLE provider_feed_snapshot_seeds (
    id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    stable_seed_key TEXT NOT NULL,
    locator TEXT NOT NULL,
    locator_display TEXT NOT NULL,
    title TEXT NOT NULL,
    published_at TIMESTAMPTZ,
    size_bytes BIGINT,
    info_hash TEXT,
    payload_digest TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    torrent_filename TEXT,
    torrent_state TEXT NOT NULL DEFAULT 'not_applicable',
    torrent_error_code TEXT,
    torrent_error_reason TEXT,
    torrent_version TEXT,
    info_hash_v1 TEXT,
    info_hash_v2 TEXT,
    payload BYTEA,
    payload_size_bytes BIGINT,
    manifest_payload_digest TEXT,
    manifest_torrent_parser_version TEXT,
    manifest_torrent_name TEXT,
    manifest_total_size_bytes BIGINT,
    manifest_file_count INTEGER,
    manifest_projection_digest TEXT,
    files JSONB NOT NULL DEFAULT '[]'::jsonb,
    CONSTRAINT provider_feed_snapshot_seeds_pkey PRIMARY KEY (id),
    CONSTRAINT provider_feed_snapshot_seeds_snapshot_id_stable_seed_key_key UNIQUE (snapshot_id, stable_seed_key),
    CONSTRAINT provider_feed_snapshot_seeds_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES provider_feed_snapshots (id) ON DELETE CASCADE,
    CONSTRAINT provider_feed_snapshot_seeds_check CHECK (payload_digest IS NULL OR size_bytes IS NOT NULL),
    CONSTRAINT provider_feed_snapshot_seeds_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT provider_feed_snapshot_seeds_info_hash_check CHECK (info_hash IS NULL OR info_hash ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    CONSTRAINT provider_feed_snapshot_seeds_info_hash_v1_check CHECK (info_hash_v1 IS NULL OR info_hash_v1 ~ '^[0-9a-f]{40}$'),
    CONSTRAINT provider_feed_snapshot_seeds_info_hash_v2_check CHECK (info_hash_v2 IS NULL OR info_hash_v2 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT provider_feed_snapshot_seeds_locator_check CHECK (char_length(btrim(locator)) BETWEEN 1 AND 2048),
    CONSTRAINT provider_feed_snapshot_seeds_locator_display_check CHECK (char_length(btrim(locator_display)) BETWEEN 1 AND 2048),
    CONSTRAINT provider_feed_snapshot_seeds_payload_digest_check CHECK (payload_digest IS NULL OR payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT provider_feed_snapshot_seeds_size_bytes_check CHECK (size_bytes IS NULL OR size_bytes BETWEEN 0 AND '1125899906842624'::bigint),
    CONSTRAINT provider_feed_snapshot_seeds_stable_seed_key_check CHECK (char_length(btrim(stable_seed_key)) BETWEEN 1 AND 256),
    CONSTRAINT provider_feed_snapshot_seeds_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 512),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_error_code_check CHECK (torrent_error_code IS NULL OR char_length(btrim(torrent_error_code)) BETWEEN 1 AND 64),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_error_reason_check CHECK (torrent_error_reason IS NULL OR char_length(btrim(torrent_error_reason)) BETWEEN 1 AND 512),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_filename_check CHECK (torrent_filename IS NULL OR char_length(btrim(torrent_filename)) BETWEEN 1 AND 512),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_state_check CHECK ((torrent_state = 'available' AND torrent_error_code IS NULL AND torrent_error_reason IS NULL) OR (torrent_state = 'unavailable' AND torrent_error_code IS NOT NULL) OR (torrent_state = 'not_applicable' AND torrent_error_code IS NULL AND torrent_error_reason IS NULL AND torrent_version IS NULL AND info_hash_v1 IS NULL AND info_hash_v2 IS NULL)),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_state_value_check CHECK (torrent_state IN ('available', 'unavailable', 'not_applicable')),
    CONSTRAINT provider_feed_snapshot_seeds_torrent_version_check CHECK (torrent_version IS NULL OR torrent_version IN ('v1', 'v2', 'hybrid')),
    CONSTRAINT provider_feed_snapshot_seeds_payload_check CHECK ((payload IS NULL) = (payload_size_bytes IS NULL) AND (payload IS NULL) = (payload_digest IS NULL) AND (payload IS NULL OR (octet_length(payload) BETWEEN 1 AND 20971520 AND payload_size_bytes = octet_length(payload) AND payload_digest = 'sha256:' || encode(sha256(payload), 'hex')))),
    CONSTRAINT provider_feed_snapshot_seeds_manifest_check CHECK ((manifest_torrent_parser_version IS NULL) = (manifest_torrent_name IS NULL) AND (manifest_torrent_parser_version IS NULL) = (manifest_payload_digest IS NULL) AND (manifest_torrent_parser_version IS NULL) = (manifest_total_size_bytes IS NULL) AND (manifest_torrent_parser_version IS NULL) = (manifest_file_count IS NULL) AND (manifest_torrent_parser_version IS NULL) = (manifest_projection_digest IS NULL) AND (torrent_state = 'available') = (manifest_torrent_parser_version IS NOT NULL) AND (manifest_torrent_parser_version IS NOT NULL OR files = '[]'::jsonb) AND (manifest_torrent_parser_version IS NULL OR (char_length(btrim(manifest_torrent_parser_version)) BETWEEN 1 AND 64 AND char_length(btrim(manifest_torrent_name)) BETWEEN 1 AND 512 AND manifest_total_size_bytes BETWEEN 0 AND '1125899906842624'::bigint AND manifest_file_count BETWEEN 1 AND 20000 AND manifest_file_count = jsonb_array_length(files) AND manifest_payload_digest ~ '^sha256:[0-9a-f]{64}$' AND manifest_projection_digest ~ '^sha256:[0-9a-f]{64}$'))),
    CONSTRAINT provider_feed_snapshot_seeds_files_check CHECK (jsonb_typeof(files) = 'array' AND pg_column_size(files) <= 16777216)
);

CREATE INDEX provider_feed_snapshot_seeds_snapshot_idx ON provider_feed_snapshot_seeds (snapshot_id, stable_seed_key, id);
CREATE TABLE anime_config_candidates (
    id TEXT NOT NULL,
    origin TEXT NOT NULL,
    origin_entry_ref TEXT,
    title TEXT NOT NULL,
    title_override TEXT,
    instance_label TEXT,
    draft JSONB NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    activated_managed_anime_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    draft_digest TEXT NOT NULL,
    last_activity TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'working',
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    current_revision BIGINT,
    current_revision_digest TEXT,
    cleanup_eligible_at TIMESTAMPTZ,
    cleanup_reason TEXT,
    CONSTRAINT anime_config_candidates_pkey PRIMARY KEY (id),
    CONSTRAINT anime_config_candidates_activated_managed_anime_id_fkey FOREIGN KEY (activated_managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT anime_config_candidates_cleanup_eligibility_check CHECK ((cleanup_eligible_at IS NULL AND cleanup_reason IS NULL) OR (cleanup_eligible_at IS NOT NULL AND cleanup_reason IN ('user_deleted', 'provider_owner_retired') AND status = 'archived' AND activated_managed_anime_id IS NULL AND (cleanup_reason <> 'provider_owner_retired' OR origin IN ('anibt', 'mikan')))),
    CONSTRAINT anime_config_candidates_current_revision_check CHECK (current_revision IS NULL OR current_revision >= 1),
    CONSTRAINT anime_config_candidates_current_revision_digest_check CHECK ((current_revision IS NULL AND current_revision_digest IS NULL) OR (current_revision IS NOT NULL AND current_revision_digest ~ '^sha256:[0-9a-f]{64}$')),
    CONSTRAINT anime_config_candidates_digest_check CHECK (draft_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT anime_config_candidates_draft_check CHECK (jsonb_typeof(draft) = 'object' AND pg_column_size(draft) <= 1048576),
    CONSTRAINT anime_config_candidates_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT anime_config_candidates_instance_label_check CHECK (instance_label IS NULL OR char_length(btrim(instance_label)) BETWEEN 1 AND 256),
    CONSTRAINT anime_config_candidates_lifecycle_check CHECK ((status = 'working' AND activated_managed_anime_id IS NULL) OR (status = 'dormant' AND activated_managed_anime_id IS NOT NULL) OR (status = 'archived' AND activated_managed_anime_id IS NULL)),
    CONSTRAINT anime_config_candidates_origin_check CHECK (origin IN ('anibt', 'mikan', 'custom', 'oneshot')),
    CONSTRAINT anime_config_candidates_origin_entry_ref_check CHECK (origin_entry_ref IS NULL OR char_length(btrim(origin_entry_ref)) BETWEEN 1 AND 256),
    CONSTRAINT anime_config_candidates_origin_shape_check CHECK (origin NOT IN ('custom', 'oneshot') OR (origin_entry_ref IS NULL AND instance_label IS NOT NULL)),
    CONSTRAINT anime_config_candidates_provider_ref_check CHECK ((origin IN ('custom', 'oneshot') AND origin_entry_ref IS NULL) OR (origin IN ('anibt', 'mikan') AND origin_entry_ref IS NOT NULL)),
    CONSTRAINT anime_config_candidates_status_check CHECK (status IN ('working', 'dormant', 'archived')),
    CONSTRAINT anime_config_candidates_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 512),
    CONSTRAINT anime_config_candidates_title_override_check CHECK (title_override IS NULL OR char_length(btrim(title_override)) BETWEEN 1 AND 512),
    CONSTRAINT anime_config_candidates_version_check CHECK (version >= 1)
);

CREATE INDEX anime_config_candidates_activated_idx ON anime_config_candidates (activated_managed_anime_id)
    WHERE activated_managed_anime_id IS NOT NULL;
CREATE INDEX anime_config_candidates_cleanup_eligible_idx ON anime_config_candidates (cleanup_eligible_at, id)
    WHERE cleanup_eligible_at IS NOT NULL;
CREATE UNIQUE INDEX anime_config_candidates_working_provider_uq ON anime_config_candidates (origin, origin_entry_ref)
    WHERE status = 'working' AND activated_managed_anime_id IS NULL AND origin IN ('anibt', 'mikan') AND origin_entry_ref IS NOT NULL;

COMMENT ON COLUMN anime_config_candidates.retention_until IS 'Compatibility/read metadata only. It is not cleanup authorization for stable candidates.';
COMMENT ON COLUMN anime_config_candidates.current_revision IS 'Pointer to the current immutable saved revision; NULL classifies a legacy candidate as revision_backfill_required (re-save required).';
COMMENT ON COLUMN anime_config_candidates.current_revision_digest IS 'Mirror of the current revision effective_digest; enforced by trigger so a pointer can never disagree with its revision body.';
COMMENT ON COLUMN anime_config_candidates.cleanup_eligible_at IS 'Explicit local candidate cleanup authorization. NULL means retention/idle time alone can never delete this stable candidate.';
COMMENT ON COLUMN anime_config_candidates.cleanup_reason IS 'Explicit cleanup authority: user_deleted or provider_owner_retired. Never inferred from origin, last_activity, or retention_until.';

CREATE TABLE anime_config_candidate_revisions (
    candidate_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    draft JSONB NOT NULL,
    draft_digest TEXT NOT NULL,
    effective JSONB NOT NULL,
    section_versions JSONB NOT NULL,
    section_schemas JSONB NOT NULL,
    effective_digest TEXT NOT NULL,
    parser_policy JSONB NOT NULL,
    parser_policy_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    business_snapshot JSONB,
    business_digest TEXT,
    CONSTRAINT anime_config_candidate_revisions_pkey PRIMARY KEY (candidate_id, revision),
    CONSTRAINT anime_config_candidate_revisions_candidate_id_fkey FOREIGN KEY (candidate_id) REFERENCES anime_config_candidates (id) ON DELETE CASCADE,
    CONSTRAINT anime_config_candidate_revisions_draft_check CHECK (jsonb_typeof(draft) = 'object' AND pg_column_size(draft) <= 1048576),
    CONSTRAINT anime_config_candidate_revisions_draft_digest_check CHECK (draft_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT anime_config_candidate_revisions_effective_check CHECK (jsonb_typeof(effective) = 'object' AND pg_column_size(effective) <= 1048576),
    CONSTRAINT anime_config_candidate_revisions_effective_digest_check CHECK (effective_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT anime_config_candidate_revisions_parser_policy_check CHECK (jsonb_typeof(parser_policy) = 'object' AND pg_column_size(parser_policy) <= 65536),
    CONSTRAINT anime_config_candidate_revisions_parser_policy_digest_check CHECK (parser_policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT anime_config_candidate_revisions_revision_check CHECK (revision >= 1),
    CONSTRAINT anime_config_candidate_revisions_section_schemas_check CHECK (jsonb_typeof(section_schemas) = 'object'),
    CONSTRAINT anime_config_candidate_revisions_section_versions_check CHECK (jsonb_typeof(section_versions) = 'object'),
    CONSTRAINT candidate_business_snapshot_pair CHECK ((business_snapshot IS NULL AND business_digest IS NULL) OR (jsonb_typeof(business_snapshot) = 'object' AND business_digest ~ '^sha256:[a-f0-9]{64}$'))
);

CREATE INDEX anime_config_candidate_revisions_created_idx ON anime_config_candidate_revisions (candidate_id, created_at DESC, revision DESC);
CREATE INDEX anime_config_candidate_revisions_effective_digest_idx ON anime_config_candidate_revisions (effective_digest);

COMMENT ON TABLE anime_config_candidate_revisions IS 'Append-only immutable effective-configuration revisions for planning candidates. Rows are never updated; they are removed only with their owning candidate.';
COMMENT ON COLUMN anime_config_candidate_revisions.effective IS 'Complete resolved whole-section values keyed by section_name, resolved at save time. Inherit is resolved here and never re-read from current globals.';
COMMENT ON COLUMN anime_config_candidate_revisions.section_versions IS 'section_name -> resolved source version (0 means the candidate override won, otherwise the Global row version or 1 for a code default).';
COMMENT ON COLUMN anime_config_candidate_revisions.section_schemas IS 'section_name -> typed schema version pinning the stored section value shape.';
COMMENT ON COLUMN anime_config_candidate_revisions.effective_digest IS 'Canonical sha256 over {effective, section_versions, section_schemas}; identifies the exact resolved configuration.';
COMMENT ON COLUMN anime_config_candidate_revisions.parser_policy IS 'Frozen parser-policy envelope: selected kind, default_offset, source/profile/rules versions and provenance. The parser axis of the revision identity.';
COMMENT ON COLUMN anime_config_candidate_revisions.parser_policy_digest IS 'Canonical sha256 over parser_policy; recomputed on every read and required to equal the stored value.';

-- One configuration row per ManagedAnime. It replaces anime_config_owners
-- (rules/subgroups versions), the three instance_* policy snapshots and
-- instance_business_snapshots, so a single-item PATCH and the frozen business
-- read/write the same row. Each axis keeps its own CAS version.
CREATE TABLE anime_business_configs (
    managed_anime_id TEXT NOT NULL,
    rules_version BIGINT NOT NULL DEFAULT 1,
    subgroups_version BIGINT NOT NULL DEFAULT 1,
    parser_kind TEXT,
    parser_source_default_version BIGINT,
    parser_version BIGINT,
    admission_mode TEXT,
    quality_profile JSONB,
    cleanup_policy JSONB,
    retention_source_default_version BIGINT,
    retention_version BIGINT,
    unclassified_route TEXT,
    file_effect_source_default_version BIGINT,
    file_effect_version BIGINT,
    candidate_id TEXT,
    revision BIGINT,
    business_snapshot JSONB,
    business_digest TEXT,
    business_version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT anime_business_configs_pkey PRIMARY KEY (managed_anime_id),
    CONSTRAINT anime_business_configs_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT anime_business_configs_candidate_revision_fkey FOREIGN KEY (candidate_id, revision) REFERENCES anime_config_candidate_revisions (candidate_id, revision),
    CONSTRAINT anime_business_configs_rules_version_check CHECK (rules_version >= 1),
    CONSTRAINT anime_business_configs_subgroups_version_check CHECK (subgroups_version >= 1),
    CONSTRAINT anime_business_configs_parser_check CHECK (
        (parser_kind IS NULL AND parser_source_default_version IS NULL AND parser_version IS NULL)
        OR (parser_kind IS NOT NULL AND (char_length(btrim(parser_kind)) BETWEEN 1 AND 64) IS TRUE
            AND parser_source_default_version IS NOT NULL AND parser_source_default_version >= 1
            AND parser_version IS NOT NULL AND parser_version >= 1)
    ),
    CONSTRAINT anime_business_configs_retention_check CHECK (
        (admission_mode IS NULL AND quality_profile IS NULL AND cleanup_policy IS NULL AND retention_source_default_version IS NULL AND retention_version IS NULL)
        OR (admission_mode IN ('all', 'per_group', 'single_copy')
            AND (retention_cleanup_policy_valid(cleanup_policy) AND pg_column_size(cleanup_policy) <= 65536) IS TRUE
            AND (retention_quality_profile_valid(quality_profile) AND pg_column_size(quality_profile) <= 65536) IS TRUE
            AND retention_source_default_version IS NOT NULL AND retention_source_default_version >= 1
            AND retention_version IS NOT NULL AND retention_version >= 1)
    ),
    CONSTRAINT anime_business_configs_file_effect_check CHECK (
        (unclassified_route IS NULL AND file_effect_source_default_version IS NULL AND file_effect_version IS NULL)
        OR ((unclassified_route IN ('route', 'skip')) IS TRUE
            AND file_effect_source_default_version IS NOT NULL AND file_effect_source_default_version >= 0
            AND file_effect_version IS NOT NULL AND file_effect_version >= 1)
    ),
    CONSTRAINT anime_business_configs_business_check CHECK (
        (candidate_id IS NULL AND revision IS NULL AND business_snapshot IS NULL AND business_digest IS NULL)
        OR (candidate_id IS NOT NULL AND revision IS NOT NULL AND revision >= 1
            AND business_snapshot IS NOT NULL AND jsonb_typeof(business_snapshot) = 'object'
            AND business_digest IS NOT NULL AND business_digest ~ '^sha256:[a-f0-9]{64}$')
    ),
    CONSTRAINT anime_business_configs_business_version_check CHECK (business_version >= 0)
);

CREATE TABLE subscription_log_entries (
    id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    outcome TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    rss_source_id TEXT,
    release_id TEXT,
    group_ref TEXT,
    details JSONB,
    dedupe_key TEXT NOT NULL,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reason_code TEXT,
    subject_type TEXT,
    subject_id TEXT,
    subject_version BIGINT,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    CONSTRAINT subscription_log_entries_pkey PRIMARY KEY (id),
    CONSTRAINT subscription_log_entries_subscription_id_dedupe_key_key UNIQUE (subscription_id, dedupe_key),
    CONSTRAINT subscription_log_entries_rss_source_id_fkey FOREIGN KEY (rss_source_id) REFERENCES rss_sources (id),
    CONSTRAINT subscription_log_entries_subscription_id_managed_anime__fkey FOREIGN KEY (subscription_id, managed_anime_id) REFERENCES subscriptions (id, managed_anime_id),
    CONSTRAINT subscription_log_activity_envelope_bounds CHECK ((reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128) AND (subject_type IS NULL OR char_length(btrim(subject_type)) BETWEEN 1 AND 128) AND (subject_id IS NULL OR char_length(btrim(subject_id)) BETWEEN 1 AND 128) AND (subject_version IS NULL OR subject_version >= 1) AND (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128) AND (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128) AND (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128)),
    CONSTRAINT subscription_log_entries_check CHECK (retention_until <= (occurred_at + '30 days'::interval)),
    CONSTRAINT subscription_log_entries_dedupe_key_check CHECK (char_length(btrim(dedupe_key)) BETWEEN 1 AND 512),
    CONSTRAINT subscription_log_entries_details_check CHECK (details IS NULL OR (jsonb_typeof(details) = 'object' AND pg_column_size(details) <= 262144)),
    CONSTRAINT subscription_log_entries_group_ref_check CHECK (group_ref IS NULL OR char_length(btrim(group_ref)) BETWEEN 1 AND 256),
    CONSTRAINT subscription_log_entries_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT subscription_log_entries_kind_check CHECK (kind IN ('subscription_created', 'subscription_stopped', 'subscription_resumed', 'metadata_applied', 'group_changed', 'policy_changed', 'rss_new_content', 'rss_refresh_failed', 'release_processed', 'download_changed', 'artifact_verified', 'placement_changed', 'replacement_decided', 'cleanup_completed', 'file_alignment_completed', 'file_alignment_failed', 'acquisition_settled')),
    CONSTRAINT subscription_log_entries_outcome_check CHECK (outcome IN ('info', 'success', 'failure')),
    CONSTRAINT subscription_log_entries_summary_check CHECK (char_length(btrim(summary)) BETWEEN 1 AND 4096),
    CONSTRAINT subscription_log_entries_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 512)
);

CREATE INDEX subscription_log_entries_activity_correlation_idx ON subscription_log_entries (correlation_id, occurred_at DESC, id)
    WHERE correlation_id IS NOT NULL;
CREATE INDEX subscription_log_entries_activity_subject_idx ON subscription_log_entries (subject_type, subject_id, subject_version, occurred_at DESC, id);
CREATE INDEX subscription_log_entries_retention_idx ON subscription_log_entries (retention_until, id);
CREATE INDEX subscription_log_entries_subscription_idx ON subscription_log_entries (subscription_id, occurred_at DESC, id);
-- The run log pages the whole history by time, newest first.
CREATE INDEX subscription_log_entries_record_idx ON subscription_log_entries (occurred_at DESC, id DESC);

CREATE FUNCTION anime_config_candidate_delete_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.cleanup_eligible_at IS NULL OR OLD.cleanup_reason IS NULL THEN
        RAISE EXCEPTION 'candidate deletion requires explicit cleanup eligibility'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_cleanup_eligibility_check';
    END IF;
    RETURN OLD;
END;
$$;

CREATE FUNCTION anime_config_candidate_identity_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
    draft_origin TEXT;
    draft_ref JSONB;
    draft_label JSONB;
    draft_title TEXT;
    top_key TEXT;
    allowed_keys TEXT[] := ARRAY[
        'instance_label', 'metadata', 'origin', 'origin_entry_ref',
        'rss_sources', 'rules', 'subgroups', 'title', 'title_override'
    ];
BEGIN
    IF draft IS NULL OR jsonb_typeof(draft) <> 'object' THEN
        RETURN FALSE;
    END IF;
    FOR top_key IN SELECT key FROM jsonb_object_keys(draft) AS key
    LOOP
        IF NOT (top_key = ANY(allowed_keys)) THEN
            RETURN FALSE;
        END IF;
    END LOOP;
    draft_origin := draft ->> 'origin';
    IF draft_origin IS NULL OR draft_origin NOT IN ('anibt', 'mikan', 'custom', 'oneshot') THEN
        RETURN FALSE;
    END IF;
    IF draft_origin <> origin THEN
        RETURN FALSE;
    END IF;
    draft_title := draft ->> 'title';
    IF draft_title IS NULL OR BTRIM(draft_title) = '' OR LENGTH(draft_title) > 512 THEN
        RETURN FALSE;
    END IF;
    draft_ref := draft -> 'origin_entry_ref';
    IF draft_origin IN ('anibt', 'mikan') THEN
        IF origin_entry_ref IS NULL OR BTRIM(origin_entry_ref) = '' THEN
            RETURN FALSE;
        END IF;
        IF jsonb_typeof(draft_ref) IS DISTINCT FROM 'string'
           OR text_trim_go_ws(draft_ref #>> '{}') = ''
           OR text_trim_go_ws(draft_ref #>> '{}') IS DISTINCT FROM origin_entry_ref THEN
            RETURN FALSE;
        END IF;
    ELSE
        IF origin_entry_ref IS NOT NULL THEN
            RETURN FALSE;
        END IF;
        IF draft_ref IS NOT NULL AND jsonb_typeof(draft_ref) IS DISTINCT FROM 'null' THEN
            RETURN FALSE;
        END IF;
        draft_label := draft -> 'instance_label';
        IF jsonb_typeof(draft_label) IS DISTINCT FROM 'string'
           OR text_trim_go_ws(draft_label #>> '{}') = ''
           OR instance_label IS NULL
           OR text_trim_go_ws(draft_label #>> '{}') IS DISTINCT FROM instance_label THEN
            RETURN FALSE;
        END IF;
    END IF;
    RETURN TRUE;
END;
$$;

CREATE FUNCTION anime_config_candidate_revision_body_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    canonical_sections TEXT[] := ARRAY[
        'subgroups', 'parser', 'release_rules', 'file_rules',
        'naming_rules', 'file_effect'
    ];
    next_revision BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'candidate revision rows are immutable'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_immutable_check';
    END IF;
    SELECT COALESCE(MAX(revision), 0) + 1 INTO next_revision
    FROM anime_config_candidate_revisions
    WHERE candidate_id = NEW.candidate_id;
    IF NEW.revision <> next_revision THEN
        RAISE EXCEPTION 'candidate revision % must be the next contiguous revision %', NEW.revision, next_revision
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_progression_check';
    END IF;
    IF NOT (NEW.effective ?& canonical_sections)
       OR (SELECT count(*) FROM jsonb_object_keys(NEW.effective)) <> cardinality(canonical_sections) THEN
        RAISE EXCEPTION 'candidate revision effective must contain exactly the canonical section keys'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_sections_check';
    END IF;
    IF NOT (NEW.section_versions ?& canonical_sections)
       OR (SELECT count(*) FROM jsonb_object_keys(NEW.section_versions)) <> cardinality(canonical_sections) THEN
        RAISE EXCEPTION 'candidate revision section_versions must contain exactly the canonical section keys'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_versions_check';
    END IF;
    IF NOT (NEW.section_schemas ?& canonical_sections)
       OR (SELECT count(*) FROM jsonb_object_keys(NEW.section_schemas)) <> cardinality(canonical_sections) THEN
        RAISE EXCEPTION 'candidate revision section_schemas must contain exactly the canonical section keys'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_schemas_check';
    END IF;
    IF NEW.parser_policy->>'schema_version' IS DISTINCT FROM 'parser-policy-envelope-v1' THEN
        RAISE EXCEPTION 'candidate revision parser_policy schema_version is invalid'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_parser_schema_check';
    END IF;
    IF COALESCE(btrim(NEW.parser_policy->>'kind'), '') = '' THEN
        RAISE EXCEPTION 'candidate revision parser_policy kind is required'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_parser_kind_check';
    END IF;
    IF COALESCE(btrim(NEW.parser_policy->>'source_owner'), '') = '' THEN
        RAISE EXCEPTION 'candidate revision parser_policy source_owner is required'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_parser_source_check';
    END IF;
    IF NEW.parser_policy ? 'default_offset' THEN
        RAISE EXCEPTION 'candidate revision parser_policy must not carry an episode offset'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_parser_offset_check';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION anime_config_candidate_revisions_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'candidate revision rows are immutable'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_immutable_check';
    END IF;
    IF EXISTS (SELECT 1 FROM anime_config_candidates WHERE id = OLD.candidate_id) THEN
        RAISE EXCEPTION 'candidate revision rows are removed only with their owning candidate'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidate_revisions_owner_delete_check';
    END IF;
    RETURN OLD;
END;
$$;

CREATE FUNCTION anime_config_candidates_current_revision_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    referenced_digest TEXT;
BEGIN
    IF NEW.current_revision IS NULL THEN
        IF NEW.current_revision_digest IS NOT NULL THEN
            RAISE EXCEPTION 'current_revision_digest requires current_revision'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'anime_config_candidates_current_revision_check';
        END IF;
        RETURN NEW;
    END IF;
    SELECT effective_digest INTO referenced_digest
    FROM anime_config_candidate_revisions
    WHERE candidate_id = NEW.id AND revision = NEW.current_revision;
    IF referenced_digest IS NULL THEN
        RAISE EXCEPTION 'current_revision does not reference an existing candidate revision'
            USING ERRCODE = '23503',
                  CONSTRAINT = 'anime_config_candidates_current_revision_fk';
    END IF;
    IF NEW.current_revision_digest IS DISTINCT FROM referenced_digest THEN
        RAISE EXCEPTION 'current_revision_digest does not match the referenced revision'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_current_revision_digest_check';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION anime_config_candidates_defaults() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    computed_digest TEXT;
BEGIN
    computed_digest := 'sha256:' || encode(
        digest(convert_to(anime_config_candidate_canonical_json(NEW.draft), 'UTF8'), 'sha256'),
        'hex'
    );
    IF NULLIF(BTRIM(NEW.draft_digest), '') IS NOT NULL
       AND NEW.draft_digest <> computed_digest THEN
        RAISE EXCEPTION 'candidate draft digest does not match canonical draft'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_digest_check';
    END IF;
    NEW.draft_digest := computed_digest;
    IF NEW.last_activity IS NULL THEN
        NEW.last_activity := CURRENT_TIMESTAMP;
    END IF;
    IF NEW.status IS NULL OR (TG_OP = 'INSERT' AND NEW.status = 'working' AND NEW.activated_managed_anime_id IS NOT NULL) THEN
        NEW.status := CASE WHEN NEW.activated_managed_anime_id IS NULL THEN 'working' ELSE 'dormant' END;
    END IF;
    IF NEW.retention_until IS NULL THEN
        NEW.retention_until := CURRENT_TIMESTAMP + INTERVAL '30 days';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION anime_config_candidates_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- Fixture/migration opt-out: tests that seed pre-contract placeholder rows
    -- (e.g. the historical '{}' draft shape) set this local GUC for their
    -- session. The application never sets it, so production writes always take
    -- the guarded path.
    IF current_setting('bansoko.candidate_fixture', TRUE) = 'on' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NOT anime_config_candidate_draft_shape_ok(NEW.draft, NEW.origin, NEW.origin_entry_ref, NEW.instance_label) THEN
            RAISE EXCEPTION 'candidate draft JSON is not a decodable typed anime draft'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'anime_config_candidates_draft_decodable_check';
        END IF;
        RETURN NEW;
    END IF;

    -- UPDATE: version moves exactly one step forward on every mutation, so no
    -- direct SQL write can silently replace a draft at the same version or
    -- rewind the CAS counter.
    IF NEW.version IS DISTINCT FROM OLD.version AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'candidate version must advance by exactly one'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_version_check';
    END IF;
    -- A linked (dormant) row is lifecycle evidence: its link, status, identity
    -- and draft are immutable; only activity/retention bookkeeping may move.
    -- Revival/archive goes through the unlink path (link cleared) only.
    IF OLD.activated_managed_anime_id IS NOT NULL AND NEW.activated_managed_anime_id IS NOT NULL
       AND (NEW.activated_managed_anime_id IS DISTINCT FROM OLD.activated_managed_anime_id
            OR NEW.status IS DISTINCT FROM OLD.status
            OR NEW.origin IS DISTINCT FROM OLD.origin
            OR NEW.origin_entry_ref IS DISTINCT FROM OLD.origin_entry_ref
            OR NEW.draft IS DISTINCT FROM OLD.draft) THEN
        RAISE EXCEPTION 'linked candidate identity and status are immutable'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_linked_identity_check';
    END IF;
    -- Newly written unlinked rows must keep an application-decodable draft.
    -- The guard binds only when the draft body or identity columns change, so
    -- rows that predate it (legacy fixtures/backfills, e.g. the historical
    -- '{}' placeholder shape) can still receive lifecycle-only updates
    -- (status/link/retention) without being forced through revalidation, while
    -- no new unreadable draft or identity can be written.
    IF (NEW.draft IS DISTINCT FROM OLD.draft
        OR NEW.origin IS DISTINCT FROM OLD.origin
        OR NEW.origin_entry_ref IS DISTINCT FROM OLD.origin_entry_ref)
       AND NOT anime_config_candidate_draft_shape_ok(NEW.draft, NEW.origin, NEW.origin_entry_ref, NEW.instance_label) THEN
        RAISE EXCEPTION 'candidate draft JSON is not a decodable typed anime draft'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'anime_config_candidates_draft_decodable_check';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION assert_feed_snapshot_set(p_id text) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE h RECORD; v_seed RECORD; v_count BIGINT; v_refs BIGINT; v_paths BIGINT; v_ords BIGINT;
    v_size NUMERIC; v_min INTEGER; v_max INTEGER; v_canonical TEXT; v_digest TEXT;
BEGIN
    SELECT hash_schema,content_hash,seed_count INTO h FROM public.provider_feed_snapshots WHERE id=p_id;
    IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_FINAL_MISSING'; END IF;
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
    SELECT '{"schema":"provider-feed-snapshot-v1","seeds":['
        || COALESCE(string_agg(canonical,',' ORDER BY canonical COLLATE "C"),'') || ']}' INTO v_canonical
    FROM (SELECT public.feed_seed_canonical(to_jsonb(s),s.manifest_projection_digest) AS canonical
        FROM public.provider_feed_snapshot_seeds s WHERE s.snapshot_id=p_id) ordered;
    IF h.content_hash IS DISTINCT FROM 'sha256:' || encode(sha256(convert_to(v_canonical,'UTF8')),'hex') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FEED_SNAPSHOT_CONTENT_HASH';
    END IF;
END;
$_$;

CREATE FUNCTION assert_managed_anime_active(p_managed_anime_id text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_state TEXT;
BEGIN
    IF p_managed_anime_id IS NULL THEN
        RETURN;
    END IF;
    -- KEY SHARE serializes with retirement's FOR UPDATE on the same row. An
    -- absent owner is left to the caller's FK: a cleaned id has no row to
    -- reference.
    SELECT anime.lifecycle_state INTO v_state
    FROM managed_anime anime
    WHERE anime.id = p_managed_anime_id
    FOR KEY SHARE;
    IF FOUND AND v_state = 'retired' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
    END IF;
END;
$$;

CREATE FUNCTION assert_queued_preparation_witness(p_q jsonb, p_business jsonb, p_request text, p_input text, p_config text, p_mode text, p_canonical text, p_digest text, p_max_bytes integer, p_max_rows integer) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
 w JSONB; policy JSONB; locked JSONB; g JSONB; d JSONB; entry JSONB;
 refs TEXT[]; decision_refs TEXT[]; selected TEXT[]; actual_selected TEXT[]:=ARRAY[]::TEXT[];
 provider TEXT; expected_mode TEXT; expected_count BOOLEAN; observed BOOLEAN;
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
  IF NOT public.business_exact_keys(d,ARRAY['group_ref','display_name','available','canonical_key','mode','score','count_in_limit','policy_origin','rank','outcome','reason_code'])
   OR NOT public.business_exact_keys(d->'policy_origin',ARRAY['layer','config_version'])
   OR NOT public.business_exact_keys(d->'display_name',ARRAY['state','value','reason_code'])
   OR d->'canonical_key' IS DISTINCT FROM '{"state":"absent","value":null,"reason_code":null}'::jsonb
   OR jsonb_typeof(d->'available') IS DISTINCT FROM 'boolean' OR jsonb_typeof(d->'count_in_limit') IS DISTINCT FROM 'boolean'
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
   expected_score:=CASE WHEN expected_mode='ranked' THEN (entry->>'score')::bigint ELSE 0 END;
   expected_count:=CASE WHEN expected_mode='ranked' THEN true WHEN expected_mode='never' THEN false ELSE (entry->>'count_in_limit')::boolean END;
  ELSE
   expected_mode:=CASE WHEN policy->'provider_defaults'->provider->>'mode'='whitelist' THEN 'never' ELSE 'ranked' END;
   expected_count:=expected_mode='ranked';
   expected_score:=CASE WHEN expected_mode='never' THEN 0 ELSE (d->>'score')::bigint END;
   IF jsonb_typeof(policy->'provider_defaults'->provider)='object' THEN
    expected_version:=(p_business->'section_versions'->>'subgroups')::bigint;
    IF (policy->'provider_defaults'->provider->'groups') ? (d->>'group_ref') THEN
     expected_layer:=CASE WHEN p_q->'config'->>'kind'='submitted' THEN 'submitted_exact' ELSE 'frozen_exact' END;
     expected_mode:=CASE WHEN policy->'provider_defaults'->provider->>'mode'='whitelist' THEN 'ranked' ELSE 'never' END;
     expected_count:=expected_mode='ranked';
     expected_score:=CASE WHEN expected_mode='ranked' THEN (policy->'provider_defaults'->provider->>'score')::bigint ELSE 0 END;
    END IF;
   END IF;
  END IF;
  IF d->>'mode' IS DISTINCT FROM expected_mode OR (d->>'score')::bigint IS DISTINCT FROM expected_score
   OR (d->>'count_in_limit')::boolean IS DISTINCT FROM expected_count
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
  ELSIF expected_mode='always' THEN outcome:='selected';reason:='policy_always';IF expected_count THEN remaining:=remaining-1;END IF;
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

CREATE FUNCTION cleanup_managed_anime_local_data(p_managed_anime_id text, p_idempotency_key text, p_request_hash text, p_expected_lifecycle_epoch bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE
    existing_hash TEXT;
    existing_epoch BIGINT;
    existing_total BIGINT;
    existing_detail JSONB;
    lifecycle_state TEXT;
    lifecycle_epoch BIGINT;
    affected BIGINT;
    total BIGINT := 0;
    detail JSONB := '{}'::jsonb;

    owner_subscriptions TEXT;
    owner_sources TEXT;
    owner_releases TEXT;
    owner_download_tasks TEXT;
    owner_artifacts TEXT;
    owner_file_operations TEXT;
    owner_snapshots TEXT;

    cleanup_labels TEXT[];
    cleanup_steps TEXT[];
    step_index INT;

    root_row RECORD;
    disposition TEXT;
    compacted JSONB;
    roots_cancelled BIGINT := 0;
    roots_compacted BIGINT := 0;
BEGIN
    IF p_managed_anime_id IS NULL OR btrim(p_managed_anime_id) = '' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_FOUND';
    END IF;

    -- Replay lives in idempotency_records keyed by owner_id (never by the
    -- managed_anime_id FK column) so it survives the owner delete.
    SELECT rec.request_hash, (rec.result_snapshot ->> 'lifecycle_epoch')::bigint,
           (rec.result_snapshot ->> 'deleted_total')::bigint, rec.result_snapshot -> 'deleted_detail'
    INTO existing_hash, existing_epoch, existing_total, existing_detail
    FROM idempotency_records rec
    WHERE rec.kind = 'local_cleanup' AND rec.owner_type = 'managed_anime'
      AND rec.owner_id = p_managed_anime_id AND rec.idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing_hash <> p_request_hash OR existing_epoch <> p_expected_lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'IDEMPOTENCY_KEY_REUSED';
        END IF;
        RETURN jsonb_build_object(
            'status', 'replayed',
            'deleted_total', existing_total,
            'deleted_detail', existing_detail);
    END IF;

    SELECT anime.lifecycle_state, anime.fence_epoch INTO lifecycle_state, lifecycle_epoch
    FROM managed_anime anime
    WHERE anime.id = p_managed_anime_id
    FOR UPDATE;
    IF NOT FOUND THEN
        -- A concurrent cleanup with the same key may have committed while this
        -- transaction waited on the owner row.
        SELECT rec.request_hash, (rec.result_snapshot ->> 'lifecycle_epoch')::bigint,
               (rec.result_snapshot ->> 'deleted_total')::bigint, rec.result_snapshot -> 'deleted_detail'
        INTO existing_hash, existing_epoch, existing_total, existing_detail
        FROM idempotency_records rec
        WHERE rec.kind = 'local_cleanup' AND rec.owner_type = 'managed_anime'
          AND rec.owner_id = p_managed_anime_id AND rec.idempotency_key = p_idempotency_key;
        IF FOUND THEN
            IF existing_hash <> p_request_hash OR existing_epoch <> p_expected_lifecycle_epoch THEN
                RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'IDEMPOTENCY_KEY_REUSED';
            END IF;
            RETURN jsonb_build_object(
                'status', 'replayed',
                'deleted_total', existing_total,
                'deleted_detail', existing_detail);
        END IF;
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_FOUND';
    END IF;
    IF lifecycle_state <> 'retired' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_NOT_RETIRED';
    END IF;
    IF lifecycle_epoch <> p_expected_lifecycle_epoch THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_STALE_EPOCH';
    END IF;

    -- Transaction-local authorization read by the guards that only cleanup
    -- may pass (live work delete, owner-cleanup compaction, owner delete).
    PERFORM set_config('bansoko.local_cleanup_owner', p_managed_anime_id, true);

    UPDATE anime_config_candidates
    SET activated_managed_anime_id = NULL,
        status = 'archived',
        version = version + 1,
        updated_at = CURRENT_TIMESTAMP
    WHERE activated_managed_anime_id = p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('candidates_detached', affected);
        total := total + affected;
    END IF;

    owner_subscriptions :=
        'SELECT su.id FROM subscriptions su WHERE su.managed_anime_id = $1';
    owner_sources :=
        'SELECT rs.id FROM rss_sources rs WHERE rs.subscription_id IN (' || owner_subscriptions || ')';
    owner_releases :=
        'SELECT r.id FROM releases r WHERE r.rss_source_id IN (' || owner_sources || ')';
    owner_download_tasks :=
        'SELECT dt.id FROM download_tasks dt WHERE dt.release_id IN (' || owner_releases || ')';
    owner_artifacts :=
        'SELECT a.id FROM artifacts a WHERE a.download_task_id IN (' || owner_download_tasks || ')';
    owner_file_operations :=
        'SELECT fo.id FROM file_operations fo WHERE fo.artifact_id IN (' || owner_artifacts || ')';
    owner_snapshots :=
        'SELECT s.id FROM media_fact_snapshots s WHERE s.release_id IN (' || owner_releases || ')';

    -- The owner's own pins on root rows go first, so compact sees a root that
    -- only its own graph references.
    EXECUTE 'DELETE FROM idempotency_records WHERE kind = ''manual_retry'' AND ('
            || ' (owner_type = ''rss_source_runtime'' AND owner_id IN (' || owner_sources || '))'
            || ' OR (owner_type = ''download_task'' AND owner_id IN (' || owner_download_tasks || '))'
            || ' OR (owner_type = ''artifact'' AND owner_id IN (' || owner_artifacts || '))'
            || ' OR (owner_type = ''file_operation'' AND owner_id IN (' || owner_file_operations || '))'
            || ' OR failed_work_item_id IN (SELECT id FROM work_item_history WHERE managed_anime_id = $1)'
            || ' OR result_work_item_id IN (SELECT id FROM work_item_history WHERE managed_anime_id = $1)'
            || ')' USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('manual_retry_idempotency', affected);
        total := total + affected;
    END IF;
    EXECUTE 'DELETE FROM idempotency_records WHERE managed_anime_id = $1'
            || ' OR release_id IN (' || owner_releases || ')' USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('idempotency_records', affected);
        total := total + affected;
    END IF;
    EXECUTE 'DELETE FROM placement_obligations WHERE file_operation_id IN (' || owner_file_operations || ')'
        USING p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected > 0 THEN
        detail := detail || jsonb_build_object('placement_obligations', affected);
        total := total + affected;
    END IF;

    -- Every related root settles and compacts before the management graph goes.
    -- A running root of the retired owner settles to cancelled/OWNER_RETIRED;
    -- one whose admitted external effect is still unresolved keeps the cleanup
    -- refused. Nothing here touches qB, a downloaded byte or a library file.
    FOR root_row IN
        SELECT r.id, r.state, r.record_state FROM acquisitions r
        WHERE r.record_state = 'full'
          AND (r.origin_managed_anime_ref = p_managed_anime_id OR EXISTS(
               SELECT 1 FROM download_tasks d JOIN releases rel ON rel.id = d.release_id
               JOIN rss_sources src ON src.id = rel.rss_source_id
               JOIN subscriptions sub ON sub.id = src.subscription_id
               WHERE d.acquisition_id = r.id AND sub.managed_anime_id = p_managed_anime_id))
        ORDER BY r.id
        FOR UPDATE OF r
    LOOP
        IF root_row.state = 'running' THEN
            disposition := acquisition_dispose_retired(root_row.id);
            IF disposition <> 'cancelled' THEN
                RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_ACQUISITION_RETAINED',
                    DETAIL = 'acquisition ' || root_row.id || ': ' || disposition;
            END IF;
            roots_cancelled := roots_cancelled + 1;
        END IF;
        compacted := acquisition_compact(root_row.id, 'owner_cleanup');
        IF compacted ->> 'status' <> 'compacted' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_ACQUISITION_RETAINED',
                DETAIL = 'acquisition ' || root_row.id || ': ' || (compacted ->> 'status');
        END IF;
        roots_compacted := roots_compacted + 1;
        SELECT detail || COALESCE(jsonb_object_agg(e.key,
                   COALESCE((detail ->> e.key)::bigint, 0) + e.value::text::bigint), '{}'::jsonb),
               total + COALESCE(sum(e.value::text::bigint), 0)
        INTO detail, total
        FROM jsonb_each(compacted -> 'deleted') e;
    END LOOP;
    IF roots_cancelled > 0 THEN
        detail := detail || jsonb_build_object('acquisitions_cancelled', roots_cancelled);
    END IF;
    IF roots_compacted > 0 THEN
        detail := detail || jsonb_build_object('acquisitions_compacted', roots_compacted);
    END IF;

    -- Fixed leaf-first order of the management graph (unchanged from 098 apart
    -- from the pins removed above). Cross-owner/global facts, acquisition
    -- roots and their retained evidence, and audit events stay.
    cleanup_labels := ARRAY[
        'work_items',
        'work_item_outcomes',
        'routine_activities',
        'diagnostic_episodes',
        'offset_migration_plans',
        'offset_migration_generations',
        'acquisition_targets',
        'media_fact_files',
        'media_fact_snapshots',
        'file_operations',
        'artifact_files',
        'artifacts',
        'release_torrent_inputs',
        'release_candidate_facts',
        'subscription_log_entries',
        'download_tasks',
        'releases',
        'rss_source_runtime',
        'rss_sources',
        'provider_group_selections',
        'placements',
        'replacement_decisions',
        'cleanup_operations',
        'alignment_operations',
        'reconciliation_files',
        'reconciliation_scans',
        'anime_config_overrides',
        'anime_business_configs',
        'anime_metadata',
        'plugin_deliveries',
        'notification_admissions',
        'filemaintenance_policies',
        'subscriptions'
    ];
    cleanup_steps := ARRAY[
        'DELETE FROM work_items WHERE managed_anime_id = $1',
        'DELETE FROM work_item_outcomes WHERE managed_anime_id = $1',
        'DELETE FROM routine_activities WHERE managed_anime_id = $1'
            || ' OR (scope_kind = ''subscription'' AND scope_id IN (' || owner_subscriptions || '))'
            || ' OR (scope_kind = ''rss_source'' AND scope_id IN (' || owner_sources || '))',
        'DELETE FROM diagnostic_episodes WHERE managed_anime_id = $1'
            || ' OR (scope_kind = ''subscription'' AND scope_id IN (' || owner_subscriptions || '))'
            || ' OR (scope_kind = ''rss_source'' AND scope_id IN (' || owner_sources || '))',
        'DELETE FROM offset_migration_plans WHERE managed_anime_id = $1',
        'DELETE FROM offset_migration_generations WHERE managed_anime_id = $1',
        'DELETE FROM acquisition_targets WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM media_fact_files WHERE snapshot_id IN (' || owner_snapshots || ')',
        'DELETE FROM media_fact_snapshots WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM file_operations WHERE artifact_id IN (' || owner_artifacts || ')',
        'DELETE FROM artifact_files WHERE artifact_id IN (' || owner_artifacts || ')',
        'DELETE FROM artifacts WHERE download_task_id IN (' || owner_download_tasks || ')',
        'DELETE FROM release_torrent_inputs WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM release_candidate_facts WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM subscription_log_entries WHERE managed_anime_id = $1'
            || ' OR subscription_id IN (' || owner_subscriptions || ')'
            || ' OR release_id IN (' || owner_releases || ')',
        'DELETE FROM download_tasks WHERE release_id IN (' || owner_releases || ')',
        'DELETE FROM releases WHERE rss_source_id IN (' || owner_sources || ')',
        'DELETE FROM rss_source_runtime WHERE rss_source_id IN (' || owner_sources || ')',
        'DELETE FROM rss_sources WHERE subscription_id IN (' || owner_subscriptions || ')',
        'DELETE FROM provider_group_selections WHERE subscription_id IN (' || owner_subscriptions || ')',
        'DELETE FROM placements WHERE managed_anime_id = $1',
        'DELETE FROM replacement_decisions WHERE managed_anime_id = $1',
        'DELETE FROM cleanup_operations WHERE managed_anime_id = $1',
        'DELETE FROM alignment_operations WHERE managed_anime_id = $1',
        'DELETE FROM reconciliation_files WHERE managed_anime_id = $1',
        'DELETE FROM reconciliation_scans WHERE managed_anime_id = $1',
        'DELETE FROM anime_config_overrides WHERE managed_anime_id = $1',
        'DELETE FROM anime_business_configs WHERE managed_anime_id = $1',
        'DELETE FROM anime_metadata WHERE managed_anime_id = $1',
        'DELETE FROM plugin_deliveries WHERE managed_anime_id = $1',
        'DELETE FROM notification_admissions WHERE managed_anime_id = $1',
        'DELETE FROM filemaintenance_policies WHERE scope_key = ''anime:'' || $1',
        'DELETE FROM subscriptions WHERE managed_anime_id = $1'
    ];

    FOR step_index IN 1 .. array_length(cleanup_steps, 1) LOOP
        EXECUTE cleanup_steps[step_index] USING p_managed_anime_id;
        GET DIAGNOSTICS affected = ROW_COUNT;
        IF affected > 0 THEN
            detail := detail || jsonb_build_object(cleanup_labels[step_index], affected);
            total := total + affected;
        END IF;
    END LOOP;

    DELETE FROM managed_anime WHERE id = p_managed_anime_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'LOCAL_CLEANUP_OWNER_VANISHED';
    END IF;
    detail := detail || jsonb_build_object('managed_anime', affected);
    total := total + affected;

    INSERT INTO idempotency_records(kind, idempotency_key, request_hash, owner_type, owner_id, result_snapshot)
    VALUES ('local_cleanup', p_idempotency_key, p_request_hash, 'managed_anime', p_managed_anime_id,
            jsonb_build_object('lifecycle_epoch', lifecycle_epoch, 'deleted_total', total, 'deleted_detail', detail));
    PERFORM set_config('bansoko.local_cleanup_owner', '', true);

    RETURN jsonb_build_object(
        'status', 'performed',
        'deleted_total', total,
        'deleted_detail', detail);
END;
$_$;

COMMENT ON FUNCTION cleanup_managed_anime_local_data(p_managed_anime_id text, p_idempotency_key text, p_request_hash text, p_expected_lifecycle_epoch bigint) IS 'Owner-held SECURITY DEFINER physical local cleanup of one retired ManagedAnime. The runtime may only EXECUTE it; the receipt insert and the leaf-first graph delete happen under the owner role and the fixed search_path.';

CREATE FUNCTION guard_feed_snapshot_seal() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    PERFORM public.assert_feed_snapshot_set(NEW.id);
    RETURN NULL;
END;
$$;

CREATE FUNCTION guard_group_observation_immutable() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF (to_jsonb(NEW) - 'retention_until' - 'observation')
        IS DISTINCT FROM (to_jsonb(OLD) - 'retention_until' - 'observation') THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_IMMUTABLE';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION guard_group_observation_seal() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    v_header RECORD;
    v_count BIGINT;
BEGIN
    SELECT id, provider, catalog_entry_ref, observed_at, source_version, schema_version, version,
        group_count, observation, observation_digest, entries INTO v_header
    FROM public.provider_group_snapshots WHERE id=NEW.id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_FINAL_MISSING';
    END IF;
    SELECT count(*) INTO v_count FROM jsonb_array_elements(v_header.entries);
    IF v_count <> v_header.group_count THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INCOMPLETE_PROJECTION';
    END IF;
    IF v_header.observation IS NOT NULL AND (
        v_header.observation->>'snapshot_ref' IS DISTINCT FROM v_header.id
        OR v_header.observation->>'snapshot_version' IS DISTINCT FROM v_header.version::text
        OR v_header.observation->'scope'->>'provider' IS DISTINCT FROM v_header.provider
        OR v_header.observation->'scope'->>'catalog_entry_ref' IS DISTINCT FROM v_header.catalog_entry_ref
        OR (v_header.observation->>'observed_at')::timestamptz IS DISTINCT FROM v_header.observed_at
        OR v_header.observation->>'provider_schema' IS DISTINCT FROM v_header.schema_version
        OR jsonb_array_length(v_header.observation->'groups') IS DISTINCT FROM v_header.group_count
        OR COALESCE(v_header.observation->'source_version'->>'value','') IS DISTINCT FROM v_header.source_version
    ) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_HEADER_MISMATCH';
    END IF;
    IF v_header.observation IS NOT NULL AND EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_header.observation->'groups') g
        WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_header.entries) e
            WHERE e->>'group_ref'=g->>'group_ref' AND e->>'display_name'=g->>'display_name'
              AND (e->>'observed_at')::timestamptz=(g->>'observed_at')::timestamptz)
    ) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='GROUP_OBSERVATION_INCOMPLETE_PROJECTION';
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION guard_managed_anime_lifecycle() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- A retired ManagedAnime is removed only by the owner-held local
        -- cleanup transaction. Active rows (one-shot retention) stay deletable.
        IF OLD.lifecycle_state = 'retired'
           AND current_setting('bansoko.local_cleanup_owner', true) IS DISTINCT FROM OLD.id THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_DELETE_FORBIDDEN';
        END IF;
        RETURN OLD;
    END IF;

    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_IMMUTABLE';
    END IF;
    IF NEW.lifecycle_version < OLD.lifecycle_version THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_VERSION_REGRESSION';
    END IF;

    -- A retired ManagedAnime is frozen as a whole: lifecycle and owner fields.
    IF OLD.lifecycle_state = 'retired' THEN
        IF to_jsonb(NEW) IS DISTINCT FROM to_jsonb(OLD) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.lifecycle_state = 'active' THEN
        IF NEW.fence_epoch <> OLD.fence_epoch
           OR NEW.retired_at IS NOT NULL
           OR NEW.retired_subscription_version IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_TRANSITION_INVALID';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.retired_at IS NULL
       OR NEW.retired_subscription_version IS NULL
       OR NEW.lifecycle_version < OLD.lifecycle_version + 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIREMENT_FENCE_INVALID';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION anime_business_config_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 -- One row carries the frozen business and its digest: a single-item PATCH
 -- rewrites both, so the digest must always be the canonical hash of the
 -- stored snapshot. This replaces the old instance_business_revision_guard,
 -- which additionally required the body to equal one immutable candidate
 -- revision; ruling 3 makes that false after any single-item PATCH.
 IF NEW.business_snapshot IS NOT NULL
    AND NEW.business_digest IS DISTINCT FROM 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(NEW.business_snapshot),'UTF8'),'sha256'),'hex') THEN
  RAISE EXCEPTION 'business digest must match the stored snapshot' USING ERRCODE='23514', CONSTRAINT='anime_business_configs_digest_check';
 END IF;
 RETURN NEW;
END;
$$;

CREATE FUNCTION provider_feed_snapshot_append_only_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_TABLE_NAME = 'provider_feed_snapshots'
       AND (to_jsonb(NEW) - 'fetched_at' - 'fresh_until' - 'retention_until')
           = (to_jsonb(OLD) - 'fetched_at' - 'fresh_until' - 'retention_until') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION '% rows are append-only', TG_TABLE_NAME
        USING ERRCODE = '23514',
              CONSTRAINT = 'provider_feed_snapshots_append_only_check';
END;
$$;

CREATE FUNCTION provider_feed_snapshot_child_delete_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM provider_feed_snapshots WHERE id = OLD.snapshot_id) THEN
        RAISE EXCEPTION 'provider feed snapshot seed rows are removed only with their owning snapshot'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'provider_feed_snapshot_seeds_owner_delete_check';
    END IF;
    RETURN OLD;
END;
$$;

CREATE FUNCTION publish_feed_snapshot(p_id text, p_provider text, p_entry text, p_group text, p_expected_version bigint, p_fetched_at timestamp with time zone, p_content_hash text, p_seeds jsonb, p_payloads bytea[]) RETURNS text
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
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s WHERE NOT public.business_exact_keys(s,ARRAY[
        'id','stable_seed_key','locator','locator_display','title','published_at','size_bytes','info_hash','payload_digest',
        'torrent_filename','torrent_state','torrent_error_code','torrent_error_reason','torrent_version','info_hash_v1','info_hash_v2',
        'payload_index','manifest','files']))
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s
            WHERE jsonb_typeof(s->'manifest') = 'object' AND NOT public.business_exact_keys(s->'manifest',ARRAY[
                'payload_digest','torrent_parser_version','torrent_name',
                'total_size_bytes','file_count','projection_digest']))
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_seeds) s, jsonb_array_elements(s->'files') f
            WHERE NOT public.business_exact_keys(f,ARRAY['file_ref','ordinal','source_path','filename','size_bytes','media_kind','language'])) THEN
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
        manifest_projection_digest,files)
    SELECT x.id,p_id,x.stable_seed_key,x.locator,x.locator_display,x.title,x.published_at,x.size_bytes,x.info_hash,
        x.payload_digest,x.torrent_filename,x.torrent_state,x.torrent_error_code,x.torrent_error_reason,x.torrent_version,x.info_hash_v1,x.info_hash_v2,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE p_payloads[x.payload_index] END,
        CASE WHEN x.payload_index IS NULL THEN NULL ELSE octet_length(p_payloads[x.payload_index]) END,
        m.manifest_payload_digest,m.manifest_torrent_parser_version,m.manifest_torrent_name,m.manifest_total_size_bytes,
        m.manifest_file_count,m.manifest_projection_digest,x.files
    FROM jsonb_to_recordset(p_seeds) AS x(id TEXT,stable_seed_key TEXT,locator TEXT,locator_display TEXT,title TEXT,
        published_at TIMESTAMPTZ,size_bytes BIGINT,info_hash TEXT,payload_digest TEXT,torrent_filename TEXT,torrent_state TEXT,
        torrent_error_code TEXT,torrent_error_reason TEXT,torrent_version TEXT,info_hash_v1 TEXT,info_hash_v2 TEXT,payload_index INTEGER,
        manifest JSONB,files JSONB)
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

CREATE FUNCTION publish_group_observation(p_canonical text, p_digest text, p_entries jsonb) RETURNS void
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
    v_state TEXT;
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
    IF o->'completeness'->>'state' = 'known' AND (
        NOT public.business_exact_keys(o->'completeness'->'value',ARRAY['complete','evidence','adapter_version'])
        OR jsonb_typeof(o->'completeness'->'value'->'complete') IS DISTINCT FROM 'boolean'
        OR o->'completeness'->'value'->>'evidence' IS DISTINCT FROM v_evidence
        OR o->'completeness'->'value'->>'adapter_version' IS DISTINCT FROM v_schema
        OR o->>'provider_schema' IS DISTINCT FROM v_schema) THEN
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
            OR (v_group->'available'->>'state'='known' AND jsonb_typeof(v_group->'available'->'value') IS DISTINCT FROM 'boolean') THEN
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
    v_state := CASE WHEN jsonb_array_length(o->'groups')=0 THEN 'empty' ELSE 'fresh' END;
    INSERT INTO public.provider_group_refresh_health(provider,catalog_entry_ref,state,last_attempt_at,last_success_at,version)
    VALUES (o->'scope'->>'provider',o->'scope'->>'catalog_entry_ref',v_state,v_time,v_time,1)
    ON CONFLICT (provider,catalog_entry_ref) DO UPDATE SET state=EXCLUDED.state,
        last_attempt_at=EXCLUDED.last_attempt_at,last_success_at=EXCLUDED.last_success_at,
        error_code=NULL,error_message=NULL,version=provider_group_refresh_health.version+1,updated_at=CURRENT_TIMESTAMP;
END;
$_$;

CREATE FUNCTION recheck_acquisition_qualification(p_qualification jsonb, p_request_hash text, p_config_digest text, p_payload_digest text) RETURNS timestamp with time zone
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
            OR v_feed.hash_schema<>'provider-feed-snapshot-v1' OR v_feed.hash_schema IS DISTINCT FROM feed->>'hash_schema'
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

CREATE FUNCTION sync_rss_source_provenance() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.provenance IS NULL THEN
        IF NEW.provider IS NOT NULL
            OR NEW.provider_entry_ref IS NOT NULL
            OR NEW.provider_group_ref IS NOT NULL
            OR NEW.discovery_snapshot_version IS NOT NULL THEN
            NEW.provenance := jsonb_build_object(
                'provider', NEW.provider,
                'provider_entry_ref', NEW.provider_entry_ref,
                'provider_group_ref', NEW.provider_group_ref,
                'discovery_snapshot_version', NEW.discovery_snapshot_version
            );
        END IF;
    ELSIF NEW.provider IS NULL
        AND NEW.provider_entry_ref IS NULL
        AND NEW.provider_group_ref IS NULL
        AND NEW.discovery_snapshot_version IS NULL THEN
        NEW.provider := NEW.provenance ->> 'provider';
        NEW.provider_entry_ref := NEW.provenance ->> 'provider_entry_ref';
        NEW.provider_group_ref := NEW.provenance ->> 'provider_group_ref';
        NEW.discovery_snapshot_version := NEW.provenance ->> 'discovery_snapshot_version';
    END IF;
    RETURN NEW;
END;
$$;


CREATE TRIGGER managed_anime_lifecycle_guard
    BEFORE DELETE OR UPDATE ON managed_anime
    FOR EACH ROW EXECUTE FUNCTION guard_managed_anime_lifecycle();






CREATE TRIGGER group_observation_immutable
    BEFORE UPDATE ON provider_group_snapshots
    FOR EACH ROW EXECUTE FUNCTION guard_group_observation_immutable();
CREATE CONSTRAINT TRIGGER group_observation_seal
    AFTER INSERT ON provider_group_snapshots
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_group_observation_seal();
CREATE TRIGGER rss_sources_provenance_sync
    BEFORE INSERT OR UPDATE OF source_kind, provenance, provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version ON rss_sources
    FOR EACH ROW EXECUTE FUNCTION sync_rss_source_provenance();


CREATE CONSTRAINT TRIGGER feed_snapshot_seal
    AFTER INSERT ON provider_feed_snapshots
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_feed_snapshot_seal();
CREATE TRIGGER provider_feed_snapshots_append_only_trg
    BEFORE UPDATE ON provider_feed_snapshots
    FOR EACH ROW EXECUTE FUNCTION provider_feed_snapshot_append_only_guard();

CREATE TRIGGER provider_feed_snapshot_seeds_append_only_trg
    BEFORE UPDATE ON provider_feed_snapshot_seeds
    FOR EACH ROW EXECUTE FUNCTION provider_feed_snapshot_append_only_guard();
CREATE TRIGGER provider_feed_snapshot_seeds_owner_delete_trg
    BEFORE DELETE ON provider_feed_snapshot_seeds
    FOR EACH ROW EXECUTE FUNCTION provider_feed_snapshot_child_delete_guard();
CREATE TRIGGER anime_config_candidate_delete_guard_trg
    BEFORE DELETE ON anime_config_candidates
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidate_delete_guard();
CREATE TRIGGER anime_config_candidates_current_revision_guard_trg
    BEFORE INSERT OR UPDATE OF current_revision, current_revision_digest ON anime_config_candidates
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidates_current_revision_guard();
CREATE TRIGGER anime_config_candidates_defaults_trg
    BEFORE INSERT OR UPDATE OF draft, draft_digest, last_activity, status, retention_until, activated_managed_anime_id ON anime_config_candidates
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidates_defaults();
CREATE TRIGGER anime_config_candidates_guard_trg
    BEFORE INSERT OR UPDATE ON anime_config_candidates
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidates_guard();

CREATE TRIGGER anime_config_candidate_revision_body_guard_trg
    BEFORE INSERT ON anime_config_candidate_revisions
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidate_revision_body_guard();
CREATE TRIGGER anime_config_candidate_revisions_guard_trg
    BEFORE DELETE OR UPDATE ON anime_config_candidate_revisions
    FOR EACH ROW EXECUTE FUNCTION anime_config_candidate_revisions_guard();

CREATE TRIGGER anime_business_config_guard_trg
    BEFORE INSERT OR UPDATE ON anime_business_configs
    FOR EACH ROW EXECUTE FUNCTION anime_business_config_guard();






GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE managed_anime,
    anime_metadata, anime_business_configs, anime_config_overrides, subscriptions,
    provider_group_scope_generations, provider_group_selections, provider_group_refresh_health,
    rss_sources, rss_source_runtime, anime_config_candidates, anime_config_candidate_revisions,
    subscription_log_entries TO bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE rss_provider_circuits TO bansoko_runtime;
GRANT SELECT, DELETE ON TABLE provider_group_snapshots, provider_feed_snapshots TO bansoko_runtime;
GRANT SELECT ON TABLE provider_feed_snapshot_seeds TO bansoko_runtime;
GRANT UPDATE (retention_until) ON TABLE provider_group_snapshots TO bansoko_runtime;
GRANT UPDATE (fetched_at) ON TABLE provider_feed_snapshots TO bansoko_runtime;
GRANT UPDATE (fresh_until) ON TABLE provider_feed_snapshots TO bansoko_runtime;
GRANT UPDATE (retention_until) ON TABLE provider_feed_snapshots TO bansoko_runtime;
REVOKE ALL ON FUNCTION assert_queued_preparation_witness, cleanup_managed_anime_local_data,
    publish_feed_snapshot, publish_group_observation, recheck_acquisition_qualification FROM PUBLIC;
GRANT EXECUTE ON FUNCTION cleanup_managed_anime_local_data, publish_feed_snapshot,
    publish_group_observation, recheck_acquisition_qualification TO bansoko_runtime;

-- ============================================================================
-- Releases, torrent inputs, release facts and media facts
-- Releases observed from feeds, their torrent inputs and parsed facts, and the
-- target rows the acquisition planner reads.
-- ============================================================================

CREATE TABLE releases (
    id TEXT NOT NULL,
    rss_source_id TEXT NOT NULL,
    stable_item_key TEXT NOT NULL,
    raw_facts JSONB NOT NULL,
    source_facts JSONB NOT NULL,
    parsed_facts JSONB,
    internal_facts JSONB,
    verdict TEXT NOT NULL,
    reason_code TEXT NOT NULL,
    reason_detail TEXT,
    evaluation_version TEXT,
    download_task_id TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    evaluated_rules_version BIGINT,
    discovery_notice_due BOOLEAN NOT NULL DEFAULT false,
    CONSTRAINT releases_pkey PRIMARY KEY (id),
    CONSTRAINT releases_download_task_id_key UNIQUE (download_task_id),
    CONSTRAINT releases_id_download_task_id_key UNIQUE (id, download_task_id),
    CONSTRAINT releases_rss_source_id_stable_item_key_key UNIQUE (rss_source_id, stable_item_key),
    CONSTRAINT releases_rss_source_id_fkey FOREIGN KEY (rss_source_id) REFERENCES rss_sources (id),
    CONSTRAINT releases_evaluated_rules_version_check CHECK (evaluated_rules_version IS NULL OR evaluated_rules_version >= 1),
    CONSTRAINT releases_evaluation_version_check CHECK (evaluation_version IS NULL OR char_length(btrim(evaluation_version)) BETWEEN 1 AND 128),
    CONSTRAINT releases_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT releases_internal_facts_check CHECK (internal_facts IS NULL OR (jsonb_typeof(internal_facts) = 'object' AND pg_column_size(internal_facts) <= 2097152)),
    CONSTRAINT releases_parsed_facts_check CHECK (parsed_facts IS NULL OR (jsonb_typeof(parsed_facts) = 'object' AND pg_column_size(parsed_facts) <= 1048576)),
    CONSTRAINT releases_raw_facts_check CHECK (jsonb_typeof(raw_facts) = 'object' AND pg_column_size(raw_facts) <= 2097152),
    CONSTRAINT releases_reason_code_check CHECK (char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT releases_reason_detail_check CHECK (reason_detail IS NULL OR char_length(reason_detail) <= 2048),
    CONSTRAINT releases_source_facts_check CHECK (jsonb_typeof(source_facts) = 'object' AND pg_column_size(source_facts) <= 1048576),
    CONSTRAINT releases_stable_item_key_check CHECK (char_length(btrim(stable_item_key)) BETWEEN 1 AND 512),
    CONSTRAINT releases_verdict_check CHECK (verdict IN ('discovered', 'waiting_metadata', 'subscription_stopped', 'source_unavailable', 'parse_failed', 'accepted_pending_download', 'filtered', 'adopted_existing', 'acquisition_failed', 'submitted') AND ((download_task_id IS NULL AND verdict <> 'submitted') OR (download_task_id IS NOT NULL AND verdict IN ('accepted_pending_download', 'submitted', 'filtered', 'waiting_metadata', 'parse_failed')))),
    CONSTRAINT releases_version_check CHECK (version >= 1)
);

-- A source's newest releases: the owner RSS list reads only the first page of each
-- source, so its cost does not grow with the owner's whole history.
CREATE INDEX releases_source_discovered_idx ON releases (rss_source_id, discovered_at DESC, id DESC);
-- Manually injected releases: the RSS refresh dispatcher skips a source whose
-- releases are all injected, and asks this index first (PRUNE-046 S2).
CREATE INDEX releases_injected_source_idx ON releases (rss_source_id)
    WHERE reason_code = 'injected';

ALTER TABLE subscription_log_entries
    ADD CONSTRAINT subscription_log_entries_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id);

COMMENT ON COLUMN releases.discovery_notice_due IS 'The release was first seen after the source baseline scan and may admit one release_discovered notice.';
COMMENT ON CONSTRAINT releases_verdict_check ON releases IS 'A download-attached Release may be parse_failed - the explicitly selected seed downloads even when the scalar episode identity is unresolved; the naming fault is surfaced by the post-processing plan stage.';

CREATE TABLE release_torrent_inputs (
    id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    payload BYTEA NOT NULL,
    payload_digest TEXT NOT NULL,
    source_url_digest TEXT NOT NULL,
    filename TEXT NOT NULL,
    parser_version TEXT NOT NULL,
    torrent_version TEXT NOT NULL,
    info_hash_v1 TEXT,
    info_hash_v2 TEXT,
    payload_size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT release_torrent_inputs_pkey PRIMARY KEY (id),
    CONSTRAINT release_torrent_inputs_release_id_key UNIQUE (release_id),
    CONSTRAINT release_torrent_inputs_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id),
    CONSTRAINT release_torrent_inputs_check CHECK (payload_size_bytes = octet_length(payload)),
    CONSTRAINT release_torrent_inputs_filename_check CHECK (char_length(btrim(filename)) BETWEEN 1 AND 512),
    CONSTRAINT release_torrent_inputs_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT release_torrent_inputs_info_hash_v1_check CHECK (info_hash_v1 IS NULL OR char_length(btrim(info_hash_v1)) BETWEEN 1 AND 128),
    CONSTRAINT release_torrent_inputs_info_hash_v2_check CHECK (info_hash_v2 IS NULL OR char_length(btrim(info_hash_v2)) BETWEEN 1 AND 128),
    CONSTRAINT release_torrent_inputs_parser_version_check CHECK (char_length(btrim(parser_version)) BETWEEN 1 AND 128),
    CONSTRAINT release_torrent_inputs_payload_check CHECK (octet_length(payload) BETWEEN 1 AND 20971520),
    CONSTRAINT release_torrent_inputs_payload_digest_check CHECK (char_length(btrim(payload_digest)) BETWEEN 8 AND 128),
    CONSTRAINT release_torrent_inputs_source_url_digest_check CHECK (char_length(btrim(source_url_digest)) BETWEEN 8 AND 128),
    CONSTRAINT release_torrent_inputs_torrent_version_check CHECK (torrent_version IN ('v1', 'v2', 'hybrid'))
);

CREATE TABLE release_candidate_facts (
    release_id TEXT NOT NULL,
    release_version BIGINT NOT NULL,
    parser_version TEXT NOT NULL,
    input_fingerprint TEXT NOT NULL,
    raw_evidence JSONB NOT NULL,
    facts JSONB NOT NULL,
    coverage_keys JSONB NOT NULL,
    resolution TEXT,
    revision TEXT,
    source TEXT,
    language TEXT,
    "group" TEXT,
    size_bytes BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    compare_values JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT release_candidate_facts_pkey PRIMARY KEY (release_id, release_version),
    CONSTRAINT release_candidate_facts_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id),
    CONSTRAINT release_candidate_facts_compare_values_check CHECK (jsonb_typeof(compare_values) = 'object' AND pg_column_size(compare_values) <= 65536),
    CONSTRAINT release_candidate_facts_coverage_keys_check CHECK (jsonb_typeof(coverage_keys) = 'array' AND pg_column_size(coverage_keys) <= 65536),
    CONSTRAINT release_candidate_facts_facts_check CHECK (jsonb_typeof(facts) = 'object' AND pg_column_size(facts) <= 262144),
    CONSTRAINT release_candidate_facts_input_fingerprint_check CHECK (char_length(btrim(input_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT release_candidate_facts_parser_version_check CHECK (char_length(btrim(parser_version)) BETWEEN 1 AND 128),
    CONSTRAINT release_candidate_facts_raw_evidence_check CHECK (jsonb_typeof(raw_evidence) = 'object' AND pg_column_size(raw_evidence) <= 262144),
    CONSTRAINT release_candidate_facts_release_version_check CHECK (release_version >= 1),
    CONSTRAINT release_candidate_facts_size_bytes_check CHECK (size_bytes IS NULL OR size_bytes >= 0)
);

CREATE INDEX release_candidate_facts_release_idx ON release_candidate_facts (release_id, release_version DESC);

COMMENT ON COLUMN release_candidate_facts.compare_values IS 'compare-values-v1: the replacement comparison values (item key -> values) and the RSS source subgroup this release contributes, frozen at acceptance. {} = no values.';

CREATE TABLE media_fact_snapshots (
    id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    resolver_version TEXT NOT NULL,
    parser_kind TEXT NOT NULL,
    parser_config_fingerprint TEXT,
    input_digest TEXT NOT NULL,
    release_identity JSONB NOT NULL,
    release_facts JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    parser_config JSONB,
    CONSTRAINT media_fact_snapshots_pkey PRIMARY KEY (id),
    CONSTRAINT media_fact_snapshots_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id),
    CONSTRAINT media_fact_snapshots_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT media_fact_snapshots_input_digest_check CHECK (char_length(btrim(input_digest)) BETWEEN 1 AND 128),
    CONSTRAINT media_fact_snapshots_parser_config_check CHECK (parser_config IS NULL OR (jsonb_typeof(parser_config) = 'object' AND pg_column_size(parser_config) <= 16384)),
    CONSTRAINT media_fact_snapshots_parser_config_fingerprint_check CHECK (parser_config_fingerprint IS NULL OR char_length(btrim(parser_config_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT media_fact_snapshots_parser_config_fingerprint_format CHECK (parser_config_fingerprint IS NULL OR parser_config_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT media_fact_snapshots_parser_kind_check CHECK (char_length(btrim(parser_kind)) BETWEEN 1 AND 64),
    CONSTRAINT media_fact_snapshots_release_facts_check CHECK (pg_column_size(release_facts) <= 65536),
    CONSTRAINT media_fact_snapshots_release_identity_check CHECK (pg_column_size(release_identity) <= 65536),
    CONSTRAINT media_fact_snapshots_resolver_version_check CHECK (char_length(btrim(resolver_version)) BETWEEN 1 AND 64),
    CONSTRAINT media_fact_snapshots_schema_version_check CHECK (char_length(btrim(schema_version)) BETWEEN 1 AND 64)
);

CREATE INDEX media_fact_snapshots_release_idx ON media_fact_snapshots (release_id, created_at DESC, id);

COMMENT ON COLUMN media_fact_snapshots.parser_config IS 'Parser configuration (for example {"episode_patterns": [...]}) the frozen Release evidence was produced with; NULL for legacy rows and for selections whose profile carried no config. Maximum 16384 bytes.';
COMMENT ON CONSTRAINT media_fact_snapshots_parser_config_fingerprint_format ON media_fact_snapshots IS 'Frozen parser config fingerprint must be the canonical sha256:<64 lowercase hex> digest (matching the nil-config digest for NULL-config rows), or NULL for legacy rows and empty profiles.';

CREATE TABLE media_fact_files (
    snapshot_id TEXT NOT NULL,
    file_ref TEXT NOT NULL,
    media_kind TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    source_fingerprint TEXT,
    file_evidence JSONB NOT NULL,
    effective_facts JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT media_fact_files_pkey PRIMARY KEY (snapshot_id, file_ref),
    CONSTRAINT media_fact_files_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES media_fact_snapshots (id),
    CONSTRAINT media_fact_files_effective_facts_check CHECK (pg_column_size(effective_facts) <= 65536),
    CONSTRAINT media_fact_files_file_evidence_check CHECK (pg_column_size(file_evidence) <= 65536),
    CONSTRAINT media_fact_files_file_ref_check CHECK (char_length(btrim(file_ref)) BETWEEN 1 AND 512),
    CONSTRAINT media_fact_files_media_kind_check CHECK (media_kind IN ('video', 'subtitle', 'other')),
    CONSTRAINT media_fact_files_size_bytes_check CHECK (size_bytes >= 0),
    CONSTRAINT media_fact_files_source_fingerprint_check CHECK (source_fingerprint IS NULL OR char_length(btrim(source_fingerprint)) BETWEEN 1 AND 128)
);

CREATE INDEX media_fact_files_fingerprint_idx ON media_fact_files (source_fingerprint, snapshot_id, file_ref)
    WHERE source_fingerprint IS NOT NULL;

CREATE TABLE acquisition_targets (
    id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    target_ref TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_kind TEXT NOT NULL,
    target_path TEXT NOT NULL,
    expected_size_bytes BIGINT NOT NULL,
    intent TEXT NOT NULL,
    collision_policy TEXT NOT NULL,
    render_snapshot JSONB NOT NULL,
    config_snapshot JSONB NOT NULL,
    spec_digest TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    observation_state TEXT,
    observed_size_bytes BIGINT,
    observation_reason_code TEXT,
    observed_at TIMESTAMPTZ,
    observation_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT acquisition_targets_pkey PRIMARY KEY (id),
    CONSTRAINT acquisition_targets_release_id_target_path_key UNIQUE (release_id, target_path),
    CONSTRAINT acquisition_targets_release_id_target_ref_key UNIQUE (release_id, target_ref),
    CONSTRAINT acquisition_targets_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id) ON DELETE CASCADE,
    CONSTRAINT acquisition_targets_collision_policy_check CHECK (collision_policy IN ('block', 'version', 'overwrite')),
    CONSTRAINT acquisition_targets_config_snapshot_check CHECK (jsonb_typeof(config_snapshot) = 'object' AND pg_column_size(config_snapshot) <= 1048576),
    CONSTRAINT acquisition_targets_expected_size_bytes_check CHECK (expected_size_bytes >= 0),
    CONSTRAINT acquisition_targets_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT acquisition_targets_intent_check CHECK (intent IN ('ensure_present', 'replace')),
    CONSTRAINT acquisition_targets_media_kind_check CHECK (media_kind IN ('video', 'subtitle', 'other')),
    CONSTRAINT acquisition_targets_render_snapshot_check CHECK (jsonb_typeof(render_snapshot) = 'object' AND pg_column_size(render_snapshot) <= 1048576),
    CONSTRAINT acquisition_targets_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096 AND source_path !~ '^/' AND source_path !~ '(^|/)\.\.(/|$)' AND source_path !~ '[[:cntrl:]]'),
    CONSTRAINT acquisition_targets_spec_digest_check CHECK (char_length(btrim(spec_digest)) BETWEEN 8 AND 128),
    CONSTRAINT acquisition_targets_target_path_check CHECK (char_length(btrim(target_path)) BETWEEN 1 AND 4096 AND target_path !~ '[[:cntrl:]]'),
    CONSTRAINT acquisition_targets_target_ref_check CHECK (char_length(btrim(target_ref)) BETWEEN 1 AND 4096),
    CONSTRAINT acquisition_targets_version_check CHECK (version >= 1),
    CONSTRAINT acquisition_targets_observation_check CHECK (
        (observation_state IS NULL) = (observed_at IS NULL)
        AND (
            (observation_state IS NULL AND observed_size_bytes IS NULL AND observation_reason_code IS NULL)
            OR (observation_state IN ('missing', 'present_matched', 'present_mismatched', 'unreadable')
                AND (observed_size_bytes IS NULL OR observed_size_bytes >= 0)
                AND (observation_reason_code IS NULL OR char_length(btrim(observation_reason_code)) BETWEEN 1 AND 128))
        ))
);

CREATE INDEX acquisition_targets_release_idx ON acquisition_targets (release_id, target_ref, id);

COMMENT ON TABLE acquisition_targets IS 'Frozen per-entry TargetSet rows for Release acquisition; not a peer workflow owner.';

CREATE FUNCTION assert_deleted_torrent_input_parent_removed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM releases WHERE id=OLD.release_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514',
            MESSAGE='RELEASE_TORRENT_INPUT_DELETE_REQUIRES_RELEASE_DELETE';
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_release_torrent_input_identity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'INSERT' OR
       OLD.payload IS DISTINCT FROM NEW.payload OR
       OLD.payload_digest IS DISTINCT FROM NEW.payload_digest OR
       OLD.parser_version IS DISTINCT FROM NEW.parser_version OR
       OLD.torrent_version IS DISTINCT FROM NEW.torrent_version OR
       OLD.info_hash_v1 IS DISTINCT FROM NEW.info_hash_v1 OR
       OLD.info_hash_v2 IS DISTINCT FROM NEW.info_hash_v2 OR
       OLD.filename IS DISTINCT FROM NEW.filename THEN
        UPDATE releases SET version=version+1, updated_at=CURRENT_TIMESTAMP
        WHERE id=NEW.release_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION target_delete_stale(p_release text, p_refs text[]) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE n bigint;
BEGIN
 DELETE FROM public.acquisition_targets WHERE release_id=p_release AND target_ref<>ALL(p_refs);
 GET DIAGNOSTICS n=ROW_COUNT; RETURN n;
END $$;

CREATE FUNCTION target_lock_artifact_snapshot(p_artifact text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 PERFORM t.id FROM public.acquisition_targets t JOIN public.download_tasks d ON d.release_id=t.release_id JOIN public.artifacts a ON a.download_task_id=d.id WHERE a.id=p_artifact ORDER BY t.id COLLATE "C" FOR SHARE OF t NOWAIT;
END $$;

CREATE FUNCTION target_lock_observation(p_id text) RETURNS text
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT release_id FROM public.acquisition_targets WHERE id=p_id FOR UPDATE NOWAIT;
$$;

CREATE FUNCTION target_observation_upsert(p_id text, p_state text, p_size bigint, p_reason text, p_at timestamp with time zone) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 -- The first observation of a target is version 1, every later one advances it,
 -- the same numbering the one-row-per-target projection used to carry.
 UPDATE public.acquisition_targets
 SET observation_state=p_state,observed_size_bytes=p_size,observation_reason_code=p_reason,observed_at=p_at,
     observation_version=CASE WHEN acquisition_targets.observation_state IS NULL THEN 1
                              ELSE acquisition_targets.observation_version+1 END
 WHERE id=p_id;
$$;

CREATE FUNCTION target_upsert(p_id text, p_release text, p_ref text, p_source text, p_media text, p_path text, p_size bigint, p_intent text, p_collision text, p_render jsonb, p_config jsonb, p_digest text) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 INSERT INTO public.acquisition_targets(id,release_id,target_ref,source_path,media_kind,target_path,expected_size_bytes,intent,collision_policy,render_snapshot,config_snapshot,spec_digest,version)
 VALUES(p_id,p_release,p_ref,p_source,p_media,p_path,p_size,p_intent,p_collision,p_render,p_config,p_digest,1)
 ON CONFLICT(release_id,target_ref) DO UPDATE SET source_path=EXCLUDED.source_path,media_kind=EXCLUDED.media_kind,target_path=EXCLUDED.target_path,expected_size_bytes=EXCLUDED.expected_size_bytes,intent=EXCLUDED.intent,collision_policy=EXCLUDED.collision_policy,render_snapshot=EXCLUDED.render_snapshot,config_snapshot=EXCLUDED.config_snapshot,spec_digest=EXCLUDED.spec_digest,version=acquisition_targets.version+1,updated_at=CURRENT_TIMESTAMP;
$$;


CREATE CONSTRAINT TRIGGER release_torrent_input_delete_parent
    AFTER DELETE ON release_torrent_inputs
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_deleted_torrent_input_parent_removed();
CREATE TRIGGER release_torrent_input_identity_version
    AFTER INSERT OR UPDATE ON release_torrent_inputs
    FOR EACH ROW EXECUTE FUNCTION bump_release_torrent_input_identity();





GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE releases, release_torrent_inputs,
    release_candidate_facts, media_fact_snapshots, media_fact_files TO bansoko_runtime;
GRANT SELECT ON TABLE acquisition_targets TO bansoko_runtime;
REVOKE ALL ON FUNCTION target_delete_stale, target_lock_artifact_snapshot,
    target_lock_observation, target_observation_upsert, target_upsert FROM PUBLIC;
GRANT EXECUTE ON FUNCTION target_delete_stale, target_lock_artifact_snapshot,
    target_lock_observation, target_observation_upsert, target_upsert TO bansoko_runtime;

-- ============================================================================
-- Acquisitions: accepted input, payload, manifest, requirements, targets, results and handoffs
-- An acquisition is one accepted request to bring a release into the library.
-- Acceptance seals its input, payload, manifest, requirements and declared
-- targets in one transaction; delivery results and management handoffs record
-- how each file ended. Sealed rows are immutable (guard triggers).
-- ============================================================================

CREATE FUNCTION render_episode_titles_valid(p_render jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE WHEN p_render->>'renderer_version'='fileoperation-render-v1' THEN COALESCE(
  jsonb_typeof(p_render->'episode_titles')='object'
  AND (SELECT array_agg(k COLLATE "C" ORDER BY k COLLATE "C") FROM jsonb_object_keys(p_render->'episode_titles') k)=ARRAY['bgm','episode','tmdb']::text[]
  AND (p_render->'episode_titles'->'episode'='null'::jsonb OR (jsonb_typeof(p_render->'episode_titles'->'episode')='string'
   AND octet_length(p_render->'episode_titles'->>'episode') BETWEEN 1 AND 64 AND p_render->'episode_titles'->>'episode' !~ '[[:cntrl:]]'))
  AND public.render_episode_title_valid(p_render->'episode_titles'->'bgm')
  AND public.render_episode_title_valid(p_render->'episode_titles'->'tmdb'),false)
 ELSE NOT (p_render ? 'episode_titles') END
$$;

CREATE TABLE acquisitions (
    id TEXT NOT NULL,
    execution_branch TEXT NOT NULL,
    entry_kind TEXT NOT NULL,
    access_scope TEXT NOT NULL,
    request_namespace TEXT NOT NULL,
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    canonical_request JSONB NOT NULL,
    admission_kind TEXT NOT NULL,
    admission_http_status INTEGER NOT NULL,
    admission_receipt JSONB NOT NULL,
    input_schema_version INTEGER NOT NULL,
    admission_digest TEXT NOT NULL,
    origin_managed_anime_ref TEXT,
    source_release_ref TEXT,
    display_title JSONB NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL,
    retention_anchor_at TIMESTAMPTZ NOT NULL,
    detail_retention_seconds INTEGER NOT NULL,
    receipt_after_accept_seconds INTEGER NOT NULL,
    receipt_after_terminal_seconds INTEGER NOT NULL,
    receipt_until TIMESTAMPTZ NOT NULL,
    detail_until TIMESTAMPTZ,
    state TEXT NOT NULL,
    terminal_at TIMESTAMPTZ,
    terminal_schema TEXT,
    terminal_receipt JSONB,
    record_state TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    preparation_mode TEXT NOT NULL,
    preparation_digest TEXT,
    facts_revision BIGINT NOT NULL DEFAULT 0,
    checked_revision BIGINT NOT NULL DEFAULT 0,
    settlement_error_count INTEGER NOT NULL DEFAULT 0,
    settlement_blocked_code TEXT,
    vars_evidence_version INTEGER NOT NULL DEFAULT 1,
    vars_evidence_digest TEXT,
    compacted_at TIMESTAMPTZ,
    compact_trigger TEXT,
    completion_open BOOLEAN NOT NULL DEFAULT false,
    compact_pinned BOOLEAN NOT NULL DEFAULT false,
    activity_logged_at TIMESTAMPTZ,
    -- PRUNE-046 S4: the 1:1 evidence children are this row. The canonical text of
    -- each frozen document is kept because its digest is recomputed from those bytes;
    -- the JSONB projection is generated so the two can never disagree.
    input_canonical_utf8 TEXT NOT NULL,
    input JSONB GENERATED ALWAYS AS (input_canonical_utf8::jsonb) STORED,
    payload_digest TEXT NOT NULL,
    preparation_witness_canonical_utf8 TEXT,
    preparation_witness JSONB GENERATED ALWAYS AS (preparation_witness_canonical_utf8::jsonb) STORED,
    manifest_digest TEXT NOT NULL,
    manifest_item_count INTEGER NOT NULL,
    manifest_sealed_at TIMESTAMPTZ NOT NULL,
    requirements_manifest_digest TEXT,
    requirements_selection_digest TEXT,
    requirements_evidence_digest TEXT,
    requirements_digest TEXT,
    requirements_item_count INTEGER,
    requirements_required_count INTEGER,
    requirements_sealed_at TIMESTAMPTZ,
    target_requirements_digest TEXT,
    target_digest TEXT,
    target_sealed_at TIMESTAMPTZ,
    vars_evaluation_canonical_utf8 TEXT,
    vars_evaluation JSONB GENERATED ALWAYS AS (vars_evaluation_canonical_utf8::jsonb) STORED,
    CONSTRAINT acquisitions_pkey PRIMARY KEY (id),
    CONSTRAINT acquisitions_access_scope_request_namespace_request_key_key UNIQUE (access_scope, request_namespace, request_key),
    CONSTRAINT acquisitions_access_scope_check CHECK (char_length(access_scope) BETWEEN 1 AND 128),
    CONSTRAINT acquisitions_activity_logged_check CHECK (activity_logged_at IS NULL OR terminal_at IS NOT NULL),
    CONSTRAINT acquisitions_completion_open_check CHECK (NOT completion_open OR (state = 'succeeded' AND execution_branch = 'download' AND record_state = 'full')),
    CONSTRAINT acquisitions_admission_digest_check CHECK (admission_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisitions_admission_receipt_check CHECK (jsonb_typeof(admission_receipt) = 'object' AND octet_length(admission_receipt::text) <= 65536),
    CONSTRAINT acquisitions_canonical_request_check CHECK (jsonb_typeof(canonical_request) = 'object' AND octet_length(canonical_request::text) <= 65536),
    CONSTRAINT acquisitions_check CHECK ((entry_kind = 'automatic' AND request_namespace = 'auto_acquire_v1') OR (entry_kind IN ('subscription_add', 'one_shot') AND request_namespace = 'manual_acquire_v1')),
    CONSTRAINT acquisitions_check1 CHECK (execution_branch = 'download' AND admission_kind = 'download_queued' AND admission_http_status = 202),
    CONSTRAINT acquisitions_check10 CHECK (receipt_until >= (retention_anchor_at + (receipt_after_accept_seconds::double precision * '00:00:01'::interval))),
    CONSTRAINT acquisitions_check11 CHECK (terminal_at IS NULL OR receipt_until >= (terminal_at + (receipt_after_terminal_seconds::double precision * '00:00:01'::interval))),
    CONSTRAINT acquisitions_check12 CHECK (detail_until IS NULL OR detail_until = (terminal_at + (detail_retention_seconds::double precision * '00:00:01'::interval))),
    CONSTRAINT acquisitions_check2 CHECK (retention_anchor_at = accepted_at),
    CONSTRAINT acquisitions_check3 CHECK (terminal_at IS NULL OR terminal_at >= accepted_at),
    CONSTRAINT acquisitions_check4 CHECK (checked_revision >= 0 AND checked_revision <= facts_revision),
    CONSTRAINT acquisitions_check7 CHECK ((preparation_mode = 'none' AND preparation_digest IS NULL) OR (preparation_mode = 'provider_selection_v1' AND preparation_digest IS NOT NULL AND preparation_digest ~ '^sha256:[0-9a-f]{64}$')),
    CONSTRAINT acquisitions_compact_record_check CHECK ((record_state = 'compact') = (compacted_at IS NOT NULL AND compact_trigger IS NOT NULL) AND (compacted_at IS NULL) = (compact_trigger IS NULL)),
    CONSTRAINT acquisitions_compact_terminal_check CHECK (record_state <> 'compact' OR state IN ('succeeded', 'failed', 'cancelled')),
    CONSTRAINT acquisitions_compact_trigger_check CHECK (compact_trigger IS NULL OR compact_trigger IN ('detail_expired', 'owner_cleanup')),
    CONSTRAINT acquisitions_detail_retention_seconds_check CHECK (detail_retention_seconds > 0),
    CONSTRAINT acquisitions_display_title_check CHECK (jsonb_typeof(display_title) = 'object' AND octet_length(display_title::text) <= 4096),
    CONSTRAINT acquisitions_entry_kind_check CHECK (entry_kind IN ('automatic', 'subscription_add', 'one_shot')),
    CONSTRAINT acquisitions_execution_branch_check CHECK (execution_branch = 'download'),
    CONSTRAINT acquisitions_facts_revision_check CHECK (facts_revision >= 0),
    CONSTRAINT acquisitions_id_check CHECK (char_length(id) BETWEEN 1 AND 128 AND id = btrim(id)),
    CONSTRAINT acquisitions_input_schema_version_check CHECK (input_schema_version = 1),
    CONSTRAINT acquisitions_input_canonical_bytes_check CHECK (octet_length(input_canonical_utf8) BETWEEN 1 AND 1048576),
    CONSTRAINT acquisitions_input_check CHECK (admission_digest = ('sha256:' || encode(sha256(((convert_to('acquisition-input:v1', 'UTF8'::name) || decode('00', 'hex')) || convert_to(input_canonical_utf8, 'UTF8'::name))), 'hex'))),
    CONSTRAINT acquisitions_input_snapshot_check CHECK (jsonb_typeof(input) = 'object' AND (input -> 'schema_version') = to_jsonb(input_schema_version)),
    CONSTRAINT acquisitions_payload_digest_check CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisitions_preparation_witness_check CHECK ((preparation_mode = 'none' AND preparation_witness_canonical_utf8 IS NULL) OR (preparation_mode = 'provider_selection_v1' AND octet_length(preparation_witness_canonical_utf8) BETWEEN 1 AND 1048576 AND jsonb_typeof(preparation_witness) = 'object' AND preparation_digest = ('sha256:' || encode(sha256(((convert_to('acquisition-preparation-witness:v1', 'UTF8'::name) || decode('00', 'hex')) || convert_to(preparation_witness_canonical_utf8, 'UTF8'::name))), 'hex')))),
    CONSTRAINT acquisitions_manifest_check CHECK ((manifest_digest ~ '^sha256:[0-9a-f]{64}$') AND manifest_item_count BETWEEN 0 AND 10000),
    CONSTRAINT acquisitions_requirements_check CHECK ((requirements_sealed_at IS NULL) = (requirements_digest IS NULL)
        AND (requirements_digest IS NULL OR (requirements_digest ~ '^sha256:[0-9a-f]{64}$' AND requirements_manifest_digest ~ '^sha256:[0-9a-f]{64}$' AND requirements_selection_digest ~ '^sha256:[0-9a-f]{64}$' AND requirements_evidence_digest ~ '^sha256:[0-9a-f]{64}$' AND requirements_required_count BETWEEN 0 AND requirements_item_count AND requirements_item_count BETWEEN 0 AND 10000 AND requirements_manifest_digest = manifest_digest))),
    CONSTRAINT acquisitions_target_check CHECK ((target_sealed_at IS NULL) = (target_digest IS NULL)
        AND (target_digest IS NULL OR (target_digest ~ '^sha256:[0-9a-f]{64}$' AND target_requirements_digest = requirements_digest))),
    CONSTRAINT acquisitions_vars_evaluation_check CHECK (vars_evaluation_canonical_utf8 IS NULL OR (octet_length(vars_evaluation_canonical_utf8) BETWEEN 1 AND 65536 AND jsonb_typeof(vars_evaluation) = 'object' AND vars_evidence_digest = ('sha256:' || encode(sha256(((convert_to('acquisition-vars-evaluation:v1', 'UTF8'::name) || decode('00', 'hex')) || convert_to(vars_evaluation_canonical_utf8, 'UTF8'::name))), 'hex')))),
    CONSTRAINT acquisitions_origin_managed_anime_ref_check CHECK (origin_managed_anime_ref IS NULL OR char_length(origin_managed_anime_ref) BETWEEN 1 AND 128),
    CONSTRAINT acquisitions_preparation_mode_check CHECK (preparation_mode IN ('none', 'provider_selection_v1')),
    CONSTRAINT acquisitions_receipt_after_accept_seconds_check CHECK (receipt_after_accept_seconds > 0),
    CONSTRAINT acquisitions_receipt_after_terminal_seconds_check CHECK (receipt_after_terminal_seconds > 0),
    CONSTRAINT acquisitions_record_state_check CHECK (record_state IN ('full', 'compact')),
    CONSTRAINT acquisitions_request_hash_check CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisitions_request_key_check CHECK (char_length(request_key) BETWEEN 1 AND 256),
    CONSTRAINT acquisitions_request_namespace_check CHECK (request_namespace IN ('auto_acquire_v1', 'manual_acquire_v1')),
    CONSTRAINT acquisitions_settlement_error_count_check CHECK (settlement_error_count BETWEEN 0 AND 5),
    CONSTRAINT acquisitions_source_release_ref_check CHECK (source_release_ref IS NULL OR char_length(source_release_ref) BETWEEN 1 AND 128),
    CONSTRAINT acquisitions_state_check CHECK (state IN ('running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT acquisitions_terminal_receipt_check CHECK (terminal_receipt IS NULL OR (jsonb_typeof(terminal_receipt) = 'object' AND octet_length(terminal_receipt::text) <= 65536)),
    CONSTRAINT acquisitions_terminal_shape_check CHECK ((state = 'running' AND terminal_at IS NULL AND terminal_schema IS NULL AND terminal_receipt IS NULL AND detail_until IS NULL) OR (state IN ('succeeded', 'failed', 'cancelled') AND terminal_at IS NOT NULL AND terminal_schema IS NOT NULL AND terminal_receipt IS NOT NULL AND detail_until IS NOT NULL)),
    CONSTRAINT acquisitions_vars_evidence_digest_check CHECK (vars_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT acquisitions_vars_evidence_version_check CHECK (vars_evidence_version = ANY (ARRAY[0, 1])),
    CONSTRAINT acquisitions_version_check CHECK (version >= 1)
);

CREATE INDEX acquisitions_anime_list_idx ON acquisitions (origin_managed_anime_ref, retention_anchor_at DESC, id DESC)
    WHERE origin_managed_anime_ref IS NOT NULL;
CREATE INDEX acquisitions_list_idx ON acquisitions (retention_anchor_at DESC, id DESC);
CREATE INDEX acquisitions_receipt_until_idx ON acquisitions (receipt_until, id);
CREATE INDEX acquisitions_source_release_idx ON acquisitions (source_release_ref, retention_anchor_at DESC, id DESC)
    WHERE source_release_ref IS NOT NULL;
-- Open rows of the 5 s background passes (PRUNE-046 S2). Each pass scans only
-- the rows it still has to act on; the row leaves the index in the transaction
-- that handles it.
--   compact_due: detail-expiry compaction. compact_pinned is maintained by
--     acquisition_refresh_compact_pin: a root whose task a Release still names,
--     or with a placement obligation, can never compact on detail expiry
--     (acquisition_compact answers release_bound / dependents_present).
--   completion_open: download completion candidates (acquisition_write_terminal
--     opens, the download_completions insert closes).
--   running: retirement disposition.
--   activity_unlogged: settled entries the activity consumer has not logged.
COMMENT ON COLUMN acquisitions.completion_open IS 'A succeeded download root that has no download_completions row yet. Set by acquisition_write_terminal, cleared by the download_completions insert (and by compaction).';
COMMENT ON COLUMN acquisitions.compact_pinned IS 'The root cannot compact on detail expiry: a Release still names one of its tasks, or one of its file operations has a placement obligation. Maintained by acquisition_refresh_compact_pin.';
COMMENT ON COLUMN acquisitions.activity_logged_at IS 'When the activity consumer logged the settled entry of this root (NULL: not yet).';
CREATE INDEX acquisitions_compact_due_idx ON acquisitions (detail_until, id)
    WHERE detail_until IS NOT NULL AND record_state = 'full' AND NOT compact_pinned;
CREATE INDEX acquisitions_completion_open_idx ON acquisitions (terminal_at, id)
    WHERE completion_open;
CREATE INDEX acquisitions_running_idx ON acquisitions (id)
    WHERE state = 'running' AND record_state = 'full';
CREATE INDEX acquisitions_activity_unlogged_idx ON acquisitions (terminal_at, id)
    WHERE terminal_at IS NOT NULL AND activity_logged_at IS NULL AND entry_kind <> 'one_shot';

-- PRUNE-046 S5: one row per acquisition file. The row is the single home for
-- the manifest member, the requirement decision, the frozen declared target, the
-- plan item and the committed delivery result; acquisition_manifest_files,
-- acquisition_requirement_files, acquisition_declared_targets,
-- acquisition_delivery_results and postprocessing_plan_items were per-file
-- projections of it, and the subtitle->video edge is carried by
-- file_operations.bound_video_operation_id instead of a dependency table.
CREATE TABLE acquisition_files (
    acquisition_id TEXT NOT NULL,
    file_ref TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    relative_path TEXT NOT NULL,
    exact_size BIGINT NOT NULL,
    media_kind TEXT NOT NULL,
    identity_evidence JSONB NOT NULL,
    requirement TEXT,
    requirement_reason_code TEXT,
    selection_evidence JSONB,
    spec_digest TEXT,
    intent TEXT,
    operation TEXT,
    canonical_path TEXT,
    collision TEXT,
    target_source_cleanup JSONB,
    render_snapshot JSONB,
    plan_kind TEXT,
    plan_target_file_ref TEXT,
    plan_operation_ref TEXT,
    plan_evidence JSONB,
    plan_reason_code TEXT,
    plan_diagnostic JSONB,
    delivery_schema_version INTEGER,
    outcome TEXT,
    completion_mode TEXT,
    effect_class TEXT,
    final_object JSONB,
    delivery_source_cleanup TEXT,
    delivery_reason_code TEXT,
    evidence_digest TEXT,
    operation_id TEXT,
    receipt JSONB,
    delivered_at TIMESTAMPTZ,
    activity_logged_at TIMESTAMPTZ,
    CONSTRAINT acquisition_files_pkey PRIMARY KEY (acquisition_id, file_ref),
    CONSTRAINT acquisition_files_root_requirement_key UNIQUE (acquisition_id, file_ref, requirement),
    CONSTRAINT acquisition_files_acquisition_id_ordinal_key UNIQUE (acquisition_id, ordinal),
    CONSTRAINT acquisition_files_acquisition_id_relative_path_key UNIQUE (acquisition_id, relative_path),
    CONSTRAINT acquisition_files_canonical_path_key UNIQUE (acquisition_id, canonical_path),
    CONSTRAINT acquisition_files_acquisition_id_fkey FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT acquisition_files_exact_size_check CHECK (exact_size >= 0),
    CONSTRAINT acquisition_files_file_ref_check CHECK (char_length(file_ref) BETWEEN 1 AND 128),
    CONSTRAINT acquisition_files_identity_evidence_check CHECK (jsonb_typeof(identity_evidence) = 'object'),
    CONSTRAINT acquisition_files_media_kind_check CHECK (media_kind IN ('video', 'subtitle', 'other')),
    CONSTRAINT acquisition_files_ordinal_check CHECK (ordinal >= 0),
    CONSTRAINT acquisition_files_relative_path_check CHECK (char_length(relative_path) BETWEEN 1 AND 4096 AND relative_path !~ '^/' AND relative_path !~ '(^|/)\.\.(/|$)' AND relative_path !~ '[[:cntrl:]]'),
    CONSTRAINT acquisition_files_requirement_check CHECK (requirement IS NULL OR requirement IN ('required', 'excluded')),
    CONSTRAINT acquisition_files_requirement_shape_check CHECK ((requirement IS NULL) = (requirement_reason_code IS NULL) AND (requirement IS NULL) = (selection_evidence IS NULL)
        AND (requirement IS NULL OR (char_length(requirement_reason_code) BETWEEN 1 AND 128 AND jsonb_typeof(selection_evidence) = 'object'))),
    CONSTRAINT acquisition_files_target_check CHECK ((spec_digest IS NULL) = (intent IS NULL) AND (spec_digest IS NULL) = (operation IS NULL)
        AND (spec_digest IS NULL) = (canonical_path IS NULL) AND (spec_digest IS NULL) = (collision IS NULL)
        AND (spec_digest IS NULL) = (target_source_cleanup IS NULL) AND (spec_digest IS NULL) = (render_snapshot IS NULL)
        AND (spec_digest IS NULL OR (requirement = 'required' AND spec_digest ~ '^sha256:[0-9a-f]{64}$'
             AND intent IN ('ensure_present', 'replace', 'explicit') AND operation IN ('move', 'copy', 'hardlink', 'noop')
             AND collision IN ('block', 'version', 'overwrite') AND char_length(canonical_path) BETWEEN 1 AND 4096
             AND canonical_path !~ '[[:cntrl:]]' AND jsonb_typeof(target_source_cleanup) = 'object' AND jsonb_typeof(render_snapshot) = 'object'))),
    CONSTRAINT acquisition_files_render_episode_titles_check CHECK (render_snapshot IS NULL OR render_episode_titles_valid(render_snapshot)),
    CONSTRAINT acquisition_files_plan_kind_check CHECK (plan_kind IS NULL OR plan_kind IN ('execute', 'adopt', 'retain', 'failed', 'dependency_failed')),
    CONSTRAINT acquisition_files_plan_requirement_check CHECK (plan_kind IS NULL OR requirement = 'required'),
    CONSTRAINT acquisition_files_plan_shape_check CHECK ((plan_kind IS NULL) = (plan_diagnostic IS NULL)
        AND (plan_kind IS NULL OR (jsonb_typeof(plan_diagnostic) = 'object' AND (
             (plan_kind = 'execute' AND plan_target_file_ref = file_ref AND plan_operation_ref IS NOT NULL AND plan_evidence IS NULL AND plan_reason_code IS NULL AND spec_digest IS NOT NULL)
             OR (plan_kind IN ('adopt', 'retain') AND plan_target_file_ref = file_ref AND plan_operation_ref IS NULL AND plan_evidence IS NOT NULL AND jsonb_typeof(plan_evidence) = 'object' AND plan_evidence <> '{}'::jsonb AND plan_reason_code IS NULL AND spec_digest IS NOT NULL)
             OR (plan_kind = 'failed' AND plan_target_file_ref IS NULL AND plan_operation_ref IS NULL AND plan_evidence IS NULL AND char_length(plan_reason_code) BETWEEN 1 AND 128)
             OR (plan_kind = 'dependency_failed' AND plan_operation_ref IS NULL AND plan_evidence IS NULL AND char_length(plan_reason_code) BETWEEN 1 AND 128 AND (plan_target_file_ref IS NULL OR plan_target_file_ref = file_ref)))))),
    CONSTRAINT acquisition_files_plan_target_self_check CHECK (plan_target_file_ref IS NULL OR plan_target_file_ref = file_ref),
    CONSTRAINT acquisition_files_delivery_requirement_check CHECK (outcome IS NULL OR requirement = 'required'),
    CONSTRAINT acquisition_files_final_object_check CHECK (final_object IS NULL OR jsonb_typeof(final_object) = 'object'),
    CONSTRAINT acquisition_files_activity_logged_check CHECK (activity_logged_at IS NULL OR outcome IS NOT NULL),
    CONSTRAINT acquisition_files_delivery_shape_check CHECK ((outcome IS NULL) = (delivery_schema_version IS NULL) AND (outcome IS NULL) = (effect_class IS NULL)
        AND (outcome IS NULL) = (delivery_source_cleanup IS NULL) AND (outcome IS NULL) = (evidence_digest IS NULL)
        AND (outcome IS NULL) = (receipt IS NULL) AND (outcome IS NULL) = (delivered_at IS NULL)
        AND (outcome IS NULL OR (
              ((outcome = 'succeeded' AND completion_mode IS NOT NULL AND completion_mode IN ('executed', 'adopted', 'retain')
                AND effect_class IS NOT NULL AND effect_class IN ('unknown', 'no_external_effect', 'confirmed') AND final_object IS NOT NULL
                AND delivery_source_cleanup IS NOT NULL AND delivery_source_cleanup IN ('not_required', 'completed') AND delivery_reason_code IS NULL)
               OR (outcome = 'failed' AND completion_mode IS NULL
                AND effect_class IS NOT NULL AND effect_class IN ('unknown', 'no_external_effect', 'confirmed')
                AND delivery_source_cleanup IS NOT NULL AND delivery_source_cleanup IN ('not_required', 'failed', 'uncertain')
                AND delivery_reason_code IS NOT NULL AND char_length(delivery_reason_code) BETWEEN 1 AND 128))
              AND delivery_schema_version = 1 AND receipt IS NOT NULL
              AND jsonb_typeof(receipt) = 'object' AND octet_length(receipt::text) <= 65536
              AND evidence_digest ~ '^sha256:[0-9a-f]{64}$'
              AND (operation_id IS NULL OR char_length(operation_id) BETWEEN 1 AND 128))))
);

-- Committed file results the activity consumer has not logged (PRUNE-046 S2/S5).
CREATE INDEX acquisition_files_activity_unlogged_idx ON acquisition_files (delivered_at, acquisition_id, file_ref)
    WHERE activity_logged_at IS NULL;


CREATE FUNCTION accept_acquisition_queued_without_vars(p_acquisition_id text, p_entry_kind text, p_access_scope text, p_request_namespace text, p_request_key text, p_request_hash text, p_canonical_request jsonb, p_admission_digest text, p_config_identity jsonb, p_display_title jsonb, p_origin_managed_anime_ref text, p_detail_retention_seconds integer, p_receipt_after_accept_seconds integer, p_receipt_after_terminal_seconds integer, p_preparation_mode text, p_preparation_digest text, p_preparation_witness text, p_input_canonical_utf8 text, p_payload bytea, p_validated_infohash_v1 text, p_validated_infohash_v2 text, p_manifest_digest text, p_manifest_files jsonb, p_source_release_id text, p_download_task_id text, p_task_idempotency_key text, p_qbittorrent_connection_id text, p_task_frozen_input jsonb, p_task_frozen_policy jsonb, p_submit_work_item_id text, p_settle_work_item_id text, p_settle_idempotency_key text, p_submit_input_snapshot jsonb, p_max_attempts integer, p_qualification jsonb, p_business_snapshot jsonb, p_witness_max_bytes integer, p_witness_max_rows integer, p_correlation_id text, p_causation_type text, p_causation_id text, p_default_sections jsonb) RETURNS jsonb
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
                    OR (e ? 'count_in_limit' AND jsonb_typeof(e->'count_in_limit') IS DISTINCT FROM 'boolean')
                    OR (e ? 'effective_selected' AND jsonb_typeof(e->'effective_selected') IS DISTINCT FROM 'boolean')
                    OR (e ? 'manual_selected' AND jsonb_typeof(e->'manual_selected') NOT IN ('null','boolean'))
                    OR (CASE WHEN jsonb_typeof(e)='object' THEN EXISTS(SELECT 1 FROM jsonb_object_keys(e) k WHERE k NOT IN ('provider','group_ref','display_name','selection_mode','score','count_in_limit','effective_selected','manual_selected')) ELSE TRUE END)
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

COMMENT ON FUNCTION accept_acquisition_queued_without_vars(p_acquisition_id text, p_entry_kind text, p_access_scope text, p_request_namespace text, p_request_key text, p_request_hash text, p_canonical_request jsonb, p_admission_digest text, p_config_identity jsonb, p_display_title jsonb, p_origin_managed_anime_ref text, p_detail_retention_seconds integer, p_receipt_after_accept_seconds integer, p_receipt_after_terminal_seconds integer, p_preparation_mode text, p_preparation_digest text, p_preparation_witness text, p_input_canonical_utf8 text, p_payload bytea, p_validated_infohash_v1 text, p_validated_infohash_v2 text, p_manifest_digest text, p_manifest_files jsonb, p_source_release_id text, p_download_task_id text, p_task_idempotency_key text, p_qbittorrent_connection_id text, p_task_frozen_input jsonb, p_task_frozen_policy jsonb, p_submit_work_item_id text, p_settle_work_item_id text, p_settle_idempotency_key text, p_submit_input_snapshot jsonb, p_max_attempts integer, p_qualification jsonb, p_business_snapshot jsonb, p_witness_max_bytes integer, p_witness_max_rows integer, p_correlation_id text, p_causation_type text, p_causation_id text, p_default_sections jsonb) IS 'Qualified queued T01 with caller owner sections and default absence locks; 125 is not qualified. The input schema is flattened to schema_version 1 (PRUNE-046 S4).';

CREATE FUNCTION vars_evidence_consistent(p_root text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.acquisitions%ROWTYPE;v jsonb;
BEGIN
 SELECT * INTO a FROM public.acquisitions WHERE id=p_root;
 IF NOT FOUND THEN RETURN true; END IF;
 IF a.record_state='compact' THEN RETURN a.vars_evaluation_canonical_utf8 IS NULL; END IF;
 v:=a.input->'config'->'vars';
 IF a.vars_evidence_version=0 OR v->>'mode'='disabled' THEN RETURN a.vars_evaluation_canonical_utf8 IS NULL AND a.vars_evidence_digest IS NULL; END IF;
 RETURN a.vars_evaluation_canonical_utf8 IS NOT NULL AND public.vars_evaluation_valid(a.vars_evaluation_canonical_utf8,a.input_canonical_utf8,a.id,a.admission_digest);
END $$;

CREATE FUNCTION accept_acquisition_queued_with_vars(p_acquisition_id text, p_entry_kind text, p_access_scope text, p_request_namespace text, p_request_key text, p_request_hash text, p_canonical_request jsonb, p_admission_digest text, p_config_identity jsonb, p_display_title jsonb, p_origin_managed_anime_ref text, p_detail_retention_seconds integer, p_receipt_after_accept_seconds integer, p_receipt_after_terminal_seconds integer, p_preparation_mode text, p_preparation_digest text, p_preparation_witness text, p_input_canonical_utf8 text, p_payload bytea, p_validated_infohash_v1 text, p_validated_infohash_v2 text, p_manifest_digest text, p_manifest_files jsonb, p_source_release_id text, p_download_task_id text, p_task_idempotency_key text, p_qbittorrent_connection_id text, p_task_frozen_input jsonb, p_task_frozen_policy jsonb, p_submit_work_item_id text, p_settle_work_item_id text, p_settle_idempotency_key text, p_submit_input_snapshot jsonb, p_max_attempts integer, p_qualification jsonb, p_business_snapshot jsonb, p_witness_max_bytes integer, p_witness_max_rows integer, p_correlation_id text, p_causation_type text, p_causation_id text, p_default_sections jsonb, p_vars_evaluation text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 DECLARE result jsonb; evidence text;
 BEGIN
 result:=public.accept_acquisition_queued_without_vars(p_acquisition_id,p_entry_kind,p_access_scope,p_request_namespace,p_request_key,p_request_hash,p_canonical_request,p_admission_digest,p_config_identity,p_display_title,p_origin_managed_anime_ref,p_detail_retention_seconds,p_receipt_after_accept_seconds,p_receipt_after_terminal_seconds,p_preparation_mode,p_preparation_digest,p_preparation_witness,p_input_canonical_utf8,p_payload,p_validated_infohash_v1,p_validated_infohash_v2,p_manifest_digest,p_manifest_files,p_source_release_id,p_download_task_id,p_task_idempotency_key,p_qbittorrent_connection_id,p_task_frozen_input,p_task_frozen_policy,p_submit_work_item_id,p_settle_work_item_id,p_settle_idempotency_key,p_submit_input_snapshot,p_max_attempts,p_qualification,p_business_snapshot,p_witness_max_bytes,p_witness_max_rows,p_correlation_id,p_causation_type,p_causation_id,p_default_sections);
 IF result->>'status'='accepted' THEN
 IF p_input_canonical_utf8::jsonb->'config'->'vars'->>'mode'='disabled' THEN
 IF p_vars_evaluation IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='VARS_EVALUATION_EVIDENCE_INCONSISTENT'; END IF;
 ELSE
 IF public.vars_evaluation_valid(p_vars_evaluation,p_input_canonical_utf8,p_acquisition_id,p_admission_digest) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='VARS_EVALUATION_EVIDENCE_INCONSISTENT'; END IF;
 evidence:='sha256:'||encode(sha256(convert_to('acquisition-vars-evaluation:v1','UTF8')||decode('00','hex')||convert_to(p_vars_evaluation,'UTF8')),'hex');
 UPDATE public.acquisitions SET vars_evidence_digest=evidence, vars_evaluation_canonical_utf8=p_vars_evaluation WHERE id=p_acquisition_id;
 END IF;
 ELSE
 IF public.vars_evidence_consistent(result->'admission_receipt'->>'acquisition_id') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='VARS_EVALUATION_EVIDENCE_INCONSISTENT'; END IF;
 END IF;
 RETURN result;
 END $$;

CREATE FUNCTION acquisition_write_terminal(p_root text, p_state text, p_receipt jsonb, p_now timestamp with time zone) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 UPDATE public.acquisitions SET
   state=p_state, terminal_at=p_now, terminal_schema='native_terminal_v1', terminal_receipt=p_receipt,
   receipt_until=GREATEST(receipt_until, p_now + receipt_after_terminal_seconds*INTERVAL '1 second'),
   detail_until=p_now + detail_retention_seconds*INTERVAL '1 second',
   checked_revision=facts_revision, settlement_blocked_code=NULL, version=version+1,
   -- A succeeded download root is a download completion candidate until its
   -- download_completions row exists (download_completions_close_candidate).
   completion_open=(p_state='succeeded' AND execution_branch='download')
 WHERE id=p_root AND state='running' AND record_state='full';
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_ROOT_NOT_RUNNING'; END IF;
 PERFORM public.acquisition_consume_settle_work(p_root);
END $$;

CREATE FUNCTION acquisition_dispose_retired(p_root text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; v_owner text; lifecycle text; now_at timestamptz:=clock_timestamp();
 status text; counts jsonb:=NULL; req_digest text:=NULL; target_digest text:=NULL;
 succeeded integer; failed integer; stage text; has_confirmed boolean; has_unknown boolean;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND OR r.state<>'running' OR r.record_state<>'full' THEN RETURN 'not_running'; END IF;
 v_owner:=public.acquisition_owner(p_root);
 SELECT l.lifecycle_state INTO lifecycle FROM public.managed_anime l WHERE l.id=v_owner;
 IF lifecycle IS DISTINCT FROM 'retired' THEN RETURN 'owner_active'; END IF;
 IF public.acquisition_unresolved_effect(p_root) THEN RETURN 'effect_unresolved'; END IF;

 SELECT count(*) FILTER (WHERE d.outcome='succeeded'), count(*) FILTER (WHERE d.outcome='failed')
  INTO succeeded, failed FROM public.acquisition_files d WHERE d.acquisition_id=p_root;
 IF r.requirements_digest IS NULL THEN
  status:=CASE WHEN r.manifest_digest IS NOT NULL THEN 'incomplete' ELSE 'not_reached' END;
 ELSIF succeeded+failed=r.requirements_required_count THEN
  status:='sealed'; req_digest:=r.requirements_digest; target_digest:=r.target_digest;
  counts:=jsonb_build_object('total',r.requirements_item_count,'required',r.requirements_required_count,'excluded',r.requirements_item_count-r.requirements_required_count,
    'succeeded',succeeded,'failed',failed);
 ELSE
  status:='incomplete';
 END IF;
 SELECT CASE
   WHEN t.id IS NULL OR t.state IN ('queued','submitting') THEN 'submit'
   WHEN t.state IN ('downloading','waiting_retry','failed') THEN 'download_wait'
   WHEN NOT EXISTS(SELECT 1 FROM public.artifacts a WHERE a.acquisition_id=p_root AND a.state='verified') THEN 'inspect'
   WHEN NOT EXISTS(SELECT 1 FROM public.postprocessing_plans p WHERE p.acquisition_id=p_root AND p.state='succeeded') THEN 'plan'
   ELSE 'file_execution' END INTO stage
 FROM (SELECT 1) one LEFT JOIN public.download_tasks t ON t.acquisition_id=p_root;
has_confirmed:=EXISTS(SELECT 1 FROM public.acquisition_files d WHERE d.acquisition_id=p_root AND d.effect_class='confirmed');
has_unknown:=EXISTS(SELECT 1 FROM public.acquisition_files d WHERE d.acquisition_id=p_root AND d.effect_class='unknown')
   OR EXISTS(SELECT 1 FROM public.work_items w
      WHERE w.state IN ('available','leased') AND w.external_effect_admitted_at IS NOT NULL
        AND w.acquisition_id=p_root);
 PERFORM public.acquisition_write_terminal(p_root,'cancelled',jsonb_build_object(
   'schema_version',1,'outcome','cancelled','requirements_status',status,'counts',counts,
   'requirements_digest',req_digest,'target_digest',CASE WHEN status='sealed' THEN target_digest END,
   'primary_failure',jsonb_build_object('stage',stage,'reason_code','OWNER_RETIRED','file_ref',NULL),
   'effect_summary',jsonb_build_object('complete',false,'has_confirmed',has_confirmed,'has_unknown',has_unknown),
   'settled_at',public.acquisition_terminal_time_text(now_at)),
   now_at);
 RETURN 'cancelled';
END $$;

CREATE FUNCTION acquisition_settle(p_root text, p_facts_revision bigint, p_receipt jsonb, p_synthesized jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; now_at timestamptz:=clock_timestamp(); s record; receipt_text text; outcome text;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_ROOT_MISSING'; END IF;
 IF r.state<>'running' OR r.record_state<>'full' THEN RETURN jsonb_build_object('status','already_terminal','state',r.state); END IF;
 IF r.execution_branch<>'download' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_BRANCH'; END IF;
 IF r.facts_revision<>p_facts_revision THEN RETURN jsonb_build_object('status','stale'); END IF;
 IF public.acquisition_live_work(p_root) THEN RETURN jsonb_build_object('status','live_work'); END IF;
 outcome:=p_receipt->>'outcome';
 IF jsonb_typeof(p_receipt)<>'object' OR (p_receipt->>'schema_version')::int IS DISTINCT FROM 1
   OR outcome NOT IN ('succeeded','failed') OR jsonb_typeof(p_synthesized)<>'array' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_DECISION_SHAPE'; END IF;
 FOR s IN SELECT x.file_ref, x.reason_code FROM jsonb_to_recordset(p_synthesized) AS x(file_ref text, reason_code text) LOOP
  IF NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p_root AND f.file_ref=s.file_ref AND f.requirement='required')
    OR EXISTS(SELECT 1 FROM public.acquisition_files d WHERE d.acquisition_id=p_root AND d.file_ref=s.file_ref AND d.outcome IS NOT NULL) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_SYNTHESIZED_FILE'; END IF;
  receipt_text:=jsonb_build_object('reason_code',s.reason_code,'schema_version',1,'synthesized',true)::text;
  UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
    delivery_source_cleanup='not_required',delivery_reason_code=s.reason_code,
    evidence_digest=public.file_proof_digest('acquisition-settlement-synthesized:v1',receipt_text),receipt=receipt_text::jsonb,
    delivered_at=CURRENT_TIMESTAMP WHERE acquisition_id=p_root AND file_ref=s.file_ref;
 END LOOP;
 IF outcome='succeeded' AND EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p_root AND f.requirement='required'
   AND f.outcome IS DISTINCT FROM 'succeeded') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_SUCCESS_UNPROVEN'; END IF;
 PERFORM public.acquisition_write_terminal(p_root,outcome,
   p_receipt || jsonb_build_object('settled_at',public.acquisition_terminal_time_text(now_at)),now_at);
 RETURN jsonb_build_object('status','settled','state',outcome);
END $$;

CREATE FUNCTION acquisition_settle_defer(p_root text, p_facts_revision bigint, p_blocked_code text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE;
BEGIN
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND OR r.state<>'running' OR r.record_state<>'full' THEN RETURN jsonb_build_object('status','not_running'); END IF;
 IF r.facts_revision<>p_facts_revision THEN RETURN jsonb_build_object('status','stale'); END IF;
 IF p_blocked_code IS NOT NULL AND p_blocked_code<>'EXECUTION_GRAPH_INCOMPLETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_SETTLE_DEFER_CODE'; END IF;
 IF p_blocked_code IS NOT NULL THEN
  UPDATE public.acquisitions SET settlement_blocked_code=p_blocked_code,
    settlement_error_count=LEAST(settlement_error_count+1,5) WHERE id=p_root;
 END IF;
 PERFORM public.acquisition_consume_settle_work(p_root);
 RETURN jsonb_build_object('status','deferred');
END $$;

CREATE FUNCTION acquisition_unknown_files_admission() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE files jsonb;
BEGIN
 files:=public.acquisition_unknown_files(NEW.id);
 IF jsonb_array_length(files)=0 THEN RETURN NULL; END IF;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
  managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'unknown_files_imported','acquisition',NEW.id,NEW.version,'unknown_files',
  NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',public.unknown_files_notification(NEW.id,files),'pending_target_resolution')
 ON CONFLICT (trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key) DO NOTHING;
 RETURN NULL;
END $$;

CREATE FUNCTION stale_failed_candidate_decisions() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NEW.source_release_ref IS NULL OR NEW.origin_managed_anime_ref IS NULL THEN
        RETURN NEW;
    END IF;
    UPDATE public.replacement_decisions d
    SET state = 'stale',
        stale_reason = 'candidate_acquisition_failed',
        version = d.version + 1,
        updated_at = CURRENT_TIMESTAMP
    FROM public.managed_anime lc
    WHERE lc.id = d.managed_anime_id
      AND lc.lifecycle_state <> 'retired'
      AND d.managed_anime_id = NEW.origin_managed_anime_ref
      AND d.candidate_release_id = NEW.source_release_ref
      AND d.candidate_placement_id IS NULL
      AND d.state = 'admitted'
      -- A key the release already placed (verified, active) keeps its
      -- decision: the evaluation binds it to that placement and settles it.
      AND NOT EXISTS (
          SELECT 1
          FROM public.placements p
          JOIN public.placement_coverage pc ON pc.placement_id = p.id
          WHERE p.managed_anime_id = d.managed_anime_id
            AND p.quality_facts_ref = d.candidate_release_id
            AND p.source_evidence_type = 'file_operation'
            AND p.state = 'active'
            AND p.verification_state = 'verified'
            AND pc.observation_key = d.coverage_key
      )
      AND NOT EXISTS (
          SELECT 1 FROM public.acquisitions r
          WHERE r.source_release_ref = NEW.source_release_ref
            AND r.origin_managed_anime_ref = NEW.origin_managed_anime_ref
            AND r.state = 'running'
            AND r.id <> NEW.id
      );
    RETURN NEW;
END;
$$;

CREATE FUNCTION vars_filefact_valid(p_root text, p_fact jsonb) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.acquisitions%ROWTYPE; input jsonb; e jsonb; expected jsonb;
BEGIN
 SELECT * INTO STRICT a FROM public.acquisitions WHERE id=p_root;
 SELECT q.input INTO STRICT input FROM public.acquisitions q WHERE q.id=p_root;
 IF public.vars_evidence_consistent(p_root) IS NOT TRUE THEN RETURN false; END IF;
 IF input->'config'->'vars'->>'mode'='disabled' THEN
 expected:=jsonb_build_object('state','absent','value',NULL,'reason_code',NULL);
 ELSIF a.vars_evidence_version=0 THEN
 expected:=jsonb_build_object('state','unavailable','value',NULL,'reason_code','VARS_EVALUATION_RECORD_UNAVAILABLE');
 ELSE
 SELECT q.vars_evaluation INTO STRICT e FROM public.acquisitions q WHERE q.id=p_root;
 expected:=jsonb_build_object('state','known','reason_code',NULL,'value',jsonb_build_object('program_digest',e->'program_digest','input_digest',e->'input_digest','output',e->'output','runtime_version',e->'runtime_version','evaluated_at',e->'evaluated_at'));
 END IF;
 RETURN p_fact->'render_inputs'->'vars'=expected;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $$;

CREATE TRIGGER acquisition_unknown_files_notification
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN (((old.state = 'running'::text) AND (new.state = ANY (ARRAY['succeeded'::text, 'failed'::text])))) EXECUTE FUNCTION acquisition_unknown_files_admission();
CREATE TRIGGER acquisitions_candidate_failed
    AFTER UPDATE OF state ON acquisitions
    FOR EACH ROW WHEN (((old.state IS DISTINCT FROM 'failed'::text) AND (new.state = 'failed'::text))) EXECUTE FUNCTION stale_failed_candidate_decisions();
GRANT SELECT ON TABLE acquisitions, acquisition_files TO bansoko_runtime;
REVOKE ALL ON FUNCTION render_episode_titles_valid,
    accept_acquisition_queued_without_vars, vars_evidence_consistent,
    accept_acquisition_queued_with_vars, acquisition_write_terminal,
    acquisition_dispose_retired, acquisition_settle, acquisition_settle_defer,
    acquisition_unknown_files_admission, vars_filefact_valid FROM PUBLIC;
GRANT EXECUTE ON FUNCTION render_episode_titles_valid,
    vars_evidence_consistent, accept_acquisition_queued_with_vars, acquisition_dispose_retired,
    acquisition_settle, acquisition_settle_defer TO bansoko_runtime;

-- ============================================================================
-- Download tasks, work items, effect publications, idempotency and one-shot acquisitions
-- Download tasks (qBittorrent submit/sync stages), the work-item queue with its
-- fencing tokens and attempt budgets, effect publications, request idempotency,
-- one-shot acquisitions and the download submission evidence recovery reads.
-- ============================================================================

CREATE TABLE download_tasks (
    id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    qbittorrent_connection_id TEXT,
    provider_correlation JSONB,
    frozen_input JSONB NOT NULL,
    frozen_policy JSONB NOT NULL,
    state TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    failure_code TEXT,
    failure_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    qbittorrent_connection_kind TEXT GENERATED ALWAYS AS (CASE WHEN qbittorrent_connection_id IS NULL THEN NULL::text ELSE 'qbittorrent' END) STORED,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    acquisition_id TEXT NOT NULL,
    stage_policy_version INTEGER,
    submit_ready_at TIMESTAMPTZ,
    submit_wall_until TIMESTAMPTZ,
    submit_max_attempts INTEGER,
    submit_attempt_timeout_seconds INTEGER,
    submit_attempts_started INTEGER NOT NULL DEFAULT 0,
    submit_attempt_ticket JSONB,
    submit_attempt_index JSONB NOT NULL DEFAULT '[]'::jsonb,
    budget_clock_at TIMESTAMPTZ,
    wait_started_at TIMESTAMPTZ,
    wait_until TIMESTAMPTZ,
    poll_interval_seconds INTEGER,
    sync_attempt_timeout_seconds INTEGER,
    max_consecutive_errors INTEGER,
    max_total_errors INTEGER,
    max_polls INTEGER,
    polls_started INTEGER NOT NULL DEFAULT 0,
    next_poll_ordinal INTEGER NOT NULL DEFAULT 1,
    consecutive_errors INTEGER NOT NULL DEFAULT 0,
    total_errors INTEGER NOT NULL DEFAULT 0,
    active_poll_ticket JSONB,
    stall_since TIMESTAMPTZ,
    stall_seconds INTEGER,
    submit_reconcile_wall_seconds INTEGER,
    submit_max_lookups INTEGER,
    verified_bytes_highwater BIGINT,
    payload_total_bytes BIGINT,
    last_progress_at TIMESTAMPTZ,
    progress_activated_at TIMESTAMPTZ,
    activity_submit_logged_at TIMESTAMPTZ,
    activity_terminal_logged_at TIMESTAMPTZ,
    -- PRUNE-046 S6 (revised 10-15): the single current submission-evidence slot,
    -- moved verbatim off `download_submission_evidence`. The slot exists when
    -- submit_permit_id is not NULL. The lookup-ticket history moved to
    -- submit_lookup_count + submit_lookup_ticket (the current ticket only).
    -- `submit_attempt` counts slot (re)writes and increments by one only when an
    -- overwrite is admitted (an empty slot, or retry_safe IS TRUE).
    submit_permit_id TEXT,
    submit_dispatch_state TEXT,
    submit_effect_class TEXT,
    submit_effect_key TEXT,
    submit_spec_digest TEXT,
    submit_executor_session TEXT,
    submit_work_item_id TEXT,
    submit_fencing_token BIGINT,
    submit_owner_version BIGINT,
    submit_attempt INTEGER NOT NULL DEFAULT 0,
    submit_prepared_at TIMESTAMPTZ,
    submit_dispatch_started_at TIMESTAMPTZ,
    submit_closed_proof JSONB,
    submit_resolution_proof JSONB,
    submit_observation JSONB,
    submit_conflict_evidence JSONB,
    submit_retry_safe BOOLEAN NOT NULL DEFAULT false,
    submit_unknown_since TIMESTAMPTZ,
    submit_reconcile_until TIMESTAMPTZ,
    submit_lookup_count INTEGER NOT NULL DEFAULT 0,
    submit_lookup_ticket JSONB,
    submit_path_snapshot JSONB,
    submit_adopted_settlement_receipt JSONB,
    CONSTRAINT download_tasks_pkey PRIMARY KEY (id),
    -- PRUNE-046 S6: the submission-evidence checks, moved verbatim with the
    -- columns renamed. The slot EXISTS iff submit_permit_id IS NOT NULL.
    CONSTRAINT download_tasks_submit_slot_check CHECK ((submit_dispatch_state = 'prepared' AND submit_dispatch_started_at IS NULL AND submit_closed_proof IS NULL) OR (submit_dispatch_state = 'dispatching' AND submit_dispatch_started_at IS NOT NULL AND submit_closed_proof IS NULL) OR (submit_dispatch_state = 'closed' AND submit_closed_proof IS NOT NULL AND (submit_dispatch_started_at IS NOT NULL OR ((submit_closed_proof ->> 'kind') = 'no_dispatch') IS TRUE)) OR (submit_permit_id IS NULL AND submit_dispatch_state IS NULL)),
    CONSTRAINT download_tasks_submit_slot_shape_check CHECK ((submit_permit_id IS NULL) = (submit_dispatch_state IS NULL)),
    CONSTRAINT download_tasks_submit_identity_shape_check CHECK (submit_permit_id IS NULL OR (submit_effect_class IS NOT NULL AND submit_effect_key IS NOT NULL AND submit_spec_digest IS NOT NULL AND submit_executor_session IS NOT NULL AND submit_work_item_id IS NOT NULL AND submit_fencing_token IS NOT NULL AND submit_owner_version IS NOT NULL AND submit_prepared_at IS NOT NULL AND submit_path_snapshot IS NOT NULL AND submit_reconcile_wall_seconds IS NOT NULL AND submit_max_lookups IS NOT NULL)),
    CONSTRAINT download_tasks_submit_prepared_effect_check CHECK (submit_dispatch_state <> 'prepared' OR submit_effect_class = 'unknown'),
    CONSTRAINT download_tasks_submit_dispatch_started_check CHECK (submit_dispatch_started_at IS NULL OR submit_dispatch_started_at >= submit_prepared_at),
    CONSTRAINT download_tasks_submit_unknown_window_check CHECK ((submit_unknown_since IS NULL AND submit_reconcile_until IS NULL) OR (submit_unknown_since IS NOT NULL AND submit_reconcile_until IS NOT NULL AND submit_reconcile_until >= submit_unknown_since)),
    CONSTRAINT download_tasks_submit_lookup_budget_check CHECK (submit_lookup_count <= submit_max_lookups),
    CONSTRAINT download_tasks_submit_reconcile_until_check CHECK (submit_reconcile_until IS NULL OR submit_reconcile_until = (submit_unknown_since + (submit_reconcile_wall_seconds::double precision * '00:00:01'::interval))),
    CONSTRAINT download_tasks_submit_retry_safe_check CHECK (NOT submit_retry_safe OR (submit_dispatch_state = 'closed' AND submit_effect_class = 'no_external_effect' AND submit_resolution_proof IS NOT NULL)),
    CONSTRAINT download_tasks_submit_closed_proof_check CHECK (submit_closed_proof IS NULL OR (jsonb_typeof(submit_closed_proof) = 'object' AND submit_closed_proof <> '{}'::jsonb AND octet_length(submit_closed_proof::text) <= 65536)),
    CONSTRAINT download_tasks_submit_conflict_evidence_check CHECK (submit_conflict_evidence IS NULL OR (jsonb_typeof(submit_conflict_evidence) = 'object' AND submit_conflict_evidence <> '{}'::jsonb AND octet_length(submit_conflict_evidence::text) <= 65536)),
    CONSTRAINT download_tasks_submit_dispatch_state_check CHECK (submit_dispatch_state IS NULL OR submit_dispatch_state IN ('prepared', 'dispatching', 'closed')),
    CONSTRAINT download_tasks_submit_effect_class_check CHECK (submit_effect_class IS NULL OR submit_effect_class IN ('unknown', 'no_external_effect', 'confirmed')),
    CONSTRAINT download_tasks_submit_effect_key_check CHECK (submit_effect_key IS NULL OR char_length(submit_effect_key) BETWEEN 1 AND 512),
    CONSTRAINT download_tasks_submit_executor_session_check CHECK (submit_executor_session IS NULL OR char_length(submit_executor_session) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_submit_fencing_token_check CHECK (submit_fencing_token IS NULL OR submit_fencing_token >= 1),
    CONSTRAINT download_tasks_submit_permit_id_check CHECK (submit_permit_id IS NULL OR char_length(submit_permit_id) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_submit_lookup_count_check CHECK (submit_lookup_count BETWEEN 0 AND 100),
    CONSTRAINT download_tasks_submit_lookup_ticket_check CHECK (submit_lookup_ticket IS NULL OR (jsonb_typeof(submit_lookup_ticket) = 'object' AND submit_lookup_ticket <> '{}'::jsonb AND octet_length(submit_lookup_ticket::text) <= 65536)),
    CONSTRAINT download_tasks_submit_observation_check CHECK (submit_observation IS NULL OR (jsonb_typeof(submit_observation) = 'object' AND octet_length(submit_observation::text) <= 524288)),
    CONSTRAINT download_tasks_submit_owner_version_check CHECK (submit_owner_version IS NULL OR submit_owner_version >= 1),
    CONSTRAINT download_tasks_submit_path_snapshot_check CHECK (submit_path_snapshot IS NULL OR (jsonb_typeof(submit_path_snapshot) = 'object' AND octet_length(submit_path_snapshot::text) <= 524288)),
    CONSTRAINT download_tasks_submit_reconcile_wall_seconds_check CHECK (submit_reconcile_wall_seconds IS NULL OR submit_reconcile_wall_seconds BETWEEN 1 AND 604800),
    CONSTRAINT download_tasks_submit_resolution_proof_check CHECK (submit_resolution_proof IS NULL OR (jsonb_typeof(submit_resolution_proof) = 'object' AND submit_resolution_proof <> '{}'::jsonb AND octet_length(submit_resolution_proof::text) <= 65536)),
    CONSTRAINT download_tasks_submit_spec_digest_check CHECK (submit_spec_digest IS NULL OR submit_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT download_tasks_submit_work_item_id_check CHECK (submit_work_item_id IS NULL OR char_length(submit_work_item_id) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_acquisition_uq UNIQUE (acquisition_id),
    CONSTRAINT download_tasks_id_acquisition_uq UNIQUE (id, acquisition_id),
    CONSTRAINT download_tasks_id_idempotency_key_key UNIQUE (id, idempotency_key),
    CONSTRAINT download_tasks_id_release_id_key UNIQUE (id, release_id),
    CONSTRAINT download_tasks_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT download_tasks_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT download_tasks_qbittorrent_connection_fkey FOREIGN KEY (qbittorrent_connection_id, qbittorrent_connection_kind) REFERENCES connections (id, kind),
    CONSTRAINT download_tasks_release_fk FOREIGN KEY (release_id) REFERENCES releases (id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT download_tasks_acquisition_id_check CHECK (acquisition_id IS NULL OR char_length(acquisition_id) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_failure_reason_check CHECK (failure_reason IS NULL OR char_length(failure_reason) <= 2048),
    CONSTRAINT download_tasks_frozen_input_check CHECK (jsonb_typeof(frozen_input) = 'object' AND pg_column_size(frozen_input) <= 1048576),
    CONSTRAINT download_tasks_frozen_policy_check CHECK (jsonb_typeof(frozen_policy) = 'object' AND pg_column_size(frozen_policy) <= 1048576),
    CONSTRAINT download_tasks_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT download_tasks_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT download_tasks_payload_total_bytes_check CHECK (payload_total_bytes IS NULL OR payload_total_bytes >= 0),
    CONSTRAINT download_tasks_provider_correlation_check CHECK (provider_correlation IS NULL OR (jsonb_typeof(provider_correlation) = 'object' AND pg_column_size(provider_correlation) <= 262144)),
    CONSTRAINT download_tasks_state_check CHECK (state IN ('queued', 'submitting', 'downloading', 'completed', 'waiting_retry', 'failed')),
    CONSTRAINT download_tasks_verified_bytes_highwater_check CHECK (verified_bytes_highwater IS NULL OR verified_bytes_highwater >= 0),
    CONSTRAINT download_tasks_version_check CHECK (version >= 1)
);

ALTER TABLE releases
    ADD CONSTRAINT releases_download_task_owner_fk FOREIGN KEY (download_task_id, id) REFERENCES download_tasks (id, release_id) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX download_tasks_correlation_idx ON download_tasks (correlation_id, created_at, id)
    WHERE correlation_id IS NOT NULL;
CREATE INDEX download_tasks_release_idx ON download_tasks (release_id, created_at, id);
CREATE UNIQUE INDEX download_tasks_submit_permit_uq ON download_tasks (submit_permit_id)
    WHERE submit_permit_id IS NOT NULL;
-- Open rows of the background passes (PRUNE-046 S2): the qB sync dispatcher's
-- unfinished tasks and the activity consumer's unlogged submission and
-- terminal entries. Keyed by id, not updated_at, and the submission index
-- does not name provider_correlation (a sync poll may rewrite it), so progress
-- updates of a downloading task stay HOT; the passes sort their few rows in
-- memory.
COMMENT ON COLUMN download_tasks.activity_submit_logged_at IS 'When the activity consumer logged the confirmed qB submission (NULL: not yet).';
COMMENT ON COLUMN download_tasks.activity_terminal_logged_at IS 'When the activity consumer logged the completed/failed download (NULL: not yet).';
CREATE INDEX download_tasks_open_idx ON download_tasks (id)
    WHERE state IN ('queued', 'submitting', 'downloading', 'waiting_retry');
CREATE INDEX download_tasks_activity_submit_idx ON download_tasks (id)
    WHERE activity_submit_logged_at IS NULL;
CREATE INDEX download_tasks_activity_terminal_idx ON download_tasks (id)
    WHERE state IN ('completed', 'failed') AND activity_terminal_logged_at IS NULL;

CREATE TABLE work_items (
    id TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'available',
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until TIMESTAMPTZ,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    input_snapshot JSONB NOT NULL,
    expected_owner_version BIGINT NOT NULL,
    last_error_code TEXT,
    last_error_reason TEXT,
    retention_days SMALLINT NOT NULL DEFAULT 7,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cancel_requested BOOLEAN NOT NULL DEFAULT false,
    managed_anime_id TEXT,
    owner_lifecycle_epoch BIGINT,
    external_effect_admitted_at TIMESTAMPTZ,
    recovery_blocked BOOLEAN NOT NULL DEFAULT false,
    recovery_reason TEXT,
    acquisition_id TEXT,
    CONSTRAINT work_items_pkey PRIMARY KEY (id),
    -- The live queue holds at most one item per owner and kind and per
    -- effect key; terminal rows live in work_item_outcomes.
    CONSTRAINT work_items_live_owner_kind_uq UNIQUE (owner_type, owner_id, kind),
    CONSTRAINT work_items_live_effect_uq UNIQUE (owner_type, kind, idempotency_key),
    CONSTRAINT work_items_acquisition_id_fkey FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT work_items_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT work_items_acquisition_root_check CHECK ((acquisition_id IS NOT NULL) = (owner_type IN ('download_task', 'artifact', 'file_operation', 'postprocessing_plan', 'acquisition')) AND (owner_type <> 'acquisition' OR acquisition_id = owner_id)),
    CONSTRAINT work_items_attempt_check CHECK (attempt >= 0),
    CONSTRAINT work_items_check CHECK (attempt <= max_attempts),
    CONSTRAINT work_items_check2 CHECK ((state = 'available' AND lease_until IS NULL) OR (state = 'leased' AND lease_until IS NOT NULL)),
    CONSTRAINT work_items_expected_owner_version_check CHECK (expected_owner_version >= 1),
    CONSTRAINT work_items_fencing_token_check CHECK (fencing_token >= 0),
    CONSTRAINT work_items_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT work_items_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT work_items_input_snapshot_check CHECK (jsonb_typeof(input_snapshot) = 'object' AND pg_column_size(input_snapshot) <= 1048576),
    CONSTRAINT work_items_last_error_code_check CHECK (last_error_code IS NULL OR char_length(btrim(last_error_code)) BETWEEN 1 AND 128),
    CONSTRAINT work_items_last_error_reason_check CHECK (last_error_reason IS NULL OR char_length(last_error_reason) <= 2048),
    CONSTRAINT work_items_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 100),
    CONSTRAINT work_items_owner_id_check CHECK (char_length(btrim(owner_id)) BETWEEN 1 AND 128),
    -- Generic owner reference: owner_type names the owner table and owner_id
    -- its key. Owner existence, the owner's acquisition root and the
    -- effect-key pin are checked on insert by fence_work_item_retirement and
    -- frozen afterwards; only the root and managed anime are real FKs.
    CONSTRAINT work_items_owner_kind_check CHECK ((owner_type = 'rss_source_runtime' AND kind = 'rss_refresh') OR (owner_type = 'release' AND kind = 'release_evaluate') OR (owner_type = 'download_task' AND kind IN ('qb_submit', 'qb_sync')) OR (owner_type = 'artifact' AND kind = 'artifact_inspect') OR (owner_type = 'file_operation' AND kind = 'file_execute') OR (owner_type = 'plugin_delivery' AND kind = 'plugin_deliver') OR (owner_type = 'file_maintenance' AND kind IN ('reconciliation_scan', 'replacement_evaluate', 'reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement')) OR (owner_type = 'alignment_operation' AND kind = 'alignment_execute') OR (owner_type = 'cleanup_operation' AND kind IN ('cleanup_quarantine', 'cleanup_purge')) OR (owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute') OR (owner_type = 'postprocessing_plan' AND kind = 'postprocess_plan') OR (owner_type = 'acquisition' AND kind = 'acquisition_settle') OR (owner_type = 'file_manager_operation' AND kind = 'file_manager_execute') OR (owner_type = 'download_completion' AND kind = 'completion_execute')),
    CONSTRAINT work_items_owner_lifecycle_epoch_check CHECK (owner_lifecycle_epoch IS NULL OR owner_lifecycle_epoch >= 1),
    CONSTRAINT work_items_recovery_reason_check CHECK ((NOT recovery_blocked AND recovery_reason IS NULL) OR (recovery_blocked AND recovery_reason IN ('cancel_requested', 'attempt_exhausted', 'effect_unknown', 'SUBMIT_RECONCILE_EXHAUSTED'))),
    CONSTRAINT work_items_retention_days_check CHECK (retention_days BETWEEN 1 AND 30),
    CONSTRAINT work_items_state_check CHECK (state IN ('available', 'leased'))
);

CREATE INDEX work_items_claim_idx ON work_items (available_at, id)
    WHERE state = 'available';
-- Claim paths (SCALE-034). work_item_claim_due walks one kind's due items in
-- (available_at, id) order; block_unrunnable and the recovery-observation
-- claim read only their (normally empty) quarantine sets instead of every
-- live row of work_items on each claim tick.
CREATE INDEX work_items_claim_kind_idx ON work_items (kind, available_at, id)
    WHERE state = 'available';
CREATE INDEX work_items_unrunnable_idx ON work_items (id)
    WHERE recovery_blocked = FALSE AND (cancel_requested OR attempt >= max_attempts);
CREATE INDEX work_items_recovery_claim_idx ON work_items (available_at, id)
    WHERE recovery_blocked;
-- Root and managed-anime readers (acquisition live-work/compaction checks,
-- retirement sets, owner cleanup) and the referencing side of both FKs.
CREATE INDEX work_items_acquisition_idx ON work_items (acquisition_id)
    WHERE acquisition_id IS NOT NULL;
CREATE INDEX work_items_managed_anime_idx ON work_items (managed_anime_id)
    WHERE managed_anime_id IS NOT NULL;
CREATE INDEX work_items_reclaim_idx ON work_items (lease_until, id)
    WHERE state = 'leased';
-- Terminal WorkItems (PRUNE-046 S1c). A live row of work_items moves here in
-- one transaction when it succeeds or fails (work_item_close); the live queue
-- only ever holds available and leased rows. No foreign keys: an outcome is
-- retained evidence of a past effect and is purged by retention_until alone.
CREATE TABLE work_item_outcomes (
    id TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    input_snapshot JSONB NOT NULL,
    expected_owner_version BIGINT NOT NULL,
    last_error_code TEXT,
    last_error_reason TEXT,
    retention_days SMALLINT NOT NULL DEFAULT 7,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cancel_requested BOOLEAN NOT NULL DEFAULT false,
    managed_anime_id TEXT,
    owner_lifecycle_epoch BIGINT,
    external_effect_admitted_at TIMESTAMPTZ,
    recovery_blocked BOOLEAN NOT NULL DEFAULT false,
    recovery_reason TEXT,
    acquisition_id TEXT,
    terminal_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retention_until TIMESTAMPTZ NOT NULL,
    CONSTRAINT work_item_outcomes_pkey PRIMARY KEY (id),
    CONSTRAINT work_item_outcomes_retention_check CHECK (retention_days BETWEEN 1 AND 30 AND retention_until = (terminal_at + (retention_days::double precision * '1 day'::interval))),
    CONSTRAINT work_item_outcomes_state_check CHECK (state IN ('succeeded', 'failed'))
);

CREATE INDEX work_item_outcomes_owner_idx ON work_item_outcomes (owner_type, owner_id, kind, terminal_at DESC, id DESC);
CREATE INDEX work_item_outcomes_retention_idx ON work_item_outcomes (retention_until, id);
CREATE INDEX work_item_outcomes_acquisition_idx ON work_item_outcomes (acquisition_id)
    WHERE acquisition_id IS NOT NULL;
CREATE INDEX work_item_outcomes_managed_anime_idx ON work_item_outcomes (managed_anime_id)
    WHERE managed_anime_id IS NOT NULL;

-- Every WorkItem row, live or terminal, for readers that replay a committed
-- receipt or check the whole history of an owner. Live rows carry NULL
-- terminal_at/retention_until; outcomes carry NULL lease_until.
CREATE VIEW work_item_history AS
SELECT id, owner_type, owner_id, kind, state, available_at, lease_until, fencing_token, idempotency_key,
       attempt, max_attempts, input_snapshot, expected_owner_version, last_error_code, last_error_reason,
       retention_days, created_at, updated_at, cancel_requested, managed_anime_id, owner_lifecycle_epoch,
       external_effect_admitted_at, recovery_blocked, recovery_reason, acquisition_id,
       NULL::timestamptz AS terminal_at, NULL::timestamptz AS retention_until
FROM work_items
UNION ALL
SELECT id, owner_type, owner_id, kind, state, available_at, NULL::timestamptz, fencing_token, idempotency_key,
       attempt, max_attempts, input_snapshot, expected_owner_version, last_error_code, last_error_reason,
       retention_days, created_at, updated_at, cancel_requested, managed_anime_id, owner_lifecycle_epoch,
       external_effect_admitted_at, recovery_blocked, recovery_reason, acquisition_id,
       terminal_at, retention_until
FROM work_item_outcomes;

CREATE TABLE idempotency_records (
    kind TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    managed_anime_id TEXT,
    release_id TEXT,
    plugin_target_config_id TEXT,
    owner_type TEXT,
    owner_id TEXT,
    failed_work_item_id TEXT,
    result_work_item_id TEXT,
    expected_version BIGINT,
    trigger_kind TEXT,
    canonical_request_body JSONB,
    delivery_id TEXT,
    result_snapshot JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT idempotency_records_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT idempotency_records_release_id_fkey FOREIGN KEY (release_id) REFERENCES releases (id),
    CONSTRAINT idempotency_records_canonical_request_body_check CHECK (canonical_request_body IS NULL OR (jsonb_typeof(canonical_request_body) = 'object' AND pg_column_size(canonical_request_body) <= 65536)),
    CONSTRAINT idempotency_records_check CHECK ((kind = 'subscribe' AND managed_anime_id IS NOT NULL AND result_snapshot IS NOT NULL AND release_id IS NULL AND plugin_target_config_id IS NULL AND owner_type IS NULL AND owner_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL) OR (kind = 'release_retry' AND release_id IS NOT NULL AND result_snapshot IS NOT NULL AND managed_anime_id IS NULL AND plugin_target_config_id IS NULL AND owner_type IS NULL AND owner_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL) OR (kind = 'manual_retry' AND owner_type IS NOT NULL AND owner_id IS NOT NULL AND failed_work_item_id IS NOT NULL AND result_work_item_id IS NOT NULL AND result_snapshot IS NOT NULL AND managed_anime_id IS NULL AND release_id IS NULL AND plugin_target_config_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL) OR (kind = 'plugin_test' AND plugin_target_config_id IS NOT NULL AND expected_version IS NOT NULL AND trigger_kind IS NOT NULL AND canonical_request_body IS NOT NULL AND delivery_id IS NOT NULL AND result_snapshot IS NULL AND managed_anime_id IS NULL AND release_id IS NULL AND owner_type IS NULL AND owner_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL) OR (kind = 'metadata_match' AND managed_anime_id IS NOT NULL AND result_snapshot IS NOT NULL AND release_id IS NULL AND plugin_target_config_id IS NULL AND owner_type IS NULL AND owner_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL) OR (kind = 'local_cleanup' AND owner_type = 'managed_anime' AND owner_id IS NOT NULL AND result_snapshot IS NOT NULL AND managed_anime_id IS NULL AND release_id IS NULL AND plugin_target_config_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL) OR (kind = 'business_apply' AND managed_anime_id IS NOT NULL AND result_snapshot IS NOT NULL AND release_id IS NULL AND plugin_target_config_id IS NULL AND owner_type IS NULL AND owner_id IS NULL AND failed_work_item_id IS NULL AND result_work_item_id IS NULL AND expected_version IS NULL AND trigger_kind IS NULL AND canonical_request_body IS NULL AND delivery_id IS NULL)),
    CONSTRAINT idempotency_records_delivery_id_check CHECK (delivery_id IS NULL OR (char_length(btrim(delivery_id)) BETWEEN 1 AND 128 AND delivery_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')),
    CONSTRAINT idempotency_records_expected_version_check CHECK (expected_version IS NULL OR expected_version >= 1),
    CONSTRAINT idempotency_records_failed_work_item_id_check CHECK (failed_work_item_id IS NULL OR char_length(btrim(failed_work_item_id)) BETWEEN 1 AND 128),
    CONSTRAINT idempotency_records_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 256),
    CONSTRAINT idempotency_records_kind_check CHECK (kind IN ('subscribe', 'release_retry', 'manual_retry', 'plugin_test', 'metadata_match', 'local_cleanup', 'business_apply')),
    CONSTRAINT idempotency_records_owner_id_check CHECK (owner_id IS NULL OR char_length(btrim(owner_id)) BETWEEN 1 AND 128),
    CONSTRAINT idempotency_records_owner_type_check CHECK (owner_type IS NULL OR owner_type IN ('rss_source_runtime', 'download_task', 'artifact', 'file_operation', 'managed_anime')),
    CONSTRAINT idempotency_records_request_hash_check CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT idempotency_records_result_snapshot_check CHECK (result_snapshot IS NULL OR (jsonb_typeof(result_snapshot) = 'object' AND pg_column_size(result_snapshot) <= 1048576)),
    CONSTRAINT idempotency_records_result_work_item_id_check CHECK (result_work_item_id IS NULL OR char_length(btrim(result_work_item_id)) BETWEEN 1 AND 128),
    CONSTRAINT idempotency_records_trigger_kind_check CHECK (trigger_kind IS NULL OR trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported'))
);

CREATE UNIQUE INDEX idempotency_records_identity_uq ON idempotency_records (kind, release_id, plugin_target_config_id, owner_type, owner_id, idempotency_key) NULLS NOT DISTINCT;
CREATE UNIQUE INDEX idempotency_records_subscribe_anime_uq ON idempotency_records (managed_anime_id)
    WHERE kind = 'subscribe' AND managed_anime_id IS NOT NULL;

COMMENT ON COLUMN idempotency_records.idempotency_key IS 'HTTP action dedupe key; never the owner external-effect idempotency key.';
COMMENT ON COLUMN idempotency_records.failed_work_item_id IS 'Intentionally not an FK so expired terminal WorkItems remain purgeable without deleting request dedupe evidence.';
COMMENT ON COLUMN idempotency_records.delivery_id IS 'Opaque Delivery reference retained without an FK so expired evidence can be purged independently.';

CREATE TABLE one_shot_acquisitions (
    id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    rss_source_id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    download_task_id TEXT,
    correlation_id TEXT NOT NULL,
    request_idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_snapshot JSONB NOT NULL,
    state TEXT NOT NULL,
    terminal_outcome TEXT,
    terminal_reason_code TEXT,
    terminal_reason TEXT,
    terminal_at TIMESTAMPTZ,
    retention_until TIMESTAMPTZ,
    admission_epoch BIGINT NOT NULL DEFAULT 1,
    active_effect_count INTEGER NOT NULL DEFAULT 0,
    active_effect_leases JSONB NOT NULL DEFAULT '[]'::jsonb,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    input_snapshot JSONB,
    current_attempt_id TEXT,
    CONSTRAINT one_shot_acquisitions_pkey PRIMARY KEY (id),
    CONSTRAINT one_shot_acquisitions_active_effect_leases_check CHECK (jsonb_typeof(active_effect_leases) = 'array' AND pg_column_size(active_effect_leases) <= 65536),
    CONSTRAINT one_shot_acquisitions_admission_epoch_check CHECK (admission_epoch >= 1),
    CONSTRAINT one_shot_acquisitions_adopted_terminal_check CHECK (download_task_id IS NOT NULL OR state <> 'running'),
    CONSTRAINT one_shot_acquisitions_correlation_id_check CHECK (char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_current_attempt_id_check CHECK (current_attempt_id IS NULL OR char_length(btrim(current_attempt_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_download_task_id_check CHECK (char_length(btrim(download_task_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_effect_count_check CHECK (active_effect_count >= 0 AND active_effect_count = jsonb_array_length(active_effect_leases)),
    CONSTRAINT one_shot_acquisitions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_input_snapshot_check CHECK (input_snapshot IS NULL OR (jsonb_typeof(input_snapshot) = 'object' AND pg_column_size(input_snapshot) <= 262144)),
    CONSTRAINT one_shot_acquisitions_leases_drained_check CHECK (jsonb_array_length(active_effect_leases) = 0 OR state IN ('running', 'cleanup_blocked')),
    CONSTRAINT one_shot_acquisitions_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_release_id_check CHECK (char_length(btrim(release_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_request_hash_check CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT one_shot_acquisitions_request_idempotency_key_check CHECK (char_length(btrim(request_idempotency_key)) BETWEEN 8 AND 128),
    CONSTRAINT one_shot_acquisitions_result_snapshot_check CHECK (jsonb_typeof(result_snapshot) = 'object' AND pg_column_size(result_snapshot) <= 65536),
    CONSTRAINT one_shot_acquisitions_rss_source_id_check CHECK (char_length(btrim(rss_source_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_state_check CHECK (state IN ('running', 'retention_pending', 'cleanup_blocked', 'reclaiming', 'cleaned')),
    CONSTRAINT one_shot_acquisitions_subscription_id_check CHECK (char_length(btrim(subscription_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_terminal_field_matrix_check CHECK ((state = 'running' AND terminal_outcome IS NULL AND terminal_reason_code IS NULL AND terminal_reason IS NULL AND terminal_at IS NULL AND retention_until IS NULL) OR (state <> 'running' AND terminal_outcome IS NOT NULL AND terminal_reason_code IS NOT NULL AND terminal_reason IS NOT NULL AND terminal_at IS NOT NULL AND retention_until IS NOT NULL)),
    CONSTRAINT one_shot_acquisitions_terminal_outcome_check CHECK (terminal_outcome IN ('succeeded', 'failed')),
    CONSTRAINT one_shot_acquisitions_terminal_reason_check CHECK (terminal_reason IS NULL OR char_length(btrim(terminal_reason)) BETWEEN 1 AND 1024),
    CONSTRAINT one_shot_acquisitions_terminal_reason_code_check CHECK (terminal_reason_code IS NULL OR char_length(btrim(terminal_reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_acquisitions_version_check CHECK (version >= 1)
);

CREATE UNIQUE INDEX one_shot_acquisitions_managed_anime_uq ON one_shot_acquisitions (managed_anime_id);
CREATE UNIQUE INDEX one_shot_acquisitions_request_key_uq ON one_shot_acquisitions (request_idempotency_key);
CREATE INDEX one_shot_acquisitions_retention_idx ON one_shot_acquisitions (retention_until, id);

COMMENT ON TABLE one_shot_acquisitions IS 'Internal finite one-shot acquisition marker owning request replay and bounded reclamation; not a generic lifecycle/event/process table.';
COMMENT ON COLUMN one_shot_acquisitions.managed_anime_id IS 'Opaque temporary-mount anchor without cascading FK; revalidated under the marker lock before any deletion.';
COMMENT ON COLUMN one_shot_acquisitions.download_task_id IS 'Anchored DownloadTask for the ordinary one-shot download chain; NULL when the exact target set already existed and the acquisition adopted it with no qB work.';
COMMENT ON COLUMN one_shot_acquisitions.request_idempotency_key IS 'Marker-owned HTTP replay identity; request_hash is compared only after this row is locked.';
COMMENT ON COLUMN one_shot_acquisitions.active_effect_leases IS 'Bounded effect-admission ledger; each entry is an object with admission_id, work_item_id, owner_type, owner_id, admission_epoch, admitted_at. Never raw payloads; shape is enforced by the application layer.';
COMMENT ON COLUMN one_shot_acquisitions.input_snapshot IS 'Complete plaintext {draft, selected_seed} admission input for UI readback; NULL is allowed only for legacy rows; maximum 262144 bytes.';
COMMENT ON COLUMN one_shot_acquisitions.current_attempt_id IS 'Current execution attempt whose terminal evidence the marker projection mirrors; set at admission, advanced only by an explicit manual recovery under the marker lock.';

CREATE TABLE one_shot_execution_attempts (
    id TEXT NOT NULL,
    acquisition_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    previous_attempt_id TEXT,
    mode TEXT NOT NULL,
    resume_stage TEXT,
    input_snapshot JSONB NOT NULL,
    input_digest TEXT NOT NULL,
    state TEXT NOT NULL,
    failed_stage TEXT,
    outcome_code TEXT,
    outcome_reason TEXT,
    terminal_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    request_idempotency_key TEXT,
    request_hash TEXT,
    CONSTRAINT one_shot_execution_attempts_pkey PRIMARY KEY (id),
    CONSTRAINT one_shot_attempts_lineage_check CHECK ((mode = 'initial' AND previous_attempt_id IS NULL AND resume_stage IS NULL AND sequence = 1) OR (mode IN ('retry', 'replan') AND previous_attempt_id IS NOT NULL AND sequence > 1)),
    CONSTRAINT one_shot_attempts_request_identity_check CHECK ((request_idempotency_key IS NULL) = (request_hash IS NULL)),
    CONSTRAINT one_shot_attempts_terminal_matrix_check CHECK ((state = 'running' AND outcome_code IS NULL AND outcome_reason IS NULL AND terminal_at IS NULL) OR (state <> 'running' AND outcome_code IS NOT NULL AND outcome_reason IS NOT NULL AND terminal_at IS NOT NULL)),
    CONSTRAINT one_shot_execution_attempts_acquisition_id_check CHECK (char_length(btrim(acquisition_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_execution_attempts_failed_stage_check CHECK (failed_stage IS NULL OR failed_stage IN ('download', 'artifact', 'planning', 'file_execution', 'placement')),
    CONSTRAINT one_shot_execution_attempts_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_execution_attempts_input_digest_check CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT one_shot_execution_attempts_input_snapshot_check CHECK (jsonb_typeof(input_snapshot) = 'object' AND pg_column_size(input_snapshot) <= 262144),
    CONSTRAINT one_shot_execution_attempts_mode_check CHECK (mode IN ('initial', 'retry', 'replan')),
    CONSTRAINT one_shot_execution_attempts_outcome_code_check CHECK (outcome_code IS NULL OR char_length(btrim(outcome_code)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_execution_attempts_outcome_reason_check CHECK (outcome_reason IS NULL OR char_length(btrim(outcome_reason)) <= 1024),
    CONSTRAINT one_shot_execution_attempts_previous_attempt_id_check CHECK (char_length(btrim(previous_attempt_id)) BETWEEN 1 AND 128),
    CONSTRAINT one_shot_execution_attempts_request_hash_check CHECK (request_hash IS NULL OR request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT one_shot_execution_attempts_request_idempotency_key_check CHECK (request_idempotency_key IS NULL OR char_length(btrim(request_idempotency_key)) BETWEEN 1 AND 256),
    CONSTRAINT one_shot_execution_attempts_resume_stage_check CHECK (resume_stage IS NULL OR resume_stage IN ('download', 'artifact', 'planning', 'file_execution', 'placement')),
    CONSTRAINT one_shot_execution_attempts_sequence_check CHECK (sequence >= 1),
    CONSTRAINT one_shot_execution_attempts_state_check CHECK (state IN ('running', 'succeeded', 'failed')),
    CONSTRAINT one_shot_execution_attempts_version_check CHECK (version >= 1)
);

CREATE INDEX one_shot_attempts_acquisition_idx ON one_shot_execution_attempts (acquisition_id, state);
CREATE UNIQUE INDEX one_shot_attempts_acquisition_seq_uq ON one_shot_execution_attempts (acquisition_id, sequence);
CREATE UNIQUE INDEX one_shot_attempts_request_key_uq ON one_shot_execution_attempts (acquisition_id, request_idempotency_key)
    WHERE request_idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX one_shot_attempts_running_uq ON one_shot_execution_attempts (acquisition_id)
    WHERE state = 'running';

COMMENT ON TABLE one_shot_execution_attempts IS 'Immutable per-attempt terminal evidence for bounded one-shot executions; the marker keeps resource ownership and effect gating. Attempts are retained until marker purge.';
COMMENT ON COLUMN one_shot_execution_attempts.request_idempotency_key IS 'Manual recovery request key; the attempt it created is the replay result.';

-- PRUNE-046 S6: `download_submission_evidence` is gone. Its columns now live on
-- `download_tasks` (submit_*), one current slot per task;
-- `download_submission_lookup_tickets` is gone with it (submit_lookup_count +
-- submit_lookup_ticket hold the current ticket only).


CREATE TABLE download_completions (
    id TEXT NOT NULL,
    acquisition_id TEXT NOT NULL,
    action TEXT NOT NULL,
    info_hash TEXT,
    provider_ref TEXT,
    remote_root TEXT,
    application_root TEXT,
    state TEXT NOT NULL,
    result_code TEXT,
    reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    CONSTRAINT download_completions_pkey PRIMARY KEY (id),
    CONSTRAINT download_completions_acquisition_id_key UNIQUE (acquisition_id),
    CONSTRAINT download_completions_acquisition_id_fkey FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT download_completions_action_check CHECK (action IN ('seed', 'pause', 'remove', 'remove_with_data')),
    CONSTRAINT download_completions_application_root_check CHECK (application_root IS NULL OR char_length(btrim(application_root)) BETWEEN 1 AND 4096),
    CONSTRAINT download_completions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT download_completions_info_hash_check CHECK (info_hash IS NULL OR info_hash ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    CONSTRAINT download_completions_provider_ref_check CHECK (provider_ref IS NULL OR char_length(btrim(provider_ref)) BETWEEN 1 AND 512),
    CONSTRAINT download_completions_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT download_completions_remote_root_check CHECK (remote_root IS NULL OR char_length(btrim(remote_root)) BETWEEN 1 AND 4096),
    CONSTRAINT download_completions_result_code_check CHECK (result_code IS NULL OR char_length(btrim(result_code)) BETWEEN 1 AND 128),
    CONSTRAINT download_completions_seed_check CHECK (action <> 'seed' OR state = 'succeeded'),
    CONSTRAINT download_completions_state_check CHECK (state IN ('pending', 'succeeded', 'failed')),
    CONSTRAINT download_completions_terminal_check CHECK ((state = 'pending') = (result_code IS NULL) AND (state = 'pending') = (completed_at IS NULL)),
    CONSTRAINT download_completions_version_check CHECK (version >= 1)
);

CREATE FUNCTION acquisition_consume_settle_work(p_root text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE affected integer:=0; v_id text;
BEGIN
 FOR v_id IN UPDATE public.work_items SET updated_at=CURRENT_TIMESTAMP
  WHERE owner_type='acquisition' AND owner_id=p_root AND kind='acquisition_settle' RETURNING id LOOP
  affected:=affected+public.work_item_close(v_id,'succeeded');
 END LOOP;
 RETURN affected;
END $$;

CREATE FUNCTION acquisition_owner(p_root text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT COALESCE(
  (SELECT sub.managed_anime_id FROM public.download_tasks t JOIN public.releases rel ON rel.id=t.release_id
     JOIN public.rss_sources src ON src.id=rel.rss_source_id JOIN public.subscriptions sub ON sub.id=src.subscription_id
   WHERE t.acquisition_id=p_root LIMIT 1),
  (SELECT r.origin_managed_anime_ref FROM public.acquisitions r WHERE r.id=p_root))
$$;

CREATE FUNCTION download_completions_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF OLD.state = 'succeeded' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_TERMINAL';
    END IF;
    IF (NEW.id, NEW.acquisition_id, NEW.action, NEW.info_hash, NEW.provider_ref, NEW.remote_root,
        NEW.application_root, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.acquisition_id, OLD.action, OLD.info_hash, OLD.provider_ref, OLD.remote_root,
        OLD.application_root, OLD.created_at) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_FROZEN';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_VERSION';
    END IF;
    IF OLD.state = 'failed' AND NEW.state <> 'pending' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'DOWNLOAD_COMPLETION_TERMINAL';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION download_ensure_settlement(p_root text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE root public.acquisitions%ROWTYPE;
BEGIN
    SELECT * INTO STRICT root FROM public.acquisitions WHERE id=p_root;
    IF root.state IS DISTINCT FROM 'running' OR root.record_state IS DISTINCT FROM 'full' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_SETTLEMENT_ROOT';
    END IF;
    UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=p_root;
    UPDATE public.work_items SET available_at=LEAST(available_at,clock_timestamp()),
        expected_owner_version=root.version,updated_at=clock_timestamp()
        WHERE acquisition_id=p_root AND owner_type='acquisition' AND kind='acquisition_settle'
        AND state='available';
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE acquisition_id=p_root
        AND owner_type='acquisition' AND kind='acquisition_settle' AND state IN ('available','leased')) THEN
        INSERT INTO public.work_items(id,owner_type,owner_id,acquisition_id,kind,idempotency_key,
            max_attempts,input_snapshot,expected_owner_version)
        VALUES(gen_random_uuid()::text,'acquisition',p_root,p_root,'acquisition_settle',
            'acquisition_settle:'||p_root||':'||(root.facts_revision+1)::text,5,
            '{"schema_version":1}'::jsonb,root.version);
    END IF;
END;
$$;

CREATE FUNCTION notification_acquisition_title(p_root text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT public.file_notification_text(COALESCE(
  (SELECT a.display_title->>'value' FROM public.acquisitions a
    WHERE a.id=p_root AND a.display_title->>'state'='known' AND jsonb_typeof(a.display_title->'value')='string'),
  (SELECT COALESCE(NULLIF(m.title_override,''),m.title) FROM public.managed_anime m
    WHERE m.id=public.acquisition_owner(p_root))))
$$;

CREATE FUNCTION download_failed_notification(p_task text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE t public.download_tasks; input jsonb; source_facts jsonb; src_kind text; retryable boolean;
BEGIN
 SELECT * INTO STRICT t FROM public.download_tasks WHERE id=p_task;
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=t.acquisition_id;
 SELECT rel.source_facts, s.source_kind INTO source_facts, src_kind
   FROM public.releases rel JOIN public.rss_sources s ON s.id=rel.rss_source_id WHERE rel.id=t.release_id;
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
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(t.acquisition_id)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(t.acquisition_id),
   'src.kind',public.file_notification_text(src_kind),
   'result.outcome','failure',
   'result.category',public.download_failure_category(t.failure_code),
   'result.retryable',retryable);
END $$;

CREATE FUNCTION download_recovery_lock(p_task text, p_work text, p_token bigint) RETURNS download_tasks
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; root public.acquisitions%ROWTYPE;
    work public.work_item_history%ROWTYPE; root_id text; db_now timestamptz;
BEGIN
    SELECT acquisition_id INTO STRICT root_id FROM public.download_tasks WHERE id=p_task;
    SELECT * INTO STRICT root FROM public.acquisitions WHERE id=root_id FOR UPDATE;
    SELECT * INTO STRICT task FROM public.download_tasks WHERE id=p_task FOR UPDATE;
    PERFORM id FROM public.work_items
        WHERE id=p_work OR (acquisition_id=root_id AND state IN ('available','leased'))
        ORDER BY id COLLATE "C" FOR UPDATE;
    SELECT * INTO STRICT work FROM public.work_item_history WHERE id=p_work;
    db_now:=clock_timestamp();
    IF task.acquisition_id IS DISTINCT FROM root.id OR root.state IS DISTINCT FROM 'running'
        OR root.record_state IS DISTINCT FROM 'full' OR work.acquisition_id IS DISTINCT FROM root.id
        OR work.owner_type IS DISTINCT FROM 'download_task' OR work.owner_id IS DISTINCT FROM task.id
        OR (work.kind IN ('qb_submit','qb_sync')) IS NOT TRUE
        OR work.state IS DISTINCT FROM 'leased' OR work.fencing_token IS DISTINCT FROM p_token
        OR work.expected_owner_version IS DISTINCT FROM task.version OR (work.lease_until>db_now) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_STAGE_STALE_CLAIM';
    END IF;
    RETURN task;
END;
$$;

-- The one terminal transition of a WorkItem: the live row is deleted from the
-- queue and recorded as an outcome in the same transaction. Every caller first
-- UPDATEs the live row under its own predicate, so the UPDATE retirement fence
-- has checked the active lifecycle and current epoch before the row leaves.
-- Returns 1, or 0 when no live row has the id.
CREATE FUNCTION work_item_close(p_id text, p_state text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.work_items%ROWTYPE;
BEGIN
 IF p_state IS NULL OR p_state NOT IN ('succeeded','failed') THEN
  RAISE EXCEPTION USING ERRCODE='22023',MESSAGE='WORK_ITEM_OUTCOME_STATE';
 END IF;
 DELETE FROM public.work_items WHERE id=p_id RETURNING * INTO r;
 IF NOT FOUND THEN RETURN 0; END IF;
 INSERT INTO public.work_item_outcomes(id,owner_type,owner_id,kind,state,available_at,fencing_token,
  idempotency_key,attempt,max_attempts,input_snapshot,expected_owner_version,last_error_code,
  last_error_reason,retention_days,created_at,updated_at,cancel_requested,managed_anime_id,
  owner_lifecycle_epoch,external_effect_admitted_at,recovery_blocked,recovery_reason,acquisition_id,
  terminal_at,retention_until)
 VALUES(r.id,r.owner_type,r.owner_id,r.kind,p_state,r.available_at,r.fencing_token,
  r.idempotency_key,r.attempt,r.max_attempts,r.input_snapshot,r.expected_owner_version,r.last_error_code,
  r.last_error_reason,r.retention_days,r.created_at,r.updated_at,r.cancel_requested,r.managed_anime_id,
  r.owner_lifecycle_epoch,r.external_effect_admitted_at,r.recovery_blocked,r.recovery_reason,r.acquisition_id,
  CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+(r.retention_days*INTERVAL '1 day'));
 RETURN 1;
END $$;

CREATE FUNCTION work_item_finish(text, bigint, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items SET expected_owner_version = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($3, $1);
END;
$_$;

CREATE FUNCTION work_item_finish_recovery_observation(text, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items SET expected_owner_version=$3,last_error_code=NULL,last_error_reason=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=$1;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($1, $2);
END;
$_$;

CREATE FUNCTION download_finish_submission(p_task text, p_work text, p_token bigint, p_session text, p_permit text, p_state text, p_correlation jsonb, p_connection text, p_sync_id text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; evidence public.download_tasks%ROWTYPE;
    ticket jsonb; now_at timestamptz; next_version bigint; next_work text; verdict text; first_confirmation boolean;
    direct_completion boolean; recovery_completion boolean; evidence_adoption boolean;
    artifact text; artifact_version bigint; inherited jsonb; inventory jsonb; fingerprints jsonb;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    -- A committed terminal work can only be read as an idempotent receipt.
    IF EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=p_work AND fencing_token=p_token
        AND owner_type='download_task' AND owner_id=p_task AND kind='qb_submit'
        AND (state='succeeded' OR (state='failed' AND EXISTS(
            SELECT 1 FROM public.download_tasks t WHERE t.id=p_task AND t.state='failed'))
        OR (state='failed' AND cancel_requested AND recovery_blocked AND recovery_reason='cancel_requested'
            AND EXISTS(SELECT 1 FROM public.download_tasks t
                WHERE t.id=p_task AND t.state='queued' AND t.submit_permit_id=p_permit
                    AND t.submit_effect_class='confirmed' AND t.submit_dispatch_state='closed' AND t.submit_resolution_proof IS NOT NULL)))) THEN
        SELECT * INTO evidence FROM public.download_tasks
            WHERE submit_permit_id=p_permit AND id=p_task;
        IF evidence.submit_adopted_settlement_receipt IS NOT NULL THEN
            IF evidence.submit_adopted_settlement_receipt->>'work_item_id' IS DISTINCT FROM p_work
                OR (evidence.submit_adopted_settlement_receipt->>'token')::bigint IS DISTINCT FROM p_token THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RECEIPT_IDENTITY';
            END IF;
            IF evidence.submit_adopted_settlement_receipt->>'executor_session' IS DISTINCT FROM p_session THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RECEIPT_SESSION';
            END IF;
            IF evidence.submit_adopted_settlement_receipt->'correlation' IS DISTINCT FROM p_correlation
                OR evidence.submit_adopted_settlement_receipt->>'state' IS DISTINCT FROM p_state THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RECEIPT_FACTS';
            END IF;
            RETURN jsonb_build_object('applied',false,'reason','SUBMIT_ALREADY_FINISHED');
        END IF;
        IF NOT FOUND OR (
            (evidence.submit_work_item_id=p_work AND evidence.submit_fencing_token=p_token)
            OR (evidence.submit_lookup_ticket->>'work_item_id'=p_work
                AND (evidence.submit_lookup_ticket->>'token')::bigint=p_token
                AND evidence.submit_lookup_ticket->>'state' IN ('issued','completed'))) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RECEIPT_IDENTITY';
        END IF;
        IF ((evidence.submit_work_item_id=p_work AND evidence.submit_fencing_token=p_token
                AND evidence.submit_executor_session=p_session)
            OR (evidence.submit_lookup_ticket->>'work_item_id'=p_work
                AND (evidence.submit_lookup_ticket->>'token')::bigint=p_token
                AND evidence.submit_lookup_ticket->>'executor_session'=p_session
                AND evidence.submit_lookup_ticket->>'state' IN ('issued','completed'))) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RECEIPT_SESSION';
        END IF;
        RETURN jsonb_build_object('applied',false,'reason','SUBMIT_ALREADY_FINISHED');
    END IF;
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions
            WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_recovery_lock(p_task,p_work,p_token);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_submit') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
    END IF;
    SELECT * INTO STRICT evidence FROM public.download_tasks
        WHERE submit_permit_id=p_permit AND id=p_task FOR UPDATE;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    ticket := task.submit_attempt_ticket;
    direct_completion := (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
        AND ticket->>'executor_session'=p_session AND ticket->>'state'='issued'
        AND evidence.submit_work_item_id=p_work AND evidence.submit_fencing_token=p_token) IS TRUE;
    -- A current recovery claim may consume strong passive confirmation from
    -- an earlier dispatch. Its own lookup grant supplies the session identity;
    -- the original mutation identity must still match the retained attempt.
    recovery_completion := (evidence.submit_lookup_ticket IS NOT NULL
        AND evidence.submit_lookup_ticket->>'work_item_id'=p_work
        AND (evidence.submit_lookup_ticket->>'token')::bigint=p_token
        AND evidence.submit_lookup_ticket->>'executor_session'=p_session
        AND evidence.submit_lookup_ticket->>'state' IN ('issued','completed'))
        AND (ticket->>'work_item_id'=evidence.submit_work_item_id
            AND (ticket->>'token')::bigint=evidence.submit_fencing_token
            AND ticket->>'executor_session'=evidence.submit_executor_session
            AND ticket->>'state'='issued') IS TRUE;
    -- A new recovery token adopts an already-recorded found receipt. It does
    -- not acquire lookup or mutation rights and cannot replace provider facts.
    evidence_adoption := evidence.submit_effect_class='confirmed' AND evidence.submit_dispatch_state='closed'
        AND evidence.submit_closed_proof IS NOT NULL AND evidence.submit_resolution_proof IS NOT NULL
        AND evidence.submit_conflict_evidence IS NULL
        AND evidence.submit_observation->'correlation'=p_correlation
        AND p_state=p_correlation->>'state'
        AND EXISTS(SELECT 1 FROM public.work_items w WHERE w.id=p_work
            AND w.recovery_blocked AND w.recovery_reason='effect_unknown')
        AND evidence.submit_lookup_ticket IS NOT NULL
        AND evidence.submit_lookup_ticket->>'work_item_id'=p_work
        AND (evidence.submit_lookup_ticket->>'token')::bigint<p_token
        AND evidence.submit_lookup_ticket->>'executor_session'=p_session
        AND evidence.submit_lookup_ticket->>'state'='completed' AND evidence.submit_lookup_ticket->>'result_kind'='found'
        AND evidence.submit_lookup_ticket->>'result_digest'='sha256:' || encode(public.digest(convert_to(jsonb_build_object(
                    'effect','confirmed','closed',evidence.submit_closed_proof,'resolution',evidence.submit_resolution_proof,
                    'observation',evidence.submit_observation)::text,'UTF8'),'sha256'),'hex')
        AND (ticket->>'work_item_id'=evidence.submit_work_item_id
            AND (ticket->>'token')::bigint=evidence.submit_fencing_token
            AND ticket->>'executor_session'=evidence.submit_executor_session
            AND ticket->>'state'='issued') IS TRUE;
    IF evidence_adoption IS TRUE THEN
        UPDATE public.download_tasks SET submit_adopted_settlement_receipt=jsonb_build_object(
            'work_item_id',p_work,'token',p_token,'executor_session',p_session,
            'correlation',p_correlation,'state',p_state) WHERE id=p_task;
    END IF;
    IF task.state IN ('completed','failed') THEN
        IF (direct_completion OR recovery_completion OR evidence_adoption) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
        END IF;
        UPDATE public.download_tasks SET
            submit_attempt_ticket=jsonb_set(ticket,'{state}','"superseded"'),
            submit_attempt_index=jsonb_set(submit_attempt_index,
                ARRAY[((ticket->>'ordinal')::integer-1)::text,'state'],'"superseded"') WHERE id=p_task;
        PERFORM public.work_item_finish_recovery_observation(p_work,
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version);
        RETURN jsonb_build_object('applied',false,'reason','SUBMIT_TASK_TERMINAL','state',task.state);
    END IF;
    direct_completion := direct_completion AND (now_at<(ticket->>'attempt_until')::timestamptz) IS TRUE;
    recovery_completion := recovery_completion AND (evidence.submit_lookup_ticket IS NOT NULL
        AND evidence.submit_lookup_ticket->>'work_item_id'=p_work
        AND (evidence.submit_lookup_ticket->>'token')::bigint=p_token
        AND evidence.submit_lookup_ticket->>'executor_session'=p_session
        AND evidence.submit_lookup_ticket->>'state' IN ('issued','completed')
        AND now_at<(evidence.submit_lookup_ticket->>'attempt_until')::timestamptz);
    recovery_completion := recovery_completion OR evidence_adoption;
    IF EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND cancel_requested) THEN
        IF recovery_completion IS NOT TRUE OR evidence.submit_effect_class IS DISTINCT FROM 'confirmed'
            OR evidence.submit_dispatch_state IS DISTINCT FROM 'closed' OR evidence.submit_resolution_proof IS NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
        END IF;
        PERFORM public.work_item_finish_recovery_observation(p_work,'failed',task.version);
        RETURN jsonb_build_object('applied',true,'state','failed','reason','cancel_requested','task_version',task.version);
    END IF;
    IF evidence.submit_effect_class='unknown' THEN
        IF (direct_completion OR recovery_completion) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
        END IF;
        -- Leave the task and mutation ticket unchanged; only read-only
        -- reconciliation may resolve this permit after an ambiguous dispatch.
        RETURN jsonb_build_object('applied',false,'reason','SUBMIT_RECONCILE_REQUIRED','permit_id',p_permit);
    END IF;
    IF evidence.submit_effect_class='no_external_effect' THEN
        IF direct_completion IS NOT TRUE OR evidence.submit_dispatch_state IS DISTINCT FROM 'closed'
            OR evidence.submit_retry_safe IS DISTINCT FROM true OR evidence.submit_resolution_proof IS NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
        END IF;
        UPDATE public.download_tasks SET
            state=CASE WHEN now_at>=submit_wall_until OR now_at>=wait_until
                OR submit_attempts_started>=submit_max_attempts THEN 'failed' ELSE 'waiting_retry' END,
            failure_code=CASE WHEN now_at>=submit_wall_until OR now_at>=wait_until
                OR submit_attempts_started>=submit_max_attempts THEN 'SUBMIT_BUDGET_EXHAUSTED' ELSE 'qb_submit_no_external_effect' END,
            failure_reason='qB submission was confirmed to have no external effect',
            budget_clock_at=now_at,version=version+1,updated_at=now_at,
            submit_attempt_ticket=jsonb_set(ticket,'{state}','"completed"'),
            submit_attempt_index=jsonb_set(submit_attempt_index,ARRAY[((ticket->>'ordinal')::integer-1)::text,'state'],'"completed"')
            WHERE id=p_task RETURNING version,state INTO next_version,verdict;
        IF verdict='failed' THEN
            PERFORM public.work_item_finish('failed',next_version,p_work);
        ELSE
            UPDATE public.work_items SET state='available',lease_until=NULL,
                expected_owner_version=next_version,available_at=now_at+INTERVAL '30 seconds',updated_at=now_at WHERE id=p_work;
        END IF;
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('applied',true,'task_version',next_version,'state',verdict);
    END IF;
    IF (p_state IN ('submitting','downloading','completed','failed')) IS NOT TRUE
        OR (direct_completion OR recovery_completion) IS NOT TRUE
        OR evidence.submit_effect_class IS DISTINCT FROM 'confirmed'
        OR evidence.submit_dispatch_state IS DISTINCT FROM 'closed' OR evidence.submit_resolution_proof IS NULL
        OR (jsonb_typeof(p_correlation)='object' AND char_length(p_correlation->>'external_id')>0) IS NOT TRUE
        OR p_sync_id IS NULL OR char_length(p_sync_id) NOT BETWEEN 1 AND 128 THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_FINISH_IDENTITY';
    END IF;
    IF now_at>=task.wait_until OR now_at>=task.submit_wall_until THEN
        RETURN jsonb_build_object('applied',false,'reason','SUBMIT_BUDGET_EXHAUSTED','state',task.state);
    END IF;
    first_confirmation := task.provider_correlation IS NULL OR task.provider_correlation IN ('null'::jsonb,'{}'::jsonb);
    UPDATE public.download_tasks SET provider_correlation=p_correlation,
        qbittorrent_connection_id=COALESCE(NULLIF(p_connection,''),qbittorrent_connection_id),
        state=p_state,failure_code=CASE WHEN p_state='failed' THEN 'qb_provider_failed' ELSE NULL END,
        failure_reason=CASE WHEN p_state='failed' THEN 'qB provider task failed' ELSE NULL END,version=version+1,
        budget_clock_at=now_at,updated_at=now_at,
        submit_attempt_ticket=jsonb_set(ticket,'{state}','"completed"'),
        submit_attempt_index=jsonb_set(submit_attempt_index,ARRAY[((ticket->>'ordinal')::integer-1)::text,'state'],'"completed"')
        WHERE id=p_task RETURNING version INTO next_version;
    IF p_state='failed' THEN
        PERFORM public.work_item_finish('failed',next_version,p_work);
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('applied',true,'task_version',next_version,
            'state','failed','reason_code','qb_provider_failed','first_confirmation',false);
    END IF;
    SELECT r.verdict INTO STRICT verdict FROM public.releases r
        WHERE id=task.release_id AND download_task_id=p_task FOR UPDATE;
    IF (verdict IN ('submitted','accepted_pending_download','filtered','waiting_metadata')) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_RELEASE_STATE';
    END IF;
    IF verdict IS DISTINCT FROM 'submitted' THEN
        UPDATE public.releases SET verdict='submitted',reason_code='submitted',reason_detail=NULL,
            version=version+1,updated_at=now_at WHERE id=task.release_id;
    END IF;
    PERFORM public.work_item_finish('succeeded',next_version,p_work);
    inherited := CASE WHEN one_shot IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('one_shot',one_shot) END;
    IF p_state='completed' THEN
        inventory := jsonb_build_object('provider_task_id',p_correlation->'external_id',
            'provider_info_hash',COALESCE(p_correlation->'info_hash','""'::jsonb),
            'name',COALESCE(p_correlation->'name','""'::jsonb),
            'progress',COALESCE(p_correlation->'progress','0'::jsonb),
            'state',COALESCE(p_correlation->'state','""'::jsonb),
            'path',COALESCE(p_correlation->'artifact_path','""'::jsonb),
            'file_count',COALESCE(p_correlation->'file_count','0'::jsonb),
            'files_complete',COALESCE(p_correlation->'files_complete','false'::jsonb),
            'files',COALESCE(p_correlation->'files','null'::jsonb));
        fingerprints := jsonb_build_object('provider_task_id',p_correlation->'external_id',
            'provider_info_hash',COALESCE(p_correlation->'info_hash','""'::jsonb),
            'path',COALESCE(p_correlation->'artifact_path','""'::jsonb),
            'files',COALESCE(p_correlation->'files','null'::jsonb));
        SELECT id,version INTO artifact,artifact_version FROM public.artifacts WHERE download_task_id=p_task;
        IF NOT FOUND THEN
            INSERT INTO public.artifacts(id,download_task_id,acquisition_id,inventory_snapshot,
                fingerprint_snapshot,state,correlation_id,causation_type,causation_id)
            VALUES(gen_random_uuid()::text,p_task,task.acquisition_id,inventory,fingerprints,
                'pending',task.correlation_id,'download_task',p_task) RETURNING id,version INTO artifact,artifact_version;
        END IF;
        SELECT public.work_item_create(p_sync_id,'artifact',artifact,'artifact_inspect',
            'artifact_inspect:'||artifact,5,jsonb_build_object('trigger','download_completed')||inherited,
            artifact_version,0::double precision,7::smallint) INTO next_work;
    ELSE
        SELECT public.work_item_create(p_sync_id,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_continuation','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,task.poll_interval_seconds::double precision,7::smallint) INTO next_work;
    END IF;
    IF next_work IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SYNC_CONFLICT';
    END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'task_version',next_version,
        'first_confirmation',first_confirmation,'sync_work_id',next_work);
END;
$$;

CREATE FUNCTION download_stage_lock(p_task text, p_work text, p_token bigint) RETURNS download_tasks
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    task public.download_tasks%ROWTYPE;
    root public.acquisitions%ROWTYPE;
    work public.work_item_history%ROWTYPE;
    root_id text;
    db_now timestamptz;
BEGIN
    SELECT acquisition_id INTO STRICT root_id FROM public.download_tasks WHERE id=p_task;
    SELECT * INTO STRICT root FROM public.acquisitions WHERE id=root_id FOR UPDATE;
    SELECT * INTO STRICT task FROM public.download_tasks WHERE id=p_task FOR UPDATE;
    PERFORM id FROM public.work_items
    WHERE id=p_work OR (acquisition_id=root_id AND state IN ('available','leased'))
    ORDER BY id COLLATE "C" FOR UPDATE;
    SELECT * INTO STRICT work FROM public.work_item_history WHERE id=p_work;
    db_now := clock_timestamp();
    IF task.acquisition_id IS DISTINCT FROM root.id OR root.state IS DISTINCT FROM 'running' OR root.record_state IS DISTINCT FROM 'full'
        OR work.owner_type IS DISTINCT FROM 'download_task' OR work.owner_id IS DISTINCT FROM task.id
        OR (work.kind IN ('qb_submit','qb_sync')) IS NOT TRUE
        OR work.state IS DISTINCT FROM 'leased' OR work.fencing_token IS DISTINCT FROM p_token
        OR work.expected_owner_version IS DISTINCT FROM task.version OR (work.lease_until>db_now) IS NOT TRUE
        OR work.cancel_requested IS DISTINCT FROM false OR work.recovery_blocked IS DISTINCT FROM false THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_STAGE_STALE_CLAIM';
    END IF;
    RETURN task;
END;
$$;

CREATE FUNCTION download_finish_sync(p_task text, p_work text, p_token bigint, p_session text, p_ordinal integer, p_state text, p_correlation jsonb, p_code text, p_reason text, p_observation_error boolean, p_observed_complete_bytes bigint, p_observed_total_bytes bigint, p_observed_provider_state text, p_next_id text, p_artifact_id text, p_inventory jsonb, p_fingerprints jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz;
    next_version bigint; next_work text; final_state text; code text; reason text; terminal_state text;
    inherited jsonb; artifact text; artifact_version bigint;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
    observed_bytes bigint; observed_total bigint; provider_state text;
    next_highwater bigint; next_last_progress timestamptz; stall_expired boolean;
BEGIN
    IF EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=p_work AND fencing_token=p_token
        AND owner_type='download_task' AND owner_id=p_task AND kind='qb_sync') THEN
        SELECT input_snapshot->'sync_ticket' INTO ticket FROM public.work_item_outcomes WHERE id=p_work;
        IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
            AND (ticket->>'ordinal')::integer=p_ordinal AND ticket->>'executor_session'=p_session) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
        END IF;
        RETURN jsonb_build_object('applied',false,'reason','SYNC_ALREADY_FINISHED');
    END IF;
    -- Preserve the existing optional marker-first continuation gate before
    -- entering the root -> task -> work -> evidence lock hierarchy.
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions
            WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING';
        END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_stage_lock(p_task,p_work,p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id=p_work) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    ticket := task.active_poll_ticket; now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_sync')
        OR (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
            AND (ticket->>'ordinal')::integer=p_ordinal AND ticket->>'executor_session'=p_session
            AND ticket->>'state'='issued') IS NOT TRUE
        OR (p_state IN ('submitting','downloading','completed','failed','waiting_retry')) IS NOT TRUE
        OR p_observation_error IS NULL OR p_next_id IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
    END IF;
    -- Typed observation shape is checked after identity/locks and before writes.
    -- The state is already ASCII-normalized by Go; SQL must not normalize it.
    IF (p_observation_error AND (p_observed_complete_bytes IS NOT NULL
            OR p_observed_total_bytes IS NOT NULL OR p_observed_provider_state IS NOT NULL))
        OR (NOT p_observation_error AND (p_observed_provider_state IS NULL
            OR octet_length(p_observed_provider_state)>128
            OR p_observed_provider_state COLLATE "C" !~ '^[a-z0-9]*$'))
        OR p_observed_complete_bytes<0 OR p_observed_total_bytes<0
        OR p_observed_complete_bytes>p_observed_total_bytes THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_OBSERVATION_SHAPE';
    END IF;
    IF task.state IN ('completed','failed') THEN
        UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"')
            WHERE id=p_task;
        PERFORM public.work_item_finish(
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version,p_work);
        RETURN jsonb_build_object('applied',false,'reason','SYNC_TASK_TERMINAL','state',task.state);
    END IF;
    IF (now_at<(ticket->>'attempt_until')::timestamptz AND now_at<task.wait_until) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_FINISH_IDENTITY';
    END IF;
    final_state := p_state; code := p_code; reason := p_reason;
    observed_bytes := p_observed_complete_bytes;
    observed_total := p_observed_total_bytes;
    provider_state := p_observed_provider_state;
    -- The mark only rises (BC 4.1). A first reliable zero is recorded as a mark
    -- but does not restart the clock the submit confirmation already started.
    IF observed_bytes IS NULL THEN
        next_highwater := task.verified_bytes_highwater;
        next_last_progress := task.last_progress_at;
    ELSE
        next_highwater := CASE WHEN task.verified_bytes_highwater IS NULL THEN observed_bytes
            ELSE greatest(task.verified_bytes_highwater,observed_bytes) END;
        IF (task.verified_bytes_highwater IS NULL AND observed_bytes>0)
            OR (task.verified_bytes_highwater IS NOT NULL AND observed_bytes>task.verified_bytes_highwater) THEN
            next_last_progress := now_at;
        ELSE
            next_last_progress := task.last_progress_at;
        END IF;
    END IF;
    -- A row whose clock was never started -- written before the monitoring
    -- window existed, or confirmed before this column -- starts it at this
    -- observation instead of being backdated into an immediate stall.
    IF next_last_progress IS NULL THEN next_last_progress := now_at; END IF;
    stall_expired := task.stall_seconds IS NOT NULL
        AND (now_at-next_last_progress)>=(task.stall_seconds * INTERVAL '1 second');
    IF p_observation_error THEN
        task.consecutive_errors := task.consecutive_errors+1; task.total_errors := task.total_errors+1;
        IF task.consecutive_errors>=task.max_consecutive_errors OR task.total_errors>=task.max_total_errors THEN
            final_state := 'failed'; code := 'SYNC_ERROR_BUDGET_EXHAUSTED';
        ELSE
            final_state := CASE
                WHEN task.state IN ('submitting','downloading') THEN task.state
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state'='downloading' THEN 'downloading'
                WHEN NULLIF(task.provider_correlation->>'external_id','') IS NOT NULL
                    AND task.provider_correlation->>'state' IN ('queued','submitted') THEN 'submitting'
                ELSE task.state END;
        END IF;
    ELSE task.consecutive_errors := 0; END IF;
    IF final_state NOT IN ('completed','failed') AND (now_at>=task.wait_until OR task.polls_started>=task.max_polls) THEN
        final_state := 'failed'; code := 'SYNC_BUDGET_EXHAUSTED';
    END IF;
    -- BC 4.2 / 13.16.12: reached only when the frozen 146 branches above left the
    -- task unsettled, so an S1 budget verdict keeps priority in every race.
    IF final_state NOT IN ('completed','failed') THEN
        IF p_observation_error THEN
            -- No trustworthy current observation. A retained high-water mark, a
            -- cached pause tag or the previous correlation cannot stand in for
            -- one, and being unable to observe is not proof of a stall.
            IF stall_expired THEN
                final_state := 'failed'; code := 'DOWNLOAD_OBSERVATION_UNAVAILABLE';
                reason := format('本轮未能读取到可信的 qBittorrent 状态，且已超过 stall 时限 %s 没有可核实进展。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'));
            END IF;
        ELSIF provider_state IN ('pauseddl','stoppeddl','checkingdl','checkingup','checkingresumedata') THEN
            -- Only this round's judgment is skipped: the clock is untouched, so
            -- the pause is not credited and a resume can stall immediately.
            NULL;
        ELSIF stall_expired THEN
            final_state := 'failed';
            IF next_highwater IS NULL THEN
                code := 'DOWNLOAD_PROGRESS_UNVERIFIABLE';
                reason := format('下载进展已 %s 无法核实：qBittorrent 尚未给出可信的完成字节数，因此不宣称“没有进展”。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'));
            ELSE
                code := 'DOWNLOAD_STALLED';
                reason := format('下载已连续 %s 没有任何进展（已完成字节停留在 %s），已停止自动同步。种子仍保留在 qBittorrent 中，未删除任何文件。',
                    to_char(now_at-next_last_progress,'HH24:MI:SS'),next_highwater);
            END IF;
        END IF;
    END IF;
    SELECT COALESCE(jsonb_build_object('one_shot',input_snapshot->'one_shot'),'{}') INTO inherited
        FROM public.work_items WHERE id=p_work AND input_snapshot ? 'one_shot';
    inherited := COALESCE(inherited,'{}');
    UPDATE public.download_tasks SET state=final_state,
        provider_correlation=COALESCE(p_correlation,provider_correlation),
        failure_code=CASE WHEN final_state IN ('failed','waiting_retry') THEN NULLIF(code,'') ELSE NULL END,
        failure_reason=CASE WHEN final_state IN ('failed','waiting_retry') THEN NULLIF(reason,'') ELSE NULL END,
        consecutive_errors=task.consecutive_errors,total_errors=task.total_errors,
        stall_since=next_last_progress,active_poll_ticket=jsonb_set(ticket,'{state}','"completed"'),
        verified_bytes_highwater=next_highwater,
        payload_total_bytes=COALESCE(observed_total,payload_total_bytes),
        last_progress_at=next_last_progress,
        budget_clock_at=now_at,version=version+1,updated_at=now_at WHERE id=p_task RETURNING version INTO next_version;
    terminal_state := CASE WHEN final_state='failed' THEN 'failed' ELSE 'succeeded' END;
    PERFORM public.work_item_finish(terminal_state,next_version,p_work);
    IF final_state IN ('submitting','downloading','waiting_retry') THEN
        SELECT public.work_item_create(p_next_id,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_progress','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,task.poll_interval_seconds::double precision,7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
    ELSIF final_state='completed' THEN
        IF p_artifact_id IS NULL OR (jsonb_typeof(p_inventory)='object' AND jsonb_typeof(p_fingerprints)='object') IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_ARTIFACT_SHAPE';
        END IF;
        SELECT id,version INTO artifact,artifact_version FROM public.artifacts WHERE download_task_id=p_task;
        IF NOT FOUND THEN
            INSERT INTO public.artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,
                state,correlation_id,causation_type,causation_id)
            VALUES(p_artifact_id,p_task,task.acquisition_id,p_inventory,p_fingerprints,
                'pending',task.correlation_id,'download_task',p_task) RETURNING id,version INTO artifact,artifact_version;
        END IF;
        SELECT public.work_item_create(p_next_id,'artifact',artifact,'artifact_inspect',
            'artifact_inspect:'||artifact,5,jsonb_build_object('trigger','download_completed')||inherited,
            artifact_version,0::double precision,7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_INSPECT_CONFLICT'; END IF;
    END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'task_version',next_version,'state',final_state,
        'reason_code',code,'reason_text',COALESCE(reason,''));
END;
$_$;

CREATE FUNCTION download_finish_unstarted_submission(p_task text, p_work text, p_token bigint, p_session text, p_code text, p_reason text, p_retryable boolean) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz; next_version bigint; final_state text;
BEGIN
    task := public.download_stage_lock(p_task,p_work,p_token);
    ticket := task.submit_attempt_ticket; now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_submit')
        OR (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
            AND ticket->>'executor_session'=p_session AND ticket->>'state'='issued') IS NOT TRUE
        OR p_retryable IS NULL OR NULLIF(p_code,'') IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_UNSTARTED_IDENTITY';
    END IF;
    PERFORM id FROM public.download_tasks WHERE id=p_task FOR UPDATE;
    IF EXISTS(SELECT 1 FROM public.download_tasks WHERE id=p_task AND submit_permit_id IS NOT NULL AND NOT submit_retry_safe) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_ALREADY_DISPATCHED';
    END IF;
    final_state := CASE WHEN p_retryable AND now_at<task.submit_wall_until AND now_at<task.wait_until
        AND task.submit_attempts_started<task.submit_max_attempts THEN 'waiting_retry' ELSE 'failed' END;
    UPDATE public.download_tasks SET state=final_state,failure_code=p_code,failure_reason=p_reason,
        budget_clock_at=now_at,version=version+1,updated_at=now_at,
        submit_attempt_ticket=jsonb_set(ticket,'{state}','"completed"'),
        submit_attempt_index=jsonb_set(submit_attempt_index,ARRAY[((ticket->>'ordinal')::integer-1)::text,'state'],'"completed"')
        WHERE id=p_task RETURNING version INTO next_version;
    IF final_state='failed' THEN PERFORM public.work_item_finish('failed',next_version,p_work);
    ELSE UPDATE public.work_items SET state='available',lease_until=NULL,
        expected_owner_version=next_version,available_at=now_at+INTERVAL '30 seconds',updated_at=now_at WHERE id=p_work; END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'state',final_state,'task_version',next_version,'reason_code',p_code);
END;
$$;

CREATE FUNCTION download_freeze_path_snapshot(p_task text, p_work text, p_token bigint, p_expected_version bigint, p_frozen_input jsonb) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; work public.work_item_history%ROWTYPE;
BEGIN
    task := public.download_stage_lock(p_task,p_work,p_token);
    SELECT * INTO STRICT work FROM public.work_item_history WHERE id=p_work;
    IF work.kind IS DISTINCT FROM 'qb_submit' OR task.version IS DISTINCT FROM p_expected_version THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_STAGE_STALE_CLAIM';
    END IF;
    IF EXISTS(SELECT 1 FROM public.download_tasks t
        WHERE t.id=task.id AND t.submit_dispatch_started_at IS NOT NULL) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_DISPATCH_STARTED';
    END IF;
    IF (jsonb_typeof(p_frozen_input)='object'
        AND jsonb_typeof(p_frozen_input->'path_snapshot')='object'
        AND p_frozen_input->'path_snapshot'<>'{}'::jsonb
        AND (p_frozen_input-'path_snapshot')=(task.frozen_input-'path_snapshot')) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_SNAPSHOT_SHAPE';
    END IF;
    IF task.frozen_input->'path_snapshot' IS NOT NULL
        AND task.frozen_input->'path_snapshot'<>'null'::jsonb THEN
        IF task.frozen_input IS DISTINCT FROM p_frozen_input THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_PATH_SNAPSHOT_CONFLICT';
        END IF;
        RETURN task.version;
    END IF;
    UPDATE public.download_tasks SET frozen_input=p_frozen_input,
        version=version+1,updated_at=clock_timestamp() WHERE id=task.id
        RETURNING version INTO task.version;
    UPDATE public.work_items SET expected_owner_version=task.version,
        updated_at=clock_timestamp() WHERE id=p_work;
    RETURN task.version;
END;
$$;

CREATE FUNCTION download_progress_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        -- The submit confirmation that first records the provider object opens
        -- the monitoring window: both the activation instant and the progress
        -- clock start there, before a single byte is complete. The writer sets
        -- budget_clock_at to the effective DB clock it already committed to, so
        -- the window opens on that clock and not on the wall clock of a later
        -- statement. Rows that never went through a submit confirmation keep
        -- NULL here and start their clock at their first observation instead of
        -- being backdated into an immediate stall.
        IF NEW.progress_activated_at IS NULL
            AND NULLIF(NEW.provider_correlation->>'external_id','') IS NOT NULL
            AND NULLIF(OLD.provider_correlation->>'external_id','') IS NULL THEN
            NEW.progress_activated_at := NEW.budget_clock_at;
            NEW.last_progress_at := COALESCE(NEW.last_progress_at,NEW.budget_clock_at);
        END IF;
        -- The mark only rises. A smaller observed count is not progress, so it
        -- must not pull the mark, or the clock derived from it, back. A first
        -- write from NULL is how the mark comes to exist and is always allowed;
        -- only a known mark can be pulled down.
        IF NEW.verified_bytes_highwater IS DISTINCT FROM OLD.verified_bytes_highwater
            AND OLD.verified_bytes_highwater IS NOT NULL
            AND NEW.verified_bytes_highwater < OLD.verified_bytes_highwater THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_HIGHWATER_REGRESSED';
        END IF;
        IF NEW.last_progress_at IS DISTINCT FROM OLD.last_progress_at
            AND OLD.last_progress_at IS NOT NULL AND NEW.last_progress_at < OLD.last_progress_at THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_PROGRESS_CLOCK_REGRESSED';
        END IF;
        -- The trusted total belongs to the frozen torrent. A task that reports a
        -- different total is provider evidence corruption, not a resize.
        IF NEW.payload_total_bytes IS DISTINCT FROM OLD.payload_total_bytes
            AND OLD.payload_total_bytes IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_TOTAL_SIZE_CHANGED';
        END IF;
        -- progress_activated_at marks the submit confirmation that opened the
        -- monitoring window; it is written once and never rewritten.
        IF NEW.progress_activated_at IS DISTINCT FROM OLD.progress_activated_at
            AND OLD.progress_activated_at IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_PROGRESS_ACTIVATED_IMMUTABLE';
        END IF;
    END IF;
    IF NEW.verified_bytes_highwater IS NOT NULL AND NEW.payload_total_bytes IS NOT NULL
        AND NEW.verified_bytes_highwater > NEW.payload_total_bytes THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_BYTES_ABOVE_TOTAL';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION download_record_submission_receipt(p_root text, p_permit text, p_session text, p_spec text, p_effect text, p_closed jsonb, p_resolution jsonb, p_observation jsonb, p_ticket text, p_work text, p_token bigint, p_lookup_ordinal integer) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    evidence public.download_tasks%ROWTYPE;
    root public.acquisitions%ROWTYPE;
    now_at timestamptz;
    v_result_kind text; v_result_digest text;
    v_current_ticket jsonb;
BEGIN
    SELECT * INTO STRICT root FROM public.acquisitions WHERE id=p_root FOR UPDATE;
    -- Active receipts use the observation gate. Old generations retain only
    -- the passive identity-checked supplementation path.
    IF EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND fencing_token=p_token
        AND state='leased' AND lease_until>clock_timestamp()) THEN
        PERFORM public.download_recovery_lock(
            (SELECT id FROM public.download_tasks WHERE submit_permit_id=p_permit AND acquisition_id=p_root),p_work,p_token);
    ELSE
        PERFORM id FROM public.download_tasks WHERE id=(
            SELECT id FROM public.download_tasks WHERE submit_permit_id=p_permit AND acquisition_id=p_root) FOR UPDATE;
    END IF;
    SELECT * INTO STRICT evidence FROM public.download_tasks
        WHERE submit_permit_id=p_permit AND acquisition_id=p_root FOR UPDATE;
    now_at := clock_timestamp();
    IF evidence.submit_spec_digest IS DISTINCT FROM p_spec OR evidence.submit_dispatch_started_at IS NULL
        OR (p_effect IN ('unknown','confirmed','no_external_effect')) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_IDENTITY';
    END IF;
    IF p_lookup_ordinal IS NULL THEN
        IF p_ticket IS NOT NULL OR (p_work=evidence.submit_work_item_id
            AND p_token=evidence.submit_fencing_token) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_IDENTITY';
        END IF;
        IF evidence.submit_executor_session IS DISTINCT FROM p_session THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_SESSION';
        END IF;
    ELSE
        -- Only the current ticket survives the fold; an older ordinal's row is gone.
        IF evidence.submit_lookup_ticket IS NULL
            OR evidence.submit_lookup_ticket->>'ticket_id' IS DISTINCT FROM p_ticket
            OR (evidence.submit_lookup_ticket->>'ordinal')::integer IS DISTINCT FROM p_lookup_ordinal
            OR evidence.submit_lookup_ticket->>'work_item_id' IS DISTINCT FROM p_work
            OR (evidence.submit_lookup_ticket->>'token')::bigint IS DISTINCT FROM p_token
            OR evidence.submit_lookup_ticket->>'executor_session' IS DISTINCT FROM p_session THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_LOOKUP_TICKET';
        END IF;
        IF p_effect='no_external_effect' THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_LOOKUP_TICKET';
        END IF;
        v_result_kind := CASE WHEN p_effect='confirmed' THEN 'found'
            WHEN p_observation->>'error' IS NOT NULL THEN 'error' ELSE 'not_found' END;
        v_result_digest := 'sha256:' || encode(public.digest(convert_to(jsonb_build_object(
            'effect',p_effect,'closed',p_closed,'resolution',p_resolution,
            'observation',p_observation)::text,'UTF8'),'sha256'),'hex');
        IF evidence.submit_lookup_ticket->>'result_digest' IS NOT NULL THEN
            IF evidence.submit_lookup_ticket->>'result_digest'=v_result_digest THEN
                RETURN jsonb_build_object('recorded',false,'effect_class',evidence.submit_effect_class);
            END IF;
            UPDATE public.download_tasks SET submit_conflict_evidence=jsonb_build_object(
                'kind','SUBMIT_RECEIPT_CONFLICT','ticket_id',p_ticket,'result_digest',v_result_digest)
                WHERE submit_permit_id=p_permit;
            RETURN jsonb_build_object('recorded',false,'reason','SUBMIT_RECEIPT_CONFLICT');
        END IF;
        IF p_effect='confirmed' AND (p_closed IS NULL OR p_resolution IS NULL) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_PROOF_REQUIRED';
        END IF;
        UPDATE public.download_tasks SET submit_lookup_ticket=evidence.submit_lookup_ticket||jsonb_build_object(
            'state',CASE WHEN evidence.submit_lookup_ticket->>'state'='issued' THEN 'completed' ELSE evidence.submit_lookup_ticket->>'state' END,
            'result_kind',v_result_kind,
            'result_digest',v_result_digest,'recorded_at',to_jsonb(now_at))
            WHERE submit_permit_id=p_permit RETURNING submit_lookup_ticket INTO v_current_ticket;
        IF v_current_ticket->>'state'='superseded' AND p_effect IS DISTINCT FROM 'confirmed' THEN
            RETURN jsonb_build_object('recorded',true,'effect_class',evidence.submit_effect_class);
        END IF;
    END IF;
    IF evidence.submit_resolution_proof IS NOT NULL THEN
        IF evidence.submit_effect_class=p_effect AND evidence.submit_resolution_proof IS NOT DISTINCT FROM p_resolution
            AND evidence.submit_closed_proof IS NOT DISTINCT FROM p_closed THEN
            RETURN jsonb_build_object('recorded',false,'effect_class',evidence.submit_effect_class);
        END IF;
        UPDATE public.download_tasks SET submit_conflict_evidence=jsonb_build_object(
            'kind','contradictory_receipt','effect_class',p_effect) WHERE submit_permit_id=p_permit;
    ELSE
        IF p_effect IS DISTINCT FROM 'unknown' AND (p_closed IS NULL OR p_resolution IS NULL) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_RECEIPT_PROOF_REQUIRED';
        END IF;
        UPDATE public.download_tasks SET submit_effect_class=p_effect,
            submit_closed_proof=p_closed,submit_resolution_proof=p_resolution,submit_observation=p_observation,
            submit_dispatch_state=CASE WHEN p_closed IS NULL THEN 'dispatching' ELSE 'closed' END,
            submit_retry_safe=(p_effect='no_external_effect' AND p_closed IS NOT NULL AND p_resolution IS NOT NULL),
            submit_unknown_since=CASE WHEN p_effect='unknown' THEN COALESCE(submit_unknown_since,now_at) ELSE submit_unknown_since END,
            submit_reconcile_until=CASE WHEN p_effect='unknown' THEN COALESCE(submit_reconcile_until,now_at+submit_reconcile_wall_seconds*INTERVAL '1 second') ELSE submit_reconcile_until END,
            updated_at=now_at WHERE submit_permit_id=p_permit;
    END IF;
    IF root.state='running' THEN
        UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=p_root;
    END IF;
    RETURN jsonb_build_object('recorded',true,'effect_class',p_effect);
END;
$$;

CREATE FUNCTION download_recover_sync(p_task text, p_work text, p_token bigint) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE task public.download_tasks%ROWTYPE; ticket jsonb; now_at timestamptz;
    next_version bigint; next_work text; inherited jsonb; exhausted boolean; cancelled boolean;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_recovery_lock(p_task,p_work,p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id=p_work) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    SELECT cancel_requested INTO cancelled FROM public.work_items WHERE id=p_work AND kind='qb_sync';
    IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_RECOVERY_IDENTITY'; END IF;
    ticket := task.active_poll_ticket; now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint<p_token
        AND ticket->>'state'='issued') IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_RECOVERY_IDENTITY';
    END IF;
    IF task.state IN ('completed','failed') OR cancelled THEN
        UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"') WHERE id=p_task;
        PERFORM public.work_item_finish_recovery_observation(p_work,
            CASE WHEN task.state='completed' AND NOT cancelled THEN 'succeeded' ELSE 'failed' END,task.version);
        RETURN jsonb_build_object('applied',true,'reason','SYNC_RECOVERY_CLOSED','state',task.state);
    END IF;
    exhausted := now_at>=task.wait_until OR task.polls_started>=task.max_polls
        OR task.consecutive_errors+1>=task.max_consecutive_errors OR task.total_errors+1>=task.max_total_errors;
    UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(ticket,'{state}','"superseded"'),
        consecutive_errors=consecutive_errors+1,total_errors=total_errors+1,
        budget_clock_at=now_at,version=version+1,updated_at=now_at,
        state=CASE WHEN exhausted THEN 'failed' ELSE state END,
        failure_code=CASE WHEN exhausted THEN 'SYNC_ERROR_BUDGET_EXHAUSTED' ELSE failure_code END,
        failure_reason=CASE WHEN exhausted THEN 'qB observation budget exhausted after interrupted poll' ELSE failure_reason END
        WHERE id=p_task RETURNING version INTO next_version;
    PERFORM public.work_item_finish_recovery_observation(p_work,'failed',next_version);
    IF NOT exhausted THEN
        SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',input_snapshot->'one_shot')
            ELSE '{}'::jsonb END INTO inherited FROM public.work_item_outcomes WHERE id=p_work;
        SELECT public.work_item_create(gen_random_uuid()::text,'download_task',p_task,'qb_sync',task.idempotency_key,5,
            jsonb_build_object('trigger','qb_sync_progress','poll_ordinal',task.next_poll_ordinal)||inherited,
            next_version,greatest(0,extract(epoch FROM
                (ticket->>'started_at')::timestamptz+task.poll_interval_seconds*INTERVAL '1 second'-now_at))::double precision,
            7::smallint) INTO next_work;
        IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
    END IF;
    PERFORM public.download_ensure_settlement(task.acquisition_id);
    RETURN jsonb_build_object('applied',true,'reason','SYNC_INTERRUPTED','task_version',next_version);
END;
$$;

CREATE FUNCTION download_start_dispatch(p_task text, p_work text, p_token bigint, p_session text, p_permit text, p_spec text, p_connection text, p_namespace text, p_locator jsonb, p_path jsonb, p_identity jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    task public.download_tasks%ROWTYPE;
    ticket jsonb;
    evidence public.download_tasks%ROWTYPE;
    now_at timestamptz;
BEGIN
    task := public.download_stage_lock(p_task,p_work,p_token);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_submit') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_ATTEMPT_IDENTITY';
    END IF;
    IF task.state IN ('completed','failed') THEN
        PERFORM public.work_item_finish(
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version,p_work);
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_TASK_TERMINAL','state',task.state);
    END IF;
    ticket := task.submit_attempt_ticket;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF (ticket->>'work_item_id'=p_work AND (ticket->>'token')::bigint=p_token
        AND ticket->>'executor_session'=p_session AND ticket->>'state'='issued'
        AND now_at<(ticket->>'attempt_until')::timestamptz
        AND now_at<task.submit_wall_until AND now_at<task.wait_until) IS NOT TRUE THEN
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_TICKET_EXPIRED');
    END IF;
    PERFORM id FROM public.download_tasks WHERE id=p_task FOR UPDATE;
    SELECT * INTO evidence FROM public.download_tasks
        WHERE id=p_task AND submit_work_item_id=p_work AND submit_fencing_token=p_token;
    IF FOUND THEN
        -- Reading an old dispatch is never permission to repeat it.
        RETURN jsonb_build_object('granted',false,'permit_id',evidence.submit_permit_id);
    END IF;
    IF EXISTS (SELECT 1 FROM public.download_tasks t
        WHERE t.id=p_task AND t.submit_permit_id IS NOT NULL AND NOT t.submit_retry_safe) THEN
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_RECONCILE_REQUIRED');
    END IF;
    -- An admitted overwrite replaces the single slot and bumps submit_attempt by one;
    -- guard_download_submission_slot refuses an unresolved slot and any other step.
    -- p_connection, p_namespace, p_locator and p_identity are no longer stored:
    -- nothing read them.
    UPDATE public.download_tasks SET
        submit_permit_id=p_permit,
        submit_attempt=submit_attempt+1,
        submit_dispatch_state='dispatching',
        submit_effect_class='unknown',
        submit_effect_key=task.idempotency_key,
        submit_spec_digest=p_spec,
        submit_executor_session=p_session,
        submit_work_item_id=p_work,
        submit_fencing_token=p_token,
        submit_owner_version=task.version,
        submit_prepared_at=now_at,
        submit_dispatch_started_at=now_at,
        submit_closed_proof=NULL,
        submit_resolution_proof=NULL,
        submit_observation=NULL,
        submit_conflict_evidence=NULL,
        submit_retry_safe=false,
        submit_unknown_since=NULL,
        submit_reconcile_until=NULL,
        submit_lookup_count=0,
        submit_lookup_ticket=NULL,
        submit_path_snapshot=p_path,
        submit_adopted_settlement_receipt=NULL,
        budget_clock_at=now_at
        WHERE id=p_task;
    RETURN jsonb_build_object('granted',true,'permit_id',p_permit,'gate_db_now',now_at,
        'attempt_until',ticket->'attempt_until');
END;
$$;

CREATE FUNCTION download_start_submit_attempt(p_task text, p_work text, p_token bigint, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    task public.download_tasks%ROWTYPE;
    now_at timestamptz;
    ticket jsonb;
BEGIN
    task := public.download_stage_lock(p_task,p_work,p_token);
    IF NOT EXISTS (SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_submit')
        OR p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_ATTEMPT_IDENTITY';
    END IF;
    IF task.state IN ('completed','failed') THEN
        PERFORM public.work_item_finish(
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version,p_work);
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_TASK_TERMINAL','state',task.state);
    END IF;
    IF EXISTS (SELECT 1 FROM public.download_tasks t
        WHERE t.id=p_task AND t.submit_permit_id IS NOT NULL
        AND NOT t.submit_retry_safe AND t.submit_dispatch_started_at IS NOT NULL) THEN
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_RECONCILE_REQUIRED');
    END IF;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF now_at>=task.submit_wall_until OR now_at>=task.wait_until
        OR (task.submit_attempts_started>=task.submit_max_attempts AND (
            task.submit_attempt_ticket->>'state'='issued'
            AND now_at<(task.submit_attempt_ticket->>'attempt_until')::timestamptz) IS NOT TRUE) THEN
        UPDATE public.download_tasks SET state='failed',failure_code='SUBMIT_BUDGET_EXHAUSTED',
            failure_reason='qB submission budget exhausted',budget_clock_at=now_at,
            submit_attempt_ticket=CASE WHEN submit_attempt_ticket->>'state'='issued'
                THEN jsonb_set(submit_attempt_ticket,'{state}','"superseded"') ELSE submit_attempt_ticket END,
            submit_attempt_index=COALESCE((SELECT jsonb_agg(CASE WHEN t->>'state'='issued'
                THEN jsonb_set(t,'{state}','"superseded"') ELSE t END ORDER BY ordinal)
                FROM jsonb_array_elements(submit_attempt_index) WITH ORDINALITY AS entries(t,ordinal)),'[]'::jsonb),
            version=version+1,updated_at=now_at WHERE id=p_task RETURNING * INTO task;
        PERFORM public.work_item_finish('failed',task.version,p_work);
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_BUDGET_EXHAUSTED');
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(task.submit_attempt_index) t
        WHERE t->>'work_item_id'=p_work AND (t->>'token')::bigint=p_token) THEN
        RETURN jsonb_build_object('granted',false,'ticket',task.submit_attempt_ticket);
    END IF;
    IF (task.submit_attempt_ticket->>'state'='issued'
        AND now_at<(task.submit_attempt_ticket->>'attempt_until')::timestamptz) IS TRUE THEN
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_ATTEMPT_ACTIVE');
    END IF;
    IF (task.submit_attempt_ticket->>'state'='issued'
        AND now_at>=(task.submit_attempt_ticket->>'attempt_until')::timestamptz) IS TRUE THEN
        UPDATE public.download_tasks SET
            submit_attempt_ticket=jsonb_set(submit_attempt_ticket,'{state}','"superseded"'),
            submit_attempt_index=jsonb_set(submit_attempt_index,
                ARRAY[((submit_attempt_ticket->>'ordinal')::integer-1)::text,'state'],'"superseded"')
            WHERE id=p_task;
    END IF;
    ticket := jsonb_build_object('work_item_id',p_work,'token',p_token,
        'ordinal',task.submit_attempts_started+1,'executor_session',p_session,
        'started_at',now_at,'attempt_until',least(task.submit_wall_until,task.wait_until,
            now_at+task.submit_attempt_timeout_seconds*INTERVAL '1 second'),'state','issued');
    UPDATE public.download_tasks SET budget_clock_at=now_at,
        submit_attempts_started=submit_attempts_started+1,submit_attempt_ticket=ticket,
        submit_attempt_index=submit_attempt_index || jsonb_build_array(ticket) WHERE id=p_task;
    RETURN jsonb_build_object('granted',true,'ticket',ticket,'gate_db_now',now_at);
END;
$$;

CREATE FUNCTION download_start_submit_lookup(p_task text, p_work text, p_token bigint, p_permit text, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    task public.download_tasks%ROWTYPE;
    evidence public.download_tasks%ROWTYPE;
    now_at timestamptz;
    ticket jsonb;
BEGIN
    task := public.download_recovery_lock(p_task,p_work,p_token);
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_submit') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_LOOKUP_IDENTITY';
    END IF;
    SELECT * INTO STRICT evidence FROM public.download_tasks
        WHERE submit_permit_id=p_permit AND id=p_task FOR UPDATE;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF evidence.submit_dispatch_started_at IS NULL OR evidence.submit_effect_class IS DISTINCT FROM 'unknown'
        OR evidence.submit_dispatch_state IS DISTINCT FROM 'dispatching' OR p_session IS NULL
        OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_LOOKUP_IDENTITY';
    END IF;
    IF evidence.submit_unknown_since IS NULL THEN
        evidence.submit_unknown_since := now_at;
        evidence.submit_reconcile_until := now_at + evidence.submit_reconcile_wall_seconds*INTERVAL '1 second';
        UPDATE public.download_tasks SET submit_unknown_since=evidence.submit_unknown_since,
            submit_reconcile_until=evidence.submit_reconcile_until WHERE id=p_task;
    END IF;
    IF evidence.submit_lookup_ticket IS NOT NULL AND evidence.submit_lookup_ticket->>'state'='issued'
        AND (evidence.submit_lookup_ticket->>'work_item_id' IS DISTINCT FROM p_work
            OR (evidence.submit_lookup_ticket->>'token')::bigint IS DISTINCT FROM p_token) THEN
        evidence.submit_lookup_ticket := evidence.submit_lookup_ticket||jsonb_build_object('state','superseded');
        UPDATE public.download_tasks SET submit_lookup_ticket=evidence.submit_lookup_ticket WHERE id=p_task;
    END IF;
    IF now_at>=evidence.submit_reconcile_until OR evidence.submit_lookup_count>=evidence.submit_max_lookups THEN
        UPDATE public.work_items SET recovery_blocked=true,
            recovery_reason='SUBMIT_RECONCILE_EXHAUSTED',updated_at=now_at WHERE id=p_work;
        RETURN jsonb_build_object('granted',false,'reason','SUBMIT_RECONCILE_EXHAUSTED');
    END IF;
    -- Reading a consumed grant never permits another provider call.
    -- Exhaustion takes precedence without closing the issued ticket.
    IF evidence.submit_lookup_ticket IS NOT NULL AND evidence.submit_lookup_ticket->>'state'='issued' THEN
        RETURN jsonb_build_object('granted',false,'ticket',evidence.submit_lookup_ticket,
            'reason','SUBMIT_LOOKUP_ACTIVE');
    END IF;
    -- The current ticket is the whole object; the per-ordinal history is gone.
    ticket := jsonb_build_object('ticket_id',gen_random_uuid()::text,'work_item_id',p_work,
        'token',p_token,'ordinal',evidence.submit_lookup_count+1,'executor_session',p_session,
        'started_at',now_at,
        'attempt_until',least(evidence.submit_reconcile_until,now_at+task.sync_attempt_timeout_seconds*INTERVAL '1 second'),
        'state','issued');
    UPDATE public.download_tasks SET submit_lookup_count=submit_lookup_count+1,
        submit_lookup_ticket=ticket,updated_at=now_at WHERE id=p_task;
    RETURN jsonb_build_object('granted',true,'ticket',ticket,'gate_db_now',now_at);
END;
$$;

CREATE FUNCTION download_start_sync(p_task text, p_work text, p_token bigint, p_ordinal integer, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    task public.download_tasks%ROWTYPE;
    now_at timestamptz;
    ticket jsonb;
    next_version bigint; next_work text; next_id text; exhausted boolean; inherited jsonb;
    one_shot jsonb; marker public.one_shot_acquisitions%ROWTYPE;
BEGIN
    -- Same optional marker-first gate as the normal T03 continuation.
    SELECT input_snapshot->'one_shot' INTO one_shot FROM public.work_item_history WHERE id=p_work;
    IF one_shot IS NOT NULL THEN
        SELECT * INTO marker FROM public.one_shot_acquisitions
            WHERE id=one_shot->>'marker_id' FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_MARKER_MISSING'; END IF;
        IF marker.state IS DISTINCT FROM 'running'
            OR (marker.admission_epoch=(one_shot->>'admission_epoch')::bigint) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
        END IF;
    END IF;
    task := public.download_stage_lock(p_task,p_work,p_token);
    IF one_shot IS DISTINCT FROM (SELECT input_snapshot->'one_shot' FROM public.work_items WHERE id=p_work) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ONE_SHOT_RECLAIMING';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.work_items WHERE id=p_work AND kind='qb_sync')
        OR p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SYNC_POLL_IDENTITY';
    END IF;
    IF task.state IN ('completed','failed') THEN
        PERFORM public.work_item_finish(
            CASE WHEN task.state='completed' THEN 'succeeded' ELSE 'failed' END,task.version,p_work);
        RETURN jsonb_build_object('granted',false,'reason','SYNC_TASK_TERMINAL','state',task.state);
    END IF;
    IF NOT EXISTS(SELECT 1 FROM public.work_items WHERE id=p_work
        AND (input_snapshot->>'poll_ordinal')::integer=p_ordinal) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_WORK_POLL_ORDINAL';
    END IF;
    now_at := greatest(clock_timestamp(),task.budget_clock_at);
    IF (task.active_poll_ticket->>'work_item_id'=p_work
        AND (task.active_poll_ticket->>'ordinal')::integer=p_ordinal
        AND task.active_poll_ticket->>'state'='issued'
        AND (task.active_poll_ticket->>'token')::bigint<p_token) IS TRUE THEN
        exhausted := now_at>=task.wait_until OR task.polls_started>=task.max_polls
            OR task.consecutive_errors+1>=task.max_consecutive_errors OR task.total_errors+1>=task.max_total_errors;
        UPDATE public.download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{state}','"superseded"'),
            consecutive_errors=consecutive_errors+1,total_errors=total_errors+1,
            budget_clock_at=now_at,version=version+1,updated_at=now_at,
            state=CASE WHEN exhausted THEN 'failed' ELSE state END,
            failure_code=CASE WHEN exhausted THEN 'SYNC_ERROR_BUDGET_EXHAUSTED' ELSE failure_code END,
            failure_reason=CASE WHEN exhausted THEN 'qB observation budget exhausted after interrupted poll' ELSE failure_reason END
            WHERE id=p_task RETURNING version INTO next_version;
        PERFORM public.work_item_finish('failed',next_version,p_work);
        IF NOT exhausted THEN
            SELECT CASE WHEN input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',input_snapshot->'one_shot')
                ELSE '{}'::jsonb END INTO inherited FROM public.work_item_outcomes WHERE id=p_work;
            next_id := gen_random_uuid()::text;
            SELECT public.work_item_create(next_id,'download_task',p_task,'qb_sync',task.idempotency_key,5,
                jsonb_build_object('trigger','qb_sync_progress','poll_ordinal',task.next_poll_ordinal)||inherited,
                next_version,greatest(0,extract(epoch FROM
                    (task.active_poll_ticket->>'started_at')::timestamptz+task.poll_interval_seconds*INTERVAL '1 second'-now_at))::double precision,
                7::smallint) INTO next_work;
            IF next_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_SUCCESSOR_CONFLICT'; END IF;
        END IF;
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('granted',false,'reason','SYNC_POLL_SUPERSEDED','task_version',next_version,'sync_work_id',next_work);
    END IF;
    IF now_at>=task.wait_until OR task.polls_started>=task.max_polls
        OR task.consecutive_errors>=task.max_consecutive_errors OR task.total_errors>=task.max_total_errors THEN
        UPDATE public.download_tasks SET state='failed',failure_code='SYNC_BUDGET_EXHAUSTED',
            failure_reason='qB download monitoring budget exhausted',budget_clock_at=now_at,
            active_poll_ticket=CASE WHEN active_poll_ticket->>'state'='issued'
                THEN jsonb_set(active_poll_ticket,'{state}','"superseded"') ELSE active_poll_ticket END,
            version=version+1,updated_at=now_at WHERE id=p_task RETURNING version INTO next_version;
        PERFORM public.work_item_finish('failed',next_version,p_work);
        PERFORM public.download_ensure_settlement(task.acquisition_id);
        RETURN jsonb_build_object('granted',false,'reason','SYNC_BUDGET_EXHAUSTED');
    END IF;
    IF (task.active_poll_ticket->>'work_item_id'=p_work
        AND (task.active_poll_ticket->>'token')::bigint=p_token) IS TRUE THEN
        RETURN jsonb_build_object('granted',false,'ticket',task.active_poll_ticket);
    END IF;
    IF p_ordinal IS DISTINCT FROM task.next_poll_ordinal OR task.active_poll_ticket->>'state'='issued' THEN
        RETURN jsonb_build_object('granted',false,'reason','SYNC_POLL_SUPERSEDED');
    END IF;
    IF task.active_poll_ticket IS NOT NULL AND now_at<
        (task.active_poll_ticket->>'started_at')::timestamptz+task.poll_interval_seconds*INTERVAL '1 second' THEN
        RETURN jsonb_build_object('granted',false,'reason','SYNC_POLL_NOT_DUE');
    END IF;
    ticket := jsonb_build_object('work_item_id',p_work,'token',p_token,'ordinal',p_ordinal,
        'executor_session',p_session,'started_at',now_at,
        'attempt_until',least(task.wait_until,now_at+task.sync_attempt_timeout_seconds*INTERVAL '1 second'),
        'state','issued');
    UPDATE public.download_tasks SET budget_clock_at=now_at,polls_started=polls_started+1,
        next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=ticket WHERE id=p_task;
    UPDATE public.work_items SET input_snapshot=jsonb_set(input_snapshot,'{sync_ticket}',ticket)
        WHERE id=p_work;
    RETURN jsonb_build_object('granted',true,'ticket',ticket,'gate_db_now',now_at);
END;
$$;

CREATE FUNCTION download_task_budget_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE
    root public.acquisitions%ROWTYPE;
    limits jsonb;
BEGIN
    SELECT * INTO STRICT root FROM public.acquisitions WHERE id = NEW.acquisition_id;
    SELECT input->'limits' INTO STRICT limits FROM public.acquisitions
    WHERE id = NEW.acquisition_id;
    IF root.execution_branch IS DISTINCT FROM 'download' OR limits IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_TASK_BUDGET_INPUT';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF (NEW.state='queued' AND NEW.submit_attempts_started=0
            AND NEW.submit_attempt_index='[]'::jsonb AND NEW.submit_attempt_ticket IS NULL
            AND NEW.polls_started=0 AND NEW.next_poll_ordinal=1 AND NEW.active_poll_ticket IS NULL
            AND NEW.consecutive_errors=0 AND NEW.total_errors=0) IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_INITIAL_SHAPE';
        END IF;
        NEW.stage_policy_version := (limits->>'policy_version')::integer;
        NEW.submit_ready_at := root.accepted_at;
        NEW.submit_wall_until := root.accepted_at + (limits->'submit'->>'wall_seconds')::integer * INTERVAL '1 second';
        NEW.submit_max_attempts := (limits->'submit'->>'max_attempts')::integer;
        NEW.submit_attempt_timeout_seconds := (limits->'submit'->>'attempt_timeout_seconds')::integer;
        NEW.budget_clock_at := root.accepted_at;
        NEW.wait_started_at := root.accepted_at;
        NEW.wait_until := root.accepted_at + (limits->>'download_wait_seconds')::integer * INTERVAL '1 second';
        NEW.poll_interval_seconds := (limits->'sync'->>'poll_interval_seconds')::integer;
        NEW.sync_attempt_timeout_seconds := (limits->'sync'->>'attempt_timeout_seconds')::integer;
        NEW.max_consecutive_errors := (limits->'sync'->>'max_consecutive_errors')::integer;
        NEW.max_total_errors := (limits->'sync'->>'max_total_errors')::integer;
        NEW.max_polls := (limits->'sync'->>'max_polls')::integer;
        NEW.stall_seconds := (limits->>'stall_seconds')::integer;
        NEW.submit_reconcile_wall_seconds := (limits->'reconcile'->>'wall_seconds')::integer;
        NEW.submit_max_lookups := (limits->'reconcile'->>'max_lookups')::integer;
    ELSE
        IF ROW(NEW.stage_policy_version,NEW.submit_ready_at,NEW.submit_wall_until,
            NEW.submit_max_attempts,NEW.submit_attempt_timeout_seconds,NEW.wait_started_at,
            NEW.wait_until,NEW.poll_interval_seconds,NEW.sync_attempt_timeout_seconds,
            NEW.max_consecutive_errors,NEW.max_total_errors,NEW.max_polls,NEW.stall_seconds,
            NEW.submit_reconcile_wall_seconds,NEW.submit_max_lookups)
            IS DISTINCT FROM ROW(OLD.stage_policy_version,OLD.submit_ready_at,OLD.submit_wall_until,
            OLD.submit_max_attempts,OLD.submit_attempt_timeout_seconds,OLD.wait_started_at,
            OLD.wait_until,OLD.poll_interval_seconds,OLD.sync_attempt_timeout_seconds,
            OLD.max_consecutive_errors,OLD.max_total_errors,OLD.max_polls,OLD.stall_seconds,
            OLD.submit_reconcile_wall_seconds,OLD.submit_max_lookups)
            OR NEW.budget_clock_at < OLD.budget_clock_at
            OR NEW.submit_attempts_started < OLD.submit_attempts_started
            OR NEW.polls_started < OLD.polls_started OR NEW.next_poll_ordinal < OLD.next_poll_ordinal
            OR NEW.total_errors < OLD.total_errors THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_TASK_BUDGET_IMMUTABLE';
        END IF;
        IF NEW.polls_started > OLD.polls_started+1
            OR NEW.submit_attempts_started > OLD.submit_attempts_started+1 THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_GRANT_STEP';
        END IF;
        IF (OLD.state IN ('completed','failed') OR NEW.state IN ('completed','failed'))
            AND (NEW.polls_started>OLD.polls_started
                OR NEW.submit_attempts_started>OLD.submit_attempts_started) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_TERMINAL_GRANT';
        END IF;
        IF (NEW.polls_started>OLD.polls_started OR NEW.submit_attempts_started>OLD.submit_attempts_started)
            AND (root.state IS DISTINCT FROM 'running' OR root.record_state IS DISTINCT FROM 'full') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_ROOT_GRANT';
        END IF;
        IF (NEW.polls_started=OLD.polls_started+1
            AND (NEW.active_poll_ticket->>'state'='issued') IS NOT TRUE)
            OR (NEW.submit_attempts_started=OLD.submit_attempts_started+1
            AND (NEW.submit_attempt_ticket->>'state'='issued') IS NOT TRUE) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_GRANT_REQUIRES_ISSUED';
        END IF;
        IF (NEW.polls_started=OLD.polls_started+1
            AND ((NEW.active_poll_ticket->>'started_at')::timestamptz<=greatest(clock_timestamp(),NEW.budget_clock_at)) IS NOT TRUE)
            OR (NEW.submit_attempts_started=OLD.submit_attempts_started+1
            AND ((NEW.submit_attempt_ticket->>'started_at')::timestamptz<=greatest(clock_timestamp(),NEW.budget_clock_at)) IS NOT TRUE) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_GRANT_FUTURE_TIME';
        END IF;
        IF (NEW.polls_started=OLD.polls_started+1
            AND greatest(clock_timestamp(),NEW.budget_clock_at)>=NEW.wait_until)
            OR (NEW.submit_attempts_started=OLD.submit_attempts_started+1
            AND greatest(clock_timestamp(),NEW.budget_clock_at)>=least(NEW.wait_until,NEW.submit_wall_until)) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_GRANT_DEADLINE';
        END IF;
        IF NEW.polls_started=OLD.polls_started+1 AND OLD.active_poll_ticket IS NOT NULL
            AND (greatest(clock_timestamp(),NEW.budget_clock_at)>=
                (OLD.active_poll_ticket->>'started_at')::timestamptz+NEW.poll_interval_seconds*INTERVAL '1 second') IS NOT TRUE THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_POLL_NOT_DUE';
        END IF;
        IF (NEW.polls_started=OLD.polls_started+1 AND NOT EXISTS(
            SELECT 1 FROM public.work_items w WHERE w.id=NEW.active_poll_ticket->>'work_item_id'
                AND w.owner_type='download_task' AND w.owner_id=NEW.id
                AND w.acquisition_id=NEW.acquisition_id AND w.kind='qb_sync'
                AND (w.input_snapshot->>'poll_ordinal')::integer=(NEW.active_poll_ticket->>'ordinal')::integer
                AND w.state='leased' AND w.lease_until>clock_timestamp()
                AND w.cancel_requested IS FALSE AND w.recovery_blocked IS FALSE
                AND w.expected_owner_version=NEW.version
                AND w.fencing_token=(NEW.active_poll_ticket->>'token')::bigint))
            OR (NEW.submit_attempts_started=OLD.submit_attempts_started+1 AND NOT EXISTS(
            SELECT 1 FROM public.work_items w WHERE w.id=NEW.submit_attempt_ticket->>'work_item_id'
                AND w.owner_type='download_task' AND w.owner_id=NEW.id
                AND w.acquisition_id=NEW.acquisition_id AND w.kind='qb_submit'
                AND w.state='leased' AND w.lease_until>clock_timestamp()
                AND w.cancel_requested IS FALSE AND w.recovery_blocked IS FALSE
                AND w.expected_owner_version=NEW.version
                AND w.fencing_token=(NEW.submit_attempt_ticket->>'token')::bigint)) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_GRANT_WORK_IDENTITY';
        END IF;
        IF (NEW.submit_attempts_started=OLD.submit_attempts_started+1 AND EXISTS(
            SELECT 1 FROM jsonb_array_elements(OLD.submit_attempt_index) previous
            WHERE previous->>'work_item_id'=NEW.submit_attempt_ticket->>'work_item_id'
                AND previous->>'token'=NEW.submit_attempt_ticket->>'token'))
            OR (NEW.polls_started=OLD.polls_started+1 AND EXISTS(
            SELECT 1 FROM public.work_items w WHERE w.id=NEW.active_poll_ticket->>'work_item_id'
                AND w.input_snapshot->'sync_ticket'->>'token'=NEW.active_poll_ticket->>'token')) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_TASK_TOKEN_REGRANT';
        END IF;
        IF EXISTS(SELECT 1 FROM jsonb_array_elements(OLD.submit_attempt_index) WITH ORDINALITY entries(ticket,ordinal)
            WHERE (ticket-'state') IS DISTINCT FROM
                ((NEW.submit_attempt_index->(ordinal::integer-1))-'state')) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_ATTEMPT_IDENTITY_IMMUTABLE';
        END IF;
        IF EXISTS(SELECT 1 FROM jsonb_array_elements(OLD.submit_attempt_index) WITH ORDINALITY entries(ticket,ordinal)
            WHERE ticket->>'state' IN ('completed','superseded')
                AND (NEW.submit_attempt_index->(ordinal::integer-1)->>'state') IS DISTINCT FROM ticket->>'state') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_ATTEMPT_STATE_REGRESSION';
        END IF;
        IF OLD.active_poll_ticket IS NOT NULL
            AND NEW.active_poll_ticket->'ordinal' IS NOT DISTINCT FROM OLD.active_poll_ticket->'ordinal'
            AND (NEW.active_poll_ticket-'state') IS DISTINCT FROM (OLD.active_poll_ticket-'state') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_POLL_IDENTITY_IMMUTABLE';
        END IF;
        IF OLD.active_poll_ticket->>'state' IN ('completed','superseded')
            AND NEW.active_poll_ticket->'ordinal' IS NOT DISTINCT FROM OLD.active_poll_ticket->'ordinal'
            AND NEW.active_poll_ticket->>'state' IS DISTINCT FROM OLD.active_poll_ticket->>'state' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_POLL_STATE_REGRESSION';
        END IF;
        IF OLD.active_poll_ticket->>'state'='issued'
            AND NEW.active_poll_ticket->'ordinal' IS DISTINCT FROM OLD.active_poll_ticket->'ordinal' THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_POLL_REPLACEMENT_BEFORE_CLOSURE';
        END IF;
    END IF;
    IF (NEW.stage_policy_version = 1
        AND NEW.submit_max_attempts BETWEEN 1 AND 100
        AND NEW.submit_attempt_timeout_seconds BETWEEN 1 AND 86400
        AND NEW.submit_wall_until >= NEW.submit_ready_at + NEW.submit_attempt_timeout_seconds * INTERVAL '1 second'
        AND NEW.submit_wall_until <= NEW.submit_ready_at + INTERVAL '7 days'
        AND NEW.wait_until > NEW.wait_started_at AND NEW.wait_until <= NEW.wait_started_at + INTERVAL '30 days'
        AND NEW.poll_interval_seconds BETWEEN 10 AND 3600
        AND NEW.sync_attempt_timeout_seconds BETWEEN 1 AND 86400
        AND NEW.max_consecutive_errors BETWEEN 1 AND 100
        AND NEW.max_total_errors BETWEEN NEW.max_consecutive_errors AND 100
        AND NEW.max_polls BETWEEN 1 AND 259201
        AND NEW.max_polls = ceil(extract(epoch FROM NEW.wait_until-NEW.wait_started_at)/NEW.poll_interval_seconds)::integer+1
        AND NEW.submit_attempts_started BETWEEN 0 AND NEW.submit_max_attempts
        AND jsonb_typeof(NEW.submit_attempt_index)='array'
        AND jsonb_array_length(NEW.submit_attempt_index)=NEW.submit_attempts_started
        AND NEW.polls_started BETWEEN 0 AND NEW.max_polls
        AND NEW.next_poll_ordinal = NEW.polls_started+1
        AND NEW.consecutive_errors BETWEEN 0 AND NEW.total_errors
        AND NEW.total_errors >= 0
        AND NEW.budget_clock_at >= NEW.submit_ready_at
        AND NEW.stall_seconds BETWEEN 1 AND extract(epoch FROM NEW.wait_until-NEW.wait_started_at)
        AND NEW.submit_reconcile_wall_seconds BETWEEN 1 AND 604800
        AND NEW.submit_max_lookups BETWEEN 1 AND 100) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='DOWNLOAD_TASK_BUDGET_SHAPE';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.submit_attempt_index) WITH ORDINALITY AS entries(ticket,ordinal)
        WHERE (jsonb_typeof(ticket)='object' AND (ticket->>'ordinal')::bigint=ordinal
            AND char_length(ticket->>'work_item_id') BETWEEN 1 AND 128
            AND (ticket->>'token')::bigint>=1
            AND char_length(ticket->>'executor_session') BETWEEN 1 AND 128
            AND ticket->>'state' IN ('issued','completed','superseded')
            AND (ticket->>'started_at')::timestamptz >= NEW.submit_ready_at
            AND (ticket->>'attempt_until')::timestamptz > (ticket->>'started_at')::timestamptz
            AND (ticket->>'attempt_until')::timestamptz <= (ticket->>'started_at')::timestamptz
                + NEW.submit_attempt_timeout_seconds * INTERVAL '1 second'
            AND (ticket->>'attempt_until')::timestamptz <= NEW.wait_until
            AND (ticket->>'attempt_until')::timestamptz <= NEW.submit_wall_until) IS NOT TRUE)
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.submit_attempt_index) ticket
            GROUP BY ticket->>'work_item_id',ticket->>'token' HAVING count(*)>1) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='SUBMIT_ATTEMPT_INDEX_SHAPE';
    END IF;
    IF (SELECT count(*) FROM jsonb_array_elements(NEW.submit_attempt_index) t
        WHERE t->>'state'='issued')>1
        OR (NEW.submit_attempts_started=0 AND NEW.submit_attempt_ticket IS NOT NULL)
        OR (NEW.submit_attempts_started>0 AND NEW.submit_attempt_ticket IS DISTINCT FROM
            NEW.submit_attempt_index->(NEW.submit_attempts_started-1))
        OR EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.submit_attempt_index) t
            WHERE t->>'state'='issued' AND t IS DISTINCT FROM NEW.submit_attempt_ticket) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_ATTEMPT_INDEX_CONSISTENCY';
    END IF;
    IF (NEW.polls_started=0 AND NEW.active_poll_ticket IS NOT NULL)
        OR (NEW.polls_started>0 AND (
            jsonb_typeof(NEW.active_poll_ticket)='object'
            AND (NEW.active_poll_ticket->>'ordinal')::integer=NEW.polls_started
            AND char_length(NEW.active_poll_ticket->>'work_item_id') BETWEEN 1 AND 128
            AND (NEW.active_poll_ticket->>'token')::bigint>0
            AND char_length(NEW.active_poll_ticket->>'executor_session') BETWEEN 1 AND 128
            AND NEW.active_poll_ticket->>'state' IN ('issued','completed','superseded')
            AND (NEW.active_poll_ticket->>'started_at')::timestamptz>=NEW.wait_started_at
            AND (NEW.active_poll_ticket->>'attempt_until')::timestamptz>
                (NEW.active_poll_ticket->>'started_at')::timestamptz
            AND (NEW.active_poll_ticket->>'attempt_until')::timestamptz <=
                (NEW.active_poll_ticket->>'started_at')::timestamptz
                    + NEW.sync_attempt_timeout_seconds * INTERVAL '1 second'
            AND (NEW.active_poll_ticket->>'attempt_until')::timestamptz<=NEW.wait_until) IS NOT TRUE) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_POLL_TICKET_SHAPE';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION download_task_failed_admission() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
  managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'download_failed','download_task',NEW.id,NEW.version,NEW.idempotency_key,
  NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',public.download_failed_notification(NEW.id),'pending_target_resolution')
 ON CONFLICT (trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key) DO NOTHING;
 RETURN NULL;
END $$;

CREATE FUNCTION download_work_ticket_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP='UPDATE' AND (OLD.input_snapshot ? 'sync_ticket' OR EXISTS(
        SELECT 1 FROM public.download_tasks t,jsonb_array_elements(t.submit_attempt_index) ticket
        WHERE t.id=OLD.owner_id AND ticket->>'work_item_id'=OLD.id))
        AND ROW(NEW.id,NEW.kind,NEW.owner_type,NEW.owner_id,NEW.acquisition_id)
            IS DISTINCT FROM ROW(OLD.id,OLD.kind,OLD.owner_type,OLD.owner_id,OLD.acquisition_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DOWNLOAD_WORK_TICKET_OWNER_IMMUTABLE';
    END IF;
    IF TG_OP='UPDATE' AND OLD.input_snapshot ? 'sync_ticket'
        AND (NEW.input_snapshot->'sync_ticket') IS DISTINCT FROM (OLD.input_snapshot->'sync_ticket') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_WORK_TICKET_IMMUTABLE';
    END IF;
    IF TG_OP='UPDATE' AND OLD.input_snapshot ? 'sync_ticket'
        AND ((NEW.input_snapshot->>'poll_ordinal')::integer=
            (OLD.input_snapshot->'sync_ticket'->>'ordinal')::integer) IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_WORK_POLL_ORDINAL_IMMUTABLE';
    END IF;
    IF NEW.input_snapshot ? 'sync_ticket' AND (TG_OP='INSERT' OR NOT (OLD.input_snapshot ? 'sync_ticket')) THEN
        IF NOT EXISTS(SELECT 1 FROM public.download_tasks t
            WHERE NEW.owner_type='download_task' AND NEW.kind='qb_sync'
                AND t.id=NEW.owner_id
                AND t.active_poll_ticket IS NOT NULL
                AND t.active_poll_ticket=NEW.input_snapshot->'sync_ticket'
                AND (NEW.input_snapshot->>'poll_ordinal')::integer=(t.active_poll_ticket->>'ordinal')::integer
                AND t.active_poll_ticket->>'work_item_id'=NEW.id
                AND (t.active_poll_ticket->>'token')::bigint=NEW.fencing_token
                AND t.active_poll_ticket->>'state'='issued') THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNC_WORK_TICKET_BINDING';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION unknown_files_notification(p_root text, p_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE input jsonb; release_id text; source_facts jsonb; only_path text; link text;
BEGIN
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=p_root;
 SELECT t.release_id INTO release_id FROM public.download_tasks t WHERE t.acquisition_id=p_root AND t.release_id IS NOT NULL LIMIT 1;
 SELECT rel.source_facts INTO source_facts FROM public.releases rel WHERE rel.id=release_id;
 -- One file opens the file manager on it with its match dialog open; several
 -- open this acquisition's execution record, which lists its files. Both are
 -- in-app hash anchors.
 only_path:=CASE WHEN jsonb_array_length(p_files)=1 THEN p_files->0->>'path' END;
 IF only_path IS NOT NULL THEN
  link:='#/?tool=files&root=library'
        ||CASE WHEN strpos(only_path,'/')>0
               THEN '&path='||public.notification_query_escape(regexp_replace(only_path,'/[^/]*$',''))
               ELSE '' END
        ||'&file='||public.notification_query_escape(only_path)||'&op=match';
 ELSE
  link:='#/?record='||public.notification_query_escape(p_root);
 END IF;
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),release_id)
  || jsonb_build_object(
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(p_root)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(p_root),
   'out.count',jsonb_array_length(p_files),
   'out.files',p_files,
   'event.link',link);
END $_$;

CREATE FUNCTION work_item_admit_managed_effect(boolean, text, bigint) RETURNS SETOF timestamp with time zone
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
WITH lifecycle AS (
    SELECT lifecycle.id AS managed_anime_id, lifecycle.fence_epoch
    FROM public.managed_anime lifecycle
    JOIN public.work_items item ON item.managed_anime_id = lifecycle.id
    WHERE item.id = $2
      AND item.state = 'leased'
      AND item.fencing_token = $3
      AND item.lease_until > CURRENT_TIMESTAMP
      AND item.cancel_requested = FALSE
      AND item.recovery_blocked = FALSE
      AND lifecycle.lifecycle_state = 'active'
      AND lifecycle.fence_epoch = item.owner_lifecycle_epoch
    FOR UPDATE OF lifecycle
)
UPDATE public.work_items item
SET external_effect_admitted_at = CASE
        WHEN $1::boolean THEN COALESCE(item.external_effect_admitted_at, CURRENT_TIMESTAMP)
        ELSE item.external_effect_admitted_at
    END,
    updated_at = CURRENT_TIMESTAMP
FROM lifecycle
WHERE item.id = $2
  AND item.state = 'leased'
  AND item.fencing_token = $3
  AND item.lease_until > CURRENT_TIMESTAMP
  AND item.cancel_requested = FALSE
  AND item.recovery_blocked = FALSE
RETURNING item.external_effect_admitted_at;
$_$;

CREATE FUNCTION work_item_block_recovery(text, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET recovery_blocked = TRUE, recovery_reason = $1,
    fencing_token = fencing_token + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND state = 'leased' AND fencing_token = $3
  AND recovery_blocked = FALSE;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_block_unrunnable(text[]) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
WITH blocked AS (
    SELECT id FROM public.work_items
    WHERE kind = ANY($1::text[])
      AND recovery_blocked = FALSE
      AND (cancel_requested OR attempt >= max_attempts)
      AND (state = 'available' OR (state = 'leased' AND lease_until <= CURRENT_TIMESTAMP))
    ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 100
)
UPDATE public.work_items AS item
SET recovery_blocked = TRUE,
    recovery_reason = CASE WHEN item.cancel_requested THEN 'cancel_requested' ELSE 'attempt_exhausted' END,
    fencing_token = item.fencing_token + 1, updated_at = CURRENT_TIMESTAMP
FROM blocked WHERE item.id = blocked.id;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_cancel_requests(text[]) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET cancel_requested = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE kind = ANY($1::text[]) AND state IN ('available', 'leased') AND cancel_requested = FALSE;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_checkpoint_owner_version(text, bigint, bigint, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items SET expected_owner_version=$4,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND fencing_token=$2 AND expected_owner_version=$3;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_claim_due(double precision, text[]) RETURNS TABLE(state_before text, id text, owner_type text, owner_id text, kind text, expected_owner_version bigint, fencing_token bigint, attempt integer, max_attempts integer, idempotency_key text, input_snapshot jsonb, cancel_requested boolean, managed_anime_id text, owner_lifecycle_epoch bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
-- The earliest runnable item in (available_at, id) order over the requested
-- kinds, due-available or lease-expired. The two states are read separately so
-- each uses its own partial index: per kind, the first unlocked due item from
-- work_items_claim_kind_idx; across kinds, the first unlocked expired lease
-- (leased rows are few). The minimum of those per-branch minimums is the same
-- item a single ordered scan over both states would return. A branch winner
-- that loses the final ordering stays row-locked only until this statement
-- commits and is not modified.
WITH due AS (
    SELECT avail.id, avail.state_before, avail.available_at
    FROM (SELECT DISTINCT requested.kind FROM unnest($2::text[]) AS requested(kind)) AS kinds
    CROSS JOIN LATERAL (
        SELECT item.id, item.state AS state_before, item.available_at
        FROM public.work_items item
        LEFT JOIN public.managed_anime lifecycle ON lifecycle.id = item.managed_anime_id
        WHERE item.kind = kinds.kind
          AND item.state = 'available' AND item.available_at <= CURRENT_TIMESTAMP
          AND item.cancel_requested = FALSE
          AND item.recovery_blocked = FALSE
          AND item.attempt < item.max_attempts
          AND (item.managed_anime_id IS NULL OR (
              lifecycle.lifecycle_state = 'active' AND lifecycle.fence_epoch = item.owner_lifecycle_epoch
          ))
        ORDER BY item.available_at, item.id
        FOR UPDATE OF item SKIP LOCKED
        LIMIT 1
    ) AS avail
    UNION ALL
    SELECT expired.id, expired.state_before, expired.available_at
    FROM (
        SELECT item.id, item.state AS state_before, item.available_at
        FROM public.work_items item
        LEFT JOIN public.managed_anime lifecycle ON lifecycle.id = item.managed_anime_id
        WHERE item.kind = ANY($2::text[])
          AND item.state = 'leased' AND item.lease_until IS NOT NULL AND item.lease_until <= CURRENT_TIMESTAMP
          AND item.cancel_requested = FALSE
          AND item.recovery_blocked = FALSE
          AND item.attempt < item.max_attempts
          AND (item.managed_anime_id IS NULL OR (
              lifecycle.lifecycle_state = 'active' AND lifecycle.fence_epoch = item.owner_lifecycle_epoch
          ))
        ORDER BY item.available_at, item.id
        FOR UPDATE OF item SKIP LOCKED
        LIMIT 1
    ) AS expired
), candidate AS (
    SELECT due.id, due.state_before FROM due ORDER BY due.available_at, due.id LIMIT 1
)
UPDATE public.work_items AS item
SET state = 'leased',
    lease_until = CURRENT_TIMESTAMP + make_interval(secs => $1),
    fencing_token = item.fencing_token + 1,
    attempt = item.attempt + 1,
    updated_at = CURRENT_TIMESTAMP
FROM candidate
WHERE item.id = candidate.id
RETURNING candidate.state_before, item.id, item.owner_type, item.owner_id, item.kind, item.expected_owner_version,
          item.fencing_token, item.attempt, item.max_attempts, item.idempotency_key, item.input_snapshot,
          item.cancel_requested, item.managed_anime_id, item.owner_lifecycle_epoch;
$_$;

CREATE FUNCTION work_item_claim_recovery_observation(text[], double precision) RETURNS TABLE(id text, owner_type text, owner_id text, kind text, expected_owner_version bigint, fencing_token bigint, attempt integer, max_attempts integer, idempotency_key text, input_snapshot jsonb, cancel_requested boolean, managed_anime_id text, owner_lifecycle_epoch bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
WITH candidate AS (
 SELECT id FROM public.work_items WHERE kind=ANY($1::text[]) AND recovery_blocked
 AND state IN ('available','leased') AND available_at<=CURRENT_TIMESTAMP
 AND (lease_until IS NULL OR lease_until<=CURRENT_TIMESTAMP)
 ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE public.work_items w SET state='leased', fencing_token=w.fencing_token+1,
 lease_until=CURRENT_TIMESTAMP+make_interval(secs=>$2),updated_at=CURRENT_TIMESTAMP
 FROM candidate WHERE w.id=candidate.id
 RETURNING w.id,w.owner_type,w.owner_id,w.kind,w.expected_owner_version,w.fencing_token,w.attempt,w.max_attempts,w.idempotency_key,w.input_snapshot,w.cancel_requested,w.managed_anime_id,w.owner_lifecycle_epoch;
$_$;

CREATE FUNCTION work_item_complete_cleanup(bigint, text, text, text, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items
SET expected_owner_version = $1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2
  AND owner_type = $3
  AND owner_id = $4
  AND kind = $5
  AND expected_owner_version <= $6;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($2, 'succeeded');
END;
$_$;

CREATE FUNCTION work_item_defer_recovery_observation(text, bigint, double precision, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items SET state='available',lease_until=NULL,available_at=CURRENT_TIMESTAMP+make_interval(secs=>$3),last_error_code=$4,last_error_reason='automatic observation blocked; preserved effect evidence',updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND fencing_token=$2 AND recovery_blocked AND state='leased';
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_delete_file_operation(text, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items
WHERE owner_type = $1 AND owner_id = $2;
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes
WHERE owner_type = $1 AND owner_id = $2;
GET DIAGNOSTICS retained = ROW_COUNT;
RETURN affected + retained;
END;
$_$;

CREATE FUNCTION work_item_fail_at_attempt_budget(bigint, text, text, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items
SET expected_owner_version = $1, last_error_code = $2, last_error_reason = NULLIF($3, ''),
    updated_at = CURRENT_TIMESTAMP
WHERE id = $4 AND expected_owner_version = $5;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($4, 'failed');
END;
$_$;

CREATE FUNCTION work_item_fail_available_file_operation(text, text, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items
SET last_error_code = NULLIF($2, ''), last_error_reason = NULLIF($3, ''), updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'available';
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($1, 'failed');
END;
$_$;

CREATE FUNCTION work_item_fail_stale_recovery(text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items SET expected_owner_version=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($1, 'failed');
END;
$_$;

CREATE FUNCTION work_item_finish_one_shot(text, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
BEGIN
UPDATE public.work_items SET updated_at=CURRENT_TIMESTAMP WHERE id=$2;
IF NOT FOUND THEN RETURN 0; END IF;
RETURN public.work_item_close($2, $1);
END;
$_$;

CREATE FUNCTION work_item_lock_claim(text, bigint) RETURNS TABLE(locked integer)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT 1 FROM public.work_items WHERE id=$1 AND state='leased' AND fencing_token=$2 AND lease_until IS NOT NULL AND lease_until>CURRENT_TIMESTAMP AND cancel_requested=FALSE AND recovery_blocked=FALSE FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_cleanup(text, text, text) RETURNS TABLE(id text, owner_type text, owner_id text, kind text, state text, expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT id,owner_type,owner_id,kind,state,expected_owner_version FROM public.work_items WHERE owner_type=$1 AND owner_id=$2 AND kind=$3 AND state IN ('available','leased') ORDER BY id LIMIT 1 FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_failed_retry(text, text, text) RETURNS TABLE(kind text, idempotency_key text, input_snapshot jsonb, max_attempts integer, state text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
RETURN QUERY SELECT w.kind, w.idempotency_key, w.input_snapshot, w.max_attempts, w.state
FROM public.work_items w WHERE w.id = $1 AND w.owner_type = $2 AND w.owner_id = $3 FOR UPDATE;
IF NOT FOUND THEN
RETURN QUERY SELECT o.kind, o.idempotency_key, o.input_snapshot, o.max_attempts, o.state
FROM public.work_item_outcomes o WHERE o.id = $1 AND o.owner_type = $2 AND o.owner_id = $3 FOR UPDATE;
END IF;
END;
$_$;

CREATE FUNCTION work_item_lock_failed_retry_nowait(text, text, text) RETURNS TABLE(kind text, idempotency_key text, input_snapshot jsonb, max_attempts integer, state text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
RETURN QUERY SELECT w.kind, w.idempotency_key, w.input_snapshot, w.max_attempts, w.state
FROM public.work_items w WHERE w.id = $1 AND w.owner_type = $2 AND w.owner_id = $3 FOR UPDATE NOWAIT;
IF NOT FOUND THEN
RETURN QUERY SELECT o.kind, o.idempotency_key, o.input_snapshot, o.max_attempts, o.state
FROM public.work_item_outcomes o WHERE o.id = $1 AND o.owner_type = $2 AND o.owner_id = $3 FOR UPDATE NOWAIT;
END IF;
END;
$_$;

CREATE FUNCTION work_item_lock_file_operation_inputs(text, text) RETURNS TABLE(input_snapshot jsonb)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
RETURN QUERY SELECT w.input_snapshot FROM public.work_items w WHERE w.owner_type=$1 AND w.owner_id=$2 ORDER BY w.created_at,w.id FOR UPDATE NOWAIT;
RETURN QUERY SELECT o.input_snapshot FROM public.work_item_outcomes o WHERE o.owner_type=$1 AND o.owner_id=$2 ORDER BY o.created_at,o.id FOR UPDATE NOWAIT;
END;
$_$;

CREATE FUNCTION work_item_lock_latest_file_execute(text, text, text) RETURNS TABLE(id text, state text, kind text, input_snapshot jsonb)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
-- At most one live row per owner and kind exists, and it is newer than every
-- outcome of that owner and kind.
RETURN QUERY SELECT w.id,w.state,w.kind,w.input_snapshot FROM public.work_items w WHERE w.owner_type=$1 AND w.owner_id=$2 AND w.kind=$3 FOR UPDATE NOWAIT;
IF NOT FOUND THEN
RETURN QUERY SELECT o.id,o.state,o.kind,o.input_snapshot FROM public.work_item_outcomes o WHERE o.owner_type=$1 AND o.owner_id=$2 AND o.kind=$3 ORDER BY o.created_at DESC,o.id DESC LIMIT 1 FOR UPDATE NOWAIT;
END IF;
END;
$_$;

CREATE FUNCTION work_item_lock_live_identity(text) RETURNS TABLE(id text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT id FROM public.work_items WHERE id=$1 AND state IN ('available','leased') FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_maintenance_cleanup(text, text) RETURNS TABLE(id text, owner_type text, owner_id text, kind text, state text, expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT id,owner_type,owner_id,kind,state,expected_owner_version FROM public.work_items WHERE owner_type='cleanup_operation' AND owner_id=$1 AND kind=$2 AND state IN ('available','leased') ORDER BY id LIMIT 1 FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_metadata_evaluations(text) RETURNS TABLE(id text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT wi.id FROM public.work_items wi JOIN public.releases r ON r.id=wi.owner_id JOIN public.rss_sources src ON src.id=r.rss_source_id JOIN public.subscriptions sub ON sub.id=src.subscription_id
WHERE wi.owner_type='release' AND wi.kind='release_evaluate' AND wi.state IN ('available','leased') AND sub.managed_anime_id=$1 AND r.verdict='waiting_metadata' AND r.verdict NOT IN ('adopted_existing','acquisition_failed') AND r.download_task_id IS NULL FOR UPDATE OF wi;
$_$;

CREATE FUNCTION work_item_lock_oneshot_identity(text) RETURNS TABLE(id text, owner_type text, owner_id text, input_snapshot jsonb)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
RETURN QUERY SELECT w.id,w.owner_type,w.owner_id,w.input_snapshot FROM public.work_items w WHERE w.id=$1 FOR UPDATE;
IF NOT FOUND THEN
RETURN QUERY SELECT o.id,o.owner_type,o.owner_id,o.input_snapshot FROM public.work_item_outcomes o WHERE o.id=$1 FOR UPDATE;
END IF;
END;
$_$;

CREATE FUNCTION work_item_lock_owner_version(text) RETURNS TABLE(expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT expected_owner_version FROM public.work_items WHERE id=$1 FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_placement_recovery(text, bigint, text) RETURNS TABLE(expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT expected_owner_version FROM public.work_items WHERE id=$1 AND fencing_token=$2 AND state='leased' AND recovery_blocked AND NOT cancel_requested AND lease_until>CURRENT_TIMESTAMP AND owner_type='file_operation' AND owner_id=$3 AND kind='file_execute' FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_placement_trigger(text, text) RETURNS TABLE(id text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
#variable_conflict use_column
BEGIN
RETURN QUERY SELECT w.id FROM public.work_items w WHERE w.owner_type='file_maintenance' AND w.owner_id=$1 AND w.kind='replacement_evaluate' AND w.input_snapshot->>'candidate_release_id'=$2 AND w.input_snapshot->>'trigger'='placement_committed' FOR UPDATE;
IF NOT FOUND THEN
RETURN QUERY SELECT o.id FROM public.work_item_outcomes o WHERE o.owner_type='file_maintenance' AND o.owner_id=$1 AND o.kind='replacement_evaluate' AND o.input_snapshot->>'candidate_release_id'=$2 AND o.input_snapshot->>'trigger'='placement_committed' AND o.state='succeeded' ORDER BY o.created_at DESC LIMIT 1 FOR UPDATE;
END IF;
END;
$_$;

CREATE FUNCTION work_item_lock_prepared_plan(text, text, bigint) RETURNS TABLE(expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT expected_owner_version FROM public.work_items WHERE id=$1 AND owner_type='postprocessing_plan' AND owner_id=$2 AND kind='postprocess_plan' AND state='leased' AND fencing_token=$3 AND lease_until>CURRENT_TIMESTAMP AND NOT cancel_requested AND NOT recovery_blocked FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_recovery(text, bigint, text, text) RETURNS TABLE(expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT expected_owner_version FROM public.work_items WHERE id=$1 AND fencing_token=$2 AND recovery_blocked AND state IN ('available','leased') AND NOT (input_snapshot ? 'one_shot') AND owner_type=$3 AND owner_id=$4 FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_recovery_observation(text, bigint, text, text) RETURNS TABLE(expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT expected_owner_version FROM public.work_items WHERE id=$1 AND fencing_token=$2 AND recovery_blocked AND state='leased' AND lease_until>CURRENT_TIMESTAMP AND owner_type=$3 AND owner_id=$4 FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_release_evaluation(text, text, text) RETURNS TABLE(id text, state text, expected_owner_version bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
SELECT id,state,expected_owner_version FROM public.work_items WHERE owner_type=$1 AND owner_id=$2 AND kind=$3 AND state IN ('available','leased') FOR UPDATE;
$_$;

CREATE FUNCTION work_item_lock_retirement_set(p_managed_anime text, p_epoch bigint) RETURNS text[]
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE ids text[];
BEGIN
 SELECT COALESCE(array_agg(id ORDER BY id COLLATE "C"),ARRAY[]::text[]) INTO ids FROM (
 SELECT id FROM public.work_items WHERE managed_anime_id=p_managed_anime AND owner_lifecycle_epoch=p_epoch
 AND state IN ('available','leased') AND external_effect_admitted_at IS NULL
 ORDER BY id COLLATE "C" FOR UPDATE NOWAIT) locked;
 RETURN ids;
END $$;

CREATE FUNCTION work_item_patch_one_shot_owner_version(bigint, text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
-- The one-shot finish closes the item before its owner write, then fences
-- the outcome with the owner version that write produced.
UPDATE public.work_item_outcomes
SET expected_owner_version = $1
WHERE id = $2;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_purge_expired(integer) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
-- Outcomes have no inbound references; one-shot outcomes leave with their
-- marker's own retention.
DELETE FROM public.work_item_outcomes AS item
WHERE item.id IN (
    SELECT item.id
    FROM public.work_item_outcomes AS item
    WHERE item.retention_until <= CURRENT_TIMESTAMP
      AND NOT (item.input_snapshot ? 'one_shot')
    ORDER BY item.retention_until, item.id
    LIMIT $1
);
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_renew_lease(timestamp with time zone, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET lease_until = $1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2
  AND state = 'leased'
  AND fencing_token = $3
  AND lease_until > CURRENT_TIMESTAMP
  AND recovery_blocked = FALSE
  AND cancel_requested = FALSE;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_request_cancel(text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET cancel_requested = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'leased' AND fencing_token = $2;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_retire_unadmitted(text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE p_managed_anime ALIAS FOR $1; p_epoch ALIAS FOR $2; ids text[]; affected bigint; retired text;
BEGIN
 ids:=public.work_item_lock_retirement_set(p_managed_anime,p_epoch);
 IF EXISTS(SELECT 1 FROM public.work_items WHERE managed_anime_id=p_managed_anime
 AND owner_lifecycle_epoch=p_epoch AND state IN ('available','leased') AND external_effect_admitted_at IS NULL
 AND NOT(id=ANY(ids))) THEN
 RAISE EXCEPTION USING ERRCODE='55P03',MESSAGE='RETIREMENT_WORK_SET_CHANGED'; END IF;
 affected:=0;
 FOR retired IN UPDATE public.work_items SET
  cancel_requested=true,last_error_code='MANAGED_ANIME_RETIRED',
  last_error_reason='the managed anime was retired before this effect was admitted',
  fencing_token=fencing_token+1,updated_at=CURRENT_TIMESTAMP
  WHERE id=ANY(ids) AND managed_anime_id=p_managed_anime AND owner_lifecycle_epoch=p_epoch
  AND external_effect_admitted_at IS NULL
  RETURNING id LOOP
  affected:=affected+public.work_item_close(retired,'failed');
 END LOOP;
 RETURN affected;
END $_$;

CREATE FUNCTION work_item_return_available(double precision, bigint, text, text, text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET state = 'available',
    available_at = CURRENT_TIMESTAMP + make_interval(secs => $1),
    lease_until = NULL, expected_owner_version = $2,
    last_error_code = $3, last_error_reason = NULLIF($4, ''),
    updated_at = CURRENT_TIMESTAMP
WHERE id = $5 AND expected_owner_version = $6;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_synchronize_available_release_version(text, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET expected_owner_version = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'available';
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_synchronize_owner_version(bigint, text, bigint, bigint) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items
SET expected_owner_version = $1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND state = 'leased' AND fencing_token = $3
  AND expected_owner_version = $4;
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_update_available_input(text, jsonb) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint;
BEGIN
UPDATE public.work_items SET input_snapshot = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'available';
GET DIAGNOSTICS affected = ROW_COUNT;
RETURN affected;
END;
$_$;

CREATE TRIGGER download_progress_guard
    BEFORE INSERT OR UPDATE ON download_tasks
    FOR EACH ROW EXECUTE FUNCTION download_progress_guard();
CREATE TRIGGER download_task_budget_guard
    BEFORE INSERT OR UPDATE ON download_tasks
    FOR EACH ROW EXECUTE FUNCTION download_task_budget_guard();
CREATE TRIGGER download_task_failed_notification
    AFTER UPDATE OF state ON download_tasks
    FOR EACH ROW WHEN (((old.state IS DISTINCT FROM 'failed'::text) AND (new.state = 'failed'::text))) EXECUTE FUNCTION download_task_failed_admission();

-- Generic WorkItem creation (PRUNE-046 S1b). The owner is the generic
-- (owner_type, owner_id) reference; fence_work_item_retirement validates the
-- owner, derives its acquisition root and freezes its managed anime on insert.
-- create never merges: a live conflict returns no row.
CREATE FUNCTION work_item_create(text, text, text, text, text, integer, jsonb, bigint, double precision, smallint) RETURNS SETOF text
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
INSERT INTO public.work_items(
    id, owner_type, owner_id, kind, idempotency_key,
    max_attempts, input_snapshot, expected_owner_version, available_at, retention_days)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP + make_interval(secs => $9), $10
-- An id is used once: a closed item's id stays taken while its outcome lives.
WHERE NOT EXISTS (SELECT 1 FROM public.work_item_outcomes o WHERE o.id = $1)
ON CONFLICT DO NOTHING
RETURNING id;
$_$;

-- merge refreshes the one live item of (owner, kind) carrying the same effect
-- key: an available item becomes due now, a leased item has its lease expired
-- so the old claim is fenced out and the reclaim observes the refreshed input.
-- Download work keeps its frozen sync/submit tickets across the merge.
CREATE FUNCTION work_item_merge(text, text, text, text, text, integer, jsonb, bigint, double precision, smallint) RETURNS SETOF text
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
INSERT INTO public.work_items(
    id, owner_type, owner_id, kind, idempotency_key,
    max_attempts, input_snapshot, expected_owner_version, available_at, retention_days)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP + make_interval(secs => $9), $10)
ON CONFLICT (owner_type, owner_id, kind)
DO UPDATE SET
    expected_owner_version = EXCLUDED.expected_owner_version,
    input_snapshot = CASE
        WHEN public.work_items.owner_type = 'download_task'
            THEN public.download_merge_work_input(public.work_items.input_snapshot, EXCLUDED.input_snapshot)
        ELSE EXCLUDED.input_snapshot
    END,
    max_attempts = EXCLUDED.max_attempts,
    available_at = CASE
        WHEN public.work_items.state = 'available' THEN CURRENT_TIMESTAMP
        ELSE public.work_items.available_at
    END,
    lease_until = CASE
        WHEN public.work_items.state = 'leased' THEN CURRENT_TIMESTAMP
        ELSE NULL
    END,
    updated_at = CURRENT_TIMESTAMP
WHERE public.work_items.idempotency_key = EXCLUDED.idempotency_key
RETURNING id;
$_$;

-- Owner and retirement fence of work_items.
-- INSERT: the owner row must exist (23503 WORK_ITEM_OWNER_MISSING); download
-- and file work pin the owner's effect idempotency key; the owner's
-- acquisition root is derived; the managed anime is resolved through the
-- owner chain, must be active, and is frozen with its lifecycle epoch.
-- UPDATE: the owner reference is immutable and the fence uses the frozen
-- managed anime only, so a row whose owner row is gone can still be failed by
-- its handler and never wedges its lane.
CREATE FUNCTION fence_work_item_retirement() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    owner_found BOOLEAN := FALSE;
    declares_no_anime BOOLEAN := FALSE;
    pins_key BOOLEAN := FALSE;
    owner_key TEXT;
    resolved_root TEXT;
    resolved_managed_anime_id TEXT;
    lifecycle_state TEXT;
    lifecycle_epoch BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.id, NEW.owner_type, NEW.owner_id, NEW.kind, NEW.acquisition_id)
           IS DISTINCT FROM (OLD.id, OLD.owner_type, OLD.owner_id, OLD.kind, OLD.acquisition_id) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_IMMUTABLE';
        END IF;
        IF NEW.managed_anime_id IS DISTINCT FROM OLD.managed_anime_id
           OR NEW.owner_lifecycle_epoch IS DISTINCT FROM OLD.owner_lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_LIFECYCLE_IMMUTABLE';
        END IF;
        IF OLD.managed_anime_id IS NULL THEN
            RETURN NEW;
        END IF;
        SELECT lifecycle.lifecycle_state, lifecycle.fence_epoch INTO lifecycle_state, lifecycle_epoch
        FROM managed_anime lifecycle
        WHERE lifecycle.id = OLD.managed_anime_id
        FOR KEY SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_MISSING';
        END IF;
        IF lifecycle_state <> 'active' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
        END IF;
        IF OLD.owner_lifecycle_epoch IS DISTINCT FROM lifecycle_epoch THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIREMENT_FENCE_STALE';
        END IF;
        RETURN NEW;
    END IF;

    CASE NEW.owner_type
    WHEN 'rss_source_runtime' THEN
        SELECT s.managed_anime_id INTO resolved_managed_anime_id
        FROM rss_source_runtime rt
        LEFT JOIN rss_sources rs ON rs.id = rt.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE rt.rss_source_id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'release' THEN
        SELECT s.managed_anime_id INTO resolved_managed_anime_id
        FROM releases r
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE r.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'download_task' THEN
        SELECT dt.acquisition_id, dt.idempotency_key, s.managed_anime_id
        INTO resolved_root, owner_key, resolved_managed_anime_id
        FROM download_tasks dt
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE dt.id = NEW.owner_id;
        owner_found := FOUND;
        pins_key := TRUE;
    WHEN 'artifact' THEN
        SELECT a.acquisition_id, s.managed_anime_id INTO resolved_root, resolved_managed_anime_id
        FROM artifacts a
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE a.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'file_operation' THEN
        SELECT fo.acquisition_id, fo.idempotency_key, s.managed_anime_id
        INTO resolved_root, owner_key, resolved_managed_anime_id
        FROM file_operations fo
        LEFT JOIN artifacts a ON a.id = fo.artifact_id
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE fo.id = NEW.owner_id;
        owner_found := FOUND;
        pins_key := TRUE;
    WHEN 'postprocessing_plan' THEN
        SELECT plan.acquisition_id, s.managed_anime_id INTO resolved_root, resolved_managed_anime_id
        FROM postprocessing_plans plan
        LEFT JOIN artifacts a ON a.id = plan.artifact_id
        LEFT JOIN download_tasks dt ON dt.id = a.download_task_id
        LEFT JOIN releases r ON r.id = dt.release_id
        LEFT JOIN rss_sources rs ON rs.id = r.rss_source_id
        LEFT JOIN subscriptions s ON s.id = rs.subscription_id
        WHERE plan.id = NEW.owner_id;
        owner_found := FOUND;
        IF owner_found AND resolved_root IS NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'ARTIFACT_PLAN_ROOT_MISSING';
        END IF;
    WHEN 'plugin_delivery' THEN
        SELECT delivery.managed_anime_id INTO resolved_managed_anime_id
        FROM plugin_deliveries delivery WHERE delivery.id = NEW.owner_id;
        owner_found := FOUND;
        declares_no_anime := owner_found AND resolved_managed_anime_id IS NULL;
    WHEN 'file_maintenance' THEN
        -- The file-maintenance owner id is the managed anime itself; the
        -- reconciliation kinds also need the anime's reconciliation scan.
        resolved_managed_anime_id := NEW.owner_id;
        owner_found := NEW.kind NOT IN ('reconciliation_scan', 'reconciliation_calibrate',
                'reconciliation_consume_alignment', 'reconciliation_consume_replacement')
            OR EXISTS (SELECT 1 FROM reconciliation_scans scan WHERE scan.managed_anime_id = NEW.owner_id);
    WHEN 'alignment_operation' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM alignment_operations op WHERE op.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'cleanup_operation' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM cleanup_operations op WHERE op.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'offset_migration_operation' THEN
        SELECT op.managed_anime_id INTO resolved_managed_anime_id
        FROM offset_migration_plans op WHERE op.id = NEW.owner_id;
        owner_found := FOUND;
    WHEN 'acquisition' THEN
        owner_found := EXISTS (SELECT 1 FROM acquisitions root WHERE root.id = NEW.owner_id);
        resolved_root := NEW.owner_id;
        declares_no_anime := TRUE;
    WHEN 'file_manager_operation' THEN
        owner_found := EXISTS (SELECT 1 FROM file_manager_operations op WHERE op.id = NEW.owner_id);
        declares_no_anime := TRUE;
    WHEN 'download_completion' THEN
        owner_found := EXISTS (SELECT 1 FROM download_completions completion WHERE completion.id = NEW.owner_id);
        declares_no_anime := TRUE;
    ELSE
        owner_found := FALSE;
    END CASE;

    IF NOT owner_found THEN
        RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'WORK_ITEM_OWNER_MISSING';
    END IF;
    IF pins_key AND owner_key IS DISTINCT FROM NEW.idempotency_key THEN
        RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'WORK_ITEM_OWNER_KEY_MISMATCH';
    END IF;
    IF NEW.acquisition_id IS NOT NULL AND NEW.acquisition_id IS DISTINCT FROM resolved_root THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_ROOT_MISMATCH';
    END IF;
    NEW.acquisition_id := resolved_root;
    NEW.managed_anime_id := NULL;
    NEW.owner_lifecycle_epoch := NULL;

    IF resolved_managed_anime_id IS NULL THEN
        IF NOT declares_no_anime THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_UNRESOLVED';
        END IF;
        RETURN NEW;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM managed_anime anime WHERE anime.id = resolved_managed_anime_id) THEN
        IF NEW.kind = 'replacement_evaluate' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'WORK_ITEM_OWNER_UNRESOLVED';
        END IF;
        -- Synthetic owner: no managed_anime identity exists, so no lifecycle
        -- marker can exist and there is nothing to fence.
        RETURN NEW;
    END IF;

    SELECT lifecycle.lifecycle_state, lifecycle.fence_epoch INTO lifecycle_state, lifecycle_epoch
    FROM managed_anime lifecycle
    WHERE lifecycle.id = resolved_managed_anime_id
    FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_MISSING';
    END IF;
    IF lifecycle_state <> 'active' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
    END IF;
    NEW.managed_anime_id := resolved_managed_anime_id;
    NEW.owner_lifecycle_epoch := lifecycle_epoch;
    RETURN NEW;
END;
$$;

-- Deleting live work of a retired managed anime is reserved to the local
-- cleanup transaction (bansoko.local_cleanup_owner); the frozen managed anime decides, never the owner chain.
CREATE FUNCTION fence_work_item_retirement_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    lifecycle_state TEXT;
BEGIN
    IF OLD.managed_anime_id IS NULL THEN
        RETURN OLD;
    END IF;
    IF current_setting('bansoko.local_cleanup_owner', true) IS NOT DISTINCT FROM OLD.managed_anime_id THEN
        RETURN OLD;
    END IF;
    SELECT lifecycle.lifecycle_state INTO lifecycle_state
    FROM managed_anime lifecycle
    WHERE lifecycle.id = OLD.managed_anime_id
    FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_LIFECYCLE_MISSING';
    END IF;
    IF lifecycle_state <> 'active' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'MANAGED_ANIME_RETIRED';
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER download_work_ticket_guard
    BEFORE INSERT OR UPDATE ON work_items
    FOR EACH ROW WHEN (NEW.owner_type = 'download_task') EXECUTE FUNCTION download_work_ticket_guard();
CREATE TRIGGER work_items_retirement_delete_fence
    BEFORE DELETE ON work_items
    FOR EACH ROW EXECUTE FUNCTION fence_work_item_retirement_delete();
CREATE TRIGGER work_items_retirement_fence
    BEFORE INSERT OR UPDATE ON work_items
    FOR EACH ROW EXECUTE FUNCTION fence_work_item_retirement();

CREATE FUNCTION guard_download_submission_slot() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP='UPDATE' AND OLD.submit_permit_id IS NOT NULL THEN
        IF NEW.submit_permit_id IS DISTINCT FROM OLD.submit_permit_id
            OR NEW.submit_effect_key IS DISTINCT FROM OLD.submit_effect_key
            OR NEW.submit_executor_session IS DISTINCT FROM OLD.submit_executor_session THEN
            -- S6: the column form of the old partial unique index
            -- `download_submission_unresolved_uq (acquisition_id, effect_key)
            -- WHERE NOT retry_safe`. A slot may only be replaced where that
            -- "unresolved" predicate is FALSE, i.e. retry_safe IS TRUE.
            IF OLD.submit_retry_safe IS NOT TRUE THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_UNRESOLVED';
            END IF;
            IF NEW.submit_attempt IS DISTINCT FROM OLD.submit_attempt+1 THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_ATTEMPT_STEP';
            END IF;
            IF NEW.submit_lookup_count IS DISTINCT FROM 0 OR NEW.submit_lookup_ticket IS NOT NULL
                OR NEW.submit_unknown_since IS NOT NULL OR NEW.submit_reconcile_until IS NOT NULL
                OR NEW.submit_closed_proof IS NOT NULL OR NEW.submit_resolution_proof IS NOT NULL
                OR NEW.submit_retry_safe IS NOT FALSE OR NEW.submit_adopted_settlement_receipt IS NOT NULL
                OR NEW.submit_dispatch_state IS DISTINCT FROM 'dispatching'
                OR NEW.submit_effect_class IS DISTINCT FROM 'unknown'
                OR NEW.submit_dispatch_started_at IS NULL THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_REPLACEMENT_SHAPE';
            END IF;
        ELSE
            -- Same slot: the granted identity is frozen and the facts only advance.
            IF ROW(NEW.submit_spec_digest,NEW.submit_work_item_id,NEW.submit_fencing_token,
                NEW.submit_owner_version,NEW.submit_prepared_at,NEW.submit_path_snapshot,NEW.submit_attempt)
                IS DISTINCT FROM ROW(OLD.submit_spec_digest,OLD.submit_work_item_id,OLD.submit_fencing_token,
                OLD.submit_owner_version,OLD.submit_prepared_at,OLD.submit_path_snapshot,OLD.submit_attempt) THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_IDENTITY_IMMUTABLE';
            END IF;
            IF OLD.submit_dispatch_state='closed' AND NEW.submit_dispatch_state IS DISTINCT FROM 'closed' THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_STATE_REGRESSION';
            END IF;
            IF (OLD.submit_dispatch_started_at IS NOT NULL
                    AND NEW.submit_dispatch_started_at IS DISTINCT FROM OLD.submit_dispatch_started_at)
                OR (OLD.submit_closed_proof IS NOT NULL
                    AND NEW.submit_closed_proof IS DISTINCT FROM OLD.submit_closed_proof)
                OR (OLD.submit_resolution_proof IS NOT NULL
                    AND NEW.submit_resolution_proof IS DISTINCT FROM OLD.submit_resolution_proof)
                OR (OLD.submit_unknown_since IS NOT NULL
                    AND ROW(NEW.submit_unknown_since,NEW.submit_reconcile_until)
                        IS DISTINCT FROM ROW(OLD.submit_unknown_since,OLD.submit_reconcile_until))
                OR NEW.submit_lookup_count < OLD.submit_lookup_count
                OR (OLD.submit_retry_safe IS TRUE AND NEW.submit_retry_safe IS DISTINCT FROM OLD.submit_retry_safe)
                OR (OLD.submit_adopted_settlement_receipt IS NOT NULL
                    AND NEW.submit_adopted_settlement_receipt IS DISTINCT FROM OLD.submit_adopted_settlement_receipt) THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBMIT_SLOT_FACT_IMMUTABLE';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER download_submission_slot_guard
    BEFORE UPDATE ON download_tasks
    FOR EACH ROW EXECUTE FUNCTION guard_download_submission_slot();

CREATE TRIGGER download_completions_guard
    BEFORE UPDATE ON download_completions
    FOR EACH ROW EXECUTE FUNCTION download_completions_guard();

-- The completion row closes the root's completion candidacy in the transaction
-- that writes it (PRUNE-046 S2; acquisitions.completion_open).
CREATE FUNCTION download_completions_close_candidate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 UPDATE public.acquisitions SET completion_open=false WHERE id=NEW.acquisition_id AND completion_open;
 RETURN NULL;
END $$;

CREATE TRIGGER download_completions_close_candidate
    AFTER INSERT ON download_completions
    FOR EACH ROW EXECUTE FUNCTION download_completions_close_candidate();

-- acquisitions.compact_pinned is exactly "a Release names one of the root's
-- tasks, or one of its file operations has a placement obligation". Either
-- makes acquisition_compact(detail_expired) decline for good (release_bound /
-- dependents_present): neither goes away except through a Release retry
-- (download_task_id moves to the new task), an owner cleanup or a one-shot
-- reclaim, and every such change re-derives the flag here. The cleanup and the
-- reclaim compact by their own paths, which do not read the flag.
CREATE FUNCTION acquisition_refresh_compact_pin(p_root text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 UPDATE public.acquisitions r SET compact_pinned=p.pinned
 FROM (SELECT EXISTS(SELECT 1 FROM public.download_tasks t JOIN public.releases rel ON rel.download_task_id=t.id
                      WHERE t.acquisition_id=p_root)
           OR EXISTS(SELECT 1 FROM public.file_operations o JOIN public.placement_obligations po ON po.file_operation_id=o.id
                      WHERE o.acquisition_id=p_root) AS pinned) p
 WHERE r.id=p_root AND r.compact_pinned IS DISTINCT FROM p.pinned;
END $$;

CREATE FUNCTION acquisition_compact_pin_from_release() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE root text;
BEGIN
 IF TG_OP IN ('UPDATE','DELETE') AND OLD.download_task_id IS NOT NULL THEN
  SELECT t.acquisition_id INTO root FROM public.download_tasks t WHERE t.id=OLD.download_task_id;
  IF root IS NOT NULL THEN PERFORM public.acquisition_refresh_compact_pin(root); END IF;
 END IF;
 IF TG_OP IN ('UPDATE','INSERT') AND NEW.download_task_id IS NOT NULL THEN
  root:=NULL;
  SELECT t.acquisition_id INTO root FROM public.download_tasks t WHERE t.id=NEW.download_task_id;
  IF root IS NOT NULL THEN PERFORM public.acquisition_refresh_compact_pin(root); END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION acquisition_compact_pin_from_task() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 -- A Release may name the task before the task row exists (deferred mutual keys).
 IF NEW.acquisition_id IS NOT NULL THEN PERFORM public.acquisition_refresh_compact_pin(NEW.acquisition_id); END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION acquisition_compact_pin_from_obligation() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE root text;
BEGIN
 SELECT o.acquisition_id INTO root FROM public.file_operations o WHERE o.id=COALESCE(NEW.file_operation_id,OLD.file_operation_id);
 IF root IS NOT NULL THEN PERFORM public.acquisition_refresh_compact_pin(root); END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER download_tasks_compact_pin
    AFTER INSERT ON download_tasks
    FOR EACH ROW EXECUTE FUNCTION acquisition_compact_pin_from_task();
CREATE TRIGGER releases_compact_pin_insert
    AFTER INSERT ON releases
    FOR EACH ROW WHEN (NEW.download_task_id IS NOT NULL)
    EXECUTE FUNCTION acquisition_compact_pin_from_release();
CREATE TRIGGER releases_compact_pin_delete
    AFTER DELETE ON releases
    FOR EACH ROW WHEN (OLD.download_task_id IS NOT NULL)
    EXECUTE FUNCTION acquisition_compact_pin_from_release();
CREATE TRIGGER releases_compact_pin_rebind
    AFTER UPDATE OF download_task_id ON releases
    FOR EACH ROW WHEN (OLD.download_task_id IS DISTINCT FROM NEW.download_task_id)
    EXECUTE FUNCTION acquisition_compact_pin_from_release();

-- The activity consumer's "logged" marker (PRUNE-046 S2). Called in the
-- consumer's per-entry transaction, after the owner locks and before the
-- run-log append: it locks the fact row (SKIP LOCKED, so a stage transaction
-- that holds it is never waited on) and stamps the marker. FALSE means the
-- row is held, gone or already logged; the consumer then skips the entry and
-- its rollback leaves the marker NULL, as does any later failure of the append.
CREATE FUNCTION acquisition_activity_mark_logged(p_fact text, p_id text, p_file_ref text DEFAULT NULL) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 CASE p_fact
 WHEN 'download_submitted' THEN
  PERFORM 1 FROM public.download_tasks WHERE id=p_id AND activity_submit_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.download_tasks SET activity_submit_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'download_terminal' THEN
  PERFORM 1 FROM public.download_tasks WHERE id=p_id AND activity_terminal_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.download_tasks SET activity_terminal_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'artifact_verified' THEN
  PERFORM 1 FROM public.artifacts WHERE id=p_id AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.artifacts SET activity_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 WHEN 'file_result' THEN
  PERFORM 1 FROM public.acquisition_files WHERE acquisition_id=p_id AND file_ref=p_file_ref AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.acquisition_files SET activity_logged_at=CURRENT_TIMESTAMP WHERE acquisition_id=p_id AND file_ref=p_file_ref;
 WHEN 'acquisition_settled' THEN
  PERFORM 1 FROM public.acquisitions WHERE id=p_id AND activity_logged_at IS NULL FOR NO KEY UPDATE SKIP LOCKED;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.acquisitions SET activity_logged_at=CURRENT_TIMESTAMP WHERE id=p_id;
 ELSE
  RAISE EXCEPTION USING ERRCODE='22023',MESSAGE='ACTIVITY_FACT_KIND';
 END CASE;
 RETURN true;
END $$;

REVOKE ALL ON FUNCTION download_completions_close_candidate, acquisition_refresh_compact_pin,
    acquisition_compact_pin_from_release, acquisition_compact_pin_from_task,
    acquisition_compact_pin_from_obligation, acquisition_activity_mark_logged FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquisition_activity_mark_logged TO bansoko_runtime;

-- PRUNE-046 S6: same split for the submission slot: submit_* is written only by
-- the SECURITY DEFINER submission functions (download_start_dispatch and
-- friends), everything else keeps the pre-fold privilege.
GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE
    idempotency_records, one_shot_acquisitions, one_shot_execution_attempts TO bansoko_runtime;
GRANT SELECT, DELETE ON TABLE download_tasks TO bansoko_runtime;
GRANT INSERT (
    id, release_id, qbittorrent_connection_id, provider_correlation, frozen_input, frozen_policy,
    state, idempotency_key, failure_code, failure_reason, version, created_at, updated_at,
    qbittorrent_connection_kind, correlation_id, causation_type, causation_id, acquisition_id,
    stage_policy_version, budget_clock_at, wait_started_at, wait_until, poll_interval_seconds,
    sync_attempt_timeout_seconds, max_consecutive_errors, max_total_errors, max_polls, polls_started,
    next_poll_ordinal, consecutive_errors, total_errors, active_poll_ticket, stall_since,
    stall_seconds, verified_bytes_highwater, payload_total_bytes, last_progress_at,
    progress_activated_at, activity_submit_logged_at, activity_terminal_logged_at
) ON TABLE download_tasks TO bansoko_runtime;
GRANT UPDATE (
    id, release_id, qbittorrent_connection_id, provider_correlation, frozen_input, frozen_policy,
    state, idempotency_key, failure_code, failure_reason, version, created_at, updated_at,
    qbittorrent_connection_kind, correlation_id, causation_type, causation_id, acquisition_id,
    stage_policy_version, budget_clock_at, wait_started_at, wait_until, poll_interval_seconds,
    sync_attempt_timeout_seconds, max_consecutive_errors, max_total_errors, max_polls, polls_started,
    next_poll_ordinal, consecutive_errors, total_errors, active_poll_ticket, stall_since,
    stall_seconds, verified_bytes_highwater, payload_total_bytes, last_progress_at,
    progress_activated_at, activity_submit_logged_at, activity_terminal_logged_at
) ON TABLE download_tasks TO bansoko_runtime;

GRANT SELECT, INSERT, UPDATE ON TABLE download_completions TO bansoko_runtime;
GRANT SELECT ON TABLE work_items, work_item_outcomes, work_item_history TO bansoko_runtime;
REVOKE ALL ON FUNCTION acquisition_consume_settle_work, acquisition_owner,
    work_item_create, work_item_merge, work_item_close, download_completions_guard,
    download_ensure_settlement, notification_acquisition_title,
    download_failed_notification, download_recovery_lock, work_item_finish, work_item_finish_recovery_observation,
    download_finish_submission, download_stage_lock, download_finish_sync,
    download_finish_unstarted_submission, download_freeze_path_snapshot,
    guard_download_submission_slot,
    download_record_submission_receipt, download_recover_sync, download_start_dispatch,
    download_start_submit_attempt, download_start_submit_lookup, download_start_sync,
    download_task_budget_guard,
    download_task_failed_admission, download_work_ticket_guard,
    unknown_files_notification, work_item_admit_managed_effect, work_item_block_recovery,
    work_item_block_unrunnable, work_item_cancel_requests, work_item_checkpoint_owner_version,
    work_item_claim_due, work_item_claim_recovery_observation, work_item_complete_cleanup,
    work_item_defer_recovery_observation,
    work_item_delete_file_operation, work_item_fail_at_attempt_budget,
    work_item_fail_available_file_operation, work_item_fail_stale_recovery,
    work_item_finish_one_shot, work_item_lock_claim,
    work_item_lock_cleanup, work_item_lock_failed_retry, work_item_lock_failed_retry_nowait,
    work_item_lock_file_operation_inputs, work_item_lock_latest_file_execute,
    work_item_lock_live_identity, work_item_lock_maintenance_cleanup,
    work_item_lock_metadata_evaluations, work_item_lock_oneshot_identity,
    work_item_lock_owner_version, work_item_lock_placement_recovery,
    work_item_lock_placement_trigger, work_item_lock_prepared_plan,
    work_item_lock_recovery,
    work_item_lock_recovery_observation, work_item_lock_release_evaluation,
    work_item_lock_retirement_set, work_item_patch_one_shot_owner_version,
    work_item_purge_expired, work_item_renew_lease, work_item_request_cancel,
    work_item_retire_unadmitted, work_item_return_available,
    work_item_synchronize_available_release_version, work_item_synchronize_owner_version,
    work_item_update_available_input FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquisition_owner, work_item_create, work_item_merge,
    work_item_finish, work_item_finish_recovery_observation,
    download_finish_submission, download_finish_sync, download_finish_unstarted_submission,
    download_freeze_path_snapshot, download_record_submission_receipt, download_recover_sync,
    download_start_dispatch, download_start_submit_attempt, download_start_submit_lookup,
    download_start_sync, work_item_admit_managed_effect, work_item_block_recovery,
    work_item_block_unrunnable, work_item_cancel_requests, work_item_checkpoint_owner_version,
    work_item_claim_due, work_item_claim_recovery_observation, work_item_complete_cleanup,
    work_item_defer_recovery_observation,
    work_item_delete_file_operation, work_item_fail_at_attempt_budget,
    work_item_fail_available_file_operation, work_item_fail_stale_recovery,
    work_item_finish_one_shot, work_item_lock_claim,
    work_item_lock_cleanup, work_item_lock_failed_retry, work_item_lock_failed_retry_nowait,
    work_item_lock_file_operation_inputs, work_item_lock_latest_file_execute,
    work_item_lock_live_identity, work_item_lock_maintenance_cleanup,
    work_item_lock_metadata_evaluations, work_item_lock_oneshot_identity,
    work_item_lock_owner_version, work_item_lock_placement_recovery,
    work_item_lock_placement_trigger, work_item_lock_prepared_plan,
    work_item_lock_recovery,
    work_item_lock_recovery_observation, work_item_lock_release_evaluation,
    work_item_lock_retirement_set, work_item_patch_one_shot_owner_version,
    work_item_purge_expired, work_item_renew_lease, work_item_request_cancel,
    work_item_retire_unadmitted, work_item_return_available,
    work_item_synchronize_available_release_version, work_item_synchronize_owner_version,
    work_item_update_available_input TO bansoko_runtime;

-- ============================================================================
-- Artifacts, inspection, post-processing plans, file operations and publication evidence
-- Artifact inspection, post-processing plans and their items, file operations
-- and the publication evidence and bindings that authorize each filesystem
-- step; local cleanup receipts authorize physical deletion of a retired owner.
-- ============================================================================

CREATE TABLE artifacts (
    id TEXT NOT NULL,
    download_task_id TEXT NOT NULL,
    inventory_snapshot JSONB NOT NULL,
    fingerprint_snapshot JSONB NOT NULL,
    state TEXT NOT NULL,
    failure_code TEXT,
    failure_reason TEXT,
    verified_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    acquisition_id TEXT NOT NULL,
    inspection_requirements_status TEXT,
    inspect_ready_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    inspect_wall_until TIMESTAMPTZ DEFAULT (CURRENT_TIMESTAMP + '24:00:00'::interval),
    inspect_max_attempts INTEGER DEFAULT 5,
    inspect_attempt_timeout_seconds INTEGER DEFAULT 300,
    inspect_attempts_started INTEGER NOT NULL DEFAULT 0,
    inspect_attempt_ticket JSONB,
    inspect_attempt_index JSONB NOT NULL DEFAULT '[]'::jsonb,
    activity_logged_at TIMESTAMPTZ,
    CONSTRAINT artifacts_pkey PRIMARY KEY (id),
    CONSTRAINT artifacts_download_task_id_key UNIQUE (download_task_id),
    CONSTRAINT artifacts_id_acquisition_uq UNIQUE (id, acquisition_id),
    CONSTRAINT artifacts_id_state_key UNIQUE (id, state),
    CONSTRAINT artifacts_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT artifacts_download_task_id_fkey FOREIGN KEY (download_task_id) REFERENCES download_tasks (id),
    CONSTRAINT artifacts_task_same_root_fk FOREIGN KEY (download_task_id, acquisition_id) REFERENCES download_tasks (id, acquisition_id),
    CONSTRAINT artifacts_acquisition_id_check CHECK (acquisition_id IS NULL OR char_length(acquisition_id) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_check CHECK ((state = 'verified') = (verified_at IS NOT NULL)),
    CONSTRAINT artifacts_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_failure_reason_check CHECK (failure_reason IS NULL OR char_length(failure_reason) <= 2048),
    CONSTRAINT artifacts_fingerprint_snapshot_check CHECK (jsonb_typeof(fingerprint_snapshot) = 'object' AND pg_column_size(fingerprint_snapshot) <= 1048576),
    CONSTRAINT artifacts_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT artifacts_inspection_requirements_status_check CHECK (inspection_requirements_status IN ('not_reached', 'incomplete')),
    CONSTRAINT artifacts_inventory_snapshot_check CHECK (jsonb_typeof(inventory_snapshot) = 'object' AND pg_column_size(inventory_snapshot) <= 2097152),
    CONSTRAINT artifacts_state_check CHECK (state IN ('pending', 'verified', 'waiting_retry', 'failed')),
    CONSTRAINT artifacts_version_check CHECK (version >= 1),
    CONSTRAINT inspect_attempt_budget_shape CHECK ((inspect_ready_at IS NOT NULL AND inspect_wall_until IS NOT NULL AND inspect_wall_until >= (inspect_ready_at + (inspect_attempt_timeout_seconds::double precision * '00:00:01'::interval)) AND inspect_max_attempts BETWEEN 1 AND 100 AND inspect_attempt_timeout_seconds BETWEEN 1 AND 86400 AND (inspect_attempts_started >= 0 AND inspect_attempts_started <= inspect_max_attempts) AND jsonb_typeof(inspect_attempt_index) = 'array' AND jsonb_array_length(inspect_attempt_index) = inspect_attempts_started AND (inspect_attempt_ticket IS NULL OR jsonb_typeof(inspect_attempt_ticket) = 'object')) IS TRUE)
);

CREATE INDEX artifacts_correlation_idx ON artifacts (correlation_id, created_at, id)
    WHERE correlation_id IS NOT NULL;
-- Verified artifacts the activity consumer has not logged (PRUNE-046 S2).
COMMENT ON COLUMN artifacts.activity_logged_at IS 'When the activity consumer logged the verified entry (NULL: not yet).';
CREATE INDEX artifacts_activity_unlogged_idx ON artifacts (verified_at, id)
    WHERE state = 'verified' AND activity_logged_at IS NULL;

CREATE TABLE artifact_files (
    id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    file_ref TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_kind TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    fingerprint JSONB NOT NULL,
    facts JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    subtitle_binding_state TEXT NOT NULL,
    bound_video_file_ref TEXT,
    subtitle_binding_snapshot JSONB,
    acquisition_id TEXT NOT NULL,
    CONSTRAINT artifact_files_pkey PRIMARY KEY (id),
    CONSTRAINT artifact_files_artifact_id_file_ref_key UNIQUE (artifact_id, file_ref),
    CONSTRAINT artifact_files_artifact_id_relative_path_key UNIQUE (artifact_id, relative_path),
    CONSTRAINT artifact_files_id_artifact_id_key UNIQUE (id, artifact_id),
    CONSTRAINT artifact_files_id_artifact_id_source_path_key UNIQUE (id, artifact_id, source_path),
    CONSTRAINT artifact_files_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT artifact_files_artifact_id_fkey FOREIGN KEY (artifact_id) REFERENCES artifacts (id),
    CONSTRAINT artifact_files_artifact_same_root_fk FOREIGN KEY (artifact_id, acquisition_id) REFERENCES artifacts (id, acquisition_id),
    CONSTRAINT artifact_files_acquisition_id_check CHECK (acquisition_id IS NULL OR char_length(acquisition_id) BETWEEN 1 AND 128),
    CONSTRAINT artifact_files_bound_video_file_ref_check CHECK (bound_video_file_ref IS NULL OR char_length(btrim(bound_video_file_ref)) BETWEEN 1 AND 512),
    CONSTRAINT artifact_files_facts_check CHECK (jsonb_typeof(facts) = 'object' AND pg_column_size(facts) <= 1048576),
    CONSTRAINT artifact_files_file_ref_check CHECK (char_length(btrim(file_ref)) BETWEEN 1 AND 512),
    CONSTRAINT artifact_files_fingerprint_check CHECK (jsonb_typeof(fingerprint) = 'object' AND pg_column_size(fingerprint) <= 262144),
    CONSTRAINT artifact_files_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT artifact_files_media_kind_check CHECK (media_kind IN ('video', 'subtitle', 'other')),
    CONSTRAINT artifact_files_relative_path_check CHECK (char_length(btrim(relative_path)) BETWEEN 1 AND 4096 AND relative_path !~ '^/' AND relative_path !~ '(^|/)\.\.(/|$)' AND relative_path !~ '[[:cntrl:]]'),
    CONSTRAINT artifact_files_size_bytes_check CHECK (size_bytes >= 0),
    CONSTRAINT artifact_files_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096 AND source_path !~ '[[:cntrl:]]'),
    CONSTRAINT artifact_files_subtitle_binding_ref_check CHECK ((subtitle_binding_state = 'bound') = (bound_video_file_ref IS NOT NULL)),
    CONSTRAINT artifact_files_subtitle_binding_shape_check CHECK ((media_kind <> 'subtitle' AND subtitle_binding_state = 'not_applicable' AND bound_video_file_ref IS NULL AND subtitle_binding_snapshot IS NULL) OR (media_kind = 'subtitle' AND subtitle_binding_state IN ('bound', 'missing', 'ambiguous'))),
    CONSTRAINT artifact_files_subtitle_binding_snapshot_check CHECK (subtitle_binding_snapshot IS NULL OR (jsonb_typeof(subtitle_binding_snapshot) = 'object' AND pg_column_size(subtitle_binding_snapshot) <= 262144)),
    CONSTRAINT artifact_files_subtitle_binding_state_check CHECK (subtitle_binding_state IN ('not_applicable', 'bound', 'missing', 'ambiguous'))
);

ALTER TABLE artifact_files
    ADD CONSTRAINT artifact_files_bound_video_same_artifact_fk FOREIGN KEY (artifact_id, bound_video_file_ref) REFERENCES artifact_files (artifact_id, file_ref);

CREATE TABLE postprocessing_plans (
    id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    artifact_version BIGINT NOT NULL,
    one_shot_id TEXT,
    state TEXT NOT NULL,
    outcome_code TEXT,
    outcome_reason TEXT,
    input_digest TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    acquisition_id TEXT NOT NULL,
    sealed_at TIMESTAMPTZ,
    requirements_digest TEXT,
    target_digest TEXT,
    plan_digest TEXT,
    planning_requirements_status TEXT,
    planning_diagnostic JSONB,
    planning_facts_revision BIGINT,
    canonical_requirements TEXT,
    canonical_targets TEXT,
    canonical_plan TEXT,
    committed_operations JSONB,
    committed_bindings JSONB,
    committed_binding_sources JSONB,
    plan_ready_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    plan_wall_until TIMESTAMPTZ DEFAULT (CURRENT_TIMESTAMP + '24:00:00'::interval),
    plan_max_attempts INTEGER DEFAULT 5,
    plan_attempt_timeout_seconds INTEGER DEFAULT 120,
    plan_attempts_started INTEGER NOT NULL DEFAULT 0,
    plan_attempt_ticket JSONB,
    plan_attempt_index JSONB NOT NULL DEFAULT '[]'::jsonb,
    CONSTRAINT postprocessing_plans_pkey PRIMARY KEY (id),
    CONSTRAINT postprocessing_plans_id_root_uq UNIQUE (id, acquisition_id),
    CONSTRAINT postprocessing_plans_root_uq UNIQUE (acquisition_id),
    CONSTRAINT postprocessing_plans_acquisition_id_fkey FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT postprocessing_plans_artifact_root_fk FOREIGN KEY (artifact_id, acquisition_id) REFERENCES artifacts (id, acquisition_id),
    CONSTRAINT plan_attempt_budget_shape CHECK ((plan_ready_at IS NOT NULL AND plan_wall_until IS NOT NULL AND plan_wall_until >= (plan_ready_at + (plan_attempt_timeout_seconds::double precision * '00:00:01'::interval)) AND plan_max_attempts BETWEEN 1 AND 100 AND plan_attempt_timeout_seconds BETWEEN 1 AND 86400 AND (plan_attempts_started >= 0 AND plan_attempts_started <= plan_max_attempts) AND jsonb_typeof(plan_attempt_index) = 'array' AND jsonb_array_length(plan_attempt_index) = plan_attempts_started AND (plan_attempt_ticket IS NULL OR jsonb_typeof(plan_attempt_ticket) = 'object')) IS TRUE),
    CONSTRAINT postprocessing_plans_artifact_id_check CHECK (char_length(btrim(artifact_id)) BETWEEN 1 AND 128),
    CONSTRAINT postprocessing_plans_artifact_version_check CHECK (artifact_version >= 1),
    CONSTRAINT postprocessing_plans_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT postprocessing_plans_input_digest_check CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT postprocessing_plans_one_shot_id_check CHECK (char_length(btrim(one_shot_id)) BETWEEN 1 AND 128),
    CONSTRAINT postprocessing_plans_outcome_code_check CHECK (outcome_code IS NULL OR char_length(btrim(outcome_code)) BETWEEN 1 AND 128),
    CONSTRAINT postprocessing_plans_outcome_reason_check CHECK (outcome_reason IS NULL OR char_length(btrim(outcome_reason)) <= 1024),
    CONSTRAINT postprocessing_plans_plan_digest_check CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT postprocessing_plans_planning_diagnostic_check CHECK (jsonb_typeof(planning_diagnostic) = 'object'),
    CONSTRAINT postprocessing_plans_planning_facts_revision_check CHECK (planning_facts_revision > 0),
    CONSTRAINT postprocessing_plans_planning_requirements_status_check CHECK (planning_requirements_status IN ('sealed', 'not_reached', 'incomplete')),
    CONSTRAINT postprocessing_plans_requirements_digest_check CHECK (requirements_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT postprocessing_plans_seal_shape CHECK ((sealed_at IS NULL AND requirements_digest IS NULL AND target_digest IS NULL AND plan_digest IS NULL) OR (sealed_at IS NOT NULL AND requirements_digest IS NOT NULL AND target_digest IS NOT NULL AND plan_digest IS NOT NULL AND state = 'succeeded')),
    CONSTRAINT postprocessing_plans_state_check CHECK (state IN ('queued', 'succeeded', 'failed')),
    CONSTRAINT postprocessing_plans_state_matrix_check CHECK ((state = 'queued' AND outcome_code IS NULL AND outcome_reason IS NULL) OR (state IN ('succeeded', 'failed') AND outcome_code IS NOT NULL AND outcome_reason IS NOT NULL)),
    CONSTRAINT postprocessing_plans_target_digest_check CHECK (target_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT postprocessing_plans_version_check CHECK (version >= 1)
);

CREATE INDEX postprocessing_plans_artifact_idx ON postprocessing_plans (artifact_id, state);
CREATE UNIQUE INDEX postprocessing_plans_live_uq ON postprocessing_plans (artifact_id)
    WHERE state = 'queued';

COMMENT ON TABLE postprocessing_plans IS 'Bounded post-processing planning stage: executes naming/target planning from one verified artifact version and freezes FileOperations; failures are retryable without rewriting the verified artifact fact.';


CREATE TABLE file_operations (
    id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    artifact_version BIGINT NOT NULL,
    artifact_file_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    source_path TEXT NOT NULL,
    target_path TEXT NOT NULL,
    source_fingerprint JSONB NOT NULL,
    target_fingerprint JSONB,
    collision_policy TEXT NOT NULL,
    frozen_render_snapshot JSONB NOT NULL,
    frozen_config_snapshot JSONB NOT NULL,
    state TEXT NOT NULL,
    observed_at TIMESTAMPTZ,
    failure_code TEXT,
    failure_reason TEXT,
    idempotency_key TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    overwrite_execution_snapshot JSONB,
    bound_video_operation_id TEXT,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    expected_size_bytes BIGINT,
    observed_target_size_bytes BIGINT,
    intent TEXT NOT NULL DEFAULT 'ensure_present',
    publication_checkpoint JSONB,
    acquisition_id TEXT NOT NULL,
    file_ref TEXT NOT NULL,
    required_tag TEXT NOT NULL DEFAULT 'required',
    postprocessing_plan_id TEXT NOT NULL,
    file_ready_at TIMESTAMPTZ NOT NULL,
    file_wall_until TIMESTAMPTZ NOT NULL,
    file_max_attempts INTEGER NOT NULL,
    file_attempt_timeout_seconds INTEGER NOT NULL,
    budget_clock_at TIMESTAMPTZ,
    file_attempts_started INTEGER NOT NULL DEFAULT 0,
    file_attempt_ticket JSONB,
    file_attempt_index JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- PRUNE-046 S6 (revised 10-15): the single current file-publication slot,
    -- moved verbatim off `file_publication_evidence`. The slot exists when
    -- publication_permit_id is not NULL. `publication_attempt` is the old
    -- attempt_ordinal and increments by one on an admitted overwrite.

    publication_permit_id TEXT,
    publication_dispatch_state TEXT,
    publication_effect_class TEXT,
    publication_effect_key TEXT,
    publication_spec_digest TEXT,
    publication_plan_digest TEXT,
    publication_dependency_digest TEXT,
    publication_executor_session TEXT,
    publication_work_item_id TEXT,
    publication_fencing_token BIGINT,
    publication_owner_version BIGINT,
    publication_attempt INTEGER NOT NULL DEFAULT 0,
    publication_evidence_version BIGINT NOT NULL DEFAULT 1,
    publication_prepared_at TIMESTAMPTZ,
    publication_dispatch_started_at TIMESTAMPTZ,
    publication_frozen_objects JSONB,
    publication_frozen_objects_canonical TEXT,
    publication_step_checkpoint JSONB,
    publication_step_proof_bytes JSONB,
    publication_stage_temp_started_at TIMESTAMPTZ,
    publication_reserve_version_started_at TIMESTAMPTZ,
    publication_publish_target_started_at TIMESTAMPTZ,
    publication_source_cleanup_started_at TIMESTAMPTZ,
    publication_temp_cleanup_started_at TIMESTAMPTZ,
    publication_closed_proof JSONB,
    publication_resolution_proof JSONB,
    publication_closure_canonical TEXT,
    publication_resolution_canonical TEXT,
    publication_resolution_checkpoint_canonical TEXT,
    publication_conflict_evidence JSONB,
    publication_conflict_count BIGINT NOT NULL DEFAULT 0,
    publication_conflict_seen JSONB NOT NULL DEFAULT '{}'::jsonb,
    publication_conflict_index_exhausted BOOLEAN NOT NULL DEFAULT false,
    publication_conflict_digest TEXT,
    publication_retry_safe BOOLEAN NOT NULL DEFAULT false,
    publication_unknown_since TIMESTAMPTZ,
    publication_reconcile_until TIMESTAMPTZ,
    publication_reconcile_wall_seconds INTEGER,
    publication_max_lookups INTEGER,
    publication_lookup_count INTEGER NOT NULL DEFAULT 0,
    publication_lookup_ticket JSONB,
    publication_closure_receipt_observed_at TIMESTAMPTZ,
    CONSTRAINT file_operations_pkey PRIMARY KEY (id),
    -- PRUNE-046 S6: the publication-evidence checks, moved verbatim with the
    -- columns renamed. The slot EXISTS iff publication_permit_id IS NOT NULL.
    CONSTRAINT file_operations_publication_slot_check CHECK ((publication_dispatch_state = 'prepared' AND publication_dispatch_started_at IS NULL AND publication_closed_proof IS NULL) OR (publication_dispatch_state = 'dispatching' AND publication_dispatch_started_at IS NOT NULL AND publication_closed_proof IS NULL) OR (publication_dispatch_state = 'closed' AND publication_closed_proof IS NOT NULL AND (publication_dispatch_started_at IS NOT NULL OR ((publication_closed_proof ->> 'kind') = 'no_dispatch') IS TRUE)) OR (publication_permit_id IS NULL AND publication_dispatch_state IS NULL)),
    CONSTRAINT file_operations_publication_slot_shape_check CHECK ((publication_permit_id IS NULL) = (publication_dispatch_state IS NULL)),
    CONSTRAINT file_operations_publication_identity_shape_check CHECK (publication_permit_id IS NULL OR (publication_effect_class IS NOT NULL AND publication_effect_key IS NOT NULL AND publication_spec_digest IS NOT NULL AND publication_plan_digest IS NOT NULL AND publication_dependency_digest IS NOT NULL AND publication_executor_session IS NOT NULL AND publication_work_item_id IS NOT NULL AND publication_fencing_token IS NOT NULL AND publication_owner_version IS NOT NULL AND publication_frozen_objects IS NOT NULL AND publication_step_checkpoint IS NOT NULL AND publication_step_proof_bytes IS NOT NULL AND publication_prepared_at IS NOT NULL AND publication_reconcile_wall_seconds IS NOT NULL AND publication_max_lookups IS NOT NULL)),
    CONSTRAINT file_operations_publication_prepared_effect_check CHECK (publication_dispatch_state <> 'prepared' OR publication_effect_class = 'unknown'),
    CONSTRAINT file_operations_publication_dispatch_started_check CHECK (publication_dispatch_started_at IS NULL OR publication_dispatch_started_at >= publication_prepared_at),
    CONSTRAINT file_operations_publication_unknown_window_check CHECK ((publication_unknown_since IS NULL AND publication_reconcile_until IS NULL) OR (publication_unknown_since IS NOT NULL AND publication_reconcile_until IS NOT NULL AND publication_reconcile_until >= publication_unknown_since)),
    CONSTRAINT file_operations_publication_lookup_budget_check CHECK (publication_lookup_count <= publication_max_lookups),
    CONSTRAINT file_operations_publication_reconcile_until_check CHECK (publication_reconcile_until IS NULL OR publication_reconcile_until = (publication_unknown_since + (publication_reconcile_wall_seconds::double precision * '00:00:01'::interval))),
    CONSTRAINT file_operations_publication_retry_safe_check CHECK (NOT publication_retry_safe OR (publication_dispatch_state = 'closed' AND publication_effect_class <> 'unknown')),
    CONSTRAINT file_operations_publication_attempt_check CHECK (publication_attempt BETWEEN 0 AND 100),
    CONSTRAINT file_operations_publication_closed_proof_check CHECK (publication_closed_proof IS NULL OR (jsonb_typeof(publication_closed_proof) = 'object' AND publication_closed_proof <> '{}'::jsonb AND octet_length(publication_closed_proof::text) <= 65536)),
    CONSTRAINT file_operations_publication_conflict_evidence_check CHECK (publication_conflict_evidence IS NULL OR (jsonb_typeof(publication_conflict_evidence) = 'object' AND publication_conflict_evidence <> '{}'::jsonb AND octet_length(publication_conflict_evidence::text) <= 65536)),
    CONSTRAINT file_operations_publication_conflict_count_check CHECK (publication_conflict_count >= 0),
    CONSTRAINT file_operations_publication_conflict_digest_check CHECK (publication_conflict_digest IS NULL OR publication_conflict_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT file_operations_publication_conflict_seen_check CHECK (jsonb_typeof(publication_conflict_seen) = 'object'),
    CONSTRAINT file_operations_publication_dependency_digest_check CHECK (publication_dependency_digest IS NULL OR publication_dependency_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT file_operations_publication_dispatch_state_check CHECK (publication_dispatch_state IS NULL OR publication_dispatch_state IN ('prepared', 'dispatching', 'closed')),
    CONSTRAINT file_operations_publication_effect_class_check CHECK (publication_effect_class IS NULL OR publication_effect_class IN ('unknown', 'no_external_effect', 'confirmed')),
    CONSTRAINT file_operations_publication_effect_key_check CHECK (publication_effect_key IS NULL OR char_length(publication_effect_key) BETWEEN 1 AND 512),
    CONSTRAINT file_operations_publication_executor_session_check CHECK (publication_executor_session IS NULL OR char_length(publication_executor_session) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_publication_fencing_token_check CHECK (publication_fencing_token IS NULL OR publication_fencing_token >= 1),
    CONSTRAINT file_operations_publication_frozen_objects_check CHECK (publication_frozen_objects IS NULL OR (jsonb_typeof(publication_frozen_objects) = 'object' AND octet_length(publication_frozen_objects::text) <= 524288)),
    CONSTRAINT file_operations_publication_permit_id_check CHECK (publication_permit_id IS NULL OR char_length(publication_permit_id) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_publication_lookup_count_check CHECK (publication_lookup_count BETWEEN 0 AND 100),
    CONSTRAINT file_operations_publication_lookup_ticket_check CHECK (publication_lookup_ticket IS NULL OR (jsonb_typeof(publication_lookup_ticket) = 'object' AND publication_lookup_ticket <> '{}'::jsonb AND octet_length(publication_lookup_ticket::text) <= 65536)),
    CONSTRAINT file_operations_publication_max_lookups_check CHECK (publication_max_lookups IS NULL OR publication_max_lookups BETWEEN 1 AND 100),
    CONSTRAINT file_operations_publication_owner_version_check CHECK (publication_owner_version IS NULL OR publication_owner_version >= 1),
    CONSTRAINT file_operations_publication_plan_digest_check CHECK (publication_plan_digest IS NULL OR publication_plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT file_operations_publication_reconcile_wall_seconds_check CHECK (publication_reconcile_wall_seconds IS NULL OR publication_reconcile_wall_seconds BETWEEN 1 AND 604800),
    CONSTRAINT file_operations_publication_resolution_proof_check CHECK (publication_resolution_proof IS NULL OR (jsonb_typeof(publication_resolution_proof) = 'object' AND publication_resolution_proof <> '{}'::jsonb AND octet_length(publication_resolution_proof::text) <= 65536)),
    CONSTRAINT file_operations_publication_spec_digest_check CHECK (publication_spec_digest IS NULL OR publication_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT file_operations_publication_step_checkpoint_check CHECK (publication_step_checkpoint IS NULL OR (jsonb_typeof(publication_step_checkpoint) = 'object' AND octet_length(publication_step_checkpoint::text) <= 524288)),
    CONSTRAINT file_operations_publication_step_proof_bytes_check CHECK (publication_step_proof_bytes IS NULL OR jsonb_typeof(publication_step_proof_bytes) = 'object'),
    CONSTRAINT file_operations_publication_work_item_id_check CHECK (publication_work_item_id IS NULL OR char_length(publication_work_item_id) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_id_acquisition_file_ref_uq UNIQUE (id, acquisition_id, file_ref),
    CONSTRAINT file_operations_id_acquisition_uq UNIQUE (id, acquisition_id),
    CONSTRAINT file_operations_id_idempotency_key_key UNIQUE (id, idempotency_key),
    CONSTRAINT file_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT file_operations_acquisition_fk FOREIGN KEY (acquisition_id) REFERENCES acquisitions (id),
    CONSTRAINT file_operations_artifact_file_id_artifact_id_source_pat_fkey FOREIGN KEY (artifact_file_id, artifact_id, source_path) REFERENCES artifact_files (id, artifact_id, source_path),
    CONSTRAINT file_operations_artifact_same_root_fk FOREIGN KEY (artifact_id, acquisition_id) REFERENCES artifacts (id, acquisition_id),
    CONSTRAINT file_operations_plan_root_fk FOREIGN KEY (postprocessing_plan_id, acquisition_id) REFERENCES postprocessing_plans (id, acquisition_id),
    CONSTRAINT file_operations_requirement_file_fk FOREIGN KEY (acquisition_id, file_ref, required_tag) REFERENCES acquisition_files (acquisition_id, file_ref, requirement),
    CONSTRAINT file_attempt_budget_shape CHECK ((budget_clock_at >= file_ready_at AND (file_attempts_started >= 0 AND file_attempts_started <= file_max_attempts) AND jsonb_typeof(file_attempt_index) = 'array' AND jsonb_array_length(file_attempt_index) = file_attempts_started AND (file_attempt_ticket IS NULL OR jsonb_typeof(file_attempt_ticket) = 'object')) IS TRUE),
    CONSTRAINT file_operations_acquisition_file_ref_pairing_check CHECK ((acquisition_id IS NULL) = (file_ref IS NULL)),
    CONSTRAINT file_operations_acquisition_id_check CHECK (acquisition_id IS NULL OR char_length(acquisition_id) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_artifact_version_check CHECK (artifact_version >= 1),
    CONSTRAINT file_operations_bound_video_not_self_check CHECK (bound_video_operation_id IS NULL OR bound_video_operation_id <> id),
    CONSTRAINT file_operations_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_check CHECK (source_path <> target_path),
    CONSTRAINT file_operations_collision_policy_check CHECK (collision_policy IN ('block', 'version', 'overwrite')),
    CONSTRAINT file_operations_completion_evidence_check CHECK (state <> 'completed' OR (observed_at IS NOT NULL AND (target_fingerprint IS NOT NULL OR (expected_size_bytes IS NOT NULL AND observed_target_size_bytes IS NOT NULL AND expected_size_bytes = observed_target_size_bytes)) AND (intent <> 'replace' OR publication_checkpoint IS NOT NULL))),
    CONSTRAINT file_operations_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_expected_size_bytes_check CHECK (expected_size_bytes IS NULL OR expected_size_bytes >= 0),
    CONSTRAINT file_operations_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_failure_reason_check CHECK (failure_reason IS NULL OR char_length(failure_reason) <= 2048),
    CONSTRAINT file_operations_file_ref_check CHECK (file_ref IS NULL OR char_length(file_ref) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_frozen_config_snapshot_check CHECK (jsonb_typeof(frozen_config_snapshot) = 'object' AND pg_column_size(frozen_config_snapshot) <= 1048576),
    CONSTRAINT file_operations_frozen_render_snapshot_check CHECK (jsonb_typeof(frozen_render_snapshot) = 'object' AND pg_column_size(frozen_render_snapshot) <= 1048576),
    CONSTRAINT file_operations_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT file_operations_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT file_operations_intent_check CHECK (intent IN ('ensure_present', 'replace', 'explicit')),
    CONSTRAINT file_operations_observed_target_size_bytes_check CHECK (observed_target_size_bytes IS NULL OR observed_target_size_bytes >= 0),
    CONSTRAINT file_operations_operation_check CHECK (operation IN ('copy', 'move', 'hardlink')),
    CONSTRAINT file_operations_overwrite_execution_snapshot_check CHECK ((collision_policy <> 'overwrite' AND overwrite_execution_snapshot IS NULL) OR (collision_policy = 'overwrite' AND (overwrite_execution_snapshot IS NULL OR (jsonb_typeof(overwrite_execution_snapshot) = 'object' AND pg_column_size(overwrite_execution_snapshot) <= 65536 AND jsonb_typeof((overwrite_execution_snapshot -> 'protocol_version')) = 'string' AND (overwrite_execution_snapshot ->> 'protocol_version') = 'overwrite-execution-v1' AND jsonb_typeof((overwrite_execution_snapshot -> 'operation_id')) = 'string' AND (overwrite_execution_snapshot ->> 'operation_id') = id AND jsonb_typeof((overwrite_execution_snapshot -> 'plan_digest')) = 'string' AND (overwrite_execution_snapshot ->> 'plan_digest') ~ '^[0-9a-f]{64}$' AND jsonb_typeof((overwrite_execution_snapshot -> 'recovery')) = 'object')))),
    CONSTRAINT file_operations_publication_checkpoint_check CHECK (publication_checkpoint IS NULL OR (jsonb_typeof(publication_checkpoint) = 'object' AND pg_column_size(publication_checkpoint) <= 65536)),
    CONSTRAINT file_operations_ready_shape CHECK (file_wall_until > file_ready_at AND file_max_attempts BETWEEN 1 AND 100 AND file_attempt_timeout_seconds BETWEEN 1 AND 86400),
    CONSTRAINT file_operations_render_episode_titles_check CHECK (render_episode_titles_valid(frozen_render_snapshot)),
    CONSTRAINT file_operations_required_tag_check CHECK (required_tag = 'required'),
    CONSTRAINT file_operations_source_fingerprint_check CHECK (jsonb_typeof(source_fingerprint) = 'object' AND pg_column_size(source_fingerprint) <= 262144),
    CONSTRAINT file_operations_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096 AND source_path !~ '[[:cntrl:]]'),
    CONSTRAINT file_operations_state_check CHECK (state IN ('pending', 'running', 'completed', 'waiting_retry', 'failed')),
    CONSTRAINT file_operations_target_fingerprint_check CHECK (target_fingerprint IS NULL OR (jsonb_typeof(target_fingerprint) = 'object' AND pg_column_size(target_fingerprint) <= 262144)),
    CONSTRAINT file_operations_target_path_check CHECK (char_length(btrim(target_path)) BETWEEN 1 AND 4096 AND target_path !~ '[[:cntrl:]]'),
    CONSTRAINT file_operations_version_check CHECK (version >= 1)
);

ALTER TABLE acquisition_files
    ADD CONSTRAINT acquisition_files_plan_operation_fk FOREIGN KEY (plan_operation_ref, acquisition_id, file_ref) REFERENCES file_operations (id, acquisition_id, file_ref) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE file_operations
    ADD CONSTRAINT file_operations_bound_video_operation_id_fkey FOREIGN KEY (bound_video_operation_id) REFERENCES file_operations (id);

CREATE INDEX file_operations_acquisition_file_idx ON file_operations (acquisition_id, file_ref)
    WHERE acquisition_id IS NOT NULL;
CREATE INDEX file_operations_artifact_idx ON file_operations (artifact_id, state);
CREATE INDEX file_operations_correlation_idx ON file_operations (correlation_id, created_at, id)
    WHERE correlation_id IS NOT NULL;
-- The unfinished operations are the file manager's in-progress owners; they are a
-- tiny slice of the table.
CREATE INDEX file_operations_inflight_source_idx ON file_operations (source_path)
    WHERE state IN ('pending', 'running', 'waiting_retry');
CREATE INDEX file_operations_inflight_target_idx ON file_operations (target_path)
    WHERE state IN ('pending', 'running', 'waiting_retry');
CREATE UNIQUE INDEX file_operations_publication_permit_uq ON file_operations (publication_permit_id)
    WHERE publication_permit_id IS NOT NULL;

COMMENT ON COLUMN file_operations.expected_size_bytes IS 'Product completion size; NULL only for legacy rows before target-based acquisition.';
COMMENT ON COLUMN file_operations.publication_checkpoint IS 'Durable publication evidence required to complete a replacement independently of incumbent size.';

-- PRUNE-046 S6: `file_publication_evidence` is gone. Its columns now live on
-- `file_operations` (publication_*), one current slot per operation.

-- `file_publication_bindings` is dropped: the permit now lives on the operation
-- row itself, so the binding is the row identity.

CREATE FUNCTION acquisition_unresolved_effect(p_root text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT EXISTS(
  SELECT 1 FROM public.work_items w
  WHERE w.kind <> 'acquisition_settle' AND w.state = 'leased'
    AND w.external_effect_admitted_at IS NOT NULL AND w.lease_until > clock_timestamp()
    AND w.acquisition_id = p_root)
$$;

CREATE FUNCTION local_cleanup_authorized(p_managed_anime_id text) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
    -- True only inside the cleanup_managed_anime_local_data transaction for
    -- this retired owner (the flag is transaction-local).
    SELECT p_managed_anime_id IS NOT NULL
       AND current_setting('bansoko.local_cleanup_owner', true) IS NOT DISTINCT FROM p_managed_anime_id
       AND EXISTS (SELECT 1 FROM managed_anime anime
                   WHERE anime.id = p_managed_anime_id AND anime.lifecycle_state = 'retired')
$$;

CREATE FUNCTION acquisition_compact(p_root text, p_trigger text, p_now timestamp with time zone DEFAULT NULL::timestamp with time zone) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE r public.acquisitions%ROWTYPE; v_owner text; affected bigint; v_outcomes bigint; detail jsonb:='{}'::jsonb;
 tasks text[]; arts text[]; fops text[]; plans text[]; works text[];
BEGIN
 IF p_trigger IS NULL OR p_trigger NOT IN ('detail_expired','owner_cleanup') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_COMPACT_TRIGGER'; END IF;
 SELECT * INTO r FROM public.acquisitions WHERE id=p_root FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_COMPACT_ROOT_MISSING'; END IF;
 IF r.record_state='compact' THEN RETURN jsonb_build_object('status','already_compact'); END IF;
 IF r.state='running' THEN RETURN jsonb_build_object('status','running'); END IF;
 v_owner:=public.acquisition_owner(p_root);
 IF p_trigger='detail_expired' AND r.detail_until>COALESCE(p_now,clock_timestamp()) THEN
  RETURN jsonb_build_object('status','detail_retained'); END IF;
 IF p_trigger='owner_cleanup' AND NOT public.local_cleanup_authorized(v_owner) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ACQ_COMPACT_UNAUTHORIZED'; END IF;

 SELECT COALESCE(array_agg(id),'{}') INTO tasks FROM public.download_tasks WHERE acquisition_id=p_root;
 SELECT COALESCE(array_agg(id),'{}') INTO arts FROM public.artifacts WHERE acquisition_id=p_root;
 SELECT COALESCE(array_agg(id),'{}') INTO fops FROM public.file_operations WHERE acquisition_id=p_root;
 SELECT COALESCE(array_agg(id),'{}') INTO plans FROM public.postprocessing_plans WHERE acquisition_id=p_root;
 SELECT COALESCE(array_agg(w.id),'{}') INTO works FROM public.work_items w
  WHERE w.acquisition_id=p_root;

 IF p_trigger='detail_expired' AND cardinality(works)>0 THEN
  RETURN jsonb_build_object('status','live_work'); END IF;
 IF p_trigger='owner_cleanup' AND public.acquisition_unresolved_effect(p_root) THEN
  RETURN jsonb_build_object('status','effect_unresolved'); END IF;
 IF EXISTS(SELECT 1 FROM public.placement_obligations o WHERE o.file_operation_id=ANY(fops))
 THEN
  RETURN jsonb_build_object('status','dependents_present'); END IF;
 -- A Release that still points at the root's task keeps the root full unless
 -- this transaction deletes that Release: an authorized owner cleanup deletes
 -- every Release of the retired owner, and a one-shot reclaim (marker in
 -- reclaiming) deletes its own Release. Nothing is detached here.
 IF EXISTS(SELECT 1 FROM public.releases rel WHERE rel.download_task_id=ANY(tasks)
     AND NOT (p_trigger='owner_cleanup' AND EXISTS(
          SELECT 1 FROM public.rss_sources src JOIN public.subscriptions sub ON sub.id=src.subscription_id
          WHERE src.id=rel.rss_source_id AND sub.managed_anime_id=v_owner))
     AND NOT EXISTS(SELECT 1 FROM public.one_shot_acquisitions m WHERE m.release_id=rel.id AND m.state='reclaiming')) THEN
  RETURN jsonb_build_object('status','release_bound'); END IF;

 UPDATE public.acquisitions SET record_state='compact', compacted_at=clock_timestamp(),
   compact_trigger=p_trigger, completion_open=false, version=version+1 WHERE id=p_root;

 DELETE FROM public.work_items WHERE id=ANY(works);
 GET DIAGNOSTICS affected=ROW_COUNT;
 DELETE FROM public.work_item_outcomes WHERE acquisition_id=p_root;
 GET DIAGNOSTICS v_outcomes=ROW_COUNT; affected:=affected+v_outcomes;
 IF affected>0 THEN detail:=detail||jsonb_build_object('work_items',affected); END IF;
 UPDATE public.acquisition_files SET plan_kind=NULL,plan_target_file_ref=NULL,plan_operation_ref=NULL,plan_evidence=NULL,
   plan_reason_code=NULL,plan_diagnostic=NULL WHERE acquisition_id=p_root;
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('plan_items',affected); END IF;
 DELETE FROM public.file_operations WHERE id=ANY(fops);
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('file_operations',affected); END IF;
 DELETE FROM public.postprocessing_plans WHERE id=ANY(plans);
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('postprocessing_plans',affected); END IF;
 DELETE FROM public.artifact_files WHERE acquisition_id=p_root;
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('artifact_files',affected); END IF;
 DELETE FROM public.artifacts WHERE id=ANY(arts);
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('artifacts',affected); END IF;
 DELETE FROM public.download_tasks WHERE id=ANY(tasks);
 GET DIAGNOSTICS affected=ROW_COUNT; IF affected>0 THEN detail:=detail||jsonb_build_object('download_tasks',affected); END IF;
 RETURN jsonb_build_object('status','compacted','deleted',detail);
END $$;

CREATE FUNCTION acquisition_live_work(p_root text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT EXISTS(
  SELECT 1 FROM public.work_items w
  WHERE w.kind <> 'acquisition_settle'
    AND (w.state = 'available' OR (w.state = 'leased' AND (w.external_effect_admitted_at IS NULL OR w.lease_until > clock_timestamp())))
    AND w.acquisition_id = p_root)
$$;

CREATE FUNCTION acquisition_unknown_files(p_root text) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH root AS (
  SELECT rtrim(btrim(value #>> '{}'),'/') AS path FROM public.global_config_sections WHERE section_name='library_root'
 ), files AS (
  SELECT CASE WHEN root.path <> '' AND left(fo.target_path,length(root.path)+1)=root.path||'/'
              THEN substring(fo.target_path FROM length(root.path)+2) END AS relative,
         COALESCE(CASE WHEN jsonb_typeof(fo.target_fingerprint->'size_bytes')='number' THEN (fo.target_fingerprint->>'size_bytes')::bigint END,
                  af.size_bytes) AS size
  FROM public.file_operations fo
  LEFT JOIN public.artifact_files af ON af.artifact_id=fo.artifact_id AND af.file_ref=fo.file_ref
  LEFT JOIN root ON TRUE
  WHERE fo.acquisition_id=p_root AND fo.state='completed'
    AND fo.frozen_render_snapshot->'unknown_file'='true'::jsonb
    AND COALESCE(af.media_kind,'video')='video'
 )
 SELECT COALESCE(jsonb_agg(jsonb_build_object('path',public.file_notification_text(relative),'size',size) ORDER BY relative),'[]'::jsonb)
 FROM files
$$;

CREATE FUNCTION artifact_recovery_lock(p_artifact text, p_work text, p_token bigint) RETURNS artifacts
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE;
 r public.acquisitions%ROWTYPE; root_id text; task_id text;
BEGIN
 SELECT acquisition_id,download_task_id INTO STRICT root_id,task_id FROM public.artifacts WHERE id=p_artifact;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 PERFORM id FROM public.download_tasks WHERE id=task_id AND acquisition_id=root_id FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=p_artifact FOR UPDATE;
 PERFORM id FROM public.work_items WHERE id=p_work OR (acquisition_id=root_id AND state IN ('available','leased')) ORDER BY id COLLATE "C" FOR UPDATE;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF r.state IS DISTINCT FROM 'running' OR r.record_state IS DISTINCT FROM 'full'
 OR a.acquisition_id IS DISTINCT FROM root_id OR a.download_task_id IS DISTINCT FROM task_id
 OR w.acquisition_id IS DISTINCT FROM root_id
 OR w.owner_type IS DISTINCT FROM 'artifact' OR w.owner_id IS DISTINCT FROM a.id
 OR w.kind IS DISTINCT FROM 'artifact_inspect' OR w.state IS DISTINCT FROM 'leased'
 OR w.fencing_token IS DISTINCT FROM p_token OR w.expected_owner_version IS DISTINCT FROM a.version
 OR (w.lease_until>clock_timestamp()) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 RETURN a;
END $$;

CREATE FUNCTION artifact_commit_inspection(p_artifact text, p_work text, p_token bigint, p_admission_digest text, p_manifest_digest text, p_manifest_files jsonb, p_inventory jsonb, p_fingerprints jsonb, p_files jsonb, p_plan text, p_plan_work text, p_plan_digest text, p_media_snapshot text, p_media_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE; m_manifest_digest text; v bigint; inherited jsonb; plan_work text;
BEGIN
 a:=public.artifact_recovery_lock(p_artifact,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.cancel_requested OR w.recovery_blocked OR a.state NOT IN ('pending','waiting_retry')
 OR (SELECT state FROM public.download_tasks WHERE id=a.download_task_id) IS DISTINCT FROM 'completed'
 OR p_admission_digest IS DISTINCT FROM (SELECT admission_digest FROM public.acquisitions WHERE id=a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 IF jsonb_typeof(p_files) IS DISTINCT FROM 'array' OR jsonb_array_length(p_files) NOT BETWEEN 1 AND 10000
 OR jsonb_typeof(p_manifest_files) IS DISTINCT FROM 'array' OR octet_length(p_manifest_files::text)>16777216
 OR jsonb_typeof(p_inventory) IS DISTINCT FROM 'object' OR jsonb_typeof(p_fingerprints) IS DISTINCT FROM 'object'
 OR p_plan IS NULL OR p_plan_work IS NULL OR p_plan_digest !~ '^sha256:[0-9a-f]{64}$' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_INSPECTION_SHAPE'; END IF;
 -- Prepared observation references must bind to the exact submitted file set.
 IF p_inventory->>'schema_version' IS DISTINCT FROM 'artifact-inspect/v1'
 OR p_fingerprints->>'schema_version' IS DISTINCT FROM 'artifact-fingerprint/v1' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_SNAPSHOT_BINDING'; END IF;
 IF jsonb_typeof(p_inventory->'files') IS DISTINCT FROM 'array'
 OR jsonb_typeof(p_inventory->'inspected_files') IS DISTINCT FROM 'array'
 OR jsonb_typeof(p_fingerprints->'files') IS DISTINCT FROM 'object'
 OR jsonb_typeof(p_fingerprints->'inspected_files') IS DISTINCT FROM 'array' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_SNAPSHOT_BINDING'; END IF;
 -- Compare sorted complete values: array order is immaterial, multiplicity is not.
 IF (SELECT count(DISTINCT f->>'file_ref') FROM jsonb_array_elements(p_files) f)<>jsonb_array_length(p_files)
 OR (SELECT jsonb_agg(f ORDER BY f::text COLLATE "C") FROM jsonb_array_elements(p_inventory->'files') f)
 IS DISTINCT FROM (SELECT jsonb_agg(f-'id' ORDER BY (f-'id')::text COLLATE "C") FROM jsonb_array_elements(p_files) f)
 OR (SELECT jsonb_agg(f ORDER BY f::text COLLATE "C") FROM jsonb_array_elements(p_inventory->'inspected_files') f)
 IS DISTINCT FROM (SELECT jsonb_agg(f-'id' ORDER BY (f-'id')::text COLLATE "C") FROM jsonb_array_elements(p_files) f)
 OR p_inventory->>'primary_file_ref' IS DISTINCT FROM p_fingerprints->>'primary_file_ref'
 OR p_inventory->'inspected_primary_file'->>'file_ref' IS DISTINCT FROM p_inventory->>'primary_file_ref'
 OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_files) f WHERE f-'id'=p_inventory->'inspected_primary_file')
 OR (SELECT jsonb_object_agg(f->>'file_ref',f->'fingerprint') FROM jsonb_array_elements(p_files) f) IS DISTINCT FROM p_fingerprints->'files'
 OR (SELECT jsonb_agg(f ORDER BY f::text COLLATE "C") FROM jsonb_array_elements(p_fingerprints->'inspected_files') f)
 IS DISTINCT FROM (SELECT jsonb_agg(projected ORDER BY projected::text COLLATE "C") FROM (
 SELECT jsonb_build_object('file_ref',f->'file_ref','relative_path',f->'relative_path','source_path',f->'source_path','fingerprint',f->'fingerprint') projected FROM jsonb_array_elements(p_files) f) expected) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_SNAPSHOT_BINDING'; END IF;
 SELECT manifest_digest INTO m_manifest_digest FROM public.acquisitions WHERE id=a.acquisition_id;
 IF m_manifest_digest IS NOT NULL THEN
 IF m_manifest_digest IS DISTINCT FROM p_manifest_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
 ELSE
INSERT INTO public.acquisition_files(acquisition_id,file_ref,ordinal,relative_path,exact_size,media_kind,identity_evidence)
SELECT a.acquisition_id,x.file_ref,x.ordinal,x.relative_path,x.exact_size,x.media_kind,x.identity_evidence
FROM jsonb_to_recordset(p_manifest_files) AS x(file_ref text,ordinal integer,relative_path text,exact_size bigint,media_kind text,identity_evidence jsonb);
 UPDATE public.acquisitions SET manifest_digest=p_manifest_digest,manifest_item_count=jsonb_array_length(p_manifest_files),manifest_sealed_at=CURRENT_TIMESTAMP WHERE id=a.acquisition_id;
 END IF;
 -- Compare the complete observed set to the immutable manifest in both directions.
IF EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=a.acquisition_id AND NOT EXISTS(
SELECT 1 FROM jsonb_to_recordset(p_files) x(file_ref text,relative_path text,size_bytes bigint) WHERE x.file_ref=f.file_ref AND x.relative_path=f.relative_path AND x.size_bytes=f.exact_size))
OR EXISTS(SELECT 1 FROM jsonb_to_recordset(p_files) x(file_ref text,relative_path text,size_bytes bigint) WHERE NOT EXISTS(
SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=a.acquisition_id AND f.file_ref=x.file_ref AND f.relative_path=x.relative_path AND f.exact_size=x.size_bytes))
OR jsonb_array_length(p_files)<>(SELECT count(*) FROM public.acquisition_files WHERE acquisition_id=a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MANIFEST_CONFLICT'; END IF;
 -- Old observations may be replaced only before any downstream commitment.
 IF EXISTS(SELECT 1 FROM public.artifact_files WHERE artifact_id=p_artifact AND acquisition_id IS DISTINCT FROM a.acquisition_id)
 OR EXISTS(SELECT 1 FROM public.postprocessing_plans WHERE artifact_id=p_artifact)
 OR EXISTS(SELECT 1 FROM public.file_operations WHERE artifact_id=p_artifact)
 OR EXISTS(SELECT 1 FROM public.artifact_files f JOIN public.artifact_files consumer ON consumer.artifact_id=f.artifact_id AND consumer.bound_video_file_ref=f.file_ref WHERE f.artifact_id=p_artifact AND consumer.acquisition_id IS DISTINCT FROM a.acquisition_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_DOWNSTREAM_COMMITMENT'; END IF;
 DELETE FROM public.artifact_files WHERE artifact_id=p_artifact;
 INSERT INTO public.artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
 SELECT x.id,p_artifact,a.acquisition_id,x.file_ref,x.relative_path,x.source_path,x.media_kind,x.size_bytes,x.fingerprint,x.facts,
 CASE WHEN x.media_kind='subtitle' THEN 'missing' ELSE 'not_applicable' END
 FROM jsonb_to_recordset(p_files) x(id text,file_ref text,relative_path text,source_path text,media_kind text,size_bytes bigint,fingerprint jsonb,facts jsonb);
 IF jsonb_typeof(p_media_files) IS DISTINCT FROM 'array' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_SHAPE'; END IF;
 IF p_media_snapshot<>'' THEN
 IF p_media_snapshot IS DISTINCT FROM (SELECT snap.id FROM public.media_fact_snapshots snap JOIN public.download_tasks t ON t.release_id=snap.release_id WHERE t.id=a.download_task_id ORDER BY snap.created_at DESC,snap.id DESC LIMIT 1)
 OR jsonb_array_length(p_media_files)<>jsonb_array_length(p_files) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 PERFORM id FROM public.media_fact_snapshots WHERE id=p_media_snapshot FOR SHARE;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(p_media_files) prepared WHERE prepared->'snapshot_binding' IS DISTINCT FROM (
 SELECT jsonb_build_object('release_facts',snap.release_facts,'release_identity',snap.release_identity,'parser_kind',snap.parser_kind,'parser_config',snap.parser_config,'parser_config_fingerprint',snap.parser_config_fingerprint) FROM public.media_fact_snapshots snap WHERE snap.id=p_media_snapshot))
 OR (SELECT count(DISTINCT prepared->>'file_ref') FROM jsonb_array_elements(p_media_files) prepared)<>jsonb_array_length(p_media_files)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_media_files) prepared WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p_files) f WHERE f->>'file_ref'=prepared->>'file_ref' AND f->'size_bytes'=prepared->'size_bytes' AND f->'media_kind'=prepared->'media_kind' AND f->'fingerprint'->>'value'=prepared->>'source_fingerprint')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 INSERT INTO public.media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
 SELECT p_media_snapshot,x.file_ref,x.media_kind,x.size_bytes,x.source_fingerprint,x.file_evidence,x.effective_facts
 FROM jsonb_to_recordset(p_media_files) x(file_ref text,media_kind text,size_bytes bigint,source_fingerprint text,file_evidence jsonb,effective_facts jsonb)
 ON CONFLICT(snapshot_id,file_ref) DO UPDATE SET media_kind=EXCLUDED.media_kind,size_bytes=EXCLUDED.size_bytes,source_fingerprint=EXCLUDED.source_fingerprint,file_evidence=EXCLUDED.file_evidence,effective_facts=EXCLUDED.effective_facts;
 ELSIF jsonb_array_length(p_media_files)<>0 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_MEDIA_BINDING'; END IF;
 UPDATE public.artifacts SET state='verified',inventory_snapshot=p_inventory,fingerprint_snapshot=p_fingerprints,verified_at=CURRENT_TIMESTAMP,
 failure_code=NULL,failure_reason=NULL,inspection_requirements_status=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 inherited:=CASE WHEN w.input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',w.input_snapshot->'one_shot') ELSE '{}'::jsonb END;
 INSERT INTO public.postprocessing_plans(id,acquisition_id,artifact_id,artifact_version,one_shot_id,input_digest,state)
 VALUES(p_plan,a.acquisition_id,p_artifact,v,w.input_snapshot->'one_shot'->>'marker_id',p_plan_digest,'queued');
 SELECT public.work_item_create(p_plan_work,'postprocessing_plan',p_plan,'postprocess_plan','plan:'||p_plan,5,inherited,1::bigint,0::double precision,7::smallint) INTO plan_work;
 IF plan_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_PLAN_WORK_CONFLICT'; END IF;
 PERFORM public.work_item_finish('succeeded',v,p_work);
 PERFORM public.download_ensure_settlement(a.acquisition_id);
 RETURN jsonb_build_object('state','verified','reason_code','artifact_verified','artifact_version',v);
END $_$;

CREATE FUNCTION artifact_inspect_fail(p_artifact text, p_work text, p_code text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; v bigint;
BEGIN
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=p_artifact;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>2048 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_FAILURE_SHAPE'; END IF;
 UPDATE public.artifacts SET state='failed',failure_code=p_code,failure_reason=NULLIF(p_reason,''),verified_at=NULL,
 inspection_requirements_status=CASE WHEN (SELECT r.requirements_digest IS NOT NULL FROM public.acquisitions r WHERE r.id=a.acquisition_id) THEN 'incomplete' ELSE 'not_reached' END,
 version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 PERFORM public.work_item_fail_at_attempt_budget(v,p_code,p_reason,p_work,a.version);
 PERFORM public.download_ensure_settlement(a.acquisition_id);
 RETURN v;
END $$;

CREATE FUNCTION artifact_inspect_failed(p_artifact text, p_work text, p_token bigint, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE;
BEGIN
 a:=public.artifact_recovery_lock(p_artifact,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.recovery_blocked OR w.cancel_requested OR a.state NOT IN ('pending','waiting_retry') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 PERFORM public.artifact_inspect_fail(p_artifact,p_work,p_code,p_reason);
 RETURN jsonb_build_object('state','failed','reason_code',p_code);
END $$;

CREATE FUNCTION work_item_requeue_recovered_inspection(p_id text, p_token bigint, p_owner_id text, p_next_owner_version bigint, p_delay_seconds double precision, p_reason_code text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE w public.work_item_history%ROWTYPE;
BEGIN
 SELECT * INTO w FROM public.work_items WHERE id=p_id FOR UPDATE;
 IF NOT FOUND THEN SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_id; END IF;
 IF w.cancel_requested THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_INSPECT_REQUEUE_CANCELLED'; END IF;
 IF w.kind IS DISTINCT FROM 'artifact_inspect' OR w.owner_type IS DISTINCT FROM 'artifact'
 OR w.owner_id IS DISTINCT FROM p_owner_id OR w.state IS DISTINCT FROM 'leased'
 OR w.fencing_token IS DISTINCT FROM p_token OR NOT w.recovery_blocked
 OR (w.lease_until>clock_timestamp()) IS NOT TRUE OR w.attempt>=w.max_attempts
 OR p_reason_code IS DISTINCT FROM 'ARTIFACT_INSPECT_INTERRUPTED'
 OR p_delay_seconds IS NULL OR p_delay_seconds<0 OR p_delay_seconds>86400
 OR p_next_owner_version IS DISTINCT FROM (SELECT version FROM public.artifacts WHERE id=p_owner_id) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 UPDATE public.work_items SET state='available',available_at=CURRENT_TIMESTAMP+make_interval(secs=>p_delay_seconds),
 lease_until=NULL,recovery_blocked=false,recovery_reason=NULL,
 fencing_token=fencing_token+1,expected_owner_version=p_next_owner_version,last_error_code=p_reason_code,
 last_error_reason='read-only inspection was interrupted',updated_at=CURRENT_TIMESTAMP WHERE id=p_id;
END $$;

CREATE FUNCTION artifact_inspect_requeue_interrupted(p_artifact text, p_work text, p_token bigint, p_delay_seconds double precision) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE; v bigint;
BEGIN
 a:=public.artifact_recovery_lock(p_artifact,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF NOT w.recovery_blocked OR a.state NOT IN ('pending','waiting_retry') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 IF w.cancel_requested THEN
 v:=public.artifact_inspect_fail(p_artifact,p_work,'ARTIFACT_INSPECT_CANCELLED','read-only inspection was cancelled');
 RETURN jsonb_build_object('state','failed','reason_code','ARTIFACT_INSPECT_CANCELLED');
 ELSIF w.attempt>=w.max_attempts THEN
 v:=public.artifact_inspect_fail(p_artifact,p_work,'ARTIFACT_INSPECT_INTERRUPTED','read-only inspection was interrupted');
 RETURN jsonb_build_object('state','failed','reason_code','ARTIFACT_INSPECT_INTERRUPTED');
 END IF;
 UPDATE public.artifacts SET state='waiting_retry',failure_code='ARTIFACT_INSPECT_INTERRUPTED',
 failure_reason='read-only inspection was interrupted',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 PERFORM public.work_item_requeue_recovered_inspection(p_work,p_token,p_artifact,v,p_delay_seconds,'ARTIFACT_INSPECT_INTERRUPTED');
 RETURN jsonb_build_object('state','waiting_retry','reason_code','ARTIFACT_INSPECT_INTERRUPTED');
END $$;

CREATE FUNCTION artifact_inspect_retry(p_artifact text, p_work text, p_token bigint, p_code text, p_reason text, p_delay double precision) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts%ROWTYPE; w public.work_item_history%ROWTYPE; v bigint;
BEGIN
 a:=public.artifact_recovery_lock(p_artifact,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.recovery_blocked OR w.cancel_requested OR a.state NOT IN ('pending','waiting_retry')
 OR p_delay IS NULL OR p_delay<0 OR p_delay>86400 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ARTIFACT_STAGE_STALE_CLAIM'; END IF;
 IF w.attempt>=w.max_attempts THEN
 v:=public.artifact_inspect_fail(p_artifact,p_work,p_code,p_reason);
 RETURN jsonb_build_object('state','failed','reason_code',p_code); END IF;
 UPDATE public.artifacts SET state='waiting_retry',failure_code=p_code,failure_reason=NULLIF(p_reason,''),version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_artifact RETURNING version INTO v;
 PERFORM public.work_item_return_available(p_delay,v,p_code,p_reason,p_work,a.version);
 RETURN jsonb_build_object('state','waiting_retry','reason_code',p_code);
END $$;

SET default_tablespace = '';

SET default_table_access_method = heap;

CREATE FUNCTION artifact_start_attempt(p_artifact text, p_work text, p_token bigint, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE a public.artifacts; w public.work_item_history; now_at timestamptz; ticket jsonb; idx jsonb; old_entry jsonb; entry jsonb; ordinal integer;
BEGIN
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=p_artifact FOR UPDATE;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (w.owner_type='artifact' AND w.owner_id=a.id AND w.kind='artifact_inspect' AND w.state='leased'
  AND w.fencing_token=p_token AND w.expected_owner_version=a.version
  AND a.state IN ('pending','waiting_retry')) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_STAGE_STALE_CLAIM'; END IF;
 IF p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_SESSION_SHAPE'; END IF;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(a.inspect_attempt_index) LOOP
  IF old_entry->>'work_item_id'=p_work AND (old_entry->>'token')::bigint=p_token THEN
   RETURN jsonb_build_object('granted',false,'ticket',a.inspect_attempt_ticket,'reason_code','ATTEMPT_ALREADY_ISSUED');
  END IF;
 END LOOP;
 now_at:=GREATEST(clock_timestamp(),a.inspect_ready_at);
 IF now_at>=a.inspect_wall_until OR a.inspect_attempts_started>=a.inspect_max_attempts THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_BUDGET_EXHAUSTED'; END IF;
 idx:='[]'::jsonb;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(a.inspect_attempt_index) LOOP
  IF old_entry->>'state'='issued' THEN old_entry:=jsonb_set(old_entry,'{state}','"superseded"'); END IF;
  idx:=idx||jsonb_build_array(old_entry);
 END LOOP;
 ordinal:=a.inspect_attempts_started+1;
 entry:=jsonb_build_object('work_item_id',p_work,'token',p_token,'ordinal',ordinal,'session_digest','sha256:'||encode(sha256(convert_to(p_session,'UTF8')),'hex'),'state','issued');
 ticket:=(entry-'session_digest')||jsonb_build_object('executor_session',p_session,'started_at',now_at,'attempt_until',LEAST(now_at+make_interval(secs=>a.inspect_attempt_timeout_seconds),a.inspect_wall_until));
 UPDATE public.artifacts SET inspect_attempts_started=ordinal,inspect_attempt_index=idx||jsonb_build_array(entry),inspect_attempt_ticket=ticket WHERE id=a.id;
 RETURN jsonb_build_object('granted',true,'ticket',ticket);
END $$;

CREATE FUNCTION check_bound_video_is_video() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.bound_video_file_ref IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM artifact_files target
        WHERE target.artifact_id = NEW.artifact_id
          AND target.file_ref = NEW.bound_video_file_ref
          AND target.media_kind = 'video'
    ) THEN
        RAISE EXCEPTION 'bound video file % in artifact % is not a video artifact file',
            NEW.bound_video_file_ref, NEW.artifact_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION check_referenced_video_kind_unchanged() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.media_kind <> 'video' AND EXISTS (
        SELECT 1
        FROM artifact_files binder
        WHERE binder.artifact_id = NEW.artifact_id
          AND binder.bound_video_file_ref = NEW.file_ref
    ) THEN
        RAISE EXCEPTION 'artifact file % in artifact % is referenced as a bound video and cannot change media kind',
            NEW.file_ref, NEW.artifact_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION file_step_authorized_at(e file_operations, s text) RETURNS timestamp with time zone
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE s WHEN 'stage_temp' THEN e.publication_stage_temp_started_at
 WHEN 'reserve_version' THEN e.publication_reserve_version_started_at
 WHEN 'publish_target' THEN e.publication_publish_target_started_at
 WHEN 'source_cleanup' THEN e.publication_source_cleanup_started_at
 WHEN 'temp_cleanup' THEN e.publication_temp_cleanup_started_at END
$$;

CREATE FUNCTION file_validate_final_proof_before181(e file_operations) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE cp jsonb; cl jsonb; resolution jsonb;
 step text; slot jsonb; required boolean; covered jsonb:='[]'::jsonb;
BEGIN
 IF e.publication_dispatch_state IS DISTINCT FROM 'closed' OR e.publication_effect_class IS DISTINCT FROM 'confirmed'
 OR e.publication_conflict_evidence IS NOT NULL OR e.publication_conflict_index_exhausted
 OR public.plan_canonical_valid(e.publication_frozen_objects_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_resolution_checkpoint_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_closure_canonical) IS NOT TRUE
 OR public.plan_canonical_valid(e.publication_resolution_canonical) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_UNAVAILABLE'; END IF;
 cp:=e.publication_resolution_checkpoint_canonical::jsonb;cl:=e.publication_closure_canonical::jsonb;resolution:=e.publication_resolution_canonical::jsonb;
 IF cp IS DISTINCT FROM e.publication_step_checkpoint OR cl IS DISTINCT FROM e.publication_closed_proof
 OR resolution IS DISTINCT FROM e.publication_resolution_proof OR e.publication_frozen_objects_canonical::jsonb IS DISTINCT FROM e.publication_frozen_objects
 OR public.observed_closed(resolution,ARRAY['schema_version','effect_class','checkpoint_digest','closure_digests']) IS NOT TRUE
 OR resolution->'schema_version' IS DISTINCT FROM '1'::jsonb OR resolution->>'effect_class' IS DISTINCT FROM 'confirmed'
 OR resolution->>'checkpoint_digest' IS DISTINCT FROM public.file_proof_digest('file-checkpoint:v1',e.publication_resolution_checkpoint_canonical)
 OR resolution->'closure_digests' IS DISTINCT FROM jsonb_build_array(public.file_proof_digest('file-closure:v1',e.publication_closure_canonical)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_DIGEST_INVALID'; END IF;
 IF public.file_wire_id_valid(cl->'boot_nonce') IS NOT TRUE OR public.file_wire_id_valid(cl->'call_nonce') IS NOT TRUE
 OR public.file_wire_time_valid(cl->'joined_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
 slot:=cp->step;
 required:=NOT ((step='source_cleanup' AND e.operation<>'move') OR (step='reserve_version' AND e.collision_policy<>'version') OR (step IN ('stage_temp','temp_cleanup') AND e.operation='hardlink' AND e.collision_policy='block'));
 IF public.observed_closed(slot,ARRAY['state','proof','observed_at','reason_code']) IS NOT TRUE
 OR (required AND slot->>'state' IS DISTINCT FROM 'confirmed')
 OR (NOT required AND slot IS DISTINCT FROM jsonb_build_object('state','not_required','proof',NULL,'observed_at',NULL,'reason_code',NULL)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_STEP_INVALID'; END IF;
 IF required THEN
 IF public.plan_canonical_valid(e.publication_step_proof_bytes->>step) IS NOT TRUE OR (e.publication_step_proof_bytes->>step)::jsonb IS DISTINCT FROM slot->'proof' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_STEP_BYTES_INVALID'; END IF;
 IF public.file_wire_time_valid(slot->'observed_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF slot->'reason_code' IS DISTINCT FROM 'null'::jsonb
 OR ((slot->>'observed_at')::timestamptz>=e.publication_dispatch_started_at AND (slot->>'observed_at')::timestamptz<=(cl->>'joined_at')::timestamptz) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF (step='stage_temp' AND public.observed_closed(slot->'proof',ARRAY['kind','object','parent','verified_source_digest','flushed']) IS NOT TRUE)
 OR (step='publish_target' AND public.observed_closed(slot->'proof',ARRAY['kind','source_evidence_digest','target','parent','atomic_method','displaced','dependency_digest']) IS NOT TRUE)
 OR (step IN ('source_cleanup','temp_cleanup') AND (public.observed_closed(slot->'proof',ARRAY['kind','before','parent','after','mutation_receipt_digest']) IS NOT TRUE OR public.observed_closed(slot->'proof'->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_SHAPE_INVALID'; END IF;
 IF step='stage_temp' AND (slot->'proof'->>'kind' IS DISTINCT FROM 'created_temp'
 OR slot->'proof'->>'verified_source_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
 OR slot->'proof'->'flushed' IS DISTINCT FROM 'true'::jsonb
 OR slot->'proof'->'object'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TEMP_INVALID'; END IF;
 IF step='publish_target' AND (slot->'proof'->>'kind' IS DISTINCT FROM 'published'
 OR slot->'proof'->>'source_evidence_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
 OR slot->'proof'->>'dependency_digest' IS DISTINCT FROM e.publication_dependency_digest
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 IF step IN ('source_cleanup','temp_cleanup') AND (slot->'proof'->>'kind' IS DISTINCT FROM 'removed_object'
 OR slot->'proof'->'after'->>'method' IS DISTINCT FROM 'lstat_enoent'
 OR slot->'proof'->'parent' IS DISTINCT FROM CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source_parent' ELSE e.publication_frozen_objects->'target_parent' END
 OR slot->'proof'->'before' IS DISTINCT FROM CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source' WHEN e.collision_policy IN ('version','overwrite') THEN cp->'publish_target'->'proof'->'displaced'->'value' ELSE cp->'stage_temp'->'proof'->'object' END
 OR slot->'proof'->'after'->'parent' IS DISTINCT FROM slot->'proof'->'parent'
 OR slot->'proof'->'after'->'location' IS DISTINCT FROM slot->'proof'->'before'->'location'
 OR slot->'proof'->>'mutation_receipt_digest' IS DISTINCT FROM public.file_proof_digest('file-mutation-receipt:v1',
 '{"before":'||public.file_json_field((e.publication_step_proof_bytes->>step)::json,'before')||',"permit_id":'||to_json(e.publication_permit_id)::text||',"removed_at":'||to_json(slot->>'observed_at')::text||',"step":'||to_json(step)::text||'}')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLEANUP_INVALID'; END IF;
 IF step='reserve_version' AND (public.observed_closed(slot->'proof',ARRAY['kind','object','parent','base_path','candidate_ordinal','reservation_method']) IS NOT TRUE
 OR e.collision_policy IS DISTINCT FROM 'version' OR slot->'proof'->>'kind' IS DISTINCT FROM 'reserved_path'
 OR slot->'proof'->>'reservation_method' IS DISTINCT FROM 'exclusive_create'
 OR slot->'proof'->>'base_path' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'canonical_path'
 OR ((slot->'proof'->>'candidate_ordinal')::bigint BETWEEN 1 AND 1001) IS NOT TRUE
 OR slot->'proof'->'object'->'location'->>'canonical_path' IS DISTINCT FROM public.file_version_candidate(e.publication_frozen_objects->'target'->>'canonical_path',(slot->'proof'->>'candidate_ordinal')::bigint)
 OR public.file_location_valid(slot->'proof'->'object'->'location') IS NOT TRUE
 OR slot->'proof'->'object'->'location'->>'root' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'root'
 OR slot->'proof'->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
 OR slot->'proof'->'object'->>'file_type' IS DISTINCT FROM 'regular'
 OR slot->'proof'->'object'->'size' IS DISTINCT FROM '0'::jsonb
 OR slot->'proof'->'object'->'identity'->>'state' IS DISTINCT FROM 'known'
 OR public.file_wire_identity_valid(slot->'proof'->'object'->'identity'->'value') IS NOT TRUE
 OR slot->'proof'->'object'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_RESERVATION_INVALID'; END IF;
 IF step='publish_target' AND (slot->'proof'->>'atomic_method' IS DISTINCT FROM CASE WHEN e.collision_policy IN ('version','overwrite') THEN 'exchange' ELSE 'no_replace' END
 OR slot->'proof'->'target'->'location' IS DISTINCT FROM CASE WHEN e.collision_policy='version' THEN cp->'reserve_version'->'proof'->'object'->'location' ELSE e.publication_frozen_objects->'target' END
 OR (e.collision_policy='block' AND slot->'proof'->'displaced' IS DISTINCT FROM jsonb_build_object('state','absent','value',NULL,'reason_code',NULL))
 OR (e.collision_policy='version' AND (slot->'proof'->'displaced'->>'state' IS DISTINCT FROM 'known'
 OR slot->'proof'->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
 OR slot->'proof'->'displaced'->'value'->'identity'->'value'->>'inode' IS DISTINCT FROM cp->'reserve_version'->'proof'->'object'->'identity'->'value'->>'inode'
 OR slot->'proof'->'displaced'->'value'->'identity'->'value'->>'device' IS DISTINCT FROM cp->'reserve_version'->'proof'->'object'->'identity'->'value'->>'device'
 OR slot->'proof'->'displaced'->'value'->'size' IS DISTINCT FROM '0'::jsonb))) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 IF step IN ('stage_temp','publish_target') THEN
 DECLARE object jsonb:=CASE WHEN step='stage_temp' THEN slot->'proof'->'object' ELSE slot->'proof'->'target' END;
 BEGIN
 IF public.observed_closed(object,ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
 OR object->>'file_type' IS DISTINCT FROM 'regular' OR object->'size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size'
 OR object->'identity'->>'state' IS DISTINCT FROM 'known' OR public.file_wire_identity_valid(object->'identity'->'value') IS NOT TRUE
 OR object->'content'->>'state' IS DISTINCT FROM 'known' OR object->'content'->'value'->>'kind' IS DISTINCT FROM 'sha256_file'
 OR object->'content'->'value'->'object_identity' IS DISTINCT FROM object->'identity'->'value'
 OR object->'content'->'value'->>'sha256' IS DISTINCT FROM e.publication_frozen_objects->'source'->'content'->'value'->>'sha256'
 OR (length(object->>'observation_source') BETWEEN 1 AND 128) IS NOT TRUE
 OR ((object->>'observed_at')::timestamptz>=e.publication_dispatch_started_at AND (object->>'observed_at')::timestamptz<=(slot->>'observed_at')::timestamptz) IS NOT TRUE
 OR (step='publish_target' AND e.operation='hardlink' AND (object->'identity'->'value'->>'device' IS DISTINCT FROM e.publication_frozen_objects->'source'->'identity'->'value'->>'device' OR object->'identity'->'value'->>'inode' IS DISTINCT FROM e.publication_frozen_objects->'source'->'identity'->'value'->>'inode')) THEN
 -- Temporal and observation-source bounds are part of object qualification.
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 END;
 END IF;
 covered:=covered||jsonb_build_array(step);
 END IF;
 END LOOP;
 IF public.observed_closed(cl,ARRAY['kind','executor_session','boot_nonce','call_nonce','covered_steps','joined_at','observer_version']) IS NOT TRUE
 OR cl->>'kind' IS DISTINCT FROM 'local_join' OR cl->>'executor_session' IS DISTINCT FROM e.publication_executor_session
 OR e.publication_executor_session IS DISTINCT FROM (cl->>'boot_nonce')||':'||(cl->>'call_nonce')
 OR cl->'covered_steps' IS DISTINCT FROM covered OR length(cl->>'boot_nonce') NOT BETWEEN 1 AND 128
 OR COALESCE(jsonb_typeof(cl->'observer_version'),'null') IS DISTINCT FROM 'string' OR COALESCE(length(cl->>'observer_version'),0) NOT BETWEEN 1 AND 128 -- §13.16.35 Q11: jsonb null/type confusion must not slip the three-valued OR chain (no upstream wire check covers observer_version)
 OR ((cl->>'joined_at')::timestamptz>=e.publication_dispatch_started_at) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
END $$;

CREATE FUNCTION file_validate_final_proof(e file_operations) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE s text; a timestamptz; o timestamptz; j timestamptz; proof jsonb; object jsonb;
BEGIN
 PERFORM public.file_validate_final_proof_before181(e);
 IF public.file_final_object_valid(e.publication_frozen_objects->'source') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF e.collision_policy='overwrite' AND public.file_final_object_valid(e.publication_frozen_objects->'expected_target') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 j:=(e.publication_closed_proof->>'joined_at')::timestamptz;
 IF e.publication_closure_receipt_observed_at IS NULL OR NOT isfinite(e.publication_closure_receipt_observed_at) OR (j<=e.publication_closure_receipt_observed_at) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_CLOSURE_INVALID'; END IF;
 FOREACH s IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
 a:=public.file_step_authorized_at(e,s);
 IF e.publication_step_checkpoint->s->>'state'='not_required' THEN
 IF a IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 CONTINUE; END IF;
 IF a IS NULL OR NOT isfinite(a) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 o:=(e.publication_step_checkpoint->s->>'observed_at')::timestamptz;
 IF (e.publication_dispatch_started_at<=a AND a<=o AND o<=j) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 proof:=e.publication_step_checkpoint->s->'proof';
 IF s IN ('stage_temp','reserve_version','publish_target') THEN
 object:=CASE WHEN s='publish_target' THEN proof->'target' ELSE proof->'object' END;
 IF public.file_final_object_valid(object) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF s='publish_target' AND proof->'displaced'->>'state'='known' THEN
 IF public.file_wire_fv_valid(proof->'displaced') IS NOT TRUE OR public.file_final_object_valid(proof->'displaced'->'value') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF proof->'displaced'->'value'->'identity'->'value'->'generation_nanos' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN e.publication_step_checkpoint->'reserve_version'->'proof'->'object'->'identity'->'value'->'generation_nanos' ELSE e.publication_frozen_objects->'expected_target'->'identity'->'value'->'generation_nanos' END) OR proof->'displaced'->'value'->'content'->'value'->'sha256' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN e.publication_step_checkpoint->'reserve_version'->'proof'->'object'->'content'->'value'->'sha256' ELSE e.publication_frozen_objects->'expected_target'->'content'->'value'->'sha256' END) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_PUBLICATION_INVALID'; END IF;
 END IF;
 IF public.file_wire_time_valid(object->'content'->'value'->'read_started_at') IS NOT TRUE OR public.file_wire_time_valid(object->'content'->'value'->'read_finished_at') IS NOT TRUE OR public.file_wire_time_valid(object->'observed_at') IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 IF (a<=(object->'content'->'value'->>'read_started_at')::timestamptz AND (object->'content'->'value'->>'read_started_at')::timestamptz<=(object->'content'->'value'->>'read_finished_at')::timestamptz AND (object->'content'->'value'->>'read_finished_at')::timestamptz=(object->>'observed_at')::timestamptz AND (object->>'observed_at')::timestamptz<=o) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 ELSE
 IF public.file_final_object_valid(proof->'before') IS NOT TRUE OR public.observed_closed(proof->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE OR public.file_location_valid(proof->'after'->'location') IS NOT TRUE OR public.file_wire_identity_valid(proof->'after'->'parent') IS NOT TRUE OR proof->'after'->>'method' IS DISTINCT FROM 'lstat_enoent' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_OBJECT_INVALID'; END IF;
 IF public.file_wire_time_valid(proof->'after'->'observed_at') IS NOT TRUE OR ((proof->'after'->>'observed_at')::timestamptz BETWEEN a AND o) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_TIME_INVALID'; END IF;
 END IF;
 END LOOP;
END $$;

CREATE FUNCTION file_assert_final_proof(p_permit text) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; o public.file_operations; dep text;
BEGIN
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=e.id;
 IF NOT EXISTS(SELECT 1 FROM jsonb_array_elements(o.file_attempt_index) x WHERE x->>'ordinal'=e.publication_attempt::text AND x->>'work_item_id'=e.publication_work_item_id AND x->>'token'=e.publication_fencing_token::text AND x->>'session_digest'='sha256:'||encode(sha256(convert_to(e.publication_executor_session,'UTF8')),'hex')) OR e.publication_effect_key IS DISTINCT FROM o.idempotency_key OR e.publication_frozen_objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path OR e.publication_frozen_objects->'source'->'size' IS DISTINCT FROM to_jsonb(o.expected_size_bytes) OR e.publication_frozen_objects->'source'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:'||(o.source_fingerprint->>'value') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 SELECT 'sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE(string_agg(parent.file_ref||':'||dep.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C"),''),'UTF8')),'hex') INTO dep FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id;
 IF dep IS DISTINCT FROM e.publication_dependency_digest THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations parent JOIN public.acquisition_files dep ON dep.acquisition_id=parent.acquisition_id AND dep.file_ref=parent.file_ref WHERE parent.id=o.bound_video_operation_id AND dep.outcome IS DISTINCT FROM 'succeeded') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.acquisition_files t JOIN public.acquisitions i ON i.id=t.acquisition_id WHERE t.acquisition_id=e.acquisition_id AND t.file_ref=e.file_ref AND t.canonical_path=o.target_path AND e.publication_frozen_objects->'target'->>'canonical_path'=o.target_path AND o.operation=t.operation AND o.intent=t.intent AND o.collision_policy=t.collision AND o.source_fingerprint->>'algorithm'='sha256' AND (e.collision_policy<>'overwrite' OR (i.input->'delivery'->>'intent'='replace' AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE r.value->'expected_object'->'location'=e.publication_frozen_objects->'expected_target'->'location' AND r.value->'expected_object'->'identity'=e.publication_frozen_objects->'expected_target'->'identity' AND r.value->'expected_object'->'size'=e.publication_frozen_objects->'expected_target'->'size' AND r.value->'expected_object'->'content'->'value'->>'sha256'=e.publication_frozen_objects->'expected_target'->'content'->'value'->>'sha256')))) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINAL_PROOF_AUTHORIZATION_INVALID'; END IF;
 PERFORM public.file_validate_final_proof(e);
END $$;

CREATE FUNCTION file_cancelled_dependency_suppression(p_operation text, p_work text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; refs text[]; child public.file_operations;
 count_suppressed integer:=0; digest text; receipt_text text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (w.owner_id=o.id AND w.acquisition_id=o.acquisition_id AND w.cancel_requested AND w.recovery_blocked
  AND EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id
   AND r.file_ref=o.file_ref AND r.operation_id=o.id AND r.outcome='succeeded' AND r.effect_class='confirmed')) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CANCEL_SUPPRESSION_SOURCE_INVALID'; END IF;
 WITH RECURSIVE descendants(operation_id, file_ref) AS (
  SELECT c.id, c.file_ref FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.bound_video_operation_id=o.id
  UNION
  SELECT c.id, c.file_ref FROM public.file_operations c JOIN descendants p ON c.bound_video_operation_id=p.operation_id
 ) SELECT array_agg(d.file_ref ORDER BY d.file_ref COLLATE "C") INTO refs FROM descendants d
 WHERE NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=d.file_ref AND r.outcome IS NOT NULL);
 IF COALESCE(cardinality(refs),0)>10000 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_GRAPH_CAPACITY'; END IF;
 -- Check the whole set before the first business write. Prepared evidence is
 -- refused until the fixed no_dispatch closure is available; never erased.
 IF EXISTS(SELECT 1 FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs)
  AND (c.state NOT IN ('pending','running') OR c.file_attempts_started<>0 OR c.file_attempt_ticket IS NOT NULL
   OR EXISTS(SELECT 1 FROM public.file_operations e WHERE e.publication_permit_id IS NOT NULL AND e.id=c.id)
   OR EXISTS(SELECT 1 FROM public.work_item_history cw WHERE cw.owner_type='file_operation' AND cw.owner_id=c.id AND (cw.state<>'available' OR cw.attempt<>0)))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DEPENDENCY_HANDOFF_EFFECT_CONFLICT'; END IF;
 FOR child IN SELECT c.* FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs) ORDER BY c.id COLLATE "C" LOOP
 receipt_text:='{"reason_code":"DEPENDENCY_HANDOFF_CANCELLED","schema_version":1}';
 digest:=public.file_proof_digest('acquisition-effect-resolution:v1',receipt_text);
 UPDATE public.file_operations SET state='failed',failure_code='DEPENDENCY_HANDOFF_CANCELLED',failure_reason='Dependency handoff cancelled',version=version+1 WHERE id=child.id;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
  delivery_source_cleanup='not_required',delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED',evidence_digest=digest,
  operation_id=child.id,receipt=receipt_text::jsonb,delivered_at=CURRENT_TIMESTAMP
  WHERE acquisition_id=o.acquisition_id AND file_ref=child.file_ref;
 PERFORM public.work_item_finish('failed',child.version+1,cw.id) FROM public.work_items cw WHERE cw.owner_type='file_operation' AND cw.owner_id=child.id AND cw.state='available';
 count_suppressed:=count_suppressed+1;
 END LOOP;
 RETURN count_suppressed;
END $$;

CREATE FUNCTION file_execution_lock(p_operation text, p_work text, p_token bigint) RETURNS file_operations
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; a public.artifacts; p public.postprocessing_plans; r public.acquisitions; w public.work_item_history; root_id text; artifact_id text; plan_id text; task_id text;
BEGIN
 SELECT op.acquisition_id,op.artifact_id,op.postprocessing_plan_id INTO STRICT root_id,artifact_id,plan_id FROM public.file_operations op WHERE op.id=p_operation;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT download_task_id INTO STRICT task_id FROM public.artifacts WHERE id=artifact_id;
 PERFORM id FROM public.download_tasks WHERE id=task_id FOR UPDATE;
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=artifact_id FOR UPDATE;
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=plan_id FOR UPDATE;
 PERFORM id FROM public.file_operations WHERE postprocessing_plan_id=plan_id ORDER BY id COLLATE "C" FOR UPDATE;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 -- Existing work, including settlement, is locked before later paths can touch
 -- it; no late acquisition of an existing work.
 PERFORM id FROM public.work_items WHERE id=p_work OR acquisition_id=root_id ORDER BY id COLLATE "C" FOR UPDATE;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (r.state='running' AND r.record_state='full' AND a.acquisition_id=root_id AND a.state='verified'
  AND p.acquisition_id=root_id AND p.artifact_id=a.id AND p.sealed_at IS NOT NULL AND p.state='succeeded'
  AND o.acquisition_id=root_id AND o.artifact_version=a.version AND o.state IN ('pending','running')
  AND w.owner_type='file_operation' AND w.owner_id=o.id AND w.kind='file_execute'
  AND w.acquisition_id=root_id AND w.state='leased' AND w.fencing_token=p_token AND w.expected_owner_version=o.version
  AND NOT w.cancel_requested AND NOT w.recovery_blocked AND w.lease_until>clock_timestamp()
  AND COALESCE(w.input_snapshot->>'phase','execute')='execute') IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STAGE_STALE_CLAIM'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations parent JOIN public.acquisition_files result ON result.acquisition_id=parent.acquisition_id AND result.file_ref=parent.file_ref
  WHERE parent.id=o.bound_video_operation_id AND result.outcome IS DISTINCT FROM 'succeeded') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_DEPENDENCY_NOT_READY'; END IF;
 RETURN o;
END $$;

CREATE FUNCTION file_dispatch_lock(p_operation text, p_work text, p_token bigint, p_permit text, p_session text) RETURNS file_operations
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; e public.file_operations; now_at timestamptz;
BEGIN
 o:=public.file_execution_lock(p_operation,p_work,p_token);
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit FOR UPDATE;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at);
 IF (e.acquisition_id=o.acquisition_id AND e.id=o.id
  AND e.publication_work_item_id=p_work AND e.publication_fencing_token=p_token AND e.publication_owner_version=o.version
  AND e.publication_executor_session=p_session AND e.publication_attempt=o.file_attempts_started
  AND o.file_attempt_ticket->>'work_item_id'=p_work AND (o.file_attempt_ticket->>'token')::bigint=p_token
  AND o.file_attempt_ticket->>'executor_session'=p_session AND o.file_attempt_ticket->>'state'='issued'
  AND now_at<(o.file_attempt_ticket->>'attempt_until')::timestamptz AND now_at<o.file_wall_until
  AND e.publication_conflict_evidence IS NULL) IS NOT TRUE OR EXISTS(
   SELECT 1 FROM public.file_operations other WHERE other.acquisition_id=o.acquisition_id AND other.publication_effect_key=e.publication_effect_key
    AND (other.publication_conflict_evidence IS NOT NULL OR (other.publication_permit_id<>e.publication_permit_id AND NOT other.publication_retry_safe))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_DISPATCH_NOT_QUALIFIED'; END IF;
 RETURN e;
END $$;

CREATE FUNCTION file_evidence_bytes_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE key text; old_bytes jsonb;
BEGIN
 IF NEW.publication_frozen_objects_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_frozen_objects_canonical) IS NOT TRUE OR
  NEW.publication_frozen_objects_canonical::jsonb IS DISTINCT FROM NEW.publication_frozen_objects) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_OBJECT_BYTES_INVALID'; END IF;
 IF NEW.publication_closure_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_closure_canonical) IS NOT TRUE OR
  NEW.publication_closure_canonical::jsonb IS DISTINCT FROM NEW.publication_closed_proof) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CLOSURE_BYTES_INVALID'; END IF;
 IF NEW.publication_resolution_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_resolution_canonical) IS NOT TRUE OR
  NEW.publication_resolution_canonical::jsonb IS DISTINCT FROM NEW.publication_resolution_proof) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESOLUTION_BYTES_INVALID'; END IF;
 IF NEW.publication_resolution_checkpoint_canonical IS NOT NULL AND (public.plan_canonical_valid(NEW.publication_resolution_checkpoint_canonical) IS NOT TRUE OR
  NEW.publication_resolution_checkpoint_canonical::jsonb IS DISTINCT FROM NEW.publication_step_checkpoint) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RESOLUTION_CHECKPOINT_INVALID'; END IF;
 FOR key IN SELECT jsonb_object_keys(NEW.publication_step_proof_bytes) LOOP
  IF key NOT IN ('stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup') OR
   jsonb_typeof(NEW.publication_step_proof_bytes->key) IS DISTINCT FROM 'string' OR
   public.plan_canonical_valid(NEW.publication_step_proof_bytes->>key) IS NOT TRUE OR
   (NEW.publication_step_proof_bytes->>key)::jsonb IS DISTINCT FROM NEW.publication_step_checkpoint->key->'proof' THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_BYTES_INVALID'; END IF;
 END LOOP;
 IF (SELECT count(*) FROM jsonb_object_keys(NEW.publication_conflict_seen))>100 OR EXISTS(SELECT 1 FROM jsonb_each(NEW.publication_conflict_seen) s WHERE s.key !~ '^sha256:[0-9a-f]{64}$' OR s.value IS DISTINCT FROM 'true'::jsonb) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_INDEX_INVALID'; END IF;
 IF TG_OP='UPDATE' AND OLD.publication_permit_id IS NOT DISTINCT FROM NEW.publication_permit_id THEN
  IF (OLD.publication_conflict_index_exhausted AND NOT NEW.publication_conflict_index_exhausted) OR NOT (NEW.publication_conflict_seen @> OLD.publication_conflict_seen) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_INDEX_IMMUTABLE'; END IF;
  IF (OLD.publication_frozen_objects_canonical IS NOT NULL AND NEW.publication_frozen_objects_canonical IS DISTINCT FROM OLD.publication_frozen_objects_canonical)
   OR (OLD.publication_closure_canonical IS NOT NULL AND NEW.publication_closure_canonical IS DISTINCT FROM OLD.publication_closure_canonical)
   OR (OLD.publication_resolution_canonical IS NOT NULL AND NEW.publication_resolution_canonical IS DISTINCT FROM OLD.publication_resolution_canonical)
   OR (OLD.publication_resolution_checkpoint_canonical IS NOT NULL AND NEW.publication_resolution_checkpoint_canonical IS DISTINCT FROM OLD.publication_resolution_checkpoint_canonical)
   OR NEW.publication_conflict_count<OLD.publication_conflict_count THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PROOF_BYTES_IMMUTABLE'; END IF;
  FOR key IN SELECT jsonb_object_keys(OLD.publication_step_proof_bytes) LOOP
   IF NEW.publication_step_proof_bytes->key IS DISTINCT FROM OLD.publication_step_proof_bytes->key THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_BYTES_IMMUTABLE'; END IF;
  END LOOP;
 END IF;
 RETURN NEW;
END $_$;

CREATE FUNCTION file_failure_effect_class(p_operation text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE
  WHEN e.publication_permit_id IS NULL THEN 'no_external_effect'
  WHEN e.publication_dispatch_state='prepared' AND NOT EXISTS(SELECT 1 FROM jsonb_each(e.publication_step_checkpoint) s
   WHERE s.value->>'state' NOT IN ('not_started','not_required')) THEN 'no_external_effect'
  WHEN e.publication_dispatch_state='closed' THEN e.publication_effect_class
  ELSE 'unknown' END
 FROM (SELECT 1) one LEFT JOIN public.file_operations e ON e.id=p_operation
$$;

CREATE FUNCTION file_failure_notification(p_operation text, p_code text, p_reason text) RETURNS jsonb
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
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.custom_title',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'release.group_name',public.file_notification_text(CASE WHEN src->'group'->>'state'='known' THEN src->'group'->>'value' END),
 'event.type','file_operation_failed','result.outcome','failure','result.reason',reason,
 'file.name',public.file_notification_text(name),'file.basename',public.file_notification_text(base),'file.extension',public.file_notification_text(lower(extension)),
 'file.size_bytes',f.size_bytes,'file.media_kind',public.file_notification_text(f.media_kind),
 'file.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'file.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'file.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'file.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'output.directory',NULL,'output.filename',NULL,'output.relative_path',NULL,
 'file_operation.kind',public.file_notification_text(o.operation),
 'file_operation.collision_policy',public.file_notification_text(o.collision_policy))
 || public.notification_input_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_title_facts(COALESCE(input,'{}'::jsonb))
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

CREATE FUNCTION file_failure_atomic_body(p_operation text, p_work text, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; child public.file_operations; refs text[];
 effect text; cleanup text; receipt_text text; next_version bigint; dependents integer:=0; payload jsonb;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>1024 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_SHAPE'; END IF;
 IF EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref AND r.outcome IS NOT NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_RESULT_EXISTS'; END IF;
 effect:=public.file_failure_effect_class(o.id);
 cleanup:=CASE WHEN o.operation='move' AND effect<>'no_external_effect' THEN 'uncertain' ELSE 'not_required' END;
 -- Descendants that never started are failed without effect; any started or
 -- evidenced descendant is a conflict and the whole command rolls back.
 WITH RECURSIVE descendants(operation_id, file_ref) AS (
  SELECT c.id, c.file_ref FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.bound_video_operation_id=o.id
  UNION
  SELECT c.id, c.file_ref FROM public.file_operations c JOIN descendants p ON c.bound_video_operation_id=p.operation_id
 ) SELECT array_agg(d.file_ref ORDER BY d.file_ref COLLATE "C") INTO refs FROM descendants d
 WHERE NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=d.file_ref AND r.outcome IS NOT NULL);
 IF EXISTS(SELECT 1 FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs)
  AND (c.state NOT IN ('pending','running') OR c.file_attempts_started<>0 OR c.file_attempt_ticket IS NOT NULL
   OR EXISTS(SELECT 1 FROM public.file_operations e WHERE e.publication_permit_id IS NOT NULL AND e.id=c.id)
   OR EXISTS(SELECT 1 FROM public.work_item_history cw WHERE cw.owner_type='file_operation' AND cw.owner_id=c.id AND (cw.state<>'available' OR cw.attempt<>0)))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DEPENDENCY_HANDOFF_EFFECT_CONFLICT'; END IF;
 UPDATE public.file_operations SET state='failed',failure_code=p_code,failure_reason=NULLIF(p_reason,''),
 observed_at=clock_timestamp(),version=version+1 WHERE id=o.id RETURNING version INTO next_version;
 receipt_text:=jsonb_build_object('reason_code',p_code,'schema_version',1,'effect_class',effect)::text;
 UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class=effect,
 delivery_source_cleanup=cleanup,delivery_reason_code=p_code,
 evidence_digest=public.file_proof_digest('acquisition-effect-resolution:v1',receipt_text),operation_id=o.id,
 receipt=receipt_text::jsonb,delivered_at=CURRENT_TIMESTAMP
 WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
 -- N1 (221): the failure transition admits its notification in this
 -- transaction, managed_anime_id NULL, like the success command.
 payload:=public.file_failure_notification(o.id,p_code,p_reason);
 IF public.file_notification_safe(payload) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_failed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',payload,'pending_target_resolution');
 FOR child IN SELECT c.* FROM public.file_operations c WHERE c.acquisition_id=o.acquisition_id AND c.file_ref=ANY(refs) ORDER BY c.id COLLATE "C" LOOP
  receipt_text:='{"reason_code":"DEPENDENCY_FAILED","schema_version":1}';
  UPDATE public.file_operations SET state='failed',failure_code='DEPENDENCY_FAILED',failure_reason='Dependency failed',version=version+1 WHERE id=child.id;
  UPDATE public.acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
  delivery_source_cleanup='not_required',delivery_reason_code='DEPENDENCY_FAILED',
  evidence_digest=public.file_proof_digest('acquisition-effect-resolution:v1',receipt_text),operation_id=child.id,
  receipt=receipt_text::jsonb,delivered_at=CURRENT_TIMESTAMP
  WHERE acquisition_id=o.acquisition_id AND file_ref=child.file_ref;
  PERFORM public.work_item_finish('failed',child.version+1,cw.id) FROM public.work_items cw WHERE cw.owner_type='file_operation' AND cw.owner_id=child.id AND cw.state='available';
  dependents:=dependents+1;
 END LOOP;
 IF public.work_item_fail_at_attempt_budget(next_version,p_code,p_reason,p_work,o.version)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','failed','owner_version',next_version,'effect_class',effect,'failed_dependencies',dependents);
END $$;

CREATE FUNCTION file_finalization_prelock(p_operation text, p_work text) RETURNS file_operations
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; r public.acquisitions; a public.artifacts; p public.postprocessing_plans;
 root_id text; artifact_id text; plan_id text; task_id text; marker jsonb; persisted_marker jsonb; m public.one_shot_acquisitions;
BEGIN
 SELECT op.acquisition_id,op.artifact_id,op.postprocessing_plan_id INTO STRICT root_id,artifact_id,plan_id FROM public.file_operations op WHERE op.id=p_operation;
 SELECT input_snapshot->'one_shot' INTO STRICT marker FROM public.work_item_history WHERE id=p_work;
 IF marker IS NOT NULL THEN
  SELECT * INTO STRICT m FROM public.one_shot_acquisitions WHERE id=marker->>'marker_id' FOR UPDATE;
  IF (m.state='running' AND m.admission_epoch=(marker->>'admission_epoch')::bigint AND m.correlation_id=marker->>'correlation_id') IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_MARKER_STALE'; END IF;
 END IF;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT download_task_id INTO STRICT task_id FROM public.artifacts WHERE id=artifact_id;
 PERFORM id FROM public.download_tasks WHERE id=task_id FOR UPDATE;
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=artifact_id FOR UPDATE;
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=plan_id FOR UPDATE;
 IF (r.state='running' AND r.record_state='full' AND a.acquisition_id=r.id AND a.state='verified'
  AND p.acquisition_id=r.id AND p.artifact_id=a.id AND p.artifact_version=a.version AND p.state='succeeded' AND p.sealed_at IS NOT NULL) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_GRAPH_STALE'; END IF;
 -- The sealed root bounds the complete neighbor set. All existing owners are
 -- acquired before any handoff/work/evidence; no later neighbor lock is needed.
 IF (SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=root_id AND f.plan_kind IS NOT NULL)>10000 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_GRAPH_CAPACITY'; END IF;
 -- The sealed declarations and their per-file projections are re-derived from
 -- the immutable canonical plan. Every execute item owns its operation and a
 -- subtitle's bound video must be the parent operation of the same plan.
 IF public.plan_canonical_valid(p.canonical_plan) IS NOT TRUE
 OR p.plan_digest IS DISTINCT FROM public.file_proof_digest('acquisition-plan:v1',p.canonical_plan)
 OR jsonb_array_length(p.canonical_plan::jsonb->'items')<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=root_id AND f.plan_kind IS NOT NULL)
 OR EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=root_id AND f.plan_kind IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.canonical_plan::jsonb->'items') x WHERE x=jsonb_build_object(
   'file_ref',f.file_ref,'kind',f.plan_kind,'target_file_ref',f.plan_target_file_ref,'evidence',f.plan_evidence,
   'reason_code',f.plan_reason_code,'diagnostic',f.plan_diagnostic)))
 OR EXISTS(SELECT 1 FROM public.acquisition_files f
  LEFT JOIN public.file_operations op ON op.id=f.plan_operation_ref
  LEFT JOIN public.artifact_files cf ON cf.artifact_id=a.id AND cf.file_ref=f.file_ref
  LEFT JOIN public.file_operations parent ON parent.id=op.bound_video_operation_id
  LEFT JOIN public.artifact_files pf ON pf.artifact_id=a.id AND pf.file_ref=parent.file_ref
  WHERE f.acquisition_id=root_id AND f.plan_kind='execute' AND (op.id IS NULL OR op.acquisition_id<>root_id
   OR op.artifact_id<>a.id OR op.artifact_version<>a.version OR op.postprocessing_plan_id<>p.id OR op.file_ref<>f.file_ref
   OR (op.bound_video_operation_id IS NOT NULL AND (parent.id IS NULL OR parent.postprocessing_plan_id IS DISTINCT FROM p.id
    OR cf.media_kind IS DISTINCT FROM 'subtitle' OR pf.media_kind IS DISTINCT FROM 'video'
    OR cf.bound_video_file_ref IS DISTINCT FROM pf.file_ref)))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_GRAPH_INVALID'; END IF;
 PERFORM id FROM public.file_operations WHERE acquisition_id=root_id ORDER BY id COLLATE "C" FOR UPDATE;
 -- Compare frozen payload, never downstream execution state/version.
 IF jsonb_typeof(p.committed_operations) IS DISTINCT FROM 'array'
 OR (SELECT count(*) FROM public.file_operations op WHERE op.postprocessing_plan_id=p.id)<>jsonb_array_length(p.committed_operations)
 OR EXISTS(SELECT 1 FROM public.file_operations op WHERE op.postprocessing_plan_id=p.id AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(p.committed_operations) x WHERE x->>'id'=op.id AND x->>'file_ref'=op.file_ref
  AND x->>'artifact'=op.artifact_id AND (x->>'version')::bigint=op.artifact_version AND x->>'file'=op.artifact_file_id
  AND x->>'operation'=op.operation AND x->>'source'=op.source_path AND x->>'target'=op.target_path
  AND x->'fingerprint'=op.source_fingerprint AND (x->>'size')::bigint=op.expected_size_bytes
  AND x->>'intent'=op.intent AND x->>'collision'=op.collision_policy AND x->'render'=op.frozen_render_snapshot
  AND x->'config'=op.frozen_config_snapshot AND NULLIF(x->'overwrite','null'::jsonb) IS NOT DISTINCT FROM op.overwrite_execution_snapshot
  AND NULLIF(x->>'bound_video','') IS NOT DISTINCT FROM op.bound_video_operation_id AND x->>'key'=op.idempotency_key
  AND op.correlation_id=p.acquisition_id AND op.causation_type='postprocessing_plan' AND op.causation_id=p.id)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_OPERATION_DECLARATION_INVALID'; END IF;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 PERFORM id FROM public.work_items WHERE acquisition_id=root_id OR id=p_work ORDER BY id COLLATE "C" FOR UPDATE;
 SELECT input_snapshot->'one_shot' INTO STRICT persisted_marker FROM public.work_item_history WHERE id=p_work;
 IF persisted_marker IS DISTINCT FROM marker OR o.acquisition_id IS DISTINCT FROM root_id OR o.artifact_version IS DISTINCT FROM a.version THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_GRAPH_STALE'; END IF;
 PERFORM id FROM public.file_operations WHERE acquisition_id=root_id AND publication_permit_id IS NOT NULL ORDER BY id COLLATE "C" FOR UPDATE;
 IF EXISTS(SELECT 1 FROM public.file_operations parent JOIN public.acquisition_files result ON result.acquisition_id=parent.acquisition_id AND result.file_ref=parent.file_ref
  WHERE parent.id=o.bound_video_operation_id AND result.outcome IS DISTINCT FROM 'succeeded') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_DEPENDENCY_STALE'; END IF;
 RETURN o;
END $$;

CREATE FUNCTION file_finalization_cancelled_gate(p_operation text, p_work text, p_token bigint, p_permit text) RETURNS timestamp with time zone
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; e public.file_operations; now_at timestamptz;
BEGIN
 o:=public.file_finalization_prelock(p_operation,p_work);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at);
 IF (o.state IN ('pending','running') AND w.owner_type='file_operation' AND w.owner_id=o.id
  AND w.kind='file_execute' AND w.acquisition_id=o.acquisition_id AND w.state='leased' AND w.fencing_token=p_token
  AND w.expected_owner_version=o.version AND w.cancel_requested AND w.recovery_blocked AND w.lease_until>now_at
  AND now_at<o.file_wall_until AND e.acquisition_id=o.acquisition_id AND e.id=o.id
  AND e.publication_work_item_id=w.id AND e.publication_fencing_token<p_token AND e.publication_owner_version<=o.version
  AND e.publication_dispatch_state='closed' AND e.publication_effect_class='confirmed' AND e.publication_conflict_evidence IS NULL
  AND e.publication_resolution_canonical IS NOT NULL AND e.publication_closure_canonical IS NOT NULL) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CANCEL_FINALIZATION_PENDING'; END IF;
 RETURN now_at;
END $$;

CREATE FUNCTION file_finalization_normal_gate(p_operation text, p_work text, p_token bigint, p_permit text) RETURNS timestamp with time zone
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; e public.file_operations; now_at timestamptz;
BEGIN
 o:=public.file_finalization_prelock(p_operation,p_work);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at);
 IF (o.state IN ('pending','running') AND w.owner_type='file_operation' AND w.owner_id=o.id
  AND w.kind='file_execute' AND w.acquisition_id=o.acquisition_id AND w.state='leased' AND w.fencing_token=p_token
  AND w.expected_owner_version=o.version AND NOT w.cancel_requested AND w.lease_until>now_at
  AND e.acquisition_id=o.acquisition_id AND e.id=o.id
  AND e.publication_work_item_id=w.id AND e.publication_fencing_token<=p_token AND e.publication_owner_version=o.version
  AND e.publication_dispatch_state='closed' AND e.publication_effect_class='confirmed' AND e.publication_conflict_evidence IS NULL
  AND e.publication_resolution_canonical IS NOT NULL AND e.publication_closure_canonical IS NOT NULL) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_NORMAL_STALE'; END IF;
 RETURN now_at;
END $$;

CREATE FUNCTION file_finish_failed(p_operation text, p_work text, p_token bigint, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history;
BEGIN
 o:=public.file_finalization_prelock(p_operation,p_work);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (o.state IN ('pending','running') AND o.postprocessing_plan_id IS NOT NULL
  AND w.owner_type='file_operation' AND w.owner_id=o.id AND w.kind='file_execute'
  AND w.acquisition_id=o.acquisition_id AND w.state='leased' AND w.fencing_token=p_token
  AND w.expected_owner_version=o.version AND w.lease_until>clock_timestamp()) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FAILURE_STALE'; END IF;
 RETURN public.file_failure_atomic_body(p_operation,p_work,p_code,p_reason);
END $$;

CREATE FUNCTION file_prepare_permit(p_operation text, p_work text, p_token bigint, p_session text, p_permit text, p_objects text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; p public.postprocessing_plans; t public.acquisition_files;
 objects jsonb; checkpoint jsonb:='{}'::jsonb; limits jsonb; dependency_digest text; step text; state text; now_at timestamptz; existing text;
BEGIN
 o:=public.file_execution_lock(p_operation,p_work,p_token);
 IF p_permit IS NULL OR char_length(p_permit) NOT BETWEEN 1 AND 128 OR p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128
  OR public.plan_canonical_valid(p_objects) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_SHAPE'; END IF;
 objects:=p_objects::jsonb;
 SELECT publication_permit_id INTO existing FROM public.file_operations WHERE publication_work_item_id=p_work AND publication_fencing_token=p_token;
 IF FOUND THEN
  IF (SELECT publication_executor_session FROM public.file_operations WHERE publication_permit_id=existing) IS DISTINCT FROM p_session THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_SESSION_MISMATCH'; END IF;
  RETURN jsonb_build_object('created',false,'permit_id',existing);
 END IF;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at);
 IF (o.file_attempt_ticket->>'work_item_id'=p_work AND (o.file_attempt_ticket->>'token')::bigint=p_token
  AND o.file_attempt_ticket->>'executor_session'=p_session AND o.file_attempt_ticket->>'state'='issued'
  AND now_at<(o.file_attempt_ticket->>'attempt_until')::timestamptz AND now_at<o.file_wall_until) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_TICKET_INVALID'; END IF;
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=o.postprocessing_plan_id;
 SELECT * INTO STRICT t FROM public.acquisition_files WHERE acquisition_id=o.acquisition_id AND file_ref=o.file_ref;
 SELECT q.input->'limits'->'reconcile' INTO STRICT limits FROM public.acquisitions q WHERE q.id=o.acquisition_id;
 IF public.observed_closed(objects,CASE WHEN o.collision_policy='overwrite' THEN ARRAY['source','source_parent','target','target_parent','temp','source_capture','temp_capture','expected_target'] WHEN o.operation IN ('copy','move') OR o.collision_policy='version' THEN ARRAY['source','source_parent','target','target_parent','temp','source_capture','temp_capture'] ELSE ARRAY['source','source_parent','target','target_parent','temp'] END) IS NOT TRUE
  OR public.observed_closed(objects->'source',ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'identity',ARRAY['state','value','reason_code']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'content',ARRAY['state','value','reason_code']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'content'->'value',ARRAY['kind','sha256','size','object_identity','read_started_at','read_finished_at','verifier_version']) IS NOT TRUE
  OR public.observed_closed(objects->'source'->'location',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'target',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR public.observed_closed(objects->'temp',ARRAY['namespace_digest','root','relative_path','canonical_path']) IS NOT TRUE
  OR objects->'source'->'identity'->'reason_code' IS DISTINCT FROM 'null'::jsonb
  OR objects->'source'->'content'->'reason_code' IS DISTINCT FROM 'null'::jsonb
  OR public.observed_identity_valid(objects->'source_parent') IS NOT TRUE
  OR public.observed_identity_valid(objects->'target_parent') IS NOT TRUE
  OR objects->'source'->'location'->>'canonical_path' IS DISTINCT FROM o.source_path
  OR objects->'target'->>'canonical_path' IS DISTINCT FROM t.canonical_path
  OR t.canonical_path IS DISTINCT FROM o.target_path OR t.operation IS DISTINCT FROM o.operation
  OR t.intent IS DISTINCT FROM o.intent OR t.collision IS DISTINCT FROM o.collision_policy
  OR objects->'source'->>'file_type' IS DISTINCT FROM 'regular'
  OR (objects->'source'->>'size')::bigint IS DISTINCT FROM o.expected_size_bytes
  OR objects->'source'->'identity'->>'state' IS DISTINCT FROM 'known'
  OR public.observed_identity_valid(objects->'source'->'identity'->'value') IS NOT TRUE
  OR objects->'source'->'content'->>'state' IS DISTINCT FROM 'known'
  OR objects->'source'->'content'->'value'->>'kind' IS DISTINCT FROM 'sha256_file'
  OR o.source_fingerprint->>'algorithm' IS DISTINCT FROM 'sha256'
  OR objects->'source'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:'||(o.source_fingerprint->>'value')
  OR (objects->'source'->'content'->'value'->>'size')::bigint IS DISTINCT FROM o.expected_size_bytes
  OR objects->'source'->'content'->'value'->'object_identity' IS DISTINCT FROM objects->'source'->'identity'->'value'
  OR objects->'temp'->>'canonical_path' IS DISTINCT FROM regexp_replace(o.target_path,'[^/]+$','')||'.bansoko-permit-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')
  OR public.file_location_valid(objects->'source'->'location') IS NOT TRUE
  OR public.file_location_valid(objects->'target') IS NOT TRUE OR public.file_location_valid(objects->'temp') IS NOT TRUE
  OR objects->'source'->>'observed_at' IS DISTINCT FROM objects->'source'->'content'->'value'->>'read_finished_at'
  OR objects->'source'->>'observation_source' IS DISTINCT FROM objects->'source'->'content'->'value'->>'verifier_version'
  OR (objects->'source'->'content'->'value'->>'verifier_version' IN ('local-stable-read-v1','local-exact-path-read-v1')) IS NOT TRUE
  OR ((objects->'source'->'content'->'value'->>'read_started_at')::timestamptz <= (objects->'source'->'content'->'value'->>'read_finished_at')::timestamptz
   AND (objects->'source'->'content'->'value'->>'read_finished_at')::timestamptz<=now_at) IS NOT TRUE
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_OBJECT_BINDING'; END IF;
 -- Overwrite's admitted backup/exchange protocol is not represented by this
 -- initial prepare producer. Refuse it until its own complete proof path lands.
 IF (o.operation IN ('copy','move') OR o.collision_policy IN ('version','overwrite')) AND (
  public.file_location_valid(objects->'source_capture') IS NOT TRUE OR public.file_location_valid(objects->'temp_capture') IS NOT TRUE
  OR objects->'source_capture'->>'canonical_path' IS DISTINCT FROM rtrim(objects->'source'->'location'->>'root','/')||'/.bansoko-cleanup-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')||'-source/object'
  OR objects->'temp_capture'->>'canonical_path' IS DISTINCT FROM rtrim(objects->'target'->>'root','/')||'/.bansoko-cleanup-'||encode(sha256(convert_to(p_permit,'UTF8')),'hex')||'-temp/object'
 ) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CLEANUP_CAPTURE_BINDING'; END IF;
 IF o.collision_policy='overwrite' AND NOT EXISTS(SELECT 1 FROM public.acquisitions i WHERE i.id=o.acquisition_id
  AND i.input->'delivery'->>'intent'='replace'
  AND i.input->'delivery'->>'collision'='overwrite' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE
  r.value->'expected_object'->'location' IS NOT DISTINCT FROM objects->'expected_target'->'location'
  AND r.value->'expected_object'->'size' IS NOT DISTINCT FROM objects->'expected_target'->'size'
  AND r.value->'expected_object'->'identity' IS NOT DISTINCT FROM objects->'expected_target'->'identity'
  AND r.value->'expected_object'->'content'->'value'->'sha256' IS NOT DISTINCT FROM objects->'expected_target'->'content'->'value'->'sha256')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_OVERWRITE_PROOF_REQUIRED'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations e WHERE e.acquisition_id=o.acquisition_id AND e.publication_effect_key=o.idempotency_key
  AND (NOT e.publication_retry_safe OR e.publication_conflict_evidence IS NOT NULL)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PREPARE_UNRESOLVED_EFFECT'; END IF;
 -- Dependency identity derives from the sealed graph and its historical
 -- delivery evidence, never from the current filesystem target observation.
 SELECT 'sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE(string_agg(
  parent.file_ref||':'||r.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C"),''),'UTF8')),'hex') INTO dependency_digest
 FROM public.file_operations parent JOIN public.acquisition_files r
 ON r.acquisition_id=parent.acquisition_id AND r.file_ref=parent.file_ref
 WHERE parent.id=o.bound_video_operation_id;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
  state:='not_started';
  IF (step IN ('stage_temp','temp_cleanup') AND o.operation='hardlink' AND o.collision_policy='block') OR
     (step='reserve_version' AND o.collision_policy<>'version') OR
     (step='source_cleanup' AND o.operation<>'move') THEN state:='not_required'; END IF;
  checkpoint:=checkpoint||jsonb_build_object(step,jsonb_build_object('state',state,'proof',NULL,'observed_at',NULL,'reason_code',NULL));
 END LOOP;
 -- S6: the slot is written onto the operation row. A retry_safe closed slot is
 -- replaced (guard_file_publication_identity admits it and bumps
 -- publication_attempt); every other slot shape is refused by that guard.
 UPDATE public.file_operations SET
  publication_permit_id=p_permit,
  publication_attempt=o.file_attempts_started,
  publication_evidence_version=1,
  publication_effect_key=o.idempotency_key,
  publication_spec_digest=t.spec_digest,
  publication_plan_digest=p.plan_digest,
  publication_dependency_digest=dependency_digest,
  publication_executor_session=p_session,
  publication_work_item_id=p_work,
  publication_fencing_token=p_token,
  publication_owner_version=o.version,
  publication_prepared_at=now_at,
  publication_dispatch_started_at=NULL,
  publication_unknown_since=NULL,
  publication_reconcile_until=NULL,
  publication_lookup_count=0,
  publication_lookup_ticket=NULL,
  publication_closed_proof=NULL,
  publication_resolution_proof=NULL,
  publication_closure_canonical=NULL,
  publication_resolution_canonical=NULL,
  publication_resolution_checkpoint_canonical=NULL,
  publication_closure_receipt_observed_at=NULL,
  publication_retry_safe=false,
  publication_stage_temp_started_at=NULL,
  publication_reserve_version_started_at=NULL,
  publication_publish_target_started_at=NULL,
  publication_source_cleanup_started_at=NULL,
  publication_temp_cleanup_started_at=NULL,
  publication_dispatch_state='prepared',
  publication_effect_class='unknown',
  publication_step_checkpoint=checkpoint,
  publication_step_proof_bytes='{}'::jsonb,
  publication_reconcile_wall_seconds=(limits->>'wall_seconds')::integer,
  publication_max_lookups=(limits->>'max_lookups')::integer,
  publication_frozen_objects=objects,
  publication_frozen_objects_canonical=p_objects,
  budget_clock_at=now_at
  WHERE id=o.id;
 RETURN jsonb_build_object('created',true,'permit_id',p_permit);
END $_$;

CREATE FUNCTION file_receipt_capture(p_permit text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; root_id text;
BEGIN
 SELECT acquisition_id INTO STRICT root_id FROM public.file_operations WHERE publication_permit_id=p_permit;
 PERFORM id FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit FOR UPDATE;
 RETURN jsonb_build_object('identity',jsonb_build_object('schema_version',1,'permit_id',e.publication_permit_id,'root_id',e.acquisition_id,
 'original_work_id',e.publication_work_item_id,'fencing_token',e.publication_fencing_token,'executor_session',e.publication_executor_session,'spec_digest',e.publication_spec_digest),
 'checkpoint',e.publication_step_checkpoint);
END $$;

CREATE FUNCTION file_record_conflict(p_permit text, p_slot text, p_first text, p_contradicting text, p_at timestamp with time zone) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; contradiction text; fact jsonb;
BEGIN
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit FOR UPDATE;
 contradiction:=public.file_proof_digest('file-conflict:v1',p_slot||':'||p_first||':'||p_contradicting);
 IF e.publication_conflict_seen ? contradiction THEN RETURN jsonb_build_object('recorded',false,'conflict',true,'evidence_version',e.publication_evidence_version); END IF;
 IF e.publication_conflict_index_exhausted THEN RETURN jsonb_build_object('recorded',false,'conflict',true,'reason_code','FILE_CONFLICT_INDEX_EXHAUSTED','evidence_version',e.publication_evidence_version); END IF;
 IF (SELECT count(*) FROM jsonb_object_keys(e.publication_conflict_seen))>=100 THEN
  UPDATE public.file_operations SET publication_conflict_index_exhausted=true,publication_evidence_version=publication_evidence_version+1,updated_at=clock_timestamp() WHERE publication_permit_id=e.publication_permit_id;
  UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=e.acquisition_id AND state='running' AND record_state='full';
  RETURN jsonb_build_object('recorded',true,'conflict',true,'reason_code','FILE_CONFLICT_INDEX_EXHAUSTED','evidence_version',e.publication_evidence_version+1);
 END IF;
 fact:=jsonb_build_object('schema_version',1,'slot',p_slot,'first_digest',p_first,'contradicting_digest',p_contradicting,'observed_at',p_at,'reason_code','FILE_PROOF_CONFLICT');
 UPDATE public.file_operations SET publication_conflict_evidence=COALESCE(publication_conflict_evidence,fact),
  publication_conflict_count=CASE WHEN publication_conflict_count<9223372036854775807::bigint THEN publication_conflict_count+1 ELSE publication_conflict_count END,publication_conflict_digest=contradiction,
  publication_conflict_seen=publication_conflict_seen||jsonb_build_object(contradiction,true),
  publication_evidence_version=publication_evidence_version+1,updated_at=clock_timestamp() WHERE publication_permit_id=e.publication_permit_id;
 UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=e.acquisition_id AND state='running' AND record_state='full';
 RETURN jsonb_build_object('recorded',true,'conflict',true,'evidence_version',e.publication_evidence_version+1);
END $$;

CREATE FUNCTION file_record_receipt(p_permit text, p_receipt text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE e public.file_operations; envelope jsonb; identity jsonb; payload jsonb; checkpoint jsonb;
 closure jsonb; step text; old_slot jsonb; new_slot jsonb; proof jsonb; covered jsonb:='[]'::jsonb;
 root_id text; changed boolean:=false; at timestamptz; class text:='unknown'; closed jsonb; resolution jsonb;
 checkpoint_bytes text; closure_bytes text; proof_bytes text; proof_index jsonb; resolution_bytes text; checkpoint_digest text; closure_digest text;
BEGIN
 IF public.plan_canonical_valid(p_receipt) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CANONICAL_INVALID'; END IF;
 envelope:=p_receipt::jsonb;identity:=envelope->'identity';payload:=envelope->'payload';checkpoint:=payload->'checkpoint';closure:=payload->'closure';
 SELECT acquisition_id INTO STRICT root_id FROM public.file_operations WHERE publication_permit_id=p_permit;
 PERFORM id FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit FOR UPDATE;
 IF public.observed_closed(envelope,ARRAY['identity','emitter','observed_at','payload']) IS NOT TRUE
 OR identity IS DISTINCT FROM jsonb_build_object('schema_version',1,'permit_id',e.publication_permit_id,'root_id',e.acquisition_id,
  'original_work_id',e.publication_work_item_id,'fencing_token',e.publication_fencing_token,'executor_session',e.publication_executor_session,'spec_digest',e.publication_spec_digest)
 OR envelope->'emitter' IS DISTINCT FROM jsonb_build_object('mode','dispatch_session','session',e.publication_executor_session)
 OR public.observed_closed(payload,ARRAY['checkpoint','closure']) IS NOT TRUE
 OR public.observed_closed(checkpoint,ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup']) IS NOT TRUE
 OR public.observed_closed(closure,ARRAY['state','value','reason_code']) IS NOT TRUE
 OR e.publication_dispatch_state='prepared' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_IDENTITY_INVALID'; END IF;
 IF public.file_wire_time_valid(envelope->'observed_at') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
 at:=(envelope->>'observed_at')::timestamptz;
 IF at IS NULL OR at<e.publication_dispatch_started_at THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
 checkpoint_bytes:=public.file_json_field(public.file_json_field(p_receipt::json,'payload')::json,'checkpoint');
 closure_bytes:=public.file_json_field(public.file_json_field(public.file_json_field(p_receipt::json,'payload')::json,'closure')::json,'value');
 proof_index:=e.publication_step_proof_bytes;
 FOREACH step IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
  old_slot:=e.publication_step_checkpoint->step;new_slot:=checkpoint->step;proof:=new_slot->'proof';
  IF public.observed_closed(new_slot,ARRAY['state','proof','observed_at','reason_code']) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_SHAPE'; END IF;
  IF old_slot->>'state' IN ('started','unknown','confirmed','no_effect') THEN covered:=covered||jsonb_build_array(step); END IF;
  IF new_slot IS NOT DISTINCT FROM old_slot THEN CONTINUE; END IF;
  proof_bytes:=public.file_json_field(public.file_json_field(checkpoint_bytes::json,step)::json,'proof');
  IF new_slot->>'state' IN ('unknown','confirmed','no_effect') THEN
   IF public.file_step_authorized_at(e,step) IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_NOT_GRANTED'; END IF;
   IF public.file_wire_time_valid(new_slot->'observed_at') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
   IF ((new_slot->>'observed_at')::timestamptz BETWEEN public.file_step_authorized_at(e,step) AND at) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TIME_INVALID'; END IF;
  END IF;
  IF old_slot->>'state' IN ('confirmed','no_effect') AND new_slot->>'state' IN ('confirmed','no_effect') THEN
   IF new_slot->>'state'=old_slot->>'state' AND proof IS NOT DISTINCT FROM old_slot->'proof' THEN checkpoint:=jsonb_set(checkpoint,ARRAY[step],old_slot);CONTINUE; END IF;
   IF proof_index->>step IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_CONFLICT_ORIGINAL_BYTES_MISSING'; END IF;
   RETURN public.file_record_conflict(e.publication_permit_id,step,public.file_proof_digest('file-step-proof:v1',proof_index->>step),public.file_proof_digest('file-step-proof:v1',proof_bytes),at);
  END IF;
  IF (old_slot->>'state' IN ('started','unknown') AND new_slot->>'state' IN ('confirmed','unknown')
   AND (new_slot->>'observed_at')::timestamptz>=(old_slot->>'observed_at')::timestamptz
   AND (new_slot->>'observed_at')::timestamptz<=at) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_STEP_NOT_GRANTED'; END IF;
  IF new_slot->>'state'='unknown' THEN
   IF proof IS DISTINCT FROM 'null'::jsonb OR (new_slot->>'reason_code' ~ '^[A-Z][A-Z0-9_]{0,127}$') IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_UNKNOWN_SHAPE'; END IF;
  ELSE
   IF new_slot->'reason_code' IS DISTINCT FROM 'null'::jsonb THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CONFIRMED_SHAPE'; END IF;
   CASE step
   WHEN 'stage_temp' THEN
    IF public.observed_closed(proof,ARRAY['kind','object','parent','verified_source_digest','flushed']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'created_temp' OR proof->'flushed' IS DISTINCT FROM 'true'::jsonb
     OR proof->'object'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR e.publication_frozen_objects_canonical IS NULL OR proof->>'verified_source_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source')) THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_TEMP_BINDING'; END IF;
   WHEN 'reserve_version' THEN
    IF public.observed_closed(proof,ARRAY['kind','object','parent','base_path','candidate_ordinal','reservation_method']) IS NOT TRUE
     OR e.collision_policy<>'version' OR proof->>'kind' IS DISTINCT FROM 'reserved_path' OR proof->>'reservation_method' IS DISTINCT FROM 'exclusive_create'
     OR proof->>'base_path' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'canonical_path'
     OR (proof->>'candidate_ordinal')::bigint NOT BETWEEN 1 AND 1001
     OR proof->'object'->'location'->>'canonical_path' IS DISTINCT FROM public.file_version_candidate(e.publication_frozen_objects->'target'->>'canonical_path',(proof->>'candidate_ordinal')::bigint)
     OR public.file_location_valid(proof->'object'->'location') IS NOT TRUE
     OR proof->'object'->'location'->>'root' IS DISTINCT FROM e.publication_frozen_objects->'target'->>'root'
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->'object'->>'file_type' IS DISTINCT FROM 'regular' OR proof->'object'->'size' IS DISTINCT FROM '0'::jsonb
     OR proof->'object'->'identity'->>'state' IS DISTINCT FROM 'known'
     OR public.observed_identity_valid(proof->'object'->'identity'->'value') IS NOT TRUE
     OR proof->'object'->'content'->'value'->>'sha256' IS DISTINCT FROM 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_RESERVATION_BINDING'; END IF;
   WHEN 'publish_target' THEN
    IF public.observed_closed(proof,ARRAY['kind','source_evidence_digest','target','parent','atomic_method','displaced','dependency_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'published' OR proof->>'atomic_method' IS DISTINCT FROM (CASE WHEN e.collision_policy IN ('version','overwrite') THEN 'exchange' ELSE 'no_replace' END)
     OR proof->'target'->'location' IS DISTINCT FROM (CASE WHEN e.collision_policy='version' THEN checkpoint->'reserve_version'->'proof'->'object'->'location' ELSE e.publication_frozen_objects->'target' END)
     OR proof->'parent' IS DISTINCT FROM e.publication_frozen_objects->'target_parent'
     OR proof->>'dependency_digest' IS DISTINCT FROM e.publication_dependency_digest
     OR e.publication_frozen_objects_canonical IS NULL OR proof->>'source_evidence_digest' IS DISTINCT FROM public.file_proof_digest('file-source-evidence:v1',public.file_json_field(e.publication_frozen_objects_canonical::json,'source'))
     OR (e.collision_policy NOT IN ('version','overwrite') AND proof->'displaced' IS DISTINCT FROM jsonb_build_object('state','absent','value',NULL,'reason_code',NULL))
     OR (e.collision_policy='overwrite' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
      OR proof->'displaced'->'value'->'identity'->'value'->>'device' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->>'device'
      OR proof->'displaced'->'value'->'identity'->'value'->>'inode' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->>'inode'
      OR proof->'displaced'->'value'->'identity'->'value'->'generation_nanos' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'identity'->'value'->'generation_nanos'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'size'
      OR proof->'displaced'->'value'->'content'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'content'->'value'->>'sha256' IS DISTINCT FROM e.publication_frozen_objects->'expected_target'->'content'->'value'->>'sha256'))
     OR (e.collision_policy='version' AND (proof->'displaced'->>'state' IS DISTINCT FROM 'known'
      OR proof->'displaced'->'value'->'location' IS DISTINCT FROM e.publication_frozen_objects->'temp'
      OR proof->'displaced'->'value'->'identity'->'value'->>'inode' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'identity'->'value'->>'inode'
      OR proof->'displaced'->'value'->'identity'->'value'->>'device' IS DISTINCT FROM checkpoint->'reserve_version'->'proof'->'object'->'identity'->'value'->>'device'
      OR proof->'displaced'->'value'->'size' IS DISTINCT FROM '0'::jsonb)) THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_PUBLICATION_BINDING'; END IF;
   WHEN 'source_cleanup','temp_cleanup' THEN
    IF public.file_final_object_valid(proof->'before') IS NOT TRUE
     OR public.file_location_valid(proof->'after'->'location') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'after'->'parent') IS NOT TRUE
     OR public.file_wire_identity_valid(proof->'parent') IS NOT TRUE
     OR public.file_wire_time_valid(proof->'after'->'observed_at') IS NOT TRUE THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
    IF public.observed_closed(proof,ARRAY['kind','before','parent','after','mutation_receipt_digest']) IS NOT TRUE
     OR proof->>'kind' IS DISTINCT FROM 'removed_object'
     OR public.observed_closed(proof->'after',ARRAY['location','parent','observed_at','method']) IS NOT TRUE
     OR proof->'after'->>'method' IS DISTINCT FROM 'lstat_enoent'
     OR proof->'before' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source' WHEN e.collision_policy IN ('version','overwrite') THEN checkpoint->'publish_target'->'proof'->'displaced'->'value' ELSE checkpoint->'stage_temp'->'proof'->'object' END)
     OR proof->'parent' IS DISTINCT FROM (CASE WHEN step='source_cleanup' THEN e.publication_frozen_objects->'source_parent' ELSE e.publication_frozen_objects->'target_parent' END)
     OR proof->'after'->'parent' IS DISTINCT FROM proof->'parent'
     OR proof->'after'->'location' IS DISTINCT FROM proof->'before'->'location'
     OR proof->>'mutation_receipt_digest' IS DISTINCT FROM public.file_proof_digest('file-mutation-receipt:v1',
      '{"before":'||public.file_json_field(proof_bytes::json,'before')||',"permit_id":'||to_json(e.publication_permit_id)::text||',"removed_at":'||to_json(checkpoint->step->>'observed_at')::text||',"step":'||to_json(step)::text||'}') THEN
     RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLEANUP_BINDING'; END IF;
   END CASE;
   IF step IN ('stage_temp','publish_target') THEN
    DECLARE object jsonb:=CASE WHEN step='stage_temp' THEN proof->'object' ELSE proof->'target' END;
    BEGIN
     IF public.observed_closed(object,ARRAY['location','file_type','size','identity','content','observed_at','observation_source']) IS NOT TRUE
      OR object->>'file_type' IS DISTINCT FROM 'regular'
      OR object->'size' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size'
      OR object->'identity'->>'state' IS DISTINCT FROM 'known'
      OR public.observed_identity_valid(object->'identity'->'value') IS NOT TRUE
      OR object->'content'->>'state' IS DISTINCT FROM 'known'
      OR object->'content'->'value'->>'kind' IS DISTINCT FROM 'sha256_file'
      OR object->'content'->'value'->'object_identity' IS DISTINCT FROM object->'identity'->'value'
      OR object->'content'->'value'->'sha256' IS DISTINCT FROM e.publication_frozen_objects->'source'->'content'->'value'->'sha256' THEN
      RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_OBJECT_INVALID'; END IF;
    END;
   END IF;
  END IF;
  IF new_slot->>'state'='confirmed' THEN proof_index:=proof_index||jsonb_build_object(step,proof_bytes); END IF;
  changed:=true;
 END LOOP;
 closed:=e.publication_closed_proof;
 IF closure->>'state'='known' THEN
  IF public.file_wire_time_valid(closure->'value'->'joined_at') IS NOT TRUE OR public.file_wire_id_valid(closure->'value'->'boot_nonce') IS NOT TRUE OR public.file_wire_id_valid(closure->'value'->'call_nonce') IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF EXISTS(SELECT 1 FROM jsonb_each(checkpoint) s WHERE s.value->>'state' IN ('confirmed','unknown','no_effect') AND ((s.value->>'observed_at')::timestamptz<=(closure->'value'->>'joined_at')::timestamptz) IS NOT TRUE) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF public.observed_closed(closure->'value',ARRAY['kind','executor_session','boot_nonce','call_nonce','covered_steps','joined_at','observer_version']) IS NOT TRUE
   OR closure->'reason_code' IS DISTINCT FROM 'null'::jsonb OR closure->'value'->>'kind' IS DISTINCT FROM 'local_join'
   OR closure->'value'->>'executor_session' IS DISTINCT FROM e.publication_executor_session
   OR e.publication_executor_session IS DISTINCT FROM (closure->'value'->>'boot_nonce')||':'||(closure->'value'->>'call_nonce')
   OR closure->'value'->'covered_steps' IS DISTINCT FROM covered
   OR char_length(closure->'value'->>'boot_nonce') NOT BETWEEN 1 AND 128
   OR char_length(closure->'value'->>'call_nonce') NOT BETWEEN 1 AND 128
   OR ((closure->'value'->>'joined_at')::timestamptz BETWEEN e.publication_dispatch_started_at AND at) IS NOT TRUE
  OR COALESCE(jsonb_typeof(closure->'value'->'observer_version'),'null') IS DISTINCT FROM 'string' OR COALESCE(char_length(closure->'value'->>'observer_version'),0) NOT BETWEEN 1 AND 128 THEN -- §13.16.35 Q11: null/type-confusion-safe (three-valued OR chain)
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_INVALID'; END IF;
  IF closed IS NOT NULL AND closed IS DISTINCT FROM closure->'value' THEN
   RETURN public.file_record_conflict(e.publication_permit_id,'closure',public.file_proof_digest('file-closure:v1',e.publication_closure_canonical),public.file_proof_digest('file-closure:v1',closure_bytes),at); END IF;
  IF closed IS NULL THEN changed:=true;closed:=closure->'value'; END IF;
 ELSIF (closure->>'state'='unavailable' AND closure->'value'='null'::jsonb AND closure->>'reason_code' ~ '^[A-Z][A-Z0-9_]{0,127}$') IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_RECEIPT_CLOSURE_SHAPE'; END IF;
 IF NOT changed THEN RETURN jsonb_build_object('recorded',false,'evidence_version',e.publication_evidence_version); END IF;
 -- Only complete facts derive a confirmed disposition. Real channel closure
 -- with remaining unknown slots stays unknown; joining never implies success.
 IF closed IS NOT NULL AND NOT EXISTS(SELECT 1 FROM jsonb_each(checkpoint) s WHERE s.value->>'state' NOT IN ('not_required','confirmed')) THEN class:='confirmed'; END IF;
 resolution_bytes:=e.publication_resolution_canonical;resolution:=e.publication_resolution_proof;
 IF class='confirmed' AND e.publication_conflict_evidence IS NULL AND resolution IS NULL THEN
  checkpoint_digest:=public.file_proof_digest('file-checkpoint:v1',checkpoint_bytes);
  closure_digest:=public.file_proof_digest('file-closure:v1',COALESCE(e.publication_closure_canonical,closure_bytes));
  resolution_bytes:='{"checkpoint_digest":"'||checkpoint_digest||'","closure_digests":["'||closure_digest||'"],"effect_class":"confirmed","schema_version":1}';
  resolution:=resolution_bytes::jsonb;
 END IF;
 at:=clock_timestamp(); -- first durable unknown uses DB time, not caller time
 UPDATE public.file_operations SET publication_step_checkpoint=checkpoint,publication_closed_proof=closed,
  publication_closure_receipt_observed_at=CASE WHEN closed->>'kind'='local_join' THEN COALESCE(e.publication_closure_receipt_observed_at,(envelope->>'observed_at')::timestamptz) ELSE e.publication_closure_receipt_observed_at END,
  publication_step_proof_bytes=proof_index,publication_closure_canonical=CASE WHEN closed IS NOT NULL THEN COALESCE(e.publication_closure_canonical,closure_bytes) ELSE NULL END,
  publication_resolution_proof=resolution,publication_resolution_canonical=resolution_bytes,
  publication_resolution_checkpoint_canonical=CASE WHEN resolution IS NOT NULL THEN COALESCE(e.publication_resolution_checkpoint_canonical,checkpoint_bytes) ELSE NULL END,
  publication_dispatch_state=CASE WHEN closed IS NOT NULL THEN 'closed' ELSE publication_dispatch_state END,publication_effect_class=class,
  publication_unknown_since=CASE WHEN class='unknown' THEN COALESCE(publication_unknown_since,at) ELSE publication_unknown_since END,
  publication_reconcile_until=CASE WHEN class='unknown' THEN COALESCE(publication_reconcile_until,COALESCE(publication_unknown_since,at)+make_interval(secs=>publication_reconcile_wall_seconds)) ELSE publication_reconcile_until END,
  publication_evidence_version=publication_evidence_version+1,updated_at=clock_timestamp() WHERE publication_permit_id=e.publication_permit_id;
 UPDATE public.acquisitions SET facts_revision=facts_revision+1 WHERE id=root_id AND state='running' AND record_state='full';
 RETURN jsonb_build_object('recorded',true,'evidence_version',e.publication_evidence_version+1);
END $_$;

CREATE FUNCTION file_start_attempt(p_operation text, p_work text, p_token bigint, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; now_at timestamptz; ticket jsonb; idx jsonb; old_entry jsonb; entry jsonb; ordinal integer;
BEGIN
 o:=public.file_execution_lock(p_operation,p_work,p_token);
 IF p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_SESSION_SHAPE'; END IF;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(o.file_attempt_index) LOOP
  IF old_entry->>'work_item_id'=p_work AND (old_entry->>'token')::bigint=p_token THEN
   RETURN jsonb_build_object('granted',false,'ticket',o.file_attempt_ticket,'reason_code','ATTEMPT_ALREADY_ISSUED');
  END IF;
 END LOOP;
 now_at:=GREATEST(clock_timestamp(),o.budget_clock_at,o.file_ready_at);
 IF now_at>=o.file_wall_until OR o.file_attempts_started>=o.file_max_attempts THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_BUDGET_EXHAUSTED'; END IF;
 IF EXISTS(SELECT 1 FROM public.file_operations WHERE acquisition_id=o.acquisition_id AND id=o.id
  AND publication_permit_id IS NOT NULL AND (NOT publication_retry_safe OR publication_conflict_evidence IS NOT NULL)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_UNRESOLVED_EFFECT'; END IF;
 idx:='[]'::jsonb;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(o.file_attempt_index) LOOP
  IF old_entry->>'state'='issued' THEN old_entry:=jsonb_set(old_entry,'{state}','"superseded"'); END IF;
  idx:=idx||jsonb_build_array(old_entry);
 END LOOP;
 ordinal:=o.file_attempts_started+1;
 entry:=jsonb_build_object('work_item_id',p_work,'token',p_token,'ordinal',ordinal,'session_digest','sha256:'||encode(sha256(convert_to(p_session,'UTF8')),'hex'),'state','issued');
 ticket:=(entry-'session_digest')||jsonb_build_object('executor_session',p_session,'started_at',now_at,'attempt_until',LEAST(now_at+make_interval(secs=>o.file_attempt_timeout_seconds),o.file_wall_until));
 UPDATE public.file_operations SET budget_clock_at=now_at,file_attempts_started=ordinal,file_attempt_index=idx||jsonb_build_array(entry),file_attempt_ticket=ticket WHERE id=o.id;
 RETURN jsonb_build_object('granted',true,'ticket',ticket);
END $$;

CREATE FUNCTION file_start_dispatch(p_operation text, p_work text, p_token bigint, p_permit text, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; now_at timestamptz;
BEGIN
 e:=public.file_dispatch_lock(p_operation,p_work,p_token,p_permit,p_session);
 IF e.publication_dispatch_state<>'prepared' THEN
  RETURN jsonb_build_object('granted',false,'permit_id',e.publication_permit_id,'reason_code','DISPATCH_ALREADY_STARTED'); END IF;
 now_at:=GREATEST(clock_timestamp(),e.publication_prepared_at,(SELECT budget_clock_at FROM public.file_operations WHERE id=p_operation));
 IF now_at>=(SELECT (file_attempt_ticket->>'attempt_until')::timestamptz FROM public.file_operations WHERE id=p_operation) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_DISPATCH_NOT_QUALIFIED'; END IF;
 UPDATE public.file_operations SET budget_clock_at=GREATEST(budget_clock_at,now_at) WHERE id=p_operation;
 UPDATE public.file_operations SET publication_dispatch_state='dispatching',publication_dispatch_started_at=now_at,publication_evidence_version=publication_evidence_version+1,updated_at=now_at WHERE publication_permit_id=e.publication_permit_id;
 RETURN jsonb_build_object('granted',true,'permit_id',e.publication_permit_id,'executor_session',e.publication_executor_session,'gate_version',e.publication_evidence_version+1,'gate_db_now',now_at,'attempt_until',(SELECT file_attempt_ticket->>'attempt_until' FROM public.file_operations WHERE id=p_operation));
END $$;

CREATE FUNCTION file_start_step(p_operation text, p_work text, p_token bigint, p_permit text, p_session text, p_step text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE e public.file_operations; now_at timestamptz; slot jsonb;
BEGIN
 IF p_step IS NULL OR p_step NOT IN ('stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_UNKNOWN'; END IF;
 e:=public.file_dispatch_lock(p_operation,p_work,p_token,p_permit,p_session);
 IF e.publication_dispatch_state<>'dispatching' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_NOT_DISPATCHING'; END IF;
 slot:=e.publication_step_checkpoint->p_step;
 IF slot->>'state' IN ('started','confirmed','no_effect','unknown','not_required') THEN
 RETURN jsonb_build_object('granted',false,'permit_id',e.publication_permit_id,'step',p_step,'reason_code','STEP_NOT_NEW'); END IF;
 IF (slot->>'state'='not_started' AND slot->'proof'='null'::jsonb AND slot->'observed_at'='null'::jsonb AND slot->'reason_code'='null'::jsonb) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_SHAPE'; END IF;
 IF public.file_step_authorized_at(e,p_step) IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 IF EXISTS(SELECT 1 FROM jsonb_each(e.publication_step_checkpoint) s WHERE s.key<>p_step AND s.value->>'state' IN ('started','unknown'))
 OR (p_step='publish_target' AND EXISTS(SELECT 1 FROM jsonb_each(e.publication_step_checkpoint) s WHERE s.key IN ('stage_temp','reserve_version') AND s.value->>'state' NOT IN ('confirmed','not_required')))
 OR (p_step='source_cleanup' AND (e.publication_step_checkpoint->'publish_target'->>'state'='confirmed') IS NOT TRUE) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_PREFIX_INCOMPLETE'; END IF;
 now_at:=GREATEST(clock_timestamp(),e.publication_dispatch_started_at,(SELECT budget_clock_at FROM public.file_operations WHERE id=p_operation));
 IF now_at>=(SELECT (file_attempt_ticket->>'attempt_until')::timestamptz FROM public.file_operations WHERE id=p_operation) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_DISPATCH_NOT_QUALIFIED'; END IF;
 UPDATE public.file_operations SET budget_clock_at=GREATEST(budget_clock_at,now_at) WHERE id=p_operation;
 UPDATE public.file_operations SET
 publication_stage_temp_started_at=CASE WHEN p_step='stage_temp' THEN now_at ELSE publication_stage_temp_started_at END,
 publication_reserve_version_started_at=CASE WHEN p_step='reserve_version' THEN now_at ELSE publication_reserve_version_started_at END,
 publication_publish_target_started_at=CASE WHEN p_step='publish_target' THEN now_at ELSE publication_publish_target_started_at END,
 publication_source_cleanup_started_at=CASE WHEN p_step='source_cleanup' THEN now_at ELSE publication_source_cleanup_started_at END,
 publication_temp_cleanup_started_at=CASE WHEN p_step='temp_cleanup' THEN now_at ELSE publication_temp_cleanup_started_at END,
 publication_step_checkpoint=jsonb_set(publication_step_checkpoint,ARRAY[p_step],jsonb_build_object('state','started','proof',NULL,'observed_at',now_at,'reason_code',NULL)),
 publication_evidence_version=publication_evidence_version+1,updated_at=now_at WHERE publication_permit_id=e.publication_permit_id;
 RETURN jsonb_build_object('granted',true,'permit_id',e.publication_permit_id,'step',p_step,'executor_session',e.publication_executor_session,'gate_version',e.publication_evidence_version+1,'started_at',now_at,'gate_db_now',now_at,'attempt_until',(SELECT file_attempt_ticket->>'attempt_until' FROM public.file_operations WHERE id=p_operation));
END $$;

CREATE FUNCTION file_step_authorization_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE s text; a timestamptz; prior timestamptz; slot jsonb;
BEGIN
 FOREACH s IN ARRAY ARRAY['stage_temp','reserve_version','publish_target','source_cleanup','temp_cleanup'] LOOP
 a:=public.file_step_authorized_at(NEW,s);slot:=NEW.publication_step_checkpoint->s;
 IF TG_OP='INSERT' THEN
  IF a IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 ELSIF OLD.publication_permit_id IS NOT DISTINCT FROM NEW.publication_permit_id THEN
  prior:=public.file_step_authorized_at(OLD,s);
  IF (prior IS NOT NULL AND a IS DISTINCT FROM prior) OR (prior IS NULL AND a IS NOT NULL AND
   (OLD.publication_step_checkpoint->s->>'state'='not_started' AND slot->>'state'='started') IS NOT TRUE) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 END IF;
 IF a IS NOT NULL AND (NOT isfinite(a) OR (NEW.publication_dispatch_started_at<=a) IS NOT TRUE
  OR slot->>'state' IN ('not_started','not_required')
  OR (slot->>'state'='started' AND ((slot->>'observed_at')::timestamptz=a) IS NOT TRUE)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 END LOOP;
 IF TG_OP='INSERT' AND NEW.publication_closure_receipt_observed_at IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 IF (NEW.publication_closed_proof->>'kind'='local_join') IS TRUE AND NEW.publication_closure_receipt_observed_at IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 IF TG_OP='UPDATE' AND OLD.publication_permit_id IS NOT DISTINCT FROM NEW.publication_permit_id AND OLD.publication_closure_receipt_observed_at IS NOT NULL AND NEW.publication_closure_receipt_observed_at IS DISTINCT FROM OLD.publication_closure_receipt_observed_at THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 IF NEW.publication_closure_receipt_observed_at IS NOT NULL AND (NOT isfinite(NEW.publication_closure_receipt_observed_at) OR (NEW.publication_closed_proof->>'kind'='local_join' AND (NEW.publication_closed_proof->>'joined_at')::timestamptz<=NEW.publication_closure_receipt_observed_at) IS NOT TRUE) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_STEP_AUTHORIZATION_INVALID'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION file_success_handoff(p_operation text, p_work text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; child public.file_operations;
 inherited jsonb; new_work text; created integer:=0;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (w.owner_id=o.id AND w.acquisition_id=o.acquisition_id AND NOT w.cancel_requested
  AND EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=o.file_ref AND r.operation_id=o.id AND r.outcome='succeeded')) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_HANDOFF_SOURCE_INVALID'; END IF;
 inherited:=CASE WHEN w.input_snapshot ? 'one_shot' THEN jsonb_build_object('one_shot',w.input_snapshot->'one_shot') ELSE '{}'::jsonb END;
 FOR child IN SELECT op.* FROM public.file_operations op
 WHERE op.acquisition_id=o.acquisition_id AND op.bound_video_operation_id=o.id
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=o.acquisition_id AND r.file_ref=op.file_ref AND r.outcome IS NOT NULL)
 ORDER BY op.id COLLATE "C" LOOP
 IF EXISTS(SELECT 1 FROM public.work_item_history existing WHERE existing.owner_type='file_operation' AND existing.owner_id=child.id AND existing.kind='file_execute') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_HANDOFF_WORK_CONFLICT'; END IF;
 SELECT public.work_item_create(gen_random_uuid()::text,'file_operation',child.id,'file_execute',
 child.idempotency_key,child.file_max_attempts,inherited||jsonb_build_object('operation_id',child.id,'artifact_id',child.artifact_id,'artifact_file_id',child.artifact_file_id)
 ||CASE WHEN child.collision_policy='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END,
 child.version,0::double precision,7::smallint) INTO new_work;
 IF new_work IS NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_HANDOFF_WORK_CONFLICT'; END IF;
 created:=created+1;
 END LOOP;
 RETURN created;
END $$;

CREATE FUNCTION file_success_notification(p_operation text, p_permit text, p_fact jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE o public.file_operations; e public.file_operations; f public.artifact_files;
 input jsonb; src jsonb; location jsonb; title text; release_id text; name text; base text; extension text;
 relative text; directory text; output_name text; root_path text; target_path text;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 SELECT * INTO STRICT f FROM public.artifact_files WHERE artifact_id=o.artifact_id AND file_ref=o.file_ref;
 SELECT q.input INTO STRICT input FROM public.acquisitions q WHERE q.id=o.acquisition_id;
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
 root_path:=rtrim(location->>'root','/');target_path:=location->>'canonical_path';
 -- Registered publication locations are canonical absolute paths. Their exact
 -- relationship is revalidated by proof before this projector is admitted.
 IF left(target_path,length(root_path)+1)=root_path||'/' THEN
 relative:=substring(target_path FROM length(root_path)+2);
 IF relative='' OR relative='..' OR left(relative,3)='../' OR public.file_notification_text(relative) IS NULL THEN relative:=NULL; END IF;
 END IF;
 IF relative IS NOT NULL THEN
 output_name:=regexp_replace(relative,'^.*/','');
 directory:=CASE WHEN strpos(relative,'/')>0 THEN regexp_replace(relative,'/[^/]*$','') ELSE '' END;
 ELSE output_name:=name; END IF;
 RETURN jsonb_build_object(
 'anime.id',NULL,'anime.title',public.file_notification_text(title),'anime.custom_title',NULL,
 'release.id',public.file_notification_text(release_id),
 'release.title',public.file_notification_text(CASE WHEN src->'title'->>'state'='known' THEN src->'title'->>'value' END),
 'release.group_name',public.file_notification_text(CASE WHEN src->'group'->>'state'='known' THEN src->'group'->>'value' END),
 'event.type','file_operation_completed','result.outcome','success','result.reason','file_operation_completed',
 'file.name',public.file_notification_text(name),'file.basename',public.file_notification_text(base),'file.extension',public.file_notification_text(lower(extension)),
 'file.size_bytes',f.size_bytes,'file.media_kind',public.file_notification_text(f.media_kind),
 'file.container',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'container')='string' THEN f.facts->>'container' END),
 'file.resolution',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'resolution')='string' THEN f.facts->>'resolution' END),
 'file.video_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'video_codec')='string' THEN f.facts->>'video_codec' END),
 'file.audio_codec',public.file_notification_text(CASE WHEN jsonb_typeof(f.facts->'audio_codec')='string' THEN f.facts->>'audio_codec' END),
 'output.directory',public.file_notification_text(directory),'output.filename',public.file_notification_text(output_name),'output.relative_path',public.file_notification_text(relative),
 'file_operation.kind',public.file_notification_text(input->'delivery'->>'operation'),
 'file_operation.collision_policy',public.file_notification_text(e.collision_policy))
 || public.notification_input_facts(input)
 || public.notification_episode_facts(p_fact->'identity_coverage'->'identity')
 || public.notification_title_facts(input)
 || public.notification_episode_title_facts(o.frozen_render_snapshot)
 || public.notification_value_facts(input->'config'->'vars'->'output',
   (SELECT r.source_facts FROM public.releases r WHERE r.id=release_id));
END $_$;

CREATE FUNCTION filefact_source_relation_valid(p_root text, p_artifact text, p_ref text, p_fact jsonb) RETURNS boolean
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
 OR p_fact->'quality' IS DISTINCT FROM i->'media'->'quality'
 OR p_fact->offset_group->'video_count' IS DISTINCT FROM video_count THEN RETURN false; END IF;
 IF offset_group='episode_offset' THEN
  expected:=CASE WHEN i->'media'->'identity'->>'state'='known' THEN jsonb_build_object('state','known','value',i->'media'->'identity'->'value'->'offset','reason_code',NULL) ELSE jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') END;
  IF p_fact->'episode_offset'->'offset' IS DISTINCT FROM expected THEN RETURN false; END IF;
 ELSE
  IF p_fact->'source_offset'->'used_offset' IS DISTINCT FROM i->'media'->'source_offset' THEN RETURN false; END IF;
  FOREACH k IN ARRAY ARRAY['source','override'] LOOP
  IF p_fact->'source_offset'->k IS DISTINCT FROM jsonb_build_object('state','unavailable','value',NULL,'reason_code','FACT_UNAVAILABLE') THEN RETURN false; END IF; END LOOP;
 END IF;
 IF p_fact->'identity_coverage'->>'coverage_schema' IS DISTINCT FROM 'episode-identity-v1'
 OR p_fact->'identity_coverage'->'coverage_keys' IS DISTINCT FROM (CASE WHEN i->'media'->'identity'->>'state'='known' THEN i->'media'->'identity'->'value'->'coverage_keys' ELSE '[]'::jsonb END) THEN RETURN false; END IF;
 SELECT render_snapshot INTO STRICT render FROM public.acquisition_files WHERE acquisition_id=p_root AND file_ref=p_ref;
 binding:=render->'binding_facts'; replay:=render->'render_facts';
 FOREACH k IN ARRAY ARRAY['File.Media.Resolution','File.Media.VideoCodec','File.Media.Container','Parsed.Language.Code','Parsed.Episode.Display','Source.GroupName','Derived.GroupName','Release.OriginalTitle','Release.FrozenEpisode.Display','Anime.BGM.WorkID','Anime.TMDB.WorkID'] LOOP
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

CREATE FUNCTION file_success_projection(p_operation text, p_permit text, p_file_fact text) RETURNS void
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
  AND published_object->'content'->'value'->>'sha256'=e.publication_frozen_objects->'source'->'content'->'value'->>'sha256'
  AND public.vars_filefact_valid(o.acquisition_id,fact)
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

CREATE FUNCTION file_success_atomic_body(p_operation text, p_work text, p_permit text, p_file_fact text, p_notification text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE o public.file_operations; w public.work_item_history; e public.file_operations;
 next_version bigint; affected integer;
BEGIN
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 SELECT * INTO STRICT e FROM public.file_operations WHERE publication_permit_id=p_permit;
 -- Notification ledger 029 accepts ordinary bounded JSON objects, not Plan
 -- canonical bytes. Frozen construction/sanitization is independently checked
 -- by the fixed caller before admission; this helper remains private.
 IF p_notification IS NULL OR jsonb_typeof(p_notification::jsonb) IS DISTINCT FROM 'object'
 OR octet_length(p_notification) NOT BETWEEN 1 AND 65536
 OR public.file_notification_safe(p_notification::jsonb) IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_INVALID'; END IF;
 -- Bind stable result fields before any business write. Full frozen display
 -- field reconstruction remains required before granting this command.
 IF p_notification::jsonb->>'event.type' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->>'result.outcome' IS DISTINCT FROM 'success'
 OR p_notification::jsonb->>'result.reason' IS DISTINCT FROM 'file_operation_completed'
 OR p_notification::jsonb->'anime.custom_title' IS DISTINCT FROM 'null'::jsonb
 OR p_notification::jsonb->>'file_operation.kind' IS DISTINCT FROM e.operation
 OR p_notification::jsonb->>'file_operation.collision_policy' IS DISTINCT FROM e.collision_policy
 OR p_notification::jsonb->'file.size_bytes' IS DISTINCT FROM e.publication_frozen_objects->'source'->'size' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 IF p_notification::jsonb IS DISTINCT FROM public.file_success_notification(p_operation,p_permit,p_file_fact::jsonb) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END IF;
 PERFORM public.file_assert_final_proof(p_permit);
 PERFORM public.file_success_projection(p_operation,p_permit,p_file_fact);
 IF w.cancel_requested THEN
  affected:=public.file_cancelled_dependency_suppression(p_operation,p_work);
 ELSE
  affected:=public.file_success_handoff(p_operation,p_work);
 END IF;
 -- The typed exchange proof is the equivalence protocol the 063
 -- completion-evidence checkpoint column records for a replace delivery:
 -- the publish_target proof carries the atomic_method='exchange' receipt and
 -- the displaced incumbent bound to the frozen expected_target (166), and
 -- temp_cleanup's before binding re-freezes the same object. Registering it
 -- here keeps the legacy checkpoint column a complete completion record
 -- without touching the frozen 063 bytes.
 UPDATE public.file_operations SET state='completed',failure_code=NULL,failure_reason=NULL,
 target_fingerprint=jsonb_build_object('algorithm','sha256','value',substring(e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'content'->'value'->>'sha256' FROM 8),
  'size_bytes',COALESCE(e.publication_step_checkpoint->'publish_target'->'proof'->'target'->'size',e.publication_frozen_objects->'source'->'size')),
 publication_checkpoint=e.publication_step_checkpoint->'publish_target'->'proof',
 observed_at=clock_timestamp(),version=version+1 WHERE id=p_operation RETURNING version INTO next_version;
 INSERT INTO public.notification_admissions(id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,
 managed_anime_id,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
 VALUES(gen_random_uuid()::text,'file_operation_completed','file_operation',o.id,next_version,o.idempotency_key,
 NULL,clock_timestamp(),'plugin-event-catalog-v1','notification-template-catalog-v1',p_notification::jsonb,'pending_target_resolution');
 IF public.work_item_finish('succeeded',next_version,p_work)<>1 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_FINALIZATION_WORK_MISSING'; END IF;
 PERFORM public.download_ensure_settlement(o.acquisition_id);
 RETURN jsonb_build_object('state','completed','owner_version',next_version,'affected_dependencies',affected);
END $$;

CREATE FUNCTION guard_file_attempt_budget() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry jsonb; n integer:=0; prior jsonb; active jsonb; identities text[]:=ARRAY[]::text[]; identity text;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.budget_clock_at IS NULL THEN NEW.budget_clock_at:=NEW.file_ready_at; END IF;
  IF NEW.file_attempts_started<>0 OR NEW.file_attempt_ticket IS NOT NULL OR NEW.file_attempt_index<>'[]'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INITIAL_SHAPE'; END IF;
 ELSE
  IF ROW(NEW.file_ready_at,NEW.file_wall_until,NEW.file_max_attempts,NEW.file_attempt_timeout_seconds) IS DISTINCT FROM
     ROW(OLD.file_ready_at,OLD.file_wall_until,OLD.file_max_attempts,OLD.file_attempt_timeout_seconds)
   OR NEW.budget_clock_at<OLD.budget_clock_at OR NEW.file_attempts_started<OLD.file_attempts_started
   OR NEW.file_attempts_started>OLD.file_attempts_started+1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_BUDGET_IMMUTABLE'; END IF;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(NEW.file_attempt_index) LOOP
  n:=n+1;
  IF jsonb_typeof(entry)<>'object' OR (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(entry) key)
   IS DISTINCT FROM ARRAY['ordinal','session_digest','state','token','work_item_id']::text[]
   OR ((entry->>'ordinal')::integer=n AND (entry->>'token')::bigint>=1
    AND entry->>'session_digest' ~ '^sha256:[0-9a-f]{64}$' AND char_length(entry->>'work_item_id') BETWEEN 1 AND 128) IS NOT TRUE
   OR (entry->>'state' IN ('issued','completed','superseded','timed_out')) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INDEX_SHAPE'; END IF;
  identity:=(entry->>'work_item_id')||':'||(entry->>'token');
  IF identity=ANY(identities) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_INDEX_DUPLICATE'; END IF;
  identities:=array_append(identities,identity);
  IF entry->>'state'='issued' THEN
   IF active IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_MULTIPLE_ACTIVE'; END IF;
   active:=entry;
  END IF;
  IF TG_OP='UPDATE' AND n<=OLD.file_attempts_started THEN
   prior:=OLD.file_attempt_index->(n-1);
   IF (entry-'state') IS DISTINCT FROM (prior-'state') OR
     (prior->>'state'<>'issued' AND entry IS DISTINCT FROM prior) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_IDENTITY_IMMUTABLE'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='UPDATE' AND OLD.file_attempt_ticket IS NOT NULL AND NEW.file_attempts_started=OLD.file_attempts_started
  AND (NEW.file_attempt_ticket-'state') IS DISTINCT FROM (OLD.file_attempt_ticket-'state') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_IMMUTABLE'; END IF;
 IF NEW.file_attempt_ticket IS NOT NULL THEN
  IF (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(NEW.file_attempt_ticket) key)
   IS DISTINCT FROM ARRAY['attempt_until','executor_session','ordinal','started_at','state','token','work_item_id']::text[]
   OR ((NEW.file_attempt_ticket-'started_at'-'attempt_until'-'executor_session')||jsonb_build_object(
    'session_digest','sha256:'||encode(sha256(convert_to(NEW.file_attempt_ticket->>'executor_session','UTF8')),'hex'))) IS DISTINCT FROM NEW.file_attempt_index->(NEW.file_attempts_started-1)
   OR ((NEW.file_attempt_ticket->>'started_at')::timestamptz>=NEW.file_ready_at AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz>(NEW.file_attempt_ticket->>'started_at')::timestamptz AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz<=NEW.file_wall_until AND
      (NEW.file_attempt_ticket->>'attempt_until')::timestamptz<=(NEW.file_attempt_ticket->>'started_at')::timestamptz+make_interval(secs=>NEW.file_attempt_timeout_seconds)) IS NOT TRUE
   THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_SHAPE'; END IF;
 ELSIF NEW.file_attempts_started<>0 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_ATTEMPT_TICKET_MISSING';
 END IF;
 RETURN NEW;
END $_$;

CREATE FUNCTION guard_file_publication_identity() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.publication_permit_id IS NOT NULL THEN
  IF NEW.publication_permit_id IS DISTINCT FROM OLD.publication_permit_id
   OR NEW.publication_effect_key IS DISTINCT FROM OLD.publication_effect_key
   OR NEW.publication_executor_session IS DISTINCT FROM OLD.publication_executor_session THEN
   -- S6: the column form of the old partial unique index
   -- `file_publication_unresolved_uq (acquisition_id, effect_key) WHERE NOT
   -- retry_safe`. Only a slot whose "unresolved" predicate is FALSE may be
   -- replaced, and the replacement starts a fresh attempt.
   IF OLD.publication_retry_safe IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_UNRESOLVED'; END IF;
   IF NEW.publication_attempt<=OLD.publication_attempt THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_ATTEMPT_STEP'; END IF;
   IF NEW.publication_dispatch_state IS DISTINCT FROM 'prepared'
    OR NEW.publication_effect_class IS DISTINCT FROM 'unknown'
    OR NEW.publication_dispatch_started_at IS NOT NULL THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_SLOT_REPLACEMENT_SHAPE'; END IF;
   RETURN NEW;
  END IF;
  IF ROW(NEW.publication_permit_id,NEW.acquisition_id,NEW.file_ref,NEW.publication_work_item_id,
   NEW.publication_fencing_token,NEW.publication_owner_version,NEW.publication_attempt,NEW.publication_effect_key,NEW.publication_spec_digest,NEW.publication_plan_digest,NEW.publication_dependency_digest,
   NEW.operation,NEW.intent,NEW.collision_policy,NEW.publication_frozen_objects,NEW.publication_executor_session,
   NEW.publication_prepared_at,NEW.publication_reconcile_wall_seconds,NEW.publication_max_lookups)
  IS DISTINCT FROM ROW(OLD.publication_permit_id,OLD.acquisition_id,OLD.file_ref,OLD.publication_work_item_id,
   OLD.publication_fencing_token,OLD.publication_owner_version,OLD.publication_attempt,OLD.publication_effect_key,OLD.publication_spec_digest,OLD.publication_plan_digest,OLD.publication_dependency_digest,
   OLD.operation,OLD.intent,OLD.collision_policy,OLD.publication_frozen_objects,OLD.publication_executor_session,
   OLD.publication_prepared_at,OLD.publication_reconcile_wall_seconds,OLD.publication_max_lookups)
  THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_IDENTITY_IMMUTABLE'; END IF;
  IF NEW.publication_evidence_version<OLD.publication_evidence_version OR NEW.publication_lookup_count<OLD.publication_lookup_count OR
   (OLD.publication_dispatch_started_at IS NOT NULL AND NEW.publication_dispatch_started_at IS DISTINCT FROM OLD.publication_dispatch_started_at) OR
   (OLD.publication_unknown_since IS NOT NULL AND ROW(NEW.publication_unknown_since,NEW.publication_reconcile_until) IS DISTINCT FROM ROW(OLD.publication_unknown_since,OLD.publication_reconcile_until)) OR
   (OLD.publication_closed_proof IS NOT NULL AND NEW.publication_closed_proof IS DISTINCT FROM OLD.publication_closed_proof) OR
   (OLD.publication_resolution_proof IS NOT NULL AND NEW.publication_resolution_proof IS DISTINCT FROM OLD.publication_resolution_proof) OR
   (OLD.publication_dispatch_state='closed' AND NEW.publication_dispatch_state<>'closed') OR
   (OLD.publication_dispatch_state='dispatching' AND NEW.publication_dispatch_state='prepared')
  THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='FILE_PUBLICATION_FACT_IMMUTABLE'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION guard_inspect_attempt_budget() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry jsonb; n integer:=0; prior jsonb; active jsonb; identities text[]:=ARRAY[]::text[]; identity text;
BEGIN
 IF TG_OP='INSERT' THEN
  -- Complete every budget column that the writer left NULL, so a
  -- bare INSERT of artifacts still satisfies the shape CHECK. The values are
  -- the DDL DEFAULTs above (inspect: 5 attempts / 300s / 86400s wall).
  IF NEW.inspect_ready_at IS NULL THEN NEW.inspect_ready_at:=CURRENT_TIMESTAMP; END IF;
  IF NEW.inspect_wall_until IS NULL THEN NEW.inspect_wall_until:=CURRENT_TIMESTAMP + INTERVAL '86400 seconds'; END IF;
  IF NEW.inspect_max_attempts IS NULL THEN NEW.inspect_max_attempts:=5; END IF;
  IF NEW.inspect_attempt_timeout_seconds IS NULL THEN NEW.inspect_attempt_timeout_seconds:=300; END IF;
  IF NEW.inspect_attempts_started<>0 OR NEW.inspect_attempt_ticket IS NOT NULL OR NEW.inspect_attempt_index<>'[]'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_INITIAL_SHAPE'; END IF;
 ELSE
  IF ROW(NEW.inspect_ready_at,NEW.inspect_wall_until,NEW.inspect_max_attempts,NEW.inspect_attempt_timeout_seconds) IS DISTINCT FROM
     ROW(OLD.inspect_ready_at,OLD.inspect_wall_until,OLD.inspect_max_attempts,OLD.inspect_attempt_timeout_seconds)
   OR NEW.inspect_attempts_started<OLD.inspect_attempts_started
   OR NEW.inspect_attempts_started>OLD.inspect_attempts_started+1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_BUDGET_IMMUTABLE'; END IF;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(NEW.inspect_attempt_index) LOOP
  n:=n+1;
  IF jsonb_typeof(entry)<>'object' OR (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(entry) key)
   IS DISTINCT FROM ARRAY['ordinal','session_digest','state','token','work_item_id']::text[]
   OR ((entry->>'ordinal')::integer=n AND (entry->>'token')::bigint>=1
    AND entry->>'session_digest' ~ '^sha256:[0-9a-f]{64}$' AND char_length(entry->>'work_item_id') BETWEEN 1 AND 128) IS NOT TRUE
   OR (entry->>'state' IN ('issued','completed','superseded','timed_out')) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_INDEX_SHAPE'; END IF;
  identity:=(entry->>'work_item_id')||':'||(entry->>'token');
  IF identity=ANY(identities) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_INDEX_DUPLICATE'; END IF;
  identities:=array_append(identities,identity);
  IF entry->>'state'='issued' THEN
   IF active IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_MULTIPLE_ACTIVE'; END IF;
   active:=entry;
  END IF;
  IF TG_OP='UPDATE' AND n<=OLD.inspect_attempts_started THEN
   prior:=OLD.inspect_attempt_index->(n-1);
   IF (entry-'state') IS DISTINCT FROM (prior-'state') OR
     (prior->>'state'<>'issued' AND entry IS DISTINCT FROM prior) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_IDENTITY_IMMUTABLE'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='UPDATE' AND OLD.inspect_attempt_ticket IS NOT NULL AND NEW.inspect_attempts_started=OLD.inspect_attempts_started
  AND (NEW.inspect_attempt_ticket-'state') IS DISTINCT FROM (OLD.inspect_attempt_ticket-'state') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_TICKET_IMMUTABLE'; END IF;
 IF NEW.inspect_attempt_ticket IS NOT NULL THEN
  IF (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(NEW.inspect_attempt_ticket) key)
   IS DISTINCT FROM ARRAY['attempt_until','executor_session','ordinal','started_at','state','token','work_item_id']::text[]
   OR ((NEW.inspect_attempt_ticket-'started_at'-'attempt_until'-'executor_session')||jsonb_build_object(
    'session_digest','sha256:'||encode(sha256(convert_to(NEW.inspect_attempt_ticket->>'executor_session','UTF8')),'hex'))) IS DISTINCT FROM NEW.inspect_attempt_index->(NEW.inspect_attempts_started-1)
   OR ((NEW.inspect_attempt_ticket->>'started_at')::timestamptz>=NEW.inspect_ready_at AND
      (NEW.inspect_attempt_ticket->>'attempt_until')::timestamptz>(NEW.inspect_attempt_ticket->>'started_at')::timestamptz AND
      (NEW.inspect_attempt_ticket->>'attempt_until')::timestamptz<=NEW.inspect_wall_until AND
      (NEW.inspect_attempt_ticket->>'attempt_until')::timestamptz<=(NEW.inspect_attempt_ticket->>'started_at')::timestamptz+make_interval(secs=>NEW.inspect_attempt_timeout_seconds)) IS NOT TRUE
   THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_TICKET_SHAPE'; END IF;
 ELSIF NEW.inspect_attempts_started<>0 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='INSPECT_ATTEMPT_TICKET_MISSING';
 END IF;
 RETURN NEW;
END $_$;

CREATE FUNCTION guard_plan_attempt_budget() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE entry jsonb; n integer:=0; prior jsonb; active jsonb; identities text[]:=ARRAY[]::text[]; identity text;
BEGIN
 IF TG_OP='INSERT' THEN
  -- Complete every budget column the writer left NULL (plan:
  -- 5 attempts / 120s / 86400s wall).
  IF NEW.plan_ready_at IS NULL THEN NEW.plan_ready_at:=CURRENT_TIMESTAMP; END IF;
  IF NEW.plan_wall_until IS NULL THEN NEW.plan_wall_until:=CURRENT_TIMESTAMP + INTERVAL '86400 seconds'; END IF;
  IF NEW.plan_max_attempts IS NULL THEN NEW.plan_max_attempts:=5; END IF;
  IF NEW.plan_attempt_timeout_seconds IS NULL THEN NEW.plan_attempt_timeout_seconds:=120; END IF;
  IF NEW.plan_attempts_started<>0 OR NEW.plan_attempt_ticket IS NOT NULL OR NEW.plan_attempt_index<>'[]'::jsonb THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INITIAL_SHAPE'; END IF;
 ELSE
  IF ROW(NEW.plan_ready_at,NEW.plan_wall_until,NEW.plan_max_attempts,NEW.plan_attempt_timeout_seconds) IS DISTINCT FROM
     ROW(OLD.plan_ready_at,OLD.plan_wall_until,OLD.plan_max_attempts,OLD.plan_attempt_timeout_seconds)
   OR NEW.plan_attempts_started<OLD.plan_attempts_started
   OR NEW.plan_attempts_started>OLD.plan_attempts_started+1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_BUDGET_IMMUTABLE'; END IF;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(NEW.plan_attempt_index) LOOP
  n:=n+1;
  IF jsonb_typeof(entry)<>'object' OR (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(entry) key)
   IS DISTINCT FROM ARRAY['ordinal','session_digest','state','token','work_item_id']::text[]
   OR ((entry->>'ordinal')::integer=n AND (entry->>'token')::bigint>=1
    AND entry->>'session_digest' ~ '^sha256:[0-9a-f]{64}$' AND char_length(entry->>'work_item_id') BETWEEN 1 AND 128) IS NOT TRUE
   OR (entry->>'state' IN ('issued','completed','superseded','timed_out')) IS NOT TRUE THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INDEX_SHAPE'; END IF;
  identity:=(entry->>'work_item_id')||':'||(entry->>'token');
  IF identity=ANY(identities) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_INDEX_DUPLICATE'; END IF;
  identities:=array_append(identities,identity);
  IF entry->>'state'='issued' THEN
   IF active IS NOT NULL THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_MULTIPLE_ACTIVE'; END IF;
   active:=entry;
  END IF;
  IF TG_OP='UPDATE' AND n<=OLD.plan_attempts_started THEN
   prior:=OLD.plan_attempt_index->(n-1);
   IF (entry-'state') IS DISTINCT FROM (prior-'state') OR
     (prior->>'state'<>'issued' AND entry IS DISTINCT FROM prior) THEN
    RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_IDENTITY_IMMUTABLE'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='UPDATE' AND OLD.plan_attempt_ticket IS NOT NULL AND NEW.plan_attempts_started=OLD.plan_attempts_started
  AND (NEW.plan_attempt_ticket-'state') IS DISTINCT FROM (OLD.plan_attempt_ticket-'state') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_IMMUTABLE'; END IF;
 IF NEW.plan_attempt_ticket IS NOT NULL THEN
  IF (SELECT array_agg(key ORDER BY key) FROM jsonb_object_keys(NEW.plan_attempt_ticket) key)
   IS DISTINCT FROM ARRAY['attempt_until','executor_session','ordinal','started_at','state','token','work_item_id']::text[]
   OR ((NEW.plan_attempt_ticket-'started_at'-'attempt_until'-'executor_session')||jsonb_build_object(
    'session_digest','sha256:'||encode(sha256(convert_to(NEW.plan_attempt_ticket->>'executor_session','UTF8')),'hex'))) IS DISTINCT FROM NEW.plan_attempt_index->(NEW.plan_attempts_started-1)
   OR ((NEW.plan_attempt_ticket->>'started_at')::timestamptz>=NEW.plan_ready_at AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz>(NEW.plan_attempt_ticket->>'started_at')::timestamptz AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz<=NEW.plan_wall_until AND
      (NEW.plan_attempt_ticket->>'attempt_until')::timestamptz<=(NEW.plan_attempt_ticket->>'started_at')::timestamptz+make_interval(secs=>NEW.plan_attempt_timeout_seconds)) IS NOT TRUE
   THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_SHAPE'; END IF;
 ELSIF NEW.plan_attempts_started<>0 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_ATTEMPT_TICKET_MISSING';
 END IF;
 RETURN NEW;
END $_$;

CREATE FUNCTION inspect_attempt_budget_from_snapshot() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE frozen integer;
BEGIN
 SELECT (i.input->'limits'->'inspect'->>'attempt_timeout_seconds')::integer INTO frozen
  FROM public.acquisitions i WHERE i.id=NEW.acquisition_id;
 IF frozen IS NOT NULL AND (NEW.inspect_attempt_timeout_seconds IS NULL OR frozen>NEW.inspect_attempt_timeout_seconds) THEN
  NEW.inspect_attempt_timeout_seconds:=frozen;
 END IF;
 RETURN NEW;
END $$;


CREATE FUNCTION plan_attempt_budget_from_snapshot() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE frozen integer;
BEGIN
 SELECT (i.input->'limits'->'plan'->>'attempt_timeout_seconds')::integer INTO frozen
  FROM public.acquisitions i WHERE i.id=NEW.acquisition_id;
 IF frozen IS NOT NULL AND (NEW.plan_attempt_timeout_seconds IS NULL OR frozen>NEW.plan_attempt_timeout_seconds) THEN
  NEW.plan_attempt_timeout_seconds:=frozen;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION plan_start_attempt(p_plan text, p_work text, p_token bigint, p_session text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans; w public.work_item_history; now_at timestamptz; ticket jsonb; idx jsonb; old_entry jsonb; entry jsonb; ordinal integer;
BEGIN
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=p_plan FOR UPDATE;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF (w.owner_type='postprocessing_plan' AND w.owner_id=p.id AND w.kind='postprocess_plan' AND w.state='leased'
  AND w.fencing_token=p_token AND w.expected_owner_version=p.version
  AND p.state='queued') IS NOT TRUE THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF p_session IS NULL OR char_length(p_session) NOT BETWEEN 1 AND 128 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_SESSION_SHAPE'; END IF;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(p.plan_attempt_index) LOOP
  IF old_entry->>'work_item_id'=p_work AND (old_entry->>'token')::bigint=p_token THEN
   RETURN jsonb_build_object('granted',false,'ticket',p.plan_attempt_ticket,'reason_code','ATTEMPT_ALREADY_ISSUED');
  END IF;
 END LOOP;
 now_at:=GREATEST(clock_timestamp(),p.plan_ready_at);
 IF now_at>=p.plan_wall_until OR p.plan_attempts_started>=p.plan_max_attempts THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_BUDGET_EXHAUSTED'; END IF;
 idx:='[]'::jsonb;
 FOR old_entry IN SELECT value FROM jsonb_array_elements(p.plan_attempt_index) LOOP
  IF old_entry->>'state'='issued' THEN old_entry:=jsonb_set(old_entry,'{state}','"superseded"'); END IF;
  idx:=idx||jsonb_build_array(old_entry);
 END LOOP;
 ordinal:=p.plan_attempts_started+1;
 entry:=jsonb_build_object('work_item_id',p_work,'token',p_token,'ordinal',ordinal,'session_digest','sha256:'||encode(sha256(convert_to(p_session,'UTF8')),'hex'),'state','issued');
 ticket:=(entry-'session_digest')||jsonb_build_object('executor_session',p_session,'started_at',now_at,'attempt_until',LEAST(now_at+make_interval(secs=>p.plan_attempt_timeout_seconds),p.plan_wall_until));
 UPDATE public.postprocessing_plans SET plan_attempts_started=ordinal,plan_attempt_index=idx||jsonb_build_array(entry),plan_attempt_ticket=ticket WHERE id=p.id;
 RETURN jsonb_build_object('granted',true,'ticket',ticket);
END $$;

CREATE FUNCTION postprocessing_plan_clean(p_plan text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT p.state='queued' AND p.sealed_at IS NULL
 AND a.state='verified' AND a.version=p.artifact_version
 AND EXISTS(SELECT 1 FROM public.acquisitions r WHERE r.id=p.acquisition_id AND r.manifest_digest IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.file_operations o WHERE o.artifact_id=p.artifact_id)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.outcome IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=f.acquisition_id AND h.requirements_digest IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=f.acquisition_id AND h.target_digest IS NOT NULL))
 FROM public.postprocessing_plans p JOIN public.artifacts a ON a.id=p.artifact_id AND a.acquisition_id=p.acquisition_id
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.requirements_digest IS NOT NULL AND (
 h.requirements_manifest_digest IS DISTINCT FROM h.manifest_digest
 OR h.requirements_item_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL)
 OR h.requirements_required_count<>(SELECT count(*) FROM public.acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required')
 OR EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.requirement IS NULL)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisitions h WHERE h.id=p.acquisition_id AND h.target_digest IS NOT NULL AND
 h.target_requirements_digest IS DISTINCT FROM h.requirements_digest)
 AND NOT EXISTS(SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND (f.acquisition_id IS DISTINCT FROM p.acquisition_id
 OR NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND m.file_ref=f.file_ref
 AND m.relative_path=f.relative_path AND m.exact_size=f.size_bytes AND m.media_kind=f.media_kind)))
 AND NOT EXISTS(SELECT 1 FROM public.acquisition_files m WHERE m.acquisition_id=p.acquisition_id AND NOT EXISTS(
 SELECT 1 FROM public.artifact_files f WHERE f.artifact_id=a.id AND f.file_ref=m.file_ref))
 WHERE p.id=p_plan
$$;

CREATE FUNCTION postprocessing_plan_recovery_lock(p_plan text, p_work text, p_token bigint) RETURNS postprocessing_plans
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; a public.artifacts%ROWTYPE;
 r public.acquisitions%ROWTYPE; w public.work_item_history%ROWTYPE; root_id text; artifact_id text;
BEGIN
 SELECT acquisition_id,p0.artifact_id INTO STRICT root_id,artifact_id FROM public.postprocessing_plans p0 WHERE id=p_plan;
 SELECT * INTO STRICT r FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT * INTO STRICT a FROM public.artifacts WHERE id=artifact_id FOR UPDATE;
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=p_plan FOR UPDATE;
 -- Prelock the existing root work set in the same order as T04 before facts
 -- can touch an existing settlement item. No leaf-to-owner lock acquisition.
 PERFORM id FROM public.work_items WHERE id=p_work OR (acquisition_id=root_id AND state IN ('available','leased'))
 ORDER BY id COLLATE "C" FOR UPDATE;
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF r.state IS DISTINCT FROM 'running' OR r.record_state IS DISTINCT FROM 'full'
 OR p.acquisition_id IS DISTINCT FROM root_id OR p.artifact_id IS DISTINCT FROM a.id
 OR a.acquisition_id IS DISTINCT FROM root_id OR a.state IS DISTINCT FROM 'verified' OR a.version IS DISTINCT FROM p.artifact_version
 OR w.acquisition_id IS DISTINCT FROM root_id
 OR w.owner_type IS DISTINCT FROM 'postprocessing_plan' OR w.owner_id IS DISTINCT FROM p.id
 OR w.kind IS DISTINCT FROM 'postprocess_plan' OR w.state IS DISTINCT FROM 'leased'
 OR w.fencing_token IS DISTINCT FROM p_token OR w.expected_owner_version IS DISTINCT FROM p.version
 OR (w.lease_until>clock_timestamp()) IS NOT TRUE THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF p.state IN ('succeeded','failed') THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_TERMINAL_WORK_LIVE'; END IF;
 IF public.postprocessing_plan_clean(p_plan) IS NOT TRUE OR w.external_effect_admitted_at IS NOT NULL
 OR EXISTS(SELECT 1 FROM public.work_items other WHERE other.owner_type='postprocessing_plan' AND other.owner_id=p.id
   AND other.id<>p_work AND other.state IN ('available','leased')) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_RECOVERY_GRAPH_INCONSISTENT'; END IF;
 RETURN p;
END $$;

CREATE FUNCTION postprocessing_plan_capture_lock(p_plan text, p_work text, p_token bigint, p_capture jsonb) RETURNS void
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
 'vars_protocol',jsonb_build_object('version',i.vars_evidence_version,'digest',i.vars_evidence_digest,'admission_digest',i.admission_digest),
 'vars_evaluation',CASE WHEN i.vars_evaluation_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.vars_evaluation_canonical_utf8,'record',i.vars_evaluation,'evidence_digest',i.vars_evidence_digest) END,
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

CREATE FUNCTION postprocessing_plan_child_diagnostic(p_plan text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE p public.postprocessing_plans%ROWTYPE; item record; fw public.work_item_history%ROWTYPE; expected jsonb; marker jsonb;
BEGIN
 SELECT * INTO p FROM public.postprocessing_plans WHERE id=p_plan;
 IF p.state IS DISTINCT FROM 'succeeded' THEN RETURN NULL; END IF;
 IF EXISTS(SELECT 1 FROM public.work_item_history child LEFT JOIN public.file_operations o ON o.id=child.owner_id WHERE child.acquisition_id=p.acquisition_id AND child.owner_type='file_operation' AND (o.id IS NULL OR o.postprocessing_plan_id IS DISTINCT FROM p.id OR child.kind IS DISTINCT FROM 'file_execute' OR child.owner_type IS DISTINCT FROM 'file_operation' OR child.owner_id IS DISTINCT FROM o.id)) THEN RETURN 'CHILD_WORK_SNAPSHOT_INVALID'; END IF;
 SELECT input_snapshot->'one_shot' INTO marker FROM public.work_item_outcomes WHERE owner_type='postprocessing_plan' AND owner_id=p.id AND kind='postprocess_plan' AND state='succeeded' AND expected_owner_version=p.version;
 FOR item IN SELECT o.* FROM public.file_operations o JOIN public.acquisition_files f ON f.plan_operation_ref=o.id WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind='execute' ORDER BY o.id COLLATE "C" LOOP
 IF (SELECT count(*) FROM public.work_item_history WHERE owner_type='file_operation' AND owner_id=item.id AND kind='file_execute')>1 THEN RETURN 'CHILD_WORK_SNAPSHOT_INVALID'; END IF;
 SELECT * INTO fw FROM public.work_item_history WHERE owner_type='file_operation' AND owner_id=item.id AND kind='file_execute' ORDER BY id COLLATE "C" LIMIT 1;
 IF NOT FOUND THEN
 -- A bound subtitle's file_execute work is created by the video's handoff,
 -- never by the plan commit.
 IF item.bound_video_operation_id IS NULL AND NOT EXISTS(SELECT 1 FROM public.acquisition_files r WHERE r.acquisition_id=p.acquisition_id AND r.file_ref=item.file_ref AND r.outcome IS NOT NULL) THEN RETURN 'CHILD_WORK_MISSING'; END IF;
 CONTINUE; END IF;
 expected:=jsonb_build_object('operation_id',item.id,'artifact_id',item.artifact_id,'artifact_file_id',item.artifact_file_id)||CASE WHEN item.collision_policy='overwrite' THEN jsonb_build_object('recovery_mode','overwrite') ELSE '{}'::jsonb END;
 IF fw.owner_type IS DISTINCT FROM 'file_operation' OR fw.owner_id IS DISTINCT FROM item.id OR fw.acquisition_id IS DISTINCT FROM p.acquisition_id OR (fw.input_snapshot-'one_shot') IS DISTINCT FROM expected THEN RETURN 'CHILD_WORK_SNAPSHOT_INVALID'; END IF;
 IF p.one_shot_id IS NOT NULL THEN
 IF NOT fw.input_snapshot ? 'one_shot' THEN RETURN 'CHILD_ONE_SHOT_MISSING'; END IF;
 IF jsonb_typeof(fw.input_snapshot->'one_shot') IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(fw.input_snapshot->'one_shot')='object' THEN fw.input_snapshot->'one_shot' ELSE '{}'::jsonb END))<>3 OR NOT (fw.input_snapshot->'one_shot') ?& ARRAY['marker_id','correlation_id','admission_epoch'] OR jsonb_typeof(fw.input_snapshot->'one_shot'->'marker_id') IS DISTINCT FROM 'string' OR jsonb_typeof(fw.input_snapshot->'one_shot'->'correlation_id') IS DISTINCT FROM 'string' OR COALESCE(fw.input_snapshot->'one_shot'->>'marker_id','')='' OR COALESCE(fw.input_snapshot->'one_shot'->>'correlation_id','')='' OR jsonb_typeof(fw.input_snapshot->'one_shot'->'admission_epoch') IS DISTINCT FROM 'number' OR COALESCE(fw.input_snapshot->'one_shot'->>'admission_epoch','') !~ '^[1-9][0-9]*$' THEN RETURN 'CHILD_ONE_SHOT_INVALID'; END IF;
 IF fw.input_snapshot->'one_shot' IS DISTINCT FROM marker THEN RETURN 'CHILD_ONE_SHOT_MISMATCH'; END IF;
 ELSIF fw.input_snapshot ? 'one_shot' THEN RETURN 'CHILD_ONE_SHOT_MISMATCH'; END IF;
 END LOOP;
 RETURN NULL;
END $_$;


CREATE FUNCTION postprocessing_plan_fail(p_plan text, p_work text, p_code text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; v bigint; status text;
BEGIN
 SELECT * INTO STRICT p FROM public.postprocessing_plans WHERE id=p_plan;
 IF p_code IS NULL OR char_length(p_code) NOT BETWEEN 1 AND 128 OR p_reason IS NULL OR char_length(p_reason)>1024 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_FAILURE_SHAPE'; END IF;
 status:=CASE WHEN (SELECT r.requirements_digest IS NOT NULL FROM public.acquisitions r WHERE r.id=p.acquisition_id) THEN 'sealed' ELSE 'not_reached' END;
 IF status='sealed' THEN
 UPDATE public.acquisition_files f SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
 delivery_source_cleanup='not_required',delivery_reason_code=p_code,
 evidence_digest='sha256:'||encode(sha256(convert_to('plan-failure:v1','UTF8')||decode('00','hex')||convert_to(p.input_digest||':'||f.file_ref||':'||p_code,'UTF8')),'hex'),
 receipt=jsonb_build_object('schema_version',1,'reason_code',p_code,'stage','planning'),
 delivered_at=CURRENT_TIMESTAMP
 WHERE f.acquisition_id=p.acquisition_id AND f.requirement='required';
 END IF;
 PERFORM public.download_ensure_settlement(p.acquisition_id);
 UPDATE public.postprocessing_plans SET state='failed',outcome_code=CASE WHEN p_code='PLAN_CANCELLED' THEN p_code ELSE 'plan_failed' END,
 outcome_reason=p_reason,planning_requirements_status=status,
 planning_diagnostic=jsonb_build_object('schema_version',1,'reason_code',p_code,'requirements_status',status),
 planning_facts_revision=(SELECT facts_revision FROM public.acquisitions WHERE id=p.acquisition_id),
 version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_plan RETURNING version INTO v;
 IF public.work_item_fail_at_attempt_budget(v,p_code,p_reason,p_work,p.version)<>1 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 RETURN v;
END $$;

CREATE FUNCTION postprocessing_plan_commit(p_plan text, p_work text, p_token bigint, p_input_digest text, p_input jsonb, p_requirements text, p_targets text, p_declarations text, p_operations jsonb, p_capture jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE; input jsonb;
 requirements jsonb; targets jsonb; declarations jsonb; rd text; td text; pd text; total integer; required integer;
 entry jsonb; op jsonb; inherited jsonb; new_work text; v bigint; now_at timestamptz; limits jsonb;
 selection_bytes text; evidence_bytes text; sd text; ed text; input_bytes text;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 IF public.vars_evidence_consistent(p.acquisition_id) IS NOT TRUE THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='VARS_EVALUATION_EVIDENCE_INCONSISTENT'; END IF;
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

-- PRUNE-046 S5: the terminal-state observation of one plan commit. The former
-- postprocessing_plan_commit_proof re-derived every per-file child table; those
-- children are now the requirement, target, plan item and delivery columns of
-- acquisition_files, written by postprocessing_plan_commit from the same
-- declarations, so only the sealed identities and the same-row projections are
-- compared here.
CREATE FUNCTION postprocessing_plan_commit_state(p_plan text, p_work text, p_token bigint) RETURNS jsonb
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
 IF public.vars_evidence_consistent(p.acquisition_id) IS NOT TRUE THEN RETURN NULL; END IF;
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

CREATE FUNCTION postprocessing_plan_failed(p_plan text, p_work text, p_token bigint, p_code text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.recovery_blocked THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF w.cancel_requested THEN p_code:='PLAN_CANCELLED';p_reason:='read-only planning was cancelled'; END IF;
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,p_code,p_reason);
 RETURN jsonb_build_object('state','failed','reason_code',p_code);
END $$;

CREATE FUNCTION postprocessing_plan_immutable() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.acquisition_id IS DISTINCT FROM OLD.acquisition_id
 OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id OR NEW.artifact_version IS DISTINCT FROM OLD.artifact_version
 OR NEW.input_digest IS DISTINCT FROM OLD.input_digest
 OR (OLD.state IN ('succeeded','failed') AND NEW IS DISTINCT FROM OLD)
 OR (OLD.sealed_at IS NOT NULL AND (NEW.sealed_at,NEW.requirements_digest,NEW.target_digest,NEW.plan_digest)
 IS DISTINCT FROM (OLD.sealed_at,OLD.requirements_digest,OLD.target_digest,OLD.plan_digest)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_IMMUTABLE'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION work_item_requeue_recovered_plan(p_id text, p_token bigint, p_owner_id text, p_next_owner_version bigint, p_delay_seconds double precision) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE w public.work_item_history%ROWTYPE;
BEGIN
 SELECT * INTO w FROM public.work_items WHERE id=p_id FOR UPDATE;
 IF NOT FOUND THEN SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_id; END IF;
 IF w.cancel_requested THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_REQUEUE_CANCELLED'; END IF;
 IF w.kind IS DISTINCT FROM 'postprocess_plan' OR w.owner_type IS DISTINCT FROM 'postprocessing_plan'
 OR w.owner_id IS DISTINCT FROM p_owner_id
 OR w.state IS DISTINCT FROM 'leased' OR w.fencing_token IS DISTINCT FROM p_token OR NOT w.recovery_blocked
 OR (w.lease_until>clock_timestamp()) IS NOT TRUE OR w.attempt>=w.max_attempts
 OR p_delay_seconds IS NULL OR p_delay_seconds<0 OR p_delay_seconds>86400
 OR p_next_owner_version IS DISTINCT FROM w.expected_owner_version+1
 OR p_next_owner_version IS DISTINCT FROM (SELECT version FROM public.postprocessing_plans WHERE id=p_owner_id)
 OR w.external_effect_admitted_at IS NOT NULL THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 UPDATE public.work_items SET state='available',available_at=clock_timestamp()+make_interval(secs=>p_delay_seconds),
 lease_until=NULL,recovery_blocked=false,recovery_reason=NULL,
 fencing_token=fencing_token+1,expected_owner_version=p_next_owner_version,last_error_code='PLAN_INTERRUPTED',
 last_error_reason='read-only planning was interrupted',updated_at=CURRENT_TIMESTAMP WHERE id=p_id;
END $$;

CREATE FUNCTION postprocessing_plan_requeue_interrupted(p_plan text, p_work text, p_token bigint, p_delay_seconds double precision) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE; v bigint;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF NOT w.recovery_blocked THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF w.cancel_requested THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,'PLAN_CANCELLED','read-only planning was cancelled');
 RETURN jsonb_build_object('state','failed','reason_code','PLAN_CANCELLED');
 ELSIF w.attempt>=w.max_attempts THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,'PLAN_INTERRUPTED','read-only planning was interrupted');
 RETURN jsonb_build_object('state','failed','reason_code','PLAN_INTERRUPTED');
 END IF;
 UPDATE public.postprocessing_plans SET version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_plan RETURNING version INTO v;
 PERFORM public.work_item_requeue_recovered_plan(p_work,p_token,p_plan,v,p_delay_seconds);
 RETURN jsonb_build_object('state','queued','reason_code','PLAN_INTERRUPTED','plan_version',v);
END $$;

CREATE FUNCTION postprocessing_plan_retry(p_plan text, p_work text, p_token bigint, p_code text, p_reason text, p_delay double precision) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE p public.postprocessing_plans%ROWTYPE; w public.work_item_history%ROWTYPE; v bigint;
BEGIN
 p:=public.postprocessing_plan_recovery_lock(p_plan,p_work,p_token);
 SELECT * INTO STRICT w FROM public.work_item_history WHERE id=p_work;
 IF w.recovery_blocked OR p_delay IS NULL OR p_delay<0 OR p_delay>86400 THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PLAN_STAGE_STALE_CLAIM'; END IF;
 IF w.cancel_requested THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,'PLAN_CANCELLED','read-only planning was cancelled');
 RETURN jsonb_build_object('state','failed','reason_code','PLAN_CANCELLED');
 ELSIF w.attempt>=w.max_attempts THEN
 PERFORM public.postprocessing_plan_fail(p_plan,p_work,p_code,p_reason);
 RETURN jsonb_build_object('state','failed','reason_code',p_code); END IF;
 UPDATE public.postprocessing_plans SET version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=p_plan RETURNING version INTO v;
 PERFORM public.work_item_return_available(p_delay,v,p_code,p_reason,p_work,p.version);
 RETURN jsonb_build_object('state','queued','reason_code',p_code);
END $$;


CREATE FUNCTION validate_file_operation_artifact() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    current_state TEXT;
    current_version BIGINT;
    current_fingerprint JSONB;
BEGIN
    SELECT artifact.state, artifact.version, artifact_file.fingerprint
    INTO current_state, current_version, current_fingerprint
    FROM artifacts AS artifact
    JOIN artifact_files AS artifact_file
      ON artifact_file.artifact_id = artifact.id
    WHERE artifact.id = NEW.artifact_id
      AND artifact_file.id = NEW.artifact_file_id
      AND artifact_file.source_path = NEW.source_path
    FOR KEY SHARE OF artifact, artifact_file;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'file operation artifact file does not exist'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF current_state <> 'verified' THEN
        RAISE EXCEPTION 'file operation requires a verified artifact'
            USING ERRCODE = 'check_violation';
    END IF;
    IF current_version <> NEW.artifact_version THEN
        RAISE EXCEPTION 'file operation artifact version is stale'
            USING ERRCODE = 'check_violation';
    END IF;
    IF current_fingerprint <> NEW.source_fingerprint THEN
        RAISE EXCEPTION 'file operation source fingerprint does not match artifact file'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION work_item_delete_one_shot(text, text, text[], text[]) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items
WHERE (owner_type = 'download_task' AND owner_id = $1)
   OR (owner_type = 'release' AND owner_id = $2)
   OR (owner_type = 'artifact' AND owner_id = ANY($3::text[]))
   OR (owner_type = 'file_operation' AND owner_id = ANY($4::text[]))
   OR (
       owner_type = 'postprocessing_plan'
       AND owner_id IN (
           SELECT plan.id FROM public.postprocessing_plans plan
           WHERE plan.artifact_id = ANY($3::text[])
       )
   );
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes
WHERE (owner_type = 'download_task' AND owner_id = $1)
   OR (owner_type = 'release' AND owner_id = $2)
   OR (owner_type = 'artifact' AND owner_id = ANY($3::text[]))
   OR (owner_type = 'file_operation' AND owner_id = ANY($4::text[]))
   OR (
       owner_type = 'postprocessing_plan'
       AND owner_id IN (
           SELECT plan.id FROM public.postprocessing_plans plan
           WHERE plan.artifact_id = ANY($3::text[])
       )
   );
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

CREATE TRIGGER inspect_attempt_budget
    BEFORE INSERT OR UPDATE ON artifacts
    FOR EACH ROW EXECUTE FUNCTION guard_inspect_attempt_budget();
CREATE TRIGGER inspect_attempt_budget_snapshot
    BEFORE INSERT ON artifacts
    FOR EACH ROW EXECUTE FUNCTION inspect_attempt_budget_from_snapshot();

CREATE TRIGGER artifact_files_bound_video_check
    BEFORE INSERT OR UPDATE OF bound_video_file_ref, artifact_id ON artifact_files
    FOR EACH ROW EXECUTE FUNCTION check_bound_video_is_video();
CREATE TRIGGER artifact_files_referenced_video_kind_check
    BEFORE UPDATE OF media_kind ON artifact_files
    FOR EACH ROW EXECUTE FUNCTION check_referenced_video_kind_unchanged();

CREATE TRIGGER plan_attempt_budget
    BEFORE INSERT OR UPDATE ON postprocessing_plans
    FOR EACH ROW EXECUTE FUNCTION guard_plan_attempt_budget();
CREATE TRIGGER plan_attempt_budget_snapshot
    BEFORE INSERT ON postprocessing_plans
    FOR EACH ROW EXECUTE FUNCTION plan_attempt_budget_from_snapshot();
CREATE TRIGGER postprocessing_plan_immutable
    BEFORE UPDATE ON postprocessing_plans
    FOR EACH ROW EXECUTE FUNCTION postprocessing_plan_immutable();


CREATE TRIGGER file_attempt_budget
    BEFORE INSERT OR UPDATE ON file_operations
    FOR EACH ROW EXECUTE FUNCTION guard_file_attempt_budget();
CREATE TRIGGER file_operations_verified_artifact_check
    BEFORE INSERT OR UPDATE OF artifact_id, artifact_version, artifact_file_id, source_path, source_fingerprint ON file_operations
    FOR EACH ROW EXECUTE FUNCTION validate_file_operation_artifact();

CREATE TRIGGER file_evidence_bytes
    BEFORE INSERT OR UPDATE ON file_operations
    FOR EACH ROW EXECUTE FUNCTION file_evidence_bytes_guard();
CREATE TRIGGER file_publication_identity
    BEFORE UPDATE ON file_operations
    FOR EACH ROW EXECUTE FUNCTION guard_file_publication_identity();
CREATE TRIGGER file_step_authorization_guard
    BEFORE INSERT OR UPDATE ON file_operations
    FOR EACH ROW EXECUTE FUNCTION file_step_authorization_guard();

-- PRUNE-046 S6: the publication proof columns are written only by the
-- SECURITY DEFINER publication functions. The runtime keeps SELECT/DELETE on
-- the row and INSERT/UPDATE on every column the fold did not add, so a direct
-- write of a publication_* column is refused with 42501.
GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE artifacts, artifact_files TO bansoko_runtime;
GRANT SELECT, DELETE ON TABLE file_operations TO bansoko_runtime;
GRANT INSERT (
    id, artifact_id, artifact_version, artifact_file_id, operation, source_path, target_path,
    source_fingerprint, target_fingerprint, collision_policy, frozen_render_snapshot,
    frozen_config_snapshot, state, observed_at, failure_code, failure_reason, idempotency_key,
    version, created_at, updated_at, overwrite_execution_snapshot, bound_video_operation_id,
    correlation_id, causation_type, causation_id, expected_size_bytes, observed_target_size_bytes,
    intent, acquisition_id, file_ref, required_tag, postprocessing_plan_id, file_ready_at,
    file_wall_until, file_max_attempts, file_attempt_timeout_seconds, budget_clock_at,
    file_attempts_started, file_attempt_ticket, file_attempt_index
) ON TABLE file_operations TO bansoko_runtime;
GRANT UPDATE (
    id, artifact_id, artifact_version, artifact_file_id, operation, source_path, target_path,
    source_fingerprint, target_fingerprint, collision_policy, frozen_render_snapshot,
    frozen_config_snapshot, state, observed_at, failure_code, failure_reason, idempotency_key,
    version, created_at, updated_at, overwrite_execution_snapshot, bound_video_operation_id,
    correlation_id, causation_type, causation_id, expected_size_bytes, observed_target_size_bytes,
    intent, acquisition_id, file_ref, required_tag, postprocessing_plan_id, file_ready_at,
    file_wall_until, file_max_attempts, file_attempt_timeout_seconds, budget_clock_at,
    file_attempts_started, file_attempt_ticket, file_attempt_index
) ON TABLE file_operations TO bansoko_runtime;

GRANT SELECT ON TABLE postprocessing_plans TO bansoko_runtime;
REVOKE ALL ON FUNCTION acquisition_unresolved_effect, acquisition_compact,
    acquisition_live_work, acquisition_unknown_files, artifact_recovery_lock,
    artifact_commit_inspection, artifact_inspect_fail, artifact_inspect_failed,
    work_item_requeue_recovered_inspection, artifact_inspect_requeue_interrupted,
    artifact_inspect_retry, artifact_start_attempt,
    file_step_authorized_at, file_validate_final_proof_before181, file_validate_final_proof,
    file_assert_final_proof, file_cancelled_dependency_suppression, file_execution_lock,
    file_dispatch_lock, file_evidence_bytes_guard, file_failure_effect_class,
    file_failure_notification, file_failure_atomic_body, file_finalization_prelock,
    file_finalization_cancelled_gate, file_finalization_normal_gate, file_finish_failed,
    file_prepare_permit, file_receipt_capture, file_record_conflict, file_record_receipt,
    file_start_attempt, file_start_dispatch, file_start_step, file_step_authorization_guard,
    file_success_handoff, file_success_notification, filefact_source_relation_valid,
    file_success_projection, file_success_atomic_body, guard_file_attempt_budget,
    guard_file_publication_identity,
    guard_inspect_attempt_budget, guard_plan_attempt_budget,
    inspect_attempt_budget_from_snapshot, plan_attempt_budget_from_snapshot, plan_start_attempt,
    postprocessing_plan_clean, postprocessing_plan_recovery_lock,
    postprocessing_plan_capture_lock, postprocessing_plan_child_diagnostic,
    postprocessing_plan_fail, postprocessing_plan_commit,
    postprocessing_plan_commit_state, postprocessing_plan_failed, postprocessing_plan_immutable,
    work_item_requeue_recovered_plan, postprocessing_plan_requeue_interrupted,
    postprocessing_plan_retry, work_item_delete_one_shot FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquisition_unresolved_effect, acquisition_compact,
    acquisition_live_work, artifact_commit_inspection, artifact_inspect_failed,
    artifact_inspect_requeue_interrupted, artifact_inspect_retry, artifact_start_attempt,
    file_finish_failed, file_prepare_permit, file_receipt_capture, file_record_receipt,
    file_start_attempt, file_start_dispatch, file_start_step, plan_start_attempt,
    postprocessing_plan_child_diagnostic, postprocessing_plan_commit,
    postprocessing_plan_commit_state, postprocessing_plan_failed,
    postprocessing_plan_requeue_interrupted, postprocessing_plan_retry,
    work_item_delete_one_shot TO bansoko_runtime;

-- ============================================================================
-- Library placements, replacement, cleanup, reconciliation, alignment and offset migration
-- Placements are files the library owns, with their coverage, obligations and
-- companions; replacement decisions and cleanup operations retire superseded
-- files; reconciliation, alignment, offset migration and file manager operations
-- keep the library consistent with its owners.
-- ============================================================================

CREATE TABLE placements (
    id TEXT NOT NULL,
    physical_path TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    source_evidence_type TEXT NOT NULL,
    source_owner_ref TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    target_fingerprint TEXT NOT NULL,
    quality_facts_ref TEXT,
    facts_version TEXT NOT NULL,
    snapshot_generation BIGINT,
    verification_state TEXT NOT NULL DEFAULT 'verified',
    state TEXT NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    replacement_eligible BOOLEAN NOT NULL DEFAULT true,
    released_at TIMESTAMPTZ,
    released_by TEXT,
    release_request_key TEXT,
    CONSTRAINT placements_pkey PRIMARY KEY (id),
    CONSTRAINT placements_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT placements_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT placements_check CHECK (replacement_eligible OR verification_state = 'verified'),
    CONSTRAINT placements_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT placements_facts_version_check CHECK (char_length(btrim(facts_version)) BETWEEN 1 AND 128),
    CONSTRAINT placements_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT placements_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT placements_physical_path_check CHECK (char_length(btrim(physical_path)) BETWEEN 1 AND 4096),
    CONSTRAINT placements_quality_facts_ref_check CHECK (quality_facts_ref IS NULL OR char_length(btrim(quality_facts_ref)) BETWEEN 1 AND 512),
    CONSTRAINT placements_release_record_check CHECK ((state = 'released') = (released_at IS NOT NULL AND released_by IS NOT NULL AND release_request_key IS NOT NULL)),
    CONSTRAINT placements_release_request_key_check CHECK (release_request_key IS NULL OR char_length(btrim(release_request_key)) BETWEEN 1 AND 128),
    CONSTRAINT placements_released_by_check CHECK (released_by IS NULL OR char_length(btrim(released_by)) BETWEEN 1 AND 128),
    CONSTRAINT placements_snapshot_generation_check CHECK (snapshot_generation IS NULL OR snapshot_generation >= 1),
    CONSTRAINT placements_source_evidence_type_check CHECK (source_evidence_type IN ('file_operation', 'reconciliation', 'recovery')),
    CONSTRAINT placements_source_fingerprint_check CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT placements_source_owner_ref_check CHECK (char_length(btrim(source_owner_ref)) BETWEEN 1 AND 512),
    CONSTRAINT placements_state_check CHECK (state IN ('active', 'superseded', 'released')),
    CONSTRAINT placements_target_fingerprint_check CHECK (char_length(btrim(target_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT placements_verification_state_check CHECK (verification_state IN ('verified', 'unverified', 'excluded')),
    CONSTRAINT placements_version_check CHECK (version >= 1)
);

CREATE INDEX placements_eligible_pool_idx ON placements (managed_anime_id, replacement_eligible, state, created_at, id)
    WHERE verification_state = 'verified';
CREATE UNIQUE INDEX placements_release_request_idx ON placements (release_request_key, id)
    WHERE release_request_key IS NOT NULL;
CREATE UNIQUE INDEX placements_source_uq ON placements (source_evidence_type, source_owner_ref);
-- Byte-order index: exact path lookups and the directory prefix ranges of the
-- file manager both compare with COLLATE "C" so they can use it.
CREATE INDEX placements_physical_path_idx ON placements (physical_path COLLATE "C");

CREATE TABLE placement_coverage (
    placement_id TEXT NOT NULL,
    observation_key TEXT NOT NULL,
    coverage_ordinal INTEGER NOT NULL,
    facts_version TEXT NOT NULL,
    evidence_ref TEXT NOT NULL,
    CONSTRAINT placement_coverage_pkey PRIMARY KEY (placement_id, observation_key),
    CONSTRAINT placement_coverage_placement_id_fkey FOREIGN KEY (placement_id) REFERENCES placements (id) ON DELETE CASCADE,
    CONSTRAINT placement_coverage_coverage_ordinal_check CHECK (coverage_ordinal >= 0),
    CONSTRAINT placement_coverage_evidence_ref_check CHECK (char_length(btrim(evidence_ref)) BETWEEN 1 AND 512),
    CONSTRAINT placement_coverage_facts_version_check CHECK (char_length(btrim(facts_version)) BETWEEN 1 AND 128),
    CONSTRAINT placement_coverage_observation_key_check CHECK (char_length(btrim(observation_key)) BETWEEN 1 AND 128)
);

CREATE INDEX placement_coverage_key_idx ON placement_coverage (observation_key);

CREATE TABLE placement_obligations (
    file_operation_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    reason_code TEXT,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    settled_at TIMESTAMPTZ,
    CONSTRAINT placement_obligations_pkey PRIMARY KEY (file_operation_id),
    CONSTRAINT placement_obligations_file_operation_id_fkey FOREIGN KEY (file_operation_id) REFERENCES file_operations (id),
    CONSTRAINT placement_obligations_attempts_check CHECK (attempts >= 0),
    CONSTRAINT placement_obligations_check CHECK (attempts <= max_attempts),
    CONSTRAINT placement_obligations_check1 CHECK (state IN ('waiting_retry', 'failed') OR (reason_code IS NULL AND reason IS NULL)),
    CONSTRAINT placement_obligations_check2 CHECK (state = 'admitted' OR settled_at IS NULL),
    CONSTRAINT placement_obligations_check3 CHECK (state <> 'admitted' OR settled_at IS NOT NULL),
    CONSTRAINT placement_obligations_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 100),
    CONSTRAINT placement_obligations_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT placement_obligations_reason_code_check CHECK (reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT placement_obligations_state_check CHECK (state IN ('pending', 'waiting_retry', 'admitted', 'failed'))
);

COMMENT ON TABLE placement_obligations IS 'Durable post-completion placement handoff obligation: pending -> waiting_retry -> admitted | failed; manual retry re-opens a failed row. Never rewrites the completed file operation.';
CREATE TRIGGER placement_obligations_compact_pin
    AFTER INSERT OR DELETE ON placement_obligations
    FOR EACH ROW EXECUTE FUNCTION acquisition_compact_pin_from_obligation();
COMMENT ON COLUMN placement_obligations.state IS 'pending: owed, not yet attempted; waiting_retry: transient stage failure recorded, continuation item live; admitted: placement/index/trigger handoff settled idempotently; failed: attempt budget exhausted, manual retry re-opens.';

CREATE TABLE placement_companions (
    id TEXT NOT NULL,
    placement_id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    source_ref TEXT NOT NULL,
    registration TEXT NOT NULL,
    media_kind TEXT NOT NULL,
    suffix TEXT NOT NULL DEFAULT '',
    original_extension TEXT NOT NULL,
    current_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    ended_reason TEXT,
    alignment_failure_code TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT placement_companions_pkey PRIMARY KEY (id),
    CONSTRAINT placement_companions_placement_id_fkey FOREIGN KEY (placement_id) REFERENCES placements (id) ON DELETE CASCADE,
    CONSTRAINT placement_companions_alignment_failure_code_check CHECK (alignment_failure_code IS NULL OR char_length(btrim(alignment_failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT placement_companions_check CHECK ((state = 'ended') = (ended_reason IS NOT NULL)),
    CONSTRAINT placement_companions_current_path_check CHECK (char_length(btrim(current_path)) BETWEEN 1 AND 4096),
    CONSTRAINT placement_companions_ended_reason_check CHECK (ended_reason IS NULL OR char_length(btrim(ended_reason)) BETWEEN 1 AND 128),
    CONSTRAINT placement_companions_fingerprint_check CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT placement_companions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT placement_companions_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT placement_companions_media_kind_check CHECK (media_kind IN ('subtitle', 'other')),
    CONSTRAINT placement_companions_original_extension_check CHECK (char_length(btrim(original_extension)) BETWEEN 1 AND 32 AND original_extension !~ '[./\\]'),
    CONSTRAINT placement_companions_registration_check CHECK (registration IN ('intake', 'adopted')),
    CONSTRAINT placement_companions_size_bytes_check CHECK (size_bytes >= 0),
    CONSTRAINT placement_companions_source_ref_check CHECK (char_length(btrim(source_ref)) BETWEEN 1 AND 512),
    CONSTRAINT placement_companions_state_check CHECK (state IN ('active', 'ended')),
    CONSTRAINT placement_companions_suffix_check CHECK (char_length(suffix) <= 255 AND suffix !~ '[/\\]'),
    CONSTRAINT placement_companions_version_check CHECK (version >= 1)
);

CREATE UNIQUE INDEX placement_companions_active_path_uq ON placement_companions (current_path)
    WHERE state = 'active';
CREATE INDEX placement_companions_current_path_c_idx ON placement_companions (current_path COLLATE "C")
    WHERE state = 'active';
CREATE INDEX placement_companions_placement_idx ON placement_companions (placement_id);
CREATE UNIQUE INDEX placement_companions_source_uq ON placement_companions (source_ref);

CREATE TABLE replacement_decisions (
    id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    candidate_release_id TEXT NOT NULL,
    policy_version BIGINT NOT NULL,
    profile_version TEXT NOT NULL,
    candidate_facts_ref TEXT NOT NULL,
    snapshot_generation BIGINT,
    coverage_keys JSONB NOT NULL,
    admission_mode TEXT NOT NULL,
    pools JSONB NOT NULL,
    verdict TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'admitted',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    candidate_placement_id TEXT,
    subscription_version BIGINT,
    input_digest TEXT,
    stale_reason TEXT,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    coverage_key TEXT NOT NULL,
    CONSTRAINT replacement_decisions_pkey PRIMARY KEY (id),
    CONSTRAINT replacement_decisions_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT replacement_decisions_admission_mode_check CHECK (admission_mode IN ('all', 'per_group', 'single_copy')),
    CONSTRAINT replacement_decisions_admitted_frozen_check CHECK (state <> 'admitted' OR ((candidate_placement_id IS NOT NULL AND subscription_version IS NOT NULL AND input_digest IS NOT NULL) OR (candidate_placement_id IS NULL AND subscription_version IS NOT NULL AND input_digest IS NOT NULL AND jsonb_path_exists(pools, '$[*]?(@."winner"."placement_id" starts with "release:")'::jsonpath)))),
    CONSTRAINT replacement_decisions_candidate_facts_ref_check CHECK (char_length(btrim(candidate_facts_ref)) BETWEEN 1 AND 512),
    CONSTRAINT replacement_decisions_candidate_placement_id_check CHECK (candidate_placement_id IS NULL OR char_length(btrim(candidate_placement_id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_candidate_release_id_check CHECK (char_length(btrim(candidate_release_id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_coverage_key_check CHECK (char_length(btrim(coverage_key)) BETWEEN 1 AND 256),
    CONSTRAINT replacement_decisions_coverage_keys_check CHECK (jsonb_typeof(coverage_keys) = 'array' AND pg_column_size(coverage_keys) <= 65536),
    CONSTRAINT replacement_decisions_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT replacement_decisions_input_digest_check CHECK (input_digest IS NULL OR char_length(btrim(input_digest)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_policy_version_check CHECK (policy_version >= 1),
    CONSTRAINT replacement_decisions_pools_check CHECK (jsonb_typeof(pools) = 'array' AND pg_column_size(pools) <= 524288),
    CONSTRAINT replacement_decisions_profile_version_check CHECK (char_length(btrim(profile_version)) BETWEEN 1 AND 64),
    CONSTRAINT replacement_decisions_snapshot_generation_check CHECK (snapshot_generation IS NULL OR snapshot_generation >= 1),
    CONSTRAINT replacement_decisions_stale_reason_check CHECK (stale_reason IS NULL OR char_length(btrim(stale_reason)) BETWEEN 1 AND 128),
    CONSTRAINT replacement_decisions_state_check CHECK (state IN ('admitted', 'queued', 'settled', 'stale', 'blocked', 'failed')),
    CONSTRAINT replacement_decisions_subscription_version_check CHECK (subscription_version IS NULL OR subscription_version >= 1),
    CONSTRAINT replacement_decisions_verdict_check CHECK (verdict IN ('keep', 'retain', 'queue', 'replace', 'blocked')),
    CONSTRAINT replacement_decisions_version_check CHECK (version >= 1)
);

CREATE INDEX replacement_decisions_anime_state_idx ON replacement_decisions (managed_anime_id, state);
CREATE INDEX replacement_decisions_candidate_placement_idx ON replacement_decisions (candidate_placement_id)
    WHERE state = 'admitted';
CREATE INDEX replacement_decisions_release_key_idx ON replacement_decisions (managed_anime_id, candidate_release_id, coverage_key);

CREATE TABLE cleanup_operations (
    id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    placement_id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    coverage_keys JSONB NOT NULL,
    replacement_ref TEXT NOT NULL,
    policy_version BIGINT NOT NULL,
    retention_mode TEXT NOT NULL,
    source_path TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    quarantine_path TEXT,
    effect TEXT NOT NULL,
    grace_deadline TIMESTAMPTZ,
    state TEXT NOT NULL DEFAULT 'admitted',
    attempt JSONB,
    failure_code TEXT,
    reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    correlation_id TEXT,
    causation_type TEXT,
    causation_id TEXT,
    companion_id TEXT,
    CONSTRAINT cleanup_operations_pkey PRIMARY KEY (id),
    CONSTRAINT cleanup_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT cleanup_operations_companion_id_fkey FOREIGN KEY (companion_id) REFERENCES placement_companions (id) ON DELETE SET NULL,
    CONSTRAINT cleanup_operations_attempt_check CHECK (attempt IS NULL OR jsonb_typeof(attempt) = 'object'),
    CONSTRAINT cleanup_operations_causation_id_check CHECK (causation_id IS NULL OR char_length(btrim(causation_id)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_causation_type_check CHECK (causation_type IS NULL OR char_length(btrim(causation_type)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_correlation_id_check CHECK (correlation_id IS NULL OR char_length(btrim(correlation_id)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_coverage_keys_check CHECK (jsonb_typeof(coverage_keys) = 'array' AND pg_column_size(coverage_keys) <= 65536),
    CONSTRAINT cleanup_operations_effect_check CHECK (effect IN ('quarantine', 'unlink', 'purge')),
    CONSTRAINT cleanup_operations_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT cleanup_operations_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_placement_id_check CHECK (char_length(btrim(placement_id)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_policy_version_check CHECK (policy_version >= 1),
    CONSTRAINT cleanup_operations_quarantine_path_check CHECK (quarantine_path IS NULL OR char_length(btrim(quarantine_path)) BETWEEN 1 AND 4096),
    CONSTRAINT cleanup_operations_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT cleanup_operations_replacement_ref_check CHECK (char_length(btrim(replacement_ref)) BETWEEN 1 AND 512),
    CONSTRAINT cleanup_operations_retention_mode_check CHECK (retention_mode IN ('per_group', 'single_copy')),
    CONSTRAINT cleanup_operations_source_fingerprint_check CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT cleanup_operations_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096),
    CONSTRAINT cleanup_operations_state_check CHECK (state IN ('admitted', 'quarantined', 'grace_waiting', 'purged', 'blocked', 'failed', 'completed')),
    CONSTRAINT cleanup_operations_version_check CHECK (version >= 1)
);

CREATE TABLE reconciliation_scans (
    managed_anime_id TEXT NOT NULL,
    state TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    scan_scope JSONB NOT NULL,
    progress JSONB NOT NULL,
    capacity_stats JSONB NOT NULL,
    input_fingerprint TEXT NOT NULL,
    failure_code TEXT,
    failure_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Folded consumer checkpoints (PRUNE-046 S10): only the latest settled
    -- generation per consumer is kept, keyed by the anchor generation below,
    -- so the cross-generation history of the old checkpoint table is gone.
    alignment_checkpoint_generation BIGINT,
    alignment_checkpoint_state TEXT,
    alignment_checkpoint_reason_code TEXT,
    alignment_checkpoint_reason TEXT,
    alignment_checkpoint_version BIGINT NOT NULL DEFAULT 0,
    alignment_checkpoint_updated_at TIMESTAMPTZ,
    replacement_checkpoint_generation BIGINT,
    replacement_checkpoint_state TEXT,
    replacement_checkpoint_reason_code TEXT,
    replacement_checkpoint_reason TEXT,
    replacement_checkpoint_version BIGINT NOT NULL DEFAULT 0,
    replacement_checkpoint_updated_at TIMESTAMPTZ,
    CONSTRAINT reconciliation_scans_pkey PRIMARY KEY (managed_anime_id),
    CONSTRAINT reconciliation_scans_capacity_stats_check CHECK (jsonb_typeof(capacity_stats) = 'object' AND pg_column_size(capacity_stats) <= 262144),
    CONSTRAINT reconciliation_scans_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_scans_failure_reason_check CHECK (failure_reason IS NULL OR char_length(failure_reason) <= 2048),
    CONSTRAINT reconciliation_scans_generation_check CHECK (generation >= 0),
    CONSTRAINT reconciliation_scans_input_fingerprint_check CHECK (char_length(btrim(input_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_scans_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_scans_progress_check CHECK (jsonb_typeof(progress) = 'object' AND pg_column_size(progress) <= 262144),
    CONSTRAINT reconciliation_scans_scan_scope_check CHECK (jsonb_typeof(scan_scope) = 'object' AND pg_column_size(scan_scope) <= 262144),
    CONSTRAINT reconciliation_scans_state_check CHECK (state IN ('idle', 'running', 'completed', 'failed', 'canceled')),
    CONSTRAINT reconciliation_scans_version_check CHECK (version >= 1),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_generation_check CHECK (alignment_checkpoint_generation IS NULL OR alignment_checkpoint_generation >= 1),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_pair_check CHECK ((alignment_checkpoint_state IS NULL) = (alignment_checkpoint_generation IS NULL)),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_reason_check CHECK (alignment_checkpoint_reason IS NULL OR char_length(alignment_checkpoint_reason) <= 2048),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_reason_code_check CHECK (alignment_checkpoint_reason_code IS NULL OR char_length(btrim(alignment_checkpoint_reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_state_check CHECK (alignment_checkpoint_state IS NULL OR alignment_checkpoint_state IN ('pending', 'consumed', 'blocked', 'failed')),
    CONSTRAINT reconciliation_scans_alignment_checkpoint_version_check CHECK (alignment_checkpoint_version >= 0),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_generation_check CHECK (replacement_checkpoint_generation IS NULL OR replacement_checkpoint_generation >= 1),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_pair_check CHECK ((replacement_checkpoint_state IS NULL) = (replacement_checkpoint_generation IS NULL)),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_reason_check CHECK (replacement_checkpoint_reason IS NULL OR char_length(replacement_checkpoint_reason) <= 2048),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_reason_code_check CHECK (replacement_checkpoint_reason_code IS NULL OR char_length(btrim(replacement_checkpoint_reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_state_check CHECK (replacement_checkpoint_state IS NULL OR replacement_checkpoint_state IN ('pending', 'consumed', 'blocked', 'failed')),
    CONSTRAINT reconciliation_scans_replacement_checkpoint_version_check CHECK (replacement_checkpoint_version >= 0)
);

-- One row per (anime, generation, path): the immutable scan observation and
-- the typed calibration of that observation, plus the calibration-only rows of
-- the derived missing set (paths that existed in the previous generation and
-- are absent now; derived_missing, no observation columns). The scan writes the
-- observation half, the calibration upsert fills the calibration half once.
CREATE TABLE reconciliation_files (
    managed_anime_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    file_path TEXT NOT NULL,
    derived_missing BOOLEAN NOT NULL DEFAULT FALSE,
    batch_index INTEGER,
    fingerprint TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    facts JSONB,
    observed_at TIMESTAMPTZ,
    disposition TEXT,
    coverage JSONB,
    resolution TEXT,
    revision TEXT,
    source TEXT,
    language TEXT,
    "group" TEXT,
    parser_version TEXT,
    facts_fingerprint TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT reconciliation_files_pkey PRIMARY KEY (managed_anime_id, generation, file_path),
    CONSTRAINT reconciliation_files_batch_index_check CHECK (batch_index IS NULL OR batch_index >= 0),
    CONSTRAINT reconciliation_files_coverage_check CHECK (coverage IS NULL OR (jsonb_typeof(coverage) = 'array' AND pg_column_size(coverage) <= 65536)),
    CONSTRAINT reconciliation_files_derived_missing_check CHECK (derived_missing = FALSE OR (batch_index IS NULL AND facts IS NULL AND observed_at IS NULL)),
    CONSTRAINT reconciliation_files_disposition_check CHECK (disposition IS NULL OR disposition IN ('present', 'unresolved', 'missing', 'fingerprint_conflict')),
    CONSTRAINT reconciliation_files_facts_check CHECK (facts IS NULL OR (jsonb_typeof(facts) = 'object' AND pg_column_size(facts) <= 262144)),
    CONSTRAINT reconciliation_files_facts_fingerprint_check CHECK (facts_fingerprint IS NULL OR char_length(btrim(facts_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_file_path_check CHECK (char_length(btrim(file_path)) BETWEEN 1 AND 4096),
    CONSTRAINT reconciliation_files_fingerprint_check CHECK (char_length(btrim(fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_generation_check CHECK (generation >= 1),
    CONSTRAINT reconciliation_files_group_check CHECK ("group" IS NULL OR (char_length(btrim("group")) >= 1 AND char_length(btrim("group")) <= 128)),
    CONSTRAINT reconciliation_files_language_check CHECK (language IS NULL OR char_length(btrim(language)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_observed_check CHECK (derived_missing OR observed_at IS NOT NULL),
    CONSTRAINT reconciliation_files_parser_version_check CHECK (parser_version IS NULL OR char_length(btrim(parser_version)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_resolution_check CHECK (resolution IS NULL OR char_length(btrim(resolution)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_revision_check CHECK (revision IS NULL OR char_length(btrim(revision)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_size_bytes_check CHECK (size_bytes >= 0),
    CONSTRAINT reconciliation_files_source_check CHECK (source IS NULL OR char_length(btrim(source)) BETWEEN 1 AND 128),
    CONSTRAINT reconciliation_files_version_check CHECK (version >= 1)
);

CREATE INDEX reconciliation_files_gen_idx ON reconciliation_files (managed_anime_id, generation);

CREATE TABLE alignment_operations (
    id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    scan_generation BIGINT NOT NULL,
    observation_ref TEXT NOT NULL,
    candidate_ref TEXT NOT NULL,
    policy_version BIGINT NOT NULL,
    metadata_version BIGINT NOT NULL,
    rules_version BIGINT NOT NULL,
    naming_version BIGINT NOT NULL,
    source_path TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    target_path TEXT NOT NULL,
    target_contract JSONB NOT NULL,
    dry_run_result JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'planned',
    attempt JSONB,
    failure_code TEXT,
    reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    companion_id TEXT,
    CONSTRAINT alignment_operations_pkey PRIMARY KEY (id),
    CONSTRAINT alignment_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT alignment_operations_companion_id_fkey FOREIGN KEY (companion_id) REFERENCES placement_companions (id) ON DELETE CASCADE,
    CONSTRAINT alignment_operations_attempt_check CHECK (attempt IS NULL OR jsonb_typeof(attempt) = 'object'),
    CONSTRAINT alignment_operations_candidate_ref_check CHECK (char_length(btrim(candidate_ref)) BETWEEN 1 AND 512),
    CONSTRAINT alignment_operations_dry_run_result_check CHECK (jsonb_typeof(dry_run_result) = 'object' AND pg_column_size(dry_run_result) <= 262144),
    CONSTRAINT alignment_operations_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT alignment_operations_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT alignment_operations_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT alignment_operations_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT alignment_operations_metadata_version_check CHECK (metadata_version >= 1),
    CONSTRAINT alignment_operations_naming_version_check CHECK (naming_version >= 1),
    CONSTRAINT alignment_operations_observation_ref_check CHECK (char_length(btrim(observation_ref)) BETWEEN 1 AND 512),
    CONSTRAINT alignment_operations_policy_version_check CHECK (policy_version >= 1),
    CONSTRAINT alignment_operations_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT alignment_operations_rules_version_check CHECK (rules_version >= 1),
    CONSTRAINT alignment_operations_scan_generation_check CHECK (scan_generation >= 1),
    CONSTRAINT alignment_operations_source_fingerprint_check CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT alignment_operations_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096),
    CONSTRAINT alignment_operations_state_check CHECK (state IN ('planned', 'ready', 'running', 'completed', 'blocked', 'failed', 'canceled')),
    CONSTRAINT alignment_operations_target_contract_check CHECK (jsonb_typeof(target_contract) = 'object' AND pg_column_size(target_contract) <= 65536),
    CONSTRAINT alignment_operations_target_path_check CHECK (char_length(btrim(target_path)) BETWEEN 1 AND 4096),
    CONSTRAINT alignment_operations_version_check CHECK (version >= 1)
);

CREATE TABLE offset_migration_generations (
    id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_owner_id TEXT NOT NULL,
    policy_version BIGINT NOT NULL,
    old_offset INTEGER NOT NULL,
    new_offset INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    planned_count INTEGER NOT NULL DEFAULT 0,
    blocked_count INTEGER NOT NULL DEFAULT 0,
    applied_count INTEGER NOT NULL DEFAULT 0,
    superseded_count INTEGER NOT NULL DEFAULT 0,
    reason_code TEXT,
    reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    CONSTRAINT offset_migration_generations_pkey PRIMARY KEY (id),
    CONSTRAINT offset_migration_generatio_scope_kind_scope_owner_id_pol_key UNIQUE (scope_kind, scope_owner_id, policy_version),
    CONSTRAINT offset_migration_generations_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT offset_migration_generations_applied_count_check CHECK (applied_count >= 0),
    CONSTRAINT offset_migration_generations_blocked_count_check CHECK (blocked_count >= 0),
    CONSTRAINT offset_migration_generations_check CHECK ((state IN ('completed', 'completed_with_blocks', 'failed', 'superseded') AND completed_at IS NOT NULL) OR (state NOT IN ('completed', 'completed_with_blocks', 'failed', 'superseded') AND completed_at IS NULL)),
    CONSTRAINT offset_migration_generations_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_generations_new_offset_check CHECK (new_offset >= '-1000'::integer AND new_offset <= 1000),
    CONSTRAINT offset_migration_generations_old_offset_check CHECK (old_offset >= '-1000'::integer AND old_offset <= 1000),
    CONSTRAINT offset_migration_generations_planned_count_check CHECK (planned_count >= 0),
    CONSTRAINT offset_migration_generations_policy_version_check CHECK (policy_version >= 1),
    CONSTRAINT offset_migration_generations_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT offset_migration_generations_reason_code_check CHECK (reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_generations_scope_kind_check CHECK (scope_kind = 'rss_source'),
    CONSTRAINT offset_migration_generations_scope_owner_id_check CHECK (char_length(btrim(scope_owner_id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_generations_state_check CHECK (state IN ('pending', 'planning', 'ready', 'running', 'completed', 'completed_with_blocks', 'failed', 'superseded')),
    CONSTRAINT offset_migration_generations_superseded_count_check CHECK (superseded_count >= 0),
    CONSTRAINT offset_migration_generations_version_check CHECK (version >= 1)
);

CREATE INDEX offset_migration_generations_live_idx ON offset_migration_generations (state, updated_at, id)
    WHERE state IN ('pending', 'planning', 'ready', 'running');

CREATE TABLE offset_migration_plans (
    id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    placement_id TEXT NOT NULL,
    policy_version BIGINT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    raw_episode TEXT,
    new_episode TEXT,
    old_target_path TEXT NOT NULL,
    new_target_path TEXT,
    state TEXT NOT NULL,
    failure_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    generation_id TEXT,
    scope_kind TEXT,
    scope_owner_id TEXT,
    placement_version BIGINT,
    source_path TEXT,
    fingerprint JSONB,
    media_fact_snapshot_id TEXT,
    media_fact_file_ref TEXT,
    rss_source_id TEXT,
    source_episode_offset INTEGER,
    media_kind TEXT,
    bound_season INTEGER,
    old_identity JSONB,
    new_identity JSONB,
    old_coverage_key TEXT,
    new_coverage_key TEXT,
    target_contract JSONB,
    attempt JSONB,
    reason_code TEXT,
    completed_at TIMESTAMPTZ,
    CONSTRAINT offset_migration_plans_pkey PRIMARY KEY (id),
    CONSTRAINT offset_migration_plans_policy_version_placement_id_sourc_key UNIQUE (policy_version, placement_id, source_fingerprint),
    CONSTRAINT offset_migration_plans_generation_id_fkey FOREIGN KEY (generation_id) REFERENCES offset_migration_generations (id),
    CONSTRAINT offset_migration_plans_media_fact_file_fk FOREIGN KEY (media_fact_snapshot_id, media_fact_file_ref) REFERENCES media_fact_files (snapshot_id, file_ref),
    CONSTRAINT offset_migration_plans_rss_source_id_fkey FOREIGN KEY (rss_source_id) REFERENCES rss_sources (id),
    CONSTRAINT offset_migration_plans_attempt_check CHECK (attempt IS NULL OR (jsonb_typeof(attempt) = 'object' AND pg_column_size(attempt) <= 65536)),
    CONSTRAINT offset_migration_plans_bound_season_check CHECK (bound_season IS NULL OR bound_season >= 0),
    CONSTRAINT offset_migration_plans_failure_reason_check CHECK (failure_reason IS NULL OR char_length(btrim(failure_reason)) <= 1024),
    CONSTRAINT offset_migration_plans_fingerprint_check CHECK (fingerprint IS NULL OR (jsonb_typeof(fingerprint) = 'object' AND pg_column_size(fingerprint) <= 262144)),
    CONSTRAINT offset_migration_plans_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_media_fact_file_pair_check CHECK ((media_fact_snapshot_id IS NULL) = (media_fact_file_ref IS NULL)),
    CONSTRAINT offset_migration_plans_media_fact_file_ref_check CHECK (media_fact_file_ref IS NULL OR char_length(btrim(media_fact_file_ref)) BETWEEN 1 AND 512),
    CONSTRAINT offset_migration_plans_media_kind_check CHECK (media_kind IS NULL OR media_kind IN ('tv', 'movie')),
    CONSTRAINT offset_migration_plans_new_coverage_key_check CHECK (new_coverage_key IS NULL OR char_length(btrim(new_coverage_key)) BETWEEN 1 AND 512),
    CONSTRAINT offset_migration_plans_new_episode_check CHECK (char_length(btrim(new_episode)) BETWEEN 1 AND 64),
    CONSTRAINT offset_migration_plans_new_identity_check CHECK (new_identity IS NULL OR (jsonb_typeof(new_identity) = 'object' AND pg_column_size(new_identity) <= 65536)),
    CONSTRAINT offset_migration_plans_new_target_path_check CHECK (char_length(btrim(new_target_path)) BETWEEN 1 AND 4096),
    CONSTRAINT offset_migration_plans_old_coverage_key_check CHECK (old_coverage_key IS NULL OR char_length(btrim(old_coverage_key)) BETWEEN 1 AND 512),
    CONSTRAINT offset_migration_plans_old_identity_check CHECK (old_identity IS NULL OR (jsonb_typeof(old_identity) = 'object' AND pg_column_size(old_identity) <= 65536)),
    CONSTRAINT offset_migration_plans_old_target_path_check CHECK (char_length(btrim(old_target_path)) BETWEEN 1 AND 4096),
    CONSTRAINT offset_migration_plans_placement_id_check CHECK (char_length(btrim(placement_id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_placement_version_check CHECK (placement_version IS NULL OR placement_version >= 1),
    CONSTRAINT offset_migration_plans_policy_version_check CHECK (policy_version >= 1),
    CONSTRAINT offset_migration_plans_raw_episode_check CHECK (char_length(btrim(raw_episode)) BETWEEN 1 AND 64),
    CONSTRAINT offset_migration_plans_reason_code_check CHECK (reason_code IS NULL OR char_length(btrim(reason_code)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_scope_kind_check CHECK (scope_kind IS NULL OR scope_kind = 'rss_source'),
    CONSTRAINT offset_migration_plans_scope_owner_id_check CHECK (scope_owner_id IS NULL OR char_length(btrim(scope_owner_id)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_source_fingerprint_check CHECK (char_length(btrim(source_fingerprint)) BETWEEN 1 AND 128),
    CONSTRAINT offset_migration_plans_source_offset_override_check CHECK (source_episode_offset IS NULL OR (source_episode_offset >= '-1000'::integer AND source_episode_offset <= 1000)),
    CONSTRAINT offset_migration_plans_source_path_check CHECK (source_path IS NULL OR char_length(btrim(source_path)) BETWEEN 1 AND 4096),
    CONSTRAINT offset_migration_plans_state_check CHECK (state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown', 'applied', 'blocked', 'failed', 'superseded')),
    CONSTRAINT offset_migration_plans_target_contract_check CHECK (target_contract IS NULL OR (jsonb_typeof(target_contract) = 'object' AND pg_column_size(target_contract) <= 65536)),
    CONSTRAINT offset_migration_plans_version_check CHECK (version >= 1)
);

CREATE INDEX offset_migration_plans_anime_idx ON offset_migration_plans (managed_anime_id, policy_version, state, created_at, id);
CREATE INDEX offset_migration_plans_generation_live_idx ON offset_migration_plans (generation_id, state, updated_at, id)
    WHERE generation_id IS NOT NULL AND state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown');
CREATE INDEX offset_migration_plans_recovery_idx ON offset_migration_plans (state, updated_at, id)
    WHERE state IN ('running', 'waiting_retry', 'effect_unknown');

CREATE TABLE file_manager_operations (
    id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    actor TEXT NOT NULL,
    operation TEXT NOT NULL,
    root_key TEXT NOT NULL,
    root_path TEXT NOT NULL,
    target_root_key TEXT,
    target_root_path TEXT,
    source_path TEXT NOT NULL,
    target_path TEXT,
    expected_size_bytes BIGINT NOT NULL,
    collision TEXT NOT NULL,
    release_placement_id TEXT,
    attempt JSONB,
    result_path TEXT,
    state TEXT NOT NULL DEFAULT 'ready',
    failure_code TEXT,
    reason TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    match_anime_id TEXT,
    match_season INTEGER,
    match_episode INTEGER,
    match_episode_titles JSONB,
    created_dirs JSONB,
    CONSTRAINT file_manager_operations_pkey PRIMARY KEY (id),
    CONSTRAINT file_manager_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT file_manager_operations_actor_check CHECK (char_length(btrim(actor)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_attempt_check CHECK (attempt IS NULL OR (jsonb_typeof(attempt) = 'object' AND pg_column_size(attempt) <= 65536)),
    CONSTRAINT file_manager_operations_collision_check CHECK (collision IN ('block', 'version')),
    CONSTRAINT file_manager_operations_created_dirs_check CHECK (created_dirs IS NULL OR (jsonb_typeof(created_dirs) = 'array' AND pg_column_size(created_dirs) <= 16384)),
    CONSTRAINT file_manager_operations_expected_size_bytes_check CHECK (expected_size_bytes >= 0),
    CONSTRAINT file_manager_operations_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_match_anime_id_check CHECK (match_anime_id IS NULL OR char_length(btrim(match_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_match_check CHECK ((operation = 'match') = (match_anime_id IS NOT NULL) AND (operation = 'match') = (match_season IS NOT NULL) AND (operation = 'match') = (match_episode IS NOT NULL) AND (operation = 'match' OR (match_episode_titles IS NULL AND created_dirs IS NULL))),
    CONSTRAINT file_manager_operations_match_episode_check CHECK (match_episode IS NULL OR match_episode BETWEEN 1 AND 99999),
    CONSTRAINT file_manager_operations_match_episode_titles_check CHECK (match_episode_titles IS NULL OR (jsonb_typeof((match_episode_titles -> 'episode')) = 'string' AND render_episode_titles_valid(jsonb_build_object('renderer_version', 'fileoperation-render-v1', 'episode_titles', match_episode_titles)))),
    CONSTRAINT file_manager_operations_match_season_check CHECK (match_season IS NULL OR match_season BETWEEN 0 AND 9999),
    CONSTRAINT file_manager_operations_operation_check CHECK (operation IN ('move', 'rename', 'copy', 'delete', 'match')),
    CONSTRAINT file_manager_operations_reason_check CHECK (reason IS NULL OR char_length(reason) <= 2048),
    CONSTRAINT file_manager_operations_release_placement_id_check CHECK (release_placement_id IS NULL OR char_length(btrim(release_placement_id)) BETWEEN 1 AND 128),
    CONSTRAINT file_manager_operations_request_digest_check CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT file_manager_operations_result_path_check CHECK (result_path IS NULL OR char_length(btrim(result_path)) BETWEEN 1 AND 4096),
    CONSTRAINT file_manager_operations_root_key_check CHECK (char_length(btrim(root_key)) BETWEEN 1 AND 160),
    CONSTRAINT file_manager_operations_root_path_check CHECK (char_length(btrim(root_path)) BETWEEN 1 AND 4096),
    CONSTRAINT file_manager_operations_source_path_check CHECK (char_length(btrim(source_path)) BETWEEN 1 AND 4096),
    CONSTRAINT file_manager_operations_state_check CHECK (state IN ('ready', 'running', 'completed', 'blocked', 'failed')),
    CONSTRAINT file_manager_operations_target_check CHECK ((operation = 'delete') = (target_path IS NULL) AND (target_path IS NULL) = (target_root_key IS NULL) AND (target_root_key IS NULL) = (target_root_path IS NULL) AND (operation IN ('delete', 'copy') OR target_path <> source_path)),
    CONSTRAINT file_manager_operations_target_path_check CHECK (target_path IS NULL OR char_length(btrim(target_path)) BETWEEN 1 AND 4096),
    CONSTRAINT file_manager_operations_target_root_key_check CHECK (target_root_key IS NULL OR char_length(btrim(target_root_key)) BETWEEN 1 AND 160),
    CONSTRAINT file_manager_operations_target_root_path_check CHECK (target_root_path IS NULL OR char_length(btrim(target_root_path)) BETWEEN 1 AND 4096),
    CONSTRAINT file_manager_operations_terminal_check CHECK ((state IN ('completed', 'blocked', 'failed')) = (completed_at IS NOT NULL) AND (state IN ('blocked', 'failed')) = (failure_code IS NOT NULL)),
    CONSTRAINT file_manager_operations_version_check CHECK (version >= 1)
);

CREATE INDEX file_manager_operations_recent_idx ON file_manager_operations (created_at DESC, id DESC);
CREATE INDEX file_manager_operations_source_idx ON file_manager_operations (source_path)
    WHERE state IN ('ready', 'running');
CREATE INDEX file_manager_operations_target_idx ON file_manager_operations (target_path)
    WHERE state IN ('ready', 'running');

CREATE TABLE episode_title_refreshes (
    file_operation_id TEXT NOT NULL,
    managed_anime_id TEXT NOT NULL,
    episode_titles JSONB NOT NULL,
    request_key TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    refreshed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT episode_title_refreshes_pkey PRIMARY KEY (file_operation_id),
    CONSTRAINT episode_title_refreshes_file_operation_id_fkey FOREIGN KEY (file_operation_id) REFERENCES file_operations (id) ON DELETE CASCADE,
    CONSTRAINT episode_title_refreshes_episode_titles_check CHECK (jsonb_typeof((episode_titles -> 'episode')) = 'string' AND render_episode_titles_valid(jsonb_build_object('renderer_version', 'fileoperation-render-v1', 'episode_titles', episode_titles))),
    CONSTRAINT episode_title_refreshes_managed_anime_id_check CHECK (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128),
    CONSTRAINT episode_title_refreshes_request_key_check CHECK (char_length(btrim(request_key)) BETWEEN 1 AND 128),
    CONSTRAINT episode_title_refreshes_version_check CHECK (version >= 1)
);

CREATE INDEX episode_title_refreshes_anime_idx ON episode_title_refreshes (managed_anime_id);

CREATE FUNCTION fence_placement_obligation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    resolved_managed_anime_id TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        resolved_managed_anime_id := managed_anime_of_owner_chain(
            'artifact',
            (SELECT fo.artifact_id FROM file_operations fo WHERE fo.id = OLD.file_operation_id));
        IF local_cleanup_authorized(resolved_managed_anime_id) THEN
            RETURN OLD;
        END IF;
        PERFORM assert_managed_anime_active(resolved_managed_anime_id);
        RETURN OLD;
    END IF;

    resolved_managed_anime_id := managed_anime_of_owner_chain(
        'artifact',
        (SELECT fo.artifact_id FROM file_operations fo WHERE fo.id = NEW.file_operation_id));
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'pending' OR NEW.attempts <> 0 OR NEW.settled_at IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_OBLIGATION_START_INVALID';
        END IF;
    ELSE
        IF NEW.file_operation_id IS DISTINCT FROM OLD.file_operation_id THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_OBLIGATION_OWNER_IMMUTABLE';
        END IF;
        IF OLD.state = 'admitted' AND NEW.state <> 'admitted' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_OBLIGATION_ADMITTED_STICKY';
        END IF;
        IF OLD.state NOT IN ('pending', 'waiting_retry', 'failed')
           OR NEW.state NOT IN ('pending', 'waiting_retry', 'failed', 'admitted') THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_OBLIGATION_TRANSITION_INVALID';
        END IF;
    END IF;

    PERFORM assert_managed_anime_active(resolved_managed_anime_id);
    RETURN NEW;
END;
$$;

CREATE FUNCTION file_manager_operations_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF OLD.state IN ('completed', 'blocked', 'failed') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_TERMINAL';
    END IF;
    IF (NEW.id, NEW.idempotency_key, NEW.request_digest, NEW.actor, NEW.operation, NEW.root_key, NEW.root_path,
        NEW.target_root_key, NEW.target_root_path, NEW.source_path, NEW.target_path, NEW.expected_size_bytes,
        NEW.collision, NEW.release_placement_id, NEW.created_at,
        NEW.match_anime_id, NEW.match_season, NEW.match_episode, NEW.match_episode_titles)
       IS DISTINCT FROM
       (OLD.id, OLD.idempotency_key, OLD.request_digest, OLD.actor, OLD.operation, OLD.root_key, OLD.root_path,
        OLD.target_root_key, OLD.target_root_path, OLD.source_path, OLD.target_path, OLD.expected_size_bytes,
        OLD.collision, OLD.release_placement_id, OLD.created_at,
        OLD.match_anime_id, OLD.match_season, OLD.match_episode, OLD.match_episode_titles) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_FROZEN';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_VERSION';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION placements_release_terminal() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'released' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_RELEASE_REQUIRES_ACTIVE';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state = 'released' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_RELEASED_TERMINAL';
    END IF;
    IF NEW.state = 'released' THEN
        IF OLD.state <> 'active' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_RELEASE_REQUIRES_ACTIVE';
        END IF;
        IF NEW.physical_path IS DISTINCT FROM OLD.physical_path THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'PLACEMENT_RELEASE_PATH_IMMUTABLE';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION validate_replacement_candidate_placement() RETURNS trigger
    LANGUAGE plpgsql
    AS $_$
BEGIN
    IF NEW.candidate_placement_id IS NOT NULL THEN
        IF NOT EXISTS (
            SELECT 1 FROM placements
            WHERE id = NEW.candidate_placement_id
              AND managed_anime_id = NEW.managed_anime_id
        ) THEN
            RAISE EXCEPTION 'candidate placement must exist for the managed anime: %', NEW.candidate_placement_id;
        END IF;
        -- Once bound, every synthetic release winner must have been replaced by
        -- a real placement. Non-synthetic winner references retain their schema semantics.
        IF EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.pools) AS pool
            WHERE pool->'winner'->>'placement_id' ~ '^release:'
        ) THEN
            RAISE EXCEPTION 'bound pre-acquisition decision has synthetic residual winner';
        END IF;
    ELSIF NEW.state = 'admitted' THEN
        IF NEW.candidate_release_id IS NULL OR NEW.candidate_release_id !~ '^[^:[:space:]]+$' THEN
            RAISE EXCEPTION 'pre-acquisition candidate release must be a non-empty opaque identifier';
        END IF;
        IF NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.pools) AS pool
            WHERE pool->'winner'->>'placement_id' = 'release:' || NEW.candidate_release_id
        ) THEN
            RAISE EXCEPTION 'pre-acquisition admission must carry its candidate release synthetic winner';
        END IF;
        IF EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.pools) AS pool
            WHERE pool->'winner'->>'placement_id' ~ '^release:'
              AND pool->'winner'->>'placement_id' <> 'release:' || NEW.candidate_release_id
        ) THEN
            RAISE EXCEPTION 'pre-acquisition admission contains a synthetic winner for another candidate release';
        END IF;
        -- Validate every synthetic reference; non-synthetic winners are allowed
        -- according to the existing pools schema semantics.
        IF EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.pools) AS pool
            WHERE pool->'winner'->>'placement_id' ~ '^release:'
              AND pool->'winner'->>'placement_id' !~ '^release:[^:[:space:]]+$'
        ) THEN
            RAISE EXCEPTION 'synthetic pre-acquisition placement reference has invalid format';
        END IF;
    END IF;
    RETURN NEW;
END $_$;

CREATE FUNCTION work_item_delete_file_maintenance(text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items AS wi
WHERE (
    wi.owner_type = 'file_maintenance'
    AND wi.owner_id = $1
) OR (
    wi.owner_type = 'alignment_operation'
    AND wi.kind = 'alignment_execute'
    AND EXISTS (
      SELECT 1
      FROM public.alignment_operations ao
      WHERE ao.id = wi.owner_id
        AND ao.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'cleanup_operation'
    AND wi.kind IN ('cleanup_quarantine', 'cleanup_purge')
    AND EXISTS (
      SELECT 1
      FROM public.cleanup_operations co
      WHERE co.id = wi.owner_id
        AND co.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'offset_migration_operation'
    AND wi.kind = 'offset_migration_execute'
    AND EXISTS (
      SELECT 1
      FROM public.offset_migration_plans omp
      WHERE omp.id = wi.owner_id
        AND omp.managed_anime_id = $1
    )
);
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes AS wi
WHERE (
    wi.owner_type = 'file_maintenance'
    AND wi.owner_id = $1
) OR (
    wi.owner_type = 'alignment_operation'
    AND wi.kind = 'alignment_execute'
    AND EXISTS (
      SELECT 1
      FROM public.alignment_operations ao
      WHERE ao.id = wi.owner_id
        AND ao.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'cleanup_operation'
    AND wi.kind IN ('cleanup_quarantine', 'cleanup_purge')
    AND EXISTS (
      SELECT 1
      FROM public.cleanup_operations co
      WHERE co.id = wi.owner_id
        AND co.managed_anime_id = $1
    )
) OR (
    wi.owner_type = 'offset_migration_operation'
    AND wi.kind = 'offset_migration_execute'
    AND EXISTS (
      SELECT 1
      FROM public.offset_migration_plans omp
      WHERE omp.id = wi.owner_id
        AND omp.managed_anime_id = $1
    )
);
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

CREATE FUNCTION work_item_delete_local_maintenance_tree(text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE affected bigint; retained bigint;
BEGIN
DELETE FROM public.work_items
WHERE (owner_type = 'file_maintenance' AND owner_id = $1)
   OR (owner_type = 'alignment_operation' AND owner_id IN (
       SELECT id FROM public.alignment_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'cleanup_operation' AND owner_id IN (
       SELECT id FROM public.cleanup_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'offset_migration_operation' AND owner_id IN (
       SELECT id FROM public.offset_migration_plans WHERE managed_anime_id = $1));
GET DIAGNOSTICS affected = ROW_COUNT;
DELETE FROM public.work_item_outcomes
WHERE (owner_type = 'file_maintenance' AND owner_id = $1)
   OR (owner_type = 'alignment_operation' AND owner_id IN (
       SELECT id FROM public.alignment_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'cleanup_operation' AND owner_id IN (
       SELECT id FROM public.cleanup_operations WHERE managed_anime_id = $1))
   OR (owner_type = 'offset_migration_operation' AND owner_id IN (
       SELECT id FROM public.offset_migration_plans WHERE managed_anime_id = $1));
GET DIAGNOSTICS retained = ROW_COUNT;
affected := affected + retained;
RETURN affected;
END;
$_$;

CREATE TRIGGER placements_release_terminal
    BEFORE INSERT OR UPDATE ON placements
    FOR EACH ROW EXECUTE FUNCTION placements_release_terminal();

CREATE TRIGGER placement_obligations_retirement_fence
    BEFORE INSERT OR DELETE OR UPDATE ON placement_obligations
    FOR EACH ROW EXECUTE FUNCTION fence_placement_obligation();

CREATE TRIGGER validate_replacement_candidate_placement_trigger
    BEFORE INSERT OR UPDATE OF candidate_placement_id, managed_anime_id, candidate_release_id, pools, state ON replacement_decisions
    FOR EACH ROW EXECUTE FUNCTION validate_replacement_candidate_placement();









CREATE TRIGGER file_manager_operations_guard
    BEFORE UPDATE ON file_manager_operations
    FOR EACH ROW EXECUTE FUNCTION file_manager_operations_guard();


GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE placements, placement_coverage,
    placement_obligations, replacement_decisions, cleanup_operations, reconciliation_scans,
    reconciliation_files, alignment_operations, offset_migration_generations,
    offset_migration_plans, episode_title_refreshes TO bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE placement_companions, file_manager_operations TO bansoko_runtime;
REVOKE ALL ON FUNCTION file_manager_operations_guard, placements_release_terminal,
    work_item_delete_file_maintenance, work_item_delete_local_maintenance_tree FROM PUBLIC;
GRANT EXECUTE ON FUNCTION work_item_delete_file_maintenance,
    work_item_delete_local_maintenance_tree TO bansoko_runtime;

-- ============================================================================
-- Notification targets, admissions and deliveries
-- Notification targets (versioned configuration), admitted workflow events and
-- their per-target deliveries and attempts.
-- ============================================================================

CREATE FUNCTION is_valid_plugin_trigger_kinds(value jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE
        WHEN jsonb_typeof(value) <> 'array' OR pg_column_size(value) > 16384 THEN FALSE
        ELSE NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements(value) AS item
            WHERE jsonb_typeof(item) <> 'string'
        )
        AND NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements_text(value) AS item(value)
            WHERE item.value NOT IN (
                'download_submitted', 'file_operation_completed', 'file_operation_failed',
                'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported')
        )
        AND jsonb_array_length(value) = (
            SELECT COUNT(DISTINCT item.value)
            FROM jsonb_array_elements_text(value) AS item(value)
        )
    END;
$$;

CREATE TABLE plugin_target_configs (
    id TEXT NOT NULL,
    name TEXT NOT NULL,
    plugin_kind TEXT NOT NULL,
    adapter_kind TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT false,
    trigger_kinds JSONB NOT NULL,
    config_schema_version INTEGER NOT NULL,
    config JSONB NOT NULL,
    template_set JSONB NOT NULL,
    secret_ref TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,
    config_status TEXT NOT NULL DEFAULT 'available',
    signing_secret_set BOOLEAN NOT NULL DEFAULT false,
    CONSTRAINT plugin_target_configs_pkey PRIMARY KEY (id),
    CONSTRAINT plugin_target_configs_secret_ref_fkey FOREIGN KEY (secret_ref) REFERENCES secret_values (ref),
    CONSTRAINT plugin_target_configs_adapter_kind_check CHECK (adapter_kind IN ('telegram', 'feishu', 'webhook')),
    CONSTRAINT plugin_target_configs_adapter_kind_match CHECK (jsonb_typeof(config) = 'object' AND (config ->> 'adapter_kind') = adapter_kind),
    CONSTRAINT plugin_target_configs_check CHECK (enabled = false OR secret_ref IS NOT NULL),
    CONSTRAINT plugin_target_configs_config_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 262144),
    CONSTRAINT plugin_target_configs_config_schema_version_check CHECK (config_schema_version >= 1),
    CONSTRAINT plugin_target_configs_config_status_check CHECK (config_status IN ('available', 'unavailable', 'invalid')),
    CONSTRAINT plugin_target_configs_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_target_configs_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 256),
    CONSTRAINT plugin_target_configs_plugin_kind_check CHECK (plugin_kind = 'notification'),
    CONSTRAINT plugin_target_configs_signing_secret_check CHECK (NOT signing_secret_set OR (adapter_kind = 'webhook' AND secret_ref IS NOT NULL)),
    CONSTRAINT plugin_target_configs_template_set_check CHECK (jsonb_typeof(template_set) = 'object' AND pg_column_size(template_set) <= 524288),
    CONSTRAINT plugin_target_configs_trigger_kinds_check CHECK (is_valid_plugin_trigger_kinds(trigger_kinds)),
    CONSTRAINT plugin_target_configs_trigger_kinds_check1 CHECK (jsonb_typeof(trigger_kinds) = 'array'),
    CONSTRAINT plugin_target_configs_version_check CHECK (version >= 1)
);

ALTER TABLE idempotency_records
    ADD CONSTRAINT idempotency_records_plugin_target_config_id_fkey FOREIGN KEY (plugin_target_config_id) REFERENCES plugin_target_configs (id);

CREATE INDEX plugin_target_configs_live_idx ON plugin_target_configs (deleted_at)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX plugin_target_configs_live_name_uq ON plugin_target_configs (name)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN plugin_target_configs.trigger_kinds IS 'Typed JSON array of unique allowlisted notification triggers; empty means test-only configuration.';
COMMENT ON COLUMN plugin_target_configs.config_status IS 'Projected adapter-config status: available serves the canonical document, invalid withholds bytes that are not provably canonical.';

CREATE TABLE notification_admissions (
    id TEXT NOT NULL,
    trigger_kind TEXT NOT NULL,
    source_owner_type TEXT NOT NULL,
    source_owner_id TEXT NOT NULL,
    source_owner_version BIGINT NOT NULL,
    result_key TEXT NOT NULL,
    managed_anime_id TEXT,
    occurred_at TIMESTAMPTZ NOT NULL,
    event_catalog_version TEXT NOT NULL,
    event_template_catalog_version TEXT NOT NULL,
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending_target_resolution',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    failure_code TEXT,
    failure_reason TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    last_error_code TEXT,
    CONSTRAINT notification_admissions_pkey PRIMARY KEY (id),
    CONSTRAINT notification_admissions_trigger_kind_source_owner_type_s_key UNIQUE (trigger_kind, source_owner_type, source_owner_id, source_owner_version, result_key),
    CONSTRAINT notification_admissions_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT notification_admissions_failure_code_check CHECK (failure_code IS NULL OR char_length(btrim(failure_code)) BETWEEN 1 AND 128),
    CONSTRAINT notification_admissions_failure_reason_check CHECK (failure_reason IS NULL OR char_length(btrim(failure_reason)) BETWEEN 1 AND 4096),
    CONSTRAINT notification_admissions_last_error_code_check CHECK (last_error_code IS NULL OR char_length(btrim(last_error_code)) BETWEEN 1 AND 128),
    CONSTRAINT notification_admissions_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT notification_admissions_state_check CHECK (state IN ('pending_target_resolution', 'admission_failed', 'resolved')),
    CONSTRAINT notification_admissions_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported'))
);

CREATE INDEX notification_admissions_due_idx ON notification_admissions (next_attempt_at NULLS FIRST, occurred_at, id)
    WHERE state = 'pending_target_resolution';
CREATE INDEX notification_admissions_pending_idx ON notification_admissions (state, occurred_at)
    WHERE state = 'pending_target_resolution';

CREATE TABLE plugin_deliveries (
    id TEXT NOT NULL,
    plugin_target_config_id TEXT NOT NULL,
    plugin_target_config_version BIGINT NOT NULL,
    adapter_kind TEXT NOT NULL,
    adapter_config_snapshot JSONB NOT NULL,
    secret_ref TEXT,
    delivery_kind TEXT NOT NULL,
    trigger_kind TEXT NOT NULL,
    source_owner_type TEXT NOT NULL,
    source_owner_id TEXT NOT NULL,
    source_owner_version BIGINT NOT NULL,
    source_result_key TEXT NOT NULL,
    test_request_idempotency_key TEXT,
    test_request_hash TEXT,
    managed_anime_id TEXT,
    state TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    idempotency_key TEXT NOT NULL,
    payload_schema_version INTEGER NOT NULL,
    payload JSONB NOT NULL,
    payload_digest TEXT NOT NULL,
    template_language_version TEXT NOT NULL,
    template_catalog_version TEXT NOT NULL,
    title_template_snapshot TEXT NOT NULL,
    body_template_snapshot TEXT NOT NULL,
    rendered_title TEXT,
    rendered_body TEXT,
    rendered_message_digest TEXT,
    target_display TEXT NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT,
    last_error TEXT,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    config_status TEXT NOT NULL DEFAULT 'available',
    config_schema_version INTEGER NOT NULL DEFAULT 1,
    next_attempt_at TIMESTAMPTZ,
    CONSTRAINT plugin_deliveries_pkey PRIMARY KEY (id),
    CONSTRAINT plugin_deliveries_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT plugin_deliveries_plugin_target_config_id_trigger_kind_s_key UNIQUE (plugin_target_config_id, trigger_kind, source_owner_type, source_owner_id, source_owner_version, source_result_key),
    CONSTRAINT plugin_deliveries_plugin_target_config_id_fkey FOREIGN KEY (plugin_target_config_id) REFERENCES plugin_target_configs (id),
    CONSTRAINT plugin_deliveries_secret_ref_fkey FOREIGN KEY (secret_ref) REFERENCES secret_values (ref),
    CONSTRAINT plugin_deliveries_adapter_config_snapshot_check CHECK (jsonb_typeof(adapter_config_snapshot) = 'object' AND pg_column_size(adapter_config_snapshot) <= 262144),
    CONSTRAINT plugin_deliveries_adapter_kind_check CHECK (adapter_kind IN ('telegram', 'feishu', 'webhook')),
    CONSTRAINT plugin_deliveries_adapter_kind_match CHECK (jsonb_typeof(adapter_config_snapshot) = 'object' AND (adapter_config_snapshot ->> 'adapter_kind') = adapter_kind),
    CONSTRAINT plugin_deliveries_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT plugin_deliveries_body_template_snapshot_check CHECK (char_length(body_template_snapshot) <= 32768),
    CONSTRAINT plugin_deliveries_check CHECK ((delivery_kind = 'test') = (source_owner_type = 'plugin_target')),
    CONSTRAINT plugin_deliveries_check1 CHECK ((delivery_kind = 'test') = (test_request_idempotency_key IS NOT NULL)),
    CONSTRAINT plugin_deliveries_check2 CHECK (state <> 'delivered' OR delivered_at IS NOT NULL),
    CONSTRAINT plugin_deliveries_config_schema_version_check CHECK (config_schema_version >= 1),
    CONSTRAINT plugin_deliveries_config_status_check CHECK (config_status IN ('available', 'unavailable', 'invalid')),
    CONSTRAINT plugin_deliveries_delivery_kind_check CHECK (delivery_kind IN ('business', 'test')),
    CONSTRAINT plugin_deliveries_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_deliveries_idempotency_key_check CHECK (char_length(btrim(idempotency_key)) BETWEEN 1 AND 512),
    CONSTRAINT plugin_deliveries_last_error_check CHECK (last_error IS NULL OR char_length(last_error) <= 4096),
    CONSTRAINT plugin_deliveries_last_error_code_check CHECK (last_error_code IS NULL OR char_length(btrim(last_error_code)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_deliveries_managed_anime_id_check CHECK (managed_anime_id IS NULL OR (char_length(btrim(managed_anime_id)) BETWEEN 1 AND 128 AND managed_anime_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$')),
    CONSTRAINT plugin_deliveries_next_attempt_check CHECK (next_attempt_at IS NULL OR state = 'waiting_retry'),
    CONSTRAINT plugin_deliveries_payload_check CHECK (jsonb_typeof(payload) = 'object' AND pg_column_size(payload) <= 1048576),
    CONSTRAINT plugin_deliveries_payload_digest_check CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT plugin_deliveries_payload_schema_version_check CHECK (payload_schema_version >= 1),
    CONSTRAINT plugin_deliveries_plugin_target_config_version_check CHECK (plugin_target_config_version >= 1),
    CONSTRAINT plugin_deliveries_rendered_body_check CHECK (rendered_body IS NULL OR char_length(rendered_body) <= 65536),
    CONSTRAINT plugin_deliveries_rendered_message_digest_check CHECK (rendered_message_digest IS NULL OR rendered_message_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT plugin_deliveries_rendered_title_check CHECK (rendered_title IS NULL OR char_length(rendered_title) <= 4096),
    CONSTRAINT plugin_deliveries_source_owner_id_check CHECK (char_length(btrim(source_owner_id)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_deliveries_source_owner_type_check CHECK (source_owner_type IN ('download_task', 'file_operation', 'plugin_target', 'release', 'subscription', 'acquisition')),
    CONSTRAINT plugin_deliveries_source_owner_version_check CHECK (source_owner_version >= 1),
    CONSTRAINT plugin_deliveries_source_result_key_check CHECK (char_length(btrim(source_result_key)) BETWEEN 1 AND 512),
    CONSTRAINT plugin_deliveries_state_check CHECK (state IN ('pending', 'delivering', 'waiting_retry', 'delivered', 'failed', 'unknown')),
    CONSTRAINT plugin_deliveries_target_display_check CHECK (char_length(btrim(target_display)) BETWEEN 1 AND 512),
    CONSTRAINT plugin_deliveries_template_catalog_version_check CHECK (template_catalog_version = 'notification-catalog-v1'),
    CONSTRAINT plugin_deliveries_template_language_version_check CHECK (template_language_version = 'bansoko-template-1'),
    CONSTRAINT plugin_deliveries_test_request_hash_check CHECK (test_request_hash IS NULL OR test_request_hash ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT plugin_deliveries_title_template_snapshot_check CHECK (char_length(title_template_snapshot) <= 32768),
    CONSTRAINT plugin_deliveries_trigger_kind_check CHECK (trigger_kind IN ('download_submitted', 'file_operation_completed', 'file_operation_failed', 'release_discovered', 'download_failed', 'subscription_changed', 'unknown_files_imported')),
    CONSTRAINT plugin_deliveries_version_check CHECK (version >= 1)
);

CREATE INDEX plugin_deliveries_record_idx ON plugin_deliveries (created_at DESC, id DESC);
CREATE INDEX plugin_deliveries_updated_idx ON plugin_deliveries (updated_at DESC, id DESC);
CREATE INDEX plugin_deliveries_retention_idx ON plugin_deliveries (retention_until, id);
CREATE UNIQUE INDEX plugin_deliveries_test_key_uq ON plugin_deliveries (plugin_target_config_id, test_request_idempotency_key)
    WHERE delivery_kind = 'test';

COMMENT ON COLUMN plugin_deliveries.secret_ref IS 'Opaque immutable secret version reference; FK preserves secret ownership semantics, while retention GC is not implemented here.';
COMMENT ON COLUMN plugin_deliveries.managed_anime_id IS 'Opaque optional ManagedAnime reference; retained without an owner FK when the owner may be purged independently.';
COMMENT ON COLUMN plugin_deliveries.config_status IS 'Frozen adapter-config snapshot status; the dispatcher fails a non-available snapshot closed instead of executing it.';
COMMENT ON COLUMN plugin_deliveries.config_schema_version IS 'Adapter config schema version the snapshot was frozen under, so the snapshot stays self-describing.';

CREATE TABLE plugin_delivery_attempts (
    id TEXT NOT NULL,
    plugin_delivery_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    request_started_at TIMESTAMPTZ,
    response_received_at TIMESTAMPTZ,
    duration_ms INTEGER,
    target_display TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    request_summary JSONB,
    response_status INTEGER,
    response_preview TEXT,
    provider_ack JSONB,
    error_code TEXT,
    error TEXT,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '14 days'::interval),
    CONSTRAINT plugin_delivery_attempts_pkey PRIMARY KEY (id),
    CONSTRAINT plugin_delivery_attempts_plugin_delivery_id_attempt_numb_key UNIQUE (plugin_delivery_id, attempt_number),
    CONSTRAINT plugin_delivery_attempts_plugin_delivery_id_fkey FOREIGN KEY (plugin_delivery_id) REFERENCES plugin_deliveries (id) ON DELETE CASCADE,
    CONSTRAINT plugin_delivery_attempts_attempt_number_check CHECK (attempt_number >= 1),
    CONSTRAINT plugin_delivery_attempts_duration_ms_check CHECK (duration_ms IS NULL OR duration_ms >= 0),
    CONSTRAINT plugin_delivery_attempts_error_check CHECK (error IS NULL OR char_length(error) <= 4096),
    CONSTRAINT plugin_delivery_attempts_error_code_check CHECK (error_code IS NULL OR char_length(btrim(error_code)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_delivery_attempts_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT plugin_delivery_attempts_payload_digest_check CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT plugin_delivery_attempts_provider_ack_check CHECK (provider_ack IS NULL OR (jsonb_typeof(provider_ack) = 'object' AND pg_column_size(provider_ack) <= 65536)),
    CONSTRAINT plugin_delivery_attempts_request_summary_check CHECK (request_summary IS NULL OR (jsonb_typeof(request_summary) = 'object' AND pg_column_size(request_summary) <= 65536)),
    CONSTRAINT plugin_delivery_attempts_response_preview_check CHECK (response_preview IS NULL OR char_length(response_preview) <= 8192),
    CONSTRAINT plugin_delivery_attempts_response_status_check CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    CONSTRAINT plugin_delivery_attempts_target_display_check CHECK (char_length(btrim(target_display)) BETWEEN 1 AND 512)
);

CREATE INDEX plugin_delivery_attempts_retention_idx ON plugin_delivery_attempts (retention_until, id);

CREATE FUNCTION managed_anime_of_owner_chain(p_parent_kind text, p_parent_id text) RETURNS text
    LANGUAGE sql STABLE
    AS $$
    SELECT CASE p_parent_kind
        WHEN 'subscription' THEN (
            SELECT x.managed_anime_id FROM subscriptions x WHERE x.id = p_parent_id)
        WHEN 'rss_source' THEN (
            SELECT s.managed_anime_id FROM rss_sources x JOIN subscriptions s ON s.id = x.subscription_id
            WHERE x.id = p_parent_id)
        WHEN 'release' THEN (
            SELECT s.managed_anime_id FROM releases x JOIN rss_sources rs ON rs.id = x.rss_source_id
            JOIN subscriptions s ON s.id = rs.subscription_id WHERE x.id = p_parent_id)
        WHEN 'download_task' THEN (
            SELECT s.managed_anime_id FROM download_tasks x JOIN releases r ON r.id = x.release_id
            JOIN rss_sources rs ON rs.id = r.rss_source_id JOIN subscriptions s ON s.id = rs.subscription_id
            WHERE x.id = p_parent_id)
        WHEN 'artifact' THEN (
            SELECT s.managed_anime_id FROM artifacts x JOIN download_tasks dt ON dt.id = x.download_task_id
            JOIN releases r ON r.id = dt.release_id JOIN rss_sources rs ON rs.id = r.rss_source_id
            JOIN subscriptions s ON s.id = rs.subscription_id WHERE x.id = p_parent_id)
        WHEN 'plugin_delivery' THEN (
            SELECT x.managed_anime_id FROM plugin_deliveries x WHERE x.id = p_parent_id)
        WHEN 'acquisition_target' THEN (
            SELECT s.managed_anime_id FROM acquisition_targets x JOIN releases r ON r.id = x.release_id
            JOIN rss_sources rs ON rs.id = r.rss_source_id JOIN subscriptions s ON s.id = rs.subscription_id
            WHERE x.id = p_parent_id)
        WHEN 'media_snapshot' THEN (
            SELECT s.managed_anime_id FROM media_fact_snapshots x JOIN releases r ON r.id = x.release_id
            JOIN rss_sources rs ON rs.id = r.rss_source_id JOIN subscriptions s ON s.id = rs.subscription_id
            WHERE x.id = p_parent_id)
        WHEN 'notification_admission' THEN (
            SELECT x.managed_anime_id FROM notification_admissions x WHERE x.id = p_parent_id)
        ELSE NULL
    END
$$;





GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE plugin_target_configs,
    notification_admissions,
    plugin_deliveries, plugin_delivery_attempts TO bansoko_runtime;

-- ============================================================================
-- Automation runs, activity and diagnostics, deployment recovery, embedded update fence, backups
-- Automation dispatch runs, routine activity and diagnostic episodes, deployment
-- recovery runs, the embedded update fence and its audit, and backup runs.
-- ============================================================================

CREATE TABLE automation_dispatch_runs (
    id TEXT NOT NULL,
    kind TEXT NOT NULL,
    trigger TEXT NOT NULL,
    state TEXT NOT NULL,
    target_count INTEGER NOT NULL DEFAULT 0,
    enqueued_count INTEGER NOT NULL DEFAULT 0,
    merged_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    dispatch_error_count INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '30 days'::interval),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    request_idempotency_key TEXT,
    CONSTRAINT automation_dispatch_runs_pkey PRIMARY KEY (id),
    CONSTRAINT automation_dispatch_runs_kind_fkey FOREIGN KEY (kind) REFERENCES automation_schedules (kind),
    CONSTRAINT automation_dispatch_runs_check CHECK ((state = 'queued' AND started_at IS NULL AND finished_at IS NULL) OR (state = 'running' AND started_at IS NOT NULL AND finished_at IS NULL) OR (state IN ('completed', 'partial', 'failed') AND started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at)),
    CONSTRAINT automation_dispatch_runs_check1 CHECK (state <> 'completed' OR dispatch_error_count = 0),
    CONSTRAINT automation_dispatch_runs_check2 CHECK (state <> 'partial' OR (dispatch_error_count > 0 AND (enqueued_count + merged_count) > 0)),
    CONSTRAINT automation_dispatch_runs_check3 CHECK (retention_until <= (COALESCE(finished_at, created_at) + '30 days'::interval)),
    CONSTRAINT automation_dispatch_runs_dispatch_error_count_check CHECK (dispatch_error_count >= 0),
    CONSTRAINT automation_dispatch_runs_enqueued_count_check CHECK (enqueued_count >= 0),
    CONSTRAINT automation_dispatch_runs_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT automation_dispatch_runs_merged_count_check CHECK (merged_count >= 0),
    CONSTRAINT automation_dispatch_runs_request_idempotency_key_check CHECK (request_idempotency_key IS NULL OR char_length(btrim(request_idempotency_key)) BETWEEN 8 AND 128),
    CONSTRAINT automation_dispatch_runs_skipped_count_check CHECK (skipped_count >= 0),
    CONSTRAINT automation_dispatch_runs_state_check CHECK (state IN ('queued', 'running', 'completed', 'partial', 'failed')),
    CONSTRAINT automation_dispatch_runs_target_count_check CHECK (target_count >= 0),
    CONSTRAINT automation_dispatch_runs_trigger_check CHECK (trigger IN ('scheduled', 'manual'))
);

CREATE UNIQUE INDEX automation_dispatch_runs_live_kind_uq ON automation_dispatch_runs (kind)
    WHERE state IN ('queued', 'running');
CREATE UNIQUE INDEX automation_dispatch_runs_request_key_uq ON automation_dispatch_runs (kind, request_idempotency_key)
    WHERE request_idempotency_key IS NOT NULL;
CREATE INDEX automation_dispatch_runs_retention_idx ON automation_dispatch_runs (retention_until, id);
CREATE INDEX automation_dispatch_runs_record_idx ON automation_dispatch_runs (created_at DESC, id DESC);

CREATE TABLE routine_activities (
    id TEXT NOT NULL,
    kind TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    managed_anime_id TEXT,
    outcome TEXT NOT NULL,
    last_executed_at TIMESTAMPTZ NOT NULL,
    last_result_code TEXT NOT NULL,
    summary TEXT NOT NULL,
    details JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT routine_activities_pkey PRIMARY KEY (id),
    CONSTRAINT routine_activities_kind_scope_kind_scope_id_key UNIQUE (kind, scope_kind, scope_id),
    CONSTRAINT routine_activities_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT routine_activities_check CHECK ((scope_kind = 'global' AND scope_id = 'global') OR (scope_kind <> 'global' AND scope_id <> 'global')),
    CONSTRAINT routine_activities_details_check CHECK (details IS NULL OR (jsonb_typeof(details) = 'object' AND pg_column_size(details) <= 262144)),
    CONSTRAINT routine_activities_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT routine_activities_kind_check CHECK (kind IN ('catalog_refresh', 'provider_refresh', 'rss_refresh', 'metadata_match', 'automation_dispatch', 'retention_pass', 'file_manager_operation')),
    CONSTRAINT routine_activities_last_result_code_check CHECK (char_length(btrim(last_result_code)) BETWEEN 1 AND 128),
    CONSTRAINT routine_activities_outcome_check CHECK (outcome IN ('success', 'warning', 'failure')),
    CONSTRAINT routine_activities_scope_id_check CHECK (char_length(btrim(scope_id)) BETWEEN 1 AND 128),
    CONSTRAINT routine_activities_scope_kind_check CHECK (scope_kind IN ('global', 'automation', 'provider', 'subscription', 'rss_source', 'component')),
    CONSTRAINT routine_activities_summary_check CHECK (char_length(btrim(summary)) BETWEEN 1 AND 4096)
);

CREATE INDEX routine_activities_record_idx ON routine_activities (last_executed_at DESC, id DESC);

COMMENT ON TABLE routine_activities IS 'Typed last-known routine activity; retention does not apply to this fixed-scope row.';
COMMENT ON COLUMN routine_activities.scope_id IS 'Non-null stable scope ID; global scope must use the exact sentinel global.';

CREATE TABLE diagnostic_episodes (
    id TEXT NOT NULL,
    severity TEXT NOT NULL,
    component TEXT NOT NULL,
    operation TEXT NOT NULL,
    error_code TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    managed_anime_id TEXT,
    first_failed_at TIMESTAMPTZ NOT NULL,
    last_failed_at TIMESTAMPTZ NOT NULL,
    occurrence_count INTEGER NOT NULL,
    last_error TEXT NOT NULL,
    details JSONB,
    succeeded_after_at TIMESTAMPTZ,
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + '14 days'::interval),
    CONSTRAINT diagnostic_episodes_pkey PRIMARY KEY (id),
    CONSTRAINT diagnostic_episodes_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id),
    CONSTRAINT diagnostic_episodes_check CHECK ((scope_kind = 'global' AND scope_id = 'global') OR (scope_kind <> 'global' AND scope_id <> 'global')),
    CONSTRAINT diagnostic_episodes_check1 CHECK (last_failed_at >= first_failed_at),
    CONSTRAINT diagnostic_episodes_check2 CHECK (succeeded_after_at IS NULL OR succeeded_after_at >= last_failed_at),
    CONSTRAINT diagnostic_episodes_check3 CHECK (retention_until <= (COALESCE(succeeded_after_at, last_failed_at) + '30 days'::interval)),
    CONSTRAINT diagnostic_episodes_component_check CHECK (component ~ '^[a-z][a-z0-9_]{0,127}$'),
    CONSTRAINT diagnostic_episodes_details_check CHECK (details IS NULL OR (jsonb_typeof(details) = 'object' AND pg_column_size(details) <= 262144)),
    CONSTRAINT diagnostic_episodes_error_code_check CHECK (error_code ~ '^[a-z][a-z0-9_]{0,127}$'),
    CONSTRAINT diagnostic_episodes_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT diagnostic_episodes_last_error_check CHECK (char_length(btrim(last_error)) BETWEEN 1 AND 8192),
    CONSTRAINT diagnostic_episodes_occurrence_count_check CHECK (occurrence_count BETWEEN 1 AND 2147483647),
    CONSTRAINT diagnostic_episodes_operation_check CHECK (operation ~ '^[a-z][a-z0-9_]{0,127}$'),
    CONSTRAINT diagnostic_episodes_scope_id_check CHECK (char_length(btrim(scope_id)) BETWEEN 1 AND 128),
    CONSTRAINT diagnostic_episodes_scope_kind_check CHECK (scope_kind IN ('global', 'provider', 'subscription', 'rss_source', 'owner', 'worker')),
    CONSTRAINT diagnostic_episodes_severity_check CHECK (severity IN ('warning', 'error'))
);

CREATE UNIQUE INDEX diagnostic_episodes_open_identity_uq ON diagnostic_episodes (component, operation, error_code, scope_kind, scope_id)
    WHERE succeeded_after_at IS NULL;
CREATE INDEX diagnostic_episodes_record_idx ON diagnostic_episodes (last_failed_at DESC, id DESC);
CREATE INDEX diagnostic_episodes_retention_idx ON diagnostic_episodes (retention_until, id);

COMMENT ON TABLE diagnostic_episodes IS 'Bounded redacted system fault episodes; purge must never cascade to business owners.';

CREATE TABLE deployment_recovery_runs (
    id TEXT NOT NULL,
    window_id TEXT NOT NULL,
    old_bundle_identity TEXT NOT NULL,
    new_bundle_identity TEXT NOT NULL,
    backup_identity TEXT NOT NULL,
    schema_identity TEXT NOT NULL,
    build_identity TEXT NOT NULL,
    registry_inventory JSONB NOT NULL,
    checkpoint JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'running',
    version BIGINT NOT NULL DEFAULT 1,
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT deployment_recovery_runs_pkey PRIMARY KEY (id),
    CONSTRAINT deployment_recovery_runs_checkpoint_check CHECK (jsonb_typeof(checkpoint) = 'object'),
    CONSTRAINT deployment_recovery_runs_registry_inventory_check CHECK (jsonb_typeof(registry_inventory) = 'array'),
    CONSTRAINT deployment_recovery_runs_state_check CHECK (state IN ('running', 'completed', 'failed'))
);

CREATE INDEX deployment_recovery_runs_updated_idx ON deployment_recovery_runs (updated_at DESC);

CREATE TABLE embedded_update_locks (
    op_id TEXT NOT NULL,
    admitted_by TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    manifest_digest TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ,
    recovered_at TIMESTAMPTZ,
    recovery_terminal_state TEXT,
    recovery_evidence TEXT,
    CONSTRAINT embedded_update_locks_pkey PRIMARY KEY (op_id),
    CONSTRAINT embedded_update_locks_admitted_by_check CHECK (admitted_by = 'embedded'),
    CONSTRAINT embedded_update_locks_sequence_check CHECK (sequence >= 1),
    CONSTRAINT embedded_update_locks_state_check CHECK (state IN ('active', 'failed', 'settled')),
    CONSTRAINT embedded_update_locks_recovery_shape_check CHECK ((recovered_at IS NULL) = (recovery_terminal_state IS NULL) AND (recovered_at IS NULL) = (recovery_evidence IS NULL)),
    CONSTRAINT embedded_update_locks_recovery_terminal_state_check CHECK (recovery_terminal_state IS NULL OR recovery_terminal_state IN ('succeeded', 'failed', 'rolled_back')),
    CONSTRAINT embedded_update_locks_recovery_evidence_check CHECK (recovery_evidence IS NULL OR length(btrim(recovery_evidence)) BETWEEN 1 AND 4096),
    CONSTRAINT embedded_update_locks_recovery_state_check CHECK (recovery_terminal_state IS NULL OR (recovery_terminal_state = 'succeeded') = (state = 'settled'))
);

CREATE UNIQUE INDEX embedded_update_locks_single_open ON embedded_update_locks ((true))
    WHERE released_at IS NULL;

CREATE TABLE backup_runs (
    id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    state TEXT NOT NULL,
    request_idempotency_key TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    lease_expires_at TIMESTAMPTZ,
    file_name TEXT,
    size_bytes BIGINT,
    schema_version TEXT,
    error_class TEXT,
    CONSTRAINT backup_runs_pkey PRIMARY KEY (id),
    CONSTRAINT backup_runs_error_class_check CHECK (error_class IS NULL OR error_class IN ('directory_unavailable', 'tools_unavailable', 'dump_failed', 'verify_failed', 'finalize_failed', 'interrupted')),
    CONSTRAINT backup_runs_file_name_check CHECK (file_name IS NULL OR file_name ~ '^bansoko-[0-9]{8}T[0-9]{6}Z-schema[0-9]{3,6}\.dump$'),
    CONSTRAINT backup_runs_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT backup_runs_manual_key CHECK ((trigger = 'manual') = (request_idempotency_key IS NOT NULL)),
    CONSTRAINT backup_runs_request_idempotency_key_check CHECK (request_idempotency_key IS NULL OR char_length(request_idempotency_key) BETWEEN 8 AND 128),
    CONSTRAINT backup_runs_schema_version_check CHECK (schema_version IS NULL OR schema_version ~ '^[0-9]{3,6}$'),
    CONSTRAINT backup_runs_size_bytes_check CHECK (size_bytes IS NULL OR size_bytes > 0),
    CONSTRAINT backup_runs_state_check CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT backup_runs_state_shape CHECK ((state = 'queued' AND trigger = 'manual' AND started_at IS NULL AND finished_at IS NULL AND lease_expires_at IS NULL AND file_name IS NULL AND size_bytes IS NULL AND schema_version IS NULL AND error_class IS NULL) OR (state = 'running' AND started_at IS NOT NULL AND finished_at IS NULL AND lease_expires_at IS NOT NULL AND file_name IS NOT NULL AND schema_version IS NOT NULL AND size_bytes IS NULL AND error_class IS NULL) OR (state = 'succeeded' AND started_at IS NOT NULL AND finished_at IS NOT NULL AND lease_expires_at IS NULL AND file_name IS NOT NULL AND size_bytes IS NOT NULL AND schema_version IS NOT NULL AND error_class IS NULL) OR (state = 'failed' AND finished_at IS NOT NULL AND lease_expires_at IS NULL AND size_bytes IS NULL AND error_class IS NOT NULL)),
    CONSTRAINT backup_runs_trigger_check CHECK (trigger IN ('scheduled', 'manual'))
);

CREATE INDEX backup_runs_finished ON backup_runs (state, finished_at DESC);
CREATE UNIQUE INDEX backup_runs_one_live ON backup_runs ((true))
    WHERE state IN ('queued', 'running');
CREATE UNIQUE INDEX backup_runs_request_key ON backup_runs (request_idempotency_key)
    WHERE request_idempotency_key IS NOT NULL;

GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE automation_dispatch_runs, routine_activities,
    diagnostic_episodes, deployment_recovery_runs, embedded_update_locks,
    backup_runs TO bansoko_runtime;

-- ============================================================================
-- Default privileges
-- Objects a later migration creates inherit the runtime grants. A future table
-- that must stay owner-only revokes them explicitly.
-- ============================================================================

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO bansoko_runtime;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO bansoko_runtime;
