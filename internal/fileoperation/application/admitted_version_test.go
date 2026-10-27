package application

import (
	"testing"

	"bansoko/internal/batchplan"
	"bansoko/internal/filekinds"
)

// A video the frozen plan gave a version number re-renders to that versioned
// target; every other video keeps its rendered target.
func TestRecordTargetVersion(t *testing.T) {
	plan := &batchplan.Plan{Files: []batchplan.File{
		{SourcePath: "Pack/Season 1/Show - 02.mkv", Kind: filekinds.KindVideo, Outcome: batchplan.OutcomePlanned},
		{SourcePath: "Pack/Season 2/Show - 02.mkv", Kind: filekinds.KindVideo, Outcome: batchplan.OutcomePlanned},
	}}
	plan.MarkTargetVersion("Pack/Season 2/Show - 02.mkv", 2)
	plan.MarkTargetVersion("Pack/Season 1/Show - 02.mkv", 1)
	prep := prepareContext{plan: plan}
	selection := newFileSelection(nil, filekinds.Version, plan.Selection())
	first := artifactFileContext{fileRef: "a", relativePath: "Pack/Season 1/Show - 02.mkv"}
	second := artifactFileContext{fileRef: "b", relativePath: "Pack/Season 2/Show - 02.mkv"}
	recordTargetVersion(&prep, selection, first)
	recordTargetVersion(&prep, selection, second)
	rendered := "/l/Show/Season 01/Show S01E02.mkv"
	if got := versionedRenderedTarget(prep, first, rendered); got != rendered {
		t.Fatalf("first copy = %s", got)
	}
	if got := versionedRenderedTarget(prep, second, rendered); got != "/l/Show/Season 01/Show S01E02 (2).mkv" {
		t.Fatalf("second copy = %s", got)
	}
}
