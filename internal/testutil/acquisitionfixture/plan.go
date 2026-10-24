package acquisitionfixture

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// PlanOperation is an original downstream fixture's frozen operation request.
// CommitOperations arranges the planning stage as owner, then invokes the real prepare stage as runtime.
// It never creates a sealed parent by inserting placeholder declarations.
type PlanOperation struct {
	ID, FileID, FileRef, Source, Target, Operation, Key string
	Collision                                           string
	Size                                                int64
	Fingerprint, Render, Config                         json.RawMessage
}

func CommitOperations(t *testing.T, tx *sql.Tx, artifact string, operations []PlanOperation) {
	t.Helper()
	ctx := context.Background()
	plan := "fixture-plan-" + artifact
	var root, digest string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT a.acquisition_id,a.version,i.admission_digest FROM artifacts a JOIN acquisitions i ON i.id=a.acquisition_id WHERE a.id=$1 AND a.state='verified'`, artifact).Scan(&root, &version, &digest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,input_digest,state) VALUES($1,$2,$3,$4,$5,'queued')`, plan, artifact, root, version, digest); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := tx.QueryRowContext(ctx, `SELECT work_item_create($1,'postprocessing_plan',$2,'postprocess_plan',$3,3,'{}'::jsonb,1::bigint,0::double precision,7::smallint)`, "fixture-plan-work-"+artifact, plan, "plan:"+plan).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE bansoko_runtime`); err != nil {
		t.Fatal(err)
	}
	var work, claimed string
	var token int64
	if err := tx.QueryRowContext(ctx, `SELECT id,owner_id,fencing_token FROM work_item_claim_due($1::double precision,ARRAY['postprocess_plan']::text[])`, time.Minute.Seconds()).Scan(&work, &claimed, &token); err != nil {
		t.Fatal(err)
	}
	if claimed != plan {
		t.Fatalf("fixture claim %s instead of %s", claimed, plan)
	}
	var input, capture []byte
	if err := tx.QueryRowContext(ctx, `SELECT i.input,jsonb_build_object('input',i.input,'artifact',to_jsonb(a),
 'manifest',jsonb_build_object('acquisition_id',i.id,'format_version',1,'manifest_digest',i.manifest_digest,'item_count',i.manifest_item_count,'sealed_at',i.manifest_sealed_at),
 'preparation_witness',CASE WHEN i.preparation_witness_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.preparation_witness_canonical_utf8,'witness',i.preparation_witness,'witness_digest',i.preparation_digest) END,
 'vars_protocol',jsonb_build_object('version',i.vars_evidence_version,'digest',i.vars_evidence_digest,'admission_digest',i.admission_digest),
 'vars_evaluation',CASE WHEN i.vars_evaluation_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.vars_evaluation_canonical_utf8,'record',i.vars_evaluation,'evidence_digest',i.vars_evidence_digest) END,
 'manifest_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id),'[]'::jsonb),
 'files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM artifact_files f WHERE f.artifact_id=a.id),'[]'::jsonb),
 'requirements',CASE WHEN i.requirements_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'manifest_digest',i.requirements_manifest_digest,'selection_digest',i.requirements_selection_digest,'evidence_digest',i.requirements_evidence_digest,'requirements_digest',i.requirements_digest,'item_count',i.requirements_item_count,'required_count',i.requirements_required_count,'sealed_at',i.requirements_sealed_at) END,
 'requirement_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'[]'::jsonb),
 'target_set',CASE WHEN i.target_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'requirements_digest',i.target_requirements_digest,'target_digest',i.target_digest,'sealed_at',i.target_sealed_at) END,
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL),'[]'::jsonb),'prepared_bindings','[]'::jsonb)
 FROM postprocessing_plans p JOIN artifacts a ON a.id=p.artifact_id JOIN acquisitions i ON i.id=p.acquisition_id WHERE p.id=$1`, plan).Scan(&input, &capture); err != nil {
		t.Fatal(err)
	}
	requirements := []any{}
	targets := []any{}
	items := []any{}
	ops := []any{}
	canonical := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p, err := protocol.ParseStrict(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(protocol.Canonical(p))
	}
	for _, o := range operations {
		if o.Collision == "" {
			o.Collision = "block"
		}
		requirements = append(requirements, map[string]any{"file_ref": o.FileRef, "requirement": "required", "reason_code": "selected", "selection_evidence": map[string]any{"path": o.Source}})
		spec := map[string]any{"file_ref": o.FileRef, "intent": "ensure_present", "operation": o.Operation, "canonical_path": o.Target, "collision": o.Collision, "source_cleanup": map[string]any{"mode": "none"}, "render_snapshot": o.Render}
		spec["spec_digest"] = protocol.Digest("acquisition-targets:v1", []byte(canonical(spec)))
		targets = append(targets, spec)
		items = append(items, map[string]any{"file_ref": o.FileRef, "kind": "execute", "target_file_ref": o.FileRef, "evidence": nil, "reason_code": nil, "diagnostic": map[string]any{}})
		ops = append(ops, map[string]any{"id": o.ID, "file_ref": o.FileRef, "artifact": artifact, "version": version, "file": o.FileID, "operation": o.Operation, "source": o.Source, "target": o.Target, "fingerprint": o.Fingerprint, "size": o.Size, "intent": "ensure_present", "collision": o.Collision, "render": o.Render, "config": o.Config, "overwrite": nil, "key": o.Key})
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	var receipt []byte
	if err := tx.QueryRowContext(ctx, `SELECT postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, plan, work, token, digest, input, canonical(requirements), canonical(targets), canonical(map[string]any{"items": items, "dependencies": []any{}}), raw, capture).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	var proof []byte
	if err := tx.QueryRowContext(ctx, `SELECT postprocessing_plan_commit_state($1,$2,$3)`, plan, work, token).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if len(proof) == 0 {
		t.Fatal("fixture T05 independent proof rejected legal graph")
	}
	if _, err := tx.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
}
