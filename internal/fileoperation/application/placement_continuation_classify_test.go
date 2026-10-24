package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/workitems"
)

// TestClassifyPlacementContinuation pins the unambiguous discriminator: a
// strict typed continuation matches, any broken placement intent is malformed
// (fail closed), and ordinary file_execute inputs stay normal.
func TestClassifyPlacementContinuation(t *testing.T) {
	owner := workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "op-1"}
	claim := func(snapshot string) workitems.Claim {
		return workitems.Claim{Owner: owner, Kind: workitems.KindFileExecute, InputSnapshot: json.RawMessage(snapshot)}
	}
	continuation := `{"schema_version":"file-execute-continuation/v1","continuation":"placement","placement_retry":true,"file_operation_id":"op-1"}`

	if _, class := classifyPlacementContinuation(claim(continuation)); class != continuationIsContinuation {
		t.Fatalf("strict continuation classified as %d", class)
	}
	for name, snapshot := range map[string]string{
		"retry flag as string":     `{"placement_retry":"yes","file_operation_id":"op-1"}`,
		"wrong schema version":     `{"schema_version":"v0","continuation":"placement","placement_retry":true,"file_operation_id":"op-1"}`,
		"wrong continuation kind":  `{"schema_version":"file-execute-continuation/v1","continuation":"file","placement_retry":true,"file_operation_id":"op-1"}`,
		"operation id mismatch":    `{"schema_version":"file-execute-continuation/v1","continuation":"placement","placement_retry":true,"file_operation_id":"op-2"}`,
		"missing operation id":     `{"schema_version":"file-execute-continuation/v1","continuation":"placement","placement_retry":true}`,
		"retry false with intent":  `{"schema_version":"file-execute-continuation/v1","continuation":"placement","placement_retry":false,"file_operation_id":"op-1"}`,
		"broken json with intent":  `{"placement_retry":`,
		"continuation key unknown": `{"schema_version":"file-execute-continuation/v1","continuation":"mystery","file_operation_id":"op-1"}`,
	} {
		if _, class := classifyPlacementContinuation(claim(snapshot)); class != continuationMalformed {
			t.Fatalf("%s: placement intent must classify malformed, got %d", name, class)
		}
	}
	for name, snapshot := range map[string]string{
		"ordinary input":  `{"operation_id":"op-1","artifact_id":"a","artifact_file_id":"f"}`,
		"empty object":    `{}`,
		"non object":      `[]`,
		"recovery mode":   `{"operation_id":"op-1","recovery_mode":"overwrite"}`,
		"one shot marker": `{"operation_id":"op-1","one_shot":{"marker_id":"m","correlation_id":"c","admission_epoch":1}}`,
	} {
		if _, class := classifyPlacementContinuation(claim(snapshot)); class != continuationNormal {
			t.Fatalf("%s: no placement intent must classify normal, got %d", name, class)
		}
	}
}
