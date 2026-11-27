ALTER TABLE provider_catalog_snapshots
    ADD COLUMN upstream_state text CHECK (upstream_state IN ('available', 'empty', 'rate_limited', 'cooldown', 'error')),
    ADD COLUMN upstream_reason_code text,
    ADD COLUMN upstream_cooldown_until timestamptz;
