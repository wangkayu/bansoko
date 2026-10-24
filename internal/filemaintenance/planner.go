package filemaintenance

import filemaintenanceplanner "bansoko/internal/filemaintenance/planner"

// SnapshotObservation aliases the planner-owned immutable observation value.
type SnapshotObservation = filemaintenanceplanner.SnapshotObservation

// SnapshotReader aliases the planner-owned snapshot contract port.
type SnapshotReader = filemaintenanceplanner.SnapshotReader

// PlanCandidate aliases the planner-owned dry-run candidate value.
type PlanCandidate = filemaintenanceplanner.PlanCandidate

// PlanResult aliases the planner-owned deterministic dry-run result.
type PlanResult = filemaintenanceplanner.PlanResult

// Planner is kept as a source-compatible alias while the planner owner lives
// in its dedicated subpackage.
type Planner = filemaintenanceplanner.Planner

// TargetRenderer aliases the planner-owned pure rendering port.
type TargetRenderer = filemaintenanceplanner.TargetRenderer

// NewPlanner is the compatibility constructor for the dedicated planner owner.
func NewPlanner(snapshots SnapshotReader, renderer TargetRenderer) *Planner {
	return filemaintenanceplanner.NewPlanner(snapshots, renderer)
}
