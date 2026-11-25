package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"bansoko/internal/testutil/acquisitionfixture"
)

// populatedBusinessSnapshot returns the v2 shape an installed 0.2.x database
// holds. Its values otherwise match the current fixture, so upgrading it must
// remove only the program references and advance the schema marker.
func populatedBusinessSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	business := queuedBusiness(t)
	raw, err := json.Marshal(business)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	historicalBusinessSnapshot(t, db, document)
	return canonicalJSON(t, document)
}

func seedBusinessCandidate(t *testing.T, db *sql.DB, id string, activated sql.NullString, status, business string) {
	t.Helper()
	ctx := context.Background()
	var draft map[string]any
	if business == "" {
		draft = map[string]any{"origin": "custom", "origin_entry_ref": nil, "title": id, "instance_label": id}
	} else {
		draft = map[string]any{"origin": "custom", "origin_entry_ref": nil, "title": id, "instance_label": id,
			"business": json.RawMessage(business), "business_digest": ""}
	}
	if business != "" {
		var digest string
		if err := db.QueryRowContext(ctx, `SELECT 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb),'UTF8'),'sha256'),'hex')`, business).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		draft["business_digest"] = digest
	}
	body := canonicalJSON(t, draft)
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
 id,origin,origin_entry_ref,title,instance_label,draft,draft_digest,
 activated_managed_anime_id,status,last_activity,retention_until)
VALUES ($1,'custom',NULL,$1,$1,$2::jsonb,
 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($2::jsonb),'UTF8'),'sha256'),'hex'),
 $3,$4,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+INTERVAL '30 days')`, id, body, activated, status); err != nil {
		t.Fatalf("seed candidate %s: %v", id, err)
	}
}

func seedBusinessRevision(t *testing.T, db *sql.DB, candidateID, business string) {
	t.Helper()
	keys := revisionSectionsCurrent
	effectiveDigest := acqDigest("d")
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_config_candidate_revisions(
 candidate_id,revision,draft,draft_digest,effective,section_versions,section_schemas,
 effective_digest,parser_policy,parser_policy_digest,business_snapshot,business_digest)
SELECT c.id,1,c.draft,c.draft_digest,$2::jsonb,$3::jsonb,$4::jsonb,$5,$6::jsonb,$7,$8::jsonb,
 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($8::jsonb),'UTF8'),'sha256'),'hex')
FROM anime_config_candidates c WHERE c.id=$1`, candidateID, keys.effective, keys.versions, keys.schemas,
		effectiveDigest, keys.parserPolicy, sha256Fixture("f"), business); err != nil {
		t.Fatalf("seed revision for %s: %v", candidateID, err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE anime_config_candidates SET current_revision=1,current_revision_digest=$2 WHERE id=$1`, candidateID, effectiveDigest); err != nil {
		t.Fatalf("point %s at its revision: %v", candidateID, err)
	}
}

func TestRetireNamingVarsUpgradesPopulatedBusinessRowsAndAutomaticAdmission(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	defer db.Close()
	installMigrationsThrough(t, db, "162_notification_episode_span_facts.sql")

	seedAcceptableRelease(t, db, "owner")
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id,year,season) VALUES('anime-owner',2024,2)`); err != nil {
		t.Fatal(err)
	}
	business := populatedBusinessSnapshot(t, db)
	seedBusinessCandidate(t, db, "candidate-working", sql.NullString{}, "working", business)
	seedBusinessRevision(t, db, "candidate-working", business)
	seedBusinessCandidate(t, db, "candidate-dormant", sql.NullString{String: "anime-owner", Valid: true}, "dormant", business)
	seedBusinessRevision(t, db, "candidate-dormant", business)
	seedBusinessCandidate(t, db, "candidate-identity", sql.NullString{}, "working", "")
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id,candidate_id,revision,business_snapshot,business_digest,business_version,parser_kind,parser_source_default_version,parser_version)
SELECT 'anime-owner','candidate-working',1,$1::jsonb,
 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb),'UTF8'),'sha256'),'hex'),7,'regex',1,1`, business); err != nil {
		t.Fatal(err)
	}
	var identityBefore []byte
	if err := db.QueryRowContext(ctx, `SELECT draft FROM anime_config_candidates WHERE id='candidate-identity'`).Scan(&identityBefore); err != nil {
		t.Fatal(err)
	}
	var dormantBefore []byte
	if err := db.QueryRowContext(ctx, `SELECT draft FROM anime_config_candidates WHERE id='candidate-dormant'`).Scan(&dormantBefore); err != nil {
		t.Fatal(err)
	}

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade populated 162: %v", err)
	}

	var ownerBody []byte
	var ownerDigest string
	var ownerVersion int64
	if err := db.QueryRowContext(ctx, `SELECT business_snapshot,business_digest,business_version FROM anime_business_configs WHERE managed_anime_id='anime-owner'`).Scan(&ownerBody, &ownerDigest, &ownerVersion); err != nil {
		t.Fatal(err)
	}
	var owner map[string]any
	if err := json.Unmarshal(ownerBody, &owner); err != nil {
		t.Fatal(err)
	}
	if owner["schema_version"] != "business-snapshot-v3" || ownerVersion != 8 {
		t.Fatalf("upgraded owner schema=%v version=%d", owner["schema_version"], ownerVersion)
	}
	if _, found := owner["programs"]; found {
		t.Fatal("upgraded owner still has program references")
	}
	var recomputed string
	if err := db.QueryRowContext(ctx, `SELECT 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json($1::jsonb),'UTF8'),'sha256'),'hex')`, ownerBody).Scan(&recomputed); err != nil {
		t.Fatal(err)
	}
	if ownerDigest != recomputed {
		t.Fatalf("owner digest %q, want canonical digest %q", ownerDigest, recomputed)
	}

	rewriteExpect(t, db, "163 populated", "working candidate and revision use v3 with mirrored digests", `
