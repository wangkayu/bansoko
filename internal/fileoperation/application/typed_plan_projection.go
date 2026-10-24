package application

import "encoding/json"

// Only this explicit row-binding slot is excluded. Arbitrary business fields
// are never recursively deleted based on a guessed ID/timestamp key name.
func typedBusinessRender(raw json.RawMessage) (json.RawMessage, error) {
	if err := validateBindingJSON(raw); err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if bindingRaw, ok := value["binding"]; ok {
		var binding map[string]json.RawMessage
		if err := json.Unmarshal(bindingRaw, &binding); err != nil {
			return nil, err
		}
		if err := validateSourceRenderBinding(binding); err != nil {
			return nil, err
		}
		confidence, err := projectConfidenceNumber(binding["confidence"])
		if err != nil {
			return nil, err
		}
		binding["confidence"], _ = json.Marshal(confidence)
		binding["schema_version"] = json.RawMessage(`1`)
		delete(binding, "bound_video_operation_id")
		cleaned, err := json.Marshal(binding)
		if err != nil {
			return nil, err
		}
		if err := validateSubtitleRenderBindingV1(cleaned); err != nil {
			return nil, err
		}
		value["binding"] = cleaned
	}
	return json.Marshal(value)
}
