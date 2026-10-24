package parser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The automatic pipeline must have exactly one explicit selection built from
// the instance parser policy kind plus the effective episode patterns, and that
// one profile must govern both the Release title and the torrent file name.
func TestNewSelectionFromProfilePinsOneProfileForEveryInputKind(t *testing.T) {
	// A profile the bundled defaults would not match: a bracketed "[E##]" form.
	patterns := []string{`\[E(?P<episode>\d{1,4})\]`}
	selection, err := NewSelectionFromProfile(ParserRegex, patterns, 3)
	if err != nil {
		t.Fatalf("build selection: %v", err)
	}
	if selection.Kind() != ParserRegex {
		t.Fatalf("selection kind = %q, want %q", selection.Kind(), ParserRegex)
	}
	if selection.Offset != 3 {
		t.Fatalf("selection offset = %d, want the instance offset 3", selection.Offset)
	}
	if !strings.HasPrefix(selection.ConfigFingerprint, "sha256:") || len(selection.ConfigFingerprint) != len("sha256:")+64 {
		t.Fatalf("config fingerprint has unexpected shape: %q", selection.ConfigFingerprint)
	}
	if selection.Descriptor.ID != ParserRegex || selection.Descriptor.OutputContractVersion == "" {
		t.Fatalf("selection descriptor is incomplete: %+v", selection.Descriptor)
	}

	registry := DefaultRegistry()
	explicit := ParserSelection{ID: string(selection.Kind()), Config: selection.Parser.Config}

	release, err := ParseEpisode(context.Background(), registry, explicit, EpisodeParseInput{
		Kind: InputReleaseTitle, Text: "[Group] Show Name [E07] [1080p]",
	})
	if err != nil {
		t.Fatalf("parse release title: %v", err)
	}
	if release.Status != StatusValue || release.Episode == nil || string(*release.Episode) != "7" {
		t.Fatalf("release title did not use the selected profile: %+v", release)
	}
	// The parser never applies the offset; the resolver does, exactly once.
	if release.Episode != nil && string(*release.Episode) == "10" {
		t.Fatal("parser applied the instance offset; offset must stay out of the adapter")
	}

	file, err := ParseEpisode(context.Background(), registry, explicit, EpisodeParseInput{
		Kind: InputFileName, Text: "[Group] Show Name [E08] [1080p].mkv",
	})
	if err != nil {
		t.Fatalf("parse file name: %v", err)
	}
	if file.Status != StatusValue || file.Episode == nil || string(*file.Episode) != "8" {
		t.Fatalf("file name did not use the same selected profile: %+v", file)
	}

	// The selected profile governs the result: on a title where the bundled
	// defaults would capture a different number, the explicit profile wins, so
	// this cannot be an accidental default match.
	discriminating := "Show Name - 07v2 [E09] [1080p]"
	withoutProfile, err := ParseEpisode(context.Background(), registry, ParserSelection{ID: ParserRegex}, EpisodeParseInput{
		Kind: InputReleaseTitle, Text: discriminating,
	})
	if err != nil {
		t.Fatalf("parse without profile: %v", err)
	}
	withProfile, err := ParseEpisode(context.Background(), registry, explicit, EpisodeParseInput{
		Kind: InputReleaseTitle, Text: discriminating,
	})
	if err != nil {
		t.Fatalf("parse with profile: %v", err)
	}
	if withProfile.Episode == nil || string(*withProfile.Episode) != "9" {
		t.Fatalf("selected profile did not govern the parse: %+v", withProfile)
	}
	if withoutProfile.Episode == nil || string(*withoutProfile.Episode) == "9" {
		t.Fatalf("the default profile produced the same result, so this proves nothing: %+v", withoutProfile)
	}
}

func TestMarshalProfileConfigOmitsEmptyPatterns(t *testing.T) {
	for _, patterns := range [][]string{nil, {}} {
		config, err := MarshalProfileConfig(patterns)
		if err != nil {
			t.Fatalf("marshal empty patterns: %v", err)
		}
		if config != nil {
			t.Fatalf("empty patterns must yield a nil config (adapter defaults), got %s", config)
		}
	}
	config, err := MarshalProfileConfig([]string{`\[E(?P<episode>\d+)\]`})
	if err != nil {
		t.Fatalf("marshal patterns: %v", err)
	}
	selection, err := NewSelectionFromProfile(ParserRegex, []string{`\[E(?P<episode>\d+)\]`}, 0)
	if err != nil {
		t.Fatalf("build selection: %v", err)
	}
	if string(selection.Parser.Config) != string(config) {
		t.Fatalf("selection config drifted from the marshalled profile: %s vs %s", selection.Parser.Config, config)
	}
}

func TestSelectionFingerprintIsStableAndProfileSensitive(t *testing.T) {
	first, err := NewSelectionFromProfile(ParserRegex, []string{`\[E(?P<episode>\d+)\]`}, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSelectionFromProfile(ParserRegex, []string{`\[E(?P<episode>\d+)\]`}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConfigFingerprint != second.ConfigFingerprint {
		t.Fatalf("fingerprint is not deterministic: %q != %q", first.ConfigFingerprint, second.ConfigFingerprint)
	}
	changed, err := NewSelectionFromProfile(ParserRegex, []string{`\[EP(?P<episode>\d+)\]`}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConfigFingerprint == first.ConfigFingerprint {
		t.Fatal("fingerprint did not change when the effective profile changed")
	}
	defaults, err := NewSelectionFromProfile(ParserRegex, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ConfigFingerprint == first.ConfigFingerprint {
		t.Fatal("fingerprint did not change between an explicit profile and adapter defaults")
	}
}

func TestNewSelectionFromProfileFailsClosedForUnregisteredKind(t *testing.T) {
	if _, err := NewSelectionFromProfile(ParserKind("anibt-structured"), nil, 0); err == nil {
		t.Fatal("unregistered parser kind must fail closed")
	}
}

func TestNewSelectionFromProfileRejectsInvalidPattern(t *testing.T) {
	if _, err := NewSelectionFromProfile(ParserRegex, []string{"("}, 0); err == nil {
		t.Fatal("an invalid episode pattern must fail closed")
	}
}

func TestNewSelectionCanonicalizesFrozenJSONConfig(t *testing.T) {
	compact, err := NewSelection(ParserRegex, json.RawMessage(`{"episode_patterns":["x"]}`), 0)
	if err != nil {
		t.Fatalf("build compact selection: %v", err)
	}
	formatted, err := NewSelection(ParserRegex, json.RawMessage(`{
  "episode_patterns": ["x"]
}`), 0)
	if err != nil {
		t.Fatalf("build formatted selection: %v", err)
	}
	if compact.ConfigFingerprint != formatted.ConfigFingerprint {
		t.Fatalf("JSON formatting changed config fingerprint: %q != %q", compact.ConfigFingerprint, formatted.ConfigFingerprint)
	}
	if string(compact.Parser.Config) != string(formatted.Parser.Config) {
		t.Fatalf("JSON formatting changed frozen config: %s != %s", compact.Parser.Config, formatted.Parser.Config)
	}
}
