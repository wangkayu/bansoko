-- Forward-only migration 009: the file manager has no root layer.
--
-- The file manager is a container file browser (B13 rebuild, 10-17): the
-- deployment's own OS permissions are the boundary, the application's data
-- directories are the only refusal set, and every path it reads, freezes or
-- executes is an absolute container path. A configured root is therefore no
-- longer part of an operation's identity — keeping a root_key column would
-- leave a second, disagreeing answer to "where is this path" next to the
-- frozen absolute path itself.
--
-- Rows are not migrated: an admitted operation already carries absolute
-- source/target paths, which is everything the execute path reads. Every
-- environment is pre-release, and a running operation is settled by its own
-- frozen attempt receipt, so nothing depends on the dropped columns.
--
-- The frozen-row guard is replaced in the same migration: it compared the
-- root columns, which no longer exist.
--
-- This is a forward-only change rather than an edit to 001_baseline.sql. The
-- baseline is immutable: an installed database verifies the checksum of every
-- migration it has applied before it applies anything else, so editing the
-- baseline would stop an existing install from starting instead of removing
-- the columns. cmd/bansoko-migrate applies this file after the baseline and
-- every later forward migration, and its checksum is recorded in
-- schema_migrations.

ALTER TABLE file_manager_operations
    DROP CONSTRAINT file_manager_operations_target_check,
    DROP CONSTRAINT file_manager_operations_root_key_check,
    DROP CONSTRAINT file_manager_operations_root_path_check,
    DROP CONSTRAINT file_manager_operations_target_root_key_check,
    DROP CONSTRAINT file_manager_operations_target_root_path_check,
    DROP COLUMN root_key,
    DROP COLUMN root_path,
    DROP COLUMN target_root_key,
    DROP COLUMN target_root_path,
    ADD CONSTRAINT file_manager_operations_target_check CHECK (
        (operation = 'delete') = (target_path IS NULL)
        AND (operation IN ('delete', 'copy') OR target_path <> source_path)
    );

CREATE OR REPLACE FUNCTION file_manager_operations_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF OLD.state IN ('completed', 'blocked', 'failed') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'FILE_MANAGER_OPERATION_TERMINAL';
    END IF;
    IF (NEW.id, NEW.idempotency_key, NEW.request_digest, NEW.actor, NEW.operation,
        NEW.source_path, NEW.target_path, NEW.expected_size_bytes,
        NEW.collision, NEW.release_placement_id, NEW.created_at,
        NEW.match_anime_id, NEW.match_season, NEW.match_episode, NEW.match_episode_titles)
       IS DISTINCT FROM
       (OLD.id, OLD.idempotency_key, OLD.request_digest, OLD.actor, OLD.operation,
        OLD.source_path, OLD.target_path, OLD.expected_size_bytes,
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
