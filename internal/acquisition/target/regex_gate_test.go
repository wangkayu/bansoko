package target

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"context"
	"testing"
)

// The release filter, the manifest path check and the batch plan acceptance
// precede the probe: a plan with nothing placeable is skipped, not failed.
func TestFilterGatePrecedesUnavailableProbe(t *testing.T) {
	for _, c := range []struct {
		name          string
		release, file animedomain.RegexFilter
		paths         []string
		reason        string
	}{
		{"all excluded", animedomain.RegexFilter{}, animedomain.RegexFilter{NotIn: "01"}, []string{"Show/01.mkv"}, "no_placeable_files"},
		{"nested only", animedomain.RegexFilter{}, animedomain.RegexFilter{}, []string{"Show/extras/01.mkv", "Show/cover.jpg"}, "no_placeable_files"},
		{"not_in on torrent name keeps the torrent", animedomain.RegexFilter{}, animedomain.RegexFilter{NotIn: "Show"}, []string{"Show/01.mkv"}, "target_probe_unavailable"},
		{"unknown only under skip", animedomain.RegexFilter{}, animedomain.RegexFilter{}, []string{"Show/SP.mkv"}, "no_placeable_files"},
		{"title excluded", animedomain.RegexFilter{NotIn: "Show"}, animedomain.RegexFilter{}, []string{"Show/main/01.mkv"}, "release_not_in_matched"},
		{"invalid path", animedomain.RegexFilter{}, animedomain.RegexFilter{}, []string{"../01.mkv"}, "file_filter_path_invalid"},
		{"partial by filename", animedomain.RegexFilter{}, animedomain.RegexFilter{MustContain: "^01"}, []string{"Show/01.mkv", "Show/02.mkv"}, "target_probe_unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input := PlanInput{Item: acquisitiondomain.FeedItem{Title: "Show"}, Effective: animedomain.EffectiveConfig{}, ParserKind: "regex", UnclassifiedRoute: "skip"}
			input.Effective.Rules.ReleaseFilter = c.release
			input.Effective.Rules.FileFilter = c.file
			for _, p := range c.paths {
				input.Item.ReleaseFiles = append(input.Item.ReleaseFiles, acquisitiondomain.ReleaseFile{SourcePath: p})
			}
			result := preflight(context.Background(), input, nil)
			if result.Admission.Reason != c.reason {
				t.Fatalf("got %s want %s", result.Admission.Reason, c.reason)
			}
			if len(result.ReleaseFiles) != len(c.paths) {
				t.Fatal("download inventory narrowed")
			}
		})
	}
}
