package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func newFileExtensionTestParser(t *testing.T) *Parser {
	t.Helper()
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return New(cfg)
}

func containerAttrs(res *model.ParseResult) []model.Attribute {
	var out []model.Attribute
	for _, a := range res.Attributes {
		if a.Type == model.AttrContainer {
			out = append(out, a)
		}
	}
	return out
}

func hasNote(res *model.ParseResult, normalized string) bool {
	for _, a := range res.Attributes {
		if a.Type == model.AttrNote && a.Normalized == normalized {
			return true
		}
	}
	return false
}

// A dual episode whose season episode echoes the season number ("S3 ... [03(51)]")

// keeps 03 as the episode and 51 as the overall alternate.

// The underscore dual form "[03_51]" in a file name is the same episode pair.

func TestParserFileExtension_ContainerSource(t *testing.T) {
	p := newFileExtensionTestParser(t)
	cases := []struct {
		raw        string
		want       string
		wantSource model.AttrSource
		wantNote   string
	}{
		{raw: "[Group] Title - 03 [1080p][MKV]", want: "MKV", wantSource: model.AttrSourceLexicon},
		{raw: "[Group] Title - 03 [1080p] MKV", want: "MKV", wantSource: model.AttrSourceLexicon},
		{raw: "[Group] Title - 03 [1080p].mkv", want: "MKV", wantSource: model.AttrSourceFileExtension},
		{raw: "[Group] Title - 03 [1080p][MKV].mkv", want: "MKV", wantSource: model.AttrSourceFileExtension},
		{raw: "[Group] Title - 03 [1080p][MKV].mp4", want: "MP4", wantSource: model.AttrSourceFileExtension, wantNote: "MKV"},
		{raw: "[Group] Title - 03 [1080p].rmvb", want: "RMVB", wantSource: model.AttrSourceFileExtension},
	}
	for _, tc := range cases {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("%s: parse failed: %v", tc.raw, err)
		}
		got := containerAttrs(res)
		if len(got) != 1 || got[0].Normalized != tc.want || got[0].Source != tc.wantSource {
			t.Fatalf("%s: container=%+v want one %s from %s", tc.raw, got, tc.want, tc.wantSource)
		}
		if tc.wantNote != "" && !hasNote(res, tc.wantNote) {
			t.Fatalf("%s: want declared %s kept as note, attrs=%+v", tc.raw, tc.wantNote, res.Attributes)
		}
		if got := canonicalEpisode(res); got != "3" {
			t.Fatalf("%s: episode=%q want=3", tc.raw, got)
		}
	}
}

// A single video file that names a season but no episode is not a season pack.

func TestParserFileExtension_SeasonOnlyFileIsUnknown(t *testing.T) {
	p := newFileExtensionTestParser(t)
	res, err := p.Parse("[BeanSub][Kusuriya no Hitorigoto S3][CHS][1080P].mp4")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != model.ContentKindUnknown {
		t.Fatalf("content_kind=%q want=UNKNOWN", got)
	}
	if got := canonicalTitle(res); got != "Kusuriya no Hitorigoto" {
		t.Fatalf("title=%q want=Kusuriya no Hitorigoto", got)
	}

	// The same name as a release title stays a season batch.
	res, err = p.Parse("[BeanSub][Kusuriya no Hitorigoto S3][CHS][1080P]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != model.ContentKindBatch {
		t.Fatalf("release title content_kind=%q want=BATCH", got)
	}
}

// Without an explicit season above 1 and an overall past the lead, "N_M" may

// be a range, so it is not read as a dual episode.
