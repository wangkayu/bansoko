package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Shared inside the caller's snapshot. Never hashes today's filesystem.
func validateObservedSatisfactionProof(ctx context.Context, tx *sql.Tx, plan string) error {
	rows, err := tx.QueryContext(ctx, `SELECT f.plan_evidence FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1 AND f.plan_kind IN ('adopt','retain') ORDER BY f.file_ref COLLATE "C"`, plan)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var wrapper struct {
			Canonical string `json:"canonical"`
			Digest    string `json:"digest"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return err
		}
		v, err := protocol.ParseStrict([]byte(wrapper.Canonical))
		if err != nil {
			return err
		}
		if string(protocol.Canonical(v)) != wrapper.Canonical || protocol.Digest("acquisition-observed-satisfaction:v1", []byte(wrapper.Canonical)) != wrapper.Digest {
			return fmt.Errorf("PLAN_OBSERVED_SATISFACTION_INVALID")
		}
		if err := protocol.ValidateObservedSatisfaction(v); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return nil
}
