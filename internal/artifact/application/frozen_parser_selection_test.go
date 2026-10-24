package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
)

// frozenFileFactsSelection must rebuild the parser selection frozen on the
// Release media fact snapshot. Changing the instance's current parser policy
// after the snapshot was written must not change how an already-frozen file is
// parsed.
func TestFrozenFileFactsSelectionUsesFrozenConfigNotCurrentPolicy(t *testing.T) {
	frozenConfig, err := parser.MarshalProfileConfig([]string{`\[E(?P<episode>\d{1,4})\]`})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parser.NewSelection(parser.ParserRegex, frozenConfig, 0)
	if err != nil {
		t.Fatalf("build the frozen selection: %v", err)
	}

	selection, err := frozenFileFactsSelection(frozenParserEvidence{
		Kind:        string(parser.ParserRegex),
		Config:      frozenConfig,
		Fingerprint: frozen.ConfigFingerprint,
	}, 0)
	if err != nil {
		t.Fatalf("rebuild frozen selection: %v", err)
	}
	if string(selection.Parser.Config) != string(frozenConfig) {
		t.Fatalf("frozen config drifted: %s vs %s", selection.Parser.Config, frozenConfig)
	}
	if selection.ConfigFingerprint != frozen.ConfigFingerprint {
		t.Fatalf("frozen fingerprint drifted: %s vs %s", selection.ConfigFingerprint, frozen.ConfigFingerprint)
	}

	// The instance policy was edited after the snapshot: the current profile no
	// longer contains the bracketed form the frozen evidence was parsed with.
	current, err := parser.NewSelectionFromProfile(parser.ParserRegex, []string{`EP(?P<episode>\d+)`}, 0)
	if err != nil {
		t.Fatal(err)
	}

	const name = "[Group] Show Name [E08] [1080p].mkv"
	frozenEpisode, ok := parseEpisodeForTest(t, selection, name)
	if !ok || frozenEpisode != "8" {
		t.Fatalf("frozen profile did not parse the file: %q ok=%v", frozenEpisode, ok)
	}
	if currentEpisode, ok := parseEpisodeForTest(t, current, name); ok {
		t.Fatalf("the edited instance profile also parsed the file, so this proves nothing: %s", currentEpisode)
	}
}

