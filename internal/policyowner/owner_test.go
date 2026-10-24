package policyowner_test

import (
	"errors"
	"testing"
	"time"

	"bansoko/internal/policyowner"
)

func TestCapabilityValidatesTypedValue(t *testing.T) {
	want := errors.New("invalid")
	capability := policyowner.Capability[string]{
		Metadata: policyowner.Metadata{Kind: "example"},
		Validate: func(string) error { return want },
	}
	if err := capability.ValidateValue("value"); !errors.Is(err, want) {
		t.Fatalf("validation error = %v, want %v", err, want)
	}
	if capability.Metadata.Kind != "example" {
		t.Fatalf("metadata kind = %q", capability.Metadata.Kind)
	}
}

func TestPolicyRowsPreserveVersionMetadata(t *testing.T) {
	now := time.Now()
	defaultRow := policyowner.Default[string]{Policy: "default", Version: 2, UpdatedAt: now}
	instanceRow := policyowner.Instance[string]{ManagedAnimeID: "anime-1", Policy: "instance", SourceDefaultVersion: 2, Version: 3, UpdatedAt: now}
	if defaultRow.Version != 2 || !defaultRow.UpdatedAt.Equal(now) {
		t.Fatalf("default row = %+v", defaultRow)
	}
	if instanceRow.ManagedAnimeID != "anime-1" || instanceRow.SourceDefaultVersion != 2 || instanceRow.Version != 3 {
		t.Fatalf("instance row = %+v", instanceRow)
	}
}

func TestLifecycleMutationCarriesCASAndAuditContext(t *testing.T) {
	defaultSave := policyowner.DefaultSave[string]{ExpectedVersion: 4, Value: "next", Actor: "web", Channel: "web"}
	instanceSave := policyowner.InstanceSave[string]{ManagedAnimeID: "anime-1", ExpectedVersion: 7, Value: "frozen", Actor: "mcp", Channel: "mcp"}
	if defaultSave.ExpectedVersion != 4 || defaultSave.Value != "next" || defaultSave.Actor != "web" || defaultSave.Channel != "web" {
		t.Fatalf("default mutation = %+v", defaultSave)
	}
	if instanceSave.ManagedAnimeID != "anime-1" || instanceSave.ExpectedVersion != 7 || instanceSave.Value != "frozen" || instanceSave.Actor != "mcp" || instanceSave.Channel != "mcp" {
		t.Fatalf("instance mutation = %+v", instanceSave)
	}
}
