-- Forward-only migration 017: one-shot reclaim may delete placement companions.
--
-- A one-shot companion belongs to the temporary owner graph, so reclaiming a
-- one-shot deletes its placement_companions rows next to its placements
-- (internal/workitems/oneshot_retention.go). The baseline granted the runtime
-- only INSERT and UPDATE on this table, so that delete failed with 42501 and
-- every one-shot reclaim stopped at the companion step. The grant matches the
-- one placements already carries for the same leaf deletion.

GRANT DELETE ON TABLE placement_companions TO bansoko_runtime;
