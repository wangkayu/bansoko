package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type preparedBinding struct {
	file     artifactFileContext
	decision *bindingDecision
	locked   *bindingSnapshot
}
type artifactPreparedBatch struct {
	capture       PreparedPlanCapture
	input         json.RawMessage
	artifact      artifactContext
	prep          prepareContext
	frozen        frozenReleaseFacts
	targets       map[string]frozenTargetRecord
	existing      map[string]operationRecord
	reused        []operationRecord
	operations    []preparedOperation
	bindings      []preparedBinding
	satisfactions map[string]preparedSatisfaction
	// excluded maps a file_ref the planner deliberately leaves out to the typed
	// reason: an unknown file under the skip route, or a subtitle that follows
	// a video that is not planned. Declarations mark these "excluded", not
	// "required", so they are neither failed nor given a binding.
	excluded map[string]string
}

func (s *Service) captureArtifactBatch(ctx context.Context, plan *postProcessingPlanRow) (*artifactPreparedBatch, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	artifact, err := loadArtifactContextWithLocks(ctx, tx, plan.artifactID, false)
	if err != nil {
		return nil, err
	}
	b := &artifactPreparedBatch{artifact: artifact, targets: map[string]frozenTargetRecord{}, existing: map[string]operationRecord{}}
	b.capture, err = captureTypedPlan(ctx, tx, plan.id)
	if err != nil {
		return nil, err
	}
	if err := loadTypedPlanInput(ctx, tx, b); err != nil {
		return nil, err
	}
	if _, err := capturedVarsResult(b); err != nil {
		return nil, err
	}
	// The "keep unknown files" switch is the batch plan's frozen route, read
	// with the snapshot below; the live instance switch is never consulted.
	var captured struct {
		Requirements     json.RawMessage `json:"requirements"`
		RequirementFiles []struct {
			Ref  string `json:"file_ref"`
			Kind string `json:"requirement"`
		} `json:"requirement_files"`
		Targets []struct {
			Ref       string          `json:"file_ref"`
			Path      string          `json:"canonical_path"`
			Intent    string          `json:"intent"`
			Collision string          `json:"collision"`
			Render    json.RawMessage `json:"render_snapshot"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(b.capture.Facts, &captured); err != nil {
		return nil, err
	}
	var accepted struct {
		Selection struct {
			Mode string   `json:"mode"`
			Refs []string `json:"explicit_refs"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(b.input, &accepted); err != nil {
		return nil, err
	}
	if accepted.Selection.Mode == "explicit_refs" {
		b.prep.selectedRefs = map[string]bool{}
		for _, ref := range accepted.Selection.Refs {
			b.prep.selectedRefs[ref] = true
		}
	}
	if len(captured.Requirements) > 0 && string(captured.Requirements) != "null" {
		b.prep.selectedRefs = map[string]bool{}
		for _, f := range captured.RequirementFiles {
			b.prep.selectedRefs[f.Ref] = f.Kind == "required"
		}
	}
	if len(captured.Targets) > 0 {
		b.prep.replanTargets = false
		for _, t := range captured.Targets {
			for _, f := range artifact.files {
				if f.fileRef == t.Ref {
					b.targets[f.id] = frozenTargetRecord{targetPath: t.Path, expectedSize: f.sizeBytes, intent: t.Intent, collisionPolicy: t.Collision, renderSnapshot: t.Render}
				}
			}
		}
	}
	if b.frozen, err = s.loadFrozenEpisodesTx(ctx, tx, artifact.releaseID); err != nil {
		return nil, err
	}
	for _, file := range artifact.files {
		var record operationRecord
		err = scanOperation(tx.QueryRowContext(ctx, operationQuery+" WHERE idempotency_key=$1", stableIdempotencyKey(file.id, artifact.artifactVersion)), &record)
		if err == nil {
			b.existing[file.id] = record
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return b, nil
}
func (s *Service) buildArtifactBatch(ctx context.Context, b *artifactPreparedBatch, oneShot *workitems.OneShotRef) error {
	clone := *s
	clone.preparedBatch = b
	// All resolver/stat/canonicalization now runs after read tx commit.
	b.prep.oneShot = oneShot
	return clone.prepareForArtifactTxWithOverrides(ctx, nil, b.artifact.artifactID, oneShot, nil)
}
func (s *Service) persistOperationPlan(ctx context.Context, tx *sql.Tx, plan operationPlan, artifact artifactContext, file artifactFileContext) error {
	if s.preparedBatch != nil {
		if plan.collisionPolicy != "overwrite" && plan.state != ownerStateFailed {
			// The accepted plan renders each operation under the global skip
			// or version policy; an approved replacement takes effect above
			// it, so the approved path alone becomes an overwrite operation
			// with its execution checkpoint.
			approved, err := approvedReplacementPaths(s.preparedBatch.input)
			if err != nil {
				return err
			}
			if approved[plan.targetPath] {
				plan.collisionPolicy = "overwrite"
				if plan.overwriteSnapshot, err = buildOverwriteExecutionSnapshot(plan, s.parentAuthorizer); err != nil {
					return err
				}
			}
		}
		if plan.collisionPolicy == "overwrite" {
			approved, err := approvedReplacementPaths(s.preparedBatch.input)
			if err != nil {
				return err
			}
			global, err := deliveryCollision(s.preparedBatch.input)
			if err != nil {
				return err
			}
			switch {
			case approved[plan.targetPath]:
				// An approved replacement is atomic whatever the global
				// policy: this one target is overwritten.
				plan.intent = "replace"
			case global != "overwrite":
				// Only an approval lifts a target above the global skip or
				// version policy; a target no approval names keeps it.
				plan = applyGlobalCollision(plan, global)
			case len(plan.overwriteSnapshot) == 0:
				// Overwrite only authorizes replacing an existing file. The
				// planner observed this target absent (no overwrite execution
				// snapshot), so it is an ordinary ensure_present that refuses
				// (block) anything appearing at the path before publication.
				plan = downgradeAbsentOverwrite(plan)
			default:
				// An existing target that no approval names. A
				// sidecar already holding the same file is adopted without a
				// write.
				adopted, err := s.adoptReplacementSidecar(ctx, s.preparedBatch, &plan, file)
				if err != nil {
					return err
				}
				if adopted {
					s.preparedBatch.operations = append(s.preparedBatch.operations, preparedOperation{plan, artifact, file})
					return nil
				}
				// Any other present file is replaced under the overwrite
				// setting: an unmanaged one, and a
				// managed one whose placement becomes displaced when the
				// successor's placement activates.
			}
		}
		if err := s.observePlanSatisfaction(ctx, s.preparedBatch, &plan, file); err != nil {
			return err
		}
		s.preparedBatch.operations = append(s.preparedBatch.operations, preparedOperation{plan, artifact, file})
		return nil
	}
	return errors.New("Operation persistence requires a captured prepare batch")
}

func batchPayload(b *artifactPreparedBatch) json.RawMessage {
	operations := make([]map[string]any, 0, len(b.operations))
	for _, op := range b.operations {
		p := op.plan
		operations = append(operations, map[string]any{"id": p.id, "artifact": p.artifactID, "version": p.artifactVersion, "file": p.artifactFileID, "operation": p.operation, "source": p.sourcePath, "target": p.targetPath, "fingerprint": p.sourceFingerprint, "size": p.expectedSizeBytes, "intent": p.intent, "collision": p.collisionPolicy, "render": p.renderSnapshot, "config": p.configSnapshot, "overwrite": p.overwriteSnapshot, "state": p.state, "failure_code": p.failureCode, "failure_reason": p.failureReason, "key": p.idempotencyKey, "bound_video": p.boundVideoOperationID})
		operations[len(operations)-1]["file_ref"] = op.file.fileRef
		for _, binding := range b.bindings {
			if binding.file.id == op.file.id && binding.locked != nil {
				operations[len(operations)-1]["binding_snapshot"] = binding.locked
			}
		}
	}
	bindings := make([]map[string]any, 0, len(b.bindings))
	byFile := map[string]map[string]any{}
	for _, binding := range b.bindings {
		row, ok := byFile[binding.file.id]
		if !ok {
			row = map[string]any{"file": binding.file.id}
			byFile[binding.file.id] = row
			bindings = append(bindings, row)
		}
		if binding.decision != nil {
			raw, _ := marshalBindingSnapshot(*binding.decision, nil)
			row["decision"] = json.RawMessage(raw)
			row["state"] = binding.decision.state
			row["bound_ref"] = nil
			if binding.decision.state == bindingStateBound {
				row["bound_ref"] = binding.decision.boundRef
			}
		}
		if binding.locked != nil {
			row["locked"] = binding.locked
		}
	}
	raw, _ := json.Marshal(map[string]any{"operations": operations, "bindings": bindings})
	return raw
}

// downgradeAbsentOverwrite turns an overwrite operation whose target was
// observed absent into an ordinary ensure_present/block operation.
func downgradeAbsentOverwrite(plan operationPlan) operationPlan {
	plan.collisionPolicy = "block"
	plan.intent = "ensure_present"
	plan.overwriteSnapshot = nil
	return plan
}

// applyGlobalCollision returns an overwrite operation no approval names to
// the delivery's global collision policy (block or version): an ordinary
// ensure_present operation.
func applyGlobalCollision(plan operationPlan, global string) operationPlan {
	plan.collisionPolicy = global
	plan.intent = "ensure_present"
	plan.overwriteSnapshot = nil
	return plan
}

// deliveryCollision reads the accepted delivery's global collision policy.
func deliveryCollision(input json.RawMessage) (string, error) {
	var frozen struct {
		Delivery struct {
			Collision string `json:"collision"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(input, &frozen); err != nil {
		return "", err
	}
	switch frozen.Delivery.Collision {
	case "block", "version", "overwrite":
		return frozen.Delivery.Collision, nil
	}
	return "", fmt.Errorf("accepted delivery collision %q is invalid", frozen.Delivery.Collision)
}

// Typed reasons a planned-away file is declared excluded.
const (
	excludedUnknownSkipped      = "unknown_skipped"
	excludedSubtitleVideoNotRun = "subtitle_video_excluded"
)

// markExcluded records that the planner leaves file out on purpose.
func (s *Service) markExcluded(fileRef, reason string) {
	if s.preparedBatch == nil {
		return
	}
	if s.preparedBatch.excluded == nil {
		s.preparedBatch.excluded = map[string]string{}
	}
	s.preparedBatch.excluded[fileRef] = reason
}

func (s *Service) excludedReason(fileRef string) string {
	if s.preparedBatch == nil {
		return ""
	}
	return s.preparedBatch.excluded[fileRef]
}

func (s *Service) dropPreparedBinding(fileID string) {
	if s.preparedBatch == nil {
		return
	}
	kept := s.preparedBatch.bindings[:0]
	for _, binding := range s.preparedBatch.bindings {
		if binding.file.id != fileID {
			kept = append(kept, binding)
		}
	}
	s.preparedBatch.bindings = kept
}

// approvedReplacementPaths reads the target paths a replace delivery approved
// for overwrite: every DeliveryV3 replacements entry (AcquisitionInputV7), or
// the single known DeliveryV1/V2 replacement of an earlier input. A delivery
// that is not intent replace approves none.
func approvedReplacementPaths(input json.RawMessage) (map[string]bool, error) {
	type approval struct {
		Expected struct {
			Location struct {
				Path string `json:"canonical_path"`
			} `json:"location"`
		} `json:"expected_object"`
	}
	var frozen struct {
		Delivery struct {
			Intent      string `json:"intent"`
			Replacement *struct {
				State string   `json:"state"`
				Value approval `json:"value"`
			} `json:"replacement"`
			Replacements []approval `json:"replacements"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(input, &frozen); err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	if frozen.Delivery.Intent != "replace" {
		return paths, nil
	}
	if r := frozen.Delivery.Replacement; r != nil && r.State == "known" && r.Value.Expected.Location.Path != "" {
		paths[r.Value.Expected.Location.Path] = true
	}
	for _, r := range frozen.Delivery.Replacements {
		if r.Expected.Location.Path != "" {
			paths[r.Expected.Location.Path] = true
		}
	}
	return paths, nil
}
