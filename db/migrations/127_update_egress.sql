-- Forward-only migration 127: the update channel selects an existing network
-- route instead of owning a route kind of its own.
--
-- A `github` route duplicated what an ordinary http_proxy route already stores:
-- endpoint, credential and enabled state. The updater now references one
-- existing route in the singleton update_egress row, so a proxy is defined and
-- edited once in the network-proxy list and the service-connections app-update
-- card only chooses it. A NULL network_route_id means the deployment default
-- egress.
--
-- An instance that already routed updates through a github route keeps doing
-- so: that row becomes an ordinary http_proxy route and the seeded update
-- egress points at it.

CREATE TABLE update_egress (
    id TEXT NOT NULL,
    network_route_id TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT update_egress_pkey PRIMARY KEY (id),
    CONSTRAINT update_egress_id_check CHECK (id = 'default'),
    CONSTRAINT update_egress_version_check CHECK (version >= 1),
    CONSTRAINT update_egress_route_fkey FOREIGN KEY (network_route_id)
        REFERENCES network_routes (id) ON DELETE SET NULL
);
COMMENT ON TABLE update_egress IS 'The one network route the update channel uses; NULL means the deployment default egress.';

INSERT INTO update_egress (id) VALUES ('default');

UPDATE update_egress
SET network_route_id = (SELECT id FROM network_routes WHERE kind = 'github' ORDER BY id LIMIT 1)
WHERE EXISTS (SELECT 1 FROM network_routes WHERE kind = 'github');

UPDATE network_routes SET kind = 'http_proxy' WHERE kind = 'github';

DROP INDEX network_routes_github_singleton;

ALTER TABLE network_routes DROP CONSTRAINT network_routes_kind_check;
ALTER TABLE network_routes ADD CONSTRAINT network_routes_kind_check
    CHECK (kind IN ('direct', 'http_proxy', 'socks5'));

ALTER TABLE network_routes DROP CONSTRAINT network_routes_check;
ALTER TABLE network_routes ADD CONSTRAINT network_routes_check
    CHECK ((kind = 'direct' AND endpoint IS NULL AND secret_ref IS NULL)
        OR (kind IN ('http_proxy', 'socks5') AND endpoint IS NOT NULL));

-- The row is seeded here and never inserted or deleted at runtime; the owner
-- only updates the selection. The table inherits the schema's default runtime
-- grants, so they are revoked first.
REVOKE ALL ON TABLE update_egress FROM bansoko_runtime;
GRANT SELECT, UPDATE ON TABLE update_egress TO bansoko_runtime;
