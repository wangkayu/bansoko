package workitems

import (
	adapter "bansoko/internal/workitems/adapter/persistence"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// LockedWorkItems binds a live-row lock set to the caller transaction.
type LockedWorkItems struct {
	tx  *sql.Tx
	ids map[string]bool
}

// LockLiveForOwners locks the current live acquisition items before their owners.
func (s *Store) LockLiveForOwners(ctx context.Context, tx *sql.Tx, owners []OwnerRef) (LockedWorkItems, error) {
	if tx == nil {
		return LockedWorkItems{}, errors.New("live work lock transaction required")
	}
	ids := map[string]bool{}
	for _, owner := range owners {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM work_items WHERE owner_type=$1 AND owner_id=$2 AND kind IN ('release_evaluate','rss_refresh') AND state IN ('available','leased')`, owner.Type, owner.ID)
		if err != nil {
			return LockedWorkItems{}, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return LockedWorkItems{}, err
			}
			ids[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return LockedWorkItems{}, err
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	locked := LockedWorkItems{tx: tx, ids: map[string]bool{}}
	for _, id := range ordered {
		var actual string
		err := (adapter.Locks{DB: tx}).LiveIdentity(ctx, id).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return LockedWorkItems{}, err
		}
		locked.ids[actual] = true
	}
	return locked, nil
}

// CreateOrMergeLocked preserves merge semantics for prelocked live rows only.
// A new live conflict is left for its already scheduled evaluation to converge.
func (s *Store) CreateOrMergeLocked(ctx context.Context, tx *sql.Tx, spec WorkItemSpec, locked LockedWorkItems) (id string, created bool, err error) {
	if tx == nil || locked.tx != tx || locked.ids == nil {
		return "", false, errors.New("locked work items scope invalid")
	}
	if spec.Owner.Type != OwnerRelease || spec.Kind != KindReleaseEvaluate {
		return "", false, fmt.Errorf("%w: locked merge only supports release evaluation", ErrInvalidSpec)
	}
	if err := spec.validate(); err != nil {
		return "", false, err
	}
	var live string
	err = tx.QueryRowContext(ctx, `SELECT id FROM work_items WHERE owner_type=$1 AND owner_id=$2 AND kind=$3 AND state IN ('available','leased')`, spec.Owner.Type, spec.Owner.ID, spec.Kind).Scan(&live)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if live != "" && !locked.ids[live] {
		// This committed live item carries the obligation after the caller releases Release.
		return live, false, nil
	}
	if live != "" {
		return s.CreateOrMerge(ctx, tx, spec)
	}
	// Caller holds Release FOR UPDATE. A concurrent creation for this Release
	// cannot remain uncommitted here when all writers honor that lock.
	id, err = s.Create(ctx, tx, spec)
	if err != nil {
		return "", false, fmt.Errorf("insert locked release evaluation: %w", err)
	}
	return id, true, nil
}
