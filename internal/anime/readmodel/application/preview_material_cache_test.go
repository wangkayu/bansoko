package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	providerdomain "bansoko/internal/provider/domain"
)

// The attr rules and the Vars program a preview evaluates with are immutable
// and addressed by their digest, so one evaluation must not re-read them for
// every release: with N seeds that was 2N database round trips and N program
// compilations before any real work. These tests pin that they are loaded once
// for a call (not once per item) and served from a bounded cache afterwards.

type materialCounters struct {
	attrRules, programs int
	attrErr             error
}

// previewMaterialFixture returns a service whose loaders are counted and a
// business snapshot whose program refs are internally consistent.
func previewMaterialFixture(t *testing.T) (*Service, *materialCounters, *animedomain.BusinessSnapshot) {
	t.Helper()
	source := `vars.anime = {ep: extract(input.release_title, /(\d+)/) ?? ""};`
	canonical, err := namingvars.CanonicalSource(source)
	if err != nil {
		t.Fatal(err)
	}
	varsDigest, err := namingvars.SourceFingerprint(string(canonical))
	if err != nil {
		t.Fatal(err)
	}
	varsRef := namingvars.ProgramRef{Version: namingvars.ProfileVersion(varsDigest), Digest: varsDigest}
	program, err := namingvars.NewProgram(varsRef, source)
	if err != nil {
		t.Fatal(err)
	}
	attrDigest := "sha256:" + fmt.Sprintf("%064x", 7)
	attrRef := namingvars.ProgramRef{Version: attrrules.ProfileVersion(attrDigest), Digest: attrDigest}

	counters := &materialCounters{}
	service := &Service{
		loadAttrRulesSource: func(context.Context, namingvars.ProgramRef) (attrrules.Rules, error) {
			counters.attrRules++
			return attrrules.Rules{}, counters.attrErr
		},
		loadVarsProgramSource: func(context.Context, namingvars.ProgramRef) (namingvars.Program, error) {
			counters.programs++
			return program, nil
		},
	}
	business := &animedomain.BusinessSnapshot{Programs: animedomain.ProgramReferences{AttrRules: attrRef, Vars: varsRef}}
	return service, counters, business
}

func previewMaterialItems(count int) []acquisitiondomain.FeedItem {
	items := make([]acquisitiondomain.FeedItem, 0, count)
	for i := 1; i <= count; i++ {
		items = append(items, acquisitiondomain.FeedItem{
			GUID: fmt.Sprintf("guid-%d", i), Title: fmt.Sprintf("[RealGroup] Preview Show - %02d [1080p]", i),
		})
	}
	return items
}

func evaluateForMaterialTest(t *testing.T, service *Service, business *animedomain.BusinessSnapshot, items []acquisitiondomain.FeedItem) (previewEvaluation, error) {
	t.Helper()
	result := newPreviewEvaluation()
	err := service.evaluateFeedItemsWithSelection(
		context.Background(), &result, 0, string(animedomain.SourceKindProviderGroup), "RealGroup",
		providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "realgroup"},
		previewTestSelection(), previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12"),
		animedomain.MetadataHold{}, "Preview Show", namingvars.AnimeEvidence{AnimeTitle: "Preview Show"}, items, nil, business,
	)
	return result, err
}

func TestEvaluateFeedItemsLoadsPreviewMaterialsOnceForManyItems(t *testing.T) {
	service, counters, business := previewMaterialFixture(t)
	result, err := evaluateForMaterialTest(t, service, business, previewMaterialItems(8))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(result.Releases) != 8 {
		t.Fatalf("every item is still evaluated, got %d releases", len(result.Releases))
	}
	if counters.attrRules != 1 || counters.programs != 1 {
		t.Fatalf("attr rules and the Vars program must each load once for 8 items, got %d and %d", counters.attrRules, counters.programs)
	}
	for _, release := range result.Releases {
		if release.FrozenVars == nil || release.FrozenVars.Status != "ok" {
			t.Fatalf("each release still runs the Vars program: %+v", release.FrozenVars)
		}
	}
}

// A second preview of the same configuration is the common case (edit, click
// preview again); the immutable materials must not be read a second time.
func TestPreviewMaterialsAreServedFromTheCacheAcrossCalls(t *testing.T) {
	service, counters, business := previewMaterialFixture(t)
	for call := 0; call < 3; call++ {
		if _, err := evaluateForMaterialTest(t, service, business, previewMaterialItems(3)); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}
	if counters.attrRules != 1 || counters.programs != 1 {
		t.Fatalf("three previews of one digest load the materials once, got %d and %d", counters.attrRules, counters.programs)
	}
}

// Loading stays lazy: a group with no seeds never needed the business snapshot.
func TestEvaluateFeedItemsWithNoItemsLoadsNothing(t *testing.T) {
	service, counters, business := previewMaterialFixture(t)
	if _, err := evaluateForMaterialTest(t, service, business, nil); err != nil {
		t.Fatalf("an empty group is not an error: %v", err)
	}
	if counters.attrRules != 0 || counters.programs != 0 {
		t.Fatalf("no items, no loads; got %d and %d", counters.attrRules, counters.programs)
	}
}

func TestEvaluateFeedItemsWithoutBusinessFailsClosedOnlyWhenThereIsAnItem(t *testing.T) {
	service, _, _ := previewMaterialFixture(t)
	if _, err := evaluateForMaterialTest(t, service, nil, nil); err != nil {
		t.Fatalf("no items and no business is not an error: %v", err)
	}
	_, err := evaluateForMaterialTest(t, service, nil, previewMaterialItems(1))
	if err == nil || businessCode(t, err) != "BUSINESS_SNAPSHOT_REQUIRED" {
		t.Fatalf("an item without a business snapshot must fail closed, got %v", err)
	}
}

// A failed load is not remembered: the next preview retries instead of serving
// a poisoned entry.
func TestPreviewMaterialLoadFailureIsNotCached(t *testing.T) {
	service, counters, business := previewMaterialFixture(t)
	counters.attrErr = errors.New("source unavailable")
	if _, err := evaluateForMaterialTest(t, service, business, previewMaterialItems(2)); err == nil {
		t.Fatal("a failed attr rules load must surface")
	}
	counters.attrErr = nil
	if _, err := evaluateForMaterialTest(t, service, business, previewMaterialItems(2)); err != nil {
		t.Fatalf("the retry must reload and succeed: %v", err)
	}
	if counters.attrRules != 2 {
		t.Fatalf("the failed load must not have been cached, loads = %d", counters.attrRules)
	}
}

func TestPreviewMaterialCacheIsBounded(t *testing.T) {
	var cache previewMaterialCache
	load := func(_ context.Context, ref namingvars.ProgramRef) (attrrules.Rules, error) {
		return attrrules.Rules{}, nil
	}
	for i := 0; i < previewMaterialCacheLimit*3; i++ {
		ref := namingvars.ProgramRef{Version: fmt.Sprintf("v%d", i), Digest: fmt.Sprintf("d%d", i)}
		if _, err := cache.attrRulesFor(context.Background(), ref, load); err != nil {
			t.Fatal(err)
		}
	}
	if size := cache.size(); size > previewMaterialCacheLimit {
		t.Fatalf("cache grew to %d entries, limit %d", size, previewMaterialCacheLimit)
	}
}
