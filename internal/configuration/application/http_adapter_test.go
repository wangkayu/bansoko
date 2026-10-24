package application

import (
	"context"
	"testing"

	"bansoko/internal/configuration/domain"
	configurationtransport "bansoko/internal/configuration/transport"
	"bansoko/internal/transport/httpapi/generated"
)

func mustConnectionInput(t *testing.T, fill func(*generated.ConnectionInput) error) generated.ConnectionInput {
	t.Helper()
	var input generated.ConnectionInput
	if err := fill(&input); err != nil {
		t.Fatal(err)
	}
	return input
}

func mustSecret(t *testing.T, fill func(*generated.ConnectionSecret) error) generated.ConnectionSecret {
	t.Helper()
	var secret generated.ConnectionSecret
	if err := fill(&secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestConnectionInputMapsBGMConnection(t *testing.T) {
	routeID := generated.ID("route-9")
	input := mustConnectionInput(t, func(input *generated.ConnectionInput) error {
		return input.FromBGMConnectionInput(generated.BGMConnectionInput{
			Kind: "bgm", Name: "BGM 反代", Enabled: true, BaseUrl: "https://bgm-proxy.local", RouteId: &routeID,
		})
	})
	mapped, err := configurationtransport.ConnectionInput(input, "bgm-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Kind != domain.ConnectionBGM || mapped.Name != "BGM 反代" || mapped.Endpoint != "https://bgm-proxy.local" {
		t.Fatalf("unexpected bgm mapping: %+v", mapped)
	}
	if mapped.NetworkRouteID != "route-9" {
		t.Fatalf("expected route binding, got %q", mapped.NetworkRouteID)
	}
	if mapped.Secret.Action != domain.SecretKeep || mapped.HTTPFeedConfig != nil || mapped.QBittorrentConfig != nil {
		t.Fatalf("bgm must stay credential-free: %+v", mapped)
	}
}

func TestConnectionViewMapsBGMConnection(t *testing.T) {
	view, err := configurationtransport.ConnectionView(domain.Connection{
		ID: "bgm-1", Kind: domain.ConnectionBGM, Name: "BGM", Endpoint: "https://api.bgm.tv",
		NetworkRouteID: "route-1", State: domain.ConnectionConfigured, Version: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	bgm, err := view.AsBGMConnection()
	if err != nil {
		t.Fatal(err)
	}
	if bgm.BaseUrl != "https://api.bgm.tv" || !bgm.Enabled || bgm.Version != 3 || bgm.RouteId == nil || *bgm.RouteId != "route-1" {
		t.Fatalf("unexpected bgm view: %+v", bgm)
	}
}

func TestConnectionSecretTriStateMapping(t *testing.T) {
	cases := []struct {
		name   string
		secret generated.ConnectionSecret
		want   domain.SecretMutation
	}{
		{name: "keep", secret: mustSecret(t, func(s *generated.ConnectionSecret) error {
			return s.FromConnectionSecretKeep(generated.ConnectionSecretKeep{Mode: "keep"})
		}), want: domain.SecretMutation{Action: domain.SecretKeep}},
		{name: "replace", secret: mustSecret(t, func(s *generated.ConnectionSecret) error {
			return s.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: "new-secret"})
		}), want: domain.SecretMutation{Action: domain.SecretReplace, Value: "new-secret"}},
		{name: "remove maps to domain clear", secret: mustSecret(t, func(s *generated.ConnectionSecret) error {
			return s.FromConnectionSecretRemove(generated.ConnectionSecretRemove{Mode: "remove"})
		}), want: domain.SecretMutation{Action: domain.SecretClear}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := configurationtransport.ConnectionSecretMutation(testCase.secret)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("expected %+v, got %+v", testCase.want, got)
			}
		})
	}
}

func TestConnectionInputMapsSecretThroughQBittorrentAndTMDB(t *testing.T) {
	remove := mustSecret(t, func(s *generated.ConnectionSecret) error {
		return s.FromConnectionSecretRemove(generated.ConnectionSecretRemove{Mode: "remove"})
	})
	qbCategory := "anime"
	qbTags := []string{"bangumi", "v2"}
	qb := mustConnectionInput(t, func(input *generated.ConnectionInput) error {
		return input.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
			Kind: "qbittorrent", Name: "qb", Enabled: true, BaseUrl: "http://qb.local:8080", Username: "u", Secret: remove,
			Category: &qbCategory, UserTags: &qbTags,
		})
	})
	mapped, err := configurationtransport.ConnectionInput(qb, "qb-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Secret.Action != domain.SecretClear {
		t.Fatalf("expected qb secret clear, got %+v", mapped.Secret)
	}
	if mapped.QBittorrentConfig == nil || mapped.QBittorrentConfig.Category != "anime" || len(mapped.QBittorrentConfig.UserTags) != 2 {
		t.Fatalf("qb typed config must carry category/user_tags: %+v", mapped.QBittorrentConfig)
	}
	tmdb := mustConnectionInput(t, func(input *generated.ConnectionInput) error {
		return input.FromTMDBConnectionInput(generated.TMDBConnectionInput{
			Kind: "tmdb", Name: "tmdb", Enabled: true, BaseUrl: "https://api.themoviedb.org/3", Secret: remove,
		})
	})
	mapped, err = configurationtransport.ConnectionInput(tmdb, "tmdb-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Secret.Action != domain.SecretClear {
		t.Fatalf("expected tmdb secret clear, got %+v", mapped.Secret)
	}
}

