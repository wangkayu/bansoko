package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"fmt"
)

// Validate every frozen object independently before using its digest or joining
// it to a receipt. Canonical bytes alone cannot establish a usable observation.
func validatePublicationObjects(objects protocol.Value, operation, collision string) error {
	keys := map[string]string{"source": "PathObjectV1", "source_parent": "PathIdentityV1", "target": "LocationV1", "target_parent": "PathIdentityV1", "temp": "LocationV1"}
	if collision == "overwrite" {
		keys["expected_target"] = "PathObjectV1"
	}
	if objects.Kind != protocol.KindObject || len(objects.Object) != len(keys) {
		return fmt.Errorf("FILE_FROZEN_OBJECT_SHAPE_INVALID")
	}
	for key, schema := range keys {
		value, ok := objects.Object[key]
		if !ok {
			return fmt.Errorf("FILE_FROZEN_OBJECT_SHAPE_INVALID: %s", key)
		}
		if err := protocol.Validate(schema, value); err != nil {
			return fmt.Errorf("FILE_FROZEN_OBJECT_SHAPE_INVALID: %s: %w", key, err)
		}
		if schema == "LocationV1" {
			path := value.Object["canonical_path"].String
			expected, err := publicationLocation(path)
			if err != nil {
				return err
			}
			canonical, err := canonicalPlanValue(expected)
			if err != nil {
				return err
			}
			if canonical != string(protocol.Canonical(value)) {
				return fmt.Errorf("FILE_FROZEN_LOCATION_BINDING_INVALID: %s", key)
			}
		}
	}
	for _, key := range []string{"source", "expected_target"} {
		if object, ok := objects.Object[key]; ok {
			if err := validatePublicationObservation(object, key); err != nil {
				return err
			}
		}
	}
	_ = operation
	return nil
}

// validatePublicationObservation checks one PathObjectV1 carried by a step
// proof: a regular file with a size at a location. Path + size is identity.
func validatePublicationObservation(object protocol.Value, key string) error {
	if err := protocol.Validate("PathObjectV1", object); err != nil {
		return fmt.Errorf("FILE_FROZEN_OBJECT_OBSERVATION_INVALID: %s: %w", key, err)
	}
	if object.Object["file_type"].String != "regular" {
		return fmt.Errorf("FILE_FROZEN_OBJECT_OBSERVATION_INVALID: %s", key)
	}
	return nil
}
