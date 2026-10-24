package maintenance

import (
	"errors"
	"sync"
	"testing"
)

func TestInitialStateServesWrites(t *testing.T) {
	g := NewGate()
	if g.RejectWrites() {
		t.Fatal("fresh gate must accept writes")
	}
	s := g.Status()
	if s.State != Serving || s.WritesRejected {
		t.Fatalf("unexpected fresh status: %+v", s)
	}
}

func TestEnterMaintenanceRejectsWritesIdempotently(t *testing.T) {
	g := NewGate()
	g.EnterMaintenance()
	g.EnterMaintenance() // idempotent
	if !g.RejectWrites() {
		t.Fatal("maintenance must reject writes")
	}
	if g.Status().State != Maintenance {
		t.Fatal("state must be maintenance")
	}
}

func TestExitMaintenanceRequiresResumeGate(t *testing.T) {
	g := NewGate()
	g.EnterMaintenance()
	if err := g.ExitMaintenance(); !errors.Is(err, ErrResumeGateNotPassed) {
		t.Fatalf("exit without resume gate must fail, got %v", err)
	}
	if !g.RejectWrites() {
		t.Fatal("still must reject writes after refused exit")
	}
	g.SetAutomationResumeGate(true)
	if err := g.ExitMaintenance(); err != nil {
		t.Fatalf("exit after resume gate must succeed: %v", err)
	}
	if g.RejectWrites() {
		t.Fatal("serving must accept writes")
	}
}

func TestDrainingRejectsUntilResumeGate(t *testing.T) {
	g := NewGate()
	g.StartDraining()
	if !g.RejectWrites() {
		t.Fatal("draining without resume gate must reject writes")
	}
	g.SetAutomationResumeGate(true)
	if g.RejectWrites() {
		t.Fatal("draining with resume gate passed must accept writes")
	}
}

func TestConcurrentAccess(t *testing.T) {
	g := NewGate()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Status()
			g.RejectWrites()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.EnterMaintenance()
		g.SetAutomationResumeGate(true)
		_ = g.ExitMaintenance()
	}()
	wg.Wait()
}

func TestRestoreReturnsServingAfterUpdaterWindow(t *testing.T) {
	gate := NewGate()
	snapshot := gate.Status()

	// The embedded updater opens its window only from serving (admission
	// verifies it), then restores that exact snapshot on a pre-effect abort.
	if snapshot.State != Serving || snapshot.WritesRejected {
		t.Fatalf("precondition violated: snapshot captured outside serving: %+v", snapshot)
	}
	gate.EnterMaintenance()
	if !gate.RejectWrites() {
		t.Fatalf("maintenance must reject writes")
	}
	gate.Restore(snapshot)
	if gate.RejectWrites() {
		t.Fatalf("restore did not return the instance to serving")
	}
	if status := gate.Status(); status.State != Serving || status.AutomationResumeGate {
		t.Fatalf("unexpected restored state %+v", status)
	}
}

func TestBlockedMaintenanceCannotBeExitedOrRestored(t *testing.T) {
	gate := NewGate()
	gate.BlockMaintenance()
	gate.SetAutomationResumeGate(true)
	if err := gate.ExitMaintenance(); !errors.Is(err, ErrMaintenanceBlocked) {
		t.Fatalf("blocked exit = %v, want ErrMaintenanceBlocked", err)
	}
	gate.Restore(Status{State: Serving, AutomationResumeGate: true})
	status := gate.Status()
	if status.State != Maintenance || !status.WritesRejected || status.AutomationResumeGate {
		t.Fatalf("blocked gate was restored: %+v", status)
	}
}
