package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Validate original JSON tokens without coercing numbers or accepting duplicate keys.
func validateBindingJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key := k.(string)
					if seen[key] {
						return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED: duplicate %s", key)
					}
					seen[key] = true
					if err := walk(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := walk(); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("invalid JSON delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func validateSourceRenderBinding(b map[string]json.RawMessage) error {
	keys := []string{"state", "bound_video_file_ref", "bound_video_operation_id", "locked_directory", "locked_basename", "locked_extension", "method", "confidence"}
	if len(b) != len(keys) {
		return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED: binding keys")
	}
	for _, key := range keys {
		if _, ok := b[key]; !ok {
			return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/%s", key)
		}
	}
	var state, method string
	if err := json.Unmarshal(b["state"], &state); err != nil || state != "bound" {
		return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/state")
	}
	if err := json.Unmarshal(b["method"], &method); err != nil {
		return err
	}
	if method != bindingMethodBasenameMatch && method != bindingMethodEpisodeMatch && method != bindingMethodUniqueVideo {
		return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/method")
	}
	for _, key := range keys[1:6] {
		var text string
		if err := json.Unmarshal(b[key], &text); err != nil || text == "" {
			return fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/%s", key)
		}
	}
	return nil
}