// A nil config with a frozen fingerprint is a frozen profile that carried no
// episode patterns: the adapter's documented defaults apply.
func TestFrozenFileFactsSelectionUsesAdapterDefaultsForEmptyFrozenProfile(t *testing.T) {
	defaults, err := parser.NewSelectionFromProfile(parser.ParserRegex, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := frozenFileFactsSelection(frozenParserEvidence{
		Kind:        string(parser.ParserRegex),
		Fingerprint: defaults.ConfigFingerprint,
	}, 0)
	if err != nil {
		t.Fatalf("rebuild defaults selection: %v", err)
	}
	if len(selection.Parser.Config) != 0 {
		t.Fatalf("an empty frozen profile must stay empty, got %s", selection.Parser.Config)
	}
	if episode, ok := parseEpisodeForTest(t, selection, "[Group] Show Name - 07 [1080p].mkv"); !ok || episode != "7" {
		t.Fatalf("adapter defaults did not parse the file: %q ok=%v", episode, ok)
	}
}

// Rows written before the frozen config column existed carry the regex
// kind with no config and no fingerprint. That is the provable same-source
// value (the only writer at the time hardcoded it), so it still resolves.
func TestFrozenFileFactsSelectionReconstructsLegacyRegexRow(t *testing.T) {
	selection, err := frozenFileFactsSelection(frozenParserEvidence{Kind: string(parser.ParserRegex)}, 0)
	if err != nil {
		t.Fatalf("legacy regex row must be reconstructable: %v", err)
	}
	if selection.Kind() != parser.ParserRegex || len(selection.Parser.Config) != 0 {
		t.Fatalf("legacy reconstruction drifted: kind=%s config=%s", selection.Kind(), selection.Parser.Config)
	}
}

// A missing, contradicted, or unusable frozen parser fails closed and is
// reported — it never silently falls back to the current instance policy.
func TestFrozenFileFactsSelectionFailsClosed(t *testing.T) {
	const fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	cases := map[string]frozenParserEvidence{
		"no frozen kind":          {Config: json.RawMessage(`{"episode_patterns":["(\\d+)"]}`), Fingerprint: fingerprint},
		"unregistered kind":       {Kind: "custom-v9", Fingerprint: fingerprint},
		"legacy non-regex":  {Kind: "anibt-structured"},
		"invalid frozen config":   {Kind: string(parser.ParserRegex), Config: json.RawMessage(`{"episode_patterns":["("]}`), Fingerprint: fingerprint},
		"malformed frozen config": {Kind: string(parser.ParserRegex), Config: json.RawMessage(`{"episode_patterns":`), Fingerprint: fingerprint},
	}
	for name, evidence := range cases {
		if _, err := frozenFileFactsSelection(evidence, 0); err == nil {
			t.Errorf("%s: selection must fail closed", name)
		} else if !errors.Is(err, ErrFrozenParserEvidence) {
			t.Errorf("%s: error %v does not report frozen parser evidence", name, err)
		}
	}
}

// The artifact file-facts query must take the parser evidence AND the effective
// offset from the frozen snapshot only. Re-introducing a current-policy parser
// column or the current link episode_offset (or the removed default_offset /
// offset_override names) is the exact drift this slice removes.
func TestFileFactsContextQueryNeverReadsCurrentParserPolicy(t *testing.T) {
	if !strings.Contains(fileFactsContextQuery, "snap.parser_kind") ||
		!strings.Contains(fileFactsContextQuery, "snap.parser_config") ||
		!strings.Contains(fileFactsContextQuery, "snap.parser_config_fingerprint") {
		t.Fatalf("the file facts query must freeze parser evidence from the snapshot:\n%s", fileFactsContextQuery)
	}
	for _, currentOwnerColumn := range []string{
		"pp.parser_kind", "pp.parser_config", "pp.parser_config_fingerprint",
		"pp.default_offset", "src.offset_override", "src.episode_offset", "anime_business_configs",
	} {
		if strings.Contains(fileFactsContextQuery, currentOwnerColumn) {
			t.Fatalf("the file facts query reads current owner column %s:\n%s", currentOwnerColumn, fileFactsContextQuery)
		}
	}
}

func parseEpisodeForTest(t *testing.T, selection parser.Selection, name string) (string, bool) {
	t.Helper()
	parsed, err := parser.ParseEpisode(context.Background(), parser.DefaultRegistry(),
		parser.ParserSelection{ID: string(selection.Kind()), Config: selection.Parser.Config},
		parser.EpisodeParseInput{Kind: parser.InputFileName, Text: name})
	if err != nil {
		t.Fatalf("parse %q: %v", name, err)
	}
	if parsed.Status != parser.StatusValue || parsed.Episode == nil {
		return "", false
	}
	return string(*parsed.Episode), true
}

// Per-file recognition must consume the frozen effective offset, never the
// current link episode_offset. The frozen
// provenance is authoritative; a legacy snapshot without provenance still
// carries its own resolved offset on the identity.
func TestFrozenReleaseResolutionContextUsesOnlyFrozenOffset(t *testing.T) {
	provenance := parser.Provenance{Kind: string(parser.ParserRegex), Offset: 5}
	factsRaw, err := json.Marshal(facts.ReleaseFacts{
		Title:            "[Group] Show - 12 [1080p]",
		ParserProvenance: &provenance,
	})
	if err != nil {
		t.Fatalf("encode release facts: %v", err)
	}
	if offset := frozenReleaseOffset(factsRaw, nil); offset != 5 {
		t.Fatalf("offline/frozen offset = %d, want the frozen provenance offset 5", offset)
	}

	// A legacy row without provenance falls back to the frozen identity offset.
	legacyRaw, err := json.Marshal(map[string]any{"parser_kind": string(parser.ParserRegex)})
	if err != nil {
		t.Fatalf("encode legacy facts: %v", err)
	}
	identityRaw, err := facts.MarshalIdentity(episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode, Offset: 4,
	})
	if err != nil {
		t.Fatalf("encode legacy identity: %v", err)
	}
	if offset := frozenReleaseOffset(legacyRaw, identityRaw); offset != 4 {
		t.Fatalf("legacy offset = %d, want the frozen identity offset 4", offset)
	}
}

