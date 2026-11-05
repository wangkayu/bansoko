-- User-facing file recovery has been removed. Internal work-item, file-operation
-- and deletion recovery do not use this replay marker.

ALTER TABLE placements DROP COLUMN recovery_generation;
