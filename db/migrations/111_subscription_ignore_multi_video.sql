-- Forward-only migration 109: a subscription can ignore releases whose
-- torrent carries more than two video files.
--
-- The option is per-instance state (nothing reads Global), off by default and
-- changeable in any subscription state. It gates only automatic admission:
-- evaluation records the verdict 'filtered' with reason_code
-- 'multi_video_files' instead of letting the release wait for the
-- subscription competition, so an ignored release is never auto-downloaded and
-- never enters a competition round. An explicit retry (retry_release) is the
-- manual entry and skips the gate, so a keep-all instance can still download
-- the release by hand; the replacement machinery is untouched. One-shot
-- acquisition does not pass through release evaluation and never sees the
-- option.
--
-- Turning the option on settles undecided releases of the instance directly
-- (awaiting_competition without a download task whose stored manifest carries
-- more than two video files becomes filtered/multi_video_files); turning it
-- off schedules a normal re-evaluation of the releases the option filtered.
-- Both run in the option's own transaction under the subscription lock.
--
-- The candidate draft shape admits the same boolean top-level key the way
-- new_releases_only is admitted, so a draft carries the option until
-- subscribe.

ALTER TABLE subscriptions
    ADD COLUMN ignore_multi_video_releases BOOLEAN NOT NULL DEFAULT false;
COMMENT ON COLUMN subscriptions.ignore_multi_video_releases IS
    'Ignore releases whose torrent manifest carries more than two video files: automatic admission records filtered/multi_video_files; an explicit retry still evaluates them.';

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
        'new_releases_only', 'provider_group_offsets', 'entry_season',
        'ignore_multi_video_releases'
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
    IF draft ? 'ignore_multi_video_releases' AND jsonb_typeof(draft -> 'ignore_multi_video_releases') <> 'boolean' THEN
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
