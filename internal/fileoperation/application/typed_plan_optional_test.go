package application

import (
	"encoding/json"
	"testing"
)

func TestFrozenPolicyIgnoresRetiredVarsAndDoesNotWriteThemBack(t *testing.T) {
	const raw = `{"effective_sections":{"parser":{},"file_rules":{},"naming_rules":{},"file_effect":{}},"section_versions":{"naming_rules":3},"vars":"not an evaluation"}`
	config, err := effectiveConfigFromFrozenDownloadPolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := marshalConfigSnapshot(config, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["vars"]; ok || string(keys["rules_version"]) != "3" {
		t.Fatalf("unexpected config snapshot: %s", snapshot)
	}
}
