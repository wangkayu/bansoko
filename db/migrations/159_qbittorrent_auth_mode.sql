-- Forward-only migration 159: a qBittorrent connection declares how it
-- authenticates to the downloader.
--
-- qBittorrent v5.2.0 (WebAPI v2.14.1) added stateless API key authentication:
-- requests carry `Authorization: Bearer <key>` and the session login round-trip
-- is skipped. The connection config gains an `auth_mode` key: `credentials`
-- keeps the username/password login (the username stays required), `api_key`
-- sends the stored secret as the bearer token (the username must be empty).
-- Either way a secret is mandatory, so the check now also refuses a
-- qBittorrent row without one.
--
-- Existing rows predate the key; they authenticate with username and password,
-- so they are backfilled as `credentials` before the tightened shape check is
-- rebuilt.

ALTER TABLE connections DROP CONSTRAINT connections_config_shape_check;

UPDATE connections
SET config = config || '{"auth_mode":"credentials"}'::jsonb
WHERE kind = 'qbittorrent' AND NOT (config ? 'auth_mode');

ALTER TABLE connections ADD CONSTRAINT connections_config_shape_check CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384 AND ((kind = 'qbittorrent' AND config ?& '{auth_mode,username,category,save_root,downloader_root,user_tags}'::text[] AND (config - '{auth_mode,username,category,save_root,downloader_root,user_tags}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('credentials', 'api_key') AND jsonb_typeof((config -> 'username')) = 'string' AND (((config ->> 'auth_mode') = 'credentials' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') = 'api_key' AND (config ->> 'username') = '')) AND jsonb_typeof((config -> 'category')) = 'string' AND char_length((config ->> 'category')) <= 256 AND jsonb_typeof((config -> 'save_root')) = 'string' AND char_length((config ->> 'save_root')) <= 4096 AND jsonb_typeof((config -> 'downloader_root')) = 'string' AND char_length((config ->> 'downloader_root')) <= 4096 AND is_valid_qbittorrent_user_tags((config -> 'user_tags')) AND secret_ref IS NOT NULL) OR (kind = 'rss_http' AND config ?& '{auth_mode,username}'::text[] AND (config - '{auth_mode,username}'::text[]) = '{}'::jsonb AND jsonb_typeof((config -> 'auth_mode')) = 'string' AND (config ->> 'auth_mode') IN ('none', 'basic', 'bearer', 'cookie') AND jsonb_typeof((config -> 'username')) = 'string' AND char_length((config ->> 'username')) <= 256 AND (((config ->> 'auth_mode') = 'basic' AND (char_length(btrim((config ->> 'username'))) >= 1 AND char_length(btrim((config ->> 'username'))) <= 256)) OR ((config ->> 'auth_mode') IN ('none', 'bearer', 'cookie') AND (config ->> 'username') = '')) AND ((config ->> 'auth_mode') <> 'none') = (secret_ref IS NOT NULL)) OR (kind = 'jackett' AND config = '{}'::jsonb AND secret_ref IS NOT NULL) OR (kind IN ('anibt', 'mikan', 'nyaa', 'tmdb', 'bgm') AND config = '{}'::jsonb) OR (kind = 'github' AND config = '{}'::jsonb AND secret_ref IS NULL)));
