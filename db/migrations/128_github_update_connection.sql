-- Forward-only migration 128: the app update channel is an ordinary GitHub
-- connection.
--
-- Migration 127 kept the update channel as a singleton update_egress row that
-- only chose a network route, which left its own reverse-proxy address without
-- a home. The channel now has a real `github` connection: its endpoint is the
-- base the canonical raw.githubusercontent.com requests are sent to (so a
-- reverse proxy is configured in exactly one place) and its network route is
-- the egress. Like the provider connections, a missing row means the official
-- endpoint over the deployment default egress.
--
-- The seeded row inherits the route 127 already selected, so an instance that
-- routed updates through a proxy keeps doing so. Secrets stay on the network
-- route; the connection itself is credential-free.

ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('qbittorrent', 'rss_http', 'anibt', 'mikan', 'nyaa', 'jackett', 'tmdb', 'bgm', 'github'));

ALTER TABLE connections DROP CONSTRAINT connections_config_shape_check;
ALTER TABLE connections ADD CONSTRAINT connections_config_shape_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384 AND ((kind = 'qbittorrent' AND config ?& '{username,category,save_root,downloader_root,user_tags}'::text[] AND (config - '{username,category,save_root,downloader_root,user_tags}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'username')) = 'string' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256) AND jsonb_typeof((config -> 'category')) = 'string' AND char_length((config ->> 'category')) <= 256 AND jsonb_typeof((config -> 'save_root')) = 'string' AND char_length((config ->> 'save_root')) <= 4096 AND jsonb_typeof((config -> 'downloader_root')) = 'string' AND char_length((config ->> 'downloader_root')) <= 4096 AND is_valid_qbittorrent_user_tags((config -> 'user_tags'))) OR (kind = 'rss_http' AND config ?& '{auth_mode,username}'::text[] AND (config - '{auth_mode,username}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('none', 'basic', 'bearer', 'cookie') AND jsonb_typeof((config -> 'username')) = 'string' AND char_length((config ->> 'username')) <= 256 AND (((config ->> 'auth_mode') = 'basic' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') IN ('none', 'bearer', 'cookie') AND (config ->> 'username') = '')) AND ((config ->> 'auth_mode') <> 'none') = (secret_ref IS NOT NULL)) OR (kind = 'jackett' AND config = '{}'::jsonb AND secret_ref IS NOT NULL) OR (kind IN ('anibt', 'mikan', 'nyaa', 'tmdb', 'bgm') AND config = '{}'::jsonb) OR (kind = 'github' AND config = '{}'::jsonb AND secret_ref IS NULL)));

INSERT INTO connections (id, kind, name, endpoint, network_route_id, state)
VALUES ('builtin-github', 'github', 'GitHub', 'https://raw.githubusercontent.com',
    (SELECT network_route_id FROM update_egress WHERE id = 'default'), 'configured')
ON CONFLICT (id) DO NOTHING;

COMMENT ON COLUMN connections.endpoint IS
    'Provider/update base URL. For kind github it replaces the canonical raw.githubusercontent.com root as the prefix every update request is sent to; the canonical URL stays the identity that is validated.';

REVOKE ALL ON TABLE update_egress FROM bansoko_runtime;
DROP TABLE update_egress;
