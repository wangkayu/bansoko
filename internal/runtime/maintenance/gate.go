// Package maintenance is the single runtime maintenance/write gate for the
// server. Web HTTP, CLI and MCP product write commands all consult
// this gate; entering maintenance makes every registered write fail closed
// while read-only readiness and recovery diagnostics keep working.
//
// State transitions are idempotent. Exiting maintenance requires the
// automation resume gate to be passed (set by the deployment recovery
// reconcile service); a plain server restart or manual override cannot
// bypass it.
package maintenance

import (
	"errors"
	"sync"
)

// State is the runtime maintenance lifecycle state.
type State string

const (
	// Serving accepts product writes.
	Serving State = "serving"
	// Draining accepts no new product writes; in-flight work is winding down.
	Draining State = "draining"
	// Maintenance rejects all product writes (fail closed).
	Maintenance State = "maintenance"
)

// ErrResumeGateNotPassed is returned by ExitMaintenance while the automation
// resume gate has not been passed.
var ErrResumeGateNotPassed = errors.New("automation resume gate not passed; maintenance cannot exit")

// Status is the machine-readable gate snapshot.
type Status struct {
	State                State `json:"state"`
	AutomationResumeGate bool  `json:"automation_resume_gate"`
	WritesRejected       bool  `json:"writes_rejected"`
}

// Gate is the process-wide maintenance gate.
type Gate struct {
	mu               sync.RWMutex
	state            State
	resumeGate       bool
	maintenanceBlock bool
}

// NewGate returns a gate in the serving state.
func NewGate() *Gate {
	return &Gate{state: Serving}
}

// EnterMaintenance idempotently enters maintenance (fail closed on writes).
func (g *Gate) EnterMaintenance() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = Maintenance
}

// BlockMaintenance enters a process-lifetime fail-closed maintenance state.
// It is used when startup has durable evidence that rollback safety was not
// proven. Unlike ordinary orchestration maintenance, this state cannot be
// exited by a maintenance-exit call or by setting the automation resume
// gate; operator recovery requires a subsequent process start after the
// durable evidence has been repaired.
func (g *Gate) BlockMaintenance() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = Maintenance
	g.resumeGate = false
	g.maintenanceBlock = true
}

// StartDraining idempotently enters the draining state.
func (g *Gate) StartDraining() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maintenanceBlock {
		return
	}
	if g.state == Serving {
		g.state = Draining
	}
}

// SetAutomationResumeGate records the deployment recovery reconcile verdict.
// Exiting maintenance is only allowed after this is passed.
func (g *Gate) SetAutomationResumeGate(passed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maintenanceBlock {
		g.resumeGate = false
		return
	}
	g.resumeGate = passed
}

// ExitMaintenance leaves maintenance. It refuses while the automation resume
// gate has not passed (rollback recovery contract: a recovered instance stays
// in maintenance until the reconcile service proves convergence).
func (g *Gate) ExitMaintenance() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != Maintenance {
		return nil
	}
	if g.maintenanceBlock {
		return ErrMaintenanceBlocked
	}
	if !g.resumeGate {
		return ErrResumeGateNotPassed
	}
	g.state = Serving
	return nil
}

// RejectWrites reports whether product writes must fail closed right now.
// Maintenance always rejects; draining also rejects until the resume gate
// passes (draining is entered during recovery orchestration).
func (g *Gate) RejectWrites() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.state == Maintenance {
		return true
	}
	if g.state == Draining {
		return !g.resumeGate
	}
	return false
}

// Status returns the current gate snapshot.
func (g *Gate) Status() Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return Status{
		State:                g.state,
		AutomationResumeGate: g.resumeGate,
		WritesRejected:       g.state == Maintenance || (g.state == Draining && !g.resumeGate),
	}
}

// Restore applies a previously captured snapshot unconditionally. Its only
// sanctioned caller is the embedded self-update chain: the updater verifies
// "serving" at admission, enters maintenance as part of its own window, and on
// a pre-effect abort must give the serving state back — ExitMaintenance alone
// cannot do that because it is coupled to the durable automation resume gate.
// A snapshot taken while any external maintenance source was active would
// wrongly restore Serving; admission rejects that case before any window opens.
func (g *Gate) Restore(snapshot Status) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maintenanceBlock {
		return
	}
	g.state = snapshot.State
	g.resumeGate = snapshot.AutomationResumeGate
}

// ErrMaintenanceBlocked indicates that startup parked the process after a
// rollback safety failure. It is deliberately stable for the claim adapter.
var ErrMaintenanceBlocked = errors.New("maintenance is permanently blocked pending operator recovery")
