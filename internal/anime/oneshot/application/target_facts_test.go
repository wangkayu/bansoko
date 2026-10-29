package application

import (
	"encoding/json"
	"fmt"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
)

// The one-shot snapshot's target facts name the stored set by digest and
// count: they are the same size for one target and for a hundred, and an
// unconfigured or failed plan names no set.
func TestOneShotTargetFactsDoNotGrowWithTargets(t *testing.T) {
	plan := func(targets int) acquisitiontarget.PlanResult {
		var result acquisitiontarget.PlanResult
		result.Admission.Admission = acquisitiontarget.AdmissionDownload
		result.Set.Digest = "sha256:" + fmt.Sprintf("%064d", targets)
		for index := 0; index < targets; index++ {
			result.Set.Targets = append(result.Set.Targets, acquisitiontarget.Spec{TargetRef: fmt.Sprintf("target-%03d", index), RenderSnapshot: json.RawMessage(`{"vars_digest":"sha256:x"}`)})
		}
		return result
	}
	one, err := encodeOneShotTargetFacts(plan(1), true, false)
	if err != nil {
		t.Fatal(err)
	}
	hundred, err := encodeOneShotTargetFacts(plan(100), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hundred)-len(one) > 2 {
		t.Fatalf("target facts are %d bytes for one target and %d for a hundred", len(one), len(hundred))
	}
	var facts acquisitiontarget.FrozenFacts
	if err := json.Unmarshal(hundred, &facts); err != nil || facts.TargetCount != 100 || facts.TargetSetDigest != plan(100).Set.Digest {
		t.Fatalf("target facts = %s (%v)", hundred, err)
	}
	unconfigured, err := encodeOneShotTargetFacts(plan(3), false, false)
	if err != nil || string(unconfigured) != `{"admission":"download_required","target_count":0}` {
		t.Fatalf("unconfigured target facts = %s (%v)", unconfigured, err)
	}
	failed, err := encodeOneShotTargetFacts(plan(3), true, true)
	if err != nil {
		t.Fatal(err)
	}
	var failedFacts acquisitiontarget.FrozenFacts
	if err := json.Unmarshal(failed, &failedFacts); err != nil || failedFacts.TargetCount != 0 || failedFacts.TargetSetDigest != "" {
		t.Fatalf("failed plan target facts = %s (%v)", failed, err)
	}
}
