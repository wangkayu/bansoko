package domain

import (
	"testing"
	"time"
)

func TestGroupRefNormalizesProviderSpecificIdentity(t *testing.T) {
	mikan, err := NewGroupRef(ProviderMikan, "00042")
	if err != nil || mikan.ExternalID != "42" || mikan.Key() != "mikan:42" {
		t.Fatalf("unexpected Mikan group ref: %+v err=%v", mikan, err)
	}
	aniBT, err := NewGroupRef(ProviderAniBT, "Lilith-Raws")
	if err != nil || aniBT.Key() != "anibt:Lilith-Raws" {
		t.Fatalf("unexpected AniBT group ref: %+v err=%v", aniBT, err)
	}
	for _, input := range []struct {
		provider Kind
		value    string
	}{
		{ProviderMikan, "0"},
		{ProviderMikan, "group"},
		{ProviderAniBT, "bad slug"},
		{Kind("custom_rss"), "group"},
	} {
		if _, err := NewGroupRef(input.provider, input.value); err == nil {
			t.Fatalf("invalid provider group accepted: %+v", input)
		}
	}
}

func TestSnapshotRejectsCrossProviderAndDuplicateGroups(t *testing.T) {
	ref, _ := NewGroupRef(ProviderAniBT, "group-a")
	entry := GroupSnapshotEntry{Ref: ref, FeedLocator: FeedLocator{Provider: ProviderAniBT, CatalogEntryRef: "123", Group: ref}, ObservedAt: time.Now()}
	snapshot := GroupSnapshot{ID: "snapshot", Provider: ProviderAniBT, CatalogEntryRef: "123", ObservedAt: time.Now(), SchemaVersion: "provider-groups-v1", Version: 1, Groups: []GroupSnapshotEntry{entry}}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	snapshot.Groups = append(snapshot.Groups, entry)
	if err := snapshot.Validate(); err == nil {
		t.Fatal("duplicate group snapshot entry accepted")
	}
}
