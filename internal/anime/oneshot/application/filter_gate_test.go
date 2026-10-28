package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"testing"
)

func TestOneShotFilterGateIndependentOfTargetConfiguration(t *testing.T) {
	item := acquisitiondomain.FeedItem{Title: "Show", ReleaseFiles: []acquisitiondomain.ReleaseFile{{SourcePath: "Show/01.mkv"}, {SourcePath: "Show/SPs/01.mkv"}, {SourcePath: "Show/01.chs.ass"}}}
	// not_in on the torrent name never excludes the torrent; the rule sees filenames.
	for _, c := range []struct{ must, exclude, code string }{{"", "01", "FILE_SELECTION_EMPTY"}, {"", "Show", ""}, {"^01", "", ""}, {"[", "", "FILTER_INVALID"}} {
		effective := animedomain.EffectiveConfig{}
		effective.Rules.FileFilter = animedomain.RegexFilter{MustContain: c.must, NotIn: c.exclude}
		if got := typedErrorCode(validateOneShotFilterGate(item, effective, nil)); got != c.code {
			t.Fatalf("got %s want %s", got, c.code)
		}
	}
}
