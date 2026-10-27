package batchplan

import (
	"reflect"
	"strings"
	"testing"

	"bansoko/internal/filekinds"
)

func classifySelected(t *testing.T, route string, selected []string, paths ...string) Plan {
	t.Helper()
	files := make([]InputFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, InputFile{SourcePath: path})
	}
	plan, err := Classify(Input{
		FileKindsVersion: filekinds.Version, UnclassifiedRoute: route, Files: files, Identity: identityOf,
		Selection: filekinds.NewSelection(selected),
		RuleExclusion: func(sourcePath string) string {
			if strings.Contains(sourcePath, "NCOP") {
				return "file_not_in"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A manual selection replaces the first-level gate only: a chosen file at any
// depth is planned like a first-level one, an unchosen file is
// ignored_unselected, the kind gate still ignores other files, and a deep
// subtitle binds to the chosen video in its own folder.
func TestClassifyManualSelectionReplacesTheFirstLevelGate(t *testing.T) {
	paths := []string{
		"Pack/Show - 01.mkv", "Pack/Show - 01.chs.ass",
		"Pack/S2/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass", "Pack/S2/Show - 02.mkv",
		"Pack/S2/cover.jpg",
	}
	plan := classifySelected(t, RouteSkip, []string{"Pack/S2/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass", "Pack/S2/cover.jpg"}, paths...)
	want := map[string]string{
		"Pack/Show - 01.mkv":        "ignored_unselected/ignored_unselected",
		"Pack/Show - 01.chs.ass":    "ignored_unselected/ignored_unselected",
		"Pack/S2/Show - 01.mkv":     "planned/",
		"Pack/S2/Show - 01.chs.ass": "subtitle_follows/",
		"Pack/S2/Show - 02.mkv":     "ignored_unselected/ignored_unselected",
		"Pack/S2/cover.jpg":         "ignored_kind/ignored_kind",
	}
	if got := outcomes(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %v\nwant %v", got, want)
	}
	if file, _ := plan.File("Pack/S2/Show - 01.chs.ass"); file.BoundVideo != "Pack/S2/Show - 01.mkv" {
		t.Fatalf("the deep subtitle binds %q, want the video in its folder", file.BoundVideo)
	}
	if !reflect.DeepEqual(plan.SelectedPaths, []string{"Pack/S2/Show - 01.chs.ass", "Pack/S2/Show - 01.mkv", "Pack/S2/cover.jpg"}) {
		t.Fatalf("selected paths = %v", plan.SelectedPaths)
	}
	if !plan.Accepted() {
		t.Fatal("a selection with one keyed video is accepted")
	}
}

// A subtitle never binds across folders, and without a selection the default
// plan is the first-level plan with no frozen selection.
func TestClassifySubtitleBindsOnlyInItsFolderAndDefaultIsUnchanged(t *testing.T) {
	plan := classifySelected(t, RouteSkip, []string{"A/Show - 01.mkv", "B/Show - 01.chs.ass"}, "A/Show - 01.mkv", "B/Show - 01.chs.ass")
	if got := outcomes(plan)["B/Show - 01.chs.ass"]; got != "subtitle_unbound/subtitle_binding_missing" {
		t.Fatalf("a subtitle in another folder = %s, want unbound", got)
	}
	defaults := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/SPs/Show - 02.mkv")
	if defaults.SelectedPaths != nil || defaults.Selection() != nil {
		t.Fatalf("the default plan freezes no selection: %v", defaults.SelectedPaths)
	}
	if got := outcomes(defaults)["Show/SPs/Show - 02.mkv"]; got != "ignored_nested/ignored_nested" {
		t.Fatalf("the default plan still ignores a nested file: %s", got)
	}
}
