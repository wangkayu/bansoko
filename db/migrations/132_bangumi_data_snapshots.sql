-- One reusable bangumi-data snapshot backs all local metadata lookups.
-- The payload is replaceable reference data, not per-subscription owner state.

CREATE TABLE bangumi_data_snapshots (
    singleton_id TEXT NOT NULL DEFAULT 'default',
    upstream_version TEXT NOT NULL,
    payload BYTEA NOT NULL,
    last_modified TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT bangumi_data_snapshots_pkey PRIMARY KEY (singleton_id),
    CONSTRAINT bangumi_data_snapshots_singleton CHECK (singleton_id = 'default'),
    CONSTRAINT bangumi_data_snapshots_version_check CHECK (upstream_version ~ '^0\.3\.[0-9]+$'),
    CONSTRAINT bangumi_data_snapshots_payload_size CHECK (octet_length(payload) BETWEEN 2 AND 33554432),
    CONSTRAINT bangumi_data_snapshots_last_modified CHECK (octet_length(last_modified) <= 256),
    CONSTRAINT bangumi_data_snapshots_times CHECK (checked_at >= fetched_at)
);
COMMENT ON TABLE bangumi_data_snapshots IS 'Latest usable bangumi-data CDN payload; stale snapshots remain usable when refresh fails.';

REVOKE ALL ON TABLE bangumi_data_snapshots FROM bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE bangumi_data_snapshots TO bansoko_runtime;
