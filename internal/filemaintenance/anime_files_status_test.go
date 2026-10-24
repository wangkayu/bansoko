package filemaintenance

import "testing"

// A placement the alignment moved after the latest scan is aligned when it
// sits at the rendered target, else unscanned; never no_target just because
// the stale observation resolves no target.
func TestVideoStatusOfPlacementMovedAfterScan(t *testing.T) {
	target := "/library/Show (2021)/S02E01.mkv"
	moved := animeFilesPlacement{Path: target}
	for name, tc := range map[string]struct {
		diff PathDiffView
		want string
	}{
		"stale observation without target": {PathDiffView{FilePath: "/library/Show 2021/S02E01.mkv", Disposition: DispositionPresent, TargetUnavailable: "placement_not_found"}, AnimeFileUnscanned},
		"moved onto target":                {PathDiffView{FilePath: "/library/Show 2021/S02E01.mkv", Disposition: DispositionPresent, ExpectedPath: &target, Differs: true}, AnimeFileAligned},
		"unmoved without target":           {PathDiffView{FilePath: target, Disposition: DispositionPresent, TargetUnavailable: "coverage_unresolved"}, AnimeFileNoTarget},
	} {
		if got, _, _ := videoStatus(moved, tc.diff); got != tc.want {
			t.Errorf("%s: status = %q, want %q", name, got, tc.want)
		}
	}
}
