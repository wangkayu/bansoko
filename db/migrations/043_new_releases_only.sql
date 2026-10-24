-- Forward-only migration 043: new releases only.
--
-- A per-subscription option, off by default. When it is on, a Release whose
-- publish time is before the instance's creation time, or which has no publish
-- time, takes the terminal verdict 'start_baseline' instead of being evaluated
-- against the rules. The baseline is the moment the subscribe command created
-- the (pending) instance, not the later first start: releases published while
-- the instance was pending are new.
--
-- 1. subscriptions.new_releases_only is the option; start_baseline_at is the
--    frozen baseline, present exactly when the option is on and always equal
--    to the row's created_at (so turning the option on while pending still
--    uses the creation time). The option may change only while the
--    subscription is pending; once started it is immutable (delete the
--    instance and subscribe again to change it). The guard trigger enforces
--    that below the API, which refuses the change with
--    NEW_RELEASES_ONLY_LOCKED. It is per-instance state: nothing reads Global.
--
-- 2. releases.verdict gains 'start_baseline'. It is fully separate from
--    'resume_baseline' (019): an explicit per-release retry evaluates a
--    start_baseline Release normally through every rule; only the baseline
--    gate is skipped for that explicit action. Automatic paths (refresh,
--    re-evaluation after a rule change) treat it as terminal.
--
-- 3. The candidate draft shape admits the optional boolean top-level key
--    new_releases_only (the draft carries the option until subscribe).
--
-- Migrations carry no data-preservation obligation before release; existing
-- subscriptions keep the option off.

ALTER TABLE subscriptions
    ADD COLUMN new_releases_only BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN start_baseline_at TIMESTAMPTZ,
    ADD CONSTRAINT subscriptions_start_baseline_check CHECK (
        new_releases_only = (start_baseline_at IS NOT NULL)
        AND (start_baseline_at IS NULL OR start_baseline_at = created_at)
    );
COMMENT ON COLUMN subscriptions.new_releases_only IS 'Only take releases published after the instance was created; changeable only while pending.';
COMMENT ON COLUMN subscriptions.start_baseline_at IS 'Frozen baseline of new_releases_only: the instance creation time, present exactly when the option is on.';

CREATE FUNCTION subscriptions_new_releases_only_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF (NEW.new_releases_only IS DISTINCT FROM OLD.new_releases_only
        OR NEW.start_baseline_at IS DISTINCT FROM OLD.start_baseline_at)
       AND OLD.state <> 'pending' THEN
        RAISE EXCEPTION 'new_releases_only is immutable once the subscription has started'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'subscriptions_new_releases_only_immutable';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at AND NEW.start_baseline_at IS NOT NULL THEN
        RAISE EXCEPTION 'the creation time of a subscription with a start baseline is frozen'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'subscriptions_new_releases_only_immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER subscriptions_new_releases_only_guard_trg
    BEFORE UPDATE OF new_releases_only, start_baseline_at, created_at ON subscriptions
    FOR EACH ROW EXECUTE FUNCTION subscriptions_new_releases_only_guard();

ALTER TABLE releases DROP CONSTRAINT releases_verdict_check;
ALTER TABLE releases ADD CONSTRAINT releases_verdict_check CHECK (verdict IN ('discovered', 'waiting_metadata', 'subscription_stopped', 'source_unavailable', 'parse_failed', 'accepted_pending_download', 'filtered', 'adopted_existing', 'acquisition_failed', 'submitted', 'resume_baseline', 'start_baseline') AND ((download_task_id IS NULL AND verdict <> 'submitted') OR (download_task_id IS NOT NULL AND verdict IN ('accepted_pending_download', 'submitted', 'filtered', 'waiting_metadata', 'parse_failed'))));

CREATE OR REPLACE FUNCTION anime_config_candidate_identity_shape_ok(draft jsonb, origin text, origin_entry_ref text, instance_label text) RETURNS boolean
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
        'rss_sources', 'rules', 'subgroups', 'title', 'title_override',
        'new_releases_only'
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
