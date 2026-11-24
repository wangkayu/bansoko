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

func TestParserEvidenceOffsetIsConfigurationNotIdentityFallback(t *testing.T) {
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Offset: 4,
	}, "Show - 02")
	if err != nil {
		t.Fatal(err)
	}
	// Neither a stale selection nor the release-title identity may override the
	// explicit configuration captured on ParserEvidence.
	evidence.Selection.Offset = 99
	evidence.Identity.Offset = 77
	identity, facts, err := ResolveFileEpisode(context.Background(), evidence, "Show - 02.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Offset != 4 || identity.Episode == nil || *identity.Episode != "6" || facts == nil || facts.Offset != 4 || evidence.Provenance().Offset != 4 {
		t.Fatalf("configured offset was not authoritative: identity=%+v facts=%+v provenance=%+v", identity, facts, evidence.Provenance())
	}
}

func TestBatchReleaseKeepsOffsetAndSpanForPerFileResolution(t *testing.T) {
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{
		Kind: "reltitle", MediaKind: "tv", Offset: 4, Span: 2,
	}, "[LoliHouse] 我与尼特女忍者的莫名同居生活 / NEET Kunoichi [01-12+EX 合集][WebRip 1080p HEVC-10bit AAC][简繁内封字幕][Fin]")
	if err != nil {
		t.Fatal(err)
	}
	// A batch title carries ShapeRange and never resolves a single release-level
	// identity, yet its configured offset and span must survive in provenance
	// and apply to every file in the torrent.
	if provenance := evidence.Provenance(); provenance.Offset != 4 || provenance.Span != 2 {
		t.Fatalf("batch provenance = %+v, want offset 4 and span 2", provenance)
	}
	identity, facts, err := ResolveFileEpisode(context.Background(), evidence, "[LoliHouse] NEET Kunoichi - 12 [WebRip 1080p HEVC-10bit AAC].mkv")
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Raw != "23" || facts.Number != "27" || facts.NumberEnd != "28" || facts.Offset != 4 ||
		strings.Join(identity.Keys(), ",") != "tv:e27,tv:e28" {
		t.Fatalf("batch file identity = %+v, facts = %+v, want offset 4 + span 2 => episodes 27-28", identity, facts)
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