SELECT c.draft->'business'->>'schema_version'='business-snapshot-v3'
 AND NOT (c.draft->'business' ? 'programs')
 AND c.draft->>'business_digest'=r.business_digest
 AND c.draft_digest='sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(c.draft),'UTF8'),'sha256'),'hex')
 AND r.business_snapshot->>'schema_version'='business-snapshot-v3'
 AND NOT (r.business_snapshot ? 'programs')
 AND r.business_digest='sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(r.business_snapshot),'UTF8'),'sha256'),'hex')
 AND r.draft->'business'->>'schema_version'='business-snapshot-v3'
 AND c.current_revision_digest=r.effective_digest
FROM anime_config_candidates c JOIN anime_config_candidate_revisions r
 ON r.candidate_id=c.id AND r.revision=c.current_revision
WHERE c.id='candidate-working'`)
	rewriteExpect(t, db, "163 populated", "dormant candidate and revision retain their old shape", `
SELECT c.draft->'business'->>'schema_version'='business-snapshot-v2'
 AND c.draft->'business' ? 'programs'
 AND r.business_snapshot->>'schema_version'='business-snapshot-v2'
 AND r.business_snapshot ? 'programs'
FROM anime_config_candidates c JOIN anime_config_candidate_revisions r
 ON r.candidate_id=c.id AND r.revision=1
WHERE c.id='candidate-dormant'`)
	var identityAfter, dormantAfter []byte
	if err := db.QueryRowContext(ctx, `SELECT (SELECT draft FROM anime_config_candidates WHERE id='candidate-identity'),(SELECT draft FROM anime_config_candidates WHERE id='candidate-dormant')`).Scan(&identityAfter, &dormantAfter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityBefore, identityAfter) || !bytes.Equal(dormantBefore, dormantAfter) {
		t.Fatal("migration rewrote an identity-only or dormant candidate")
	}

	// Admission reads the upgraded owner version and digest. The input carries
	// explicit owner year/season and none of the retired Vars members.
	var parserVersion, rulesVersion, subgroupsVersion, metadataVersion int64
	if err := db.QueryRowContext(ctx, `SELECT b.parser_version,b.rules_version,b.subgroups_version,m.version
