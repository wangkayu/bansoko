package application

import (
	"context"
	"database/sql"
	"sync"

	catalogapp "bansoko/internal/catalog/application"
	providerdomain "bansoko/internal/provider/domain"
)

type groupReadFunc func(context.Context, *sql.DB, catalogapp.GroupReader, string, string) (providerdomain.GroupReadResult, providerdomain.Status, error)

// memoizeGroupRead shares one upstream read and its health record across all
// subscriptions for the same provider entry in a dispatch run.
func memoizeGroupRead() groupReadFunc {
	type scope struct{ provider, entry string }
	type read struct {
		result providerdomain.GroupReadResult
		status providerdomain.Status
		err    error
	}
	var mu sync.Mutex
	cache := map[scope]read{}
	return func(ctx context.Context, db *sql.DB, reader catalogapp.GroupReader, provider, entry string) (providerdomain.GroupReadResult, providerdomain.Status, error) {
		mu.Lock()
		defer mu.Unlock()
		key := scope{provider, entry}
		if cached, ok := cache[key]; ok {
			return cached.result, cached.status, cached.err
		}
		result, status, err := catalogapp.ReadProviderGroups(ctx, db, reader, provider, entry)
		cache[key] = read{result, status, err}
		return result, status, err
	}
}