// A nil config with the canonical nil-config digest is a frozen profile that
// carried no episode patterns: the adapter's documented defaults apply. Any
// other non-NULL fingerprint with NULL config contradicts the frozen evidence
// and fails closed.
func TestFrozenFileFactsSelectionRejectsMismatchedFingerprintEvidence(t *testing.T) {
	validConfig, err := parser.MarshalProfileConfig([]string{`\[E(?P<episode>\d{1,4})\]`})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := parser.NewSelection(parser.ParserRegex, validConfig, 0)
	if err != nil {
		t.Fatalf("build valid frozen selection: %v", err)
	}
	otherConfig, err := parser.MarshalProfileConfig([]string{`EP(?P<episode>\d+)`})
	if err != nil {
		t.Fatal(err)
	}
	other, err := parser.NewSelection(parser.ParserRegex, otherConfig, 0)
	if err != nil {
		t.Fatalf("build other selection: %v", err)
	}
	if other.ConfigFingerprint == valid.ConfigFingerprint {
		t.Fatal("test setup: two different configs must fingerprint differently")
	}

	cases := map[string]frozenParserEvidence{
		// Config A paired with fingerprint B: the frozen evidence contradicts
		// itself, so the rebuild must fail closed instead of guessing.
		"config with another config's fingerprint": {
			Kind:        string(parser.ParserRegex),
			Config:      validConfig,
			Fingerprint: other.ConfigFingerprint,
		},
		// A present config without any fingerprint is unprovable: the writer
		// always freezes both, so this row cannot be trusted.
		"config without fingerprint": {
			Kind:   string(parser.ParserRegex),
			Config: validConfig,
		},
		// NULL config with a fingerprint that is not the canonical nil-config
		// digest: the fingerprint proves a config was hashed, but the config is
		// gone — never substitute the adapter defaults silently.
		"NULL config with non-nil-config fingerprint": {
			Kind:        string(parser.ParserRegex),
			Fingerprint: valid.ConfigFingerprint,
		},
		// NULL config with a well-formed but wrong-shaped fingerprint value.
		"NULL config with unrelated digest": {
			Kind:        string(parser.ParserRegex),
			Fingerprint: "sha256:" + strings.Repeat("a", 64),
		},
	}
	for name, evidence := range cases {
		if _, err := frozenFileFactsSelection(evidence, 0); err == nil {
			t.Errorf("%s: selection must fail closed", name)
		} else if !errors.Is(err, ErrFrozenParserEvidence) {
			t.Errorf("%s: error %v does not report frozen parser evidence", name, err)
		}
	}

	// The exact admissible branch: NULL config + the canonical nil-config
	// digest rebuilds the documented-defaults selection.
	selection, err := frozenFileFactsSelection(frozenParserEvidence{
		Kind:        string(parser.ParserRegex),
		Fingerprint: parser.NilConfigFingerprint(),
	}, 0)
	if err != nil {
		t.Fatalf("nil-config digest row must rebuild: %v", err)
	}
	if len(selection.Parser.Config) != 0 || selection.ConfigFingerprint != parser.NilConfigFingerprint() {
		t.Fatalf("nil-config digest rebuild drifted: config=%s fingerprint=%s", selection.Parser.Config, selection.ConfigFingerprint)
	}
}

// The file-facts context must resolve the metadata media kind/season binding
// exclusively from the frozen snapshot facts: current anime_metadata is
// never consulted, so a metadata edit after submission cannot drift per-file
// recognition.
func TestFileFactsContextQueryNeverReadsCurrentMetadata(t *testing.T) {
	if !strings.Contains(fileFactsContextQuery, "snap.release_facts") {
		t.Fatalf("the file facts query must freeze the media binding from the snapshot facts:\n%s", fileFactsContextQuery)
	}
	for _, currentOwnerColumn := range []string{"anime_metadata", "meta.tmdb_season", "meta.tmdb_media_kind"} {
		if strings.Contains(fileFactsContextQuery, currentOwnerColumn) {
			t.Fatalf("the file facts query reads current metadata column %s:\n%s", currentOwnerColumn, fileFactsContextQuery)
		}
	}

	legacyKind, legacySeason, legacyBound := facts.DecodeFrozenMediaBinding(nil)
	if legacyKind != "tv" || legacySeason != 0 || legacyBound {
		t.Fatalf("legacy binding = %q/%d/%v, want the tv default", legacyKind, legacySeason, legacyBound)
	}
	movieFacts, err := json.Marshal(facts.ReleaseFacts{Title: "t", MediaKind: "movie", Season: 0})
	if err != nil {
		t.Fatal(err)
	}
	kind, season, bound := facts.DecodeFrozenMediaBinding(movieFacts)
	if kind != "movie" || season != 0 || bound {
		t.Fatalf("movie binding = %q/%d/%v, want movie/0/false", kind, season, bound)
	}
	tvFacts, err := json.Marshal(facts.ReleaseFacts{Title: "t", MediaKind: "tv", Season: 2})
	if err != nil {
		t.Fatal(err)
	}
	kind, season, bound = facts.DecodeFrozenMediaBinding(tvFacts)
	if kind != "tv" || season != 2 || bound {
		t.Fatalf("tv binding = %q/%d/%v, want tv/2/false", kind, season, bound)
	}
}