FROM anime_business_configs b JOIN anime_metadata m ON m.managed_anime_id=b.managed_anime_id
WHERE b.managed_anime_id='anime-owner'`).Scan(&parserVersion, &rulesVersion, &subgroupsVersion, &metadataVersion); err != nil {
		t.Fatal(err)
	}
	p := validQueued(t, "acq-after-upgrade", "key-after-upgrade", "owner")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["source"].(map[string]any)["kind"] = "automatic_source"
	media := input["media"].(map[string]any)
	media["year"] = map[string]any{"state": "known", "value": 2024, "reason_code": nil}
	media["season"] = map[string]any{"state": "known", "value": 2, "reason_code": nil}
	input["config"].(map[string]any)["effective_digest"] = ownerDigest
	input["config"].(map[string]any)["rules_version"] = map[string]any{"state": "known", "value": rulesVersion, "reason_code": nil}
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	p.BusinessSnapshot = string(ownerBody)
	mutateQualification(t, &p, func(q map[string]any) {
		q["config"] = map[string]any{"kind": "owner", "managed_anime_ref": "anime-owner", "config_version": ownerVersion, "config_digest": ownerDigest}
		management := member(q, "management")
		management["subscription_policy"] = "automatic_active"
		management["expected_metadata_version"] = metadataVersion
		management["expected_parser_policy_version"] = parserVersion
		management["expected_rules_owner_version"] = rulesVersion
		management["expected_subgroups_owner_version"] = subgroupsVersion
	})
	accepted, err := callQueuedAccept(t, db, p)
	if err != nil {
		t.Fatalf("automatic admission after the owner upgrade: %v", err)
	}
	if accepted["status"] != "accepted" {
		t.Fatalf("automatic admission result: %v", accepted)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT input_canonical_utf8 FROM acquisitions WHERE id=$1`, p.AcquisitionID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(stored), []byte(p.InputCanonical)) {
		t.Fatal("accepted input bytes changed while being stored")
	}
}

// Keep the terminal helper local to the populated upgrade fixture: the old
// acquisition contract is intentionally constructed only before migration 163.
func terminalHistoricalAcquisition(t *testing.T, db *sql.DB, p queuedParams, state string, compact bool) {
	t.Helper()
	now := time.Now().UTC()
	terminalSchema := "native_terminal_v1"
	if _, err := db.ExecContext(context.Background(), `UPDATE acquisitions SET state=$2,terminal_at=$3::timestamptz,terminal_schema=$4,terminal_receipt='{}',
 detail_until=$3::timestamptz+detail_retention_seconds*INTERVAL '1 second',record_state=$5,
 compacted_at=CASE WHEN $5='compact' THEN $3::timestamptz ELSE NULL END,
 compact_trigger=CASE WHEN $5='compact' THEN 'detail_expired' ELSE NULL END
 WHERE id=$1`, p.AcquisitionID, state, now.Add(time.Hour), terminalSchema, map[bool]string{true: "compact", false: "full"}[compact]); err != nil {
		t.Fatalf("terminal historical acquisition %s: %v", p.AcquisitionID, err)
	}
}

// acceptHistoricalEvaluation creates the complete frozen Vars evidence a 0.2.x
// running or full acquisition could still hold. Migration 163 must drop only
// the obsolete side columns, never the accepted input or its admission digest.
func acceptHistoricalEvaluation(t *testing.T, db *sql.DB, tag string) queuedParams {
	t.Helper()
	seedAcceptableRelease(t, db, tag)
	p := validHistoricalQueued(t, db, "acq-"+tag, "key-"+tag, tag)
	var qualification map[string]any
	if err := json.Unmarshal([]byte(p.Qualification), &qualification); err != nil {
		t.Fatal(err)
	}
	varsDigest := member(qualification, "programs")["vars_digest"].(string)
	knownInputs := map[string]any{"facts": map[string]any{"anime": map[string]any{"year": "1999", "season": 3}}}
	output := map[string]any{"anime": map[string]any{"title": "Frozen title"}}
	programVersion := "naming-vars-v1@" + varsDigest
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["config"].(map[string]any)["vars"] = map[string]any{
		"mode": "frozen", "status": "ok", "reason_code": nil,
		"program":      map[string]any{"digest": varsDigest, "version": programVersion, "runtime_version": "naming-vars-v1"},
		"known_inputs": knownInputs,
		"output":       output,
	}
	member(member(input, "delivery"), "naming")["vars_choice"] = "frozen"
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	record := map[string]any{
		"schema_version": 1, "acquisition_id": p.AcquisitionID, "admission_digest": p.AdmissionDigest,
		"evaluation_id": "evaluation-" + tag, "program_digest": varsDigest, "program_version": programVersion,
		"program_runtime_version": "naming-vars-v1", "runtime_version": "naming-vars-v1",
		"input_digest":  acqDomainDigest("acquisition-vars-input:v1", []byte(canonicalJSON(t, knownInputs))),
		"output":        output,
		"output_digest": acqDomainDigest("acquisition-vars-output:v1", []byte(canonicalJSON(t, output))),
		"evaluated_at":  "2026-01-02T03:04:05Z",
	}
	evaluation := canonicalJSON(t, record)
	result, err := decodeQueuedAccept(t, db, queuedSQL("accept_acquisition_queued_with_vars", 43), append(p.args(), evaluation))
	if err != nil {
		t.Fatalf("accept historical Vars evidence: %v", err)
	}
	if result["status"] != "accepted" {
		t.Fatalf("historical accept result: %v", result)
	}
	var consistent bool
	if err := db.QueryRowContext(context.Background(), `SELECT vars_evaluation_canonical_utf8=$2 AND vars_evidence_consistent(id) FROM acquisitions WHERE id=$1`, p.AcquisitionID, evaluation).Scan(&consistent); err != nil {
		t.Fatal(err)
	}
	if !consistent {
		t.Fatal("historical Vars evaluation fixture is not consistent")
	}
	return p
}

