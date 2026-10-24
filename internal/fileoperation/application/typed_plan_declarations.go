package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/filekinds"
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Declarations are canonical bytes, not SQL-generated JSON text. Row identities
// and topological ranks are excluded from business digests.
type typedPlanDeclarations struct {
	Requirements string `json:"requirements"`
	Targets      string `json:"targets"`
	Plan         string `json:"plan"`
}

func canonicalPlanValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	parsed, err := protocol.ParseStrict(raw)
	if err != nil {
		return "", err
	}
	return string(protocol.Canonical(parsed)), nil
}

func buildTypedPlanDeclarations(ctx context.Context, b *artifactPreparedBatch) (typedPlanDeclarations, error) {
	if err := ctx.Err(); err != nil {
		return typedPlanDeclarations{}, err
	}
	var in struct {
		Selection struct {
			Mode string   `json:"mode"`
			Refs []string `json:"explicit_refs"`
		} `json:"selection"`
		Delivery struct {
			Cleanup json.RawMessage `json:"source_cleanup"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(b.input, &in); err != nil {
		return typedPlanDeclarations{}, err
	}
	refs := map[string]bool{}
	for _, ref := range in.Selection.Refs {
		refs[ref] = true
	}
	var captured struct {
		Requirements     json.RawMessage  `json:"requirements"`
		RequirementFiles []map[string]any `json:"requirement_files"`
		TargetSet        json.RawMessage  `json:"target_set"`
		Targets          []map[string]any `json:"targets"`
	}
	if err := json.Unmarshal(b.capture.Facts, &captured); err != nil {
		return typedPlanDeclarations{}, err
	}
	earlyRequirements := len(captured.Requirements) > 0 && string(captured.Requirements) != "null"
	earlyTargets := len(captured.TargetSet) > 0 && string(captured.TargetSet) != "null"
	earlyByRef := map[string]map[string]any{}
	for _, row := range captured.RequirementFiles {
		earlyByRef[row["file_ref"].(string)] = row
	}
	operations := map[string]preparedOperation{}
	paths := map[string][]string{}
	for _, op := range b.operations {
		operations[op.file.fileRef] = op
		if op.plan.failureCode == "" {
			paths[op.plan.targetPath] = append(paths[op.plan.targetPath], op.file.fileRef)
		}
	}
	requirements := []map[string]any{}
	targets := []map[string]any{}
	items := []map[string]any{}
	dependencies := []map[string]any{}
	sorted := append([]artifactFileContext(nil), b.artifact.files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].fileRef < sorted[j].fileRef })
	// Only first-level videos and subtitles exist for planning (fileSelection);
	// the file rules select videos by filename and subtitles are not subject to
	// them. Everything else is excluded here exactly as prepare excludes it,
	// so no ignored file is ever declared required (and then failed).
	selection := newFileSelection(b.artifact.frozenInput, b.frozen.fileKindsVersion)
	for _, file := range sorted {
		kind, ignored := selection.classify(file)
		required := ignored == "" && (kind != filekinds.KindVideo || fileAllowedByFilter(file, b.prep.config.fileFilter))
		if in.Selection.Mode == "explicit_refs" {
			required = refs[file.fileRef] && ignored == ""
		}
		// A file the planner left out on purpose (an unknown file under the
		// skip route, a subtitle that follows an unplanned video) is excluded
		// with its own reason, never required-and-failed.
		special := b.excluded[file.fileRef]
		if special != "" {
			required = false
		}
		if earlyRequirements {
			row, ok := earlyByRef[file.fileRef]
			if !ok {
				return typedPlanDeclarations{}, fmt.Errorf("early requirements lack manifest member")
			}
			required = row["requirement"] == "required"
		}
		requirement, reason := "excluded", "file_rules_excluded"
		if ignored != "" {
			reason = ignored
		}
		if required {
			requirement, reason = "required", "file_rules_selected"
		}
		if special != "" && !earlyRequirements {
			reason = special
		} else if in.Selection.Mode == "explicit_refs" {
			reason = "explicit_refs"
		}
		requirements = append(requirements, map[string]any{"file_ref": file.fileRef, "requirement": requirement, "reason_code": reason, "selection_evidence": map[string]any{"selection": json.RawMessage(selectionRaw(b.input)), "path": file.relativePath, "size": file.sizeBytes, "fingerprint": file.fingerprint}})
		if !required {
			continue
		}
		op, found := operations[file.fileRef]
		item := map[string]any{"file_ref": file.fileRef, "kind": "failed", "target_file_ref": nil, "evidence": nil, "reason_code": "PLAN_TARGET_UNAVAILABLE", "diagnostic": map[string]any{}}
		if found {
			p := op.plan
			if p.failureCode != "" {
				item["reason_code"] = p.failureCode
				item["diagnostic"] = map[string]any{"reason": p.failureReason}
			} else if len(paths[p.targetPath]) > 1 {
				item["reason_code"] = "PLAN_NAMING_COLLISION"
			} else {
				businessRender, err := typedBusinessRender(p.renderSnapshot)
				if err != nil {
					return typedPlanDeclarations{}, err
				}
				spec := map[string]any{"file_ref": file.fileRef, "intent": p.intent, "operation": p.operation, "canonical_path": p.targetPath, "collision": p.collisionPolicy, "source_cleanup": json.RawMessage(in.Delivery.Cleanup), "render_snapshot": businessRender, "exact_size": file.sizeBytes, "source_fingerprint": json.RawMessage(file.fingerprint)}
				canonical, err := canonicalPlanValue(spec)
				if err != nil {
					return typedPlanDeclarations{}, err
				}
				spec["spec_digest"] = protocol.Digest("acquisition-targets:v1", []byte(canonical))
				targets = append(targets, spec)
				item["kind"], item["target_file_ref"], item["reason_code"] = "execute", file.fileRef, nil
				if satisfaction, ok := b.satisfactions[file.fileRef]; ok {
					evidence, err := satisfactionEvidence(b, file, p, satisfaction, spec["spec_digest"].(string))
					if err != nil {
						return typedPlanDeclarations{}, err
					}
					item["kind"], item["evidence"] = satisfaction.kind, evidence
				}
				if p.boundVideoOperationID != "" && item["kind"] == "execute" {
					var dependency string
					for _, video := range b.operations {
						if video.plan.id == p.boundVideoOperationID {
							dependency = video.file.fileRef
							break
						}
					}
					if dependency == "" {
						return typedPlanDeclarations{}, fmt.Errorf("planning dependency is outside prepared batch")
					}
					dependencies = append(dependencies, map[string]any{"file_ref": file.fileRef, "depends_on_ref": dependency, "dependency_kind": "subtitle_video"})
				}
			}
		}
		items = append(items, item)
	}
	// Naming collisions fail every member; dependent subtitles are declared as
	// dependency_failed rather than acquiring executable work behind a failure.
	byRef := map[string]map[string]any{}
	for _, item := range items {
		byRef[item["file_ref"].(string)] = item
	}
	for _, edge := range dependencies {
		if parent := byRef[edge["depends_on_ref"].(string)]; parent != nil && parent["kind"] != "execute" && parent["kind"] != "adopt" && parent["kind"] != "retain" {
			item := byRef[edge["file_ref"].(string)]
			item["kind"] = "dependency_failed"
			item["reason_code"] = "DEPENDENCY_FAILED"
		}
	}
	if earlyRequirements {
		requirements = []map[string]any{}
		for _, file := range sorted {
			row := earlyByRef[file.fileRef]
			requirements = append(requirements, map[string]any{"file_ref": file.fileRef, "requirement": row["requirement"], "reason_code": row["requirement_reason_code"], "selection_evidence": row["selection_evidence"]})
		}
	}
	if earlyTargets {
		targets = []map[string]any{}
		for _, row := range captured.Targets {
			targets = append(targets, map[string]any{"file_ref": row["file_ref"], "spec_digest": row["spec_digest"], "intent": row["intent"], "operation": row["operation"], "canonical_path": row["canonical_path"], "collision": row["collision"], "source_cleanup": row["target_source_cleanup"], "render_snapshot": row["render_snapshot"]})
		}
	}
	requirementsRaw, err := canonicalPlanValue(requirements)
	if err != nil {
		return typedPlanDeclarations{}, err
	}
	targetsRaw, err := canonicalPlanValue(targets)
	if err != nil {
		return typedPlanDeclarations{}, err
	}
	planRaw, err := canonicalPlanValue(map[string]any{"items": items, "dependencies": dependencies})
	if err != nil {
		return typedPlanDeclarations{}, err
	}
	return typedPlanDeclarations{requirementsRaw, targetsRaw, planRaw}, nil
}

func selectionRaw(input json.RawMessage) json.RawMessage {
	var in map[string]json.RawMessage
	_ = json.Unmarshal(input, &in)
	return in["selection"]
}