func TestConnectionViewMapsQBittorrentTypedConfig(t *testing.T) {
	view, err := configurationtransport.ConnectionView(domain.Connection{
		ID: "qb-1", Kind: domain.ConnectionQBittorrent, Name: "QB", Endpoint: "http://qb.local:8080",
		State: domain.ConnectionConfigured, Version: 5,
		QBittorrentConfig: &domain.QBittorrentConnectionConfig{Username: "u", Category: "anime", UserTags: []string{"bangumi", "v2"}, SaveRoot: "/downloads", ApplicationRoot: "/user/downloads"},
	})
	if err != nil {
		t.Fatal(err)
	}
	qb, err := view.AsQBittorrentConnection()
	if err != nil {
		t.Fatal(err)
	}
	if qb.Category == nil || *qb.Category != "anime" || qb.UserTags == nil || len(*qb.UserTags) != 2 || qb.DownloadRoot == nil || *qb.DownloadRoot != "/downloads" {
		t.Fatalf("qb view must expose typed config: %+v", qb)
	}
	if qb.DownloaderRoot == nil || *qb.DownloaderRoot != "/user/downloads" {
		t.Fatalf("qb view must expose downloader_root mapping: %+v", qb)
	}
}

// The rss_http input carries only name, enabled and route: the endpoint is a
// backend-written placeholder, auth is fixed to none, and UA, timeout and
// size limits no longer go through the connection configuration.
func TestConnectionInputMapsRSSHTTPWithFixedTransportPolicy(t *testing.T) {
	input := mustConnectionInput(t, func(input *generated.ConnectionInput) error {
		return input.FromRSSHTTPConnectionInput(generated.RSSHTTPConnectionInput{
			Kind: "rss_http", Name: "RSS 传输", Enabled: true,
		})
	})
	mapped, err := configurationtransport.ConnectionInput(input, "rss-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Kind != domain.ConnectionHTTPFeed || mapped.Endpoint != configurationtransport.RssHTTPEndpointPlaceholder {
		t.Fatalf("unexpected rss_http mapping: %+v", mapped)
	}
	if mapped.HTTPFeedConfig == nil || mapped.HTTPFeedConfig.AuthMode != domain.HTTPFeedAuthNone {
		t.Fatalf("expected fixed auth_mode=none, got %+v", mapped.HTTPFeedConfig)
	}
	view, err := configurationtransport.ConnectionView(domain.Connection{
		ID: "rss-1", Kind: domain.ConnectionHTTPFeed, Name: "RSS 传输", Endpoint: configurationtransport.RssHTTPEndpointPlaceholder,
		NetworkRouteID: "route-2", State: domain.ConnectionConfigured, Version: 1,
		HTTPFeedConfig: &domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	rss, err := view.AsRSSHTTPConnection()
	if err != nil {
		t.Fatal(err)
	}
	if rss.Name != "RSS 传输" || rss.RouteId == nil || *rss.RouteId != "route-2" {
		t.Fatalf("unexpected rss_http view: %+v", rss)
	}
}

type generatedHTTPService struct{ service *HTTPService }

func newGeneratedHTTPService(service *HTTPService) generatedHTTPService {
	return generatedHTTPService{service: service}
}

func (s generatedHTTPService) ListConnections(ctx context.Context) ([]generated.Connection, error) {
	values, err := s.service.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	return configurationtransport.ConnectionsView(values)
}

func (s generatedHTTPService) PutConnection(ctx context.Context, id string, expected int64, input generated.ConnectionInput) (generated.Connection, error) {
	mapped, err := configurationtransport.ConnectionInput(input, id)
	if err != nil {
		return generated.Connection{}, err
	}
	value, err := s.service.PutConnection(ctx, id, expected, mapped)
	if err != nil {
		return generated.Connection{}, err
	}
	return configurationtransport.ConnectionView(value)
}

func (s generatedHTTPService) TestConnection(ctx context.Context, id string) (generated.ConnectionTestResult, error) {
	value, err := s.service.TestConnection(ctx, id)
	if err != nil {
		return generated.ConnectionTestResult{}, err
	}
	return configurationtransport.ConnectionTestResult(value), nil
}

func (s generatedHTTPService) ListNetworkRoutes(ctx context.Context) ([]generated.NetworkRoute, error) {
	values, err := s.service.ListNetworkRoutes(ctx)
	if err != nil {
		return nil, err
	}
	return configurationtransport.NetworkRoutesView(values)
}

func (s generatedHTTPService) PutNetworkRoute(ctx context.Context, id string, expected int64, input generated.NetworkRouteInput) (generated.NetworkRoute, error) {
	mapped, err := configurationtransport.NetworkRouteInput(input, id)
	if err != nil {
		return generated.NetworkRoute{}, err
	}
	value, err := s.service.PutNetworkRoute(ctx, id, expected, mapped)
	if err != nil {
		return generated.NetworkRoute{}, err
	}
	return configurationtransport.NetworkRouteView(value)
}

func (s generatedHTTPService) TestNetworkRoute(ctx context.Context, id string) (generated.NetworkRouteTestResult, error) {
	value, err := s.service.TestNetworkRoute(ctx, id)
	if err != nil {
		return generated.NetworkRouteTestResult{}, err
	}
	return configurationtransport.NetworkRouteTestResult(value), nil
}
