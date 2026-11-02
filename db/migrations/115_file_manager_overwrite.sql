-- Forward-only migration 115: a confirmed file-manager overwrite.
--
-- The user may replace a plain file at the target. The collision choice gains
-- 'overwrite'; a managed or in-progress target is still refused before the
-- effect (the owner rows name it), and the execution refuses to replace it
-- again at the exchange. overwrite_recovery is the durable exchange record of
-- the atomic replace, written checkpoint by checkpoint, so an interrupted
-- exchange is finished or restored from the record instead of guessed.

ALTER TABLE file_manager_operations
    DROP CONSTRAINT file_manager_operations_collision_check,
    ADD CONSTRAINT file_manager_operations_collision_check
        CHECK (collision IN ('block', 'version', 'overwrite')),
    ADD COLUMN overwrite_recovery JSONB,
    ADD CONSTRAINT file_manager_operations_overwrite_recovery_check
        CHECK (overwrite_recovery IS NULL OR (jsonb_typeof(overwrite_recovery) = 'object' AND pg_column_size(overwrite_recovery) <= 65536));
