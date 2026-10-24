package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
)

// adoptReplacementReservation handles already admitted DB-only reservations:
// every replace decision of the release bound to a candidate placement (one
// per coverage key) at the claimed Subscription version. It never evaluates
// candidates or runs a file executor. Frozen loser cleanup owners and their
// continuations are persisted in this same fenced transaction; a loser shared
// by two key decisions (a multi-episode file) is handed off once.
func (s *ReplacementDecisionService) adoptReplacementReservation(ctx context.Context, c workitems.Claim, release string) (worker.Outcome, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM replacement_decisions WHERE managed_anime_id=$1 AND candidate_release_id=$2 AND subscription_version=$3 AND verdict='replace' AND candidate_placement_id IS NOT NULL AND state IN ('admitted','settled') ORDER BY coverage_key,created_at DESC,id DESC`, c.Owner.ID, release, c.ExpectedOwnerVersion)
	if err != nil {
		return worker.Outcome{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return worker.Outcome{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return worker.Outcome{}, err
	}
	rows.Close()
	decisions := make([]*decisionRow, 0, len(ids))
	for _, id := range ids {
		d, err := s.loadDecisionRow(ctx, id)
		if err != nil {
			return worker.Outcome{}, err
		}
		if d == nil || d.Winner == nil {
			return worker.AwaitRecovery(""), nil
		}
		decisions = append(decisions, d)
	}
	if len(decisions) == 0 {
		return worker.AwaitRecovery(""), nil
	}
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		for _, d := range decisions {
			if err := lockFrozenRecoveryDecisionTx(ctx, scope, c, release, d); err != nil {
				return 0, err
			}
		}
		handed := map[string]bool{}
		for _, d := range decisions {
			if d.State == "settled" {
				if err := revalidateSettledRecoveryTx(ctx, scope, d); err != nil {
					return 0, err
				}
				if err := s.handoffRecoveryLosersTx(ctx, scope, d, handed); err != nil {
					return 0, err
				}
				continue
			}
			stale, err := revalidateDecisionTx(ctx, tx, d)
			if err != nil {
				return 0, err
			}
			if stale == "" {
				if stale, err = loserCoverageOutsideCandidateTx(ctx, tx, d); err != nil {
					return 0, err
				}
			}
			if stale != "" {
				return 0, workitems.ErrStaleClaim
			}
			if len(d.Losers) > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE placements SET state='superseded',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=ANY($1) AND state='active'`, d.loserIDs()); err != nil {
					return 0, err
				}
				if err := s.handoffRecoveryLosersTx(ctx, scope, d, handed); err != nil {
					return 0, err
				}
			}
			result, err := tx.ExecContext(ctx, `UPDATE replacement_decisions SET state='settled',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state='admitted'`, d.ID)
			if err != nil {
				return 0, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			if n != 1 {
				return 0, workitems.ErrStaleClaim
			}
		}
		return c.ExpectedOwnerVersion, nil
	}
	return recoveryCommitted("settled", writer), nil
}

// lockFrozenRecoveryDecisionTx locks one recovered decision and proves its
// state, winner and losers are still exactly the frozen evidence read before
// the fenced transaction.
func lockFrozenRecoveryDecisionTx(ctx context.Context, scope txscope.Tx, c workitems.Claim, release string, d *decisionRow) error {
	var state string
	var pools []byte
	if err := scope.SQLTx().QueryRowContext(ctx, `SELECT state,pools FROM replacement_decisions WHERE id=$1 AND managed_anime_id=$2 AND candidate_release_id=$3 AND subscription_version=$4 FOR UPDATE NOWAIT`, d.ID, c.Owner.ID, release, c.ExpectedOwnerVersion).Scan(&state, &pools); err != nil {
		return err
	}
	var fresh []PoolEvidence
	if json.Unmarshal(pools, &fresh) != nil {
		return workitems.ErrStaleClaim
	}
	var losers []PlacementRef
	for _, pool := range fresh {
		losers = append(losers, pool.Losers...)
	}
	if len(losers) != len(d.Losers) {
		return workitems.ErrStaleClaim
	}
	for i := range losers {
		if losers[i] != d.Losers[i] {
			return workitems.ErrStaleClaim
		}
	}
	var winner *PlacementRef
	for _, pool := range fresh {
		if pool.Winner != nil {
			if winner != nil && *winner != *pool.Winner {
				return workitems.ErrStaleClaim
			}
			winner = pool.Winner
		}
	}
	if winner == nil || *winner != *d.Winner || state != d.State {
		return workitems.ErrStaleClaim
	}
	return nil
}
