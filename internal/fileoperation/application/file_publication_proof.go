package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	protocol "bansoko/internal/acquisition/terminal/domain"
)

// loadPublicationProof independently checks persisted canonical facts. It is
// read-only and grants neither execution nor generic Finish. The finish must still
// bind the full Plan/owner/dependency/Delivery/cleanup/ledger write set.
func (s *Service) loadPublicationProof(ctx context.Context, permit string) (protocol.Value, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return protocol.Value{}, err
	}
	defer tx.Rollback()
	result, err := loadPublicationProofTx(ctx, tx, permit)
	if err != nil {
		return protocol.Value{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Value{}, err
	}
	return result, nil
}

// loadPublicationProofTx shares the independent verifier with the future
// root-first finish-locked transaction; it performs no lock or write itself.
func loadPublicationProofTx(ctx context.Context, tx *sql.Tx, permit string) (protocol.Value, error) {
	if tx == nil {
		return protocol.Value{}, ErrInvalidClaim
	}
	var objects, checkpoint, closure, resolution string
	var class, state string
	var conflict bool
	var stepRaw []byte
	var session, operation, collision, dependency string
	var binding bool
	if err := tx.QueryRowContext(ctx, `SELECT e.publication_executor_session,e.operation,e.collision_policy,e.publication_dependency_digest,EXISTS(SELECT 1 FROM public.postprocessing_plans p JOIN public.acquisition_files t ON t.acquisition_id=e.acquisition_id AND t.file_ref=e.file_ref AND t.plan_kind='execute' AND t.plan_operation_ref=e.id WHERE e.publication_work_item_id IS NOT NULL AND e.publication_fencing_token IS NOT NULL AND p.id=e.postprocessing_plan_id AND p.sealed_at IS NOT NULL AND p.plan_digest=e.publication_plan_digest AND t.spec_digest=e.publication_spec_digest) FROM public.file_operations e WHERE e.publication_permit_id=$1`, permit).Scan(&session, &operation, &collision, &dependency, &binding); err != nil {
		return protocol.Value{}, err
	}
	var dependencyValid bool
	if err := tx.QueryRowContext(ctx, `SELECT e.publication_dependency_digest='sha256:'||encode(sha256(convert_to('file-dependency:v1','UTF8')||decode('00','hex')||convert_to(COALESCE((SELECT string_agg(parent.file_ref||':'||r.evidence_digest,',' ORDER BY parent.file_ref COLLATE "C") FROM file_operations parent JOIN acquisition_files r ON r.acquisition_id=parent.acquisition_id AND r.file_ref=parent.file_ref WHERE parent.id=e.bound_video_operation_id),''),'UTF8')),'hex') AND NOT EXISTS(SELECT 1 FROM file_operations parent JOIN acquisition_files r ON r.acquisition_id=parent.acquisition_id AND r.file_ref=parent.file_ref WHERE parent.id=e.bound_video_operation_id AND r.outcome IS DISTINCT FROM 'succeeded') FROM file_operations e WHERE e.publication_permit_id=$1`, permit).Scan(&dependencyValid); err != nil {
		return protocol.Value{}, err
	}
	if !dependencyValid {
		return protocol.Value{}, fmt.Errorf("FILE_DEPENDENCY_PROOF_BINDING_INVALID")
	}
	if !binding {
		return protocol.Value{}, fmt.Errorf("FILE_ORIGINAL_PROOF_BINDING_INVALID")
	}
	// Recheck the frozen Operation/target and approved incumbent independently
	// of recorder success; accepted replacement is an immutable input source.
	// An overwrite of a target no approval names is the direct branch
	// (migration 024): the occupant, managed or not, is replaced and a
	// managed one is displaced when the successor's placement activates.
	var objectBinding bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.file_operations e JOIN public.file_operations o ON o.id=e.id JOIN public.acquisition_files t ON t.acquisition_id=e.acquisition_id AND t.file_ref=e.file_ref JOIN public.acquisitions i ON i.id=e.acquisition_id WHERE e.publication_permit_id=$1 AND o.operation=e.operation AND o.intent=e.intent AND o.collision_policy=e.collision_policy AND t.operation=e.operation AND t.intent=e.intent AND t.collision=e.collision_policy AND e.publication_frozen_objects->'source'->'location'->>'canonical_path'=o.source_path AND e.publication_frozen_objects->'target'->>'canonical_path'=o.target_path AND t.canonical_path=o.target_path AND e.publication_frozen_objects->'source'->'size'=to_jsonb(o.expected_size_bytes) AND (e.collision_policy<>'overwrite' OR (i.input->'delivery'->>'intent'='replace' AND EXISTS(SELECT 1 FROM public.delivery_replacements(i.input->'delivery') r(value) WHERE r.value->'expected_object'->'location'=e.publication_frozen_objects->'expected_target'->'location' AND r.value->'expected_object'->'size'=e.publication_frozen_objects->'expected_target'->'size')) OR public.delivery_direct_overwrite(i.input->'delivery',o.target_path)))`, permit).Scan(&objectBinding); err != nil {
		return protocol.Value{}, err
	}
	if !objectBinding {
		return protocol.Value{}, fmt.Errorf("FILE_FROZEN_OBJECT_BINDING_INVALID")
	}
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(publication_frozen_objects_canonical,''),COALESCE(publication_resolution_checkpoint_canonical,''),COALESCE(publication_closure_canonical,''),COALESCE(publication_resolution_canonical,''),publication_effect_class,publication_dispatch_state,publication_conflict_evidence IS NOT NULL,publication_step_proof_bytes FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&objects, &checkpoint, &closure, &resolution, &class, &state, &conflict, &stepRaw)
	if err != nil {
		return protocol.Value{}, err
	}
	if conflict || state != "closed" || class != "confirmed" {
		return protocol.Value{}, fmt.Errorf("FILE_SUCCESS_PROOF_UNAVAILABLE")
	}
	parse := func(raw, name string) (protocol.Value, error) {
		v, err := protocol.ParseStrict([]byte(raw))
		if err != nil {
			return v, err
		}
		if string(protocol.Canonical(v)) != raw {
			return v, fmt.Errorf("FILE_PROOF_BYTES_INVALID")
		}
		if name != "" {
			err = protocol.Validate(name, v)
		}
		return v, err
	}
	o, err := parse(objects, "")
	if err != nil {
		return protocol.Value{}, err
	}
	if err := validatePublicationObjects(o, operation, collision); err != nil {
		return protocol.Value{}, err
	}
	cp, err := parse(checkpoint, "FileCheckpointV1")
	if err != nil {
		return protocol.Value{}, err
	}
	cl, err := parse(closure, "ClosureV1")
	if err != nil {
		return protocol.Value{}, err
	}
	if cl.Object["kind"].String != "local_join" || cl.Object["executor_session"].String != session || session != cl.Object["boot_nonce"].String+":"+cl.Object["call_nonce"].String {
		return protocol.Value{}, fmt.Errorf("FILE_ORIGINAL_CLOSURE_BINDING_INVALID")
	}
	result, err := parse(resolution, "FileResolutionV1")
	if err != nil {
		return protocol.Value{}, err
	}
	if result.Object["effect_class"].String != "confirmed" || result.Object["checkpoint_digest"].String != protocol.Digest("file-checkpoint:v1", []byte(checkpoint)) || len(result.Object["closure_digests"].Array) != 1 || result.Object["closure_digests"].Array[0].String != protocol.Digest("file-closure:v1", []byte(closure)) {
		return protocol.Value{}, fmt.Errorf("FILE_RESOLUTION_BINDING_INVALID")
	}
	var steps map[string]string
	if err := json.Unmarshal(stepRaw, &steps); err != nil {
		return protocol.Value{}, err
	}
	covered := []string{}
	for _, step := range []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"} {
		slot := cp.Object[step]
		required := !(step == "source_cleanup" && operation != "move" || step == "reserve_version" && collision != "version" || (step == "stage_temp" || step == "temp_cleanup") && operation == "hardlink" && collision == "block")
		if required && slot.Object["state"].String == "not_required" || !required && slot.Object["state"].String != "not_required" {
			return protocol.Value{}, fmt.Errorf("FILE_REQUIRED_STEP_BINDING_INVALID")
		}
		if slot.Object["state"].String == "not_required" {
			continue
		}
		if slot.Object["state"].String != "confirmed" {
			return protocol.Value{}, fmt.Errorf("FILE_STEP_NOT_CONFIRMED")
		}
		proof, err := parse(steps[step], "FileStepProofV1")
		if err != nil {
			return protocol.Value{}, err
		}
		objectKey := "object"
		if step == "publish_target" {
			objectKey = "target"
		}
		if step == "stage_temp" || step == "reserve_version" || step == "publish_target" {
			if err := validatePublicationObservation(proof.Object[objectKey], step); err != nil {
				return protocol.Value{}, err
			}
		}
		if step == "publish_target" && (collision == "version" || collision == "overwrite") {
			if err := validatePublicationObservation(proof.Object["displaced"].Object["value"], "displaced"); err != nil {
				return protocol.Value{}, err
			}
		}
		if string(protocol.Canonical(proof)) != string(protocol.Canonical(slot.Object["proof"])) {
			return protocol.Value{}, fmt.Errorf("FILE_STEP_PROOF_BINDING_INVALID")
		}
		if step == "publish_target" && proof.Object["source_evidence_digest"].String != protocol.Digest("file-source-evidence:v1", protocol.Canonical(o.Object["source"])) {
			return protocol.Value{}, fmt.Errorf("FILE_SOURCE_PROOF_BINDING_INVALID")
		}
		if step == "source_cleanup" || step == "temp_cleanup" {
			mutation, err := canonicalPlanValue(map[string]any{"permit_id": permit, "step": step, "before": json.RawMessage(protocol.Canonical(proof.Object["before"])), "removed_at": slot.Object["observed_at"].String})
			if err != nil {
				return protocol.Value{}, err
			}
			if proof.Object["mutation_receipt_digest"].String != protocol.Digest("file-mutation-receipt:v1", []byte(mutation)) {
				return protocol.Value{}, fmt.Errorf("FILE_CLEANUP_MUTATION_BINDING_INVALID")
			}
		}
		if err := verifyPublicationStepBinding(step, collision, dependency, o, cp, proof); err != nil {
			return protocol.Value{}, err
		}
		covered = append(covered, step)
	}
	if len(covered) != len(cl.Object["covered_steps"].Array) {
		return protocol.Value{}, fmt.Errorf("FILE_CLOSURE_COVERAGE_INVALID")
	}
	for i, step := range covered {
		if cl.Object["covered_steps"].Array[i].String != step {
			return protocol.Value{}, fmt.Errorf("FILE_CLOSURE_COVERAGE_INVALID")
		}
	}
	return result, nil
}
