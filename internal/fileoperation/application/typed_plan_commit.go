package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Service) commitTypedPlan(ctx context.Context, c workitems.Claim, b *artifactPreparedBatch) (worker.Outcome, error) {
	declarations, err := buildTypedPlanDeclarations(ctx, b)
	if err != nil {
		return worker.Outcome{}, err
	}
	for _, raw := range []string{declarations.Requirements, declarations.Targets, declarations.Plan} {
		parsed, parseErr := protocol.ParseStrict([]byte(raw))
		if parseErr != nil {
			return worker.Outcome{}, parseErr
		}
		if string(protocol.Canonical(parsed)) != raw {
			return worker.Outcome{}, fmt.Errorf("PLAN_CANONICAL_INVALID")
		}
	}
	var result workitems.OwnerResult
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var capture map[string]json.RawMessage
		if err := json.Unmarshal(b.capture.Facts, &capture); err != nil {
			return "", err
		}
		var payload struct {
			Bindings json.RawMessage `json:"bindings"`
		}
		if err := json.Unmarshal(batchPayload(b), &payload); err != nil {
			return "", err
		}
		capture["prepared_bindings"] = payload.Bindings
		handoffs := []map[string]any{}
		var planItems struct {
			Items []struct {
				Ref      string         `json:"file_ref"`
				Kind     string         `json:"kind"`
				Evidence map[string]any `json:"evidence"`
			} `json:"items"`
		}
		decoder := json.NewDecoder(strings.NewReader(declarations.Plan))
		decoder.UseNumber()
		if err := decoder.Decode(&planItems); err != nil {
			return "", err
		}
		for _, item := range planItems.Items {
			if item.Kind != "adopt" && item.Kind != "retain" {
				continue
			}
			for _, file := range b.artifact.files {
				if file.fileRef == item.Ref {
					facts, err := observedSatisfactionFileFact(b, file, item.Evidence)
					if err != nil {
						return "", err
					}
					handoffs = append(handoffs, map[string]any{"file_ref": item.Ref, "canonical_file_facts": facts, "delivery_digest": item.Evidence["digest"]})
				}
			}
		}
		capture["prepared_handoffs"], err = json.Marshal(handoffs)
		if err != nil {
			return "", err
		}
		captureBytes, err := json.Marshal(capture)
		if err != nil {
			return "", err
		}
		var raw []byte
		err = scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, b.capture.PlanInputDigest, b.input, declarations.Requirements, declarations.Targets, declarations.Plan, typedExecuteOperations(b, declarations), captureBytes).Scan(&raw)
		if err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		if confirmed, ok, observeErr := s.confirmTypedPlanCommit(ctx, c); observeErr == nil && ok {
			return confirmed, nil
		}
		return worker.Outcome{}, err
	}
	out := worker.Committed(c, result)
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, nil
}

func (s *Service) typedPlanError(ctx context.Context, c workitems.Claim, cause error, terminal bool) (worker.Outcome, error) {
	var result workitems.OwnerResult
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		code, reason := planTerminalCode(cause), boundedReason(safeReason(cause), 1024)
		var err error
		if terminal {
			err = scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_failed($1,$2,$3,$4,$5)`, c.Owner.ID, c.ID, c.FencingToken, code, reason).Scan(&raw)
		} else {
			err = scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_retry($1,$2,$3,$4,$5,$6)`, c.Owner.ID, c.ID, c.FencingToken, code, reason, planRetryAfter.Seconds()).Scan(&raw)
		}
		if err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		if confirmed, ok, observeErr := s.confirmTypedPlanCommit(ctx, c); observeErr == nil && ok {
			return confirmed, nil
		}
		return worker.Outcome{}, err
	}
	out := worker.Committed(c, result)
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, nil
}
