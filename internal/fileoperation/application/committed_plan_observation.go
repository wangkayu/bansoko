package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type CommittedPlanObservationRequest struct {
	PlanID, ArtifactID, AcquisitionID string
	OneShot                           *workitems.OneShotRef
}
type CommittedPlanObservation struct{ Status, ReasonCode, DetailCode string }

// ObserveCommittedPlan reads one complete graph snapshot; confirmation grants no execution right.
func (s *Service) ObserveCommittedPlan(ctx context.Context, req CommittedPlanObservationRequest) (CommittedPlanObservation, error) {
	failure := CommittedPlanObservation{Status: "observation_error"}
	if ctx == nil || s == nil || s.db == nil || req.PlanID == "" || req.ArtifactID == "" || req.AcquisitionID == "" {
		return failure, ErrInvalidClaim
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return failure, err
	}
	defer tx.Rollback()
	var artifact, root string
	var marker sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT artifact_id,acquisition_id,one_shot_id FROM postprocessing_plans WHERE id=$1`, req.PlanID).Scan(&artifact, &root, &marker); err != nil {
		return failure, err
	}
	blocked := CommittedPlanObservation{Status: "blocked", ReasonCode: "PLAN_RECOVERY_GRAPH_INCONSISTENT"}
	if artifact != req.ArtifactID || root != req.AcquisitionID {
		return blocked, workitems.ErrInvalidSpec
	}
	if marker.Valid && req.OneShot == nil {
		return blocked, workitems.ErrInvalidOneShotClaim
	}
	if req.OneShot != nil {
		if req.OneShot.MarkerID == "" || req.OneShot.CorrelationID == "" || req.OneShot.AdmissionEpoch <= 0 {
			return blocked, workitems.ErrInvalidOneShotClaim
		}
		if !marker.Valid || marker.String != req.OneShot.MarkerID {
			return blocked, workitems.ErrInvalidSpec
		}
		var correlation, state string
		var epoch int64
		err = tx.QueryRowContext(ctx, `SELECT correlation_id,state,admission_epoch FROM one_shot_acquisitions WHERE id=$1`, marker.String).Scan(&correlation, &state, &epoch)
		if errors.Is(err, sql.ErrNoRows) {
			return blocked, workitems.ErrOneShotMarkerMissing
		}
		if err != nil {
			return failure, err
		}
		if state != "running" || epoch != req.OneShot.AdmissionEpoch {
			return blocked, workitems.ErrOneShotReclaiming
		}
		if correlation != req.OneShot.CorrelationID {
			return blocked, workitems.ErrInvalidSpec
		}
	}
	if err := validateObservedPlanLedger(ctx, tx, req.PlanID); err != nil {
		if errors.Is(err, workitems.ErrInvalidOneShotLedger) {
			return blocked, err
		}
		return failure, err
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE owner_type='postprocessing_plan' AND owner_id=$1 AND kind='postprocess_plan' AND state IN ('available','leased'))`, req.PlanID).Scan(&live); err != nil {
		return failure, err
	}
	if live {
		blocked.ReasonCode = "PLAN_TERMINAL_WORK_LIVE"
		return blocked, nil
	}
	var detail sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT postprocessing_plan_child_diagnostic($1)`, req.PlanID).Scan(&detail); err != nil {
		return failure, err
	}
	if detail.Valid {
		blocked.DetailCode = detail.String
		return blocked, nil
	}
	if err := validateObservedSatisfactionProof(ctx, tx, req.PlanID); err != nil {
		return blocked, err
	}
	var proofs []byte
	// Resolve the exact committed planning identity from the graph, never claim or guess a token.
	if err = tx.QueryRowContext(ctx, `SELECT jsonb_agg(postprocessing_plan_commit_state(w.owner_id,w.id,w.fencing_token)) FROM work_item_outcomes w JOIN postprocessing_plans p ON p.id=w.owner_id WHERE p.id=$1 AND w.owner_type='postprocessing_plan' AND w.kind='postprocess_plan' AND w.state=p.state AND w.expected_owner_version=p.version`, req.PlanID).Scan(&proofs); err != nil {
		return failure, err
	}
	var values []json.RawMessage
	if json.Unmarshal(proofs, &values) != nil || len(values) != 1 || string(values[0]) == "null" {
		return blocked, nil
	}
	if err = tx.Commit(); err != nil {
		return failure, err
	}
	return CommittedPlanObservation{Status: "confirmed"}, nil
}