type populatedPlan struct {
	tag, id, artifact, work string
	token                   int64
	waiting                 bool
}

// seedPopulatedPlan leaves a real pre-upgrade plan in either a leased queued
// state or the configuration wait state. Its artifact names the required video
// file, allowing the same plan to commit after the upgrade.
func seedPopulatedPlan(t *testing.T, db *sql.DB, p queuedParams, waiting bool) populatedPlan {
	t.Helper()
	ctx := context.Background()
	fixture := populatedPlan{tag: p.AcquisitionID, id: "plan-" + p.AcquisitionID, artifact: "artifact-" + p.AcquisitionID, waiting: waiting}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE download_tasks SET state='completed' WHERE id=$1`, []any{p.TaskID}},
		{`INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at)
VALUES ($1,$2,$3,'{}','{}','verified',CURRENT_TIMESTAMP)`, []any{fixture.artifact, p.TaskID, p.AcquisitionID}},
		{`INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,subtitle_binding_state)
VALUES ($1,$2,$3,'file-a','a/file-a.mkv',$4,'video',1000,'{"sha256":"fp"}','not_applicable')`,
			[]any{"file-" + p.AcquisitionID, fixture.artifact, p.AcquisitionID, "/downloads/" + p.AcquisitionID + "/file-a.mkv"}},
		{`INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,subtitle_binding_state)
VALUES ($1,$2,$3,'file-b','a/file-b.ass',$4,'subtitle',100,'{"sha256":"sub"}','missing')`,
			[]any{"subtitle-" + p.AcquisitionID, fixture.artifact, p.AcquisitionID, "/downloads/" + p.AcquisitionID + "/file-b.ass"}},
		{`INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,input_digest,state)
