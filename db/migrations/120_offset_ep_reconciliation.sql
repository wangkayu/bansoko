-- Offset changes retain both sides of every episode re-key as durable evidence
-- of the activity and of the subscription batch that drove candidate
-- re-evaluation. The batch marker is separate from the activity state: a
-- change with no imported placements is recorded immediately but still waits
-- for the same subscription-level dispatch barrier.

ALTER TABLE offset_alignment_changes
    ADD COLUMN candidates_scheduled_at TIMESTAMPTZ;

CREATE TABLE offset_alignment_change_keys (
    change_id TEXT NOT NULL,
    placement_id TEXT NOT NULL,
    old_observation_key TEXT NOT NULL,
    new_observation_key TEXT NOT NULL,
    CONSTRAINT offset_alignment_change_keys_pkey PRIMARY KEY (change_id, placement_id),
    CONSTRAINT offset_alignment_change_keys_change_fkey FOREIGN KEY (change_id) REFERENCES offset_alignment_changes (id) ON DELETE CASCADE
);
CREATE INDEX offset_alignment_change_keys_new_key_idx ON offset_alignment_change_keys (new_observation_key);
CREATE INDEX offset_alignment_change_keys_old_key_idx ON offset_alignment_change_keys (old_observation_key);
CREATE INDEX offset_alignment_unscheduled_batch_idx ON offset_alignment_changes (rss_source_id, created_at, id)
    WHERE candidates_scheduled_at IS NULL;

COMMENT ON TABLE offset_alignment_change_keys IS 'The old and new episode identity of each placement actually re-keyed by an offset change; retained independently of replaceable alignment requests and later placement cleanup.';
COMMENT ON COLUMN offset_alignment_change_keys.placement_id IS 'Historical placement identity only: retention may delete the placement before candidate dispatch, but must not erase the affected episode keys.';
COMMENT ON COLUMN offset_alignment_changes.candidates_scheduled_at IS 'When the complete active subscription batch dispatched its unified Release re-evaluation; NULL keeps the change behind the batch barrier.';

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE offset_alignment_change_keys TO bansoko_runtime;
GRANT UPDATE (candidates_scheduled_at) ON TABLE offset_alignment_changes TO bansoko_runtime;
