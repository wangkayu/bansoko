-- Forward-only migration 160: a file manager match may name a consecutive
-- episode range for one video.
--
-- A matched file previously covered exactly one metadata episode
-- (match_episode). Combined-episode releases (one video holding episodes
-- 1-2, 3-4, ...) need the frozen operation to remember the range end as well,
-- so the rendered target and the operation view keep naming every covered
-- episode after restarts and replays. match_episode_end is NULL for the
-- single-episode form, so existing operations decode unchanged.

ALTER TABLE file_manager_operations ADD COLUMN match_episode_end INTEGER;

ALTER TABLE file_manager_operations
    ADD CONSTRAINT file_manager_operations_match_episode_end_check CHECK (
        match_episode_end IS NULL
        OR (operation = 'match'
            AND match_episode_end >= match_episode
            AND match_episode_end <= 99999
            AND match_episode_end - match_episode + 1 <= 12)
    );

-- The frozen-choice guard must also cover the new column: after admission a
-- match operation's range is as immutable as its season and episode.
CREATE OR REPLACE FUNCTION file_manager_operations_guard() RETURNS trigger
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
        NEW.match_anime_id, NEW.match_season, NEW.match_episode, NEW.match_episode_end, NEW.match_episode_titles)
       IS DISTINCT FROM
       (OLD.id, OLD.idempotency_key, OLD.request_digest, OLD.actor, OLD.operation, OLD.root_key, OLD.root_path,
        OLD.target_root_key, OLD.target_root_path, OLD.source_path, OLD.target_path, OLD.expected_size_bytes,
        OLD.collision, OLD.release_placement_id, OLD.created_at,
        OLD.match_anime_id, OLD.match_season, OLD.match_episode, OLD.match_episode_end, OLD.match_episode_titles) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_FROZEN';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_VERSION';
    END IF;
    RETURN NEW;
END;
$$;
