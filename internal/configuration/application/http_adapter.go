package application

import (
	"context"
	"time"

	"bansoko/internal/configuration/domain"
	"bansoko/internal/download/qbittorrent"
)

// HTTPService is the typed connection and route application boundary. It
// contains no persistence of its own.
type HTTPService struct{ owner *Service }

func NewHTTPService(owner *Service) *HTTPService { return &HTTPService{owner: owner} }

var _ interface {
	ListConnections(context.Context) ([]domain.Connection, error)
	PutConnection(context.Context, string, int64, domain.ConnectionInput) (domain.Connection, error)
	TestConnection(context.Context, string) (domain.ConnectionTestResult, error)
	ListNetworkRoutes(context.Context) ([]domain.NetworkRoute, error)
	PutNetworkRoute(context.Context, string, int64, domain.NetworkRouteInput) (domain.NetworkRoute, error)
	TestNetworkRoute(context.Context, string) (domain.NetworkRouteTestResult, error)
} = (*HTTPService)(nil)

func (s *HTTPService) ListConnections(ctx context.Context) ([]domain.Connection, error) {
	return s.owner.ListConnections(ctx)
}

func (s *HTTPService) PutConnection(ctx context.Context, id string, expected int64, input domain.ConnectionInput) (domain.Connection, error) {
	input.ID = id
	input.ExpectedVersion = expected
	return s.owner.UpsertConnection(ctx, input)
}

func (s *HTTPService) TestConnection(ctx context.Context, id string) (domain.ConnectionTestResult, error) {
	item, err := s.owner.GetConnection(ctx, id)
	if err != nil {
		return domain.ConnectionTestResult{}, err
	}
	// ClientResolver must be the same resolution path the runtime uses
	// (s.owner), so a route resolved here is the route the matcher resolves.
	snapshot := (ProviderConnectionTester{ResolveSecret: s.owner.secretResolver(), ClientResolver: s.owner}).Test(ctx, item)
	if err := s.owner.RecordHealth(ctx, snapshot); err != nil {
		return domain.ConnectionTestResult{}, err
	}
	result := domain.ConnectionTestResult{ConnectionID: item.ID, Kind: item.Kind, CheckedAt: snapshot.CheckedAt}
	if snapshot.State == domain.ConnectionConfigured {
		result.Health = "ready"
	} else {
		result.Health = "failed"
		result.ReasonCode = &snapshot.Code
		result.ReasonDetail = &snapshot.Message
	}
	return result, nil
}

func (s *HTTPService) ListNetworkRoutes(ctx context.Context) ([]domain.NetworkRoute, error) {
	return s.owner.ListNetworkRoutes(ctx)
}

func (s *HTTPService) PutNetworkRoute(ctx context.Context, id string, expected int64, input domain.NetworkRouteInput) (domain.NetworkRoute, error) {
	input.ID = id
	return s.owner.UpsertNetworkRoute(ctx, input, expected)
}

func (s *HTTPService) TestNetworkRoute(ctx context.Context, id string) (domain.NetworkRouteTestResult, error) {
	item, err := s.owner.GetNetworkRoute(ctx, id)
	if err != nil {
		return domain.NetworkRouteTestResult{}, err
	}
	return domain.NetworkRouteTestResult{RouteID: item.ID, Kind: item.Kind, Health: "ready", CheckedAt: timeNow()}, nil
}

func (s *HTTPService) GetQBittorrentRuntimeConfig(ctx context.Context) (qbittorrent.RuntimeConfig, error) {
	item, err := s.owner.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		return qbittorrent.RuntimeConfig{}, err
	}
	return qbittorrent.RuntimeConfig{
		ConnectionID:      item.ConnectionID,
		ConnectionVersion: item.ConnectionVersion,
		Endpoint:          item.Endpoint,
		Username:          item.Username,
		Password:          item.Password,
		Category:          item.Category,
		UserTags:          item.UserTags,
		SaveRoot:          item.SaveRoot,
		ApplicationRoot:   item.ApplicationRoot,
	}, nil
}

func (s *Service) secretResolver() SecretResolver {
	if resolver, ok := s.secrets.(SecretResolver); ok {
		return resolver
	}
	return nil
}

func timeNow() time.Time { return time.Now().UTC() }