VALUES ($1,$2,$3,1,$4,'queued')`, []any{fixture.id, fixture.artifact, p.AcquisitionID, p.AdmissionDigest}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed populated plan: %v", err)
		}
	}
	var created string
	fixture.work = "work-" + p.AcquisitionID
	if err := tx.QueryRowContext(ctx, `SELECT work_item_create($1,'postprocessing_plan',$2,'postprocess_plan',$3,3,'{}',1,0::double precision,7::smallint)`,
		fixture.work, fixture.id, "plan:"+fixture.id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if waiting {
		fixture.work = acquisitionfixture.ParkDuePlan(t, tx, fixture.id, `[{"item":"template_variable_undefined:anime.year","files":1}]`, 1)
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT owner_id,fencing_token FROM work_item_claim_due(60,ARRAY['postprocess_plan'])`).Scan(&created, &fixture.token); err != nil {
			t.Fatal(err)
		}
		if created != fixture.id {
			t.Fatalf("claimed plan %s, want %s", created, fixture.id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=TRUE,recovery_reason='effect_unknown' WHERE id=$1`, fixture.work); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

type frozenAcquisitionBytes struct {
	input, admission string
}

func snapshotFrozenAcquisitions(t *testing.T, db *sql.DB) map[string]frozenAcquisitionBytes {
	t.Helper()
	rows, err := db.Query(`SELECT id,input_canonical_utf8,admission_digest FROM acquisitions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]frozenAcquisitionBytes{}
	for rows.Next() {
		var id string
		var evidence frozenAcquisitionBytes
		if err := rows.Scan(&id, &evidence.input, &evidence.admission); err != nil {
			t.Fatal(err)
		}
		result[id] = evidence
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func continuePopulatedPlan(t *testing.T, db *sql.DB, fixture populatedPlan, p queuedParams) {
	t.Helper()
	ctx := context.Background()
	if fixture.waiting {
		if _, err := db.ExecContext(ctx, `SELECT postprocessing_plan_wake_config(NULL,10)`); err != nil {
			t.Fatal(err)
		}
	} else {
		var receipt []byte
		if err := db.QueryRowContext(ctx, `SELECT postprocessing_plan_requeue_interrupted($1,$2,$3,0::double precision)`, fixture.id, fixture.work, fixture.token).Scan(&receipt); err != nil {
			t.Fatalf("requeue the pre-upgrade lease: %v", err)
		}
	}
	var work, owner string
	if err := db.QueryRowContext(ctx, `SELECT id,owner_id,fencing_token FROM work_item_claim_due(60,ARRAY['postprocess_plan'])`).Scan(&work, &owner, &fixture.token); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.id {
		t.Fatalf("claimed %s, want %s", owner, fixture.id)
	}
	if fixture.waiting {
		var receipt []byte
		if err := db.QueryRowContext(ctx, `SELECT postprocessing_plan_config_resume($1,$2,$3)`, fixture.id, work, fixture.token).Scan(&receipt); err != nil {
			t.Fatalf("resume the pre-upgrade config wait: %v", err)
		}
	}

	var input, capture []byte
	if err := db.QueryRowContext(ctx, `SELECT i.input,jsonb_build_object('input',i.input,'artifact',to_jsonb(a),
 'manifest',jsonb_build_object('acquisition_id',i.id,'format_version',1,'manifest_digest',i.manifest_digest,'item_count',i.manifest_item_count,'sealed_at',i.manifest_sealed_at),
 'preparation_witness',CASE WHEN i.preparation_witness_canonical_utf8 IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'canonical_utf8',i.preparation_witness_canonical_utf8,'witness',i.preparation_witness,'witness_digest',i.preparation_digest) END,
 'manifest_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id),'[]'::jsonb),
 'files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM artifact_files f WHERE f.artifact_id=a.id),'[]'::jsonb),
 'requirements',CASE WHEN i.requirements_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'manifest_digest',i.requirements_manifest_digest,'selection_digest',i.requirements_selection_digest,'evidence_digest',i.requirements_evidence_digest,'requirements_digest',i.requirements_digest,'item_count',i.requirements_item_count,'required_count',i.requirements_required_count,'sealed_at',i.requirements_sealed_at) END,
 'requirement_files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.requirement IS NOT NULL),'[]'::jsonb),
 'target_set',CASE WHEN i.target_digest IS NULL THEN NULL ELSE jsonb_build_object('acquisition_id',i.id,'requirements_digest',i.target_requirements_digest,'target_digest',i.target_digest,'sealed_at',i.target_sealed_at) END,
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.file_ref COLLATE "C") FROM acquisition_files f WHERE f.acquisition_id=p.acquisition_id AND f.spec_digest IS NOT NULL),'[]'::jsonb))
 FROM postprocessing_plans p JOIN artifacts a ON a.id=p.artifact_id JOIN acquisitions i ON i.id=p.acquisition_id WHERE p.id=$1`, fixture.id).Scan(&input, &capture); err != nil {
		t.Fatal(err)
	}
	var captureMembers map[string]json.RawMessage
	if err := json.Unmarshal(capture, &captureMembers); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"vars_protocol", "vars_evaluation"} {
		if _, found := captureMembers[retired]; found {
			t.Fatalf("current plan capture still contains %s", retired)
		}
	}
	source := "/downloads/" + p.AcquisitionID + "/file-a.mkv"
	target := "/library/" + p.AcquisitionID + "/file-a.mkv"
	requirements := []any{
		map[string]any{"file_ref": "file-a", "requirement": "required", "reason_code": "selected", "selection_evidence": map[string]any{"path": source}},
		map[string]any{"file_ref": "file-b", "requirement": "excluded", "reason_code": "not_selected", "selection_evidence": map[string]any{"path": "/downloads/" + p.AcquisitionID + "/file-b.ass"}},
	}
	spec := map[string]any{"file_ref": "file-a", "intent": "ensure_present", "operation": "copy", "canonical_path": target,
		"collision": "block", "source_cleanup": map[string]any{"mode": "none"}, "render_snapshot": map[string]any{}}
	spec["spec_digest"] = acqDomainDigest("acquisition-targets:v1", []byte(canonicalJSON(t, spec)))
	targets := []any{spec}
	declarations := map[string]any{"items": []any{map[string]any{"file_ref": "file-a", "kind": "execute", "target_file_ref": "file-a", "evidence": nil, "reason_code": nil, "diagnostic": map[string]any{}}}, "dependencies": []any{}}
	operations := []any{map[string]any{
		"id": "operation-" + p.AcquisitionID, "file_ref": "file-a", "artifact": fixture.artifact, "version": 1,
		"file": "file-" + p.AcquisitionID, "operation": "copy", "source": source, "target": target,
		"fingerprint": map[string]any{"sha256": "fp"}, "size": 1000, "intent": "ensure_present", "collision": "block",
		"render": map[string]any{}, "config": map[string]any{}, "overwrite": nil, "key": "operation-key-" + p.AcquisitionID,
	}}
	var receipt []byte
	if err := db.QueryRowContext(ctx, `SELECT postprocessing_plan_commit($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb,$10::jsonb)`,
		fixture.id, work, fixture.token, p.AdmissionDigest, input, canonicalJSON(t, requirements), canonicalJSON(t, targets), canonicalJSON(t, declarations), adoptJSON(t, operations), capture).Scan(&receipt); err != nil {
		t.Fatalf("commit the pre-upgrade plan after 163: %v", err)
	}
	var state string
	var operationsCommitted int
	if err := db.QueryRowContext(ctx, `SELECT state,(SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1) FROM postprocessing_plans WHERE id=$1`, fixture.id).Scan(&state, &operationsCommitted); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || operationsCommitted != 1 {
		t.Fatalf("continued plan state=%s operations=%d", state, operationsCommitted)
	}
	var proof []byte
	if err := db.QueryRowContext(ctx, `SELECT postprocessing_plan_commit_state($1,$2,$3)`, fixture.id, work, fixture.token).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if len(proof) == 0 {
		t.Fatal("continued plan has no independent commit proof")
	}
}

func TestRetireNamingVarsPreservesHistoricalEvidenceAndContinuesInflightPlans(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	defer db.Close()
	installMigrationsThrough(t, db, "162_notification_episode_span_facts.sql")

	queued := acceptHistoricalEvaluation(t, db, "running-queued")
	queuedPlan := seedPopulatedPlan(t, db, queued, false)
	waiting := acceptHistoricalEvaluation(t, db, "running-waiting")
	waitingPlan := seedPopulatedPlan(t, db, waiting, true)
	full := acceptHistoricalEvaluation(t, db, "terminal-full")
	terminalHistoricalAcquisition(t, db, full, "succeeded", false)
	seedAcceptableRelease(t, db, "terminal-compact")
	compact := validHistoricalQueued(t, db, "acq-terminal-compact", "key-terminal-compact", "terminal-compact")
	if _, err := callHistoricalQueuedAccept(t, db, compact); err != nil {
		t.Fatal(err)
	}
	terminalHistoricalAcquisition(t, db, compact, "succeeded", true)

	before := snapshotFrozenAcquisitions(t, db)
	if len(before) != 4 {
		t.Fatalf("seeded %d acquisitions, want 4", len(before))
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade populated acquisitions: %v", err)
	}
	after := snapshotFrozenAcquisitions(t, db)
	if len(after) != len(before) {
		t.Fatalf("acquisition count changed from %d to %d", len(before), len(after))
	}
	for id, old := range before {
		current, found := after[id]
		if !found || current.input != old.input || current.admission != old.admission {
			t.Errorf("%s changed frozen input or admission digest", id)
		}
	}

	replay := full
	replay.Qualification = "{}"
	replay.BusinessSnapshot = "{}"
	replayed, err := callQueuedAccept(t, db, replay)
	if err != nil {
		t.Fatalf("historical replay was revalidated under the new contract: %v", err)
	}
	if replayed["status"] != "replayed" || replayed["terminal_receipt"] == nil {
		t.Fatalf("historical replay result: %v", replayed)
	}
	for _, continuation := range []struct {
		fixture populatedPlan
		params  queuedParams
	}{{queuedPlan, queued}, {waitingPlan, waiting}} {
		continuePopulatedPlan(t, db, continuation.fixture, continuation.params)
	}
}
