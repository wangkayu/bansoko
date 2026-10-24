package application

import (
	providerdomain "bansoko/internal/provider/domain"
	"testing"
)

func TestDiscoveryRejectsWrongEntryAndDuplicateStableIDs(t *testing.T) {
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "123")
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderMikan, CatalogEntryRef: "entry", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "Name"}}}
	if err := validateDiscoveryIdentity(result, "mikan", "entry"); err != nil {
		t.Fatal(err)
	}
	if err := validateDiscoveryIdentity(result, "mikan", "wrong"); err == nil {
		t.Fatal("wrong entry accepted")
	}
	result.Groups = append(result.Groups, providerdomain.GroupSnapshotEntry{Ref: ref, DisplayName: "Different name"})
	if err := validateDiscoveryIdentity(result, "mikan", "entry"); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}
