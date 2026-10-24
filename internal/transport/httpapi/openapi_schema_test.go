package httpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// This file validates real HTTP responses against the published contract in
// api/openapi.yaml. The schema is compiled from the spec itself, not from a
// hand-written copy, so a response field the contract does not declare (a
// storage-only column leaking through the raw record union, for example) fails
// the response that carries it.

var (
	openAPIResourceOnce sync.Once
	openAPIResource     map[string]any
	openAPIResourceErr  error
)

// openAPIDocument loads api/openapi.yaml once per test binary. OpenAPI 3.1
// component schemas are JSON Schema 2020-12, so the document itself can be the
// schema resource: `#/components/schemas/...` references resolve inside it and
// the OpenAPI-only keywords (`openapi`, `paths`, ...) are ignored.
func openAPIDocument() (map[string]any, error) {
	openAPIResourceOnce.Do(func() {
		path := filepath.Join("..", "..", "..", "api", "openapi.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			openAPIResourceErr = fmt.Errorf("read OpenAPI source %s: %w", path, err)
			return
		}
		var document map[string]any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			openAPIResourceErr = fmt.Errorf("decode OpenAPI source: %w", err)
			return
		}
		if _, ok := document["components"]; !ok {
			openAPIResourceErr = fmt.Errorf("OpenAPI source has no components section")
			return
		}
		// OpenAPI's `nullable: true` is not a JSON Schema keyword. Rewrite every
		// occurrence into an anyOf with a null member so a nullable field is
		// validated as nullable instead of as its base type only.
		nullableToJSONSchema(document)
		openAPIResource = document
	})
	return openAPIResource, openAPIResourceErr
}

func nullableToJSONSchema(node any) {
	switch typed := node.(type) {
	case map[string]any:
		for _, value := range typed {
			nullableToJSONSchema(value)
		}
		nullable, ok := typed["nullable"].(bool)
		if !ok || !nullable {
			return
		}
		delete(typed, "nullable")
		base := make(map[string]any, len(typed))
		for key, value := range typed {
			base[key] = value
		}
		for key := range typed {
			delete(typed, key)
		}
		typed["anyOf"] = []any{base, map[string]any{"type": "null"}}
	case []any:
		for _, value := range typed {
			nullableToJSONSchema(value)
		}
	}
}

// validateAgainstOpenAPI fails the test when payload does not satisfy the
// named component schema of api/openapi.yaml.
func validateAgainstOpenAPI(t *testing.T, schemaName string, payload []byte) {
	t.Helper()
	document, err := openAPIDocument()
	if err != nil {
		t.Fatalf("load OpenAPI contract: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	const resource = "bansoko-openapi"
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatalf("load OpenAPI schema resource: %v", err)
	}
	schema, err := compiler.Compile(resource + "#/components/schemas/" + schemaName)
	if err != nil {
		t.Fatalf("compile %s schema: %v", schemaName, err)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode response payload: %v", err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("%s response violates api/openapi.yaml: %v\npayload: %s", schemaName, err, payload)
	}
}
