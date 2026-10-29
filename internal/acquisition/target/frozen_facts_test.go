package target

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
)

// The carrier form of an unplanned admission names no set and no targets.
func TestUnplannedFrozenFactsEncodeWithoutTargets(t *testing.T) {
	raw, err := json.Marshal(UnplannedFrozenFacts())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"admission":"download_required","target_count":0}` {
		t.Fatalf("unplanned frozen facts = %s", raw)
	}
}

// The vars digest is a function of the evaluation's content: the same
// evaluation, however its maps were built, names the same digest; another
// inventory names another one; no evaluation has none.
func TestVarsDigestIsCanonicalAndContentBound(t *testing.T) {
	if digest, err := VarsDigest(nil); err != nil || digest != "" {
		t.Fatalf("absent evaluation digest = %q, %v", digest, err)
	}
	build := func(files ...string) *namingvars.FrozenEvaluation {
		input := namingvars.Input{ReleaseTitle: "Detective Conan [01-02]", Facts: map[string]any{"anime": map[string]any{"title": "Detective Conan", "year": "1996"}}}
		for index, name := range files {
			input.Files = append(input.Files, namingvars.File{Name: name, Path: "Detective Conan/" + name, Size: int64(index + 1)})
		}
		return &namingvars.FrozenEvaluation{Status: "ok", Output: namingvars.Vars{"actual": {"b": "2", "a": "1"}}, Input: input}
	}
	first, err := VarsDigest(build("01.mkv", "02.mkv"))
	if err != nil || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest = %q, %v", first, err)
	}
	again, err := VarsDigest(build("01.mkv", "02.mkv"))
	if err != nil || again != first {
		t.Fatalf("the same evaluation digests to %q and %q", first, again)
	}
	other, err := VarsDigest(build("01.mkv", "03.mkv"))
	if err != nil || other == first {
		t.Fatalf("another inventory digests to %q (first %q)", other, first)
	}
}

// Every planned target names the release-level vars evaluation by digest, so
// a render snapshot is the same size however many files the torrent lists.
func TestPlannedRenderSnapshotsNameTheVarsByDigest(t *testing.T) {
	render := func(extra int) json.RawMessage {
		scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
		for index := 0; index < extra; index++ {
			scenario.preview.FrozenVars.Input.Files = append(scenario.preview.FrozenVars.Input.Files, namingvars.File{Name: fmt.Sprintf("extra-%04d.png", index), Path: fmt.Sprintf("Scans/extra-%04d.png", index), Size: 1})
		}
		plan := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
		if plan.Admission.Admission != AdmissionDownload || len(plan.Set.Targets) != 1 {
			t.Fatalf("plan = %+v", plan.Admission)
		}
		digest, err := VarsDigest(scenario.preview.FrozenVars)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot map[string]any
		if err := json.Unmarshal(plan.Set.Targets[0].RenderSnapshot, &snapshot); err != nil {
			t.Fatal(err)
		}
		if _, embedded := snapshot["vars"]; embedded || snapshot["vars_digest"] != digest {
			t.Fatalf("render snapshot = %s, want the vars named by digest %s", plan.Set.Targets[0].RenderSnapshot, digest)
		}
		return plan.Set.Targets[0].RenderSnapshot
	}
	small, large := render(0), render(2000)
	if len(small) != len(large) {
		t.Fatalf("render snapshot is %d bytes with 1 file and %d bytes with 2001", len(small), len(large))
	}
}

// A torrent over the filter's inventory budget is refused by its size, not as
// an invalid path.
func TestPlanRefusesAnInventoryOverTheBudgetAsTooLarge(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	size := int64(1)
	for index := 0; index < animedomain.MaxFilterInventoryEntries; index++ {
		name := fmt.Sprintf("Frame %05d.png", index)
		scenario.item.ReleaseFiles = append(scenario.item.ReleaseFiles, acquisitiondomain.ReleaseFile{SourcePath: name, Filename: name, SizeBytes: &size, MediaKind: "other"})
	}
	plan := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
	if plan.Admission.Reason != "torrent_too_large" {
		t.Fatalf("admission = %+v, want torrent_too_large", plan.Admission)
	}
}
