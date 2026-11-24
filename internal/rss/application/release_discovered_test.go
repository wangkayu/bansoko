package application

import (
	"context"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/batchplan"
)

func TestReleaseDiscoveredFactsSupportEpisodeSpanAndBatchPlans(t *testing.T) {
	size := int64(1024)
	snapshot := releaseSnapshot{
		managedAnimeID: "anime-1",
		releaseID:      "rel-1",
		groupName:      "LoliHouse",
		sourceKind:     "custom_rss",
	}

	// Single-file combined episode (span = 2): both release.episodes and ep.* carry 3-4.
	combinedEvidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), acquisitiontarget.ParserSelectionInput{
		Kind: "regex", MediaKind: "tv", Span: 2,
	}, "[LoliHouse] Show - 02 [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	combinedItem := acquisitiondomain.FeedItem{
		Title: "[LoliHouse] Show - 02 [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{
			SourcePath: "Show - 02.mkv", Filename: "Show - 02.mkv", SizeBytes: &size, MediaKind: "video",
		}},
	}
	combinedPlan, err := acquisitiontarget.BuildBatchPlan(context.Background(), acquisitiontarget.BatchPlanInput{
		Evidence: combinedEvidence, Files: combinedItem.ReleaseFiles, UnclassifiedRoute: batchplan.RouteSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	combinedFacts := releaseDiscoveredFacts(snapshot, combinedItem, "Show", &releaseParserEvidence{ParserEvidence: combinedEvidence}, &combinedPlan)
	if combinedFacts["release.episodes"] != "03-04" || combinedFacts["ep.number"] != "3" || combinedFacts["ep.end"] != "4" || combinedFacts["ep.display"] != "3-4" || combinedFacts["ep.raw"] != "3" {
		t.Fatalf("combined release_discovered facts = %#v", combinedFacts)
	}

	// Multi-file batch with span = 2: release.episodes carries full range 01-04 and ep.* stay empty.
	batchEvidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), acquisitiontarget.ParserSelectionInput{
		Kind: "reltitle", MediaKind: "tv", Span: 2,
	}, "[LoliHouse] Show [01-02 合集][1080p]")
	if err != nil {
		t.Fatal(err)
	}
	batchItem := acquisitiondomain.FeedItem{
		Title: "[LoliHouse] Show [01-02 合集][1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{SourcePath: "Show - 01.mkv", Filename: "Show - 01.mkv", SizeBytes: &size, MediaKind: "video"},
			{SourcePath: "Show - 02.mkv", Filename: "Show - 02.mkv", SizeBytes: &size, MediaKind: "video"},
		},
	}
	batchPlan, err := acquisitiontarget.BuildBatchPlan(context.Background(), acquisitiontarget.BatchPlanInput{
		Evidence: batchEvidence, Files: batchItem.ReleaseFiles, UnclassifiedRoute: batchplan.RouteSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	batchFacts := releaseDiscoveredFacts(snapshot, batchItem, "Show", &releaseParserEvidence{ParserEvidence: batchEvidence}, &batchPlan)
	if batchFacts["release.episodes"] != "01-04" {
		t.Fatalf("batch release.episodes = %#v, want 01-04", batchFacts["release.episodes"])
	}
	for _, key := range []string{"ep.number", "ep.end", "ep.raw", "ep.display"} {
		if batchFacts[key] != nil {
			t.Fatalf("batch %s = %#v, want nil", key, batchFacts[key])
		}
	}
}
