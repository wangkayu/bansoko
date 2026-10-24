package parser

import (
	"context"
	"encoding/json"
	"testing"
)

func parseWith(t *testing.T, kind string, input InputKind, title string) EpisodeParseResult {
	t.Helper()
	result, err := ParseEpisode(context.Background(), NewDefaultRegistry(), ParserSelection{ID: kind}, EpisodeParseInput{Kind: input, Text: title})
	if err != nil {
		t.Fatalf("%s %q: %v", kind, title, err)
	}
	return result
}

func episodeText(r EpisodeParseResult) string {
	if r.Episode == nil {
		return ""
	}
	return string(*r.Episode)
}

// Real SampleGroup / Mikan-style titles: where the two kinds agree and
// where they differ, reltitle's typed result is the golden.
func TestReltitleEpisodeGolden(t *testing.T) {
	cases := []struct {
		title        string
		input        InputKind
		status       ParseStatus
		shape        ParseShape
		episode      string
		regexStatus  ParseStatus
		regexShape   ParseShape
		regexEpisode string
	}{
		{"[SampleGroup] 进击的巨人 3 - 06 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", InputReleaseTitle, StatusValue, ShapeSingle, "6", StatusValue, ShapeSingle, "6"},
		{"[SampleGroup] Kaijuu 8-gou /怪獸8號 - 01 [1080P][Baha][WEB-DL]", InputReleaseTitle, StatusValue, ShapeSingle, "1", StatusValue, ShapeSingle, "1"},
		{"[SampleGroup] 【我推的孩子】 第三季 / Oshi no Ko S3 - 09 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", InputReleaseTitle, StatusValue, ShapeSingle, "9", StatusValue, ShapeSingle, "9"},
		{"[Group] Show - 07.5 [1080p]", InputReleaseTitle, StatusValue, ShapeSingle, "7.5", StatusValue, ShapeSingle, "7.5"},
		// Ranges stay unsupported for both kinds.
		{"[7³ACG] 【我推的孩子】/Oshi no Ko S01 | 25-35 [简繁字幕] BDrip 1080p AV1 OPUS 2.0", InputReleaseTitle, StatusUnsupported, ShapeRange, "", StatusUnsupported, ShapeRange, ""},
		// Differences: regex reads the season digit or the resolution as an episode.
		{"[Group] Show Season 2 [1080p]", InputReleaseTitle, StatusUnsupported, ShapeSeasonOnly, "", StatusValue, ShapeSingle, "2"},
		{"Show.Name.S02E05.1080p.WEB-DL.mkv", InputFileName, StatusValue, ShapeSingle, "5", StatusValue, ShapeSingle, "5.108"},
		// No episode at all is missing, never a guess.
		{"[Group] Show 2024 [1080p]", InputReleaseTitle, StatusMissing, ShapeUnknown, "", StatusUnsupported, ShapeUnknown, ""},
	}
	for _, tc := range cases {
		got := parseWith(t, ParserReltitle, tc.input, tc.title)
		if got.Status != tc.status || got.Shape != tc.shape || episodeText(got) != tc.episode {
			t.Errorf("reltitle %q: got %s/%s/%q want %s/%s/%q", tc.title, got.Status, got.Shape, episodeText(got), tc.status, tc.shape, tc.episode)
		}
		regex := parseWith(t, ParserRegex, tc.input, tc.title)
		if regex.Status != tc.regexStatus || regex.Shape != tc.regexShape || episodeText(regex) != tc.regexEpisode {
			t.Errorf("regex %q: got %s/%s/%q want %s/%s/%q", tc.title, regex.Status, regex.Shape, episodeText(regex), tc.regexStatus, tc.regexShape, tc.regexEpisode)
		}
	}
}

func TestReltitleRefusesConfigAndIgnoresProfilePatterns(t *testing.T) {
	adapter := ReltitleAdapter{}
	if err := adapter.ValidateConfig(json.RawMessage(`{"episode_patterns":["x"]}`)); err == nil {
		t.Fatal("reltitle must refuse episode patterns")
	}
	if err := adapter.ValidateConfig(nil); err != nil {
		t.Fatal(err)
	}
	selection, err := NewSelectionFromProfile(ParserKind(ParserReltitle), []string{`(\d+)`}, 0)
	if err != nil {
		t.Fatalf("profile patterns must not reach reltitle: %v", err)
	}
	if selection.ConfigFingerprint != NilConfigFingerprint() {
		t.Fatalf("reltitle selection must carry the nil config, got %s", selection.ConfigFingerprint)
	}
}

func TestInputDigestCarriesVersions(t *testing.T) {
	descriptor := ParserDescriptor{ID: "t", ImplementationVersion: "impl-1", OutputContractVersion: "c", ConfigVersion: "cfg-1"}
	input := EpisodeParseInput{Kind: InputReleaseTitle, Text: "Show - 01"}
	draft := EpisodeEvidenceDraft{Status: StatusValue, Shape: ShapeSingle, Episode: "1"}
	base, err := Normalize(descriptor, nil, input, draft)
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]ParserDescriptor{
		"implementation": {ID: "t", ImplementationVersion: "impl-2", OutputContractVersion: "c", ConfigVersion: "cfg-1"},
		"config":         {ID: "t", ImplementationVersion: "impl-1", OutputContractVersion: "c", ConfigVersion: "cfg-2"},
	} {
		other, err := Normalize(changed, nil, input, draft)
		if err != nil {
			t.Fatal(err)
		}
		if other.InputDigest == base.InputDigest {
			t.Errorf("%s version change must change the input digest", name)
		}
	}
}

func TestReltitleDescriptorPinsVersions(t *testing.T) {
	descriptor := ReltitleAdapter{}.Descriptor()
	if descriptor.ImplementationVersion == "" || descriptor.ConfigVersion == "" {
		t.Fatalf("reltitle must pin implementation and lexicon versions: %+v", descriptor)
	}
}
