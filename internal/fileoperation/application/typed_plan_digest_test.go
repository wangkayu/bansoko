package application

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTypedPlanDigestExcludesOperationRowIdentity(t *testing.T) {
	makeBatch := func(id string) *artifactPreparedBatch {
		file := artifactFileContext{id: "artifact-file", fileRef: "f1", relativePath: "a.mkv", sourcePath: "/downloads/a.mkv", sizeBytes: 10, fingerprint: json.RawMessage(`{"algorithm":"metadata","value":"meta:size=10","size_bytes":10}`)}
		return &artifactPreparedBatch{capture: PreparedPlanCapture{Facts: json.RawMessage(`{"requirements":null,"requirement_files":[],"target_set":null,"targets":[]}`)}, input: json.RawMessage(`{"selection":{"mode":"explicit_refs","explicit_refs":["f1"]},"delivery":{"source_cleanup":{"mode":"none"}}}`), artifact: artifactContext{files: []artifactFileContext{file}}, operations: []preparedOperation{{file: file, plan: operationPlan{id: id, targetPath: "/library/a.mkv", operation: "copy", intent: "ensure_present", collisionPolicy: "block", renderSnapshot: json.RawMessage(`{}`)}}}}
	}
	a, err := buildTypedPlanDeclarations(context.Background(), makeBatch("operation-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildTypedPlanDeclarations(context.Background(), makeBatch("operation-b"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Plan != b.Plan {
		t.Fatalf("Plan business canonical bytes depend on Operation row ID: %s vs %s", a.Plan, b.Plan)
	}
}
