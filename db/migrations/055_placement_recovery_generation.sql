-- Forward-only migration 055: the placement recovery generation marker.
--
-- A scan-alignment recovery confirm re-attaches a placement and its companion
-- unit to the paths the current naming rules render, WITHOUT moving a byte.
-- It changes the physical location and the target fingerprint only: the
-- placement's SOURCE evidence is untouched, so `source_evidence_type` stays
-- `file_operation` and every lineage reader keeps recognizing the row
-- (render facts, the scan's operation size join, replacement candidates,
-- replacement decisions, companion binding, placement obligation retry).
--
-- Rewriting `source_evidence_type` to 'recovery' instead would leave a zombie
-- owner: `state = 'active'` but invisible to all six of those readers, which
-- all join on `source_evidence_type = 'file_operation'`. That is why the
-- confirm needs its own marker column rather than reusing the source-evidence
-- one.
--
-- `snapshot_generation` was considered and rejected: admission already stores
-- the scan snapshot generation there (`PlacementService.admitTx`), so reusing
-- it for recovery identity would conflate two independent facts.
--
-- `recovery_generation` records the calibrated scan generation whose recovery
-- candidate this row accepted. It is the replay identity: a second confirm of
-- the same generation answers `replay = true` without bumping a version or
-- writing a second journal entry. NULL means the placement was never
-- recovered, which is the normal case for every admitted row.
--
-- Migrations carry no data-preservation obligation before release.

ALTER TABLE placements
    ADD COLUMN recovery_generation BIGINT,
    ADD CONSTRAINT placements_recovery_generation_check
        CHECK (recovery_generation IS NULL OR recovery_generation >= 1);
