package application

import (
	"encoding/json"
	"fmt"
)

// SubtitleRenderBindingV1 is the digest wire only. Never normalizes caller input.
func validateSubtitleRenderBindingV1(raw json.RawMessage) error {
	if err := validateBindingJSON(raw); err != nil {
		return err
	}
	var b map[string]json.RawMessage
	if err := json.Unmarshal(raw, &b); err != nil {
		return err
	}
	if string(b["schema_version"]) != "1" {
		return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/schema_version")
	}
	var c string
	if err := json.Unmarshal(b["confidence"], &c); err != nil || !wireConfidenceSyntax.MatchString(c) {
		return fmt.Errorf("BINDING_CONFIDENCE_INVALID at binding/confidence")
	}
	source := map[string]json.RawMessage{}
	for k, v := range b {
		source[k] = v
	}
	delete(source, "schema_version")
	source["bound_video_operation_id"] = json.RawMessage(`"wire-validation"`)
	if err := validateSourceRenderBinding(source); err != nil {
		return err
	}
	return nil
}
