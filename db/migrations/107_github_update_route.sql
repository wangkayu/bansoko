-- Forward-only migration 107: the update channel can route its GitHub
-- requests through one dedicated forward proxy.
--
-- `github` is a route kind of its own rather than a generic http_proxy the
-- updater would have to discover: the updater only ever selects an enabled
-- `github` route, ordinary connections may not bind it, and at most one row of
-- that kind may exist. The transport is an HTTP forward proxy, so the endpoint
-- rule matches http_proxy.

ALTER TABLE network_routes DROP CONSTRAINT network_routes_kind_check;
ALTER TABLE network_routes ADD CONSTRAINT network_routes_kind_check
    CHECK (kind IN ('direct', 'http_proxy', 'socks5', 'github'));

ALTER TABLE network_routes DROP CONSTRAINT network_routes_check;
ALTER TABLE network_routes ADD CONSTRAINT network_routes_check
    CHECK ((kind = 'direct' AND endpoint IS NULL AND secret_ref IS NULL)
        OR (kind IN ('http_proxy', 'socks5', 'github') AND endpoint IS NOT NULL));

CREATE UNIQUE INDEX network_routes_github_singleton ON network_routes (kind) WHERE kind = 'github';
