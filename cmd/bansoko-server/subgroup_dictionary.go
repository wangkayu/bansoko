package main

import (
	"context"

	catalogdomain "bansoko/internal/catalog/domain"
	provideradapter "bansoko/internal/provider/adapter"
	"bansoko/internal/provider/groupdir"
)

// groupDirectory adapts the embedded collected snapshot to the catalog's
// GroupDirectory port. It is deliberately the only place the two shapes meet:
// the catalog owns what a dictionary entry means, and the provider tree owns
// what the snapshot holds.
//
// It performs no I/O and reads no connection, which is what makes the
// dictionary listing work on an install that has configured no provider.
type groupDirectory struct{}

func (groupDirectory) Matches(provider, query string) []catalogdomain.ResolvedGroup {
	matches := groupdir.Matches(provider, query)
	groups := make([]catalogdomain.ResolvedGroup, 0, len(matches))
	for _, match := range matches {
		groups = append(groups, catalogdomain.ResolvedGroup{
			Provider: provider, GroupRef: match.Key, DisplayName: match.Name, Available: match.Alive,
		})
	}
	return groups
}

func (groupDirectory) Lookup(provider, groupRef string) (catalogdomain.ResolvedGroup, bool) {
	match, ok := groupdir.Lookup(provider, groupRef)
	if !ok {
		return catalogdomain.ResolvedGroup{}, false
	}
	return catalogdomain.ResolvedGroup{
		Provider: provider, GroupRef: match.Key, DisplayName: match.Name, Available: match.Alive,
	}, true
}

// configuredGroupResolver adapts the live provider lookup to the catalog's
// GroupResolver port, mapping the provider layer's entries onto the catalog's
// transport-neutral shape. A failure — including a missing provider connection,
// raised as 503 PROVIDER_GROUP_LOOKUP_FAILED — crosses unchanged, so the
// dictionary's retrieval reports exactly what the provider read reported.
type configuredGroupResolver struct {
	lookup provideradapter.ConfiguredGroupLookup
}

func (r configuredGroupResolver) QueryKey(provider, query string) (string, bool) {
	return r.lookup.QueryKey(provider, query)
}

func (r configuredGroupResolver) Resolve(ctx context.Context, provider, query string) ([]catalogdomain.ResolvedGroup, error) {
	entries, err := r.lookup.Resolve(ctx, provider, query)
	if err != nil {
		return nil, err
	}
	groups := make([]catalogdomain.ResolvedGroup, 0, len(entries))
	for _, entry := range entries {
		groups = append(groups, catalogdomain.ResolvedGroup{
			Provider: string(entry.Ref.Provider), GroupRef: entry.Ref.ExternalID, DisplayName: entry.DisplayName, Available: entry.Available,
		})
	}
	return groups, nil
}
