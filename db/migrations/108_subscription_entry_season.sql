-- Forward-only migration 108: a subscription remembers the provider season page
-- it was created from (entry_season), and the candidate draft shape admits it.
--
-- The instance-management surface filters subscribed anime by the combination
-- of origin and the provider season catalog they were created from: mikan with
-- 2026-spring and anibt with 2026-spring are different batches, while
-- keyword-search-created and custom instances have no season. The value is
-- frozen provenance taken from the browse context at creation; it is never
-- re-derived from a later catalog snapshot (entries drift between season
-- lists) or from a first-air date. Only provider origins may carry it.
--
-- The candidate draft gains the same key, with the shape function learning it
-- the same way provider_group_offsets did in 105: an unknown top-level key is
-- rejected by anime_config_candidates_draft_decodable_check, so the write path
-- must admit it here or every draft carrying a season is refused.
--
-- No data changes: no stored row carries the value yet.

ALTER TABLE subscriptions ADD COLUMN entry_season TEXT;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_entry_season_check
    CHECK (entry_season IS NULL OR entry_season ~ '^\d{4}-(winter|spring|summer|fall)$');
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_entry_season_origin_check
    CHECK (entry_season IS NULL OR origin IN ('anibt', 'mikan'));
COMMENT ON COLUMN subscriptions.entry_season IS
    'Provider season page the instance was created from, frozen at creation (YYYY-term). Null for search-created, custom and pre-108 instances.';

CREATE OR REPLACE FUNCTION anime_config_candidate_identity_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
    draft_origin TEXT;
    draft_ref JSONB;
    draft_label JSONB;
    draft_title TEXT;
    top_key TEXT;
    offset_entry JSONB;
    allowed_keys TEXT[] := ARRAY[
        'instance_label', 'metadata', 'origin', 'origin_entry_ref',
        'rss_sources', 'rules', 'subgroups', 'title', 'title_override',
        'new_releases_only', 'provider_group_offsets', 'entry_season'
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
    IF draft ? 'new_releases_only' AND jsonb_typeof(draft -> 'new_releases_only') <> 'boolean' THEN
        RETURN FALSE;
    END IF;
    draft_origin := draft ->> 'origin';
    IF draft_origin IS NULL OR draft_origin NOT IN ('anibt', 'mikan', 'custom', 'oneshot') THEN
        RETURN FALSE;
    END IF;
    IF draft_origin <> origin THEN
        RETURN FALSE;
    END IF;
    IF draft ? 'entry_season' THEN
        IF jsonb_typeof(draft -> 'entry_season') IS DISTINCT FROM 'null' THEN
            IF draft_origin NOT IN ('anibt', 'mikan') THEN
                RETURN FALSE;
            END IF;
            IF jsonb_typeof(draft -> 'entry_season') IS DISTINCT FROM 'string'
               OR (draft ->> 'entry_season') !~ '^\d{4}-(winter|spring|summer|fall)$' THEN
                RETURN FALSE;
            END IF;
        END IF;
    END IF;
    IF draft ? 'provider_group_offsets' THEN
        IF draft_origin NOT IN ('anibt', 'mikan') THEN
            RETURN FALSE;
        END IF;
        IF jsonb_typeof(draft -> 'provider_group_offsets') IS DISTINCT FROM 'array'
           OR jsonb_array_length(draft -> 'provider_group_offsets') > 100 THEN
            RETURN FALSE;
        END IF;
        FOR offset_entry IN SELECT * FROM jsonb_array_elements(draft -> 'provider_group_offsets')
        LOOP
            IF jsonb_typeof(offset_entry) IS DISTINCT FROM 'object'
               OR NOT business_exact_keys(offset_entry, ARRAY['provider_group_ref', 'episode_offset']) THEN
                RETURN FALSE;
            END IF;
            IF jsonb_typeof(offset_entry -> 'provider_group_ref') IS DISTINCT FROM 'string'
               OR text_trim_go_ws(offset_entry #>> '{provider_group_ref}') = ''
               OR length(offset_entry ->> 'provider_group_ref') > 256
               OR offset_entry ->> 'provider_group_ref' IS DISTINCT FROM text_trim_go_ws(offset_entry #>> '{provider_group_ref}') THEN
                RETURN FALSE;
            END IF;
            IF jsonb_typeof(offset_entry -> 'episode_offset') IS DISTINCT FROM 'number'
               OR (offset_entry ->> 'episode_offset')::numeric <> floor((offset_entry ->> 'episode_offset')::numeric)
               OR (offset_entry ->> 'episode_offset')::numeric < -1000
               OR (offset_entry ->> 'episode_offset')::numeric > 1000 THEN
                RETURN FALSE;
            END IF;
        END LOOP;
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
