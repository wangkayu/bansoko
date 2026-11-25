package application

import (
	"context"
	"fmt"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/frozenpolicy"
)

func previewBusinessGuardItems(count int) []acquisitiondomain.FeedItem {
	items := make([]acquisitiondomain.FeedItem, 0, count)
	for i := 1; i <= count; i++ {
		items = append(items, acquisitiondomain.FeedItem{
			GUID: fmt.Sprintf("guid-%d", i), Title: fmt.Sprintf("[RealGroup] Preview Show - %02d [1080p]", i),
		})
	}
	return items
}

func evaluateForBusinessGuardTest(t *testing.T, business *animedomain.BusinessSnapshot, items []acquisitiondomain.FeedItem) (previewEvaluation, error) {
	t.Helper()
	result := newPreviewEvaluation()
	err := (&Service{}).evaluateFeedItemsWithSelection(
		context.Background(), &result, 0, string(animedomain.SourceKindProviderGroup), "RealGroup",
		providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "realgroup"},
		previewTestSelection(), previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12"),
		animedomain.MetadataHold{}, "Preview Show", naming.AnimeNamingFacts{Title: "Preview Show"}, items, nil, business,
	)
	return result, err
}

func TestEvaluateFeedItemsWithoutBusinessFailsClosedOnlyWhenThereIsAnItem(t *testing.T) {
	if _, err := evaluateForBusinessGuardTest(t, nil, nil); err != nil {
		t.Fatalf("no items and no business is not an error: %v", err)
	}
	_, err := evaluateForBusinessGuardTest(t, nil, previewBusinessGuardItems(1))
	if err == nil || businessCode(t, err) != "BUSINESS_SNAPSHOT_REQUIRED" {
		t.Fatalf("an item without a business snapshot must fail closed, got %v", err)
	}
}

func TestEvaluateFeedItemsUsesTypedNamingFacts(t *testing.T) {
	business, err := frozenpolicy.Business()
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluateForBusinessGuardTest(t, &business, previewBusinessGuardItems(8))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(result.Releases) != 8 {
		t.Fatalf("every item is still evaluated, got %d releases", len(result.Releases))
	}
	for _, release := range result.Releases {
		if release.NamingAnime.Title != "Preview Show" {
			t.Fatalf("release lost its typed naming facts: %+v", release.NamingAnime)
		}
	}
}
