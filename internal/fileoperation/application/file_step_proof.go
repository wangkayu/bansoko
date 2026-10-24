package application

import (
	"encoding/json"
	"fmt"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
)

func publicationStepProof(base map[string]any, objects protocol.Value, dependency string, op domain.FileOperation, r domain.PublicationStepReceipt) (map[string]any, error) {
	raw := func(v protocol.Value) json.RawMessage { return protocol.Canonical(v) }
	sourceDigest := protocol.Digest("file-source-evidence:v1", protocol.Canonical(objects.Object["source"]))
	parent := raw(objects.Object["target_parent"])
	object := func(location protocol.Value, o domain.PathObservation) map[string]any {
		return pathObject(raw(location), o)
	}
	switch r.Step {
	case "stage_temp":
		return map[string]any{"kind": "created_temp", "object": object(objects.Object["temp"], r.Target), "parent": parent, "verified_source_digest": sourceDigest, "flushed": true}, nil
	case "publish_target":
		location := objects.Object["target"]
		method := "no_replace"
		var displaced any = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
		if op.CollisionPolicy == domain.CollisionVersion || op.CollisionPolicy == domain.CollisionOverwrite {
			loc, err := publicationLocation(r.TargetPath)
			if err != nil {
				return nil, err
			}
			b, err := canonicalPlanValue(loc)
			if err != nil {
				return nil, err
			}
			location, err = protocol.ParseStrict([]byte(b))
			if err != nil {
				return nil, err
			}
			// rename(2) replaces the reserved placeholder (version) or the
			// existing target (overwrite); the displaced object is the one
			// observed at the final location just before the rename.
			method = "replace"
			displaced = frozenKnown(object(location, r.Displaced))
		}
		return map[string]any{"kind": "published", "source_evidence_digest": sourceDigest, "target": object(location, r.Target), "parent": parent, "atomic_method": method, "displaced": displaced, "dependency_digest": dependency}, nil
	case "reserve_version":
		loc, err := publicationLocation(r.TargetPath)
		if err != nil {
			return nil, err
		}
		b, err := canonicalPlanValue(loc)
		if err != nil {
			return nil, err
		}
		location, err := protocol.ParseStrict([]byte(b))
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "reserved_path", "object": object(location, r.Target), "parent": parent, "base_path": op.TargetPath, "candidate_ordinal": r.CandidateOrdinal, "reservation_method": "exclusive_create"}, nil
	case "source_cleanup", "temp_cleanup":
		var before any
		var location any
		if r.Step == "source_cleanup" {
			before = raw(objects.Object["source"])
			location = raw(objects.Object["source"].Object["location"])
			parent = raw(objects.Object["source_parent"])
		} else {
			checkpoint := base["payload"].(map[string]any)["checkpoint"].(map[string]any)
			proof, ok := checkpoint["stage_temp"].(map[string]any)["proof"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("temp cleanup lacks created object proof")
			}
			before = proof["object"]
			location = raw(objects.Object["temp"])
		}
		mutation, err := canonicalPlanValue(map[string]any{"permit_id": base["identity"].(map[string]any)["permit_id"], "step": r.Step, "before": before, "removed_at": observationTime(r.ObservedAt)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "removed_object", "before": before, "parent": parent, "after": map[string]any{"location": location, "parent": parent, "observed_at": observationTime(r.ObservedAt), "method": "lstat_enoent"}, "mutation_receipt_digest": protocol.Digest("file-mutation-receipt:v1", []byte(mutation))}, nil
	default:
		return nil, fmt.Errorf("unsupported publication step %s", r.Step)
	}
}
