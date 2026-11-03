package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bansoko/internal/filemaintenance"
	filesystem "bansoko/internal/library/adapter/filesystem"
	observabilityport "bansoko/internal/observability/port"
)

// One bounded startup pass over the directories bansoko writes into. Its only
// target is the transient names no durable row owns: an interrupted action can
// leave one behind and nothing else ever revisits it.
const (
	transientResidueMaxRecordedPaths   = 20000
	transientResidueMaxDirectories     = 4000
	transientResidueMaxEntriesPerDir   = 20000
	transientResidueSafetyMargin       = 15 * time.Minute
	transientResidueSweepTimeout       = 5 * time.Minute
	transientResidueSweepComponentName = "filemaintenance"
)

// runTransientResidueSweep removes abandoned copy, placement, publication
// permit, and hardlink probe files from recorded directories. The cutoff is the
// pass's own start minus a safety margin, so a concurrent action's fresh
// temporary is never swept. Failures are counted, not fatal: the pass is
// residue hygiene, never a startup gate.
func runTransientResidueSweep(ctx context.Context, db *sql.DB, logger observabilityport.RuntimeLogger) {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	started := time.Now()
	sweepCtx, cancel := context.WithTimeout(ctx, transientResidueSweepTimeout)
	defer cancel()
	dirs, err := filemaintenance.TransientResidueDirectories(sweepCtx, db, transientResidueMaxRecordedPaths)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "transient residue directories could not be read",
				Component:  transientResidueSweepComponentName,
				Operation:  "transient_residue_sweep",
				Outcome:    "failure",
				ReasonCode: "transient_residue_directories_failed",
				ErrorClass: "filesystem",
			})
		}
		return
	}
	report, err := filesystem.SweepTransientResidue(sweepCtx, dirs, filesystem.TransientResidueSweepOptions{
		ModifiedBefore:         started.Add(-transientResidueSafetyMargin),
		MaxDirectories:         transientResidueMaxDirectories,
		MaxEntriesPerDirectory: transientResidueMaxEntriesPerDir,
	})
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "transient residue sweep did not finish",
				Component:  transientResidueSweepComponentName,
				Operation:  "transient_residue_sweep",
				Outcome:    "failure",
				ReasonCode: "transient_residue_sweep_interrupted",
				ErrorClass: "filesystem",
			})
		}
		return
	}
	if report.Failed > 0 {
		logger.Warn(ctx, observabilityport.RuntimeEvent{
			Message: fmt.Sprintf("transient residue sweep left entries (removed=%d failed=%d directories=%d truncated=%t)",
				report.Removed, report.Failed, report.Directories, report.Truncated),
			Component:  transientResidueSweepComponentName,
			Operation:  "transient_residue_sweep",
			Outcome:    "failure",
			ReasonCode: "transient_residue_sweep_incomplete",
			ErrorClass: "filesystem",
		})
		return
	}
	if report.Removed > 0 || report.Truncated {
		logger.Info(ctx, observabilityport.RuntimeEvent{
			Message: fmt.Sprintf("transient residue sweep removed files (removed=%d directories=%d truncated=%t)",
				report.Removed, report.Directories, report.Truncated),
			Component:  transientResidueSweepComponentName,
			Operation:  "transient_residue_sweep",
			Outcome:    "success",
			ReasonCode: "transient_residue_swept",
		})
	}
}
