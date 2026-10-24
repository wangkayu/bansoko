package migrations

import (
	fileapp "bansoko/internal/fileoperation/application"
	"bansoko/internal/workitems"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlanAtomicCommit(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprintf("early_%v", early), func(t *testing.T) { runPlanAtomicCommit(t, early, false, false) })
	}
	t.Run("dependency", func(t *testing.T) { runPlanAtomicCommit(t, false, true, false) })
	t.Run("failed_item", func(t *testing.T) { runPlanAtomicCommit(t, false, false, true) })
	t.Run("settlement_new", func(t *testing.T) { runPlanAtomicCommitWithSettlement(t, false, false, false, true) })
}

func runPlanAtomicCommit(t *testing.T, early, dependency, failedItem bool) {
	runPlanAtomicCommitWithSettlement(t, early, dependency, failedItem, false)
}

func runPlanAtomicCommitWithSettlement(t *testing.T, early, dependency, failedItem, newSettlement bool) {
	owner, db, store, recovery := seedTypedPlanRecovery(t)
	ctx := context.Background()
	var receipt []byte
	if err := db.QueryRow(`SELECT public.postprocessing_plan_requeue_interrupted($1,$2,$3,0::double precision)`, recovery.Owner.ID, recovery.ID, recovery.FencingToken).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(ctx, "atomic-plan", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var input, capture []byte
	var digest string
	if err := db.QueryRow(`SELECT i.input,p.input_digest,jsonb_build_object('input',i.input,'artifact',to_jsonb(a),
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
 FROM postprocessing_plans p JOIN artifacts a ON a.id=p.artifact_id JOIN acquisitions i ON i.id=p.acquisition_id WHERE p.id=$1`, c.Owner.ID).Scan(&input, &digest, &capture); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id,file_ref,source_path,size_bytes,fingerprint FROM artifact_files WHERE artifact_id='plan-artifact' ORDER BY file_ref`)
	if err != nil {
		t.Fatal(err)
	}
	requirements := []any{}
	targets := []any{}
	items := []any{}
	ops := []any{}
	for rows.Next() {
		var id, ref, path string
		var size int64
		var fingerprint json.RawMessage
		if err := rows.Scan(&id, &ref, &path, &size, &fingerprint); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		operation := "atomic-op-" + ref
		target := "/library/" + ref + ".mkv"
		requirements = append(requirements, map[string]any{"file_ref": ref, "requirement": "required", "reason_code": "selected", "selection_evidence": map[string]any{"path": path}})
		spec := map[string]any{"file_ref": ref, "intent": "ensure_present", "operation": "copy", "canonical_path": target, "collision": "block", "source_cleanup": map[string]any{"mode": "none"}, "render_snapshot": map[string]any{}}
		spec["spec_digest"] = acqDomainDigest("acquisition-targets:v1", []byte(canonicalJSON(t, spec)))
		targets = append(targets, spec)
		items = append(items, map[string]any{"file_ref": ref, "kind": "execute", "target_file_ref": ref, "evidence": nil, "reason_code": nil, "diagnostic": map[string]any{}})
		ops = append(ops, map[string]any{"id": operation, "file_ref": ref, "artifact": "plan-artifact", "version": int64(2), "file": id, "operation": "copy", "source": path, "target": target, "fingerprint": fingerprint, "size": size, "intent": "ensure_present", "collision": "block", "render": map[string]any{}, "config": map[string]any{}, "overwrite": nil, "key": "atomic-key-" + ref})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	opRaw, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	reqRaw := canonicalJSON(t, requirements)
	targetRaw := canonicalJSON(t, targets)
	if failedItem {
		item := items[1].(map[string]any)
		item["kind"] = "failed"
		item["target_file_ref"] = nil
		item["reason_code"] = "PLAN_TARGET_UNAVAILABLE"
		targets = targets[:1]
		ops = ops[:1]
		targetRaw = canonicalJSON(t, targets)
		opRaw, err = json.Marshal(ops)
		if err != nil {
			t.Fatal(err)
		}
	}
	edges := []any{}
	if dependency {
		parent := items[0].(map[string]any)["file_ref"].(string)
		child := items[1].(map[string]any)["file_ref"].(string)
		edges = append(edges, map[string]any{"file_ref": child, "depends_on_ref": parent, "dependency_kind": "subtitle_video"})
		ops[1].(map[string]any)["bound_video"] = ops[0].(map[string]any)["id"]
		binding := map[string]any{"state": "bound", "bound_video_file_ref": parent, "bound_video_operation_id": ops[0].(map[string]any)["id"], "locked_directory": "/library", "locked_basename": "fixture", "locked_extension": ".mkv", "method": "unique_video", "confidence": json.RawMessage(`0.6`)}
		ops[1].(map[string]any)["render"] = map[string]any{"binding": binding}
		ops[1].(map[string]any)["binding_snapshot"] = map[string]any{"version": "subtitle-binding-1", "method": "unique_video", "confidence": json.RawMessage(`0.6`), "diagnostics": []any{}, "locked": map[string]any{"video_file_ref": parent, "video_operation_id": ops[0].(map[string]any)["id"], "directory": "/library", "basename": "fixture", "extension": ".mkv"}}
		business := map[string]any{}
		for k, v := range binding {
			business[k] = v
		}
		delete(business, "bound_video_operation_id")
		business["schema_version"] = 1
		business["confidence"] = "0.6"
		targets[1].(map[string]any)["render_snapshot"] = map[string]any{"binding": business}
		spec := map[string]any{}
		for k, v := range targets[1].(map[string]any) {
			if k != "spec_digest" {
				spec[k] = v
			}
		}
		targets[1].(map[string]any)["spec_digest"] = acqDomainDigest("acquisition-targets:v1", []byte(canonicalJSON(t, spec)))
		targetRaw = canonicalJSON(t, targets)
		opRaw, err = json.Marshal(ops)
		if err != nil {
			t.Fatal(err)
		}
	}
	// The SQL fixture includes a required subtitle: provide the same complete
	// decision set as the production builder before any fault-point injection.
	var captureObject map[string]json.RawMessage
	if err := json.Unmarshal(capture, &captureObject); err != nil {
		t.Fatal(err)
	}
	var capturedFiles []map[string]any
	if err := json.Unmarshal(captureObject["files"], &capturedFiles); err != nil {
		t.Fatal(err)
	}
	preparedBindings := []map[string]any{}
	for _, file := range capturedFiles {
		if file["media_kind"] != "subtitle" || file["subtitle_binding_snapshot"] != nil {
			continue
		}
		decision := map[string]any{"version": "subtitle-binding-1", "diagnostics": []any{}}
		row := map[string]any{"file": file["id"], "state": "missing", "bound_ref": nil, "decision": decision}
		if dependency {
			row["state"], row["bound_ref"] = "bound", items[0].(map[string]any)["file_ref"]
			decision["method"], decision["confidence"] = "unique_video", json.RawMessage(`0.6`)
		}
		preparedBindings = append(preparedBindings, row)
	}
	captureObject["prepared_bindings"], err = json.Marshal(preparedBindings)
	if err != nil {
		t.Fatal(err)
	}
	capture, err = json.Marshal(captureObject)
	if err != nil {
		t.Fatal(err)
	}
	planRaw := canonicalJSON(t, map[string]any{"items": items, "dependencies": edges})
	var earlyBefore []byte
	if early {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`UPDATE acquisition_files f SET requirement=x.requirement,requirement_reason_code=x.reason_code,selection_evidence=x.selection_evidence FROM postprocessing_plans p CROSS JOIN jsonb_to_recordset($2::jsonb) x(file_ref text,requirement text,reason_code text,selection_evidence jsonb) WHERE p.id=$1 AND f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref`, c.Owner.ID, reqRaw); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE acquisitions a SET requirements_manifest_digest=a.manifest_digest,
 requirements_selection_digest='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(plan_selection_bytes(a.input_canonical_utf8),'UTF8')),'hex'),
 requirements_evidence_digest='sha256:'||encode(sha256(convert_to('acquisition-requirements:v1','UTF8')||decode('00','hex')||convert_to(plan_evidence_bytes($2),'UTF8')),'hex'),
 requirements_digest=$3, requirements_item_count=2, requirements_required_count=2, requirements_sealed_at=CURRENT_TIMESTAMP
 WHERE a.id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`, c.Owner.ID, reqRaw, acqDomainDigest("acquisition-requirements:v1", []byte(reqRaw))); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE acquisition_files f SET spec_digest=x.spec_digest,intent=x.intent,operation=x.operation,canonical_path=x.canonical_path,collision=x.collision,target_source_cleanup=x.source_cleanup,render_snapshot=x.render_snapshot FROM postprocessing_plans p CROSS JOIN jsonb_to_recordset($2::jsonb) x(file_ref text,spec_digest text,intent text,operation text,canonical_path text,collision text,source_cleanup jsonb,render_snapshot jsonb) WHERE p.id=$1 AND f.acquisition_id=p.acquisition_id AND f.file_ref=x.file_ref`, c.Owner.ID, targetRaw); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE acquisitions SET target_requirements_digest=$2, target_digest=$3, target_sealed_at=CURRENT_TIMESTAMP WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`, c.Owner.ID, acqDomainDigest("acquisition-requirements:v1", []byte(reqRaw)), acqDomainDigest("acquisition-targets:v1", []byte(targetRaw))); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(`SELECT jsonb_build_object('requirements',jsonb_build_object('acquisition_id',a.id,'manifest_digest',a.requirements_manifest_digest,'selection_digest',a.requirements_selection_digest,'evidence_digest',a.requirements_evidence_digest,'requirements_digest',a.requirements_digest,'item_count',a.requirements_item_count,'required_count',a.requirements_required_count,'sealed_at',a.requirements_sealed_at),'requirement_files',(SELECT jsonb_agg(jsonb_build_object('acquisition_id',f.acquisition_id,'file_ref',f.file_ref,'requirement',f.requirement,'reason_code',f.requirement_reason_code,'selection_evidence',f.selection_evidence) ORDER BY f.file_ref) FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'target_set',jsonb_build_object('acquisition_id',a.id,'requirements_digest',a.target_requirements_digest,'target_digest',a.target_digest,'sealed_at',a.target_sealed_at),'targets',(SELECT jsonb_agg(jsonb_build_object('acquisition_id',f.acquisition_id,'file_ref',f.file_ref,'required_tag','required','spec_digest',f.spec_digest,'intent',f.intent,'operation',f.operation,'canonical_path',f.canonical_path,'collision',f.collision,'source_cleanup',f.target_source_cleanup,'render_snapshot',f.render_snapshot) ORDER BY f.file_ref) FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL)) FROM postprocessing_plans p JOIN acquisitions a ON a.id=p.acquisition_id WHERE p.id=$1`, c.Owner.ID).Scan(&earlyBefore); err != nil {
			t.Fatal(err)
		}
		var captureMap, earlyMap map[string]json.RawMessage
		if err := json.Unmarshal(capture, &captureMap); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(earlyBefore, &earlyMap); err != nil {
			t.Fatal(err)
		}
		for k, v := range earlyMap {
			captureMap[k] = v
		}
		for _, key := range []string{"manifest_files", "requirement_files", "targets"} {
			var projected []byte
			if err := owner.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C"),'[]'::jsonb) FROM acquisition_files f WHERE f.acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND ($2 <> 'requirement_files' OR f.requirement IS NOT NULL) AND ($2 <> 'targets' OR f.spec_digest IS NOT NULL)`, c.Owner.ID, key).Scan(&projected); err != nil {
				t.Fatal(err)
			}
			captureMap[key] = json.RawMessage(projected)
		}
		capture, err = json.Marshal(captureMap)
		if err != nil {
			t.Fatal(err)
		}
	}
	if early {
		t.Run("early_header_corruption_rejected_zero_write", func(t *testing.T) {
			for _, statement := range []string{`UPDATE acquisitions SET requirements_selection_digest='sha256:'||repeat('e',64) WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`, `UPDATE acquisitions SET target_digest='sha256:'||repeat('e',64) WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`} {
				tx, err := owner.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				before := graphSnapshot(t, owner)
				if _, err := tx.Exec(statement, c.Owner.ID); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				err = tx.QueryRow(`SELECT postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, reqRaw, targetRaw, planRaw, opRaw, capture).Scan(&receipt)
				if err == nil {
					tx.Rollback()
					t.Fatal("corrupt early set accepted")
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, graphSnapshot(t, owner)) {
					t.Fatal("early rejection changed graph")
				}
			}
		})
	}
	t.Run("extra_operation_rejected_zero_write", func(t *testing.T) {
		before := graphSnapshot(t, owner)
		extra := append(append([]any(nil), ops...), map[string]any{"id": "extra-unbound-operation"})
		raw, err := json.Marshal(extra)
		if err != nil {
			t.Fatal(err)
		}
		err = db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, reqRaw, targetRaw, planRaw, raw, capture).Scan(&receipt)
		if err == nil {
			t.Fatal("extra unbound Operation accepted by typed commit")
		}
		if !bytes.Equal(before, graphSnapshot(t, owner)) {
			t.Fatal("rejected extra Operation changed graph")
		}
	})
	if t.Failed() {
		return
	}
	t.Run("noncanonical_requirements_rejected_zero_write", func(t *testing.T) {
		before := graphSnapshot(t, owner)
		err := db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, " "+reqRaw, targetRaw, planRaw, opRaw, capture).Scan(&receipt)
		if err == nil {
			t.Fatal("noncanonical requirements bytes accepted by typed commit")
		}
		if !bytes.Equal(before, graphSnapshot(t, owner)) {
			t.Fatal("noncanonical rejection changed graph")
		}
	})
	for _, field := range []string{"targets", "plan"} {
		t.Run("noncanonical_"+field+"_rejected_zero_write", func(t *testing.T) {
			before := graphSnapshot(t, owner)
			target, plan := targetRaw, planRaw
			if field == "targets" {
				target = " " + target
			} else {
				plan = plan + "\n"
			}
			err := db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, reqRaw, target, plan, opRaw, capture).Scan(&receipt)
			if err == nil {
				t.Fatal("noncanonical bytes accepted")
			}
			if !bytes.Equal(before, graphSnapshot(t, owner)) {
				t.Fatal("noncanonical rejection changed graph")
			}
		})
	}
	if t.Failed() {
		return
	}
	for _, table := range []string{"acquisition_files", "file_operations", "postprocessing_plans", "work_items"} {
		t.Run("rollback_"+table, func(t *testing.T) {
			if _, err := owner.Exec(`CREATE FUNCTION s3_inject_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'S3_INJECTED_ROLLBACK'; END $$`); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(`CREATE TRIGGER s3_fault BEFORE INSERT OR UPDATE ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION s3_inject_fault()`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := owner.Exec(`DROP TRIGGER IF EXISTS s3_fault ON ` + table + `; DROP FUNCTION IF EXISTS s3_inject_fault()`); err != nil {
					t.Error(err)
				}
			}()
			before := graphSnapshot(t, owner)
			err := db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, reqRaw, targetRaw, planRaw, opRaw, capture).Scan(&receipt)
			if err == nil || !strings.Contains(err.Error(), "S3_INJECTED_ROLLBACK") {
				t.Fatalf("fault not reached: %v", err)
			}
			if !bytes.Equal(before, graphSnapshot(t, owner)) {
				t.Fatal("rollback changed complete graph")
			}
		})
	}
	if t.Failed() {
		return
	}
	var settleIDBefore, settleIDAfter string
	var settleBudgetBefore, settleBudgetAfter int
	if err := owner.QueryRow(`SELECT id,max_attempts FROM work_items WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND kind='acquisition_settle' AND state IN ('available','leased')`, c.Owner.ID).Scan(&settleIDBefore, &settleBudgetBefore); err != nil {
		t.Fatal(err)
	}
	if newSettlement {
		if _, err := owner.Exec(`DELETE FROM work_items WHERE id=$1`, settleIDBefore); err != nil {
			t.Fatal(err)
		}
	}
	var diagnosticBefore, diagnosticAfter []byte
	if err := owner.QueryRow(`SELECT jsonb_build_object('code',last_error_code,'reason',last_error_reason) FROM work_item_history WHERE id=$1`, c.ID).Scan(&diagnosticBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, digest, input, reqRaw, targetRaw, planRaw, opRaw, capture).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if early {
		var after []byte
		if err := owner.QueryRow(`SELECT jsonb_build_object('requirements',jsonb_build_object('acquisition_id',a.id,'manifest_digest',a.requirements_manifest_digest,'selection_digest',a.requirements_selection_digest,'evidence_digest',a.requirements_evidence_digest,'requirements_digest',a.requirements_digest,'item_count',a.requirements_item_count,'required_count',a.requirements_required_count,'sealed_at',a.requirements_sealed_at),'requirement_files',(SELECT jsonb_agg(jsonb_build_object('acquisition_id',f.acquisition_id,'file_ref',f.file_ref,'requirement',f.requirement,'reason_code',f.requirement_reason_code,'selection_evidence',f.selection_evidence) ORDER BY f.file_ref) FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'target_set',jsonb_build_object('acquisition_id',a.id,'requirements_digest',a.target_requirements_digest,'target_digest',a.target_digest,'sealed_at',a.target_sealed_at),'targets',(SELECT jsonb_agg(jsonb_build_object('acquisition_id',f.acquisition_id,'file_ref',f.file_ref,'required_tag','required','spec_digest',f.spec_digest,'intent',f.intent,'operation',f.operation,'canonical_path',f.canonical_path,'collision',f.collision,'source_cleanup',f.target_source_cleanup,'render_snapshot',f.render_snapshot) ORDER BY f.file_ref) FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL)) FROM postprocessing_plans p JOIN acquisitions a ON a.id=p.acquisition_id WHERE p.id=$1`, c.Owner.ID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(earlyBefore, after) {
			t.Fatal("plan finish rewrote early sealed sets")
		}
	}
	if err := owner.QueryRow(`SELECT jsonb_build_object('code',last_error_code,'reason',last_error_reason) FROM work_item_history WHERE id=$1`, c.ID).Scan(&diagnosticAfter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diagnosticBefore, diagnosticAfter) {
		t.Fatal("plan ordinary finish changed retained diagnostic history")
	}
	if err := owner.QueryRow(`SELECT id,max_attempts FROM work_items WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND kind='acquisition_settle' AND state IN ('available','leased')`, c.Owner.ID).Scan(&settleIDAfter, &settleBudgetAfter); err != nil {
		t.Fatal(err)
	}
	if newSettlement {
		if settleIDAfter == settleIDBefore || settleBudgetAfter != 5 {
			t.Fatalf("new settlement id=%s budget=%d", settleIDAfter, settleBudgetAfter)
		}
	} else if settleIDAfter != settleIDBefore || settleBudgetAfter != settleBudgetBefore {
		t.Fatal("EnsureSettlement reset reused identity/budget")
	}
	var state, work string
	var seal bool
	var itemCount, operations, fileWork int
	if err := owner.QueryRow(`SELECT p.state,w.state,p.sealed_at IS NOT NULL,(SELECT count(*) FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.plan_kind IS NOT NULL),(SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=p.id),(SELECT count(*) FROM work_item_history fw JOIN file_operations o ON o.id=fw.owner_id WHERE o.postprocessing_plan_id=p.id) FROM postprocessing_plans p JOIN work_item_history w ON w.id=$1 WHERE p.id=$2`, c.ID, c.Owner.ID).Scan(&state, &work, &seal, &itemCount, &operations, &fileWork); err != nil {
		t.Fatal(err)
	}
	wantWork := len(ops)
	if dependency {
		wantWork--
	}
	if state != "succeeded" || work != "succeeded" || !seal || itemCount != len(items) || operations != len(ops) || fileWork != wantWork {
		t.Fatalf("state=%s work=%s seal=%v items=%d operations=%d fileWork=%d", state, work, seal, itemCount, operations, fileWork)
	}
	ports := noEffectPlanPorts{t}
	service, err := fileapp.NewService(db, ports, ports)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.PostProcessingPlanHandler().ObserveRecovery(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.CommittedClaim == nil {
		t.Fatalf("complete receipt not confirmed: %+v", confirmed)
	}
	t.Run("repeat_unknown_commit_observation_zero_write", func(t *testing.T) {
		before := graphSnapshot(t, owner)
		for i := 0; i < 3; i++ {
			result, err := service.PostProcessingPlanHandler().ObserveRecovery(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if result.CommittedClaim == nil {
				t.Fatal("complete lost receipt not confirmed")
			}
		}
		if !bytes.Equal(before, graphSnapshot(t, owner)) {
			t.Fatal("commit observation wrote graph")
		}
	})
	var proof []byte
	if err := db.QueryRow(`SELECT public.postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if len(proof) == 0 {
		t.Fatal("complete committed graph was not confirmed")
	}
	t.Run("downstream_advanced_and_settlement_consumed", func(t *testing.T) {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`UPDATE file_operations SET version=version+1 WHERE postprocessing_plan_id=$1`, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE acquisitions SET checked_revision=facts_revision WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM work_items WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND kind='acquisition_settle'`, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(`SELECT postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
			t.Fatal(err)
		}
		if len(proof) == 0 {
			t.Fatal("legitimate downstream advancement rejected")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	})
	if err := db.QueryRow(`SELECT public.postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken-1).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if len(proof) != 0 {
		t.Fatal("stale token borrowed committed generation")
	}
	if dependency {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`UPDATE file_operations SET bound_video_operation_id=NULL WHERE postprocessing_plan_id=$1`, c.Owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(`SELECT postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
			t.Fatal(err)
		}
		if len(proof) != 0 {
			t.Fatal("dependency proof accepted missing predecessor Operation binding")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	if failedItem {
		for _, column := range []string{"evidence_digest", "effect_class", "delivery_source_cleanup", "receipt"} {
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"evidence_digest": "'sha256:'||repeat('e',64)", "effect_class": "'unknown'", "delivery_source_cleanup": "'uncertain'", "receipt": "'{}'::jsonb"}
			if _, err := tx.Exec(`UPDATE acquisition_files SET `+column+`=`+values[column]+` WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND outcome IS NOT NULL`, c.Owner.ID); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.QueryRow(`SELECT postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if len(proof) != 0 {
				tx.Rollback()
				t.Fatalf("failed result proof accepted corrupt %s", column)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mutation := range []string{
		`UPDATE work_items SET input_snapshot='{}' WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND kind='acquisition_settle'`,
		`DELETE FROM work_items WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND kind='acquisition_settle'`,
		`DELETE FROM work_items WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`,
		`UPDATE work_items SET input_snapshot=input_snapshot||'{"extra":true}' WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`,
		`UPDATE work_items SET retention_days=retention_days+1 WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`,
		`UPDATE file_operations SET frozen_config_snapshot='{"changed":true}' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET frozen_render_snapshot='{"changed":true}' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET target_path=target_path||'.changed' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET causation_type='changed' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET correlation_id='changed' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET causation_id='changed' WHERE postprocessing_plan_id=$1`,
		`UPDATE acquisition_files SET plan_diagnostic='{"changed":true}'::jsonb WHERE acquisition_id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1) AND plan_kind IS NOT NULL`,
		`UPDATE acquisitions SET requirements_selection_digest='sha256:'||repeat('e',64) WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`,
		`UPDATE acquisitions SET requirements_evidence_digest='sha256:'||repeat('e',64) WHERE id=(SELECT acquisition_id FROM postprocessing_plans WHERE id=$1)`,
		`UPDATE file_operations SET file_ready_at=file_ready_at+interval '1 second',file_wall_until=file_wall_until+interval '1 second' WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET file_max_attempts=file_max_attempts+1 WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET file_attempt_timeout_seconds=file_attempt_timeout_seconds+1 WHERE postprocessing_plan_id=$1`,
		`UPDATE file_operations SET file_wall_until=file_wall_until+interval '1 second' WHERE postprocessing_plan_id=$1`,
		`UPDATE work_items SET input_snapshot='{}' WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`,
		`UPDATE work_items SET max_attempts=max_attempts+1 WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`,
		`UPDATE work_item_outcomes SET fencing_token=fencing_token+1 WHERE owner_type='postprocessing_plan' AND owner_id=$1`,
	} {
		beforeMutation := graphSnapshot(t, owner)
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, mutation, c.Owner.ID); err != nil {
			tx.Rollback()
			if strings.Contains(mutation, "topological_rank") && strings.Contains(err.Error(), "PLAN_DECLARATIONS_SEALED") {
				continue
			} // independent immutable guard rejects this single-field corruption
			if strings.Contains(mutation, "UPDATE file_operations SET file_") && strings.Contains(err.Error(), "FILE_ATTEMPT_BUDGET_IMMUTABLE") {
				if !bytes.Equal(beforeMutation, graphSnapshot(t, owner)) {
					t.Fatal("immutable budget rejection changed graph")
				}
				continue
			} // The budget guard rejects the frozen-budget corruption before the proof reader.
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT public.postprocessing_plan_commit_state($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&proof); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if len(proof) != 0 {
			tx.Rollback()
			t.Fatalf("incomplete proof accepted: %s", mutation)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
