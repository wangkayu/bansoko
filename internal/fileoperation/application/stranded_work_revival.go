package application

import (
	"context"
	"errors"
	"fmt"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/workitems"
)

// strandedWorkBatch bounds one revival pass. The stranded set is a finite
// leftover of earlier releases, never a steady-state queue.
const strandedWorkBatch = 100

// StrandedWorkRevival reports one revival pass: the operations whose failed
// work was re-opened for the recovery lane and the ones refused (operation id
// to refusal code), which keep their state untouched.
type StrandedWorkRevival struct {
	Revived []string
	Refused map[string]string
}

// ReviveStrandedWork re-opens the file_execute work of every operation
// stranded by a writer-less retry at its attempt budget: the operation is
// pending or running, its acquisition is running, it has no live work and its
// failed work still carries the operation's version (the failure never wrote
// the operation). Under normal operation that set is empty — every terminal
// work outcome is written with its operation, and the typed lane quarantines
// an exhausted retry for the recovery lane instead of failing it.
//
// Each operation goes through file_execute_revive_stranded_work in its own
// transaction; the function re-checks every condition under the root-first
// locks and moves the failed outcome back to the live queue under its own id
// (publication evidence is bound to that id), recovery-blocked. The recovery
// observation then converges it from the evidence without starting an effect:
// a closed proof finishes through the fixed entry, anything else ends the
// operation visibly, and the acquisition settles as usual. A refusal
// (one-shot work, evidence bound to another work, ...) writes nothing and is
// logged.
func (s *Service) ReviveStrandedWork(ctx context.Context) (StrandedWorkRevival, error) {
	result := StrandedWorkRevival{Refused: map[string]string{}}
	if s == nil || s.db == nil {
		return result, errors.New("file operation service is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id FROM public.file_operations o
 JOIN public.acquisitions r ON r.id=o.acquisition_id AND r.state='running'
 WHERE o.state IN ('pending','running')
  AND NOT EXISTS(SELECT 1 FROM public.work_items w WHERE w.owner_type='file_operation' AND w.owner_id=o.id AND w.kind='file_execute')
  AND EXISTS(SELECT 1 FROM public.work_item_outcomes w WHERE w.owner_type='file_operation' AND w.owner_id=o.id AND w.kind='file_execute' AND w.state='failed' AND w.expected_owner_version=o.version)
 ORDER BY o.id LIMIT $1`, strandedWorkBatch)
	if err != nil {
		return result, fmt.Errorf("scan stranded file work: %w", err)
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, fmt.Errorf("scan stranded file work: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	var firstErr error
	for _, id := range candidates {
		code, err := s.reviveStrandedWork(ctx, id)
		switch {
		case err != nil:
			if firstErr == nil {
				firstErr = fmt.Errorf("revive stranded file work %s: %w", id, err)
			}
			s.logStrandedWorkRevival(ctx, id, "file_work_revival_failed", "failure")
		case code == "revived":
			result.Revived = append(result.Revived, id)
			s.logStrandedWorkRevival(ctx, id, "file_work_revived", "success")
		default:
			result.Refused[id] = code
			s.logStrandedWorkRevival(ctx, id, "file_work_revival_refused_"+code, "blocked")
		}
	}
	return result, firstErr
}

func (s *Service) reviveStrandedWork(ctx context.Context, operationID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var code string
	if err := tx.QueryRowContext(ctx, `SELECT public.file_execute_revive_stranded_work($1)`, operationID).Scan(&code); err != nil {
		return "", err
	}
	if code != "revived" {
		return code, nil
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

func (s *Service) logStrandedWorkRevival(ctx context.Context, operationID, reasonCode, outcome string) {
	if s.logger == nil {
		return
	}
	event := observabilityport.RuntimeEvent{
		Component:  "fileoperation",
		Operation:  "file_work_revival",
		Outcome:    outcome,
		ReasonCode: reasonCode,
		OwnerType:  workitems.OwnerFileOperation,
		OwnerID:    operationID,
	}
	if outcome == "success" {
		event.Message = "stranded file work re-opened for the recovery lane"
		s.logger.Warn(ctx, event)
		return
	}
	event.Message = "stranded file work left untouched"
	event.ErrorClass = "file_work_revival"
	s.logger.Error(ctx, event)
}
