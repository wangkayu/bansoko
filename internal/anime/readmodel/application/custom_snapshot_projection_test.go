package application

import (
	animedomain "bansoko/internal/anime/domain"
	catalogapplication "bansoko/internal/catalog/application"
	"context"
	"testing"
)

func TestCustomSnapshotProjectionExactScopeAndNoImplicitRead(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	draft.Origin = animedomain.SubscriptionOriginCustom
	draft.RssSources = []animedomain.RSSSourceDraft{{SourceKind: "custom_rss", Locator: "https://example.test/feed", GroupName: "Only Group"}}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	source := &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}
	service := &Service{seedPreviews: source}
	previews, entries, err := service.resolveCustomSnapshotProjection(context.Background(), "candidate#1", draft, effective, nil)
	if err != nil || len(previews) != 0 || len(entries) != 1 || source.calls != 0 {
		t.Fatalf("implicit read %v/%d", err, source.calls)
	}
	entry, group := catalogapplication.CustomSnapshotScope("candidate#1", draft.RssSources[0].Locator, "Only Group")
	ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
	ref.Provider = animedomain.ProviderKind("custom")
	ref.ProviderEntryRef = entry
	ref.ProviderGroupRef = group
	previews, _, err = service.resolveCustomSnapshotProjection(context.Background(), "candidate#1", draft, effective, []PreviewFeedSnapshotRef{ref})
	if err != nil || len(previews) != 1 || source.calls != 1 {
		t.Fatalf("exact read %v/%d", err, source.calls)
	}
	if _, _, err = service.resolveCustomSnapshotProjection(context.Background(), "other-candidate#1", draft, effective, []PreviewFeedSnapshotRef{ref}); err == nil {
		t.Fatal("wrong candidate accepted")
	}
	if source.calls != 1 {
		t.Fatal("wrong candidate reached snapshot reader")
	}
}
