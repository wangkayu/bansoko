package application

import "encoding/json"

// Failed/colliding/dependency-failed declarations never carry an Operation.
func typedExecuteOperations(b *artifactPreparedBatch, d typedPlanDeclarations) json.RawMessage {
	var plan struct {
		Items []struct {
			Kind string `json:"kind"`
			Ref  string `json:"file_ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(d.Plan), &plan); err != nil {
		return nil
	}
	keep := map[string]bool{}
	for _, item := range plan.Items {
		if item.Kind == "execute" {
			for _, op := range b.operations {
				if op.file.fileRef == item.Ref {
					keep[op.plan.id] = true
				}
			}
		}
	}
	var payload struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(batchPayload(b), &payload); err != nil {
		return nil
	}
	result := []json.RawMessage{}
	for _, raw := range payload.Operations {
		var op struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil
		}
		if keep[op.ID] {
			var fields map[string]json.RawMessage
			json.Unmarshal(raw, &fields)
			var parent string
			json.Unmarshal(fields["bound_video"], &parent)
			if parent != "" && !keep[parent] {
				fields["bound_video"] = json.RawMessage(`""`)
				raw, _ = json.Marshal(fields)
			}
			result = append(result, raw)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return raw
}
