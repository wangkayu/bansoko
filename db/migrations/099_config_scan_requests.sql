-- Forward-only migration 099: a configuration write requests the scan that
-- re-renders the subscription's files (owner ruling 2026-10-29: files render
-- with the current configuration).
--
-- config_scan_requests holds at most one pending request per anime. The write
-- that can change a rendered path (metadata patch, per-anime naming rules,
-- scheduler metadata backfill) upserts it in its own transaction, so the
-- request survives a crash after commit and several writes coalesce. revision
-- advances with every write: the scheduler deletes the row only at the
-- revision it read before submitting, so a write made while that scan runs
-- stays pending and gets its own scan.

CREATE TABLE config_scan_requests (
    managed_anime_id TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT config_scan_requests_pkey PRIMARY KEY (managed_anime_id),
    CONSTRAINT config_scan_requests_managed_anime_id_fkey FOREIGN KEY (managed_anime_id) REFERENCES managed_anime (id) ON DELETE CASCADE,
    CONSTRAINT config_scan_requests_revision_check CHECK (revision >= 1)
);
COMMENT ON TABLE config_scan_requests IS 'One pending reconciliation scan per anime, requested by a configuration write that can change rendered paths; the scan scheduler dispatches and clears it.';

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE config_scan_requests TO bansoko_runtime;
