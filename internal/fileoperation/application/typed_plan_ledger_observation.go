package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
)

func validateObservedPlanLedger(ctx context.Context, tx *sql.Tx, plan string) error {
	var count sql.NullInt32
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT m.active_effect_count,m.active_effect_leases FROM postprocessing_plans p LEFT JOIN one_shot_acquisitions m ON m.id=p.one_shot_id WHERE p.id=$1`, plan).Scan(&count, &raw)
	if err != nil {
		return err
	}
	if !count.Valid {
		return nil
	} // ordinary context or missing marker is checked by the graph proof
	return workitems.ValidateOneShotLedgerObservation(count.Int32, raw)
}
