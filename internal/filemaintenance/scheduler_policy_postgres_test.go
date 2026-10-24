package filemaintenance

import (
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"testing"
)

func TestPGSchedulerUsesEffectivePolicyDisabledAndNullInterval(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)
	interval := 3600
	for _, id := range []string{"disabled", "inherit"} {
		seedSchedulerInstance(t, db, id, "active")
		// Both works placed files, so only the effective policy decides who is
		// due: a missing scan scope is never the reason.
		seedSchedulerPlacement(t, db, id, "plc-"+id, "/media/"+id+"/"+id+" - E01.mkv")
	}
	if _, err := policies.SavePolicy(ctx, GlobalScope, 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"disabled", "inherit"} {
		if _, err := policies.SavePolicy(ctx, "anime:"+id, 0, PolicyRow{Enabled: id == "inherit"}, "owner", "test"); err != nil {
			t.Fatal(err)
		}
	}
	submit := &fakeSubmitter{}
	scheduler := NewScanScheduler(db, policies, submit, &fakeAnimeLister{ids: []string{"disabled", "inherit"}}, observabilityport.NopRuntimeLogger{})
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if submit.calls["disabled"] != 0 || submit.calls["inherit"] != 1 {
		t.Fatalf("effective resolver mismatch: %+v", submit.calls)
	}
}
