package domain

import "testing"

func TestSecretMutationSemantics(t *testing.T) {
	if err := (SecretMutation{Action: SecretKeep, Value: "x"}).Validate(); err == nil {
		t.Fatal("keep must not accept a value")
	}
	if err := (SecretMutation{Action: SecretReplace, Value: "new-secret"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SecretMutation{Action: SecretClear}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFeedConnectionConfigRequiresExplicitAuthSemantics(t *testing.T) {
	none, err := (HTTPFeedConnectionConfig{}).Normalize()
	if err != nil || none.AuthMode != HTTPFeedAuthNone || none.Username != "" {
		t.Fatalf("default HTTP feed auth was not normalized to none: %+v err=%v", none, err)
	}
	basic, err := (HTTPFeedConnectionConfig{AuthMode: HTTPFeedAuthBasic, Username: " feed-user "}).Normalize()
	if err != nil || basic.Username != "feed-user" {
		t.Fatalf("basic HTTP feed auth was not normalized: %+v err=%v", basic, err)
	}
	for _, config := range []HTTPFeedConnectionConfig{
		{AuthMode: HTTPFeedAuthBasic},
		{AuthMode: HTTPFeedAuthNone, Username: "user"},
		{AuthMode: HTTPFeedAuthBearer, Username: "user"},
		{AuthMode: HTTPFeedAuthCookie, Username: "user"},
		{AuthMode: "digest"},
	} {
		if _, err := config.Normalize(); err == nil {
			t.Fatalf("invalid HTTP feed auth config was accepted: %+v", config)
		}
	}
}

func TestNetworkRouteInputValidatesTypedEndpoints(t *testing.T) {
	direct, err := (NetworkRouteInput{Name: " direct ", Kind: NetworkRouteDirect}).Normalize()
	if err != nil || direct.Name != "direct" || direct.Endpoint != "" {
		t.Fatalf("direct route was not normalized: %+v err=%v", direct, err)
	}
	proxyRoute, err := (NetworkRouteInput{Name: "http", Kind: NetworkRouteHTTPProxy, Endpoint: "http://127.0.0.1:7890"}).Normalize()
	if err != nil || proxyRoute.Endpoint == "" {
		t.Fatalf("HTTP proxy route was rejected: %+v err=%v", proxyRoute, err)
	}
	if _, err := (NetworkRouteInput{Name: "bad", Kind: NetworkRouteHTTPProxy, Endpoint: "socks5://127.0.0.1:1080"}).Normalize(); err == nil {
		t.Fatal("proxy route must reject SOCKS5 endpoint")
	}
	if _, err := (NetworkRouteInput{Name: "credentials", Kind: NetworkRouteHTTPProxy, Endpoint: "http://user:pass@127.0.0.1:7890"}).Normalize(); err == nil {
		t.Fatal("route endpoint must not embed credentials")
	}
	github, err := (NetworkRouteInput{Name: "github", Kind: NetworkRouteGitHub, Endpoint: "http://127.0.0.1:7890"}).Normalize()
	if err != nil || github.Kind != NetworkRouteGitHub || github.Endpoint == "" {
		t.Fatalf("github route was rejected: %+v err=%v", github, err)
	}
	if _, err := (NetworkRouteInput{Name: "github", Kind: NetworkRouteGitHub, Endpoint: "socks5://127.0.0.1:1080"}).Normalize(); err == nil {
		t.Fatal("github route must reject a SOCKS5 endpoint")
	}
	if _, err := (NetworkRouteInput{Name: "github", Kind: NetworkRouteGitHub}).Normalize(); err == nil {
		t.Fatal("github route must require a proxy endpoint")
	}
}

func TestQBittorrentConfigNormalizesAndProtectsSystemTags(t *testing.T) {
	config, err := (QBittorrentConnectionConfig{
		Username: " admin ", Category: " anime ", UserTags: []string{"z", "a"}, SaveRoot: "/downloads/",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if config.Username != "admin" || config.Category != "anime" || config.SaveRoot != "/downloads" {
		t.Fatalf("config was not normalized: %+v", config)
	}
	if config.ApplicationRoot != "" {
		t.Fatalf("downloader_root should default to empty (same-path), got %q", config.ApplicationRoot)
	}
	if len(config.UserTags) != 2 || config.UserTags[0] != "a" {
		t.Fatalf("tags were not canonicalized: %+v", config.UserTags)
	}
	if _, err := (QBittorrentConnectionConfig{Username: "admin", UserTags: []string{"bansoko-system"}}).Normalize(); err == nil {
		t.Fatal("system idempotency tag prefix must be reserved")
	}
}

func TestQBittorrentConfigNormalizesDownloaderRootMapping(t *testing.T) {
	// An explicit downloader_root is the host-readable mapping of the qB
	// savepath; it is normalized the same way as save_root and persisted
	// independently (empty means the same-path convention).
	config, err := (QBittorrentConnectionConfig{
		Username: "admin", SaveRoot: "/downloads", ApplicationRoot: "/user/downloads/",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if config.ApplicationRoot != "/user/downloads" {
		t.Fatalf("downloader_root was not normalized: %q", config.ApplicationRoot)
	}
	if _, err := (QBittorrentConnectionConfig{
		Username: "admin", SaveRoot: "/downloads", ApplicationRoot: "../escape",
	}).Normalize(); err == nil {
		t.Fatal("downloader_root with parent traversal must be rejected")
	}
}
