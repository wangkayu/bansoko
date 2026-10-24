package domain

import (
	"testing"
	"time"
)

func TestBuiltInGroupTranslationIsValidatedAndReadOnlyReferenceData(t *testing.T) {
	translation, err := BuiltInGroupTranslation()
	if err != nil {
		t.Fatal(err)
	}
	if translation.SchemaVersion == "" || len(translation.Items) == 0 {
		t.Fatalf("seed translation is empty: %+v", translation)
	}
	for _, item := range translation.Items {
		if len(item.ProviderRefs) == 0 {
			t.Fatalf("translation row has no explicit provider mapping: %+v", item)
		}
	}
}

func TestLocalGroupCatalogViewMergesObservedAndTranslationOnlyGroups(t *testing.T) {
	translation := TranslationVersion{
		SchemaVersion: "schema-v1", DataVersion: "data-v1",
		Items: []Translation{{CanonicalKey: "known", DisplayName: "Known", ProviderRefs: []GroupRef{{Provider: ProviderMikan, ExternalID: "42"}}}},
	}
	observedRef, _ := NewGroupRef(ProviderAniBT, "observed")
	translatedRef, _ := NewGroupRef(ProviderMikan, "42")
	snapshot := GroupSnapshot{
		ID: "snapshot-1", Provider: ProviderAniBT, CatalogEntryRef: "100", ObservedAt: time.Unix(10, 0), SchemaVersion: "groups-v1", Version: 1,
		Groups: []GroupSnapshotEntry{{Ref: observedRef, DisplayName: "Observed", FeedLocator: FeedLocator{Provider: ProviderAniBT, CatalogEntryRef: "100", Group: observedRef}, ObservedAt: time.Unix(10, 0)}},
	}
	view, err := BuildLocalGroupCatalogView(translation, []GroupSnapshot{snapshot}, time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("expected observed plus translation-only rows: %+v", view.Items)
	}
	for _, item := range view.Items {
		switch item.Ref {
		case observedRef:
			if item.Availability != AvailabilityObserved || item.DisplayName != "Observed" {
				t.Fatalf("observed group was not preserved: %+v", item)
			}
		case translatedRef:
			if item.Availability != AvailabilityNotObserved || item.CanonicalKey != "known" {
				t.Fatalf("translation-only group was not marked correctly: %+v", item)
			}
		default:
			t.Fatalf("unexpected catalog item: %+v", item)
		}
	}
}

func TestLocalGroupCatalogViewRejectsMalformedSnapshotInsteadOfPublishingEmptyData(t *testing.T) {
	translation := TranslationVersion{SchemaVersion: "schema-v1", DataVersion: "data-v1"}
	_, err := BuildLocalGroupCatalogView(translation, []GroupSnapshot{{ID: "bad", Provider: ProviderMikan, CatalogEntryRef: "1", Version: 1, SchemaVersion: "groups-v1"}}, time.Now())
	if err == nil {
		t.Fatal("malformed snapshot accepted")
	}
}
