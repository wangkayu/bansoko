package target

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/batchplan"
)

func TestParserEvidenceFreezesSpanForReleaseAndFileIdentities(t *testing.T) {
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Span: 2,
	}, "Show - 02")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Span != 2 || evidence.Identity.Episode == nil || *evidence.Identity.Episode != "3" ||
		evidence.Identity.EpisodeEnd == nil || *evidence.Identity.EpisodeEnd != "4" {
		t.Fatalf("release evidence = %+v, want frozen span 2 and episodes 3-4", evidence)
	}

	identity, facts, err := ResolveFileEpisode(context.Background(), evidence, "Show - 02.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Number != "3" || facts.NumberEnd != "4" ||
		strings.Join(identity.Keys(), ",") != "tv:e3,tv:e4" {
		t.Fatalf("file identity = %+v, facts = %+v, want episodes 3-4", identity, facts)
	}

	identity, facts, err = ResolveFileEpisode(context.Background(), evidence, "Show - 01-02.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Number != "1" || facts.NumberEnd != "2" ||
		strings.Join(identity.Keys(), ",") != "tv:e1,tv:e2" {
		t.Fatalf("explicit range was multiplied by span: identity = %+v, facts = %+v", identity, facts)
	}
}

func TestBuildBatchPlanCarriesSpanCoverageIntoCompetition(t *testing.T) {
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Span: 2,
	}, "Show - 02")
	if err != nil {
		t.Fatal(err)
	}
	size := int64(1024)
	plan, err := BuildBatchPlan(context.Background(), BatchPlanInput{
		Evidence: evidence,
		Files: []acquisitiondomain.ReleaseFile{{
			SourcePath: "Show - 02.mkv", Filename: "Show - 02.mkv", SizeBytes: &size, MediaKind: "video",
		}},
		UnclassifiedRoute: batchplan.RouteSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.CoverageKeys, ","); got != "tv:e3,tv:e4" {
		t.Fatalf("batch plan coverage = %s, want tv:e3,tv:e4", got)
	}
}

func TestReleaseMediaInputDigestKeepsDefaultSpanCompatible(t *testing.T) {
	base, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Offset: 2,
	}, "Show - 02")
	if err != nil {
		t.Fatal(err)
	}
	input := ReleaseMediaSnapshotInput{Title: "Show - 02", Evidence: base}
	legacyDigest := ReleaseMediaInputDigest(input)
	legacyProvenance, err := json.Marshal(base.Provenance())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyProvenance), `"span"`) {
		t.Fatalf("default span changed legacy provenance: %s", legacyProvenance)
	}

	explicitDefault := base
	explicitDefault.Span = 1
	input.Evidence = explicitDefault
	if got := ReleaseMediaInputDigest(input); got != legacyDigest {
		t.Fatalf("explicit default span changed digest: %q != %q", got, legacyDigest)
	}

	combined, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Offset: 2, Span: 2,
	}, "Show - 02")
	if err != nil {
		t.Fatal(err)
	}
	input.Evidence = combined
	combinedProvenance, err := json.Marshal(combined.Provenance())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(combinedProvenance), `"span":2`) {
		t.Fatalf("combined provenance lost span: %s", combinedProvenance)
	}
	if got := ReleaseMediaInputDigest(input); got == legacyDigest {
		t.Fatal("a combined-episode span must change the frozen media input digest")
	}
}
