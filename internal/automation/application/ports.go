package application

import (
	"context"

	catalogdomain "bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// GroupReader discovers provider groups for one catalog entry.
type GroupReader interface {
	ReadGroups(context.Context, string) (providerdomain.GroupReadResult, error)
}

// ConnectionSource provides the current user-configured connections.
type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// CatalogRefresher refreshes one provider catalog season (conditional request
// included) and reads the provider's scheduled-refresh switch.
type CatalogRefresher interface {
	RefreshProviderSeason(context.Context, string, string) (catalogdomain.Snapshot, error)
	ProviderRefreshEnabled(context.Context, string) (bool, error)
}
