package application

import (
	"encoding/json"
	"fmt"
)

// Rebind only reference-bearing fields in the already prepared observation.
// Paths, fingerprints, timestamps and frozen evidence retain their values.
func rebindInspectionSnapshot(raw []byte, refs map[string]string, fingerprint bool) ([]byte, error) {
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	rebind := func(ref string) (string, error) {
		next, ok := refs[ref]
		if !ok || next == "" {
			return "", fmt.Errorf("inspection snapshot has unbound file reference %q", ref)
		}
		return next, nil
	}
	if primary, ok := snapshot["primary_file_ref"].(string); ok {
		next, err := rebind(primary)
		if err != nil {
			return nil, err
		}
		snapshot["primary_file_ref"] = next
	}
	rebindEntry := func(value any) error {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("inspection snapshot file entry is not an object")
		}
		ref, ok := entry["file_ref"].(string)
		if !ok {
			return fmt.Errorf("inspection snapshot file reference is missing")
		}
		next, err := rebind(ref)
		if err != nil {
			return err
		}
		entry["file_ref"] = next
		return nil
	}
	for _, key := range []string{"files", "inspected_files"} {
		value, exists := snapshot[key]
		if !exists {
			continue
		}
		if key == "files" && fingerprint {
			entries, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("inspection fingerprint files is not an object")
			}
			rebound := make(map[string]any, len(entries))
			for ref, facts := range entries {
				next, err := rebind(ref)
				if err != nil {
					return nil, err
				}
				if _, exists := rebound[next]; exists {
					return nil, fmt.Errorf("duplicate rebound inspection reference %q", next)
				}
				rebound[next] = facts
			}
			snapshot[key] = rebound
		} else {
			entries, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("inspection snapshot files is not an array")
			}
			for _, entry := range entries {
				if err := rebindEntry(entry); err != nil {
					return nil, err
				}
			}
		}
	}
	if entry, exists := snapshot["inspected_primary_file"]; exists {
		if err := rebindEntry(entry); err != nil {
			return nil, err
		}
	}
	return json.Marshal(snapshot)
}
