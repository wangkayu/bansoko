-- Forward-only migration 059: Nyaa is a built-in torrent-search source.
--
-- Nyaa is a connection kind like anibt and mikan (an address, an optional
-- network route and an enabled flag, with empty typed config), and a keyword
-- search over it is stored as a `search` feed snapshot. It has no work catalog,
-- so it is deliberately NOT added to any discovery, subscription or catalog
-- constraint: a provider feed snapshot, a group, a subscription source and a
-- catalog entry still cannot be Nyaa. Only the search kind may carry it.

ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('qbittorrent', 'rss_http', 'anibt', 'mikan', 'nyaa', 'tmdb', 'bgm'));

ALTER TABLE connections DROP CONSTRAINT connections_config_shape_check;
ALTER TABLE connections ADD CONSTRAINT connections_config_shape_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384 AND ((kind = 'qbittorrent' AND config ?& '{username,category,save_root,downloader_root,user_tags}'::text[] AND (config - '{username,category,save_root,downloader_root,user_tags}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'username')) = 'string' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256) AND jsonb_typeof((config -> 'category')) = 'string' AND char_length((config ->> 'category')) <= 256 AND jsonb_typeof((config -> 'save_root')) = 'string' AND char_length((config ->> 'save_root')) <= 4096 AND jsonb_typeof((config -> 'downloader_root')) = 'string' AND char_length((config ->> 'downloader_root')) <= 4096 AND is_valid_qbittorrent_user_tags((config -> 'user_tags'))) OR (kind = 'rss_http' AND config ?& '{auth_mode,username}'::text[] AND (config - '{auth_mode,username}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('none', 'basic', 'bearer', 'cookie') AND jsonb_typeof((config -> 'username')) = 'string' AND char_length((config ->> 'username')) <= 256 AND (((config ->> 'auth_mode') = 'basic' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') IN ('none', 'bearer', 'cookie') AND (config ->> 'username') = '')) AND ((config ->> 'auth_mode') <> 'none') = (secret_ref IS NOT NULL)) OR (kind IN ('anibt', 'mikan', 'nyaa', 'tmdb', 'bgm') AND config = '{}'::jsonb)));

ALTER TABLE provider_feed_snapshots DROP CONSTRAINT provider_feed_snapshots_provider_check;
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_provider_check
    CHECK (provider IN ('anibt', 'mikan', 'nyaa', 'custom'));

-- The kind/provider pairing already allows any non-custom provider for the
-- provider_feed kind, so Nyaa is pinned to the search kind here.
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_nyaa_search_only_check
    CHECK (provider <> 'nyaa' OR snapshot_kind = 'search');

ALTER TABLE provider_feed_snapshots DROP CONSTRAINT provider_feed_snapshots_search_scope_check;
ALTER TABLE provider_feed_snapshots ADD CONSTRAINT provider_feed_snapshots_search_scope_check CHECK (
    snapshot_kind <> 'search'
    OR (provider IN ('anibt', 'mikan', 'nyaa')
        AND provider_entry_ref ~ '^search-scope:[0-9a-f]{64}$'
        AND provider_group_ref ~ '^search-query:[0-9a-f]{64}$')
);
