package filemaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

func openPolicyTestDB(t *testing.T) *policyTestDB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return &policyTestDB{db: db, svc: NewPolicyService(db)}
}

type policyTestDB struct {
	db  interface{ Close() error }
	svc *PolicyService
}

func (p *policyTestDB) Close() { _ = p.db.Close() }

func TestSavePolicyCASConflict(t *testing.T) {
	fixture := openPolicyTestDB(t)
	defer fixture.Close()
	ctx := context.Background()

	global := PolicyRow{Enabled: true, Safety: json.RawMessage(`{"max_operations":100}`)}
	saved, err := fixture.svc.SavePolicy(ctx, GlobalScope, 0, global, "owner", "web")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 {
		t.Fatalf("expected version 1, got %d", saved.Version)
	}
	// Stale expected version must conflict explicitly.
	if _, err := fixture.svc.SavePolicy(ctx, GlobalScope, 0, global, "owner", "web"); !errors.Is(err, ErrPolicyConflict) {
		t.Fatalf("stale CAS must conflict, got %v", err)
	}
	// Correct version succeeds.
	updated, err := fixture.svc.SavePolicy(ctx, GlobalScope, 1, PolicyRow{Enabled: false, Safety: global.Safety}, "owner", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Version)
	}
}

func TestEffectivePolicyMergesOverride(t *testing.T) {
	fixture := openPolicyTestDB(t)
	defer fixture.Close()
	ctx := context.Background()

	global := PolicyRow{Enabled: true, Safety: json.RawMessage(`{"max_operations":100}`)}
	if _, err := fixture.svc.SavePolicy(ctx, GlobalScope, 0, global, "owner", "web"); err != nil {
		t.Fatal(err)
	}
	interval := 3600
	override := PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: global.Safety}
	if _, err := fixture.svc.SavePolicy(ctx, "anime:anime-1", 0, override, "owner", "web"); err != nil {
		t.Fatal(err)
	}
	effective, err := fixture.svc.GetEffective(ctx, "anime-1")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Source != "override" || effective.ScanIntervalSeconds == nil || *effective.ScanIntervalSeconds != 3600 {
		t.Fatalf("override must replace the owned field: %+v", effective)
	}
	// Anime without override inherits global.
	noOverride, err := fixture.svc.GetEffective(ctx, "anime-2")
	if err != nil {
		t.Fatal(err)
	}
	if noOverride.Source != "global" || !noOverride.Enabled {
		t.Fatalf("global must be inherited: %+v", noOverride)
	}
}
