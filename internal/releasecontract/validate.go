package releasecontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	hardeningdomain "bansoko/internal/hardening/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed manifest.schema.json
var currentSchema []byte

// ValidateCurrent validates the release-manifest-1 manifest contract. The
// rehearsal exception only permits an empty images array; it does not relax
// any other schema or domain rule.
func ValidateCurrent(path string, allowEmptyImages bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateSchema(document, allowEmptyImages); err != nil {
		return err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	var manifest hardeningdomain.ReleaseManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return fmt.Errorf("decode release manifest: %w", err)
	}
	if err := manifest.ValidateForEmbeddedUpdate(); err != nil {
		return fmt.Errorf("manifest semantic validation failed: %w", err)
	}
	return nil
}

func validateSchema(document map[string]any, allowEmptyImages bool) error {
	var schemaDocument map[string]any
	if err := json.Unmarshal(currentSchema, &schemaDocument); err != nil {
		return fmt.Errorf("decode embedded manifest schema: %w", err)
	}
	if allowEmptyImages {
		if images, ok := document["images"].([]any); ok && len(images) == 0 {
			properties := schemaDocument["properties"].(map[string]any)
			imagesSchema := properties["images"].(map[string]any)
			delete(imagesSchema, "minItems")
		}
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("bansoko-current", schemaDocument); err != nil {
		return fmt.Errorf("load embedded manifest schema: %w", err)
	}
	sch, err := compiler.Compile("bansoko-current")
	if err != nil {
		return fmt.Errorf("compile embedded manifest schema: %w", err)
	}
	if err := sch.Validate(document); err != nil {
		return fmt.Errorf("manifest schema validation failed: %w", err)
	}
	return nil
}
