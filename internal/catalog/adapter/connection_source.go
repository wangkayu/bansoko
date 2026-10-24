package adapter

import (
	"context"
	"database/sql"
	"fmt"

	configurationdomain "bansoko/internal/configuration/domain"
)

// DBConnectionSource reads connections directly from the connections table
// and projects them into configuration domain Connection values. It exists so
// the catalog ConfiguredReader can resolve the saved provider connection
// without depending on the configuration service's generated-type return
// shape.
type DBConnectionSource struct {
	DB *sql.DB
}

func (s DBConnectionSource) ListConnections(ctx context.Context) ([]configurationdomain.Connection, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("catalog connection source database is nil")
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, kind, name, endpoint, COALESCE(secret_ref, ''), COALESCE(network_route_id, ''),
       state, health_version, version, updated_at
FROM connections
ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("query connections: %w", err)
	}
	defer rows.Close()
	var connections []configurationdomain.Connection
	for rows.Next() {
		var c configurationdomain.Connection
		var kind, state string
		if err := rows.Scan(&c.ID, &kind, &c.Name, &c.Endpoint, &c.SecretRef, &c.NetworkRouteID,
			&state, &c.HealthVersion, &c.Version, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		c.Kind = configurationdomain.ConnectionKind(kind)
		c.State = configurationdomain.ConnectionState(state)
		connections = append(connections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connections: %w", err)
	}
	return connections, nil
}