// answer is distinguishable from the bundled parser, so a per-file recognition
// regression can prove which parser actually ran.
// nonDefaultFileParserAdapter is a registered non-default parser kind whose
// answer is distinguishable from the bundled parser, so a per-file recognition
// regression can prove which parser actually ran.
type nonDefaultFileParserAdapter struct{}

func (nonDefaultFileParserAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{
		ID:                    "test-artifact-frozen-parser",
		ImplementationVersion: "test-artifact-frozen-v1",
		OutputContractVersion: "episode-evidence-v1",
		DisplayName:           "Artifact frozen test parser",
		Capabilities:          []string{"file_name"},
		ConfigVersion:         "test-artifact-frozen-config-v1",
	}
}

func (nonDefaultFileParserAdapter) ValidateConfig(json.RawMessage) error { return nil }

func (nonDefaultFileParserAdapter) ParseDraft(_ context.Context, input parser.EpisodeParseInput, _ json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	if strings.TrimSpace(input.Text) == "" {
		return parser.EpisodeEvidenceDraft{Status: parser.StatusMissing, Shape: parser.ShapeUnknown}, nil
	}
	return parser.EpisodeEvidenceDraft{
		Status: parser.StatusValue, Shape: parser.ShapeSingle, RawCapture: "42", Episode: "42",
	}, nil
}

// Per-file recognition must run the frozen non-default kind, not the bundled
// parser: the frozen selection alone decides the per-file identity, and the
// bundled patterns would have produced a different episode from the same name.
func TestFrozenFileFactsSelectionUsesNonDefaultFrozenKind(t *testing.T) {
	const kind = "test-artifact-frozen-parser"
	if !parser.IsRegistered(parser.ParserKind(kind)) {
		if err := parser.DefaultRegistry().Register(nonDefaultFileParserAdapter{}); err != nil {
			t.Fatalf("register test parser: %v", err)
		}
	}
	frozenConfig, err := parser.MarshalProfileConfig([]string{`EP(?P<episode>\d+)`})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parser.NewSelection(parser.ParserKind(kind), frozenConfig, 0)
	if err != nil {
		t.Fatalf("build frozen selection: %v", err)
	}
	selection, err := frozenFileFactsSelection(frozenParserEvidence{
		Kind:        kind,
		Config:      frozenConfig,
		Fingerprint: frozen.ConfigFingerprint,
	}, 0)
	if err != nil {
		t.Fatalf("rebuild frozen selection: %v", err)
	}
	if selection.Kind() != parser.ParserKind(kind) {
		t.Fatalf("frozen selection kind = %q, want %q", selection.Kind(), kind)
	}

	// The bundled parser would read 07 from this name; the frozen kind says 42.
	const name = "[Group] Show Name - 07 [1080p].mkv"
	episode, ok := parseEpisodeForTest(t, selection, name)
	if !ok || episode != "42" {
		t.Fatalf("frozen non-default parser episode = %q ok=%v, want 42", episode, ok)
	}
	bundled, err := parser.NewSelectionFromProfile(parser.ParserRegex, []string{`EP(?P<episode>\d+)`}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bundledEpisode, ok := parseEpisodeForTest(t, bundled, name); ok && bundledEpisode == "42" {
		t.Fatalf("the bundled parser also produced 42, so this test proves nothing")
	}
}
