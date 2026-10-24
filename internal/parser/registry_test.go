package parser

import (
	"testing"

	"bansoko/internal/episodeidentity"
)

func TestSelectionUsesAuthoritativeRegistryAndCopiesConfig(t *testing.T) {
	config := []byte(`{"episode_patterns":["x"]}`)
	selection, err := NewSelection(ParserRegex, config, 0)
	if err != nil {
		t.Fatalf("NewSelection: %v", err)
	}
	config[2] = 'y'
	if selection.Kind() != ParserRegex || string(selection.Parser.Config) != `{"episode_patterns":["x"]}` {
		t.Fatalf("selection=%+v, want copied regex config", selection)
	}
}

func TestRegistryFailsClosedForUnregisteredKind(t *testing.T) {
	if _, err := DefaultRegistry().Resolve("not-a-parser"); err == nil {
		t.Fatal("Resolve(unregistered) succeeded, want fail-closed error")
	}
	if IsRegistered("not-a-parser") {
		t.Fatal("IsRegistered(unregistered) = true, want false")
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	// The regex adapter may already be registered by TestMain-less
	// package init in production wiring; register under a fresh kind to test
	// the duplicate guard without mutating the production kind.
	registry := NewRegistry()
	if err := registry.Register(RegexRuleAdapter{}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := registry.Register(RegexRuleAdapter{}); err == nil {
		t.Fatal("duplicate Register must be rejected")
	}
}

func TestRegexAdapterValueEvidence(t *testing.T) {
	adapter := regexEvidence{}
	result, err := adapter.Parse(titleInput{
		Kind:  InputReleaseTitle,
		Title: "[SampleGroup] 进击的巨人 3 - 06 [1080P][Baha][WEB-DL][AAC AVC][CHT]",
	})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	evidence := result.Evidence
	if evidence.Status != episodeidentity.StatusValue {
		t.Fatalf("status=%s, want value", evidence.Status)
	}
	if evidence.Episode == nil || *evidence.Episode != "6" {
		t.Fatalf("episode=%v, want 6", evidence.Episode)
	}
	if evidence.Source != episodeidentity.EpisodeSourceReleaseParser {
		t.Fatalf("source=%s, want release_parser", evidence.Source)
	}
}

func TestRegexAdapterKeepsDecimal(t *testing.T) {
	adapter := regexEvidence{}
	result, err := adapter.Parse(titleInput{
		Kind:  InputFileName,
		Title: "[Group] Show - 07.5 [1080p]",
	})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.Evidence.Episode == nil || *result.Evidence.Episode != "7.5" {
		t.Fatalf("episode=%v, want 7.5 preserved", result.Evidence.Episode)
	}
}

func TestRegexAdapterNeverAppliesOffset(t *testing.T) {
	adapter := regexEvidence{}
	result, err := adapter.Parse(titleInput{
		Kind:  InputReleaseTitle,
		Title: "[Group] Show - 12 [1080p]",
	})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.Evidence.Episode == nil || *result.Evidence.Episode != "12" {
		t.Fatalf("episode=%v, want raw 12 (adapter must not apply offset)", result.Evidence.Episode)
	}
}

func TestRegexAdapterClassifiesUnsupportedShapes(t *testing.T) {
	adapter := regexEvidence{}
	tests := []struct {
		title string
		shape string
	}{
		{"[Group] Show - 09~16 [1080p]", episodeidentity.ShapeRange},
		{"[Group] Show - 09-16 [1080p]", episodeidentity.ShapeRange},
		{"[Group] Show 第01-12话 [1080p]", episodeidentity.ShapeRange},
		{"Show S01E01-E04 1080p", episodeidentity.ShapeRange},
		{"Show S01E01E02 1080p", episodeidentity.ShapeList},
		{"[Group][Show][01][02][1080p]", episodeidentity.ShapeList},
		{"[Group] Show S2 [1080p]", episodeidentity.ShapeSeasonOnly},
		{"[Group] Show [SP] [1080p]", episodeidentity.ShapeSpecial},
		{"[Group] Show Special [1080p]", episodeidentity.ShapeSpecial},
	}
	for _, tt := range tests {
		result, err := adapter.Parse(titleInput{Kind: InputReleaseTitle, Title: tt.title})
		if err != nil {
			t.Fatalf("%q: Parse error: %v", tt.title, err)
		}
		if result.Evidence.Status != episodeidentity.StatusUnsupported {
			t.Fatalf("%q: status=%s, want unsupported", tt.title, result.Evidence.Status)
		}
		if result.Evidence.Shape != tt.shape {
			t.Fatalf("%q: shape=%s, want %s", tt.title, result.Evidence.Shape, tt.shape)
		}
	}
}

// A single episode written with surrounding tags must stay a scalar episode:
// the multi-episode detection only fires on genuine ranges and lists.
func TestRegexAdapterKeepsScalarEpisodes(t *testing.T) {
	adapter := regexEvidence{}
	for title, want := range map[string]string{
		"[Group] Show - 05 [1080p]":      "5",
		"[Group][Show][05][1080p][CHS]":  "5",
		"[Group] Show S02E05 [1080p]":    "5",
		"[Group] Show 第05话 [1080p]":      "5",
		"[Group][Show][2024][05][1080p]": "5",
	} {
		result, err := adapter.Parse(titleInput{Kind: InputReleaseTitle, Title: title})
		if err != nil {
			t.Fatalf("%q: %v", title, err)
		}
		if result.Evidence.Status != episodeidentity.StatusValue || result.Evidence.Episode == nil || string(*result.Evidence.Episode) != want {
			t.Fatalf("%q: evidence=%+v, want episode %s", title, result.Evidence, want)
		}
	}
}

func TestRegexAdapterEmptyTitleIsMissing(t *testing.T) {
	adapter := regexEvidence{}
	result, err := adapter.Parse(titleInput{Kind: InputFileName, Title: "  "})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.Evidence.Status != episodeidentity.StatusMissing {
		t.Fatalf("status=%s, want missing", result.Evidence.Status)
	}
}

func TestSelectionProvenanceCarriesDescriptorAndFingerprint(t *testing.T) {
	selection, err := NewSelectionFromProfile(ParserRegex, []string{`\[E(?P<episode>\d+)\]`}, 0)
	if err != nil {
		t.Fatalf("build selection: %v", err)
	}
	provenance := selection.Provenance()
	if provenance.Kind != ParserRegex {
		t.Fatalf("provenance kind = %q, want %q", provenance.Kind, ParserRegex)
	}
	if provenance.ConfigFingerprint != selection.ConfigFingerprint {
		t.Fatalf("provenance fingerprint = %q, want %q", provenance.ConfigFingerprint, selection.ConfigFingerprint)
	}
	if provenance.ConfigVersion == "" || provenance.ImplementationVersion == "" || provenance.OutputContractVersion == "" {
		t.Fatalf("provenance lost its descriptor versions: %+v", provenance)
	}
	// A parse digest is filled by the caller that ran the selection; it must
	// never be invented here.
	if provenance.InputDigest != "" || provenance.ResultDigest != "" {
		t.Fatalf("provenance invented parse digests: %+v", provenance)
	}
}

func TestDescribeProvenanceKeepsFrozenKindWhenItIsOffline(t *testing.T) {
	known := DescribeProvenance(ParserRegex, "sha256:frozen")
	if known.Kind != ParserRegex || known.ConfigFingerprint != "sha256:frozen" {
		t.Fatalf("known kind provenance = %+v", known)
	}
	if known.ImplementationVersion == "" {
		t.Fatalf("known kind provenance lost its descriptor: %+v", known)
	}
	offline := DescribeProvenance(ParserKind("test-offline-provenance-kind"), "sha256:gone")
	if offline.Kind != "test-offline-provenance-kind" || offline.ConfigFingerprint != "sha256:gone" {
		t.Fatalf("offline kind provenance dropped the frozen evidence: %+v", offline)
	}
	if offline.ImplementationVersion != "" || offline.ConfigVersion != "" || offline.OutputContractVersion != "" {
		t.Fatalf("offline kind provenance invented descriptor versions: %+v", offline)
	}
}

type titleInput struct {
	Kind  InputKind
	Title string
}

type evidenceResult struct {
	Evidence episodeidentity.EpisodeEvidence
}

// regexEvidence runs the bundled parser through the authoritative
// registry and returns the pre-offset episode evidence.
type regexEvidence struct{}

func (regexEvidence) Parse(input titleInput) (evidenceResult, error) {
	result, err := ParseEpisode(nil, DefaultRegistry(), ParserSelection{ID: ParserRegex}, EpisodeParseInput{Kind: input.Kind, Text: input.Title})
	if err != nil {
		return evidenceResult{}, err
	}
	return evidenceResult{Evidence: Evidence(result, input.Kind)}, nil
}
