package domain

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
)

func TestLogicalGroupIdentityKeepsProviderAndCustomGroupsDistinct(t *testing.T) {
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "42")
	for _, identity := range []LogicalGroupIdentity{
		{Kind: LogicalGroupProvider, ProviderGroup: &ref},
		{Kind: LogicalGroupCustom, CustomGroupName: "My Group"},
	} {
		if err := identity.Validate(); err != nil {
			t.Fatalf("valid logical group rejected: %v", err)
		}
	}
	if err := (LogicalGroupIdentity{Kind: LogicalGroupProvider, ProviderGroup: &ref, CustomGroupName: "fake"}).Validate(); err == nil {
		t.Fatal("mixed provider/custom group identity accepted")
	}
}
