package application

import (
	"context"
)

// Only immutable accepted material and planning observations enter this binding.
// The one statement sees one snapshot. Current management metadata and legacy
// release-keyed targets are deliberately absent from the typed capture.
func captureTypedPlan(ctx context.Context, q preparedCaptureQuery, id string) (PreparedPlanCapture, error) {
	var c PreparedPlanCapture
	err := q.QueryRowContext(ctx, `SELECT p.id,p.version,p.input_digest,p.artifact_id,p.artifact_version,i.input->'config',
 jsonb_build_object('input',i.input,'artifact',to_jsonb(a),
 'manifest',jsonb_build_object('acquisition_id',i.id,'format_version',1,'manifest_digest',i.manifest_digest,'item_count',i.manifest_item_count,'sealed_at',i.manifest_sealed_at),
 'preparation_witness',CASE WHEN i.preparation_witness_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.preparation_witness_canonical_utf8,'witness',i.preparation_witness,'witness_digest',i.preparation_digest) END,
 'vars_protocol',jsonb_build_object('version',i.vars_evidence_version,'digest',i.vars_evidence_digest,'admission_digest',i.admission_digest),
 'vars_evaluation',CASE WHEN i.vars_evaluation_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.vars_evaluation_canonical_utf8,'record',i.vars_evaluation,'evidence_digest',i.vars_evidence_digest) END,
 'manifest_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id),'[]'::jsonb),
 'files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM artifact_files f WHERE f.artifact_id=a.id),'[]'::jsonb),
 'requirements',CASE WHEN i.requirements_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'manifest_digest',i.requirements_manifest_digest,'selection_digest',i.requirements_selection_digest,'evidence_digest',i.requirements_evidence_digest,'requirements_digest',i.requirements_digest,'item_count',i.requirements_item_count,'required_count',i.requirements_required_count,'sealed_at',i.requirements_sealed_at) END,
 'requirement_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'[]'::jsonb),
 'target_set',CASE WHEN i.target_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'requirements_digest',i.target_requirements_digest,'target_digest',i.target_digest,'sealed_at',i.target_sealed_at) END,
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL),'[]'::jsonb))
 FROM postprocessing_plans p JOIN artifacts a ON a.id=p.artifact_id AND a.acquisition_id=p.acquisition_id
 JOIN acquisitions i ON i.id=p.acquisition_id
 WHERE p.id=$1`, id).Scan(&c.PlanID, &c.PlanVersion, &c.PlanInputDigest, &c.ArtifactID, &c.ArtifactVersion, &c.Config, &c.Facts)
	return c, err
}
