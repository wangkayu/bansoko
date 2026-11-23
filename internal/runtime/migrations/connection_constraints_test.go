package migrations

import "testing"

// connections carries each kind's typed config on the connection row.
func TestConnectionConfigConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("qBittorrent typed config", `
		INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		VALUES ('conn-qb-probe', 'qbittorrent', 'QB Probe', 'http://qb.local', 'secret-qb-probe', 'configured',
			'{"auth_mode":"credentials","username":"admin","category":"anime","save_root":"/downloads","downloader_root":"","user_tags":["a","b"]}'::jsonb)`)
	p.accept("qBittorrent api_key config", `
		INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		VALUES ('conn-qb-apikey-probe', 'qbittorrent', 'QB API Key Probe', 'http://qb5.local', 'secret-qb-apikey-probe', 'configured',
			'{"auth_mode":"api_key","username":"","category":"","save_root":"/downloads","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.accept("rss_http basic config with a secret", `
		INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		VALUES ('conn-feed-probe', 'rss_http', 'Feed Probe', 'https://feed.local/rss', 'secret-feed-probe', 'configured',
			'{"auth_mode":"basic","username":"feed-user"}'::jsonb)`)
	p.accept("rss_http none config without a secret", `
		INSERT INTO connections(id, kind, name, endpoint, state, config)
		VALUES ('conn-feed-none-probe', 'rss_http', 'Feed None Probe', 'https://open.local/rss', 'configured',
			'{"auth_mode":"none","username":""}'::jsonb)`)
	p.reject("qBittorrent config without username",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-qb-no-user', 'qbittorrent', 'QB NoUser', 'http://qb2.local', 'secret-x', 'configured',
			'{"auth_mode":"credentials","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.reject("qBittorrent credentials config without a secret",
		`INSERT INTO connections(id, kind, name, endpoint, state, config)
		 VALUES ('conn-qb-no-secret', 'qbittorrent', 'QB NoSecret', 'http://qb6.local', 'configured',
			'{"auth_mode":"credentials","username":"admin","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.reject("qBittorrent api_key config without a secret",
		`INSERT INTO connections(id, kind, name, endpoint, state, config)
		 VALUES ('conn-qb-apikey-no-secret', 'qbittorrent', 'QB APIKey NoSecret', 'http://qb7.local', 'configured',
			'{"auth_mode":"api_key","username":"","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.reject("qBittorrent api_key config with a username",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-qb-apikey-user', 'qbittorrent', 'QB APIKey User', 'http://qb8.local', 'secret-x', 'configured',
			'{"auth_mode":"api_key","username":"admin","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.reject("qBittorrent config with an unknown auth mode",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-qb-bad-mode', 'qbittorrent', 'QB BadMode', 'http://qb9.local', 'secret-x', 'configured',
			'{"auth_mode":"token","username":"admin","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`)
	p.reject("qBittorrent duplicate user tags",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-qb-dup-tags', 'qbittorrent', 'QB DupTags', 'http://qb3.local', 'secret-x', 'configured',
			'{"auth_mode":"credentials","username":"admin","category":"","save_root":"","downloader_root":"","user_tags":["a","a"]}'::jsonb)`)
	p.reject("qBittorrent blank user tag",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-qb-blank-tag', 'qbittorrent', 'QB BlankTag', 'http://qb4.local', 'secret-x', 'configured',
			'{"auth_mode":"credentials","username":"admin","category":"","save_root":"","downloader_root":"","user_tags":["  "]}'::jsonb)`)
	p.reject("qBittorrent config on another kind",
		`INSERT INTO connections(id, kind, name, endpoint, state, config)
		 VALUES ('conn-tmdb-config', 'tmdb', 'TMDB Config', 'https://tmdb.local', 'configured',
			'{"username":"admin","category":"","save_root":"","user_tags":[]}'::jsonb)`)
	p.reject("rss_http basic without a secret",
		`INSERT INTO connections(id, kind, name, endpoint, state, config)
		 VALUES ('conn-feed-basic-nosecret', 'rss_http', 'Feed Basic NoSecret', 'https://f1.local/rss', 'configured',
			'{"auth_mode":"basic","username":"u"}'::jsonb)`)
	p.reject("rss_http none with a secret",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-feed-none-secret', 'rss_http', 'Feed None Secret', 'https://f2.local/rss', 'secret-y', 'configured',
			'{"auth_mode":"none","username":""}'::jsonb)`)
	p.reject("rss_http basic without username",
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
		 VALUES ('conn-feed-basic-nouser', 'rss_http', 'Feed Basic NoUser', 'https://f3.local/rss', 'secret-z', 'configured',
			'{"auth_mode":"basic","username":""}'::jsonb)`)
}
